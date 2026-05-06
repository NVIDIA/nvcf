/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nvca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/sirupsen/logrus"
	"github.com/sourcegraph/conc/pool"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/logging"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/hostisolation"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// Now that dequeue calls are parallelized, it makes sense to recheck the queues
	// at least as often as the calls take via waiting for messages.
	defaultSyncQueueInterval = maximumWaitTimeSeconds * time.Second

	maximumWaitTimeSeconds                = 3
	maxCreationSkip                       = uint64(50)
	defaultVisibilityTimeoutSeconds       = int64(30)
	creationQueueVisibilityTimeoutSeconds = int64(360)
)

type QueueManager struct {
	// Mutex protects queue fields
	qmu    sync.RWMutex
	qcreds types.QueueCredentials
	// Tracks the starting index for each GPU's queue ring for round-robin dequeues.
	queueRingStartIdxByGPU map[types.GPUName]int

	// maxQueueMessages represents the maximum number of messages we will
	// pull from the queue per batch
	maxQueueMessages int64

	Client     queue.Client
	Backendk8s *BackendK8sCache

	// when non-zero, skips processing creation messages temporarily
	// its local random backoff
	numSkipProcessCreation    map[types.GPUName]uint64
	numSkipProcessCreationMtx sync.Mutex

	// If certain status components are unavailable,
	// the queue manager will not pop messages off the queue.
	statusGetter health.StatusGetter
	// health status flag for liveness probe verification
	healthy atomic.Bool
	// paused indicates if the queue manager is paused (e.g., due to no GPUs available).
	// When paused, creation messages are not processed but termination messages continue.
	paused atomic.Bool

	// gpuAtCapacityStore keeps track if a particular GPU is at capacity or not
	gpuAtCapacityStore *sync.Map

	// maintenanceMode indicates if NVCA is in maintenance mode
	maintenanceMode types.MaintenanceMode

	// metrics for recording queue operations
	metrics *nvcametrics.Metrics
}

func getQueueManagerMaxQueueMessages(fff featureflag.FeatureFlagFetcher) int64 {
	if fff.IsFeatureFlagEnabled(featureflag.MaxSQSBatchPull) {
		return 10
	} else if v, ok := os.LookupEnv("NVCA_MAX_SQS_BATCH_PULL"); ok {
		if sqsBatchSize, err := strconv.Atoi(v); err == nil {
			return int64(sqsBatchSize)
		}
	}
	return 1
}

func NewQueueManager(
	bk8s *BackendK8sCache,
	bsc health.StatusGetter,
	qc queue.Client,
	qcreds types.QueueCredentials,
	fff featureflag.FeatureFlagFetcher,
	maintenanceMode types.MaintenanceMode,
	metrics *nvcametrics.Metrics,
) *QueueManager {
	if qcreds.CreationQueues == nil {
		panic("creation queues are empty")
	}
	qm := &QueueManager{
		Client:                 qc,
		Backendk8s:             bk8s,
		qcreds:                 qcreds,
		queueRingStartIdxByGPU: map[types.GPUName]int{},
		maxQueueMessages:       getQueueManagerMaxQueueMessages(fff),
		numSkipProcessCreation: map[types.GPUName]uint64{},
		statusGetter:           bsc,
		gpuAtCapacityStore:     &sync.Map{},
		maintenanceMode:        maintenanceMode,
		metrics:                metrics,
	}
	// Set initial status to OK
	qm.SetStatusOK(true)
	return qm
}

// updateQueues must be used to update the queue manager's queues.
func (qm *QueueManager) updateQueues(queueCreds types.QueueCredentials) {
	qm.qmu.Lock()
	defer qm.qmu.Unlock()
	qm.qcreds = queueCreds
}

func (qm *QueueManager) getCreateQueue(gpuName types.GPUName) queue.MessageQueueInfo {
	qm.qmu.RLock()
	defer qm.qmu.RUnlock()
	return qm.qcreds.CreationQueues[gpuName]
}

func (qm *QueueManager) getCreationQueueInfo(queueURL string) queue.MessageQueueInfo {
	qm.qmu.RLock()
	defer qm.qmu.RUnlock()
	for _, q := range qm.qcreds.CreationQueues {
		if q.QueueURL == queueURL {
			return q
		}
	}
	for _, q := range qm.qcreds.ClusterCreationQueues {
		if q.QueueURL == queueURL {
			return q
		}
	}
	for _, q := range qm.qcreds.TaskClusterCreationQueues {
		if q.QueueURL == queueURL {
			return q
		}
	}
	return queue.MessageQueueInfo{}
}

func (qm *QueueManager) getTermQueue() queue.MessageQueueInfo {
	qm.qmu.RLock()
	defer qm.qmu.RUnlock()
	return qm.qcreds.TerminationQueue
}

type queueWorkInput struct {
	gpuName   types.GPUName
	queueType string
	qi        queue.MessageQueueInfo
	vtoSec    int64
}

type queueWorkOutput struct {
	messages []queue.ReceiveMessageOutput
	err      error
}

type queueWorkOutputs []queueWorkOutput

type queueRingMetadata struct {
	startIdx int
	ringSize int
}

func (qm *QueueManager) collectAvailableCreationQueues(ctx context.Context) ([]queueWorkInput, map[types.GPUName]queueRingMetadata) {
	getQueues := func(ctx context.Context, queueType string, qis types.CreationQueueInfoSet) (qwis []queueWorkInput) {
		for gpuName, qi := range qis {
			if qm.mustSkipQueueForGPU(ctx, gpuName) {
				continue
			}
			qwis = append(qwis, queueWorkInput{
				gpuName:   gpuName,
				queueType: queueType,
				qi:        qi,
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			})
		}
		return qwis
	}

	// Copy queues to contest mutex minimally.
	qwInputs := []queueWorkInput{}
	qm.qmu.Lock()
	qwInputs = append(qwInputs, getQueues(ctx, "createQueue", qm.qcreds.CreationQueues)...)
	qwInputs = append(qwInputs, getQueues(ctx, "clusterCreateQueue", qm.qcreds.ClusterCreationQueues)...)
	qwInputs = append(qwInputs, getQueues(ctx, "taskClusterCreateQueue", qm.qcreds.TaskClusterCreationQueues)...)
	currQueueRingMetadata := map[types.GPUName]queueRingMetadata{}
	for _, qwi := range qwInputs {
		if _, ok := currQueueRingMetadata[qwi.gpuName]; !ok {
			currQueueRingMetadata[qwi.gpuName] = queueRingMetadata{
				ringSize: 1,
				startIdx: qm.queueRingStartIdxByGPU[qwi.gpuName],
			}
		} else {
			qmeta := currQueueRingMetadata[qwi.gpuName]
			qmeta.ringSize++
			currQueueRingMetadata[qwi.gpuName] = qmeta
		}
	}
	for gpuName, qmeta := range currQueueRingMetadata {
		qmeta.startIdx = qmeta.startIdx % qmeta.ringSize
		currQueueRingMetadata[gpuName] = qmeta
		qm.queueRingStartIdxByGPU[gpuName] = (qmeta.startIdx + 1) % qmeta.ringSize
	}
	if len(currQueueRingMetadata) != len(qm.queueRingStartIdxByGPU) {
		for gpuName := range qm.queueRingStartIdxByGPU {
			if _, ok := currQueueRingMetadata[gpuName]; !ok {
				delete(qm.queueRingStartIdxByGPU, gpuName)
			}
		}
	}
	qm.qmu.Unlock()
	return qwInputs, currQueueRingMetadata
}

type createMessageTuple struct {
	gpuName   types.GPUName
	message   queue.ReceiveMessageOutput
	queueInfo queue.MessageQueueInfo
}

func (qm *QueueManager) SyncQueues(ctx context.Context) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc": "QueueManager.SyncQueues",
	})

	// Always freshen health status to detect capacity/component issues.
	if ah, err := qm.statusGetter.RefreshStatus(ctx); err != nil {
		log.WithError(err).Error("Failed to refresh agent health status")
		return err
	} else {
		log.Debugf("Current health: %#v", ah)
	}

	// Skip creation message processing if paused (e.g., no GPUs available)
	if qm.IsPaused() {
		log.Debug("Queue manager is paused - skipping creation message processing, continuing with terminations")

		// emit WorkloadPausedMetric for monitoring
		metrics := nvcametrics.FromContext(ctx)
		metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventWorkloadPaused)...).Inc()

		// Only process termination messages when paused
		termQueue := qm.getTermQueue()

		// Skip termination processing if we don't have valid credentials yet
		// (e.g., GracefulNoGPU is enabled and we're waiting for GPUs to register with ICMS)
		if termQueue.QueueURL == "" {
			log.Debug("Queue manager paused with no credentials - waiting for GPUs to become available before processing queues")
			return nil
		}

		termQWOutput := qm.tryPopMessage(ctx, queueWorkInput{
			qi:        termQueue,
			queueType: "termQueue",
			vtoSec:    defaultVisibilityTimeoutSeconds,
		})

		// Process termination messages
		if termQWOutput.err != nil {
			log.WithError(termQWOutput.err).Error("Failed to fetch termination messages while paused")
			return termQWOutput.err
		}

		// List all ICMS requests to check for existing messages
		existingSRs, err := qm.Backendk8s.icmsRequestLister.List(labels.Everything())
		if err != nil {
			return fmt.Errorf("failed to list ICMS requests for existence detection while paused: %w", err)
		}
		existingMsgIDs := sets.Set[string]{}
		for _, sr := range existingSRs {
			if sr.Spec.Action != common.TerminationAction {
				continue
			}
			existingMsgIDs.Insert(sr.Labels[SQSMessageIDKey])
		}

		// Process termination messages
		for _, message := range termQWOutput.messages {
			if existingMsgIDs.Has(message.MessageID) {
				log.Debugf("Termination ICMS request for %s already in progress while paused, skipping", message.MessageID)
				continue
			}
			if message.Body == nil {
				log.Debugf("Termination ICMS request for %s has an empty body while paused, skipping", message.MessageID)
				continue
			}
			if err := qm.doTerminationMessage(ctx, termQueue, message); err != nil {
				log.WithError(err).Error("Failed to handle message from termination queue while paused")
			}
		}
		return nil
	}

	// Skip creation message processing if in maintenance mode and process only Termination messages
	if qm.maintenanceMode == types.MaintenanceModeCordon || qm.maintenanceMode == types.MaintenanceModeCordonAndDrain {
		if qm.maintenanceMode == types.MaintenanceModeCordon {
			log.Debugf("NVCA in %s maintenance mode - skipping creation message processing, continuing with terminations and heartbeats", qm.maintenanceMode)
		} else {
			log.Debugf("NVCA in %s maintenance mode - skipping creation message processing, draining workloads, continuing with terminations and heartbeats", qm.maintenanceMode)
		}

		// emit WorkloadPausedMetric for monitoring
		metrics := nvcametrics.FromContext(ctx)
		metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventWorkloadPaused)...).Inc()

		// Only process termination messages in maintenance mode
		termQueue := qm.getTermQueue()
		termQWOutput := qm.tryPopMessage(ctx, queueWorkInput{
			qi:        termQueue,
			queueType: "termQueue",
			vtoSec:    defaultVisibilityTimeoutSeconds,
		})

		// Process termination messages
		if termQWOutput.err != nil {
			log.WithError(termQWOutput.err).Error("Failed to fetch termination messages")
			qm.SetStatusOK(false)
			return termQWOutput.err
		}

		// List all ICMS requests to check for existing messages
		existingSRs, err := qm.Backendk8s.icmsRequestLister.List(labels.Everything())
		if err != nil {
			return fmt.Errorf("failed to list ICMS requests for existence detection: %w", err)
		}
		existingMsgIDs := sets.Set[string]{}
		for _, sr := range existingSRs {
			// skip if the action is not Terminate
			if sr.Spec.Action != common.TerminationAction {
				continue
			}
			existingMsgIDs.Insert(sr.Labels[SQSMessageIDKey])
		}

		// Process termination messages
		for _, message := range termQWOutput.messages {
			if existingMsgIDs.Has(message.MessageID) {
				log.Debugf("Termination ICMS request for %s already in progress, skipping", message.MessageID)
				continue
			}
			if message.Body == nil {
				log.Debugf("Termination ICMS request for %s has an empty body, skipping", message.MessageID)
				continue
			}
			if err := qm.doTerminationMessage(ctx, termQueue, message); err != nil {
				log.WithError(err).Error("Failed to handle message from termination queue")
			}
		}

		qm.SetStatusOK(true)
		return nil
	}

	// Copy queues to contest mutex minimally.
	qwInputs, currQueueRingMetadata := qm.collectAvailableCreationQueues(ctx)
	qwInputsSize := len(qwInputs)
	termQueue := qm.getTermQueue()

	var termQWOutput queueWorkOutput
	var wg sync.WaitGroup
	// Add an extra for term queue goroutine.
	wg.Add(qwInputsSize + 1)
	// Start termination queue fetch.
	go func(qwInput queueWorkInput) {
		defer wg.Done()
		termQWOutput = qm.tryPopMessage(ctx, qwInput)
	}(queueWorkInput{
		qi:        termQueue,
		queueType: "termQueue",
		vtoSec:    defaultVisibilityTimeoutSeconds,
	})
	// Start creation queue fetches.
	qwOutputs := make(queueWorkOutputs, qwInputsSize)
	for workIdx, qwInput := range qwInputs {
		go func(workIdx int, qwInput queueWorkInput) {
			defer wg.Done()
			qwOutputs[workIdx] = qm.tryPopMessage(ctx, qwInput)
		}(workIdx, qwInput)
	}
	wg.Wait()

	// List all ICMS requests once to check for new message existence
	// since it could become a bottleneck when large numbers of messages are in the queue.
	existingSRs, err := qm.Backendk8s.icmsRequestLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list ICMS requests for existence detection: %w", err)
	}
	existingMsgIDs := sets.Set[string]{}
	for _, sr := range existingSRs {
		existingMsgIDs.Insert(sr.Labels[SQSMessageIDKey])
	}

	// Track if there is any error at all
	anyQueuePullError := false
	// Hydrate with the new request message terminate IDs and filter to only those
	// that are not already created
	var filteredTermMessages []queue.ReceiveMessageOutput
	if termQWOutput.err != nil {
		anyQueuePullError = true
	} else {
		for _, message := range termQWOutput.messages {
			if existingMsgIDs.Has(message.MessageID) {
				log.Debugf("Termination ICMS request for %s already in progress, skipping", message.MessageID)
				continue
			}
			existingMsgIDs.Insert(message.MessageID)
			if message.Body == nil {
				log.Debugf("Termination ICMS request for %s has an empty body, skipping", message.MessageID)
				continue
			}
			filteredTermMessages = append(filteredTermMessages, message)
		}
	}

	// Hydrate the creation messages with the new request message IDs in matrics
	// based on round-robin starting index.
	creationMessagesByGPU, anyQueuePullError := createCreationMessageMatricesByGPU(
		log, qwInputs, qwOutputs, currQueueRingMetadata, existingMsgIDs,
	)

	// Create worker pool to the number of termination messages plus the unique number of GPUs
	// Termination requests can act in parallel while creation requests must be locked to a single GPU type per goroutine
	maxGoRoutines := len(filteredTermMessages) + len(creationMessagesByGPU)
	if maxGoRoutines < 1 {
		maxGoRoutines = 1
	}
	p := pool.New().WithMaxGoroutines(maxGoRoutines)

	// Even if the instance's pods are looked up to subtract its resources from the cumulative GPU
	// request, there may be a long grace period before a pod's containers release the GPU resource,
	// so do not attempt that here and consider those GPUs still in use.
	for _, message := range filteredTermMessages {
		p.Go(func() {
			if err := qm.doTerminationMessage(ctx, termQueue, message); err != nil {
				log.WithError(err).Error("Failed to handle message from termination queue")
			}
		})
	}

	// Apply creation messages in order, in parallel but lock to ensure GPU calculationss
	// are done synchronously per GPU
	maxMessages := int(qm.maxQueueMessages)
	for _, messageMatrix := range creationMessagesByGPU {
		p.Go(func() {
			iter := newIterCreationMessageMatrix(maxMessages, messageMatrix)
			// Since the new ICMS request may not appear in the informer cache to check for allocatable GPUs
			// by the time the next queue message's ICMS request is created,
			// calculated GPU usage may not return an accurate picture of cluster GPU usage.
			// Instead cumulative new GPU usage can be tracked in-flight.
			var cumGPUReq uint64 = 0
			iter(func(cm createMessageTuple) bool {
				gpuName := cm.gpuName
				usedGPUs, err := qm.doCreationMessage(ctx, gpuName, cumGPUReq, cm.queueInfo, cm.message)
				if err != nil {
					log.WithError(err).Error("Failed to handle message from creation queue")
				} else {
					// Accumulate number of Used GPUs per batch of requests
					cumGPUReq += usedGPUs
				}
				return true
			})
		})
	}

	// Wait for all work to finish
	p.Wait()

	qm.SetStatusOK(!anyQueuePullError)
	return nil
}

func createCreationMessageMatricesByGPU(
	log *logrus.Entry,
	qwInputs []queueWorkInput,
	qwOutputs []queueWorkOutput,
	currQueueRingMetadata map[types.GPUName]queueRingMetadata,
	existingMsgIDs sets.Set[string],
) (creationMessagesByGPU map[types.GPUName][][]createMessageTuple, hasErrors bool) {
	creationMessagesByGPU = map[types.GPUName][][]createMessageTuple{}
	for i, qwOutput := range qwOutputs {
		if qwOutput.err != nil {
			hasErrors = true
			continue
		}
		gpuName := qwInputs[i].gpuName
		qmeta := currQueueRingMetadata[gpuName]
		if _, ok := creationMessagesByGPU[gpuName]; !ok {
			creationMessagesByGPU[gpuName] = make([][]createMessageTuple, qmeta.ringSize)
		}
		var tuples []createMessageTuple
		// Construct creation message tuples in order, and dedupe against current messages
		for _, message := range qwOutput.messages {
			if existingMsgIDs.Has(message.MessageID) {
				log.Debugf("Creation ICMS request for %s already in progress, skipping", message.MessageID)
				continue
			}
			existingMsgIDs.Insert(message.MessageID)
			if message.Body == nil {
				log.Debugf("Creation ICMS request for %s has an empty body, skipping", message.MessageID)
				continue
			}
			tuples = append(tuples, createMessageTuple{
				message:   message,
				gpuName:   gpuName,
				queueInfo: qwInputs[i].qi,
			})
		}
		messageMatrix := creationMessagesByGPU[gpuName]
		messageMatrix[qmeta.startIdx] = tuples
		qmeta.startIdx = (qmeta.startIdx + 1) % qmeta.ringSize
		currQueueRingMetadata[gpuName] = qmeta
	}

	return creationMessagesByGPU, hasErrors
}

func (qm *QueueManager) mustSkipQueueForGPU(ctx context.Context, gpuName types.GPUName) bool {
	qm.numSkipProcessCreationMtx.Lock()
	defer qm.numSkipProcessCreationMtx.Unlock()
	log := core.GetLogger(ctx)
	numSkipProcessCreation := qm.numSkipProcessCreation[gpuName]
	skip := false
	if numSkipProcessCreation > 0 {
		log.Infof("Skip processing CreationMessages: currently in backoff, %v more iterations to retry",
			numSkipProcessCreation)
		// decrement SkipProcessing
		numSkipProcessCreation--
		qm.numSkipProcessCreation[gpuName] = numSkipProcessCreation
		skip = true
	} else if !qm.creationMessagesFetchable(ctx, gpuName) {
		skip = true
	}
	return skip
}

func (qm *QueueManager) setNumSkipProcessCreation(gpuName types.GPUName, skip uint64) {
	qm.numSkipProcessCreationMtx.Lock()
	defer qm.numSkipProcessCreationMtx.Unlock()
	qm.numSkipProcessCreation[gpuName] = skip
}

func (qm *QueueManager) creationMessagesFetchable(ctx context.Context, gpuName types.GPUName) bool {
	log := core.GetLogger(ctx)
	return qm.isHealthy(ctx, func(gs types.GPUResourceSet) bool {
		currGPURes := gs[gpuName]
		// Use <= to guard against calculation bugs.
		if currGPURes.Capacity <= currGPURes.Allocated {
			// Only report GPU at capacity the first time it switches from having capacity
			// to not having capacity
			if alreadyAtCapacity := qm.SetGPUAtCapacity(gpuName, true); !alreadyAtCapacity {
				log.Warnf("Messages not fetchable for GPU %s: at capacity (%d)",
					gpuName, currGPURes.Capacity)
			}
			return false
		}
		// Store that capacity is available
		qm.SetGPUAtCapacity(gpuName, false)
		return true
	})
}

func (qm *QueueManager) tryPopMessage(ctx context.Context, qv queueWorkInput) (qr queueWorkOutput) {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"queueType": qv.queueType,
		"queueURL":  qv.qi.QueueURL,
	})
	if qv.gpuName != "" {
		log = log.WithField("gpu", qv.gpuName)
	}

	log.Debugf("Fetching first %d available messages for %ds", qm.maxQueueMessages, maximumWaitTimeSeconds)

	qr.messages, qr.err = qm.Client.ReceiveMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:                qv.qi,
		MaxNumberOfMessages:      qm.maxQueueMessages,
		WaitTimeSeconds:          maximumWaitTimeSeconds,
		VisibilityTimeoutSeconds: qv.vtoSec,
	})
	if qr.err != nil {
		log.WithError(qr.err).Error("Receive message failed")
	} else {
		messageCount := len(qr.messages)
		if messageCount == 0 {
			log.Debug("No messages available")
		} else {
			log.Debugf("fetched %d messages from SQS", messageCount)
		}
		// Record dequeue metrics for every attempt (including zero-message pulls)
		gpuNameStr := string(qv.gpuName)
		if gpuNameStr == "" {
			gpuNameStr = "none"
		}
		// Only increment dequeued counter when we actually got messages
		if messageCount > 0 {
			qm.metrics.RecordQueueMessageDequeued(qv.queueType, gpuNameStr, messageCount)
		}
		// Always record batch size (including zeros) to track empty pulls
		qm.metrics.RecordQueueDequeueBatchSize(qv.queueType, gpuNameStr, messageCount)
	}

	return qr
}

func newIterCreationMessageMatrix(maxMessages int, messageMatrix [][]createMessageTuple) iter.Seq[createMessageTuple] {
	return func(yield func(createMessageTuple) bool) {
		// Pop creation messages in row then column order to preserve both queue and message ordering.
		for r := range maxMessages {
			for c := range len(messageMatrix) {
				if r >= len(messageMatrix[c]) {
					continue
				}
				cm := messageMatrix[c][r]
				if !yield(cm) {
					return
				}
			}
		}
	}
}

func (qm *QueueManager) doCreationMessage(ctx context.Context,
	gpuName types.GPUName,
	cumGPUReq uint64,
	qi queue.MessageQueueInfo,
	message queue.ReceiveMessageOutput,
) (uint64, error) {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"queueType": queue.CreationQueue,
		"gpu":       gpuName,
	})

	msg, err := translate.DecodeCreationQueueMessage(message.Body)
	if err != nil {
		log.WithError(err).Errorf("Decode creation message %+v", message)
		return 0, fmt.Errorf("decode creation message: %v", err)
	}

	log.Debugf("Applying creation message")

	msgMeta := msg.GetCreationQueueMessageMetadata()

	log.Debugf("Request metadata: %+v", msgMeta)

	var gpi uint64
	if msgMeta.RequestedGPUCount == 0 {
		gpi = 1
	} else {
		gpi = msgMeta.RequestedGPUCount
	}
	request := msgMeta.InstanceCount * gpi

	var cerr error
	var icmsReq *nvcav2beta1.ICMSRequest
	if !qm.creationRequestServiceable(ctx, gpuName, cumGPUReq, request) {
		// make the message visible again immediately and activate random skip
		skip := (uint64(time.Now().UnixNano()) + maxCreationSkip + 1) % maxCreationSkip
		qm.setNumSkipProcessCreation(gpuName, skip)
		cerr = fmt.Errorf("gpu %s is over capacity, backing off", gpuName)
	} else {
		// reset random skip if any set
		qm.setNumSkipProcessCreation(gpuName, 0)

		if sr, err := qm.Backendk8s.CreateICMSCreationMessageRequest(ctx, msg,
			message.ReceiptHandle, message.MessageID, qi.QueueURL,
		); err != nil {
			cerr = fmt.Errorf("failed in CreateICMSCreationMessageRequest, err: %v", err)
		} else {
			icmsReq = sr
		}
	}
	if cerr != nil {
		// make the message visible again immediately
		vTO := int64(0)

		if err := qm.Client.ChangeMessageVisibility(ctx, queue.ChangeMessageVisibilityInput{
			QueueInfo:                qi,
			ReceiptHandle:            message.ReceiptHandle,
			VisibilityTimeoutSeconds: &vTO,
		}); err != nil {
			log.WithError(err).Errorf("failed to make visible immediately, will be after %v seconds",
				creationQueueVisibilityTimeoutSeconds)
		} else {
			log.WithError(cerr).Error("Failed to apply message, it will be visible immediately")
		}
		return 0, fmt.Errorf("failed to apply creation message: %v", cerr)
	}

	logging.NewICMSRequestFieldLogger(icmsReq, log).Info("Applied one creation message")

	return request, nil
}

func (qm *QueueManager) doTerminationMessage(ctx context.Context,
	qi queue.MessageQueueInfo,
	message queue.ReceiveMessageOutput,
) error {
	log := core.GetLogger(ctx).WithField("queueType", queue.TerminationQueue)

	var tm types.ICMSTerminationMessage
	if err := json.NewDecoder(bytes.NewReader(message.Body)).Decode(&tm); err != nil {
		log.WithError(err).Errorf("Decode termination message %+v", message)
		return fmt.Errorf("decode termination message: %v", err)
	}

	// Add metrics for the termination message
	metrics := nvcametrics.FromContext(ctx)
	metrics.QueueMessageProcessedTotal.
		WithLabelValues(metrics.WithDefaultLabelValues(string(tm.Action))...).Inc()

	log.Debugf("Applying termination message")

	// TODO: instead create a CRD for the TerminationMessage and have it event driven
	// So RequestStatuses can be better managed by BART
	if err := qm.Backendk8s.CreateICMSTerminationMessageRequest(ctx, tm,
		message.ReceiptHandle, message.MessageID,
	); err != nil {
		return fmt.Errorf("failed to apply termination message: %v", err)
	}

	// purge the message from queue after processing
	if err := qm.Client.DeleteMessage(ctx, queue.DeleteMessageInput{
		QueueInfo:     qi,
		ReceiptHandle: message.ReceiptHandle,
	}); err != nil {
		return fmt.Errorf("failed to delete the Message: receipt: %v, err: %v", message.ReceiptHandle, err)
	}

	log.Info("Applied one termination message")

	return nil
}

func (qm *QueueManager) creationRequestServiceable(ctx context.Context,
	gpuName types.GPUName,
	cumGPURequest, request uint64,
) bool {
	log := core.GetLogger(ctx)
	return qm.isHealthy(ctx, func(gs types.GPUResourceSet) bool {
		currGPURes := gs[gpuName]
		if !currGPURes.HasCapacityForRequest(request + cumGPURequest) {
			log.Warnf("Request not serviceable for GPU %s: capacity=%d, allocated=%d, applied=%d, requested=%d",
				gpuName, currGPURes.Capacity, currGPURes.Allocated, cumGPURequest, request)
			return false
		}
		return true
	})
}

func (qm *QueueManager) isHealthy(ctx context.Context,
	checkGPUs func(types.GPUResourceSet) bool,
) bool {
	log := core.GetLogger(ctx)

	ah := qm.statusGetter.GetStatus()

	if !checkGPUs(ah.GPUUsage) {
		return false
	}

	// These components, when configured at agent startup,
	// are required to schedule _any_ workload Pods.
	var unhealthyCmps []string
	for _, cmpName := range []string{
		kata.ComponentName,
		hostisolation.ComponentName,
	} {
		if cmpHealth, ok := ah.Components[cmpName]; ok && !cmpHealth.IsHealthy() {
			unhealthyCmps = append(unhealthyCmps, cmpName)
		}
	}
	if len(unhealthyCmps) != 0 {
		log.Warnf("Request not serviceable due to unhealthy components: %+q", unhealthyCmps)
		return false
	}

	return true
}

func (qm *QueueManager) DeleteCreationMessageV2(ctx context.Context, rhdl, queueURL string) error {
	return qm.Client.DeleteMessage(ctx, queue.DeleteMessageInput{
		QueueInfo:     qm.getCreationQueueInfo(queueURL),
		ReceiptHandle: rhdl,
	})
}

func (qm *QueueManager) DeleteCreationMessage(ctx context.Context, gpuName, rhdl string) error {
	return qm.Client.DeleteMessage(ctx, queue.DeleteMessageInput{
		QueueInfo:     qm.getCreateQueue(types.GPUName(gpuName)),
		ReceiptHandle: rhdl,
	})
}

func (qm *QueueManager) ExtendCreationMessableVisibilityTimeout(ctx context.Context, gpuName, rhdl string) error {
	// set the visibilityTimeout this will start a new Ticker
	vTO := creationQueueVisibilityTimeoutSeconds

	return qm.Client.ChangeMessageVisibility(ctx, queue.ChangeMessageVisibilityInput{
		QueueInfo:                qm.getCreateQueue(types.GPUName(gpuName)),
		ReceiptHandle:            rhdl,
		VisibilityTimeoutSeconds: &vTO,
	})
}

func (qm *QueueManager) ExtendCreationMessableVisibilityTimeoutV2(ctx context.Context, rhdl, queueURL string) error {
	// set the visibilityTimeout this will start a new Ticker
	vTO := creationQueueVisibilityTimeoutSeconds

	return qm.Client.ChangeMessageVisibility(ctx, queue.ChangeMessageVisibilityInput{
		QueueInfo:                qm.getCreationQueueInfo(queueURL),
		ReceiptHandle:            rhdl,
		VisibilityTimeoutSeconds: &vTO,
	})
}

func (qm *QueueManager) SetStatusOK(ok bool) { qm.healthy.Store(ok) }
func (qm *QueueManager) StatusOK() bool      { return qm.healthy.Load() }
func (qm *QueueManager) Name() string        { return "queuemanager" }

// Pause pauses queue processing. Creation messages will not be processed,
// but termination messages will continue to be processed.
// Existing in-flight work will continue to completion.
func (qm *QueueManager) Pause() { qm.paused.Store(true) }

// Resume resumes queue processing after a pause.
func (qm *QueueManager) Resume() { qm.paused.Store(false) }

// IsPaused returns whether the queue manager is currently paused.
func (qm *QueueManager) IsPaused() bool { return qm.paused.Load() }

// IsGPUAtCapacity checks if GPU is at capacity
func (qm *QueueManager) IsGPUAtCapacity(gpuName types.GPUName) bool {
	atCapacity, _ := qm.gpuAtCapacityStore.LoadOrStore(gpuName, false)
	return atCapacity.(bool)
}

// SetGPUAtCapacity returns previous value
func (qm *QueueManager) SetGPUAtCapacity(gpuName types.GPUName, atCapacity bool) bool {
	prevValue, loaded := qm.gpuAtCapacityStore.LoadOrStore(gpuName, atCapacity)
	if loaded {
		// If the key existed, we need to update it and return the previous value
		qm.gpuAtCapacityStore.Store(gpuName, atCapacity)
		return prevValue.(bool)
	}
	// If the key didn't exist, return false (default value)
	return false
}
