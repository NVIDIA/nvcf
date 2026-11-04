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
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/dsx-ai-factory/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/dsx-ai-factory/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	fakebartclient "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	nvcastorage "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// A writer Job that exhausts its backoff limit makes NVCA fall back to
// non-cached workers. Later passes over the same request must keep that
// decision instead of creating the writer Job again.
func TestSetupContainerModelCachingKeepsFallbackAcrossPasses(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	const (
		ns      = "nvcf-backend"
		handle  = "test-cache-handle"
		jobName = "writer-job-" + handle
	)

	rwPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-pvc-" + handle, Namespace: ns},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	newJob := func(failed int32) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: ns},
			Spec:       batchv1.JobSpec{BackoffLimit: ptr.To(int32(3))},
			Status:     batchv1.JobStatus{Failed: failed},
		}
	}
	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "sr-test", Namespace: ns},
		Spec: nvcav2beta1.ICMSRequestSpec{
			TaskDetails: task.Details{TaskID: "test-task"},
		},
	}
	mf := func(obj client.Object) { obj.SetNamespace(ns) }

	// A backend without the agent's background workers, so nothing else touches the request.
	h, k8s := rwxTestBackend(req, rwPVC, newJob(4))
	bart := h.clients.BART

	podMF, _, err := h.setupContainerModelCaching(ctx, req.DeepCopy(), rwPVC.DeepCopy(), newJob(0), mf)
	require.NoError(t, err, "pass 1 falls back to non-cached workers")
	require.NotNil(t, podMF)
	_, err = k8s.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
	require.True(t, k8serrors.IsNotFound(err), "failed writer Job is cleaned up")

	stored, err := bart.NvcaV2beta1().ICMSRequests(ns).Get(ctx, req.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "true", stored.Annotations[types.ModelCachingFailedAnnotationKey],
		"the fallback is recorded on the request")

	// A later pass, for example after the image credential helper asked for a requeue.
	podMF, _, err = h.setupContainerModelCaching(ctx, stored.DeepCopy(), rwPVC.DeepCopy(), newJob(0), mf)
	require.NoError(t, err, "pass 2 keeps using non-cached workers")
	require.NotNil(t, podMF)
	_, err = k8s.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err), "no writer Job is created after the fallback")
}

func TestSetupContainerModelCachingRetriesFallbackPersistence(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "legacy"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			currentPVC := pvc.DeepCopy()
			currentPVC.Status.Phase = corev1.ClaimBound
			req := rwxRequest(t, nil)
			req.Spec.TaskDetails = task.Details{TaskID: "test-task"}
			if shared {
				req = rwxRequest(t, rwxSelection(t))
				currentPVC = boundSharedClaim(false)
			}
			if req.Annotations == nil {
				req.Annotations = map[string]string{}
			}
			req.Annotations["example.com/other"] = "keep"
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1
			// A backend without the agent's background workers, so nothing else touches the request.
			h, k8s := rwxTestBackend(nil, currentPVC, failed)
			clients := h.clients
			clients.K8s = k8s
			stored, err := clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Create(ctx, req, metav1.CreateOptions{})
			require.NoError(t, err)
			mf := func(obj client.Object) { obj.SetNamespace(req.Namespace) }
			patchFails := true
			patches := 0
			clients.BART.(*fakebartclient.Clientset).PrependReactor("patch", "icmsrequests",
				func(_ k8stesting.Action) (bool, runtime.Object, error) {
					patches++
					if patchFails {
						return true, nil, k8serrors.NewServiceUnavailable("injected patch failure")
					}
					return false, nil, nil
				})

			podMF, _, err := h.setupContainerModelCaching(ctx, stored.DeepCopy(), pvc, job, mf)
			require.Error(t, err, "failed persistence must requeue instead of allowing workers")
			require.Nil(t, podMF)
			_, err = clients.K8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.NoError(t, err, "keep the failed writer until fallback is persisted")
			_, err = clients.K8s.CoreV1().PersistentVolumeClaims(req.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
			require.NoError(t, err, "keep the claim until fallback is persisted")
			stored, err = clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Empty(t, stored.Annotations[types.ModelCachingFailedAnnotationKey])
			// The fallback clears the cache reference on purpose; everything else must be kept.
			keptStatus := stored.Status.DeepCopy()
			keptStatus.CacheReferenceName = ""
			// A concurrent status write the pass below does not see: its copy stays stale, so a full-object
			// write from that copy would lose this value.
			concurrent, err := clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			concurrent.Status.Instances = map[string]nvcav2beta1.InstanceStatus{"concurrent-instance": {}}
			_, err = clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).UpdateStatus(ctx, concurrent, metav1.UpdateOptions{})
			require.NoError(t, err)
			keptStatus.Instances = concurrent.Status.Instances
			// Clearing the cache reference writes status and refreshes this timestamp; compare the rest.
			keptStatus.LastStatusUpdated = nil
			wantStatus, err := json.Marshal(keptStatus)
			require.NoError(t, err)

			patchFails = false
			podMF, claim, err := h.setupContainerModelCaching(ctx, stored.DeepCopy(), pvc, job, mf)
			require.NoError(t, err)
			require.NotNil(t, podMF)
			require.Empty(t, claim)
			pod := &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: ModelVolumeName}}}}
			want := pod.DeepCopy()
			podMF(pod)
			assert.Equal(t, want, pod, "fallback leaves the non-cached pod unchanged")
			_, err = clients.K8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.True(t, k8serrors.IsNotFound(err), "cleanup follows successful persistence")
			stored, err = clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, "true", stored.Annotations[types.ModelCachingFailedAnnotationKey])
			assert.Equal(t, "keep", stored.Annotations["example.com/other"])
			assert.Equal(t, req.Spec, stored.Spec)
			got := stored.Status.DeepCopy()
			got.LastStatusUpdated = nil
			gotStatus, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, string(wantStatus), string(gotStatus), "the merge patch preserves concurrent status updates")

			// A freshly loaded request retains fallback even when writes fail again.
			patchFails = true
			podMF, claim, err = h.setupContainerModelCaching(ctx, stored.DeepCopy(), pvc, job, mf)
			require.NoError(t, err)
			require.NotNil(t, podMF)
			assert.Empty(t, claim)
			assert.Equal(t, 2, patches, "already persisted fallback needs no further patch")
			_, err = clients.K8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.True(t, k8serrors.IsNotFound(err))

			if shared {
				other := req.DeepCopy()
				other.Name = "other-request"
				_, err = clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Create(ctx, other, metav1.CreateOptions{})
				require.NoError(t, err)
				_, _, err = h.setupContainerModelCaching(ctx, other, pvc, job, mf)
				require.Error(t, err, "another request can start caching for the same handle")
				_, err = clients.K8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
				require.NoError(t, err)
				assert.Empty(t, other.Annotations[types.ModelCachingFailedAnnotationKey])
			}
		})
	}
}

// An earlier cleanup may have failed, so a request that already recorded its fallback repeats the
// cleanup while the failed writer is still present, and leaves anything else alone.
func TestSetupContainerModelCachingRetriesCleanupAfterRecordedFallback(t *testing.T) {
	failedJob := func(job *batchv1.Job) *batchv1.Job {
		failed := job.DeepCopy()
		failed.Spec.BackoffLimit = ptr.To(int32(0))
		failed.Status.Failed = 1
		return failed
	}
	tests := []struct {
		name          string
		shared        bool
		populated     bool
		jobFailed     bool
		wantJobGone   bool
		wantClaimGone bool
	}{
		{name: "legacy failed writer", jobFailed: true, wantJobGone: true, wantClaimGone: true},
		{name: "legacy running writer is untouched"},
		{name: "shared failed writer and unpopulated claim", shared: true, jobFailed: true, wantJobGone: true, wantClaimGone: true},
		{name: "shared failed writer keeps a populated claim", shared: true, populated: true, jobFailed: true, wantJobGone: true},
		{name: "shared running writer is untouched", shared: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			claim := pvc.DeepCopy()
			claim.Status.Phase = corev1.ClaimBound
			req := rwxRequest(t, nil)
			if tc.shared {
				req = rwxRequest(t, rwxSelection(t))
				claim = boundSharedClaim(tc.populated)
			}
			if req.Annotations == nil {
				req.Annotations = map[string]string{}
			}
			req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
			writer := job.DeepCopy()
			if tc.jobFailed {
				writer = failedJob(job)
			}
			c, k8s := rwxTestBackend(req, claim, writer)

			podMF, _, err := c.setupContainerModelCaching(ctx, req, pvc, job, func(client.Object) {})
			require.NoError(t, err)
			require.NotNil(t, podMF)

			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.Equal(t, tc.wantJobGone, k8serrors.IsNotFound(err), "writer Job")
			_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, claim.Name, metav1.GetOptions{})
			assert.Equal(t, tc.wantClaimGone, k8serrors.IsNotFound(err), "claim")
		})
	}
}

// Cache setup and the cleanup retry must not interleave: the retry could otherwise delete a
// writer that another request has just recreated for the same cache handle.
func TestModelCacheSetupAndCleanupRetryAreSerialized(t *testing.T) {
	cases := []struct {
		name string
		run  func(c K8sComputeBackend, ctx context.Context, req *nvcav2beta1.ICMSRequest,
			pvc *corev1.PersistentVolumeClaim, job *batchv1.Job, sel *nvcastorage.PersistedModelCacheStorageSelection)
	}{
		{"cleanup retry", func(c K8sComputeBackend, ctx context.Context, req *nvcav2beta1.ICMSRequest,
			pvc *corev1.PersistentVolumeClaim, job *batchv1.Job, _ *nvcastorage.PersistedModelCacheStorageSelection) {
			assert.NoError(t, c.retryFailedModelCacheCleanup(ctx, req, pvc, job))
		}},
		{"shared setup", func(c K8sComputeBackend, ctx context.Context, req *nvcav2beta1.ICMSRequest,
			pvc *corev1.PersistentVolumeClaim, job *batchv1.Job, sel *nvcastorage.PersistedModelCacheStorageSelection) {
			c.setupRWXReadOnlyModelCachingForRequest(ctx, req, pvc, job, sel)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1
			sel := rwxSelection(t)
			req := rwxRequest(t, sel)
			c, k8s := rwxTestBackend(req, boundSharedClaim(false), failed)

			c.bk8s.modelCacheMtx.Lock()
			done := make(chan struct{})
			go func() {
				defer close(done)
				tc.run(c, ctx, req, pvc, job, sel)
			}()
			select {
			case <-done:
				c.bk8s.modelCacheMtx.Unlock()
				t.Fatal("ran while another cache setup held the lock")
			case <-time.After(200 * time.Millisecond):
			}
			_, err := k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.NoError(t, err, "nothing is deleted while the lock is held")
			c.bk8s.modelCacheMtx.Unlock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("did not run after the lock was released")
			}
		})
	}
}

// Setup refuses a claim that is not the shared claim it recorded, and keeps it. The cleanup retry
// on a later pass must keep it too, together with the writer Job that is not provably this
// request's, because the failed state alone does not say the claim is ours to delete.
func TestCleanupRetryLeavesClaimsThatAreNotTheRecordedOnes(t *testing.T) {
	otherClass := "some-other-class"
	cases := []struct {
		name   string
		shared bool
		claim  func() *corev1.PersistentVolumeClaim
		extra  func() *corev1.PersistentVolumeClaim // a read-only claim already published for the handle
	}{
		{name: "shared selection, claim on another StorageClass", shared: true, claim: func() *corev1.PersistentVolumeClaim {
			c := boundSharedClaim(false)
			c.Spec.StorageClassName = &otherClass
			return c
		}},
		{name: "shared selection, claim is not ReadWriteMany", shared: true, claim: func() *corev1.PersistentVolumeClaim {
			c := boundSharedClaim(false)
			c.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			return c
		}},
		{name: "legacy, the writer claim is a shared claim", claim: func() *corev1.PersistentVolumeClaim {
			return boundSharedClaim(false)
		}},
		{name: "legacy, a read-only claim is already published", claim: func() *corev1.PersistentVolumeClaim {
			c, _ := rwxArtifacts()
			c.Status.Phase = corev1.ClaimBound
			return c
		}, extra: func() *corev1.PersistentVolumeClaim {
			c, _ := rwxArtifacts()
			c.Name = "ro-pvc-" + rwxTestHandle
			c.Status.Phase = corev1.ClaimBound
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1
			var sel *nvcastorage.PersistedModelCacheStorageSelection
			if tc.shared {
				sel = rwxSelection(t)
			}
			req := rwxRequest(t, sel)
			claim := tc.claim()
			objs := []runtime.Object{claim, failed}
			if tc.extra != nil {
				objs = append(objs, tc.extra())
			}
			c, k8s := rwxTestBackend(req, objs...)
			mf := func(obj client.Object) { obj.SetNamespace(rwxTestNamespace) }

			if tc.shared {
				// Pass 1: setup refuses the claim, records the fallback and leaves everything alone.
				_, _, err := c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, mf)
				require.NoError(t, err)
			} else {
				req.Annotations = map[string]string{types.ModelCachingFailedAnnotationKey: "true"}
			}
			stored, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			if tc.shared {
				require.Equal(t, "true", stored.Annotations[types.ModelCachingFailedAnnotationKey])
			} else {
				stored = req
			}

			// A later pass retries cleanup. It must not delete what setup refused to share.
			_, _, err = c.setupContainerModelCaching(ctx, stored.DeepCopy(), pvc, job, mf)
			require.NoError(t, err)
			_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, claim.Name, metav1.GetOptions{})
			assert.NoError(t, err, "the claim is kept")
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.NoError(t, err, "the writer Job is kept")
			if tc.extra != nil {
				_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, "ro-pvc-"+rwxTestHandle, metav1.GetOptions{})
				assert.NoError(t, err, "the published read-only claim is kept")
			}
		})
	}
}

// The failed writer Job is the retry marker, so it is deleted last. If removing a claim fails, the Job
// stays and a later pass finishes the cleanup. Deleting the Job first would leave nothing to retry from.
func TestPartialCleanupKeepsFailedWriterUntilClaimsAreGone(t *testing.T) {
	const witness = "claim-uid"
	cases := []struct {
		name   string
		shared bool
	}{
		{name: "legacy"},
		{name: "shared", shared: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1
			var sel *nvcastorage.PersistedModelCacheStorageSelection
			claim := pvc.DeepCopy()
			claim.Status.Phase = corev1.ClaimBound
			if tc.shared {
				sel = rwxSelection(t)
				claim = boundSharedClaim(false)
				failed.Spec.Template.Annotations = map[string]string{nvcastorage.ModelCacheWriterPVCUIDAnnotationKey: witness}
			}
			req := rwxRequest(t, sel)
			if req.Annotations == nil {
				req.Annotations = map[string]string{}
			}
			req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
			c, k8s := rwxTestBackend(req, claim, failed)
			mf := func(client.Object) {}

			claimDeleteFails := true
			k8s.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
				if claimDeleteFails {
					return true, nil, k8serrors.NewServiceUnavailable("injected claim delete failure")
				}
				return false, nil, nil
			})

			_, _, err := c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, mf)
			require.NoError(t, err)
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.NoError(t, err, "the failed writer stays as the retry marker while a claim could not be removed")

			claimDeleteFails = false
			_, _, err = c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, mf)
			require.NoError(t, err)
			_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, claim.Name, metav1.GetOptions{})
			assert.True(t, k8serrors.IsNotFound(err), "the retry removes the claim")
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.True(t, k8serrors.IsNotFound(err), "and then the writer Job")
		})
	}
}

// If the claim is already gone and only the failed Job is left, the Job is removed when it carries the
// shared writer's claim UID annotation, which only shared-flow writers have, and left alone otherwise.
func TestFailedSharedWriterWithoutClaimIsRemovedOnlyWhenItIsASharedWriter(t *testing.T) {
	for _, shared := range []bool{true, false} {
		name := "job without the shared writer annotation is kept"
		if shared {
			name = "job with the shared writer annotation is removed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1
			if shared {
				failed.Spec.Template.Annotations = map[string]string{nvcastorage.ModelCacheWriterPVCUIDAnnotationKey: "claim-uid"}
			}
			req := rwxRequest(t, rwxSelection(t))
			req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
			c, k8s := rwxTestBackend(req, failed) // no claim

			_, _, err := c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, func(client.Object) {})
			require.NoError(t, err)
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.Equal(t, shared, k8serrors.IsNotFound(err), "writer Job removed")
		})
	}
}

// The original cleanup also ran when the read-only claim stayed unbound. If that cleanup failed, the
// writer Job may already be gone, so the retry must not depend on a failed Job. A read-only claim that
// is bound, or still within the bind grace period, is not ours to remove.
func TestCleanupRetryHandlesReadOnlyClaimThatFailedToBind(t *testing.T) {
	const roName = "ro-pvc-" + rwxTestHandle
	roClaim := func(phase corev1.PersistentVolumeClaimPhase, age time.Duration) *corev1.PersistentVolumeClaim {
		c, _ := rwxArtifacts()
		c.Name = roName
		c.Status.Phase = phase
		c.CreationTimestamp = metav1.NewTime(time.Now().Add(-age))
		return c
	}
	rebindAttempted := func(c *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
		c.Annotations = map[string]string{types.NVCARebindAttemptedAnnotationKey: "true"}
		return c
	}
	cases := []struct {
		name       string
		ro         *corev1.PersistentVolumeClaim
		rebind     bool // the agent has PVC rebind enabled
		wantROGone bool
	}{
		{name: "unbound past the grace period", ro: roClaim(corev1.ClaimPending, time.Hour), wantROGone: true},
		{name: "lost, rebind disabled", ro: roClaim(corev1.ClaimLost, time.Minute), wantROGone: true},
		{name: "lost again after a rebind attempt", ro: rebindAttempted(roClaim(corev1.ClaimLost, time.Minute)), rebind: true, wantROGone: true},
		{name: "lost, rebind enabled and not yet attempted", ro: roClaim(corev1.ClaimLost, time.Minute), rebind: true},
		{name: "unbound within the grace period", ro: roClaim(corev1.ClaimPending, 0)},
		{name: "bound", ro: roClaim(corev1.ClaimBound, time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			req := rwxRequest(t, nil)
			req.Annotations = map[string]string{types.ModelCachingFailedAnnotationKey: "true"}
			// No writer Job: an earlier cleanup already removed it.
			c, k8s := rwxTestBackend(req, tc.ro)
			c.bk8s.k8sTimeConfig = &k8sutil.TimeConfig{ModelCacheROPVCBindTimeGracePeriod: 10 * time.Minute}
			c.bk8s.pvcRebindEnabled = tc.rebind

			podMF, _, err := c.setupContainerModelCaching(ctx, req, pvc, job, func(client.Object) {})
			require.NoError(t, err)
			require.NotNil(t, podMF)
			_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, roName, metav1.GetOptions{})
			assert.Equal(t, tc.wantROGone, k8serrors.IsNotFound(err), "read-only claim removed")
		})
	}
}

// A pass can start from the informer copy of the request before that copy shows the fallback annotation.
// The writer Job was already removed by cleanup, so without the agent remembering its own fallback that
// stale pass would start a new writer.
func TestStaleRequestCopyKeepsFallback(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "legacy"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1
			var sel *nvcastorage.PersistedModelCacheStorageSelection
			claim := pvc.DeepCopy()
			claim.Status.Phase = corev1.ClaimBound
			if shared {
				sel = rwxSelection(t)
				claim = boundSharedClaim(false)
			}
			req := rwxRequest(t, sel)
			c, k8s := rwxTestBackend(req, claim, failed)
			mf := func(obj client.Object) { obj.SetNamespace(rwxTestNamespace) }

			// Pass 1 records the fallback and removes the failed writer. It also annotates its own copy.
			local := req.DeepCopy()
			_, _, err := c.setupContainerModelCaching(ctx, local, pvc, job, mf)
			require.NoError(t, err)
			require.Equal(t, "true", local.Annotations[types.ModelCachingFailedAnnotationKey])
			// A function creates one worker per pod artifact in the same pass, so setup runs again on
			// that locally annotated copy before any later pass.
			_, _, err = c.setupContainerModelCaching(ctx, local, pvc, job, mf)
			require.NoError(t, err)
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.True(t, k8serrors.IsNotFound(err))

			// Pass 2 starts from a copy that predates the annotation.
			stale := req.DeepCopy()
			require.Empty(t, stale.Annotations[types.ModelCachingFailedAnnotationKey])
			podMF, _, err := c.setupContainerModelCaching(ctx, stale, pvc, job, mf)
			require.NoError(t, err)
			require.NotNil(t, podMF)
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.True(t, k8serrors.IsNotFound(err), "a stale copy must not start another writer")

			// A pass on a copy with the annotation does not end the record: a pass that started from an
			// older copy can still run after it.
			stored, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			_, _, err = c.setupContainerModelCaching(ctx, stored, pvc, job, mf)
			require.NoError(t, err)
			_, _, err = c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, mf)
			require.NoError(t, err)
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.True(t, k8serrors.IsNotFound(err), "a stale copy after an annotated one must not start another writer")
			_, remembered := c.bk8s.modelCachingFallbacks.Load(modelCachingFallbackKey(req))
			assert.True(t, remembered, "the in-memory fallback lasts until the request is deleted")
		})
	}
}

// Cleanup errors keep the underlying API error, so callers can tell, for example, an unavailable API
// server from a missing object.
func TestCleanupModelCachingResourcesWrapsErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(rwxTestContext(t))
	t.Cleanup(cancel)
	pvc, job := rwxArtifacts()
	claim := pvc.DeepCopy()
	claim.Status.Phase = corev1.ClaimBound
	c, k8s := rwxTestBackend(nil, claim, job.DeepCopy())
	k8s.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServiceUnavailable("injected claim delete failure")
	})

	err := c.CleanupModelCachingResources(ctx, pvc, job.Name)
	require.Error(t, err)
	assert.True(t, k8serrors.IsServiceUnavailable(err), "the API error is wrapped, got %v", err)
}

// The claim can still name a volume that is already gone, for example one removed by its Delete reclaim
// policy after an earlier partial cleanup. That is nothing left to update, so cleanup must go on and
// remove the claims and then the failed writer, instead of stopping there on every pass.
func TestCleanupContinuesWhenTheVolumeIsAlreadyGone(t *testing.T) {
	ctx, cancel := context.WithCancel(rwxTestContext(t))
	t.Cleanup(cancel)
	pvc, job := rwxArtifacts()
	claim := pvc.DeepCopy()
	claim.Status.Phase = corev1.ClaimBound
	claim.Spec.VolumeName = "pv-already-deleted" // no such PersistentVolume
	c, k8s := rwxTestBackend(nil, claim, job.DeepCopy())

	require.NoError(t, c.CleanupModelCachingResources(ctx, pvc, job.Name))
	_, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, claim.Name, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err), "the claim is removed")
	_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err), "and then the writer Job")
}

// The cleanup retry treats the writer as failed in every case CheckInitCacheJobState does, not only after
// the backoff limit: a writer active past InitCacheJobFailureThreshold, and one whose write claim failed to
// bind. Each case starts with a claim delete that fails, so only the retry on the next pass can finish the
// cleanup. A writer that is still within the threshold, or that completed, is left alone.
func TestCleanupRetryCoversEveryWriterFailure(t *testing.T) {
	running := func(age time.Duration) func(*batchv1.Job) {
		return func(j *batchv1.Job) {
			j.Status.Active = 1
			j.CreationTimestamp = metav1.NewTime(time.Now().Add(-age))
		}
	}
	cases := []struct {
		name     string
		job      func(*batchv1.Job)
		claim    func(*corev1.PersistentVolumeClaim)
		wantGone bool
	}{
		{name: "writer active past the failure threshold", job: running(2 * time.Hour), wantGone: true},
		{name: "write claim lost with rebind disabled", job: running(time.Minute),
			claim: func(c *corev1.PersistentVolumeClaim) { c.Status.Phase = corev1.ClaimLost }, wantGone: true},
		{name: "write claim unbound past the grace period", job: running(time.Minute),
			claim: func(c *corev1.PersistentVolumeClaim) {
				c.Status.Phase = corev1.ClaimPending
				c.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
			}, wantGone: true},
		{name: "writer within the failure threshold is untouched", job: running(time.Minute)},
		{name: "completed writer is untouched", job: func(j *batchv1.Job) {
			j.Status.Succeeded = 1
			j.Status.CompletionTime = &metav1.Time{Time: time.Now()}
		}, claim: func(c *corev1.PersistentVolumeClaim) { c.Status.Phase = corev1.ClaimLost }},
	}
	for _, shared := range []bool{false, true} {
		for _, tc := range cases {
			name := "legacy, " + tc.name
			if shared {
				name = "shared, " + tc.name
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(rwxTestContext(t))
				t.Cleanup(cancel)
				pvc, job := rwxArtifacts()
				var sel *nvcastorage.PersistedModelCacheStorageSelection
				claim := pvc.DeepCopy()
				claim.Status.Phase = corev1.ClaimBound
				if shared {
					sel = rwxSelection(t)
					claim = boundSharedClaim(false)
				}
				if tc.claim != nil {
					tc.claim(claim)
				}
				writer := job.DeepCopy()
				tc.job(writer)
				req := rwxRequest(t, sel)
				if req.Annotations == nil {
					req.Annotations = map[string]string{}
				}
				req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
				c, k8s := rwxTestBackend(req, claim, writer)
				c.bk8s.k8sTimeConfig = &k8sutil.TimeConfig{
					InitCacheJobFailureThreshold:       time.Hour,
					ModelCacheROPVCBindTimeGracePeriod: 10 * time.Minute,
				}
				claimDeleteFails := true
				k8s.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
					if claimDeleteFails {
						return true, nil, k8serrors.NewServiceUnavailable("injected claim delete failure")
					}
					return false, nil, nil
				})

				_, _, err := c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, func(client.Object) {})
				require.NoError(t, err)
				_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
				require.NoError(t, err, "the writer stays while its claim could not be removed")

				claimDeleteFails = false
				_, _, err = c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, func(client.Object) {})
				require.NoError(t, err)
				_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, claim.Name, metav1.GetOptions{})
				assert.Equal(t, tc.wantGone, k8serrors.IsNotFound(err), "claim removed")
				_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
				assert.Equal(t, tc.wantGone, k8serrors.IsNotFound(err), "writer Job removed")
			})
		}
	}
}

// Once every non-cached worker exists, the creation handlers return before cache setup, and once the
// request is in RequestInstancesInProgress it no longer reaches them at all. A cleanup that failed on a
// transient error must still be retried from both places, for both functions and tasks.
func TestCleanupRetryRunsAfterAllWorkersExist(t *testing.T) {
	encode := func(t *testing.T, obj runtime.Object) string {
		t.Helper()
		raw, err := yaml.Marshal(obj)
		require.NoError(t, err)
		return base64.StdEncoding.EncodeToString(raw)
	}
	type entry struct{ kind, via string }
	var entries []entry
	for _, kind := range []string{"function", "task"} {
		for _, via := range []string{"creation handler", "status sync"} {
			entries = append(entries, entry{kind, via})
		}
	}
	for _, e := range entries {
		kind := e.kind
		t.Run(kind+" via "+e.via, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			claim := pvc.DeepCopy()
			claim.Status.Phase = corev1.ClaimBound
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1

			req := rwxRequest(t, nil)
			req.Annotations = map[string]string{types.ModelCachingFailedAnnotationKey: "true"}
			req.Spec.CreationMsgInfo.InstanceCount = 1
			req.Status.Instances = map[string]nvcav2beta1.InstanceStatus{"instance-1": {}}
			req.Spec.Action = common.FunctionCreationAction
			if kind == "task" {
				req.Spec.Action = common.TaskCreationAction
				req.Spec.TaskDetails = task.Details{TaskID: "test-task"}
				req.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
					CacheLaunchSpecification: &common.CacheLaunchSpecification{CacheArtifacts: true, CacheHandle: rwxTestHandle},
				}
			} else {
				pvcArt := pvc.DeepCopy()
				pvcArt.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"}
				jobArt := job.DeepCopy()
				jobArt.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
				req.Spec.CreationMsgInfo.LaunchArtifacts = function.LaunchArtifacts{
					{Type: function.LaunchArtifactTypeBlockDevice, Specification: encode(t, pvcArt)},
					{Type: function.LaunchArtifactTypeInitCacheJob, Specification: encode(t, jobArt)},
				}
			}

			c, k8s := rwxTestBackend(req, claim, failed)
			c.bk8s.cachingSupportEnabled = true
			helper := &MockICMSRequestHelper{}
			helper.On("AggregateInstanceStatuses", mock.Anything, mock.Anything).
				Return(AggregatedInstanceStatusModelCachingInProgress)
			c.bk8s.icmsRequestHelper = helper
			apply := c.applyFunctionCreationMessage
			if kind == "task" {
				apply = c.applyContainerTaskCreationMessage
			}
			if e.via == "status sync" {
				apply = c.RetryFailedModelCacheCleanup
			}
			claimDeleteFails := true
			k8s.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
				if claimDeleteFails {
					return true, nil, k8serrors.NewServiceUnavailable("injected claim delete failure")
				}
				return false, nil, nil
			})

			firstErr := apply(ctx, req.DeepCopy())
			if e.via == "status sync" {
				require.True(t, k8serrors.IsServiceUnavailable(firstErr), "status sync must requeue on cleanup failure")
			} else {
				require.NoError(t, firstErr, "creation continues with non-cached workers")
			}
			_, err := k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.NoError(t, err, "the writer stays while its claim could not be removed")

			claimDeleteFails = false
			require.NoError(t, apply(ctx, req.DeepCopy()))
			_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, claim.Name, metav1.GetOptions{})
			assert.True(t, k8serrors.IsNotFound(err), "the claim is removed after the workers exist")
			_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
			assert.True(t, k8serrors.IsNotFound(err), "and then the writer Job")
		})
	}
}

// Cleanup remains reachable while workers run and after completion is acknowledged.
func TestSyncRetriesModelCacheCleanupWhileInstancesInProgress(t *testing.T) {
	cases := []struct {
		name   string
		action common.MessageAction
		status nvcav2beta1.RequestStatus
		want   bool
	}{
		{name: "task creation, instances in progress", action: common.TaskCreationAction,
			status: nvcav2beta1.ICMSRequestStatusInstancesInProgress, want: true},
		{name: "function creation, instances in progress", action: common.FunctionCreationAction,
			status: nvcav2beta1.ICMSRequestStatusInstancesInProgress, want: true},
		{name: "termination, instances in progress", action: common.TerminationAction,
			status: nvcav2beta1.ICMSRequestStatusInstancesInProgress},
		{name: "task creation, completion acknowledged", action: common.TaskCreationAction,
			status: nvcav2beta1.ICMSRequestStatusCompletionAcknowledged, want: true},
		{name: "function creation, completion acknowledged", action: common.FunctionCreationAction,
			status: nvcav2beta1.ICMSRequestStatusCompletionAcknowledged, want: true},
		{name: "termination, completion acknowledged", action: common.TerminationAction,
			status: nvcav2beta1.ICMSRequestStatusCompletionAcknowledged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			req := rwxRequest(t, nil)
			req.Finalizers = []string{NVCAFinalizer}
			req.Spec.Action = tc.action
			req.Status.RequestStatus = tc.status
			c, _ := rwxTestBackend(req)
			helper := &MockICMSRequestHelper{}
			helper.On("AggregateInstanceStatuses", mock.Anything, mock.Anything).
				Return(AggregatedInstanceStatusModelCachingInProgress)
			helper.On("AllInstancesTerminatedAndReported", mock.Anything, mock.Anything).Return(false)
			helper.On("RetryFailedModelCacheCleanup", mock.Anything, mock.Anything).Return(nil)
			c.bk8s.icmsRequestHelper = helper

			require.NoError(t, c.bk8s.syncICMSRequest(ctx, req))
			if tc.want {
				helper.AssertCalled(t, "RetryFailedModelCacheCleanup", mock.Anything, mock.Anything)
			} else {
				helper.AssertNotCalled(t, "RetryFailedModelCacheCleanup", mock.Anything, mock.Anything)
			}
		})
	}
}

// A request that falls back uses no cache, so the cache reference recorded while the writer ran is
// cleared, both on the pass that records the fallback and on a later pass.
func TestFallbackClearsCacheReference(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		for _, shared := range []bool{false, true} {
			name := "legacy"
			if shared {
				name = "shared"
			}
			if recorded {
				name += ", fallback already recorded"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(rwxTestContext(t))
				t.Cleanup(cancel)
				pvc, job := rwxArtifacts()
				failed := job.DeepCopy()
				failed.Spec.BackoffLimit = ptr.To(int32(0))
				failed.Status.Failed = 1
				var sel *nvcastorage.PersistedModelCacheStorageSelection
				claim := pvc.DeepCopy()
				claim.Status.Phase = corev1.ClaimBound
				if shared {
					sel = rwxSelection(t)
					claim = boundSharedClaim(false)
				}
				req := rwxRequest(t, sel)
				req.Status.CacheReferenceName = claim.Name
				if recorded {
					if req.Annotations == nil {
						req.Annotations = map[string]string{}
					}
					req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
				}
				c, _ := rwxTestBackend(req, claim, failed)

				_, _, err := c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, func(client.Object) {})
				require.NoError(t, err)
				stored, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
				require.NoError(t, err)
				assert.Empty(t, stored.Status.CacheReferenceName)
			})
		}
	}
}

// The unpopulated shared claim is deleted with a UID precondition, so a claim recreated after the
// check is not removed in its place.
func TestFailedSharedWriterClaimDeleteIsBoundToTheCheckedClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(rwxTestContext(t))
	t.Cleanup(cancel)
	_, job := rwxArtifacts()
	claim := boundSharedClaim(false)
	c, k8s := rwxTestBackend(nil, claim, job.DeepCopy())
	var uid string
	k8s.PrependReactor("delete", "persistentvolumeclaims", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if p := a.(k8stesting.DeleteActionImpl).DeleteOptions.Preconditions; p != nil && p.UID != nil {
			uid = string(*p.UID)
		}
		return false, nil, nil
	})

	require.NoError(t, c.removeFailedSharedWriter(ctx, job.Name, "", claim.Name))
	assert.Equal(t, rwxClaimUID, uid)
}

// A request deleted before the agent saw its annotated copy drops the in-memory fallback record,
// whether the informer hands over the object or a tombstone.
func TestDeletedRequestForgetsFallback(t *testing.T) {
	for _, tombstone := range []bool{false, true} {
		c, _ := rwxTestBackend(nil)
		req := rwxRequest(t, nil)
		req.UID = "request-uid"
		key := modelCachingFallbackKey(req)
		c.bk8s.modelCachingFallbacks.Store(key, struct{}{})
		var obj any = req
		if tombstone {
			obj = cache.DeletedFinalStateUnknown{Key: req.Namespace + "/" + req.Name, Obj: req}
		}
		c.bk8s.forgetModelCachingFallback(obj)
		_, remembered := c.bk8s.modelCachingFallbacks.Load(key)
		assert.False(t, remembered, "tombstone=%v", tombstone)
	}
}

// If the status write that clears the cache reference fails on the fallback pass, the workers are still
// created, so the clearing is retried from the after-workers-exist paths, for functions and tasks.
func TestCacheReferenceClearIsRetriedAfterWorkersExist(t *testing.T) {
	for _, kind := range []string{"function", "task"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			req := rwxRequest(t, nil)
			req.Annotations = map[string]string{types.ModelCachingFailedAnnotationKey: "true"}
			req.Spec.CreationMsgInfo.InstanceCount = 1
			req.Status.Instances = map[string]nvcav2beta1.InstanceStatus{"instance-1": {}}
			req.Status.CacheReferenceName = pvc.Name
			if kind == "task" {
				req.Spec.Action = common.TaskCreationAction
				req.Spec.TaskDetails = task.Details{TaskID: "test-task"}
				req.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
					CacheLaunchSpecification: &common.CacheLaunchSpecification{CacheArtifacts: true, CacheHandle: rwxTestHandle},
				}
			} else {
				req.Spec.Action = common.FunctionCreationAction
				encode := func(obj runtime.Object) string {
					raw, err := yaml.Marshal(obj)
					require.NoError(t, err)
					return base64.StdEncoding.EncodeToString(raw)
				}
				pvcArt := pvc.DeepCopy()
				pvcArt.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"}
				jobArt := job.DeepCopy()
				jobArt.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
				req.Spec.CreationMsgInfo.LaunchArtifacts = function.LaunchArtifacts{
					{Type: function.LaunchArtifactTypeBlockDevice, Specification: encode(pvcArt)},
					{Type: function.LaunchArtifactTypeInitCacheJob, Specification: encode(jobArt)},
				}
			}
			c, _ := rwxTestBackend(req)
			c.bk8s.cachingSupportEnabled = true
			statusWriteFails := true
			c.clients.BART.(*fakebartclient.Clientset).PrependReactor("update", "icmsrequests",
				func(a k8stesting.Action) (bool, runtime.Object, error) {
					if a.GetSubresource() == "status" && statusWriteFails {
						return true, nil, k8serrors.NewBadRequest("injected status write failure")
					}
					return false, nil, nil
				})

			require.NoError(t, c.RetryFailedModelCacheCleanup(ctx, req.DeepCopy()))
			stored, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, pvc.Name, stored.Status.CacheReferenceName, "the failed write leaves the reference")

			statusWriteFails = false
			require.NoError(t, c.RetryFailedModelCacheCleanup(ctx, stored.DeepCopy()))
			stored, err = c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Empty(t, stored.Status.CacheReferenceName, "a later pass after the workers exist clears it")
		})
	}
}

// The cleanup retry deletes the failed shared writer with the UID of the Job it inspected, so a writer
// recreated under the same name since is not removed in its place.
func TestFailedSharedWriterJobDeleteIsBoundToTheInspectedJob(t *testing.T) {
	ctx, cancel := context.WithCancel(rwxTestContext(t))
	t.Cleanup(cancel)
	pvc, job := rwxArtifacts()
	failed := job.DeepCopy()
	failed.UID = "writer-uid-1"
	failed.Spec.BackoffLimit = ptr.To(int32(0))
	failed.Status.Failed = 1
	req := rwxRequest(t, rwxSelection(t))
	req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
	c, k8s := rwxTestBackend(req, boundSharedClaim(false), failed)
	var uid string
	k8s.PrependReactor("delete", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if p := a.(k8stesting.DeleteActionImpl).DeleteOptions.Preconditions; p != nil && p.UID != nil {
			uid = string(*p.UID)
		}
		return false, nil, nil
	})

	_, _, err := c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, func(client.Object) {})
	require.NoError(t, err)
	assert.Equal(t, "writer-uid-1", uid)
}

// The pass that records the fallback can start from an informer copy older than the cache reference
// written while the writer ran. It must clear the reference on the live request anyway.
func TestFallbackClearsLiveCacheReferenceFromStaleCopy(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "legacy"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(rwxTestContext(t))
			t.Cleanup(cancel)
			pvc, job := rwxArtifacts()
			failed := job.DeepCopy()
			failed.Spec.BackoffLimit = ptr.To(int32(0))
			failed.Status.Failed = 1
			var sel *nvcastorage.PersistedModelCacheStorageSelection
			claim := pvc.DeepCopy()
			claim.Status.Phase = corev1.ClaimBound
			if shared {
				sel = rwxSelection(t)
				claim = boundSharedClaim(false)
			}
			live := rwxRequest(t, sel)
			live.Status.CacheReferenceName = claim.Name
			stale := live.DeepCopy()
			stale.Status.CacheReferenceName = ""
			c, _ := rwxTestBackend(live, claim, failed)

			_, _, err := c.setupContainerModelCaching(ctx, stale, pvc, job, func(client.Object) {})
			require.NoError(t, err)
			stored, err := c.clients.BART.NvcaV2beta1().ICMSRequests(live.Namespace).Get(ctx, live.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Empty(t, stored.Status.CacheReferenceName)
		})
	}
}

func TestCompletionAcknowledgedRetriesCleanupBeforeRemovingRequest(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	failed := job.DeepCopy()
	failed.Spec.BackoffLimit = ptr.To(int32(0))
	failed.Status.Failed = 1
	req := rwxRequest(t, rwxSelection(t))
	req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
	req.Spec.Action = common.TaskCreationAction
	req.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
		CacheLaunchSpecification: &common.CacheLaunchSpecification{CacheArtifacts: true, CacheHandle: rwxTestHandle},
	}
	req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusCompletionAcknowledged
	c, k8s := rwxTestBackend(req, boundSharedClaim(false), failed)
	c.bk8s.cachingSupportEnabled = true
	c.bk8s.requestsNamespace = req.Namespace
	helper := &MockICMSRequestHelper{}
	helper.On("RetryFailedModelCacheCleanup", mock.Anything, mock.Anything).Return(func(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
		_, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
		require.NoError(t, err, "cleanup runs before request deletion")
		return c.RetryFailedModelCacheCleanup(ctx, req)
	}).Once()
	helper.On("AllInstancesTerminatedAndReported", mock.Anything, mock.Anything).Return(true)
	c.bk8s.icmsRequestHelper = helper

	require.NoError(t, c.bk8s.syncICMSRequest(ctx, req.DeepCopy()))
	helper.AssertExpectations(t)
	_, err := k8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err), "failed writer is removed")
	_, err = k8s.CoreV1().PersistentVolumeClaims(req.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err), "unpopulated claim is removed")
	_, err = c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err), "request is then removed")
}

func TestCompletedRequestSurvivesCacheCleanupFailure(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, operation := range []string{"get claim", "get job", "delete claim", "delete job", "update volume"} {
			if shared && operation == "update volume" {
				continue
			}
			name := "function legacy, " + operation
			if shared {
				name = "task shared, " + operation
			}
			t.Run(name, func(t *testing.T) {
				ctx := rwxTestContext(t)
				pvc, job := rwxArtifacts()
				claim := pvc.DeepCopy()
				claim.Status.Phase = corev1.ClaimBound
				req := rwxRequest(t, nil)
				if shared {
					req = rwxRequest(t, rwxSelection(t))
					claim = boundSharedClaim(false)
					req.Spec.Action = common.TaskCreationAction
					req.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
						CacheLaunchSpecification: &common.CacheLaunchSpecification{CacheArtifacts: true, CacheHandle: rwxTestHandle},
					}
				} else {
					req.Spec.Action = common.FunctionCreationAction
					pvc.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"}
					job.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
					for _, artifact := range []struct {
						kind function.LaunchArtifactType
						obj  runtime.Object
					}{{function.LaunchArtifactTypeBlockDevice, pvc}, {function.LaunchArtifactTypeInitCacheJob, job}} {
						raw, err := yaml.Marshal(artifact.obj)
						require.NoError(t, err)
						req.Spec.CreationMsgInfo.LaunchArtifacts = append(req.Spec.CreationMsgInfo.LaunchArtifacts,
							function.LaunchArtifact{Type: artifact.kind, Specification: base64.StdEncoding.EncodeToString(raw)})
					}
				}
				if req.Annotations == nil {
					req.Annotations = map[string]string{}
				}
				req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
				req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusCompletionAcknowledged
				failed := job.DeepCopy()
				failed.Spec.BackoffLimit = ptr.To(int32(0))
				failed.Status.Failed = 1
				failed.Spec.Template.Annotations = map[string]string{nvcastorage.ModelCacheWriterPVCUIDAnnotationKey: "cache-claim"}
				objs := []runtime.Object{claim, failed}
				if operation == "update volume" {
					claim.Spec.VolumeName = "cache-volume"
					objs = append(objs, &corev1.PersistentVolume{
						ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName},
						Spec:       corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain},
					})
				}
				c, k8s := rwxTestBackend(req, objs...)
				c.bk8s.cachingSupportEnabled = true
				c.bk8s.requestsNamespace = req.Namespace
				helper := &MockICMSRequestHelper{}
				helper.On("RetryFailedModelCacheCleanup", mock.Anything, mock.Anything).
					Return(func(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
						return c.RetryFailedModelCacheCleanup(ctx, req)
					}).Twice()
				helper.On("AllInstancesTerminatedAndReported", mock.Anything, mock.Anything).Return(true).Once()
				c.bk8s.icmsRequestHelper = helper
				failing := true
				k8s.PrependReactor("*", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
					operationForResource := map[string]string{
						"get/persistentvolumeclaims": "get claim", "get/jobs": "get job",
						"delete/persistentvolumeclaims": "delete claim", "delete/jobs": "delete job",
						"update/persistentvolumes": "update volume",
					}
					if failing && operationForResource[a.GetVerb()+"/"+a.GetResource().Resource] == operation {
						return true, nil, k8serrors.NewServiceUnavailable("injected cleanup failure")
					}
					return false, nil, nil
				})

				cleanupErr := c.bk8s.syncICMSRequest(ctx, req.DeepCopy())
				stored, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
				require.NoError(t, err, "completed request is kept for another cleanup attempt")
				require.True(t, k8serrors.IsServiceUnavailable(cleanupErr), "cleanup error requeues the request: %v", cleanupErr)
				helper.AssertNotCalled(t, "AllInstancesTerminatedAndReported", mock.Anything, mock.Anything)

				failing = false
				require.NoError(t, c.bk8s.syncICMSRequest(ctx, stored.DeepCopy()))
				helper.AssertExpectations(t)
				_, err = k8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
				assert.True(t, k8serrors.IsNotFound(err))
				_, err = k8s.CoreV1().PersistentVolumeClaims(req.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
				assert.True(t, k8serrors.IsNotFound(err))
				_, err = c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
				assert.True(t, k8serrors.IsNotFound(err), "request is removed only after cleanup succeeds")
			})
		}
	}
}

func TestLegacyCleanupRetryPreservesReplacementWriter(t *testing.T) {
	for _, writerPresent := range []bool{true, false} {
		name := "failed writer replaced during cleanup"
		if !writerPresent {
			name = "writer created during reader cleanup"
		}
		t.Run(name, func(t *testing.T) {
			ctx := rwxTestContext(t)
			pvc, job := rwxArtifacts()
			claim := pvc.DeepCopy()
			claim.Status.Phase = corev1.ClaimBound
			req := rwxRequest(t, nil)
			req.Annotations = map[string]string{types.ModelCachingFailedAnnotationKey: "true"}
			objs := []runtime.Object{claim}
			if writerPresent {
				failed := job.DeepCopy()
				failed.UID = "old-writer"
				failed.Spec.BackoffLimit = ptr.To(int32(0))
				failed.Status.Failed = 1
				objs = append(objs, failed)
			} else {
				reader := pvc.DeepCopy()
				reader.Name = "ro-pvc-" + rwxTestHandle
				reader.Status.Phase = corev1.ClaimLost
				objs = append(objs, reader)
			}
			c, k8s := rwxTestBackend(req, objs...)
			jobs := batchv1.SchemeGroupVersion.WithResource("jobs")
			replaced := false
			k8s.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
				if !replaced {
					if writerPresent {
						require.NoError(t, k8s.Tracker().Delete(jobs, req.Namespace, job.Name))
					}
					replacement := job.DeepCopy()
					replacement.UID = "new-writer"
					replacement.Status.Active = 1
					require.NoError(t, k8s.Tracker().Create(jobs, replacement, req.Namespace))
					replaced = true
				}
				return false, nil, nil
			})
			deleteAttempted := false
			k8s.PrependReactor("delete", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
				deleteAttempted = true
				opts := a.(k8stesting.DeleteAction).GetDeleteOptions()
				if opts.Preconditions != nil && opts.Preconditions.UID != nil {
					assert.Equal(t, "old-writer", string(*opts.Preconditions.UID))
					// Fake clientsets do not enforce UID preconditions; emulate the API rejection.
					return true, nil, k8serrors.NewConflict(batchv1.Resource("jobs"), job.Name, assert.AnError)
				}
				return false, nil, nil
			})

			err := c.retryFailedModelCacheCleanup(ctx, req, pvc, job)
			if writerPresent {
				require.True(t, k8serrors.IsConflict(err), "replacement rejects the old UID")
			} else {
				require.NoError(t, err)
			}
			require.True(t, replaced)
			assert.Equal(t, writerPresent, deleteAttempted)
			remaining, err := k8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.NoError(t, err, "replacement writer survives")
			assert.Equal(t, "new-writer", string(remaining.UID))
		})
	}
}

func TestSharedClaimTransientErrorsDoNotPersistFallback(t *testing.T) {
	for _, operation := range []string{"get", "create", "get after already exists"} {
		t.Run(operation, func(t *testing.T) {
			ctx := rwxTestContext(t)
			pvc, job := rwxArtifacts()
			req := rwxRequest(t, rwxSelection(t))
			req.Status.CacheReferenceName = pvc.Name
			var objs []runtime.Object
			if operation != "create" {
				objs = append(objs, boundSharedClaim(false))
			}
			c, k8s := rwxTestBackend(req, objs...)
			failing := true
			gets := 0
			k8s.PrependReactor("get", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
				gets++
				if failing {
					if operation == "get after already exists" && gets == 1 {
						return true, nil, k8serrors.NewNotFound(corev1.Resource("persistentvolumeclaims"), pvc.Name)
					}
					if operation != "create" {
						return true, nil, k8serrors.NewServiceUnavailable("injected get failure")
					}
				}
				return false, nil, nil
			})
			k8s.PrependReactor("create", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
				if failing && operation == "create" {
					return true, nil, k8serrors.NewServiceUnavailable("injected create failure")
				}
				return false, nil, nil
			})

			podMF, _, err := c.setupContainerModelCaching(ctx, req.DeepCopy(), pvc, job, func(client.Object) {})
			require.Error(t, err, "transient error requeues cache setup")
			require.Nil(t, podMF)
			stored, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Empty(t, stored.Annotations[types.ModelCachingFailedAnnotationKey])
			assert.False(t, c.bk8s.modelCachingFailed(stored))
			assert.Equal(t, pvc.Name, stored.Status.CacheReferenceName)

			failing = false
			_, _, err = c.setupContainerModelCaching(ctx, stored.DeepCopy(), pvc, job, func(client.Object) {})
			require.Error(t, err, "recovered API allows cache population to start")
			_, err = k8s.BatchV1().Jobs(req.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
			require.NoError(t, err, "writer starts after recovery")
			stored, err = c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Empty(t, stored.Annotations[types.ModelCachingFailedAnnotationKey])
		})
	}
}

// A permanent cleanup error, such as Forbidden, does not keep a completed request forever: it is logged
// and the request is removed as usual. Only transient errors keep the request for another attempt.
func TestCompletedRequestIsRemovedAfterPermanentCleanupError(t *testing.T) {
	ctx := rwxTestContext(t)
	_, job := rwxArtifacts()
	failed := job.DeepCopy()
	failed.Spec.BackoffLimit = ptr.To(int32(0))
	failed.Status.Failed = 1
	req := rwxRequest(t, rwxSelection(t))
	req.Annotations[types.ModelCachingFailedAnnotationKey] = "true"
	req.Spec.Action = common.TaskCreationAction
	req.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
		CacheLaunchSpecification: &common.CacheLaunchSpecification{CacheArtifacts: true, CacheHandle: rwxTestHandle},
	}
	req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusCompletionAcknowledged
	c, k8s := rwxTestBackend(req, boundSharedClaim(false), failed)
	c.bk8s.cachingSupportEnabled = true
	c.bk8s.requestsNamespace = req.Namespace
	helper := &MockICMSRequestHelper{}
	helper.On("RetryFailedModelCacheCleanup", mock.Anything, mock.Anything).
		Return(func(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
			return c.RetryFailedModelCacheCleanup(ctx, req)
		}).Once()
	helper.On("AllInstancesTerminatedAndReported", mock.Anything, mock.Anything).Return(true)
	c.bk8s.icmsRequestHelper = helper
	k8s.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "persistentvolumeclaims"}, "cache", nil)
	})

	require.NoError(t, c.bk8s.syncICMSRequest(ctx, req.DeepCopy()))
	helper.AssertExpectations(t)
	_, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err), "the request is removed despite the permanent cleanup error")
}
