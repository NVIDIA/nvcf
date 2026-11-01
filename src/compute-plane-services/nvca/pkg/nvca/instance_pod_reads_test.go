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
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// podReadsPassInterval is how far apart these tests run passes of the instance status loop.
const podReadsPassInterval = 15 * time.Second

var podReadsPassesPerWindow = int(instancePodRefreshWindow / podReadsPassInterval)

func podReadsRunningPod(name, uid, resourceVersion, podIP string) *corev1.Pod {
	started := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: RequestsNamespace, UID: apitypes.UID(uid), ResourceVersion: resourceVersion,
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &started,
			HostIP:    "192.0.2.10",
			PodIP:     podIP,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: started},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "inference",
				Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
			}},
		},
	}
}

// podReadsStartingPod was just scheduled and has not started its containers yet.
func podReadsStartingPod(name, uid, resourceVersion string) *corev1.Pod {
	started := metav1.Now()
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: RequestsNamespace, UID: apitypes.UID(uid), ResourceVersion: resourceVersion,
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodPending,
			StartTime:  &started,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
		},
	}
}

// podReadsPullBackOffPod was scheduled 10 minutes ago and still waits on its image, which is past
// the image pull threshold, so the status loop purges it.
func podReadsPullBackOffPod(name, uid, resourceVersion string) *corev1.Pod {
	started := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: RequestsNamespace, UID: apitypes.UID(uid), ResourceVersion: resourceVersion,
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodPending,
			StartTime:  &started,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "inference",
				Image: "registry.example.invalid/app:1",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: k8sutil.ImagePullIssueAlternateReason,
				}},
			}},
		},
	}
}

// podReadsDegradedPod started 3 hours ago and has not been ready for the last 2, well past the
// worker degradation timeout, so the status loop classifies it as a degraded worker.
func podReadsDegradedPod(name, uid, resourceVersion string) *corev1.Pod {
	p := podReadsRunningPod(name, uid, resourceVersion, "198.51.100.7")
	started := metav1.NewTime(time.Now().Add(-3 * time.Hour))
	p.Status.StartTime = &started
	p.Status.ContainerStatuses[0].State.Running.StartedAt = started
	since := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
		{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
		{Type: corev1.ContainersReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady", LastTransitionTime: since},
		{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: since},
	}
	p.Status.ContainerStatuses[0].Ready = false
	return p
}

// podReadsInstance is a Pod instance last reported to ICMS in state at reportedAt. A nil reportedAt
// is an instance that has not been reported yet.
func podReadsInstance(id string, state types.ICMSInstanceState, reportedAt *metav1.Time) nvcav2beta1.InstanceStatus {
	return nvcav2beta1.InstanceStatus{
		ID:                    id,
		Type:                  nvcav2beta1.InstanceTypePod,
		Status:                string(state),
		LastReportedStatus:    string(state),
		LastReportedTimestamp: reportedAt,
	}
}

func podReadsRequest(instances ...nvcav2beta1.InstanceStatus) *nvcav2beta1.ICMSRequest {
	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "sr-pod-reads", Namespace: RequestsNamespace},
		Spec:       nvcav2beta1.ICMSRequestSpec{RequestID: "req-pod-reads", Action: common.FunctionCreationAction},
		Status: nvcav2beta1.ICMSRequestStatus{
			RequestStatus: nvcav2beta1.ICMSRequestStatusCompletionAcknowledged,
			Instances:     map[string]nvcav2beta1.InstanceStatus{},
		},
	}
	for _, st := range instances {
		req.Status.Instances[st.ID] = st
	}
	return req
}

// newPodReadsBackend returns a backend whose Pod informer cache is lister and whose API server is
// the one behind clients, so that the two can disagree as they do while a watch lags.
func newPodReadsBackend(clients *kubeclients.KubeClients, lister listersv1.PodNamespaceLister,
	flags ...*featureflag.FeatureFlag,
) (K8sComputeBackend, *BackendK8sCache) {
	bc := &BackendK8sCache{
		clients:                              clients,
		podInstanceNamespace:                 RequestsNamespace,
		systemNamespace:                      SystemNamespace,
		featureFlagFetcher:                   &featureflagmock.Fetcher{EnabledFFs: flags},
		k8sTimeConfig:                        (&k8sutil.TimeConfig{}).Complete(),
		podSpecLister:                        lister,
		autoPurgeDegradedWorkers:             true,
		lowLatencyStreamingEnabled:           true,
		enablePeriodicInstanceStatusUpdate:   true,
		periodicInstanceStatusUpdateInterval: 5 * time.Minute,
	}
	kb := K8sComputeBackend{clients: clients, bk8s: bc}
	bc.icmsRequestHelper = kb
	return kb, bc
}

// podReadsCache returns a lister over an informer-style cache that holds pods.
func podReadsCache(t *testing.T, pods ...*corev1.Pod) listersv1.PodNamespaceLister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, p := range pods {
		require.NoError(t, indexer.Add(p))
	}
	return listersv1.NewPodLister(indexer).Pods(RequestsNamespace)
}

// podReadsAPIServer returns clients whose fake API server holds copies of pods.
func podReadsAPIServer(pods ...*corev1.Pod) (*kubeclients.KubeClients, *fakek8sclient.Clientset) {
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p.DeepCopy())
	}
	clients := mockKubeClients(objs...)
	return clients, clients.K8s.(*fakek8sclient.Clientset)
}

func podReads(ctx context.Context, source, result string) float64 {
	m := nvcametrics.FromContext(ctx)
	return testutil.ToFloat64(m.InstancePodReadsTotal.WithLabelValues(m.WithDefaultLabelValues(source, result)...))
}

// podAPICalls is what TrackK8sAPICall counted for Pods, which only live reads add to.
func podAPICalls(ctx context.Context) float64 {
	m := nvcametrics.FromContext(ctx)
	labels := m.WithDefaultLabelValues("pod")
	return testutil.ToFloat64(m.K8sAPISuccessTotal.WithLabelValues(labels...)) +
		testutil.ToFloat64(m.K8sAPIFailureTotal.WithLabelValues(labels...))
}

// podDeletes returns the Pod DELETEs the fake API server was sent.
func podDeletes(client *fakek8sclient.Clientset) []ktesting.DeleteAction {
	var deletes []ktesting.DeleteAction
	for _, a := range client.Actions() {
		if del, ok := a.(ktesting.DeleteAction); ok && a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
			deletes = append(deletes, del)
		}
	}
	return deletes
}

// logEntries returns the log entries with message.
func logEntries(entries []*logrus.Entry, message string) []*logrus.Entry {
	var matched []*logrus.Entry
	for _, e := range entries {
		if e.Message == message {
			matched = append(matched, e)
		}
	}
	return matched
}

// staleCacheWarnings returns the log entries of refreshes that found a change the informer cache missed.
func staleCacheWarnings(entries []*logrus.Entry) []*logrus.Entry {
	return logEntries(entries, "Periodic live read found a Pod instance change missing from the informer cache")
}

// livePodGets counts, by Pod name, the Pod GETs the fake API server served.
func livePodGets(client *fakek8sclient.Clientset) map[string]int {
	gets := map[string]int{}
	for _, a := range client.Actions() {
		if get, ok := a.(ktesting.GetAction); ok && a.GetVerb() == "get" && a.GetResource().Resource == "pods" {
			gets[get.GetName()]++
		}
	}
	return gets
}

func countLivePodGets(client *fakek8sclient.Clientset) int {
	n := 0
	for _, c := range livePodGets(client) {
		n += c
	}
	return n
}

// podLists counts the Pod LISTs the fake API server served.
func podLists(client *fakek8sclient.Clientset) int {
	n := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "list" && a.GetResource().Resource == "pods" {
			n++
		}
	}
	return n
}

func livePodExists(t *testing.T, client *fakek8sclient.Clientset, name string) (*corev1.Pod, bool) {
	t.Helper()
	obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), RequestsNamespace, name)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)
	return obj.(*corev1.Pod), true
}

// enforceDeletePreconditions makes the fake API server check delete preconditions as the real
// one does, failing with a Conflict when the Pod's UID or resourceVersion differs. beforeDelete,
// when set, runs first, as a change that lands between the status loop's read and its DELETE.
func enforceDeletePreconditions(client *fakek8sclient.Clientset, beforeDelete func(ktesting.ObjectTracker)) {
	client.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		del := action.(ktesting.DeleteAction)
		if beforeDelete != nil {
			beforeDelete(client.Tracker())
		}
		pre := del.GetDeleteOptions().Preconditions
		obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), del.GetNamespace(), del.GetName())
		if pre == nil || err != nil {
			return false, nil, nil
		}
		pod := obj.(*corev1.Pod)
		if (pre.UID != nil && *pre.UID != pod.UID) ||
			(pre.ResourceVersion != nil && *pre.ResourceVersion != pod.ResourceVersion) {
			return true, nil, apierrors.NewConflict(corev1.Resource("pods"), pod.Name, errors.New("precondition failed"))
		}
		return false, nil, nil
	})
}

// podReadsClock is a manual clock for core.WithClock.
type podReadsClock struct{ now time.Time }

func (c *podReadsClock) Now() time.Time { return c.now }

// podReadsTickingClock is a clock for core.WithClock that moves forward by tick on every read, and
// counts the reads.
type podReadsTickingClock struct {
	next  time.Time
	tick  time.Duration
	reads int
}

func (c *podReadsTickingClock) Now() time.Time {
	now := c.next
	c.next = c.next.Add(c.tick)
	c.reads++
	return now
}

// runPodReadsPass runs one pass of the instance status loop over req, with ICMS accepting every
// update. It returns the updates by instance ID.
func runPodReadsPass(ctx context.Context, kb K8sComputeBackend,
	req *nvcav2beta1.ICMSRequest,
) map[string]types.ICMSRequestUpdateInfo {
	return runPodReadsPassPosting(ctx, kb, req, func(types.ICMSRequestUpdateInfo) error { return nil })
}

// runPodReadsPassPosting runs one pass of the instance status loop over req and posts each update
// with post. As the agent does, it records the updates post accepted as reported and keeps the
// status of every other instance. It returns the updates by instance ID, accepted or not.
func runPodReadsPassPosting(ctx context.Context, kb K8sComputeBackend, req *nvcav2beta1.ICMSRequest,
	post func(types.ICMSRequestUpdateInfo) error,
) map[string]types.ICMSRequestUpdateInfo {
	updates := map[string]types.ICMSRequestUpdateInfo{}
	posted := map[string]nvcav2beta1.InstanceStatus{}
	for _, upd := range kb.GetICMSRequestUpdatesForCreateRequest(ctx, req) {
		updates[upd.InstanceID] = upd
		if post(upd) == nil {
			posted[upd.InstanceID] = getPostedInstanceStatus(ctx, upd)
		}
	}
	req.Status.Instances = getUpdatedInstanceStatusMap(req.Status.Instances, posted)
	return updates
}

func TestInstancePodReads_CacheMissAndLiveNotFoundReportsTerminated(t *testing.T) {
	ctx := newTestContext()
	clients, _ := podReadsAPIServer()
	kb, _ := newPodReadsBackend(clients, podReadsCache(t), featureflag.InstancePodCacheReads)
	reportedAt := metav1.Now()
	req := podReadsRequest(podReadsInstance("0-sr-gone", types.ICMSInstanceRunning, &reportedAt))

	upd, err := kb.GetICMSRequestUpdatesForCreatePodRequest(ctx, req.Status.Instances["0-sr-gone"], req)

	require.NoError(t, err)
	assert.Equal(t, types.ICMSInstanceTerminated, upd.Payload.InstanceState)
	assert.Equal(t, types.ICMSInstanceFailedNotFound, upd.Payload.TerminationCause)
	assert.Equal(t, types.ICMSInstanceRequestClosed, upd.Payload.RequestState)
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceCache, nvcametrics.InstancePodReadResultNotFound))
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceMiss, nvcametrics.InstancePodReadResultNotFound))
	assert.Equal(t, float64(1), podAPICalls(ctx), "only the live read is a Kubernetes API call")
}

// A cache that still shows a Pod in a bad state past its threshold must not purge it: the live Pod
// has recovered, and its state is what gets reported.
func TestInstancePodReads_StaleBadStateInCacheIsConfirmedLive(t *testing.T) {
	ctx, logHook := core.WithTestingLogger(newTestContext())
	core.GetLogger(ctx).Logger.SetLevel(logrus.DebugLevel)
	const name = "0-sr-recovered"
	clients, client := podReadsAPIServer(podReadsRunningPod(name, "uid-1", "200", "198.51.100.7"))
	kb, _ := newPodReadsBackend(clients, podReadsCache(t, podReadsPullBackOffPod(name, "uid-1", "100")),
		featureflag.InstancePodCacheReads)
	reportedAt := metav1.Now()
	req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceStarted, &reportedAt))

	upd, err := kb.GetICMSRequestUpdatesForCreatePodRequest(ctx, req.Status.Instances[name], req)

	require.NoError(t, err)
	assert.Equal(t, types.ICMSInstanceRunning, upd.Payload.InstanceState)
	assert.Equal(t, types.ICMSInstanceStateNoStatus, upd.Payload.TerminationCause)
	_, exists := livePodExists(t, client, name)
	assert.True(t, exists, "the recovered Pod must not be purged")
	assert.Empty(t, podDeletes(client))
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceCache, nvcametrics.InstancePodReadResultFound))
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceConfirm, nvcametrics.InstancePodReadResultFound))
	changed := logEntries(logHook.AllEntries(), "Live read changed the decision taken from the informer cache")
	require.Len(t, changed, 1)
	assert.Equal(t, logrus.DebugLevel, changed[0].Level, "a confirm read that changes the decision is routine")
}

// A state ICMS has not been told, a changed one or the first one, is reported with the live Pod's
// fields, not the cached ones.
func TestInstancePodReads_ConfirmedChangeReportsLiveFields(t *testing.T) {
	const name = "0-sr-ips"
	reportedAt := metav1.Now()
	tests := []struct {
		name     string
		instance nvcav2beta1.InstanceStatus
	}{
		{name: "a changed state", instance: podReadsInstance(name, types.ICMSInstanceStarted, &reportedAt)},
		{name: "a state not reported yet", instance: podReadsInstance(name, types.ICMSInstanceRunning, nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients, _ := podReadsAPIServer(podReadsRunningPod(name, "uid-1", "200", "198.51.100.8"))
			kb, _ := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
				featureflag.InstancePodCacheReads)
			req := podReadsRequest(tt.instance)

			upd, err := kb.GetICMSRequestUpdatesForCreatePodRequest(ctx, req.Status.Instances[name], req)

			require.NoError(t, err)
			assert.Equal(t, types.ICMSInstanceRunning, upd.Payload.InstanceState)
			assert.Equal(t, []string{"192.0.2.10", "198.51.100.8"}, upd.Payload.InstanceIPs)
			assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceConfirm, nvcametrics.InstancePodReadResultFound))
		})
	}
}

// A periodic report of an unchanged state, in whatever case it was reported, is built from a live
// read, and that read is also the instance's refresh.
func TestInstancePodReads_HeartbeatIsReadLive(t *testing.T) {
	const name = "0-sr-heartbeat"
	for _, reported := range []string{string(types.ICMSInstanceRunning), strings.ToUpper(string(types.ICMSInstanceRunning))} {
		t.Run(reported, func(t *testing.T) {
			clients, client := podReadsAPIServer(podReadsRunningPod(name, "uid-1", "200", "198.51.100.8"))
			kb, bc := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
				featureflag.InstancePodCacheReads)
			// The status loop's clock stands still, so the second pass is not due anything new.
			clock := &podReadsClock{now: time.Now()}
			logCtx, logHook := core.WithTestingLogger(newTestContext())
			ctx := core.WithClock(logCtx, clock.Now)
			// The last live read was a window ago, so the refresh is due as well.
			bc.instancePodLiveReads.recordLive(name, clock.now.Add(-instancePodRefreshWindow))
			reportedAt := metav1.NewTime(clock.now.Add(-6 * time.Minute))
			instance := podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt)
			instance.LastReportedStatus = reported
			req := podReadsRequest(instance)

			upd := runPodReadsPass(ctx, kb, req)[name]

			assert.Equal(t, types.ICMSInstanceRunning, upd.Payload.InstanceState, "the periodic report is posted")
			assert.Equal(t, []string{"192.0.2.10", "198.51.100.8"}, upd.Payload.InstanceIPs, "from the live Pod")
			assert.Equal(t, 1, countLivePodGets(client))
			assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceHeartbeat, nvcametrics.InstancePodReadResultFound))
			assert.Empty(t, staleCacheWarnings(logHook.AllEntries()), "the reported state did not change")

			assert.Empty(t, runPodReadsPass(ctx, kb, req), "the periodic report is not due again")
			assert.Equal(t, 1, countLivePodGets(client), "the heartbeat read was also the refresh")
			assert.Zero(t, podReads(ctx, nvcametrics.InstancePodReadSourceRefresh, nvcametrics.InstancePodReadResultFound))
		})
	}
}

// A heartbeat read that finds a change the informer cache missed reports the live state and warns
// of the stale cache, as a refresh does.
func TestInstancePodReads_HeartbeatFindsChangeMissingFromCache(t *testing.T) {
	const name = "0-sr-heartbeat-stale"
	tests := []struct {
		name       string
		live       []*corev1.Pod
		wantState  types.ICMSInstanceState
		wantCause  types.ICMSInstanceState
		wantResult string
	}{
		{
			name:       "a changed Pod",
			live:       []*corev1.Pod{podReadsStartingPod(name, "uid-1", "200")},
			wantState:  types.ICMSInstanceStarted,
			wantCause:  types.ICMSInstanceStateNoStatus,
			wantResult: nvcametrics.InstancePodReadResultFound,
		},
		{
			name:       "a deleted Pod",
			wantState:  types.ICMSInstanceTerminated,
			wantCause:  types.ICMSInstanceFailedNotFound,
			wantResult: nvcametrics.InstancePodReadResultNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients, _ := podReadsAPIServer(tt.live...)
			kb, _ := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
				featureflag.InstancePodCacheReads)
			clock := &podReadsClock{now: time.Now()}
			logCtx, logHook := core.WithTestingLogger(newTestContext())
			ctx := core.WithClock(logCtx, clock.Now)
			reportedAt := metav1.NewTime(clock.now.Add(-6 * time.Minute))
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

			upd := runPodReadsPass(ctx, kb, req)[name]

			assert.Equal(t, tt.wantState, upd.Payload.InstanceState)
			assert.Equal(t, tt.wantCause, upd.Payload.TerminationCause)
			assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceHeartbeat, tt.wantResult))
			warnings := staleCacheWarnings(logHook.AllEntries())
			require.Len(t, warnings, 1)
			assert.Equal(t, logrus.WarnLevel, warnings[0].Level)
			assert.Equal(t, nvcametrics.InstancePodReadSourceHeartbeat, warnings[0].Data["podRead"])
		})
	}
}

// A periodic report ICMS rejects is retried on the next pass from a new live read, never from an
// earlier one.
func TestInstancePodReads_RejectedHeartbeatIsReadLiveAgain(t *testing.T) {
	const name = "0-sr-heartbeat-retry"
	clients, client := podReadsAPIServer(podReadsRunningPod(name, "uid-1", "100", "198.51.100.7"))
	kb, _ := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
		featureflag.InstancePodCacheReads)
	// The status loop's clock stands still, so no refresh falls between the passes.
	clock := &podReadsClock{now: time.Now()}
	ctx := core.WithClock(newTestContext(), clock.Now)
	reportedAt := metav1.NewTime(clock.now.Add(-6 * time.Minute))
	req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))
	rejected := func(types.ICMSRequestUpdateInfo) error { return errors.New("service unavailable") }

	for i, podIP := range []string{"198.51.100.8", "198.51.100.9"} {
		// The Pod changes between passes, and the stalled watch does not deliver the change.
		_, err := client.CoreV1().Pods(RequestsNamespace).UpdateStatus(ctx,
			podReadsRunningPod(name, "uid-1", fmt.Sprint(200+i), podIP), metav1.UpdateOptions{})
		require.NoError(t, err)

		upd := runPodReadsPassPosting(ctx, kb, req, rejected)[name]

		assert.Equal(t, []string{"192.0.2.10", podIP}, upd.Payload.InstanceIPs, "pass %d reports what it read live", i)
		assert.Equal(t, i+1, countLivePodGets(client))
		assert.Equal(t, reportedAt, *req.Status.Instances[name].LastReportedTimestamp, "a rejected report is not recorded")
	}

	upd := runPodReadsPass(ctx, kb, req)[name]
	assert.Equal(t, []string{"192.0.2.10", "198.51.100.9"}, upd.Payload.InstanceIPs)
	assert.Equal(t, 3, countLivePodGets(client))
	assert.Equal(t, float64(3), podReads(ctx, nvcametrics.InstancePodReadSourceHeartbeat, nvcametrics.InstancePodReadResultFound))

	assert.Empty(t, runPodReadsPass(ctx, kb, req), "the accepted report is not due again")
	assert.Equal(t, 3, countLivePodGets(client))
}

// While live reads fail, an instance whose periodic report is due is neither reported from the
// cache nor recorded as reported, and each pass tries a new live read. Once live reads succeed
// again, the report is built from the live Pod.
func TestInstancePodReads_APIOutageReportsNothing(t *testing.T) {
	const name = "0-sr-outage"
	clients, client := podReadsAPIServer(podReadsRunningPod(name, "uid-1", "200", "198.51.100.8"))
	outage := true
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if outage {
			return true, nil, apierrors.NewInternalError(errors.New("request timed out"))
		}
		return false, nil, nil
	})
	kb, _ := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
		featureflag.InstancePodCacheReads)
	clock := &podReadsClock{now: time.Now()}
	ctx := core.WithClock(newTestContext(), clock.Now)
	reportedAt := metav1.NewTime(clock.now.Add(-6 * time.Minute))
	req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

	const passes = 3
	for range passes {
		assert.Empty(t, runPodReadsPass(ctx, kb, req), "nothing is reported without a live read")
		clock.now = clock.now.Add(podReadsPassInterval)
	}
	assert.Equal(t, passes, countLivePodGets(client), "each pass tries a new live read")
	assert.Equal(t, float64(passes), podReads(ctx, nvcametrics.InstancePodReadSourceHeartbeat, nvcametrics.InstancePodReadResultError))
	assert.Equal(t, reportedAt, *req.Status.Instances[name].LastReportedTimestamp)

	outage = false
	upd := runPodReadsPass(ctx, kb, req)[name]
	assert.Equal(t, types.ICMSInstanceRunning, upd.Payload.InstanceState)
	assert.Equal(t, []string{"192.0.2.10", "198.51.100.8"}, upd.Payload.InstanceIPs)
}

// A periodic report that falls due between the cache read and the decision to report, at the
// boundary of the heartbeat interval, is not built from the cached Pod: the next pass reads the Pod
// live and reports it.
func TestInstancePodReads_HeartbeatDueAtTheBoundaryWaitsForALiveRead(t *testing.T) {
	const name = "0-sr-boundary"
	clients, client := podReadsAPIServer(podReadsRunningPod(name, "uid-1", "200", "198.51.100.8"))
	kb, bc := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
		featureflag.InstancePodCacheReads)
	// Every read of the clock is a second after the one before. readInstancePod reads it first, a
	// second before the interval has passed, and serves the Pod from the cache. The decision to report
	// reads it next, when the interval has passed.
	start := time.Now()
	clock := &podReadsTickingClock{next: start, tick: time.Second}
	logCtx, logHook := core.WithTestingLogger(newTestContext())
	core.GetLogger(logCtx).Logger.SetLevel(logrus.DebugLevel)
	ctx := core.WithClock(logCtx, clock.Now)
	reportedAt := metav1.NewTime(start.Add(time.Second - bc.periodicInstanceStatusUpdateInterval))
	req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

	upd, err := kb.GetICMSRequestUpdatesForCreatePodRequest(ctx, req.Status.Instances[name], req)
	require.NoError(t, err, "a report that falls due after the cache read is skipped, not failed")
	require.Equal(t, 2, clock.reads, "readInstancePod and the decision to report each read the clock once")
	assert.Zero(t, upd, "a periodic report is not built from the cached Pod")
	assert.Zero(t, countLivePodGets(client))
	skipped := logEntries(logHook.AllEntries(),
		"Instance status report is due but the Pod was read from the informer cache, reporting it on the next pass")
	require.Len(t, skipped, 1)
	assert.Equal(t, logrus.DebugLevel, skipped[0].Level, "skipping the report is routine")

	clock.next = start.Add(podReadsPassInterval)
	upd = runPodReadsPass(ctx, kb, req)[name]
	assert.Equal(t, []string{"192.0.2.10", "198.51.100.8"}, upd.Payload.InstanceIPs)
	assert.Equal(t, 1, countLivePodGets(client))
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceHeartbeat, nvcametrics.InstancePodReadResultFound))
}

// The periodic report of one instance keeps the status of the instances that report nothing on
// the pass.
func TestInstancePodReads_HeartbeatKeepsUnreportedInstances(t *testing.T) {
	const due, notDue = "0-sr-due", "1-sr-not-due"
	pods := []*corev1.Pod{
		podReadsRunningPod(due, "uid-0", "100", "198.51.100.7"),
		podReadsRunningPod(notDue, "uid-1", "100", "198.51.100.8"),
	}
	clients, _ := podReadsAPIServer(pods...)
	kb, _ := newPodReadsBackend(clients, podReadsCache(t, pods...), featureflag.InstancePodCacheReads)
	clock := &podReadsClock{now: time.Now()}
	ctx := core.WithClock(newTestContext(), clock.Now)
	dueAt := metav1.NewTime(clock.now.Add(-6 * time.Minute))
	notDueAt := metav1.NewTime(clock.now.Add(-time.Minute))
	notDueStatus := podReadsInstance(notDue, types.ICMSInstanceRunning, &notDueAt)
	req := podReadsRequest(podReadsInstance(due, types.ICMSInstanceRunning, &dueAt), notDueStatus)

	updates := runPodReadsPass(ctx, kb, req)

	assert.Len(t, updates, 1)
	assert.Contains(t, updates, due)
	assert.Equal(t, clock.now, req.Status.Instances[due].LastReportedTimestamp.Time)
	assert.Equal(t, notDueStatus, req.Status.Instances[notDue], "an instance that reported nothing keeps its status")
}

// Every instance served from the cache is read live within each refresh window, even with periodic
// heartbeats disabled, so a watch that stalls cannot hide a change for longer than the window. A
// refresh that finds a change the cache missed warns of the stale cache.
func TestInstancePodReads_RefreshWithinWindowWithoutHeartbeats(t *testing.T) {
	const name = "0-sr-refresh"
	t.Run("an unchanged Pod is read live once per window and is not reported", func(t *testing.T) {
		pod := podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")
		clients, client := podReadsAPIServer(pod)
		kb, bc := newPodReadsBackend(clients, podReadsCache(t, pod), featureflag.InstancePodCacheReads)
		bc.enablePeriodicInstanceStatusUpdate = false
		clock := &podReadsClock{now: time.Now()}
		start := clock.now
		logCtx, logHook := core.WithTestingLogger(newTestContext())
		ctx := core.WithClock(logCtx, clock.Now)
		reportedAt := metav1.Now()
		req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

		var refreshes []time.Time
		for range 2*podReadsPassesPerWindow + 1 {
			before := countLivePodGets(client)
			assert.Empty(t, runPodReadsPass(ctx, kb, req), "an unchanged state is not reported without heartbeats")
			if countLivePodGets(client) > before {
				refreshes = append(refreshes, clock.now)
			}
			clock.now = clock.now.Add(podReadsPassInterval)
		}

		require.Len(t, refreshes, 2, "one live read per window")
		assert.LessOrEqual(t, refreshes[0].Sub(start), instancePodRefreshWindow)
		assert.InDelta(t, float64(instancePodRefreshWindow), float64(refreshes[1].Sub(refreshes[0])), float64(podReadsPassInterval))
		assert.Equal(t, float64(2), podReads(ctx, nvcametrics.InstancePodReadSourceRefresh, nvcametrics.InstancePodReadResultFound))
		assert.Zero(t, podReads(ctx, nvcametrics.InstancePodReadSourceConfirm, nvcametrics.InstancePodReadResultFound))
		assert.Empty(t, staleCacheWarnings(logHook.AllEntries()))
	})

	tests := []struct {
		name       string
		live       []*corev1.Pod
		wantState  types.ICMSInstanceState
		wantCause  types.ICMSInstanceState
		wantResult string
	}{
		{
			name:       "a Pod changed behind a stalled watch is reported within the window",
			live:       []*corev1.Pod{podReadsStartingPod(name, "uid-1", "200")},
			wantState:  types.ICMSInstanceStarted,
			wantCause:  types.ICMSInstanceStateNoStatus,
			wantResult: nvcametrics.InstancePodReadResultFound,
		},
		{
			name:       "a Pod deleted behind a stalled watch is reported within the window",
			wantState:  types.ICMSInstanceTerminated,
			wantCause:  types.ICMSInstanceFailedNotFound,
			wantResult: nvcametrics.InstancePodReadResultNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients, _ := podReadsAPIServer(tt.live...)
			kb, bc := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
				featureflag.InstancePodCacheReads)
			bc.enablePeriodicInstanceStatusUpdate = false
			clock := &podReadsClock{now: time.Now()}
			start := clock.now
			logCtx, logHook := core.WithTestingLogger(newTestContext())
			ctx := core.WithClock(logCtx, clock.Now)
			reportedAt := metav1.Now()
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

			var upd types.ICMSRequestUpdateInfo
			var reported bool
			for range podReadsPassesPerWindow + 1 {
				if upd, reported = runPodReadsPass(ctx, kb, req)[name]; reported {
					break
				}
				clock.now = clock.now.Add(podReadsPassInterval)
			}

			require.True(t, reported, "the change must be reported within one window")
			assert.LessOrEqual(t, clock.now.Sub(start), instancePodRefreshWindow)
			assert.Equal(t, tt.wantState, upd.Payload.InstanceState)
			assert.Equal(t, tt.wantCause, upd.Payload.TerminationCause)
			assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceRefresh, tt.wantResult))
			warnings := staleCacheWarnings(logHook.AllEntries())
			require.Len(t, warnings, 1)
			assert.Equal(t, logrus.WarnLevel, warnings[0].Level)
			assert.Equal(t, "100", warnings[0].Data["cachedResourceVersion"])
		})
	}
}

// Instances seen together, as after an agent start, do not all read live on the same pass.
func TestInstancePodReads_RefreshesSpreadAcrossPasses(t *testing.T) {
	const instances = 200
	var pods []*corev1.Pod
	var statuses []nvcav2beta1.InstanceStatus
	reportedAt := metav1.Now()
	for i := range instances {
		name := fmt.Sprintf("%d-sr-5f0c2a7e-1b2d-4c3e-9f8a-0123456789ab", i)
		pods = append(pods, podReadsRunningPod(name, fmt.Sprintf("uid-%d", i), "100", "198.51.100.7"))
		statuses = append(statuses, podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))
	}
	clients, client := podReadsAPIServer(pods...)
	kb, bc := newPodReadsBackend(clients, podReadsCache(t, pods...), featureflag.InstancePodCacheReads)
	bc.enablePeriodicInstanceStatusUpdate = false
	// Refresh slots are aligned to the Unix epoch, so a fixed start makes every run the same.
	clock := &podReadsClock{now: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	ctx := core.WithClock(newTestContext(), clock.Now)
	req := podReadsRequest(statuses...)

	var readsPerPass []int
	for range 2*podReadsPassesPerWindow + 1 {
		before := countLivePodGets(client)
		assert.Empty(t, runPodReadsPass(ctx, kb, req))
		readsPerPass = append(readsPerPass, countLivePodGets(client)-before)
		clock.now = clock.now.Add(podReadsPassInterval)
	}

	assert.Zero(t, readsPerPass[0], "the first pass serves every instance from the cache")
	// The busiest pass reads at most 1.6 times the mean of a pass.
	assert.LessOrEqual(t, slices.Max(readsPerPass), 8*instances/(5*podReadsPassesPerWindow),
		"refreshes must spread across passes, got %v", readsPerPass)
	for _, pod := range pods {
		assert.Equal(t, 2, livePodGets(client)[pod.Name], "Pod %s must be read live once per window", pod.Name)
	}
}

// The Pod of an instance reported terminated is expected to be gone, so the status loop reads it
// live only for the instance's refresh and for the periodic report of its terminated state, and the
// report is built from that live read. A Pod created again under the instance's name, which a
// stalled watch does not deliver, is still reported within a window.
func TestInstancePodReads_TerminatedInstanceIsReadLiveOnlyWhenDue(t *testing.T) {
	const name, running = "0-sr-terminated", "1-sr-running"
	// setup lists the terminated instance, last reported reportedAgo before now, with a running
	// instance, which keeps the request from counting as terminated and reported. The API server
	// also holds the Pods in live.
	setup := func(t *testing.T, heartbeats bool, reportedAgo time.Duration, live ...*corev1.Pod) (context.Context,
		*podReadsClock, K8sComputeBackend, *fakek8sclient.Clientset, *nvcav2beta1.ICMSRequest,
	) {
		runningPod := podReadsRunningPod(running, "uid-1", "100", "198.51.100.7")
		clients, client := podReadsAPIServer(append(live, runningPod)...)
		kb, bc := newPodReadsBackend(clients, podReadsCache(t, runningPod), featureflag.InstancePodCacheReads)
		bc.enablePeriodicInstanceStatusUpdate = heartbeats
		clock := &podReadsClock{now: time.Now()}
		ctx := core.WithClock(newTestContext(), clock.Now)
		reportedAt := metav1.NewTime(clock.now.Add(-reportedAgo))
		runningReportedAt := metav1.NewTime(clock.now)
		req := podReadsRequest(
			podReadsInstance(name, types.ICMSInstanceTerminated, &reportedAt),
			podReadsInstance(running, types.ICMSInstanceRunning, &runningReportedAt),
		)
		return ctx, clock, kb, client, req
	}

	t.Run("without periodic reports, a gone Pod is read live once per window", func(t *testing.T) {
		ctx, clock, kb, client, req := setup(t, false, 6*time.Minute)

		passes := 2*podReadsPassesPerWindow + 1
		var reads []time.Time
		for range passes {
			before := livePodGets(client)[name]
			assert.NotContains(t, runPodReadsPass(ctx, kb, req), name)
			if livePodGets(client)[name] > before {
				reads = append(reads, clock.now)
			}
			clock.now = clock.now.Add(podReadsPassInterval)
		}

		require.Len(t, reads, 2, "one live read per window")
		assert.Equal(t, instancePodRefreshWindow, reads[1].Sub(reads[0]))
		assert.Equal(t, float64(passes), podReads(ctx, nvcametrics.InstancePodReadSourceCache, nvcametrics.InstancePodReadResultNotFound))
		assert.Equal(t, float64(2), podReads(ctx, nvcametrics.InstancePodReadSourceMiss, nvcametrics.InstancePodReadResultNotFound))
	})

	t.Run("a gone Pod is read live for each periodic report, which repeats the terminated state", func(t *testing.T) {
		ctx, clock, kb, client, req := setup(t, true, 5*time.Minute)
		interval := 5 * time.Minute

		var reports []time.Duration
		start := clock.now
		for at := time.Duration(0); at < 2*interval; at += podReadsPassInterval {
			clock.now = start.Add(at)
			before := livePodGets(client)[name]
			upd, reported := runPodReadsPass(ctx, kb, req)[name]
			if !reported {
				continue
			}
			reports = append(reports, at)
			assert.Equal(t, before+1, livePodGets(client)[name], "the report at %v is built from a live read", at)
			assert.Equal(t, types.ICMSInstanceTerminated, upd.Payload.InstanceState)
			assert.Equal(t, types.ICMSInstanceFailedNotFound, upd.Payload.TerminationCause)
		}

		assert.Equal(t, []time.Duration{0, interval}, reports)
		reads := livePodGets(client)[name]
		assert.LessOrEqual(t, reads, int(2*interval/instancePodRefreshWindow)+len(reports),
			"one live read per window and per report")
	})

	t.Run("a periodic report that falls due after the cache miss waits for a live read", func(t *testing.T) {
		runningPod := podReadsRunningPod(running, "uid-1", "100", "198.51.100.7")
		clients, client := podReadsAPIServer(runningPod)
		kb, bc := newPodReadsBackend(clients, podReadsCache(t, runningPod), featureflag.InstancePodCacheReads)
		// Every read of the clock is a second after the one before. readInstancePod reads it for the
		// refresh and then for the periodic report, a second before the report is due.
		start := time.Now()
		clock := &podReadsTickingClock{next: start, tick: time.Second}
		ctx := core.WithClock(newTestContext(), clock.Now)
		reportedAt := metav1.NewTime(start.Add(2*time.Second - bc.periodicInstanceStatusUpdateInterval))
		runningReportedAt := metav1.NewTime(start)
		req := podReadsRequest(
			podReadsInstance(name, types.ICMSInstanceTerminated, &reportedAt),
			podReadsInstance(running, types.ICMSInstanceRunning, &runningReportedAt),
		)

		upd, err := kb.GetICMSRequestUpdatesForCreatePodRequest(ctx, req.Status.Instances[name], req)

		require.NoError(t, err)
		assert.Zero(t, upd, "a periodic report is not built from a cache miss")
		assert.Equal(t, 2, clock.reads, "only readInstancePod reads the clock")
		assert.Zero(t, livePodGets(client)[name])
	})

	t.Run("a Pod created again under its name is reported within a window", func(t *testing.T) {
		ctx, clock, kb, client, req := setup(t, true, 0, podReadsRunningPod(name, "uid-2", "300", "198.51.100.9"))
		start := clock.now

		var upd types.ICMSRequestUpdateInfo
		var reported bool
		for range podReadsPassesPerWindow + 1 {
			if upd, reported = runPodReadsPass(ctx, kb, req)[name]; reported {
				break
			}
			clock.now = clock.now.Add(podReadsPassInterval)
		}

		require.True(t, reported, "the Pod must be reported within one window")
		assert.LessOrEqual(t, clock.now.Sub(start), instancePodRefreshWindow)
		assert.Equal(t, types.ICMSInstanceRunning, upd.Payload.InstanceState)
		assert.Equal(t, []string{"192.0.2.10", "198.51.100.9"}, upd.Payload.InstanceIPs)
		assert.Equal(t, string(types.ICMSInstanceRunning), req.Status.Instances[name].LastReportedStatus)
		assert.Equal(t, 1, livePodGets(client)[name])
	})
}

// Flag off, every pass reads every Pod live, as before, and the cache, which could serve the
// unchanged Pod, is never consulted. A periodic report is built from the live Pod.
func TestInstancePodReads_KillSwitchReadsEveryPassLive(t *testing.T) {
	const name = "0-sr-killswitch"
	tests := []struct {
		name        string
		reportedAgo time.Duration
		wantReports int
	}{
		{name: "an unchanged state"},
		{name: "a periodic report", reportedAgo: 6 * time.Minute, wantReports: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients, client := podReadsAPIServer(podReadsRunningPod(name, "uid-1", "200", "198.51.100.8"))
			kb, _ := newPodReadsBackend(clients, podReadsCache(t, podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")))
			reportedAt := metav1.NewTime(time.Now().Add(-tt.reportedAgo))
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

			var reports []types.ICMSRequestUpdateInfo
			for range 3 {
				for _, upd := range runPodReadsPass(ctx, kb, req) {
					reports = append(reports, upd)
				}
			}

			require.Len(t, reports, tt.wantReports)
			for _, upd := range reports {
				assert.Equal(t, []string{"192.0.2.10", "198.51.100.8"}, upd.Payload.InstanceIPs)
			}
			assert.Equal(t, 3, countLivePodGets(client))
			assert.Equal(t, float64(3), podAPICalls(ctx))
			for _, source := range nvcametrics.AllInstancePodReadSources {
				for _, result := range nvcametrics.AllInstancePodReadResults {
					assert.Zero(t, podReads(ctx, source, result), "source=%s result=%s", source, result)
				}
			}
		})
	}
}

// A purge deletes the Pod only as the status loop read it: not a replacement Pod with the same
// name, and not the Pod after it changed. A Pod kept that way is not reported, and the next pass
// decides again. A Pod that is gone by the DELETE, or that the DELETE fails for, is reported
// terminated as before.
func TestInstancePodReads_PurgeDeletesOnlyThePodItWasDecidedFrom(t *testing.T) {
	const name = "0-sr-purge"
	podsGVR := corev1.SchemeGroupVersion.WithResource("pods")
	tests := []struct {
		name         string
		flags        []*featureflag.FeatureFlag
		beforeDelete func(t *testing.T, tracker ktesting.ObjectTracker)
		deleteErr    error
		wantReported bool
		wantKept     bool
	}{
		{
			name:         "the Pod as read is purged",
			flags:        []*featureflag.FeatureFlag{featureflag.InstancePodCacheReads},
			wantReported: true,
		},
		{
			name:  "a Pod gone by the DELETE is reported terminated",
			flags: []*featureflag.FeatureFlag{featureflag.InstancePodCacheReads},
			beforeDelete: func(t *testing.T, tracker ktesting.ObjectTracker) {
				require.NoError(t, tracker.Delete(podsGVR, RequestsNamespace, name))
			},
			wantReported: true,
		},
		{
			name:         "a Pod the DELETE fails for is reported terminated",
			flags:        []*featureflag.FeatureFlag{featureflag.InstancePodCacheReads},
			deleteErr:    apierrors.NewInternalError(errors.New("request timed out")),
			wantReported: true,
			wantKept:     true,
		},
		{
			name:  "a replacement Pod with the same name is kept",
			flags: []*featureflag.FeatureFlag{featureflag.InstancePodCacheReads},
			beforeDelete: func(t *testing.T, tracker ktesting.ObjectTracker) {
				require.NoError(t, tracker.Delete(podsGVR, RequestsNamespace, name))
				require.NoError(t, tracker.Create(podsGVR, podReadsRunningPod(name, "uid-2", "300", "198.51.100.9"), RequestsNamespace))
			},
			wantKept: true,
		},
		{
			name:  "a Pod that recovered after it was read is kept",
			flags: []*featureflag.FeatureFlag{featureflag.InstancePodCacheReads},
			beforeDelete: func(t *testing.T, tracker ktesting.ObjectTracker) {
				require.NoError(t, tracker.Update(podsGVR, podReadsRunningPod(name, "uid-1", "101", "198.51.100.9"), RequestsNamespace))
			},
			wantKept: true,
		},
		{
			name: "with cache reads disabled, a replacement Pod with the same name is kept",
			beforeDelete: func(t *testing.T, tracker ktesting.ObjectTracker) {
				require.NoError(t, tracker.Delete(podsGVR, RequestsNamespace, name))
				require.NoError(t, tracker.Create(podsGVR, podReadsRunningPod(name, "uid-2", "300", "198.51.100.9"), RequestsNamespace))
			},
			wantKept: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			badPod := podReadsPullBackOffPod(name, "uid-1", "100")
			clients, client := podReadsAPIServer(badPod)
			var once sync.Once
			enforceDeletePreconditions(client, func(tracker ktesting.ObjectTracker) {
				if tt.beforeDelete != nil {
					once.Do(func() { tt.beforeDelete(t, tracker) })
				}
			})
			if tt.deleteErr != nil {
				client.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.deleteErr
				})
			}
			kb, _ := newPodReadsBackend(clients, podReadsCache(t, badPod), tt.flags...)
			reportedAt := metav1.Now()
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceStarted, &reportedAt))

			updates := runPodReadsPass(ctx, kb, req)

			deletes := podDeletes(client)
			require.Len(t, deletes, 1)
			pre := deletes[0].GetDeleteOptions().Preconditions
			require.NotNil(t, pre)
			assert.Equal(t, apitypes.UID("uid-1"), *pre.UID)
			assert.Equal(t, "100", *pre.ResourceVersion)

			live, exists := livePodExists(t, client, name)
			assert.Equal(t, tt.wantKept, exists)
			if tt.wantReported {
				assert.Equal(t, types.ICMSInstanceTerminated, updates[name].Payload.InstanceState)
				assert.Equal(t, types.ICMSInstanceFailedImagePullIssues, updates[name].Payload.TerminationCause)
				return
			}
			require.True(t, exists, "the Pod must not be deleted")
			assert.Empty(t, updates, "nothing is reported for a Pod that changed before it could be purged")
			assert.Equal(t, string(types.ICMSInstanceStarted), req.Status.Instances[name].LastReportedStatus)

			// The next pass decides again, from the Pod as it is now.
			updates = runPodReadsPass(ctx, kb, req)
			assert.Equal(t, types.ICMSInstanceRunning, updates[name].Payload.InstanceState)
			after, exists := livePodExists(t, client, name)
			require.True(t, exists)
			assert.Equal(t, live.UID, after.UID)
		})
	}
}

// A purge seen in the cache is confirmed live even once ICMS has been told the instance terminated,
// as after a DELETE that failed, so the retried DELETE carries the live Pod's preconditions.
func TestInstancePodReads_PurgeRetryIsConfirmedLive(t *testing.T) {
	ctx := newTestContext()
	const name = "0-sr-purge-retry"
	clients, client := podReadsAPIServer(podReadsPullBackOffPod(name, "uid-1", "150"))
	enforceDeletePreconditions(client, nil)
	kb, _ := newPodReadsBackend(clients, podReadsCache(t, podReadsPullBackOffPod(name, "uid-1", "100")),
		featureflag.InstancePodCacheReads)
	reportedAt := metav1.Now()
	req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceTerminated, &reportedAt))

	runPodReadsPass(ctx, kb, req)

	deletes := podDeletes(client)
	require.Len(t, deletes, 1)
	assert.Equal(t, "150", *deletes[0].GetDeleteOptions().Preconditions.ResourceVersion)
	_, exists := livePodExists(t, client, name)
	assert.False(t, exists, "the Pod still in a bad state is purged")
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceConfirm, nvcametrics.InstancePodReadResultFound))
}

// A degraded function Pod is purged only when degraded workers are purged automatically, whether or
// not Pods are read from the informer cache and whether or not its degraded state was reported.
func TestInstancePodReads_DegradedWorkerFollowsAutoPurge(t *testing.T) {
	const name = "0-sr-degraded"
	for _, cacheReads := range []bool{true, false} {
		for _, autoPurge := range []bool{true, false} {
			for _, reported := range []types.ICMSInstanceState{types.ICMSInstanceRunning, types.ICMSInstanceDegradedWorker} {
				t.Run(fmt.Sprintf("cache reads %v, auto-purge %v, reported %s", cacheReads, autoPurge, reported), func(t *testing.T) {
					ctx := newTestContext()
					clients, client := podReadsAPIServer(podReadsDegradedPod(name, "uid-1", "100"))
					var flags []*featureflag.FeatureFlag
					if cacheReads {
						flags = append(flags, featureflag.InstancePodCacheReads)
					}
					kb, bc := newPodReadsBackend(clients, podReadsCache(t, podReadsDegradedPod(name, "uid-1", "100")), flags...)
					bc.autoPurgeDegradedWorkers = autoPurge
					reportedAt := metav1.Now()
					req := podReadsRequest(podReadsInstance(name, reported, &reportedAt))

					upd, isReported := runPodReadsPass(ctx, kb, req)[name]

					_, exists := livePodExists(t, client, name)
					switch {
					case autoPurge:
						assert.Len(t, podDeletes(client), 1)
						assert.False(t, exists)
						assert.Equal(t, types.ICMSInstanceTerminated, upd.Payload.InstanceState)
						assert.Equal(t, types.ICMSInstanceDegradedWorker, upd.Payload.TerminationCause)
					case reported == types.ICMSInstanceRunning:
						assert.Empty(t, podDeletes(client))
						assert.True(t, exists)
						assert.Equal(t, types.ICMSInstanceDegradedWorker, upd.Payload.InstanceState)
					default:
						assert.Empty(t, podDeletes(client))
						assert.True(t, exists)
						assert.False(t, isReported, "an unchanged degraded state is not reported again before its periodic report")
					}
				})
			}
		}
	}
}

// A task purge seen in the cache keeps the task's grace period: a task whose utils container
// exited with an error, or that ran past its maximum runtime, is deleted at once.
func TestInstancePodReads_TaskPurgeKeepsGracePeriod(t *testing.T) {
	const name = "0-sr-task"
	utilsFailed := podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")
	utilsFailed.Status.ContainerStatuses = append(utilsFailed.Status.ContainerStatuses, corev1.ContainerStatus{
		Name:  common.UtilsContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	})
	pastMaxRuntime := podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")
	pastMaxRuntime.Status.StartTime = &metav1.Time{Time: time.Now().Add(-2 * time.Hour)}
	tests := []struct {
		name string
		pod  *corev1.Pod
	}{
		{name: "the utils container exited with an error", pod: utilsFailed},
		{name: "the task ran past its maximum runtime", pod: pastMaxRuntime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients, client := podReadsAPIServer(tt.pod)
			enforceDeletePreconditions(client, nil)
			kb, _ := newPodReadsBackend(clients, podReadsCache(t, tt.pod), featureflag.InstancePodCacheReads)
			reportedAt := metav1.Now()
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))
			req.Spec.Action = common.TaskCreationAction
			req.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
				MaxRuntimeDuration: "PT1H",
				MaxQueuedDuration:  "PT2M",
			}

			updates := runPodReadsPass(ctx, kb, req)

			assert.Equal(t, types.ICMSInstanceTerminated, updates[name].Payload.InstanceState)
			deletes := podDeletes(client)
			require.Len(t, deletes, 1)
			opts := deletes[0].GetDeleteOptions()
			require.NotNil(t, opts.GracePeriodSeconds)
			assert.Zero(t, *opts.GracePeriodSeconds)
			require.NotNil(t, opts.Preconditions)
			assert.Equal(t, apitypes.UID("uid-1"), *opts.Preconditions.UID)
			assert.Equal(t, "100", *opts.Preconditions.ResourceVersion)
			_, exists := livePodExists(t, client, name)
			assert.False(t, exists)
		})
	}
}

// A Pod that is being deleted is left to the termination request to report. A cached one is not
// read live for that, even with its periodic report due, and one a confirming live read finds being
// deleted is not purged.
func TestInstancePodReads_PodBeingDeletedIsNotReported(t *testing.T) {
	const name = "0-sr-deleting"
	beingDeleted := func(p *corev1.Pod) *corev1.Pod {
		p.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		return p
	}
	tests := []struct {
		name         string
		cached, live *corev1.Pod
		reportedAgo  time.Duration
		passes       int
		wantGets     int
	}{
		{
			name:   "in the cache",
			cached: beingDeleted(podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
			live:   beingDeleted(podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
		},
		{
			name:        "in the cache, with its periodic report due",
			cached:      beingDeleted(podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
			live:        beingDeleted(podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")),
			reportedAgo: 6 * time.Minute,
			passes:      3,
		},
		{
			name:     "on the confirming live read",
			cached:   podReadsPullBackOffPod(name, "uid-1", "100"),
			live:     beingDeleted(podReadsPullBackOffPod(name, "uid-1", "200")),
			wantGets: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &podReadsClock{now: time.Now()}
			ctx := core.WithClock(newTestContext(), clock.Now)
			clients, client := podReadsAPIServer(tt.live)
			kb, _ := newPodReadsBackend(clients, podReadsCache(t, tt.cached), featureflag.InstancePodCacheReads)
			reportedAt := metav1.NewTime(clock.now.Add(-tt.reportedAgo))
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

			for range max(tt.passes, 1) {
				assert.Empty(t, runPodReadsPass(ctx, kb, req))
			}

			assert.Equal(t, tt.wantGets, countLivePodGets(client))
			assert.Empty(t, podDeletes(client))
		})
	}
}

// A live read that fails with an error other than NotFound leaves the instance's state unknown, so
// the pass neither reports nor purges it. A failed periodic read does not warn of a stale cache.
func TestInstancePodReads_LiveReadErrorSkipsInstance(t *testing.T) {
	const name = "0-sr-unreadable"
	tests := []struct {
		source      string
		cached      []*corev1.Pod
		reportedAgo time.Duration
	}{
		{source: nvcametrics.InstancePodReadSourceMiss},
		{source: nvcametrics.InstancePodReadSourceConfirm, cached: []*corev1.Pod{podReadsPullBackOffPod(name, "uid-1", "100")}},
		{
			source:      nvcametrics.InstancePodReadSourceHeartbeat,
			cached:      []*corev1.Pod{podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")},
			reportedAgo: 6 * time.Minute,
		},
		{source: nvcametrics.InstancePodReadSourceRefresh, cached: []*corev1.Pod{podReadsRunningPod(name, "uid-1", "100", "198.51.100.7")}},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			clients, client := podReadsAPIServer(podReadsPullBackOffPod(name, "uid-1", "100"))
			client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewInternalError(errors.New("request timed out"))
			})
			kb, bc := newPodReadsBackend(clients, podReadsCache(t, tt.cached...), featureflag.InstancePodCacheReads)
			clock := &podReadsClock{now: time.Now()}
			logCtx, logHook := core.WithTestingLogger(newTestContext())
			ctx := core.WithClock(logCtx, clock.Now)
			// The last live read was a window ago, so a refresh is due.
			bc.instancePodLiveReads.recordLive(name, clock.now.Add(-instancePodRefreshWindow))
			reportedAt := metav1.NewTime(clock.now.Add(-tt.reportedAgo))
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))

			assert.Empty(t, runPodReadsPass(ctx, kb, req))

			assert.Empty(t, podDeletes(client))
			assert.Equal(t, 1, countLivePodGets(client))
			assert.Equal(t, float64(1), podReads(ctx, tt.source, nvcametrics.InstancePodReadResultError))
			assert.Equal(t, float64(1), podAPICalls(ctx))
			assert.Empty(t, staleCacheWarnings(logHook.AllEntries()))
		})
	}
}

// podWatchGate serves the Pod LIST of a client unchanged and holds its watch events behind a gate,
// so a test decides when an informer cache sees a change: closed, the gate acts as a lagging or
// stalled watch. expire ends the current watch with an expired error, as the API server ends a
// watch that fell too far behind, which makes the informer relist.
type podWatchGate struct {
	client  kubernetes.Interface
	started chan struct{}

	mu      sync.Mutex
	closed  bool
	watches []*gatedPodWatch
}

func newPodWatchGate(client kubernetes.Interface) *podWatchGate {
	return &podWatchGate{client: client, started: make(chan struct{}, 8)}
}

func (g *podWatchGate) List(options metav1.ListOptions) (runtime.Object, error) {
	return g.client.CoreV1().Pods(metav1.NamespaceAll).List(context.Background(), options)
}

func (g *podWatchGate) Watch(options metav1.ListOptions) (watch.Interface, error) {
	inner, err := g.client.CoreV1().Pods(metav1.NamespaceAll).Watch(context.Background(), options)
	if err != nil {
		return nil, err
	}
	w := &gatedPodWatch{
		gate:    g,
		inner:   inner,
		out:     make(chan watch.Event),
		wake:    make(chan struct{}, 1),
		expired: make(chan struct{}),
		done:    make(chan struct{}),
	}
	g.mu.Lock()
	g.watches = append(g.watches, w)
	g.mu.Unlock()
	go w.run()
	select {
	case g.started <- struct{}{}:
	default:
	}
	return w, nil
}

// IsWatchListSemanticsUnSupported makes the informer list, then watch.
func (g *podWatchGate) IsWatchListSemanticsUnSupported() bool { return true }

func (g *podWatchGate) hold()    { g.setClosed(true) }
func (g *podWatchGate) release() { g.setClosed(false) }

func (g *podWatchGate) setClosed(closed bool) {
	g.mu.Lock()
	g.closed = closed
	watches := slices.Clone(g.watches)
	g.mu.Unlock()
	for _, w := range watches {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (g *podWatchGate) isClosed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

// expire ends the latest watch with an expired error, dropping the events it holds.
func (g *podWatchGate) expire() {
	g.mu.Lock()
	w := g.watches[len(g.watches)-1]
	g.mu.Unlock()
	w.expireOnce.Do(func() { close(w.expired) })
}

func (g *podWatchGate) waitForWatch(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the informer did not start a watch")
	}
}

type gatedPodWatch struct {
	gate       *podWatchGate
	inner      watch.Interface
	out        chan watch.Event
	wake       chan struct{}
	expired    chan struct{}
	expireOnce sync.Once
	done       chan struct{}
	stopOnce   sync.Once
}

func (w *gatedPodWatch) run() {
	defer close(w.out)
	var held []watch.Event
	for {
		var out chan watch.Event
		var next watch.Event
		if len(held) > 0 && !w.gate.isClosed() {
			out, next = w.out, held[0]
		}
		select {
		case ev, ok := <-w.inner.ResultChan():
			if !ok {
				return
			}
			held = append(held, ev)
		case out <- next:
			held = held[1:]
		case <-w.wake:
		case <-w.expired:
			select {
			case w.out <- watch.Event{Type: watch.Error, Object: &apierrors.NewResourceExpired("too old resource version").ErrStatus}:
			case <-w.done:
			}
			return
		case <-w.done:
			return
		}
	}
}

func (w *gatedPodWatch) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
		w.inner.Stop()
	})
}

func (w *gatedPodWatch) ResultChan() <-chan watch.Event { return w.out }

// startGatedPodInformer runs a Pod informer behind gate and waits for its cache and its watch.
func startGatedPodInformer(ctx context.Context, t *testing.T, gate *podWatchGate) listersv1.PodNamespaceLister {
	t.Helper()
	informer := cache.NewSharedIndexInformer(gate, &corev1.Pod{}, 0,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	go informer.RunWithContext(ctx)
	require.True(t, cache.WaitForCacheSync(ctx.Done(), informer.HasSynced))
	gate.waitForWatch(t)
	return listersv1.NewPodLister(informer.GetIndexer()).Pods(RequestsNamespace)
}

// A Pod is created before its instance is recorded, so the status loop can see an instance whose
// Pod the informer cache has not caught up with. It must read the Pod live, not report it gone.
func TestInstancePodReads_NewPodMissingFromLaggingCacheIsReadLive(t *testing.T) {
	// The status loop's clock stands still, so no refresh falls between the passes.
	clock := &podReadsClock{now: time.Now()}
	ctx, cancel := context.WithCancel(core.WithClock(newTestContext(), clock.Now))
	t.Cleanup(cancel)
	const name = "0-sr-new"
	clients, client := podReadsAPIServer()
	gate := newPodWatchGate(client)
	lister := startGatedPodInformer(ctx, t, gate)
	kb, _ := newPodReadsBackend(clients, lister, featureflag.InstancePodCacheReads)

	gate.hold()
	_, err := client.CoreV1().Pods(RequestsNamespace).Create(ctx, podReadsStartingPod(name, "uid-1", "1"), metav1.CreateOptions{})
	require.NoError(t, err)
	req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceStateNoStatus, nil))

	_, err = lister.Get(name)
	require.True(t, apierrors.IsNotFound(err), "the lagging cache must not have the Pod yet")
	updates := runPodReadsPass(ctx, kb, req)
	assert.Equal(t, types.ICMSInstanceStarted, updates[name].Payload.InstanceState)
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceCache, nvcametrics.InstancePodReadResultNotFound))
	assert.Equal(t, float64(1), podReads(ctx, nvcametrics.InstancePodReadSourceMiss, nvcametrics.InstancePodReadResultFound))

	gate.release()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := lister.Get(name)
		assert.NoError(c, err)
	}, 5*time.Second, 10*time.Millisecond)
	gets := countLivePodGets(client)
	assert.Empty(t, runPodReadsPass(ctx, kb, req), "the reported state is unchanged")
	assert.Equal(t, gets, countLivePodGets(client), "once cached, an unchanged Pod is not read live")
}

// When the watch ends with an expired error, the informer relists. Neither while the watch is
// stalled nor across the relist does an existing Pod get reported as terminated, and afterwards
// the cache, and what is reported, match the API server.
func TestInstancePodReads_WatchExpiredRelistKeepsStatusCorrect(t *testing.T) {
	// The status loop's clock stands still, so no refresh falls between the passes.
	clock := &podReadsClock{now: time.Now()}
	ctx, cancel := context.WithCancel(core.WithClock(newTestContext(), clock.Now))
	t.Cleanup(cancel)
	const (
		steady    = "0-sr-steady"
		recovers  = "1-sr-recovers"
		created   = "2-sr-created"
		deleted   = "3-sr-deleted"
		ipAddress = "198.51.100.7"
	)
	clients, client := podReadsAPIServer(
		podReadsRunningPod(steady, "uid-steady", "1", ipAddress),
		podReadsStartingPod(recovers, "uid-recovers", "1"),
		podReadsRunningPod(deleted, "uid-deleted", "1", ipAddress),
	)
	gate := newPodWatchGate(client)
	lister := startGatedPodInformer(ctx, t, gate)
	kb, _ := newPodReadsBackend(clients, lister, featureflag.InstancePodCacheReads)
	reportedAt := metav1.Now()
	req := podReadsRequest(
		podReadsInstance(steady, types.ICMSInstanceRunning, &reportedAt),
		podReadsInstance(recovers, types.ICMSInstanceStarted, &reportedAt),
		podReadsInstance(deleted, types.ICMSInstanceRunning, &reportedAt),
	)
	assertNotTerminated := func(t assert.TestingT, updates map[string]types.ICMSRequestUpdateInfo) {
		for id, upd := range updates {
			if id != deleted {
				assert.NotEqual(t, types.ICMSInstanceTerminated, upd.Payload.InstanceState, "instance %s", id)
			}
		}
	}

	// The watch stalls while Pods change.
	gate.hold()
	_, err := client.CoreV1().Pods(RequestsNamespace).UpdateStatus(ctx,
		podReadsRunningPod(recovers, "uid-recovers", "2", ipAddress), metav1.UpdateOptions{})
	require.NoError(t, err)
	_, err = client.CoreV1().Pods(RequestsNamespace).Create(ctx, podReadsStartingPod(created, "uid-created", "1"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, client.CoreV1().Pods(RequestsNamespace).Delete(ctx, deleted, metav1.DeleteOptions{}))
	req.Status.Instances[created] = podReadsInstance(created, types.ICMSInstanceStateNoStatus, nil)

	updates := runPodReadsPass(ctx, kb, req)
	assertNotTerminated(t, updates)
	assert.Equal(t, types.ICMSInstanceStarted, updates[created].Payload.InstanceState, "a Pod the cache misses is read live")
	stale, err := lister.Get(recovers)
	require.NoError(t, err)
	require.Equal(t, corev1.PodPending, stale.Status.Phase, "the stalled watch keeps the cache stale")

	// The watch ends with an expired error; the informer relists.
	lists := podLists(client)
	gate.expire()
	gate.waitForWatch(t)
	assert.Greater(t, podLists(client), lists, "the informer relists after the expired watch")
	var relistUpdates []map[string]types.ICMSRequestUpdateInfo
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		updates := runPodReadsPass(ctx, kb, req)
		relistUpdates = append(relistUpdates, updates)
		_, deletedErr := lister.Get(deleted)
		assert.True(c, apierrors.IsNotFound(deletedErr))
		p, err := lister.Get(recovers)
		if assert.NoError(c, err) {
			assert.Equal(c, corev1.PodRunning, p.Status.Phase)
		}
		_, err = lister.Get(created)
		assert.NoError(c, err)
	}, 10*time.Second, 20*time.Millisecond)
	// Every pass across the relist, not only the last one, keeps existing Pods from terminating.
	for _, updates := range relistUpdates {
		assertNotTerminated(t, updates)
	}

	// After the relist, each instance reports its live state.
	runPodReadsPass(ctx, kb, req)
	assert.Equal(t, string(types.ICMSInstanceRunning), req.Status.Instances[steady].LastReportedStatus)
	assert.Equal(t, string(types.ICMSInstanceRunning), req.Status.Instances[recovers].LastReportedStatus)
	assert.Equal(t, string(types.ICMSInstanceStarted), req.Status.Instances[created].LastReportedStatus)
	assert.Equal(t, string(types.ICMSInstanceTerminated), req.Status.Instances[deleted].LastReportedStatus,
		"only the Pod that is gone from the API server is reported as terminated")
	gets := livePodGets(client)
	assert.Empty(t, runPodReadsPass(ctx, kb, req))
	for _, name := range []string{steady, recovers, created} {
		assert.Equal(t, gets[name], livePodGets(client)[name], "the relisted cache serves %s", name)
	}
}

func TestInstancePodLiveReads(t *testing.T) {
	start := time.Now()
	t.Run("an instance seen for the first time is not due", func(t *testing.T) {
		var r instancePodLiveReads
		assert.False(t, r.refreshDue("0-sr-a", start))
	})
	t.Run("an instance is due once its refresh slot passes, and a live read resets it", func(t *testing.T) {
		var r instancePodLiveReads
		require.False(t, r.refreshDue("0-sr-a", start))
		slot := lastRefreshSlot(start, refreshSlotOffset("0-sr-a")).Add(instancePodRefreshWindow)
		assert.False(t, r.refreshDue("0-sr-a", slot.Add(-time.Nanosecond)))
		assert.True(t, r.refreshDue("0-sr-a", slot))
		r.recordLive("0-sr-a", slot)
		assert.False(t, r.refreshDue("0-sr-a", slot.Add(instancePodRefreshWindow-time.Nanosecond)))
		assert.True(t, r.refreshDue("0-sr-a", slot.Add(instancePodRefreshWindow)))
	})
	t.Run("an instance the status loop is slow to revisit is due on its next visit", func(t *testing.T) {
		var r instancePodLiveReads
		r.recordLive("0-sr-a", start)
		// Another instance keeps the tracker in use, so it prunes, while 0-sr-a waits two windows.
		revisit := start.Add(2 * instancePodRefreshWindow)
		for now := start; now.Before(revisit); now = now.Add(podReadsPassInterval) {
			r.refreshDue("1-sr-b", now)
		}
		assert.True(t, r.refreshDue("0-sr-a", revisit))
	})
	t.Run("the time without a live read stays under a window plus the time between visits", func(t *testing.T) {
		for _, every := range []time.Duration{podReadsPassInterval, time.Minute, 110 * time.Second, 3 * time.Minute} {
			var r instancePodLiveReads
			r.recordLive("0-sr-a", start)
			last := start
			for now := start; now.Before(start.Add(time.Hour)); now = now.Add(every) {
				assert.Less(t, now.Sub(last), instancePodRefreshWindow+every, "visits every %v", every)
				if r.refreshDue("0-sr-a", now) {
					r.recordLive("0-sr-a", now)
					last = now
				}
			}
		}
	})
	t.Run("an instance forgotten before each visit is never due", func(t *testing.T) {
		var r instancePodLiveReads
		r.recordLive("0-sr-a", start)
		// Each visit comes more than the retention after the last and prunes the instance before
		// looking it up, so it starts a new window every time.
		for visit := 1; visit <= 10; visit++ {
			now := start.Add(time.Duration(visit) * (instancePodLiveReadRetention + time.Minute))
			assert.False(t, r.refreshDue("0-sr-a", now), "visit %d", visit)
		}
	})
	t.Run("instances not seen for the retention are forgotten", func(t *testing.T) {
		var r instancePodLiveReads
		r.recordLive("0-sr-a", start)
		prune := start.Add(instancePodLiveReadRetention - time.Second)
		r.refreshDue("1-sr-b", prune)
		assert.Contains(t, r.instances, "0-sr-a")
		r.refreshDue("1-sr-b", prune.Add(instancePodRefreshWindow))
		assert.Len(t, r.instances, 1)
		assert.Contains(t, r.instances, "1-sr-b")
	})
	t.Run("is safe for concurrent use", func(t *testing.T) {
		// The status loop serves requests from a pool of goroutines. Run with -race.
		var r instancePodLiveReads
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 100 {
					id := fmt.Sprintf("%d-sr-a", i%8)
					now := start.Add(time.Duration(i) * podReadsPassInterval)
					r.refreshDue(id, now)
					r.recordLive(id, now)
				}
			}()
		}
		wg.Wait()
		assert.NotEmpty(t, r.instances)
	})
}

type podReadsRoundTripper func(*http.Request) (*http.Response, error)

func (f podReadsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// createEnvtestPod creates a Pod that is not bound to a node, so a DELETE removes it at once, and
// gives it status unless status is empty.
func createEnvtestPod(ctx context.Context, t *testing.T, client kubernetes.Interface, name string,
	status corev1.PodStatus,
) *corev1.Pod {
	t.Helper()
	pod, err := client.CoreV1().Pods(RequestsNamespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: RequestsNamespace},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "inference", Image: "registry.example.invalid/app:1"}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	if status.Phase == "" {
		return pod
	}
	pod.Status = status
	pod, err = client.CoreV1().Pods(RequestsNamespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	return pod
}

// Against a real API server: a new Pod that a lagging informer cache misses is read live, and a
// purge sends delete preconditions that the API server enforces, so that neither a replacement Pod
// with the same name nor a Pod that changed after the confirming read is deleted.
func TestInstancePodReads_Envtest(t *testing.T) {
	binAssetsDir := os.Getenv("KUBEBUILDER_ASSETS")
	if binAssetsDir == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: binAssetsDir}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	admin, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	_, err = admin.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: RequestsNamespace}}, metav1.CreateOptions{})
	require.NoError(t, err)

	// The status loop's client runs beforeDelete, once, just before it sends a Pod DELETE.
	var (
		hookMu       sync.Mutex
		beforeDelete func()
	)
	loopCfg := rest.CopyConfig(cfg)
	loopCfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return podReadsRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/pods/") {
				hookMu.Lock()
				hook := beforeDelete
				beforeDelete = nil
				hookMu.Unlock()
				if hook != nil {
					hook()
				}
			}
			return rt.RoundTrip(req)
		})
	}
	loopClient, err := kubernetes.NewForConfig(loopCfg)
	require.NoError(t, err)
	gate := newPodWatchGate(admin)
	lister := startGatedPodInformer(ctx, t, gate)
	kb, _ := newPodReadsBackend(&kubeclients.KubeClients{K8s: loopClient}, lister, featureflag.InstancePodCacheReads)

	t.Run("a new Pod the lagging cache has not seen is read live", func(t *testing.T) {
		gate.hold()
		defer gate.release()
		pod := createEnvtestPod(ctx, t, admin, "0-sr-new", corev1.PodStatus{})
		req := podReadsRequest(podReadsInstance(pod.Name, types.ICMSInstanceStateNoStatus, nil))
		_, err := lister.Get(pod.Name)
		require.True(t, apierrors.IsNotFound(err), "the lagging cache must not have the Pod yet")

		updates := runPodReadsPass(ctx, kb, req)

		assert.Equal(t, types.ICMSInstanceStarted, updates[pod.Name].Payload.InstanceState)
	})

	running := podReadsRunningPod("", "", "", "").Status
	running.HostIP, running.PodIP = "", ""
	purgeCases := []struct {
		name       string
		change     func(t *testing.T, name string)
		wantPurged bool
	}{
		{name: "the Pod as read is purged", wantPurged: true},
		{
			name: "a replacement Pod with the same name is kept",
			change: func(t *testing.T, name string) {
				require.NoError(t, admin.CoreV1().Pods(RequestsNamespace).Delete(ctx, name, metav1.DeleteOptions{}))
				createEnvtestPod(ctx, t, admin, name, running)
			},
		},
		{
			name: "a Pod that recovered after it was read is kept",
			change: func(t *testing.T, name string) {
				pod, err := admin.CoreV1().Pods(RequestsNamespace).Get(ctx, name, metav1.GetOptions{})
				require.NoError(t, err)
				pod.Status = running
				_, err = admin.CoreV1().Pods(RequestsNamespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				require.NoError(t, err)
			},
		},
	}
	for i, tc := range purgeCases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("%d-sr-purge", i+1)
			pod := createEnvtestPod(ctx, t, admin, name, podReadsPullBackOffPod("", "", "").Status)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				cached, err := lister.Get(name)
				if assert.NoError(c, err) {
					assert.Equal(c, pod.ResourceVersion, cached.ResourceVersion)
				}
			}, 5*time.Second, 10*time.Millisecond)
			if tc.change != nil {
				hookMu.Lock()
				beforeDelete = func() { tc.change(t, name) }
				hookMu.Unlock()
			}
			reportedAt := metav1.Now()
			req := podReadsRequest(podReadsInstance(name, types.ICMSInstanceStarted, &reportedAt))

			updates := runPodReadsPass(ctx, kb, req)

			live, err := admin.CoreV1().Pods(RequestsNamespace).Get(ctx, name, metav1.GetOptions{})
			if tc.wantPurged {
				assert.True(t, apierrors.IsNotFound(err), "the Pod must be deleted, got %v", err)
				assert.Equal(t, types.ICMSInstanceTerminated, updates[name].Payload.InstanceState)
				assert.Equal(t, types.ICMSInstanceFailedImagePullIssues, updates[name].Payload.TerminationCause)
				return
			}
			require.NoError(t, err, "the Pod must not be deleted")
			assert.Empty(t, updates, "nothing is reported for a Pod that changed before it could be purged")

			// The next pass decides again, from the Pod as it is now, once the cache has it.
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				cached, err := lister.Get(name)
				if assert.NoError(c, err) {
					assert.Equal(c, live.ResourceVersion, cached.ResourceVersion)
				}
			}, 5*time.Second, 10*time.Millisecond)
			updates = runPodReadsPass(ctx, kb, req)
			assert.Equal(t, types.ICMSInstanceRunning, updates[name].Payload.InstanceState)
			after, err := admin.CoreV1().Pods(RequestsNamespace).Get(ctx, name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, live.UID, after.UID)
		})
	}
}
