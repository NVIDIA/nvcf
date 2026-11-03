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

package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

const (
	smbPendingBadImage = "stg.nvcr.io/nv-cf/nvcf-core/samba:ar374-missing-20260924"
)

// smbDefaultTimes holds the default NVCA time thresholds that the SMB server pod timeout reuses.
var smbDefaultTimes = (&nvcak8sutil.TimeConfig{}).Complete()

// smbPendingNow is the fixed reconcile time of the timeout tests. Conditions that the
// reconciler creates are stamped by the wall clock, so only tests that seed an old
// condition use it.
var smbPendingNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func smbPendingRun(t *testing.T, name string, podStatus corev1.PodStatus) *nvcav1new.StorageRequest {
	t.Helper()
	res, stCopy, err := smbPendingReconcile(t, name, podStatus, nil, time.Now())
	// A pending Pod must keep retrying, never fail or return an error.
	require.NoError(t, err)
	assert.NotZero(t, res.RequeueAfter, "pending pod must requeue")
	assert.Equal(t, nvcav1new.StorageInitRunning, stCopy.Status.Phase, "pending pod must not fail the request")
	return stCopy
}

// smbPendingReconcile runs one InitRunning reconcile against a pod with the given
// status, starting from the given conditions.
func smbPendingReconcile(t *testing.T, name string, podStatus corev1.PodStatus,
	conds []metav1.Condition, now time.Time, opts ...func(*Reconciler),
) (reconcile.Result, *nvcav1new.StorageRequest, error) {
	t.Helper()
	sch := newTestScheme()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "smb-pending-" + name}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: SMBServerPodName, Namespace: ns.Name},
		Status:     podStatus,
	}
	k8sClient := clientfake.NewClientBuilder().
		WithScheme(sch).
		WithRESTMapper(newTestRESTMapper(sch)).
		WithObjects(ns, pod).
		WithStatusSubresource(&nvcav1new.StorageRequest{}).
		Build()
	st := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shared-storage", Namespace: ns.Name,
			Labels: map[string]string{"function-version-id": "test-function"},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage: smbPendingBadImage,
				Size:              resource.MustParse("1Gi"),
			},
		},
		Status: nvcav1new.StorageRequestStatus{Phase: nvcav1new.StorageInitRunning, Conditions: conds},
	}
	stCopy := st.DeepCopy()
	r := &Reconciler{Client: k8sClient, fff: &featureflagmock.Fetcher{}, nowFunc: func() time.Time { return now }}
	for _, opt := range opts {
		opt(r)
	}
	res, err := r.doSharedStorageSMB(newTestContext(), st, stCopy)
	return res, stCopy, err
}

func TestSMBPodPendingSurfacesReason(t *testing.T) {
	for _, tt := range []struct {
		name          string
		status        corev1.PodStatus
		wantReason    string
		wantInMessage []string
	}{
		{
			name: "image-pull-backoff",
			status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: SMBServerContainerName, Image: smbPendingBadImage,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ImagePullBackOff",
						Message: "Back-off pulling image " + smbPendingBadImage,
					}},
				}},
			},
			wantReason:    ConditionReasonImagePullFailed,
			wantInMessage: []string{smbPendingBadImage},
		},
		{
			name: "err-image-pull",
			status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: SMBServerContainerName, Image: smbPendingBadImage,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason: "ErrImagePull", Message: "not found",
					}},
				}},
			},
			wantReason:    ConditionReasonImagePullFailed,
			wantInMessage: []string{smbPendingBadImage, "not found"},
		},
		{
			name: "unschedulable",
			status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
					Message: "0/3 nodes are available: 3 Insufficient cpu.",
				}},
			},
			wantReason:    ConditionReasonUnschedulable,
			wantInMessage: []string{"3 Insufficient cpu"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := smbPendingRun(t, tt.name, tt.status)
			cond := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeSMBServerReady)
			require.NotNil(t, cond, "no %s condition recorded for a pending SMB pod", ConditionTypeSMBServerReady)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			for _, s := range tt.wantInMessage {
				assert.Contains(t, cond.Message, s)
			}
		})
	}
}

// A pod that is just starting (scheduled, container creating) is normal and must
// not be reported as a problem.
func TestSMBPodPendingNormalStartIsQuiet(t *testing.T) {
	st := smbPendingRun(t, "quiet", corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionTrue,
		}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  SMBServerContainerName,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
		}},
	})
	cond := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeSMBServerReady)
	if cond != nil {
		assert.NotEqual(t, metav1.ConditionFalse, cond.Status)
	}
}

// The condition must flip to True once the pod is up, so a stale failure reason
// is never left on a healthy StorageRequest.
func TestSMBPodRecoveryMarksConditionTrue(t *testing.T) {
	sch := newTestScheme()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "smb-pending-recovered"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: SMBServerPodName, Namespace: ns.Name},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			PodIP:      "10.0.0.1",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	k8sClient := clientfake.NewClientBuilder().
		WithScheme(sch).
		WithRESTMapper(newTestRESTMapper(sch)).
		WithObjects(ns, pod).
		WithStatusSubresource(&nvcav1new.StorageRequest{}).
		Build()
	st := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shared-storage", Namespace: ns.Name,
			Labels: map[string]string{"function-version-id": "test-function"},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage: smbPendingBadImage,
				Size:              resource.MustParse("1Gi"),
			},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StorageInitRunning,
			Conditions: []metav1.Condition{{
				Type: ConditionTypeSMBServerReady, Status: metav1.ConditionFalse,
				Reason: ConditionReasonImagePullFailed, Message: "stale",
			}},
		},
	}
	stCopy := st.DeepCopy()
	r := &Reconciler{Client: k8sClient, fff: &featureflagmock.Fetcher{}, nowFunc: func() time.Time { return time.Now() }}
	_, _ = r.doSharedStorageSMB(newTestContext(), st, stCopy)
	cond := meta.FindStatusCondition(stCopy.Status.Conditions, ConditionTypeSMBServerReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, ConditionReasonPodReady, cond.Reason)
}

// A pod that stays unable to start past the timeout fails the request with the
// cause, so the task reports it. Inside the timeout the request keeps retrying.
func TestSMBPodPendingFailsAfterTimeout(t *testing.T) {
	unschedulable := corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: "0/3 nodes are available: 3 Insufficient cpu.",
		}},
	}
	stuckSince := func(d time.Duration) []metav1.Condition {
		return []metav1.Condition{{
			Type: ConditionTypeSMBServerReady, Status: metav1.ConditionFalse, Reason: ConditionReasonUnschedulable,
			LastTransitionTime: metav1.NewTime(smbPendingNow.Add(-d)),
		}}
	}

	t.Run("within-timeout-keeps-retrying", func(t *testing.T) {
		res, st, err := smbPendingReconcile(t, "within", unschedulable, stuckSince(smbDefaultTimes.PodScheduledThreshold/2), smbPendingNow)
		require.NoError(t, err)
		assert.NotZero(t, res.RequeueAfter)
		assert.Equal(t, nvcav1new.StorageInitRunning, st.Status.Phase)
	})

	t.Run("past-timeout-fails-with-cause", func(t *testing.T) {
		_, st, err := smbPendingReconcile(t, "past", unschedulable, stuckSince(smbDefaultTimes.PodScheduledThreshold+time.Minute), smbPendingNow)
		require.Error(t, err)
		assert.True(t, isTerminal(err), "failure must be terminal so it is not retried")
		assert.Contains(t, err.Error(), "3 Insufficient cpu")
		assert.Equal(t, nvcav1new.StorageFailed, st.Status.Phase)

		cond := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeSMBServerReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)

		v2 := &nvcav2beta1.StorageRequest{Status: nvcav2beta1.StorageRequestStatus{
			Phase: nvcav2beta1.StorageFailed, Conditions: st.Status.Conditions,
		}}
		assert.Contains(t, GetStorageRequestErrorLog(v2), "3 Insufficient cpu")
	})
}

func smbStaleCondition(reason string) []metav1.Condition {
	return []metav1.Condition{{
		Type: ConditionTypeSMBServerReady, Status: metav1.ConditionFalse, Reason: reason,
		Message: "stale", LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute)),
	}}
}

// Once the pod is past the stuck state the old reason must go, whether the pod is back to
// a normal start or already Running but not Ready.
func TestSMBPodStaleConditionIsCleared(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status corev1.PodStatus
	}{
		{
			name: "pending-container-creating",
			status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionTrue,
				}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  SMBServerContainerName,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
				}},
			},
		},
		{
			name: "running-not-ready",
			status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, st, err := smbPendingReconcile(t, tt.name, tt.status, smbStaleCondition(ConditionReasonImagePullFailed), time.Now())
			require.NoError(t, err)
			assert.NotZero(t, res.RequeueAfter)
			assert.Equal(t, nvcav1new.StorageInitRunning, st.Status.Phase)
			assert.Nil(t, meta.FindStatusCondition(st.Status.Conditions, ConditionTypeSMBServerReady),
				"stale %s condition must be cleared", ConditionTypeSMBServerReady)
		})
	}
}

// The failure happens only after the timeout has fully elapsed: exactly at the timeout the
// request still retries, just past it the request fails.
func TestSMBPodPendingTimeoutBoundary(t *testing.T) {
	unschedulable := corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: "0/3 nodes are available",
		}},
	}
	seed := func(d time.Duration) []metav1.Condition {
		return []metav1.Condition{{
			Type: ConditionTypeSMBServerReady, Status: metav1.ConditionFalse, Reason: ConditionReasonUnschedulable,
			LastTransitionTime: metav1.NewTime(smbPendingNow.Add(-d)),
		}}
	}

	res, st, err := smbPendingReconcile(t, "at-boundary", unschedulable, seed(smbDefaultTimes.PodScheduledThreshold), smbPendingNow)
	require.NoError(t, err)
	assert.NotZero(t, res.RequeueAfter)
	assert.Equal(t, nvcav1new.StorageInitRunning, st.Status.Phase)

	_, st, err = smbPendingReconcile(t, "just-past", unschedulable, seed(smbDefaultTimes.PodScheduledThreshold+time.Second), smbPendingNow)
	require.Error(t, err)
	assert.Equal(t, nvcav1new.StorageFailed, st.Status.Phase)
}

// The image falls back to the matching container in the pod spec, init containers included,
// and a missing match or an empty spec must not panic.
func TestSMBPodPendingCauseImageLookup(t *testing.T) {
	waiting := func(name string) corev1.ContainerStatus {
		return corev1.ContainerStatus{
			Name:  name,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "not found"}},
		}
	}
	for _, tt := range []struct {
		name         string
		pod          corev1.Pod
		wantContains string
		wantAbsent   string
	}{
		{
			name: "regular-container",
			pod: corev1.Pod{
				Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "smb-server", Image: "registry.example/smb:1"}}},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{waiting("smb-server")}},
			},
			wantContains: "registry.example/smb:1",
		},
		{
			name: "init-container-not-first-regular-container",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{Name: "setup", Image: "registry.example/setup:2"}},
					Containers:     []corev1.Container{{Name: "smb-server", Image: "registry.example/smb:1"}},
				},
				Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{waiting("setup")}},
			},
			wantContains: "registry.example/setup:2",
			wantAbsent:   "registry.example/smb:1",
		},
		{
			name: "no-matching-container",
			pod: corev1.Pod{
				Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "other", Image: "registry.example/other:3"}}},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{waiting("smb-server")}},
			},
			wantContains: "cannot pull its image",
			wantAbsent:   "registry.example/other:3",
		},
		{
			name: "empty-spec",
			pod: corev1.Pod{
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{waiting("smb-server")}},
			},
			wantContains: "cannot pull its image",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reason, message := smbPodPendingCause(&tt.pod)
			assert.Equal(t, ConditionReasonImagePullFailed, reason)
			assert.Contains(t, message, tt.wantContains)
			if tt.wantAbsent != "" {
				assert.NotContains(t, message, tt.wantAbsent)
			}
		})
	}
}

// The timeout depends on the cause: image pull failures use MaxImagePullErrorThreshold and
// unschedulable pods use PodScheduledThreshold, and both can be overridden through the
// time config like the same thresholds elsewhere in NVCA.
func TestSMBPodPendingTimeoutPerCause(t *testing.T) {
	imagePull := corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name: SMBServerContainerName, Image: smbPendingBadImage,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "back-off"}},
		}},
	}
	unschedulable := corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: "0/3 nodes are available",
		}},
	}
	seed := func(reason string, d time.Duration) []metav1.Condition {
		return []metav1.Condition{{
			Type: ConditionTypeSMBServerReady, Status: metav1.ConditionFalse, Reason: reason,
			LastTransitionTime: metav1.NewTime(smbPendingNow.Add(-d)),
		}}
	}
	withTimes := func(tc nvcak8sutil.TimeConfig) func(*Reconciler) {
		return func(r *Reconciler) { r.k8sTimeConfig = tc.Complete() }
	}

	// Defaults: image pull fails after MaxImagePullErrorThreshold, well before an
	// unschedulable pod reaches PodScheduledThreshold.
	stuck := smbDefaultTimes.MaxImagePullErrorThreshold + time.Second
	require.Less(t, smbDefaultTimes.MaxImagePullErrorThreshold, smbDefaultTimes.PodScheduledThreshold)

	_, st, err := smbPendingReconcile(t, "pull-default", imagePull, seed(ConditionReasonImagePullFailed, stuck), smbPendingNow)
	require.Error(t, err)
	assert.Equal(t, nvcav1new.StorageFailed, st.Status.Phase)

	res, st, err := smbPendingReconcile(t, "unsched-default", unschedulable, seed(ConditionReasonUnschedulable, stuck), smbPendingNow)
	require.NoError(t, err, "unschedulable must not use the shorter image pull threshold")
	assert.NotZero(t, res.RequeueAfter)
	assert.Equal(t, nvcav1new.StorageInitRunning, st.Status.Phase)

	// Overrides: a longer image pull threshold keeps retrying where the default would fail,
	// and a shorter scheduling threshold fails where the default would retry.
	res, st, err = smbPendingReconcile(t, "pull-override", imagePull, seed(ConditionReasonImagePullFailed, stuck), smbPendingNow,
		withTimes(nvcak8sutil.TimeConfig{MaxImagePullErrorThreshold: 5 * time.Minute}))
	require.NoError(t, err)
	assert.NotZero(t, res.RequeueAfter)
	assert.Equal(t, nvcav1new.StorageInitRunning, st.Status.Phase)

	_, st, err = smbPendingReconcile(t, "unsched-override", unschedulable, seed(ConditionReasonUnschedulable, stuck), smbPendingNow,
		withTimes(nvcak8sutil.TimeConfig{PodScheduledThreshold: 30 * time.Second}))
	require.Error(t, err)
	assert.Equal(t, nvcav1new.StorageFailed, st.Status.Phase)
}

// A change of cause restarts the timer. SetStatusCondition alone would keep the old
// LastTransitionTime, so a pod that was unschedulable for a while and then hits an image pull
// failure would fail at once on the shorter image pull threshold.
func TestSMBPodPendingCauseChangeRestartsTimer(t *testing.T) {
	imagePull := corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name: SMBServerContainerName, Image: smbPendingBadImage,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "back-off"}},
		}},
	}
	unschedulable := corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: "0/3 nodes are available",
		}},
	}
	// Stuck for longer than the image pull threshold, but well inside the scheduling one.
	stuckFor := smbDefaultTimes.MaxImagePullErrorThreshold + 4*time.Minute
	require.Less(t, stuckFor, smbDefaultTimes.PodScheduledThreshold)
	seed := func(reason string) []metav1.Condition {
		return []metav1.Condition{{
			Type: ConditionTypeSMBServerReady, Status: metav1.ConditionFalse, Reason: reason,
			Message: "old", LastTransitionTime: metav1.NewTime(time.Now().Add(-stuckFor)),
		}}
	}

	for _, tt := range []struct {
		name       string
		from, to   string
		podStatus  corev1.PodStatus
		wantReason string
	}{
		{"unschedulable-to-image-pull", ConditionReasonUnschedulable, ConditionReasonImagePullFailed, imagePull, ConditionReasonImagePullFailed},
		{"image-pull-to-unschedulable", ConditionReasonImagePullFailed, ConditionReasonUnschedulable, unschedulable, ConditionReasonUnschedulable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().Add(-time.Second)
			res, st, err := smbPendingReconcile(t, tt.name, tt.podStatus, seed(tt.from), time.Now())
			require.NoError(t, err, "a new cause must not inherit the old timer")
			assert.NotZero(t, res.RequeueAfter)
			assert.Equal(t, nvcav1new.StorageInitRunning, st.Status.Phase)
			cond := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeSMBServerReady)
			require.NotNil(t, cond)
			assert.Equal(t, tt.wantReason, cond.Reason)
			assert.True(t, cond.LastTransitionTime.Time.After(before), "LastTransitionTime must restart on a cause change")
		})
	}

	// The same cause keeps its timer, so a persistent failure still times out.
	_, st, err := smbPendingReconcile(t, "same-cause", imagePull, seed(ConditionReasonImagePullFailed), time.Now())
	require.Error(t, err)
	assert.Equal(t, nvcav1new.StorageFailed, st.Status.Phase)
}
