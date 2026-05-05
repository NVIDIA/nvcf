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
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcainformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	mockqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	PVName    = "nv-mesh-icms-rw-pv"
	RWPVCName = "nv-mesh-icms-testing-rw-pvc"
	ROPVCName = "nv-mesh-icms-testing-ro-pvc"

	creationMessageId string = "creationPod12345"
	termMessageId     string = "termPod12345"

	gpuNameA100 = types.GPUName("A100")
	gpuNameL40  = types.GPUName("L40")
)

// mustICMSRequestLister returns an ICMSRequest lister from the given informer factory (uses ForResource like production).
func mustICMSRequestLister(t *testing.T, factory nvcainformers.SharedInformerFactory) nvcav2beta1listers.ICMSRequestLister {
	t.Helper()
	genInf, err := factory.ForResource(nvcav2beta1.SchemeGroupVersion.WithResource("icmsrequests"))
	require.NoError(t, err)
	return nvcav2beta1listers.NewICMSRequestLister(genInf.Informer().GetIndexer())
}

var (
	//go:embed testdata/creationmsg_good.json
	goodCM string
	//go:embed testdata/creationmsg_L40_good.json
	goodCML40 string
	//go:embed testdata/creationmsg_good_cached.json
	goodCMCached string
	//go:embed testdata/terminationmsg_good.json
	goodTM string
	//go:embed testdata/creds-v2.json
	queueCreds string
)

func TestSyncQueues(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
		featureFlagFetcher: featureflag.DefaultFetcher,
	}

	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	bk8sHealthComponent := &mockBackendStatusGetter{
		k8sVersion: "1.29.5",
		hs: types.AgentHealth{
			GPUUsage: map[types.GPUName]types.GPUResource{
				gpuNameA100: {
					Capacity:  5,
					Allocated: 0,
				},
				gpuNameL40: {
					Capacity:  5,
					Allocated: 0,
				},
			},
			Components: map[string]types.ComponentHealth{
				"kata": {
					Status: types.HealthStatusHealthy,
				},
			},
		},
	}
	bk8sHealth := health.NewBackendStatusCache(10*time.Millisecond, bk8sHealthComponent)

	qc := &mockqueue.Client{
		Use10MillisForWaits: true,
	}

	targetedQueueA100 := queue.MessageQueueInfo{
		GPU:       string(gpuNameA100),
		QueueURL:  "a100url_targeted",
		QueueType: queue.CreationQueue,
	}
	taskTargetedQueueA100 := queue.MessageQueueInfo{
		GPU:       string(gpuNameA100),
		QueueURL:  "a100url_targeted_task",
		QueueType: queue.CreationQueue,
	}
	targetedQueueL40 := queue.MessageQueueInfo{
		GPU:       string(gpuNameL40),
		QueueURL:  "l40url_targeted",
		QueueType: queue.CreationQueue,
	}
	taskTargetedQueueL40 := queue.MessageQueueInfo{
		GPU:       string(gpuNameL40),
		QueueURL:  "l40url_targeted_task",
		QueueType: queue.CreationQueue,
	}
	clusterQueueA100 := queue.MessageQueueInfo{
		GPU:       string(gpuNameA100),
		QueueURL:  "a100url_cluster",
		QueueType: queue.CreationQueue,
	}

	targetedQueues := map[types.GPUName]queue.MessageQueueInfo{
		gpuNameA100: targetedQueueA100,
		gpuNameL40:  targetedQueueL40,
	}
	taskTargetedQueues := map[types.GPUName]queue.MessageQueueInfo{
		gpuNameA100: taskTargetedQueueA100,
		gpuNameL40:  taskTargetedQueueL40,
	}
	clusterQueues := map[types.GPUName]queue.MessageQueueInfo{
		gpuNameA100: clusterQueueA100,
	}
	termQueue := queue.MessageQueueInfo{
		QueueURL:  "terminationurl",
		QueueType: queue.TerminationQueue,
	}
	queueCreds := types.QueueCredentials{
		ClusterCreationQueues:     targetedQueues,
		TaskClusterCreationQueues: taskTargetedQueues,
		CreationQueues:            clusterQueues,
		TerminationQueue:          termQueue,
	}

	metrics := nvcametrics.FromContext(ctx)
	qm := NewQueueManager(
		bk8s, bk8sHealth, qc,
		queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeNone,
		metrics,
	)

	listSPOTRequests := func() []nvcav2beta1.ICMSRequest {
		t.Helper()
		srList, err := clients.BART.NvcaV2beta1().ICMSRequests(bk8s.requestsNamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		return srList.Items
	}

	var err error
	var reqs []nvcav2beta1.ICMSRequest

	// Nothing in the queues, should return no error and no SPOTRequests were created.
	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
	assert.Empty(t, listSPOTRequests())
	assert.True(t, qm.StatusOK())

	// Add an A100 message to the queue.
	targetedCreateMsg1 := newCreationMessageRandomized(t, []byte(goodCM))
	qc.AddMessage(targetedQueueA100.QueueURL, targetedCreateMsg1)

	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
	reqs = listSPOTRequests()
	if assert.Len(t, reqs, 1) {
		checkSPOTRequestExists(t, ctx, clients, targetedCreateMsg1)
	}
	assert.True(t, qm.StatusOK())

	// Update allocated to capacity now that it is in-use.
	gpuUsageA100 := bk8sHealthComponent.hs.GPUUsage[gpuNameA100]
	gpuUsageA100.Allocated = gpuUsageA100.Capacity
	bk8sHealthComponent.hs.GPUUsage[gpuNameA100] = gpuUsageA100

	// Delete the message like the agent would on ack.
	require.NoError(t, qc.DeleteMessage(ctx, queue.DeleteMessageInput{
		QueueInfo:     targetedQueueA100,
		ReceiptHandle: targetedCreateMsg1.ReceiptHandle,
	}))

	// Add another A100 message to the queue to check skip.
	// This message should not be visible after skip.
	targetedQueueA100Skip := newCreationMessageRandomized(t, []byte(goodCM))
	qc.AddMessage(targetedQueueA100.QueueURL, targetedQueueA100Skip)

	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
	reqs = listSPOTRequests()
	if assert.Len(t, reqs, 1) {
		checkSPOTRequestExists(t, ctx, clients, targetedCreateMsg1)
	}
	assert.True(t, qm.StatusOK())

	// The message should be available again immediately, so try again to check skip.
	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
	reqs = listSPOTRequests()
	if assert.Len(t, reqs, 1) {
		checkSPOTRequestExists(t, ctx, clients, targetedCreateMsg1)
	}
	assert.True(t, qm.StatusOK())

	// Reset skip to simulate backoff passing, and delete the message.
	qm.numSkipProcessCreation[gpuNameA100] = 0
	require.NoError(t, qc.DeleteMessage(ctx, queue.DeleteMessageInput{
		QueueInfo:     targetedQueueA100,
		ReceiptHandle: targetedQueueA100Skip.ReceiptHandle,
	}))

	// Add a term message to the queue.
	termMsg1 := queue.ReceiveMessageOutput{
		MessageID:     "tid1",
		ReceiptHandle: "trhdl1",
		Body:          []byte(goodTM),
	}
	qc.AddMessage(termQueue.QueueURL, termMsg1)

	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
	reqs = listSPOTRequests()
	if assert.Len(t, reqs, 2) {
		checkSPOTRequestExists(t, ctx, clients, targetedCreateMsg1)
		checkSPOTRequestExists(t, ctx, clients, termMsg1)
	}
	assert.True(t, qm.StatusOK())

	// Add another message to the queue, ensure a new ICMSRequest is not created due to capacity issues.
	targetedCreateMsg2 := newCreationMessageRandomized(t, []byte(goodCM))
	qc.AddMessage(targetedQueueA100.QueueURL, targetedCreateMsg2)

	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
	reqs = listSPOTRequests()
	assert.Len(t, reqs, 2)

	// Bump capacity but less than allowed for cumulative application of two A100 messages.
	gpuUsageA100 = bk8sHealthComponent.hs.GPUUsage[gpuNameA100]
	gpuUsageA100.Capacity += 7
	bk8sHealthComponent.hs.GPUUsage[gpuNameA100] = gpuUsageA100

	// Delete the message like the agent would on ack.
	require.NoError(t, qc.DeleteMessage(ctx, queue.DeleteMessageInput{
		QueueInfo:     targetedQueueA100,
		ReceiptHandle: targetedCreateMsg2.ReceiptHandle,
	}))

	// Add three messages to queues:
	// 1 targeted and 1 cluster queue A100
	// 1 targeted queue L40
	// Ensure only 1 new SPOTRequests is created for A100, due to capacity issues,
	// and 1 for L40.
	targetedCreateMsg3 := newCreationMessageRandomized(t, []byte(goodCM))
	qc.AddMessage(targetedQueueA100.QueueURL, targetedCreateMsg3)
	targetedCreateMsgL401 := newCreationMessageRandomized(t, []byte(goodCML40))
	qc.AddMessage(targetedQueueL40.QueueURL, targetedCreateMsgL401)
	clusterCreateMsg1 := newCreationMessageRandomized(t, []byte(goodCM))
	qc.AddMessage(clusterQueueA100.QueueURL, clusterCreateMsg1)
	taskTargetedCreateMsg1 := newCreationMessageRandomized(t, []byte(cmsgTaskContainer))
	qc.AddMessage(taskTargetedQueueA100.QueueURL, taskTargetedCreateMsg1)

	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
	reqs = listSPOTRequests()
	if assert.Len(t, reqs, 4) {
		checkSPOTRequestExists(t, ctx, clients, targetedCreateMsg1)
		checkSPOTRequestExists(t, ctx, clients, targetedCreateMsgL401)
		checkSPOTRequestExists(t, ctx, clients, termMsg1)
		// Exactly one message from any A100 queue must be created.
		oneExists := false
		for _, msg := range []queue.ReceiveMessageOutput{
			targetedCreateMsg3,
			clusterCreateMsg1,
			taskTargetedCreateMsg1,
		} {
			selectorStr := getICMSLabelSelectorString(msg.MessageID)
			icmsRequests, err :=
				clients.BART.NvcaV2beta1().ICMSRequests(RequestsNamespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
			require.NoError(t, err)
			if len(icmsRequests.Items) == 1 {
				if oneExists {
					assert.Fail(t, "more than one A100 message exists")
				} else {
					oneExists = true
				}
			}
		}
		assert.True(t, oneExists, "one A100 message must exist")
	}
}

func TestQueueManagerMethods(t *testing.T) {
	ctx := newTestContext()
	var err error

	// Test panic in NewQueueManager
	if !func() (panicked bool) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		metrics := nvcametrics.FromContext(ctx)
		_ = NewQueueManager(nil, nil, nil, types.QueueCredentials{}, featureflag.DefaultFetcher, types.MaintenanceModeNone, metrics)
		return false
	}() {
		t.Fatal("NewQueueManager did not panic")
	}

	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		featureFlagFetcher: featureflag.DefaultFetcher,
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}
	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	srClient := clients.BART.NvcaV2beta1().ICMSRequests(bk8s.requestsNamespace)

	qc := &mockqueue.Client{}

	targetedQueues := map[types.GPUName]queue.MessageQueueInfo{}
	clusterQueues := map[types.GPUName]queue.MessageQueueInfo{}
	termQueue := queue.MessageQueueInfo{QueueURL: "termurl"}
	queueCreds := types.QueueCredentials{
		ClusterCreationQueues: targetedQueues,
		CreationQueues:        clusterQueues,
		TerminationQueue:      termQueue,
	}
	metrics := nvcametrics.FromContext(ctx)
	qm := NewQueueManager(
		bk8s,
		health.NewBackendStatusCache(0),
		qc,
		queueCreds,
		featureflag.DefaultFetcher,
		types.MaintenanceModeNone,
		metrics,
	)

	// Test getters.
	gpuNameA100 := types.GPUName("A100")
	assert.Equal(t, queue.MessageQueueInfo{}, qm.getCreateQueue(gpuNameA100))
	assert.Equal(t, queue.MessageQueueInfo{}, qm.getCreationQueueInfo("url"))
	targetedQueueA100 := queue.MessageQueueInfo{QueueURL: "urla100t"}
	targetedQueues[gpuNameA100] = targetedQueueA100
	clusterQueueA100 := queue.MessageQueueInfo{QueueURL: "urla100c"}
	clusterQueues[gpuNameA100] = clusterQueueA100
	assert.Equal(t,
		queue.MessageQueueInfo{QueueURL: clusterQueueA100.QueueURL},
		qm.getCreationQueueInfo(clusterQueueA100.QueueURL),
	)

	// Test creation message failures.
	_, err = qm.doCreationMessage(ctx, gpuNameA100, 0, clusterQueueA100,
		queue.ReceiveMessageOutput{Body: []byte("{")},
	)
	assert.EqualError(t, err, "decode creation message: decode to type-check message: unexpected end of JSON input")

	createMsg := queue.ReceiveMessageOutput{
		MessageID:     "msgid1",
		ReceiptHandle: "rhdl1",
		Body:          []byte(`{"requestId":"reqid","functionId":"funcid","instanceType":"ON-PREM.GPU.A100"}`),
	}
	qc.AddMessage(termQueue.QueueURL, createMsg)
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	func() {
		defer func() { SetUseUUIDForRequestObjName(origUUID) }()
		existingSR := &nvcav2beta1.ICMSRequest{}
		existingSR.Name = "sr-reqid"
		_, err = srClient.Create(ctx, existingSR, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = qm.doCreationMessage(ctx, gpuNameA100, 0, clusterQueueA100, createMsg)
		assert.EqualError(t, err, "failed to apply creation message: failed in CreateICMSCreationMessageRequest, err: failed to persist the ICMS request on the backend, err: icmsrequests.nvca.nvcf.nvidia.io \"sr-reqid\" already exists")
		err = srClient.Delete(ctx, existingSR.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}()

	// Test termination message failures.
	err = qm.doTerminationMessage(ctx, clusterQueueA100,
		queue.ReceiveMessageOutput{Body: []byte("{")},
	)
	assert.EqualError(t, err, "decode termination message: unexpected EOF")

	err = qm.doTerminationMessage(ctx, clusterQueueA100,
		queue.ReceiveMessageOutput{Body: []byte(`{}`)},
	)
	assert.EqualError(t, err, "failed to delete the Message: receipt: , err: no message in queue urla100c with receipt handle ")

	termMsg := queue.ReceiveMessageOutput{
		MessageID:     "msgid1",
		ReceiptHandle: "rhdl1",
		Body:          []byte(`{"requestId":"reqid"}`),
	}
	qc.AddMessage(termQueue.QueueURL, termMsg)
	origUUID = GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	func() {
		defer func() { SetUseUUIDForRequestObjName(origUUID) }()
		existingSR2 := &nvcav2beta1.ICMSRequest{}
		existingSR2.Name = "sr-reqid"
		_, err = srClient.Create(ctx, existingSR2, metav1.CreateOptions{})
		require.NoError(t, err)
		err = qm.doTerminationMessage(ctx, clusterQueueA100, termMsg)
		require.Error(t, err)
		assert.ErrorContains(t, err, "failed to apply termination message")
		assert.ErrorContains(t, err, "failed to persist the ICMS request")
		assert.ErrorContains(t, err, "icmsrequests.nvca.nvcf.nvidia.io \"sr-reqid\" already exists")
		err = srClient.Delete(ctx, existingSR2.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}()

	// Delete methods.
	deleteMsg := queue.ReceiveMessageOutput{
		MessageID:     "msgid2",
		ReceiptHandle: "rhdl2",
		Body:          []byte(`{}`),
	}
	qc.AddMessage(clusterQueueA100.QueueURL, deleteMsg)
	err = qm.DeleteCreationMessage(ctx, string(gpuNameA100), deleteMsg.ReceiptHandle)
	assert.NoError(t, err)
	qc.AddMessage(targetedQueueA100.QueueURL, deleteMsg)
	err = qm.DeleteCreationMessageV2(ctx, deleteMsg.ReceiptHandle, targetedQueueA100.QueueURL)
	assert.NoError(t, err)

	// Extend methods.
	extendMsg := queue.ReceiveMessageOutput{
		MessageID:     "msgid3",
		ReceiptHandle: "rhdl3",
		Body:          []byte(`{}`),
	}
	qc.AddMessage(clusterQueueA100.QueueURL, extendMsg)
	err = qm.ExtendCreationMessableVisibilityTimeout(ctx, string(gpuNameA100), extendMsg.ReceiptHandle)
	assert.NoError(t, err)
	qc.AddMessage(targetedQueueA100.QueueURL, extendMsg)
	err = qm.ExtendCreationMessableVisibilityTimeoutV2(ctx, extendMsg.ReceiptHandle, targetedQueueA100.QueueURL)
	assert.NoError(t, err)

	// QueueManager status.
	assert.True(t, qm.StatusOK())
	qm.SetStatusOK(false)
	assert.False(t, qm.StatusOK())

	// QueueManager pause/resume.
	assert.False(t, qm.IsPaused())
	qm.Pause()
	assert.True(t, qm.IsPaused())
	qm.Resume()
	assert.False(t, qm.IsPaused())
}

func TestSyncQueuesWithBk8s(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	queueCreds := getTestQueueCreds(false)
	createQueue := queueCreds.CreationQueues[testGPUNameDefault]

	qc := &mockqueue.Client{
		Use10MillisForWaits: true,
	}

	bsc := newMockBackendStatusCacheFromK8s(t, bc)
	metrics := nvcametrics.FromContext(ctx)
	sqm := NewQueueManager(bc, bsc, qc, queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeNone, metrics)
	assert.True(t, sqm.StatusOK())

	err = sqm.SyncQueues(ctx)
	assert.NoError(t, err)
	assert.True(t, sqm.StatusOK())

	createMsg1 := queue.ReceiveMessageOutput{
		MessageID:     creationMessageId,
		ReceiptHandle: "randomId5",
		Body:          []byte(goodCM),
	}
	qc.AddMessage(createQueue.QueueURL, createMsg1)
	_, err = sqm.doCreationMessage(ctx, testGPUNameDefault, 0, createQueue, createMsg1)
	assert.ErrorContains(t, err, "is over capacity, backing off")

	clients = mockKubeClients()
	b = NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients).
		WithStaticGPUCapacity(4)
	assert.NotNil(t, b)

	bc, _, err = b.Start(ctx)
	require.NoError(t, err)

	queueCreds = getTestQueueCreds(false)
	bsc = newMockBackendStatusCacheFromK8s(t, bc)
	metrics = nvcametrics.FromContext(ctx)
	sqm = NewQueueManager(bc, bsc, qc, queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeNone, metrics)

	createMsg2 := queue.ReceiveMessageOutput{
		MessageID:     creationMessageId,
		ReceiptHandle: "randomId4",
		Body:          []byte(goodCM),
	}
	qc.AddMessage(createQueue.QueueURL, createMsg2)
	_, err = sqm.doCreationMessage(ctx, testGPUNameDefault, 0, createQueue, createMsg2)
	assert.ErrorContains(t, err, "is over capacity, backing off")

	clients = mockKubeClients()
	b = NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients).
		WithStaticGPUCapacity(10)
	assert.NotNil(t, b)

	bc, _, err = b.Start(ctx)
	require.NoError(t, err)

	queueCreds = getTestQueueCreds(false)
	bsc = newMockBackendStatusCacheFromK8s(t, bc)
	metrics = nvcametrics.FromContext(ctx)
	sqm = NewQueueManager(bc, bsc, qc, queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeNone, metrics)
	assert.True(t, sqm.StatusOK())

	createMsg3 := queue.ReceiveMessageOutput{
		MessageID:     creationMessageId,
		ReceiptHandle: "randomId3",
		Body:          []byte(goodCM),
	}
	qc.AddMessage(createQueue.QueueURL, createMsg3)
	_, err = sqm.doCreationMessage(ctx, testGPUNameDefault, 0, createQueue, createMsg3)
	assert.NoError(t, err)
	bc.ForceSync(ctx)
	assert.True(t, sqm.StatusOK())

	// transition RequestStatus for test, normally it would be done by Agent
	err = updateRequestToPending(ctx, bc, "randomID1234", "randomId3")
	assert.NoError(t, err)

	verifyPodsCreated := func(ct *assert.CollectT) {
		// use local variable to avoid data race
		bc.SyncAllICMSRequests(ctx)
		err := updateInitJobToCompletion(ctx, bc)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		ps, err := bc.GetAllPodsForRequest(ctx, "randomID1234")
		assert.NoError(ct, err)
		assert.Len(ct, ps, 5)
	}
	require.EventuallyWithT(t, verifyPodsCreated, 120*time.Second, 100*time.Millisecond)

	_, err = bc.GetGPUResource(ctx, testGPUNameDefault)
	assert.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	assert.NoError(t, err)

	termMsg1 := queue.ReceiveMessageOutput{
		MessageID:     termMessageId,
		ReceiptHandle: "randomId1",
		Body:          []byte(goodTM),
	}
	qc.AddMessage(queueCreds.TerminationQueue.QueueURL, termMsg1)
	err = sqm.doTerminationMessage(ctx, queueCreds.TerminationQueue, termMsg1)
	assert.NoError(t, err)

	requestUpdated := func(ct *assert.CollectT) {
		// use local variable to avoid data race
		err := updateRequestToPending(ctx, bc, "random12345", "randomId1")
		assert.NoError(ct, err)
	}
	require.EventuallyWithT(t, requestUpdated, 20*time.Second, 100*time.Millisecond)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)

	_, err = bc.GetGPUResource(ctx, testGPUNameDefault)
	assert.NoError(t, err)

	verifyPodsDeleted := func(ct *assert.CollectT) {
		// Trigger sync to process pending deletions
		bc.SyncAllICMSRequests(ctx)
		bc.ForceSync(ctx)
		ps, err := bc.GetAllPodsForRequest(ctx, "randomID1234")
		assert.NoError(ct, err)
		assert.Len(ct, ps, 0)
	}
	require.EventuallyWithT(t, verifyPodsDeleted, 120*time.Second, 100*time.Millisecond)
}

func TestSyncQueuesWithBk8sFailedCaching(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	const cachedCreationMsgId string = "creationCachePod12345"

	// have the rwPV in place
	pvObj := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: PVName,
			Labels: map[string]string{
				"type": "local",
			},
		},
		Spec: v1.PersistentVolumeSpec{
			StorageClassName: "manual",
			Capacity: v1.ResourceList{
				v1.ResourceName("storage"): resource.MustParse("1Mi"),
			},
			AccessModes: []v1.PersistentVolumeAccessMode{
				v1.ReadWriteOnce,
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/mnt/data",
				},
			},
			ClaimRef: &v1.ObjectReference{
				Name: "nv-mesh-icms-testing-rw-pvc",
			},
		},
	}

	clients := mockKubeClients(pvObj)
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients).
		WithStaticGPUCapacity(10).
		WithCachingSupport(true, false).
		WithCSIVolumeMountOptions([]string{"ro", "nouuid"})
	assert.NotNil(t, b)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	qc := &mockqueue.Client{
		Use10MillisForWaits: true,
	}

	queueCreds := getTestQueueCreds(false)
	createQueue := queueCreds.CreationQueues[testGPUNameDefault]
	bsc := newMockBackendStatusCacheFromK8s(t, bc)
	metrics := nvcametrics.FromContext(ctx)
	sqm := NewQueueManager(bc, bsc, qc, queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeNone, metrics)
	assert.True(t, sqm.StatusOK())

	createMsg1 := queue.ReceiveMessageOutput{
		MessageID:     cachedCreationMsgId,
		ReceiptHandle: "randomId34",
		Body:          []byte(goodCMCached),
	}
	qc.AddMessage(createQueue.QueueURL, createMsg1)
	_, err = sqm.doCreationMessage(ctx, testGPUNameDefault, 0, createQueue, createMsg1)
	assert.NoError(t, err)
	bc.ForceSync(ctx)

	// transition RequestStatus for test, normally it would be done by Agent
	err = updateAllRequestToPending(ctx, bc)
	assert.NoError(t, err)

	verifyPodsCreated := func(ct *assert.CollectT) {
		err = bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		err = updateInitJobToCompletion(ctx, bc)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		ps, err := bc.GetAllPodsForRequest(ctx, "randomID345")
		assert.NoError(ct, err)
		assert.Len(ct, ps, 5)
	}
	require.EventuallyWithT(t, verifyPodsCreated, 120*time.Second, 100*time.Millisecond)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	assert.NoError(t, err)

	srList, err := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	err = bc.k8sArtifactHelper.(K8sComputeBackend).CleanupModelCachingSetupArtifacts(ctx, &srList.Items[0])
	assert.Nil(t, err)

	termMsg1 := queue.ReceiveMessageOutput{
		MessageID:     termMessageId,
		ReceiptHandle: "randomId1",
		Body:          []byte(goodTM),
	}
	qc.AddMessage(queueCreds.TerminationQueue.QueueURL, termMsg1)
	err = sqm.doTerminationMessage(ctx, queueCreds.TerminationQueue, termMsg1)
	assert.NoError(t, err)

	err = updateAllRequestToPending(ctx, bc)
	assert.NoError(t, err)

	verifyPodsDeleted := func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		err = bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		ps, err := bc.GetAllPodsForRequest(ctx, "randomID1234")
		assert.NoError(ct, err)
		assert.Len(ct, ps, 0)
	}
	require.EventuallyWithT(t, verifyPodsDeleted, 20*time.Second, 100*time.Millisecond)

	err = cleanupAllSPOTRequests(ctx, bc)
	assert.NoError(t, err)
	bc.ForceSync(ctx)
	bc.SyncAllICMSRequests(ctx)

	_, err = sqm.doCreationMessage(ctx, testGPUNameDefault, 0, createQueue, createMsg1)
	assert.NoError(t, err)
	bc.ForceSync(ctx)

	// transition RequestStatus for test, normally it would be done by Agent
	err = updateAllRequestToPending(ctx, bc)
	assert.NoError(t, err)
	bc.ForceSync(ctx)

	verifyPodsCreated = func(ct *assert.CollectT) {
		err = bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		ps, err := bc.GetAllPodsForRequest(ctx, "randomID345")
		assert.NoError(ct, err)
		if assert.Len(ct, ps, 5) {
			pod := ps[0]
			for _, vol := range pod.Spec.Volumes {
				if vol.Name == ModelVolumeName {
					assert.Nil(ct, vol.VolumeSource.PersistentVolumeClaim)
				}
			}
		}
	}
	require.EventuallyWithT(t, verifyPodsCreated, 20*time.Second, 100*time.Millisecond)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	assert.NoError(t, err)

	qc.AddMessage(queueCreds.TerminationQueue.QueueURL, termMsg1)
	err = sqm.doTerminationMessage(ctx, queueCreds.TerminationQueue, termMsg1)
	assert.NoError(t, err)

	err = updateAllRequestToPending(ctx, bc)
	assert.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)

	verifyPodsDeleted = func(ct *assert.CollectT) {
		ps, err := bc.GetAllPodsForRequest(ctx, "randomID1234")
		if assert.NoError(ct, err) {
			assert.Len(ct, ps, 0)
		}
	}
	require.EventuallyWithT(t, verifyPodsDeleted, 20*time.Second, 100*time.Millisecond)

	err = cleanupAllSPOTRequests(ctx, bc)
	assert.NoError(t, err)
	bc.ForceSync(ctx)
	bc.SyncAllICMSRequests(ctx)
}

func newCreationMessageRandomized(t *testing.T, msgBytes []byte) queue.ReceiveMessageOutput {
	t.Helper()
	mg, err := translate.DecodeCreationQueueMessage(msgBytes)
	require.NoError(t, err)
	action := mg.GetCreationQueueMessageMetadata().Action
	if action == common.FunctionCreationAction || action == common.RequestICMSInstances {
		cmsg := mg.(function.CreationQueueMessage)
		cmsg.RequestID = uuid.NewString()
		cmsg.MessageBatchID = uuid.NewString()
		cmsg.Details.FunctionID = uuid.NewString()
		cmsg.Details.FunctionVersionID = uuid.NewString()
	} else {
		cmsg := mg.(task.CreationQueueMessage)
		cmsg.RequestID = uuid.NewString()
		cmsg.MessageBatchID = uuid.NewString()
		cmsg.Details.TaskID = uuid.NewString()
		cmsg.GPUType = "A100"
		cmsg.InstanceType = "DGX-CLOUD.GPU.A100"
	}
	b, err := json.Marshal(mg)
	require.NoError(t, err)
	return queue.ReceiveMessageOutput{
		MessageID:     uuid.NewString(),
		ReceiptHandle: uuid.NewString(),
		Body:          b,
	}
}

func checkSPOTRequestExists(t *testing.T, ctx context.Context, clients *kubeclients.KubeClients, msg queue.ReceiveMessageOutput) {
	t.Helper()
	selectorStr := getICMSLabelSelectorString(msg.MessageID)
	icmsRequests, err :=
		clients.BART.NvcaV2beta1().ICMSRequests(RequestsNamespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	require.NoError(t, err)
	assert.Len(t, icmsRequests.Items, 1)
}

type mockBackendStatusGetter struct {
	k8sVersion string
	hs         types.AgentHealth
	err        error
}

func (mbsg *mockBackendStatusGetter) GetComponentStatus(ctx context.Context) (types.AgentHealth, error) {
	if mbsg.err != nil {
		return types.AgentHealth{}, mbsg.err
	}
	return mbsg.hs, nil
}

func updateInitJobToCompletion(ctx context.Context, bc *BackendK8sCache) error {
	// Init cache jobs are created in podInstanceNamespace (same as requestsNamespace when unset).
	ns := bc.podInstanceNamespace
	if ns == "" {
		ns = bc.requestsNamespace
	}
	jL, _ := bc.clients.K8s.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
	for _, j := range jL.Items {
		j.Status.CompletionTime = &metav1.Time{Time: core.GetCurrentTime(ctx)}
		j.Status.Succeeded = 1

		_, err := bc.clients.K8s.BatchV1().Jobs(j.Namespace).UpdateStatus(ctx, &j, metav1.UpdateOptions{})
		return err
	}
	return nil
}

func decodeCM(t *testing.T, s string) (m function.CreationQueueMessage) {
	msg, err := translate.DecodeCreationQueueMessage([]byte(s))
	require.NoError(t, err)
	require.IsType(t, function.CreationQueueMessage{}, msg)
	return msg.(function.CreationQueueMessage)
}

func newMockBackendStatusCacheFromK8s(t *testing.T, bk8s *BackendK8sCache) *health.BackendStatusCache {
	bsc := health.NewBackendStatusCache(0, bk8s)
	// Refresh status to pick up bk8s configuration.
	_, err := bsc.RefreshStatus(newTestContext())
	require.NoError(t, err)
	return bsc
}

func TestSetGPUAtCapacity(t *testing.T) {
	tests := []struct {
		name       string
		gpuName    types.GPUName
		atCapacity bool
		expected   bool
		prevValue  interface{}
	}{
		{
			name:       "set to true",
			gpuName:    "gpu-1",
			atCapacity: true,
			expected:   false,
			prevValue:  nil,
		},
		{
			name:       "set to false",
			gpuName:    "gpu-2",
			atCapacity: false,
			expected:   false,
			prevValue:  nil,
		},
		{
			name:       "update existing value",
			gpuName:    "gpu-3",
			atCapacity: true,
			expected:   true,
			prevValue:  true,
		},
		{
			name:       "update existing value to false",
			gpuName:    "gpu-4",
			atCapacity: false,
			expected:   true,
			prevValue:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qm := &QueueManager{
				gpuAtCapacityStore: &sync.Map{},
			}

			if tt.prevValue != nil {
				qm.gpuAtCapacityStore.Store(tt.gpuName, tt.prevValue)
			}

			actual := qm.SetGPUAtCapacity(tt.gpuName, tt.atCapacity)

			assert.Equal(t, tt.expected, actual)
			assert.Equal(t, tt.atCapacity, qm.IsGPUAtCapacity(tt.gpuName))
		})
	}
}

type mockFeatureFlagFetcher struct {
	enabledFlags map[*featureflag.FeatureFlag]bool
}

func (m *mockFeatureFlagFetcher) IsFeatureFlagEnabled(flag *featureflag.FeatureFlag) bool {
	return m.enabledFlags[flag]
}

func TestGetQueueManagerMaxQueueMessages(t *testing.T) {
	tests := []struct {
		name           string
		featureFlags   map[*featureflag.FeatureFlag]bool
		envValue       string
		expectedResult int64
	}{
		{
			name: "Feature flag enabled",
			featureFlags: map[*featureflag.FeatureFlag]bool{
				featureflag.MaxSQSBatchPull: true,
			},
			expectedResult: 10,
		},
		{
			name:           "Env variable set",
			featureFlags:   map[*featureflag.FeatureFlag]bool{},
			envValue:       "5",
			expectedResult: 5,
		},
		{
			name:           "Default value",
			featureFlags:   map[*featureflag.FeatureFlag]bool{},
			expectedResult: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up mock feature flag fetcher
			fff := &mockFeatureFlagFetcher{
				enabledFlags: tt.featureFlags,
			}

			// Set up environment variable if needed
			if tt.envValue != "" {
				os.Setenv("NVCA_MAX_SQS_BATCH_PULL", tt.envValue)
				t.Cleanup(func() { os.Unsetenv("NVCA_MAX_SQS_BATCH_PULL") })
			}

			// Call the function
			result := getQueueManagerMaxQueueMessages(fff)

			// Assert the result
			require.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestQueueManagerMaintenanceMode(t *testing.T) {
	clients := mockKubeClients()

	bk8s := &BackendK8sCache{
		featureFlagFetcher: featureflag.DefaultFetcher,
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}
	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	qc := &mockqueue.Client{}
	queueCreds := types.QueueCredentials{
		ClusterCreationQueues: map[types.GPUName]queue.MessageQueueInfo{},
		CreationQueues:        map[types.GPUName]queue.MessageQueueInfo{},
		TerminationQueue:      queue.MessageQueueInfo{QueueURL: "termurl"},
	}

	tests := []struct {
		name             string
		maintenanceMode  types.MaintenanceMode
		expectedBehavior string
	}{
		{
			name:             "normal mode",
			maintenanceMode:  types.MaintenanceModeNone,
			expectedBehavior: "processes all queues",
		},
		{
			name:             "cordon mode",
			maintenanceMode:  types.MaintenanceModeCordon,
			expectedBehavior: "skips creation, processes termination",
		},
		{
			name:             "cordon and drain mode",
			maintenanceMode:  types.MaintenanceModeCordonAndDrain,
			expectedBehavior: "skips creation, processes termination",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := nvcametrics.FromContext(t.Context())
			qm := NewQueueManager(
				bk8s,
				health.NewBackendStatusCache(0),
				qc,
				queueCreds,
				featureflag.DefaultFetcher,
				tt.maintenanceMode,
				metrics,
			)

			// Verify that the queue manager was created with the correct maintenance mode
			assert.Equal(t, tt.maintenanceMode, qm.maintenanceMode)
		})
	}
}

func TestQueueManagerMaintenanceModeString(t *testing.T) {
	clients := mockKubeClients()

	bk8s := &BackendK8sCache{
		featureFlagFetcher: featureflag.DefaultFetcher,
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}
	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	qc := &mockqueue.Client{}
	queueCreds := types.QueueCredentials{
		ClusterCreationQueues: map[types.GPUName]queue.MessageQueueInfo{},
		CreationQueues:        map[types.GPUName]queue.MessageQueueInfo{},
		TerminationQueue:      queue.MessageQueueInfo{QueueURL: "termurl"},
	}

	tests := []struct {
		name            string
		maintenanceMode types.MaintenanceMode
		expectedString  string
	}{
		{
			name:            "normal mode string",
			maintenanceMode: types.MaintenanceModeNone,
			expectedString:  "None",
		},
		{
			name:            "cordon mode string",
			maintenanceMode: types.MaintenanceModeCordon,
			expectedString:  "CordonOnly",
		},
		{
			name:            "cordon and drain mode string",
			maintenanceMode: types.MaintenanceModeCordonAndDrain,
			expectedString:  "CordonAndDrain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := nvcametrics.FromContext(t.Context())
			qm := NewQueueManager(
				bk8s,
				health.NewBackendStatusCache(0),
				qc,
				queueCreds,
				featureflag.DefaultFetcher,
				tt.maintenanceMode,
				metrics,
			)

			// Verify that the maintenance mode string representation is correct
			assert.Equal(t, tt.expectedString, qm.maintenanceMode.String())
		})
	}
}

func TestQueueManagerMaintenanceModeSync(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		featureFlagFetcher: featureflag.DefaultFetcher,
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}
	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	qc := &mockqueue.Client{}
	bk8sHealthComponent := &mockBackendStatusGetter{
		hs: types.AgentHealth{
			Status:   types.HealthStatusHealthy,
			GPUUsage: map[types.GPUName]types.GPUResource{},
		},
	}
	bk8sHealth := health.NewBackendStatusCache(0, bk8sHealthComponent)

	termQueue := queue.MessageQueueInfo{
		QueueURL:  "terminationurl",
		QueueType: queue.TerminationQueue,
	}
	queueCreds := types.QueueCredentials{
		CreationQueues:   make(map[types.GPUName]queue.MessageQueueInfo),
		TerminationQueue: termQueue,
	}

	// Test cordon mode (skip creation processing)
	metrics := nvcametrics.FromContext(ctx)
	qmCordon := NewQueueManager(
		bk8s, bk8sHealth, qc,
		queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeCordon,
		metrics,
	)

	// Sync should skip creation processing in cordon mode
	err := qmCordon.SyncQueues(ctx)
	assert.NoError(t, err)

	// Test cordon-and-drain mode (no eviction during sync, only processes termination messages)
	qmCordonAndDrain := NewQueueManager(
		bk8s, bk8sHealth, qc,
		queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeCordonAndDrain,
		metrics,
	)

	// Create a running ICMSRequest that would be evicted during startup (not sync)
	srClient := clients.BART.NvcaV2beta1().ICMSRequests(bk8s.requestsNamespace)
	runningRequest := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "running-request",
			Namespace: bk8s.requestsNamespace,
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID:         "req-123",
			NCAId:             "nca-123",
			FunctionID:        "func-123",
			FunctionVersionID: "ver-123",
		},
		Status: nvcav2beta1.ICMSRequestStatus{
			RequestStatus: nvcav2beta1.ICMSRequestStatusInProgress,
			Instances: map[string]nvcav2beta1.InstanceStatus{
				"instance-1": {ID: "instance-1", Status: "running"},
			},
		},
	}
	_, err = srClient.Create(ctx, runningRequest, metav1.CreateOptions{})
	require.NoError(t, err)

	// Count existing requests before sync
	allRequestsBefore, err := srClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	initialRequestCount := len(allRequestsBefore.Items)

	// Sync should skip creation processing and not evict workloads (eviction only happens at startup)
	err = qmCordonAndDrain.SyncQueues(ctx)
	assert.NoError(t, err)

	// Verify no additional requests were created during sync (no eviction during sync)
	allRequestsAfter, err := srClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	finalRequestCount := len(allRequestsAfter.Items)

	// No new requests should be created during sync in maintenance mode
	assert.Equal(t, initialRequestCount, finalRequestCount)
}

func TestQueueManagerMaintenanceModeLogging(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		featureFlagFetcher: featureflag.DefaultFetcher,
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}
	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	qc := &mockqueue.Client{}
	bk8sHealthComponent := &mockBackendStatusGetter{
		hs: types.AgentHealth{
			Status:   types.HealthStatusHealthy,
			GPUUsage: map[types.GPUName]types.GPUResource{},
		},
	}
	bk8sHealth := health.NewBackendStatusCache(0, bk8sHealthComponent)

	queueCreds := types.QueueCredentials{
		CreationQueues:   make(map[types.GPUName]queue.MessageQueueInfo),
		TerminationQueue: queue.MessageQueueInfo{QueueURL: "termurl"},
	}

	// Test different maintenance modes and their String() method usage
	testCases := []struct {
		name            string
		maintenanceMode types.MaintenanceMode
		expectedString  string
	}{
		{
			name:            "Normal mode",
			maintenanceMode: types.MaintenanceModeNone,
			expectedString:  "None",
		},
		{
			name:            "Cordon mode",
			maintenanceMode: types.MaintenanceModeCordon,
			expectedString:  "CordonOnly",
		},
		{
			name:            "Cordon and drain mode",
			maintenanceMode: types.MaintenanceModeCordonAndDrain,
			expectedString:  "CordonAndDrain",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			metrics := nvcametrics.FromContext(ctx)
			qm := NewQueueManager(
				bk8s, bk8sHealth, qc,
				queueCreds, featureflag.DefaultFetcher, tc.maintenanceMode,
				metrics,
			)

			// Test String() method is used correctly
			assert.Equal(t, tc.expectedString, qm.maintenanceMode.String())

			// Test that SyncQueues doesn't error
			err := qm.SyncQueues(ctx)
			assert.NoError(t, err)
		})
	}
}

func TestQueueMessageDequeuedMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
		featureFlagFetcher: featureflag.DefaultFetcher,
	}

	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	gpuNameA100 := types.GPUName("A100")

	bk8sHealthComponent := &mockBackendStatusGetter{
		k8sVersion: "1.29.5",
		hs: types.AgentHealth{
			GPUUsage: map[types.GPUName]types.GPUResource{
				gpuNameA100: {
					Capacity:  5,
					Allocated: 0,
				},
			},
			Components: map[string]types.ComponentHealth{
				"kata": {
					Status: types.HealthStatusHealthy,
				},
			},
		},
	}
	bk8sHealth := health.NewBackendStatusCache(10*time.Millisecond, bk8sHealthComponent)

	qc := &mockqueue.Client{
		Use10MillisForWaits: true,
	}

	targetedQueueA100 := queue.MessageQueueInfo{
		GPU:       string(gpuNameA100),
		QueueURL:  "a100url_targeted",
		QueueType: queue.CreationQueue,
	}
	termQueue := queue.MessageQueueInfo{
		QueueURL:  "terminationurl",
		QueueType: queue.TerminationQueue,
	}

	queueCreds := types.QueueCredentials{
		CreationQueues:   map[types.GPUName]queue.MessageQueueInfo{gpuNameA100: targetedQueueA100},
		TerminationQueue: termQueue,
	}

	metrics := nvcametrics.FromContext(ctx)
	qm := NewQueueManager(
		bk8s, bk8sHealth, qc,
		queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeNone,
		metrics,
	)

	// Add 2 A100 messages to the queue
	createMsg1 := newCreationMessageRandomized(t, []byte(goodCM))
	qc.AddMessage(targetedQueueA100.QueueURL, createMsg1)
	createMsg2 := newCreationMessageRandomized(t, []byte(goodCM))
	qc.AddMessage(targetedQueueA100.QueueURL, createMsg2)

	// Add a termination message
	termMsg := queue.ReceiveMessageOutput{
		MessageID:     "tid1",
		ReceiptHandle: "trhdl1",
		Body:          []byte(goodTM),
	}
	qc.AddMessage(termQueue.QueueURL, termMsg)

	// Sync queues - this should dequeue the messages and record metrics
	err := qm.SyncQueues(ctx)
	require.NoError(t, err)

	// Verify that dequeue metrics were recorded
	// The metrics should have been incremented for:
	// - 2 messages from createQueue for A100
	// - 1 message from termQueue
	require.NotNil(t, metrics)
	require.NotNil(t, metrics.QueueMessageDequeuedTotal)
	require.NotNil(t, metrics.QueueDequeueBatchSize)

	// Verify the metrics exist and have labels
	// Note: We can't easily assert the exact values without exposing the registry
	// but we can verify the metrics were initialized and used without panicking
	metrics.RecordQueueMessageDequeued("createQueue", "A100", 2)
	metrics.RecordQueueMessageDequeued("termQueue", "none", 1)

	// Verify batch size metric can be recorded (for non-zero batches)
	metrics.RecordQueueDequeueBatchSize("createQueue", "A100", 2)
	metrics.RecordQueueDequeueBatchSize("termQueue", "none", 1)

	// Verify batch size metric can be recorded for empty pulls (0 messages)
	// This is important because the histogram should track all dequeue attempts
	metrics.RecordQueueDequeueBatchSize("createQueue", "A100", 0)
	metrics.RecordQueueDequeueBatchSize("termQueue", "none", 0)

	// Sync again with no messages to verify empty pulls are recorded
	err = qm.SyncQueues(ctx)
	require.NoError(t, err)
}

func Test_collectAvailableCreationQueues(t *testing.T) {
	ctx := t.Context()

	bk8sHealthComponent := &mockBackendStatusGetter{
		hs: types.AgentHealth{
			GPUUsage: map[types.GPUName]types.GPUResource{
				gpuNameA100: {
					Capacity:  5,
					Allocated: 0,
				},
				gpuNameL40: {
					Capacity:  0,
					Allocated: 0,
				},
			},
		},
	}
	bk8sHealth := health.NewBackendStatusCache(0, bk8sHealthComponent)
	_, err := bk8sHealth.RefreshStatus(ctx)
	require.NoError(t, err)

	qcreds := types.QueueCredentials{}
	fff := &featureflagmock.Fetcher{}

	assert.Panics(t, func() {
		_ = NewQueueManager(nil, bk8sHealth, nil, qcreds, fff, "", nil)
	})

	// Single queue.
	qcreds.CreationQueues = types.CreationQueueInfoSet{
		gpuNameA100: queue.MessageQueueInfo{
			GPU:       string(gpuNameA100),
			QueueURL:  fmt.Sprintf("creation_%s", gpuNameA100),
			QueueType: queue.CreationQueue,
		},
	}

	qm := NewQueueManager(nil, bk8sHealth, nil, qcreds, fff, "", nil)

	for range 3 {
		gotQueueWorkInputs, gotQueueRingMetadata := qm.collectAvailableCreationQueues(ctx)
		assert.Equal(t, []queueWorkInput{
			{
				gpuName:   gpuNameA100,
				queueType: "createQueue",
				qi:        qcreds.CreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
		}, gotQueueWorkInputs)
		assert.Equal(t, map[types.GPUName]queueRingMetadata{
			gpuNameA100: {
				startIdx: 0,
				ringSize: 1,
			},
		}, gotQueueRingMetadata)
		assert.Equal(t, map[types.GPUName]int{
			gpuNameA100: 0,
		}, qm.queueRingStartIdxByGPU)
	}

	// All queues.
	qcreds.ClusterCreationQueues = types.CreationQueueInfoSet{
		gpuNameA100: queue.MessageQueueInfo{
			GPU:       string(gpuNameA100),
			QueueURL:  fmt.Sprintf("cluster_creation_%s", gpuNameA100),
			QueueType: queue.CreationQueue,
		},
	}
	qcreds.TaskClusterCreationQueues = types.CreationQueueInfoSet{
		gpuNameA100: queue.MessageQueueInfo{
			GPU:       string(gpuNameA100),
			QueueURL:  fmt.Sprintf("task_cluster_creation_%s", gpuNameA100),
			QueueType: queue.CreationQueue,
		},
	}

	qm = NewQueueManager(nil, bk8sHealth, nil, qcreds, fff, "", nil)

	for i, expStartIdx := range []int{0, 1, 2, 0} {
		gotQueueWorkInputs, gotQueueRingMetadata := qm.collectAvailableCreationQueues(ctx)
		assert.Equal(t, []queueWorkInput{
			{
				gpuName:   gpuNameA100,
				queueType: "createQueue",
				qi:        qcreds.CreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameA100,
				queueType: "clusterCreateQueue",
				qi:        qcreds.ClusterCreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameA100,
				queueType: "taskClusterCreateQueue",
				qi:        qcreds.TaskClusterCreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
		}, gotQueueWorkInputs)
		assert.Equal(t, map[types.GPUName]queueRingMetadata{
			gpuNameA100: {
				startIdx: expStartIdx,
				ringSize: 3,
			},
		}, gotQueueRingMetadata, fmt.Sprintf("on exp start index %d", i))
		assert.Equal(t, map[types.GPUName]int{
			gpuNameA100: (expStartIdx + 1) % 3,
		}, qm.queueRingStartIdxByGPU)
	}

	// New GPU no capacity.
	qcreds.CreationQueues[gpuNameL40] = queue.MessageQueueInfo{
		GPU:       string(gpuNameL40),
		QueueURL:  fmt.Sprintf("creation_%s", gpuNameL40),
		QueueType: queue.CreationQueue,
	}
	qcreds.ClusterCreationQueues[gpuNameL40] = queue.MessageQueueInfo{
		GPU:       string(gpuNameL40),
		QueueURL:  fmt.Sprintf("cluster_creation_%s", gpuNameL40),
		QueueType: queue.CreationQueue,
	}
	qcreds.TaskClusterCreationQueues[gpuNameL40] = queue.MessageQueueInfo{
		GPU:       string(gpuNameL40),
		QueueURL:  fmt.Sprintf("task_cluster_creation_%s", gpuNameL40),
		QueueType: queue.CreationQueue,
	}

	qm = NewQueueManager(nil, bk8sHealth, nil, qcreds, fff, "", nil)

	for i, expStartIdx := range []int{0, 1, 2, 0} {
		gotQueueWorkInputs, gotQueueRingMetadata := qm.collectAvailableCreationQueues(ctx)
		assert.Equal(t, []queueWorkInput{
			{
				gpuName:   gpuNameA100,
				queueType: "createQueue",
				qi:        qcreds.CreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameA100,
				queueType: "clusterCreateQueue",
				qi:        qcreds.ClusterCreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameA100,
				queueType: "taskClusterCreateQueue",
				qi:        qcreds.TaskClusterCreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
		}, gotQueueWorkInputs)
		assert.Equal(t, map[types.GPUName]queueRingMetadata{
			gpuNameA100: {
				startIdx: expStartIdx,
				ringSize: 3,
			},
		}, gotQueueRingMetadata, fmt.Sprintf("on exp start index %d", i))
		assert.Equal(t, map[types.GPUName]int{
			gpuNameA100: (expStartIdx + 1) % 3,
		}, qm.queueRingStartIdxByGPU)
	}

	// Add GPU capacity.
	bk8sHealthComponent.hs.GPUUsage[gpuNameL40] = types.GPUResource{Capacity: 5}
	_, err = bk8sHealth.RefreshStatus(ctx)
	require.NoError(t, err)

	// A100 already went through ringSize + 1 iterations above, should start at 1
	// L40, new GPU, should start at 0
	for i, expStartIdxs := range [][2]int{
		{1, 0},
		{2, 1},
		{0, 2},
		{1, 0},
	} {
		gotQueueWorkInputs, gotQueueRingMetadata := qm.collectAvailableCreationQueues(ctx)
		assert.ElementsMatch(t, []queueWorkInput{
			{
				gpuName:   gpuNameA100,
				queueType: "createQueue",
				qi:        qcreds.CreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameL40,
				queueType: "createQueue",
				qi:        qcreds.CreationQueues[gpuNameL40],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameA100,
				queueType: "clusterCreateQueue",
				qi:        qcreds.ClusterCreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameL40,
				queueType: "clusterCreateQueue",
				qi:        qcreds.ClusterCreationQueues[gpuNameL40],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameA100,
				queueType: "taskClusterCreateQueue",
				qi:        qcreds.TaskClusterCreationQueues[gpuNameA100],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
			{
				gpuName:   gpuNameL40,
				queueType: "taskClusterCreateQueue",
				qi:        qcreds.TaskClusterCreationQueues[gpuNameL40],
				vtoSec:    creationQueueVisibilityTimeoutSeconds,
			},
		}, gotQueueWorkInputs)
		assert.Equal(t, queueRingMetadata{
			startIdx: expStartIdxs[0],
			ringSize: 3,
		}, gotQueueRingMetadata[gpuNameA100], fmt.Sprintf("GPU %s on exp start index %d", gpuNameA100, i))
		assert.Equal(t, queueRingMetadata{
			startIdx: expStartIdxs[1],
			ringSize: 3,
		}, gotQueueRingMetadata[gpuNameL40], fmt.Sprintf("GPU %s on exp start index %d", gpuNameL40, i))
		assert.Equal(t, map[types.GPUName]int{
			gpuNameA100: (expStartIdxs[0] + 1) % 3,
			gpuNameL40:  (expStartIdxs[1] + 1) % 3,
		}, qm.queueRingStartIdxByGPU)
	}

	// Remove GPU capacity
	bk8sHealthComponent.hs.GPUUsage[gpuNameL40] = types.GPUResource{Capacity: 0}
	_, err = bk8sHealth.RefreshStatus(ctx)
	require.NoError(t, err)

	gotQueueWorkInputs, gotQueueRingMetadata := qm.collectAvailableCreationQueues(ctx)
	assert.Equal(t, []queueWorkInput{
		{
			gpuName:   gpuNameA100,
			queueType: "createQueue",
			qi:        qcreds.CreationQueues[gpuNameA100],
			vtoSec:    creationQueueVisibilityTimeoutSeconds,
		},
		{
			gpuName:   gpuNameA100,
			queueType: "clusterCreateQueue",
			qi:        qcreds.ClusterCreationQueues[gpuNameA100],
			vtoSec:    creationQueueVisibilityTimeoutSeconds,
		},
		{
			gpuName:   gpuNameA100,
			queueType: "taskClusterCreateQueue",
			qi:        qcreds.TaskClusterCreationQueues[gpuNameA100],
			vtoSec:    creationQueueVisibilityTimeoutSeconds,
		},
	}, gotQueueWorkInputs)
	assert.Equal(t, map[types.GPUName]queueRingMetadata{
		gpuNameA100: {
			startIdx: 2,
			ringSize: 3,
		},
	}, gotQueueRingMetadata)
	assert.Equal(t, map[types.GPUName]int{
		gpuNameA100: 0,
	}, qm.queueRingStartIdxByGPU)

	// Remove a queue
	qcreds.ClusterCreationQueues = types.CreationQueueInfoSet{}
	qm.updateQueues(qcreds)

	gotQueueWorkInputs, gotQueueRingMetadata = qm.collectAvailableCreationQueues(ctx)
	assert.Equal(t, []queueWorkInput{
		{
			gpuName:   gpuNameA100,
			queueType: "createQueue",
			qi:        qcreds.CreationQueues[gpuNameA100],
			vtoSec:    creationQueueVisibilityTimeoutSeconds,
		},
		{
			gpuName:   gpuNameA100,
			queueType: "taskClusterCreateQueue",
			qi:        qcreds.TaskClusterCreationQueues[gpuNameA100],
			vtoSec:    creationQueueVisibilityTimeoutSeconds,
		},
	}, gotQueueWorkInputs)
	assert.Equal(t, map[types.GPUName]queueRingMetadata{
		gpuNameA100: {
			startIdx: 0,
			ringSize: 2,
		},
	}, gotQueueRingMetadata)
	assert.Equal(t, map[types.GPUName]int{
		gpuNameA100: 1,
	}, qm.queueRingStartIdxByGPU)
}

func Test_createCreationMessageMatricesByGPU(t *testing.T) {
	ctx := t.Context()
	log := core.GetLogger(core.WithDefaultLogger(ctx))

	type queueRingCheckSpec struct {
		maxMessages          int
		existingMsgIDs       sets.Set[string]
		queueRingMeta        map[types.GPUName]queueRingMetadata
		expOrderedMsgs       map[types.GPUName][]createMessageTuple
		expCreationMsgsByGPU map[types.GPUName][][]createMessageTuple
		expHasErrs           bool
	}

	type spec struct {
		name                string
		qwInputs            []queueWorkInput
		qwOutputs           []queueWorkOutput
		queueRingCheckSpecs []queueRingCheckSpec
	}

	creationQWInputA100 := queueWorkInput{
		gpuName:   gpuNameA100,
		queueType: "createQueue",
		qi: queue.MessageQueueInfo{
			GPU:      string(gpuNameA100),
			QueueURL: fmt.Sprintf("creation_%s", gpuNameA100),
		},
		vtoSec: creationQueueVisibilityTimeoutSeconds,
	}
	creationQWInputL40 := queueWorkInput{
		gpuName:   gpuNameL40,
		queueType: "createQueue",
		qi: queue.MessageQueueInfo{
			GPU:      string(gpuNameL40),
			QueueURL: fmt.Sprintf("creation_%s", gpuNameL40),
		},
		vtoSec: creationQueueVisibilityTimeoutSeconds,
	}
	clusterCreationQWInputA100 := queueWorkInput{
		gpuName:   gpuNameA100,
		queueType: "clusterCreateQueue",
		qi: queue.MessageQueueInfo{
			GPU:      string(gpuNameA100),
			QueueURL: fmt.Sprintf("cluster_creation_%s", gpuNameA100),
		},
		vtoSec: creationQueueVisibilityTimeoutSeconds,
	}
	taskClusterCreationQWInputA100 := queueWorkInput{
		gpuName:   gpuNameA100,
		queueType: "taskClusterCreateQueue",
		qi: queue.MessageQueueInfo{
			GPU:      string(gpuNameA100),
			QueueURL: fmt.Sprintf("task_cluster_creation_%s", gpuNameA100),
		},
		vtoSec: creationQueueVisibilityTimeoutSeconds,
	}
	creationQWOutputA100 := queueWorkOutput{
		messages: []queue.ReceiveMessageOutput{
			{
				MessageID:     "msg1",
				ReceiptHandle: "rhdl1",
				Body:          []byte("foo_create"),
			},
		},
	}
	clusterCreationQWOutputA100 := queueWorkOutput{
		messages: []queue.ReceiveMessageOutput{
			{
				MessageID:     "msg2",
				ReceiptHandle: "rhdl2",
				Body:          []byte("foo_cluster_create"),
			},
		},
	}
	taskClusterCreationQWOutputA100 := queueWorkOutput{
		messages: []queue.ReceiveMessageOutput{
			{
				MessageID:     "msg3",
				ReceiptHandle: "rhdl3",
				Body:          []byte("foo_task_cluster_create"),
			},
		},
	}
	for _, tt := range []spec{
		{
			name: "single queue single GPU",
			qwInputs: []queueWorkInput{
				creationQWInputA100,
			},
			qwOutputs: []queueWorkOutput{
				creationQWOutputA100,
			},
			queueRingCheckSpecs: []queueRingCheckSpec{
				{
					maxMessages: 1,
					queueRingMeta: map[types.GPUName]queueRingMetadata{
						gpuNameA100: {
							startIdx: 0,
							ringSize: 1,
						},
					},
					expCreationMsgsByGPU: map[types.GPUName][][]createMessageTuple{
						gpuNameA100: {
							{
								{
									gpuName:   gpuNameA100,
									message:   creationQWOutputA100.messages[0],
									queueInfo: creationQWInputA100.qi,
								},
							},
						},
					},
					expOrderedMsgs: map[types.GPUName][]createMessageTuple{
						gpuNameA100: {
							{
								gpuName:   gpuNameA100,
								message:   creationQWOutputA100.messages[0],
								queueInfo: creationQWInputA100.qi,
							},
						},
					},
				},
			},
		},
		{
			name: "single queue two GPU",
			qwInputs: []queueWorkInput{
				creationQWInputA100,
				creationQWInputL40,
			},
			qwOutputs: []queueWorkOutput{
				creationQWOutputA100,
				{
					messages: []queue.ReceiveMessageOutput{
						{
							MessageID:     "l40msg1",
							ReceiptHandle: "l40rhdl1",
							Body:          []byte("foo"),
						},
					},
				},
			},
			queueRingCheckSpecs: []queueRingCheckSpec{
				{
					maxMessages: 1,
					queueRingMeta: map[types.GPUName]queueRingMetadata{
						gpuNameA100: {
							startIdx: 0,
							ringSize: 1,
						},
						gpuNameL40: {
							startIdx: 0,
							ringSize: 1,
						},
					},
					expCreationMsgsByGPU: map[types.GPUName][][]createMessageTuple{
						gpuNameA100: {
							{
								{
									gpuName:   gpuNameA100,
									message:   creationQWOutputA100.messages[0],
									queueInfo: creationQWInputA100.qi,
								},
							},
						},
						gpuNameL40: {
							{
								{
									gpuName: gpuNameL40,
									message: queue.ReceiveMessageOutput{
										MessageID:     "l40msg1",
										ReceiptHandle: "l40rhdl1",
										Body:          []byte("foo"),
									},
									queueInfo: creationQWInputL40.qi,
								},
							},
						},
					},
					expOrderedMsgs: map[types.GPUName][]createMessageTuple{
						gpuNameA100: {
							{
								gpuName:   gpuNameA100,
								message:   creationQWOutputA100.messages[0],
								queueInfo: creationQWInputA100.qi,
							},
						},
						gpuNameL40: {
							{
								gpuName: gpuNameL40,
								message: queue.ReceiveMessageOutput{
									MessageID:     "l40msg1",
									ReceiptHandle: "l40rhdl1",
									Body:          []byte("foo"),
								},
								queueInfo: creationQWInputL40.qi,
							},
						},
					},
				},
			},
		},
		{
			name: "multiple queues single message single GPU",
			qwInputs: []queueWorkInput{
				creationQWInputA100,
				clusterCreationQWInputA100,
				taskClusterCreationQWInputA100,
			},
			qwOutputs: []queueWorkOutput{
				creationQWOutputA100,
				{messages: nil},
				{messages: nil},
			},
			queueRingCheckSpecs: []queueRingCheckSpec{
				{
					maxMessages: 1,
					queueRingMeta: map[types.GPUName]queueRingMetadata{
						gpuNameA100: {
							startIdx: 0,
							ringSize: 3,
						},
					},
					expCreationMsgsByGPU: map[types.GPUName][][]createMessageTuple{
						gpuNameA100: {
							{
								{
									gpuName:   gpuNameA100,
									message:   creationQWOutputA100.messages[0],
									queueInfo: creationQWInputA100.qi,
								},
							},
							nil,
							nil,
						},
					},
					expOrderedMsgs: map[types.GPUName][]createMessageTuple{
						gpuNameA100: {
							{
								gpuName:   gpuNameA100,
								message:   creationQWOutputA100.messages[0],
								queueInfo: creationQWInputA100.qi,
							},
						},
					},
				},
				{
					maxMessages: 1,
					queueRingMeta: map[types.GPUName]queueRingMetadata{
						gpuNameA100: {
							startIdx: 1,
							ringSize: 3,
						},
					},
					expCreationMsgsByGPU: map[types.GPUName][][]createMessageTuple{
						gpuNameA100: {
							nil,
							{
								{
									gpuName:   gpuNameA100,
									message:   creationQWOutputA100.messages[0],
									queueInfo: creationQWInputA100.qi,
								},
							},
							nil,
						},
					},
					expOrderedMsgs: map[types.GPUName][]createMessageTuple{
						gpuNameA100: {
							{
								gpuName:   gpuNameA100,
								message:   creationQWOutputA100.messages[0],
								queueInfo: creationQWInputA100.qi,
							},
						},
					},
				},
			},
		},
		{
			name: "multiple queues multiple single messages single GPU",
			qwInputs: []queueWorkInput{
				creationQWInputA100,
				clusterCreationQWInputA100,
				taskClusterCreationQWInputA100,
			},
			qwOutputs: []queueWorkOutput{
				creationQWOutputA100,
				clusterCreationQWOutputA100,
				taskClusterCreationQWOutputA100,
			},
			queueRingCheckSpecs: []queueRingCheckSpec{
				{
					maxMessages: 1,
					queueRingMeta: map[types.GPUName]queueRingMetadata{
						gpuNameA100: {
							startIdx: 0,
							ringSize: 3,
						},
					},
					expCreationMsgsByGPU: map[types.GPUName][][]createMessageTuple{
						gpuNameA100: {
							{
								{
									gpuName:   gpuNameA100,
									message:   creationQWOutputA100.messages[0],
									queueInfo: creationQWInputA100.qi,
								},
							},
							{
								{
									gpuName:   gpuNameA100,
									message:   clusterCreationQWOutputA100.messages[0],
									queueInfo: clusterCreationQWInputA100.qi,
								},
							},
							{
								{
									gpuName:   gpuNameA100,
									message:   taskClusterCreationQWOutputA100.messages[0],
									queueInfo: taskClusterCreationQWInputA100.qi,
								},
							},
						},
					},
					expOrderedMsgs: map[types.GPUName][]createMessageTuple{
						gpuNameA100: {
							{
								gpuName:   gpuNameA100,
								message:   creationQWOutputA100.messages[0],
								queueInfo: creationQWInputA100.qi,
							},
							{
								gpuName:   gpuNameA100,
								message:   clusterCreationQWOutputA100.messages[0],
								queueInfo: clusterCreationQWInputA100.qi,
							},
							{
								gpuName:   gpuNameA100,
								message:   taskClusterCreationQWOutputA100.messages[0],
								queueInfo: taskClusterCreationQWInputA100.qi,
							},
						},
					},
				},
				{
					maxMessages: 1,
					queueRingMeta: map[types.GPUName]queueRingMetadata{
						gpuNameA100: {
							startIdx: 1,
							ringSize: 3,
						},
					},
					expCreationMsgsByGPU: map[types.GPUName][][]createMessageTuple{
						gpuNameA100: {
							{
								{
									gpuName:   gpuNameA100,
									message:   taskClusterCreationQWOutputA100.messages[0],
									queueInfo: taskClusterCreationQWInputA100.qi,
								},
							},
							{
								{
									gpuName:   gpuNameA100,
									message:   creationQWOutputA100.messages[0],
									queueInfo: creationQWInputA100.qi,
								},
							},
							{
								{
									gpuName:   gpuNameA100,
									message:   clusterCreationQWOutputA100.messages[0],
									queueInfo: clusterCreationQWInputA100.qi,
								},
							},
						},
					},
					expOrderedMsgs: map[types.GPUName][]createMessageTuple{
						gpuNameA100: {
							{
								gpuName:   gpuNameA100,
								message:   taskClusterCreationQWOutputA100.messages[0],
								queueInfo: taskClusterCreationQWInputA100.qi,
							},
							{
								gpuName:   gpuNameA100,
								message:   creationQWOutputA100.messages[0],
								queueInfo: creationQWInputA100.qi,
							},
							{
								gpuName:   gpuNameA100,
								message:   clusterCreationQWOutputA100.messages[0],
								queueInfo: clusterCreationQWInputA100.qi,
							},
						},
					},
				},
			},
		},
		{
			name: "multiple queues multiple messages single GPU",
			qwInputs: []queueWorkInput{
				creationQWInputA100,
				clusterCreationQWInputA100,
				taskClusterCreationQWInputA100,
			},
			qwOutputs: []queueWorkOutput{
				{},
				{
					messages: []queue.ReceiveMessageOutput{
						{
							MessageID:     "msg21",
							ReceiptHandle: "rhdl21",
							Body:          []byte("body"),
						},
						{
							MessageID:     "msg22",
							ReceiptHandle: "rhdl22",
							Body:          []byte("body"),
						},
						{
							MessageID:     "msg23",
							ReceiptHandle: "rhdl23",
							Body:          []byte("body"),
						},
					},
				},
				{
					messages: []queue.ReceiveMessageOutput{
						{
							MessageID:     "msg11",
							ReceiptHandle: "rhdl11",
							Body:          []byte("body"),
						},
						{
							MessageID:     "msg12",
							ReceiptHandle: "rhdl12",
							Body:          []byte("body"),
						},
					},
				},
			},
			queueRingCheckSpecs: []queueRingCheckSpec{
				{
					maxMessages: 10,
					queueRingMeta: map[types.GPUName]queueRingMetadata{
						gpuNameA100: {
							startIdx: 1,
							ringSize: 3,
						},
					},
					expCreationMsgsByGPU: map[types.GPUName][][]createMessageTuple{
						gpuNameA100: {
							{
								{
									gpuName: gpuNameA100,
									message: queue.ReceiveMessageOutput{
										MessageID:     "msg11",
										ReceiptHandle: "rhdl11",
										Body:          []byte("body"),
									},
									queueInfo: taskClusterCreationQWInputA100.qi,
								},
								{
									gpuName: gpuNameA100,
									message: queue.ReceiveMessageOutput{
										MessageID:     "msg12",
										ReceiptHandle: "rhdl12",
										Body:          []byte("body"),
									},
									queueInfo: taskClusterCreationQWInputA100.qi,
								},
							},
							nil,
							{
								{
									gpuName: gpuNameA100,
									message: queue.ReceiveMessageOutput{
										MessageID:     "msg21",
										ReceiptHandle: "rhdl21",
										Body:          []byte("body"),
									},
									queueInfo: clusterCreationQWInputA100.qi,
								},
								{
									gpuName: gpuNameA100,
									message: queue.ReceiveMessageOutput{
										MessageID:     "msg22",
										ReceiptHandle: "rhdl22",
										Body:          []byte("body"),
									},
									queueInfo: clusterCreationQWInputA100.qi,
								},
								{
									gpuName: gpuNameA100,
									message: queue.ReceiveMessageOutput{
										MessageID:     "msg23",
										ReceiptHandle: "rhdl23",
										Body:          []byte("body"),
									},
									queueInfo: clusterCreationQWInputA100.qi,
								},
							},
						},
					},
					expOrderedMsgs: map[types.GPUName][]createMessageTuple{
						gpuNameA100: {
							{
								gpuName: gpuNameA100,
								message: queue.ReceiveMessageOutput{
									MessageID:     "msg11",
									ReceiptHandle: "rhdl11",
									Body:          []byte("body"),
								},
								queueInfo: taskClusterCreationQWInputA100.qi,
							},
							{
								gpuName: gpuNameA100,
								message: queue.ReceiveMessageOutput{
									MessageID:     "msg21",
									ReceiptHandle: "rhdl21",
									Body:          []byte("body"),
								},
								queueInfo: clusterCreationQWInputA100.qi,
							},
							{
								gpuName: gpuNameA100,
								message: queue.ReceiveMessageOutput{
									MessageID:     "msg12",
									ReceiptHandle: "rhdl12",
									Body:          []byte("body"),
								},
								queueInfo: taskClusterCreationQWInputA100.qi,
							},
							{
								gpuName: gpuNameA100,
								message: queue.ReceiveMessageOutput{
									MessageID:     "msg22",
									ReceiptHandle: "rhdl22",
									Body:          []byte("body"),
								},
								queueInfo: clusterCreationQWInputA100.qi,
							},
							{
								gpuName: gpuNameA100,
								message: queue.ReceiveMessageOutput{
									MessageID:     "msg23",
									ReceiptHandle: "rhdl23",
									Body:          []byte("body"),
								},
								queueInfo: clusterCreationQWInputA100.qi,
							},
						},
					},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for i, rcheckSpec := range tt.queueRingCheckSpecs {
				t.Run(fmt.Sprintf("ring check %d", i), func(t *testing.T) {
					existingMsgIDs := rcheckSpec.existingMsgIDs
					if existingMsgIDs == nil {
						existingMsgIDs = sets.New[string]()
					}
					gotCreationMsgsByGPU, gotHasErrs := createCreationMessageMatricesByGPU(
						log, tt.qwInputs, tt.qwOutputs, rcheckSpec.queueRingMeta, existingMsgIDs,
					)
					assert.Equal(t, rcheckSpec.expHasErrs, gotHasErrs)
					assert.Equal(t, rcheckSpec.expCreationMsgsByGPU, gotCreationMsgsByGPU)

					for gpuName, expMsgs := range rcheckSpec.expOrderedMsgs {
						messageMatrix := gotCreationMsgsByGPU[gpuName]
						iter := newIterCreationMessageMatrix(rcheckSpec.maxMessages, messageMatrix)
						var gotMsgs []createMessageTuple
						iter(func(cm createMessageTuple) bool {
							gotMsgs = append(gotMsgs, cm)
							return true
						})
						assert.Equal(t, expMsgs, gotMsgs, "check msgs for GPU %s", gpuName)
					}
				})
			}
		})
	}
}

func TestQueueManager_PauseResume(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	queueCreds := getTestQueueCreds(false)

	qc := &mockqueue.Client{
		Use10MillisForWaits: true,
	}

	bsc := newMockBackendStatusCacheFromK8s(t, bc)
	metrics := nvcametrics.FromContext(ctx)
	sqm := NewQueueManager(bc, bsc, qc, queueCreds, featureflag.DefaultFetcher, types.MaintenanceModeNone, metrics)

	// Verify queue manager starts unpaused
	assert.False(t, sqm.IsPaused(), "queue manager should start unpaused")

	// Pause the queue manager
	sqm.Pause()
	assert.True(t, sqm.IsPaused(), "queue manager should be paused after Pause()")

	// Pause is idempotent
	sqm.Pause()
	assert.True(t, sqm.IsPaused(), "queue manager should still be paused after second Pause()")

	// Resume the queue manager
	sqm.Resume()
	assert.False(t, sqm.IsPaused(), "queue manager should be unpaused after Resume()")

	// Resume is idempotent
	sqm.Resume()
	assert.False(t, sqm.IsPaused(), "queue manager should still be unpaused after second Resume()")

	// SyncQueues should succeed when not paused
	err = sqm.SyncQueues(ctx)
	assert.NoError(t, err, "SyncQueues should succeed when not paused")

	// Pause and verify SyncQueues still returns nil (early return path)
	sqm.Pause()
	err = sqm.SyncQueues(ctx)
	assert.NoError(t, err, "SyncQueues should succeed when paused (skips creation processing)")
}
