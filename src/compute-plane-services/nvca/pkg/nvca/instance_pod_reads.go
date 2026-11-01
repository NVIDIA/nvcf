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
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcainternaltranslate "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/translate"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// instancePodRefreshWindow is how often the instance status loop reads a Pod instance live that it
	// otherwise serves from the informer cache: once in each window, on its first visit after the
	// instance's refresh slot. While the loop revisits the instance within
	// instancePodLiveReadRetention, that bounds how long a watch that stalls without an error can hide
	// a change to less than one window plus the time between visits, whether or not periodic instance
	// status reporting is enabled.
	instancePodRefreshWindow = 2 * time.Minute
	// instancePodLiveReadRetention is how long the refresh tracker remembers an instance the status
	// loop has stopped visiting. An instance revisited within it keeps its last live read and is
	// refreshed on that visit when a refresh slot has passed since then. An instance not visited for
	// that long, as when the loop pauses or its passes run that far apart, can be forgotten, and then
	// starts a new window on its next visit, so an instance the loop keeps revisiting less often than
	// the retention can go without a refresh for as long as that lasts. Its periodic instance status
	// report still reads it live when due, but with periodic reporting disabled a watch that stalls
	// without an error can hide a change to it until the loop revisits it within the retention again
	// after a refresh slot.
	instancePodLiveReadRetention = 3 * instancePodRefreshWindow
)

// instancePodDecision is what the instance status loop concludes from one read of a Pod instance.
type instancePodDecision struct {
	is                 types.ICMSInstanceState
	reason             string
	isBadState         bool
	needsPurge         bool
	gracePeriodSeconds *int64
}

// reportedState is the instance state reported to ICMS for this decision.
func (d instancePodDecision) reportedState() types.ICMSInstanceState {
	if d.needsPurge {
		return types.ICMSInstanceTerminated
	}
	return d.is
}

// needsLiveConfirm reports whether acting on this decision would purge the Pod or report a state ICMS
// has not been told, which a decision taken from the informer cache may only do after a live read.
func (d instancePodDecision) needsLiveConfirm(st nvcav2beta1.InstanceStatus) bool {
	return d.needsPurge || st.LastReportedTimestamp == nil ||
		!strings.EqualFold(string(d.reportedState()), st.LastReportedStatus)
}

func isTaskCreationAction(action common.MessageAction) bool {
	return action == common.RequestICMSInstancesForTask || action == common.TaskCreationAction
}

// readInstancePod returns the Pod backing the Pod instance st, the decision taken from it, and
// whether the Pod was read live in this call.
//
// With InstancePodCacheReads enabled, it reads the Pod from the informer cache, and reads it live
// instead when the cache does not have it, when the cached Pod would purge the instance or report a
// state ICMS has not been told, when the periodic report of the instance's state is due, or when the
// instance's periodic refresh is due. The Pod of an instance reported terminated is expected to be
// gone, so when the cache does not have it, it is read live only when the instance's refresh or the
// periodic report of its state is due. A Pod returned from the cache is the informer's own object
// and must not be modified or reported. Otherwise every read is live.
//
// A Pod that is being deleted gets the zero decision. A NotFound error comes from a live read, or
// from the cache for an instance reported terminated that is not due a live read. Any other error
// leaves the instance's state unknown for this pass.
func (c K8sComputeBackend) readInstancePod(ctx context.Context, st nvcav2beta1.InstanceStatus,
	req *nvcav2beta1.ICMSRequest,
) (*corev1.Pod, instancePodDecision, bool, error) {
	if c.bk8s.podSpecLister == nil || !c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.InstancePodCacheReads) {
		p, err := c.getInstancePodLive(ctx, st.ID)
		if err != nil {
			return nil, instancePodDecision{}, true, err
		}
		d, err := c.decideInstancePod(ctx, p, req)
		return p, d, true, err
	}

	p, err := c.bk8s.podSpecLister.Get(st.ID)
	nvcametrics.FromContext(ctx).RecordInstancePodRead(nvcametrics.InstancePodReadSourceCache, err)
	if apierrors.IsNotFound(err) {
		if strings.EqualFold(st.LastReportedStatus, string(types.ICMSInstanceTerminated)) {
			refreshDue := c.bk8s.instancePodLiveReads.refreshDue(st.ID, core.GetCurrentTime(ctx))
			if !refreshDue && !c.bk8s.shouldReportInstanceStatusHeartbeat(ctx, req, st.ID,
				string(types.ICMSInstanceTerminated), st.LastReportedStatus, st.LastReportedTimestamp) {
				return nil, instancePodDecision{}, false, err
			}
		}
		// A Pod is created before its instance is recorded, so the cache can still miss a new Pod.
		live, d, err := c.readInstancePodLive(ctx, st.ID, req, nvcametrics.InstancePodReadSourceMiss)
		return live, d, true, err
	}
	if err != nil {
		return nil, instancePodDecision{}, false, err
	}
	d, err := c.decideInstancePod(ctx, p, req)
	if err != nil {
		return nil, instancePodDecision{}, false, err
	}

	now := core.GetCurrentTime(ctx)
	refreshDue := c.bk8s.instancePodLiveReads.refreshDue(st.ID, now)
	var source string
	switch {
	case p.DeletionTimestamp == nil && d.needsLiveConfirm(st):
		source = nvcametrics.InstancePodReadSourceConfirm
	case p.DeletionTimestamp == nil && c.bk8s.instanceStatusHeartbeatDue(st.LastReportedTimestamp, now):
		source = nvcametrics.InstancePodReadSourceHeartbeat
	case refreshDue:
		source = nvcametrics.InstancePodReadSourceRefresh
	default:
		return p, d, false, nil
	}
	live, liveDecision, err := c.readInstancePodLive(ctx, st.ID, req, source)
	if apierrors.IsNotFound(err) || (err == nil && liveDecision.reportedState() != d.reportedState()) {
		log := core.GetLogger(ctx).WithFields(logrus.Fields{
			"instanceID":            st.ID,
			"podRead":               source,
			"cachedState":           d.reportedState(),
			"cachedResourceVersion": p.ResourceVersion,
		})
		if err != nil {
			log = log.WithError(err)
		} else {
			log = log.WithFields(logrus.Fields{
				"liveState":           liveDecision.reportedState(),
				"liveResourceVersion": live.ResourceVersion,
			})
		}
		switch source {
		case nvcametrics.InstancePodReadSourceHeartbeat, nvcametrics.InstancePodReadSourceRefresh:
			log.Warn("Periodic live read found a Pod instance change missing from the informer cache")
		default:
			log.Debug("Live read changed the decision taken from the informer cache")
		}
	}
	return live, liveDecision, true, err
}

// readInstancePodLive reads the Pod from the API server for the given read source, and decides from
// the live Pod. A read that finds the Pod, or finds it gone, counts as the instance's last live read
// for its refresh. A failed read does not.
func (c K8sComputeBackend) readInstancePodLive(ctx context.Context, id string, req *nvcav2beta1.ICMSRequest,
	source string,
) (*corev1.Pod, instancePodDecision, error) {
	p, err := c.getInstancePodLive(ctx, id)
	nvcametrics.FromContext(ctx).RecordInstancePodRead(source, err)
	if err == nil || apierrors.IsNotFound(err) {
		c.bk8s.instancePodLiveReads.recordLive(id, core.GetCurrentTime(ctx))
	}
	if err != nil {
		return nil, instancePodDecision{}, err
	}
	d, err := c.decideInstancePod(ctx, p, req)
	return p, d, err
}

func (c K8sComputeBackend) getInstancePodLive(ctx context.Context, id string) (*corev1.Pod, error) {
	p, err := c.clients.K8s.CoreV1().Pods(c.bk8s.podInstanceNamespace).Get(ctx, id, metav1.GetOptions{})
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall("pod", err)
	}
	return p, err
}

// decideInstancePod classifies the Pod and decides whether to purge it. It only reads p, so p can
// be a cached Pod. A Pod that is being deleted gets the zero decision.
func (c K8sComputeBackend) decideInstancePod(ctx context.Context, p *corev1.Pod,
	req *nvcav2beta1.ICMSRequest,
) (instancePodDecision, error) {
	var d instancePodDecision
	if p.DeletionTimestamp != nil {
		return d, nil
	}
	d.is, d.reason = podPhaseToInstanceState(p, c.bk8s.k8sTimeConfig)
	_, d.isBadState = badICMSInstanceStates[d.is]
	switch {
	case isTaskCreationAction(req.Spec.Action):
		log := core.GetLogger(ctx).WithField("instanceID", p.Name)
		maxRuntimeDurationStr := req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxRuntimeDuration
		maxQueuedDurationStr := req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxQueuedDuration
		// Wait for the utils container to exit, or for a max duration to have passed.
		mrd, err := nvcainternaltranslate.ParseMaxRuntimeDuration(maxRuntimeDurationStr)
		if err != nil {
			log.WithError(err).WithField("maxRuntimeDurationStr", maxRuntimeDurationStr).
				Error("Error parsing max runtime duration for container task")
			return d, nvcaerrors.TerminalError(fmt.Errorf("parse max runtime duration: %w", err))
		}
		mqd, err := translateutil.ParseISO8601Duration(maxQueuedDurationStr)
		if err != nil {
			log.WithError(err).WithField("maxQueuedDurationStr", maxQueuedDurationStr).
				Error("Error parsing max queued duration for container task")
			return d, nvcaerrors.TerminalError(fmt.Errorf("parse max queued duration: %w", err))
		}
		d.gracePeriodSeconds, d.needsPurge = c.reconcileContainerTaskPodState(ctx, p, d.is, mqd, mrd, time.Now())
	case d.isBadState:
		d.needsPurge = d.is != types.ICMSInstanceDegradedWorker || c.bk8s.autoPurgeDegradedWorkers
	}
	return d, nil
}

// instancePodLiveReads tracks when the instance status loop last read each Pod instance live, to
// decide when an instance served from the informer cache is due a live refresh. Each instance has
// one refresh slot per window, at an offset taken from a hash of its ID, so the refreshes of many
// instances spread over the window instead of landing on the same pass. The zero value is ready
// to use.
type instancePodLiveReads struct {
	mu        sync.Mutex
	instances map[string]*instancePodLiveRead
	lastPrune time.Time
}

type instancePodLiveRead struct {
	slotOffset time.Duration
	lastLive   time.Time
	lastSeen   time.Time
}

// refreshDue reports whether the instance, served from the cache at now, is due a live read
// because a refresh slot has passed since its last live read. The window of an instance seen for
// the first time starts at now, which right after agent start follows the informer's initial list.
// An instance forgotten after not being seen for instancePodLiveReadRetention counts as seen for
// the first time.
func (r *instancePodLiveReads) refreshDue(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(id, now)
	e.lastSeen = now
	return lastRefreshSlot(now, e.slotOffset).After(e.lastLive)
}

// recordLive notes a live read of the instance at now.
func (r *instancePodLiveReads) recordLive(id string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(id, now)
	e.lastLive, e.lastSeen = now, now
}

// entry returns the instance's entry, creating it at now. Once per window it drops the entries of
// instances not seen for instancePodLiveReadRetention, so the map only holds instances the status
// loop still visits.
func (r *instancePodLiveReads) entry(id string, now time.Time) *instancePodLiveRead {
	if r.instances == nil {
		r.instances = map[string]*instancePodLiveRead{}
	}
	if now.Sub(r.lastPrune) >= instancePodRefreshWindow {
		for k, e := range r.instances {
			if now.Sub(e.lastSeen) >= instancePodLiveReadRetention {
				delete(r.instances, k)
			}
		}
		r.lastPrune = now
	}
	e, ok := r.instances[id]
	if !ok {
		e = &instancePodLiveRead{slotOffset: refreshSlotOffset(id), lastLive: now}
		r.instances[id] = e
	}
	return e
}

// refreshSlotOffset places the instance's refresh slot within the window.
func refreshSlotOffset(id string) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return time.Duration(h.Sum64() % uint64(instancePodRefreshWindow)) //nolint:gosec // less than the window
}

// lastRefreshSlot returns the latest refresh slot at or before now, for slots at offset into
// every window.
func lastRefreshSlot(now time.Time, offset time.Duration) time.Time {
	window := int64(instancePodRefreshWindow)
	sinceSlot := (now.UnixNano() - int64(offset)) % window
	if sinceSlot < 0 {
		sinceSlot += window
	}
	return now.Add(-time.Duration(sinceSlot))
}
