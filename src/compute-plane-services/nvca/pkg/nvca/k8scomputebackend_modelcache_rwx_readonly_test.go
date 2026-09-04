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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	fakebartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

const (
	rwxTestNamespace = "nvcf-backend"
	rwxTestHandle    = "sharedhandle"
	rwxTestClass     = "nvcf-sc"
)

// rwxTestContext carries metrics on a private registry so this file never
// collides with other tests that register collectors in the same process.
func rwxTestContext(t *testing.T) context.Context {
	t.Helper()
	m := nvcametrics.NewDefaultMetrics("cluster", "backend", "backend", "rwx-test",
		nvcametrics.WithRegisterer(prometheus.NewRegistry()))
	return nvcametrics.WithMetrics(newTestContext(), m)
}

// rwxTestBackend builds a compute backend over fake clientsets. req, when
// given, is registered with the NVCA clientset so status updates made while
// caching is in progress have an object to land on.
func rwxTestBackend(req *nvcav2beta1.ICMSRequest, objs ...runtime.Object) (K8sComputeBackend, *fakek8sclient.Clientset) {
	k8sClient := fakek8sclient.NewSimpleClientset(objs...)
	var reqObjs []runtime.Object
	if req != nil {
		reqObjs = append(reqObjs, req)
	}
	clients := &kubeclients.KubeClients{K8s: k8sClient, BART: fakebartclient.NewSimpleClientset(reqObjs...)}
	bk8s := &BackendK8sCache{
		clients:              clients,
		podInstanceNamespace: rwxTestNamespace,
		featureFlagFetcher:   &featureflagmock.Fetcher{},
	}
	return K8sComputeBackend{clients: clients, bk8s: bk8s}, k8sClient
}

// rwxSelection is a durable regular-workflow selection for a ReadWriteMany
// provider, shaped the way the agent persists it at request creation.
func rwxSelection(t *testing.T) *nvcastorage.PersistedModelCacheStorageSelection {
	t.Helper()
	return &nvcastorage.PersistedModelCacheStorageSelection{
		Version:             nvcastorage.ModelCacheStorageSelectionVersion,
		Workflow:            nvcastorage.ModelCacheWorkflowRegular,
		Mode:                nvcastorage.ModelCacheSelectionDurable,
		StorageClassName:    rwxTestClass,
		StorageClassUID:     "sc-uid",
		StorageClassDigest:  "v1:sha256:" + strings.Repeat("a", 64),
		ProfileDigest:       "sha256:" + strings.Repeat("b", 64),
		Provider:            "weka",
		Provisioner:         "csi.weka.io",
		Transition:          nvcastorage.ModelCacheTransitionRWXReadOnly,
		RequiredAccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
	}
}

func rwxRequest(t *testing.T, selection *nvcastorage.PersistedModelCacheStorageSelection) *nvcav2beta1.ICMSRequest {
	t.Helper()
	req := &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{Name: "request", Namespace: rwxTestNamespace}}
	if selection != nil {
		raw, err := selection.Marshal()
		require.NoError(t, err)
		req.Annotations = map[string]string{nvcastorage.ModelCacheStorageSelectionAnnotationKey: raw}
	}
	return req
}

// rwxArtifacts mirrors what the translator emits: a ReadWriteOnce claim and a
// writer Job mounting it read-write under the model volume name.
func rwxArtifacts() (*corev1.PersistentVolumeClaim, *batchv1.Job) {
	other := "some-other-class"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-pvc-" + rwxTestHandle, Namespace: rwxTestNamespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &other,
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "writer-job-" + rwxTestHandle, Namespace: rwxTestNamespace},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: ModelVolumeName, VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name},
			}}},
			Containers: []corev1.Container{{Name: "writer", VolumeMounts: []corev1.VolumeMount{{Name: ModelVolumeName, MountPath: "/model"}}}},
		}}},
	}
	return pvc, job
}

const rwxClaimUID = "claim-uid-1"

func boundSharedClaim(populated bool) *corev1.PersistentVolumeClaim {
	pvc, _ := rwxArtifacts()
	pvc.UID = rwxClaimUID
	class := rwxTestClass
	pvc.Spec.StorageClassName = &class
	pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	pvc.Status.Phase = corev1.ClaimBound
	if populated {
		pvc.Labels = map[string]string{nvcastorage.ModelCachePopulatedLabelKey: nvcastorage.ModelCachePopulatedLabelValue}
	}
	return pvc
}

func TestRWXReadOnly_FirstRequestCreatesSharedClaimAndWriter(t *testing.T) {
	ctx := rwxTestContext(t)
	c, k8s := rwxTestBackend(nil)
	pvc, job := rwxArtifacts()

	state, claim := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingInProgress, state)
	assert.Equal(t, pvc.Name, claim, "an in-progress result names the claim so the request records its reference")

	created, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, created.Spec.AccessModes,
		"the artifact's ReadWriteOnce claim becomes the shared ReadWriteMany claim")
	require.NotNil(t, created.Spec.StorageClassName)
	assert.Equal(t, rwxTestClass, *created.Spec.StorageClassName, "the claim lands on the class the selection recorded")

	writer, err := k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	require.NoError(t, err)
	_, hasWitness := writer.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey]
	assert.True(t, hasWitness, "the writer records which claim it populates")
}

func TestRWXReadOnly_CompletedWriterMarksClaimPopulated(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	done := job.DeepCopy()
	done.Status.Succeeded = 1
	now := metav1.Now()
	done.Status.CompletionTime = &now
	done.Spec.Template.Annotations = map[string]string{nvcastorage.ModelCacheWriterPVCUIDAnnotationKey: rwxClaimUID}
	c, k8s := rwxTestBackend(nil, boundSharedClaim(false), done)

	state, claim := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingCompleted, state)
	assert.Equal(t, pvc.Name, claim, "readers mount the shared claim itself")

	got, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvcastorage.ModelCachePopulatedLabelValue, got.Labels[nvcastorage.ModelCachePopulatedLabelKey])
}

func TestRWXReadOnly_PopulatedClaimIsReusedWithoutAWriter(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	c, k8s := rwxTestBackend(nil, boundSharedClaim(true))

	state, claim := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingCompleted, state)
	assert.Equal(t, pvc.Name, claim)

	_, err := k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "a populated cache must not start another writer")
}

func TestRWXReadOnly_FailedWriterRemovesUnpopulatedClaim(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	failed := job.DeepCopy()
	var zero int32
	failed.Spec.BackoffLimit = &zero
	failed.Status.Failed = 1
	c, k8s := rwxTestBackend(nil, boundSharedClaim(false), failed)

	state, _ := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingFailed, state)

	_, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "an unpopulated claim whose writer failed is removed so the next request retries")
	_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err))
}

func TestRWXReadOnly_RefusesAClaimThatIsNotShared(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	foreign := pvc.DeepCopy()
	foreign.Status.Phase = corev1.ClaimBound
	c, k8s := rwxTestBackend(nil, foreign)

	state, _ := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingFailed, state)

	still, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	require.NoError(t, err, "a claim that is not ours to share is neither mounted nor deleted")
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, still.Spec.AccessModes)
	_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err))
}

func TestRWXReadOnly_WriterJobMustMountTheClaimReadWrite(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly = true
	c, k8s := rwxTestBackend(nil)

	state, _ := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingFailed, state)
	_, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "an invalid writer must not create anything")
}

// The dispatch: a persisted ReadWriteMany selection routes the container
// workflow onto the shared claim, a non-durable selection skips caching, and a
// request with no selection is untouched here (it takes the legacy path).
func TestSetupContainerModelCaching_FollowsPersistedSelection(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()

	t.Run("ReadWriteMany selection creates the shared claim", func(t *testing.T) {
		req := rwxRequest(t, rwxSelection(t))
		c, k8s := rwxTestBackend(req)
		_, _, err := c.setupContainerModelCaching(ctx, req, pvc, job, nil)
		require.Error(t, err, "first pass is in progress")
		created, gerr := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
		require.NoError(t, gerr)
		assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, created.Spec.AccessModes)
		updated, gerr := c.clients.BART.NvcaV2beta1().ICMSRequests(rwxTestNamespace).Get(ctx, req.Name, metav1.GetOptions{})
		require.NoError(t, gerr)
		assert.Equal(t, pvc.Name, updated.Status.CacheReferenceName,
			"the request records the shared claim while the writer runs, so the reference sweep sees it in use")
	})

	t.Run("completed shared claim mounts read-only at source and mount", func(t *testing.T) {
		req := rwxRequest(t, rwxSelection(t))
		c, _ := rwxTestBackend(req, boundSharedClaim(true))
		mf, claim, err := c.setupContainerModelCaching(ctx, req, pvc, job, nil)
		require.NoError(t, err)
		assert.Equal(t, pvc.Name, claim)
		pod := &corev1.Pod{Spec: corev1.PodSpec{
			Volumes:    []corev1.Volume{{Name: ModelVolumeName}},
			Containers: []corev1.Container{{VolumeMounts: []corev1.VolumeMount{{Name: ModelVolumeName, ReadOnly: false}}}},
		}}
		mf(pod)
		require.NotNil(t, pod.Spec.Volumes[0].PersistentVolumeClaim)
		assert.Equal(t, pvc.Name, pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
		assert.True(t, pod.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly)
		assert.True(t, pod.Spec.Containers[0].VolumeMounts[0].ReadOnly, "a shared claim is never mounted writable")
	})

	t.Run("a none selection skips caching without touching the cluster", func(t *testing.T) {
		none := rwxSelection(t)
		none.Mode = nvcastorage.ModelCacheSelectionNone
		none.StorageClassName, none.StorageClassUID, none.StorageClassDigest, none.ProfileDigest = "", "", "", ""
		none.Provider, none.Provisioner, none.Transition, none.RequiredAccessModes = "", "", "", nil
		req := rwxRequest(t, none)
		c, k8s := rwxTestBackend(req)
		mf, claim, err := c.setupContainerModelCaching(ctx, req, pvc, job, nil)
		require.NoError(t, err)
		assert.Empty(t, claim)
		require.NotNil(t, mf)
		assert.Empty(t, k8s.Actions(), "no Kubernetes writes for a request whose selection is none")
	})
}

func TestRegularModelCacheKeepsSharedClaim(t *testing.T) {
	assert.True(t, regularModelCacheKeepsSharedClaim(rwxRequest(t, rwxSelection(t))),
		"a shared claim is left for the reference sweep, not deleted with the request")
	assert.False(t, regularModelCacheKeepsSharedClaim(rwxRequest(t, nil)),
		"a legacy request without a selection keeps the existing cleanup")
	rox := rwxSelection(t)
	rox.Transition = nvcastorage.ModelCacheTransitionROXReadOnly
	rox.Provider, rox.Provisioner = nvcastorage.ModelCacheProviderNVMesh, nvcastorage.NVMeshStorageClassProvisioner
	rox.RequiredAccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadOnlyMany}
	rox.RequiredMountOptions = []string{"ro", "norecovery", "nouuid"}
	assert.False(t, regularModelCacheKeepsSharedClaim(rwxRequest(t, rox)),
		"the NVMesh shape keeps its per-request claim cleanup")
}

func TestRWXReadOnly_StaleWriterWitnessDoesNotMarkANewClaim(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	done := job.DeepCopy()
	done.Status.Succeeded = 1
	now := metav1.Now()
	done.Status.CompletionTime = &now
	done.Spec.Template.Annotations = map[string]string{nvcastorage.ModelCacheWriterPVCUIDAnnotationKey: "an-earlier-claim"}
	c, k8s := rwxTestBackend(nil, boundSharedClaim(false), done)

	state, claim := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingInProgress, state, "a Job that populated an earlier claim proves nothing about this one")
	assert.Equal(t, pvc.Name, claim)

	got, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, got.Labels, nvcastorage.ModelCachePopulatedLabelKey, "the claim must not be marked populated")
	_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "the stale writer is removed so a fresh one runs")
}

func TestRWXReadOnly_RefusesAClaimOnAnotherStorageClass(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	elsewhere := boundSharedClaim(true)
	other := "previous-backend"
	elsewhere.Spec.StorageClassName = &other
	c, k8s := rwxTestBackend(nil, elsewhere)

	state, _ := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingFailed, state, "same handle on a different backend must not be mounted")

	still, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	require.NoError(t, err, "and must not be deleted either")
	assert.Equal(t, other, *still.Spec.StorageClassName)
	_, err = k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err))
}

func TestRWXReadOnly_WriterJobMustMountTheClaimInAContainer(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()
	job.Spec.Template.Spec.Containers[0].VolumeMounts = nil
	c, k8s := rwxTestBackend(nil)

	state, _ := c.setupRWXReadOnlyModelCachingForRequest(ctx, rwxRequest(t, rwxSelection(t)), pvc, job, rwxSelection(t))
	assert.Equal(t, ModelCachingFailed, state, "a writer that never mounts the claim would mark an empty claim populated")
	_, err := k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err))
}

func TestCleanupSharedClaimRequestArtifacts(t *testing.T) {
	ctx := rwxTestContext(t)
	pvc, job := rwxArtifacts()

	t.Run("a running shared writer and the claim both survive one request's cleanup", func(t *testing.T) {
		active := job.DeepCopy()
		active.Status.Active = 1
		c, k8s := rwxTestBackend(nil, boundSharedClaim(false), active)
		require.NoError(t, c.cleanupSharedClaimRequestArtifacts(ctx, job, pvc.Name))
		_, err := k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
		require.NoError(t, err, "other requests are waiting on this writer")
		_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
		require.NoError(t, err, "the shared claim is reclaimed by the reference sweep, not by request cleanup")
	})

	t.Run("a finished writer is removed, the claim stays", func(t *testing.T) {
		done := job.DeepCopy()
		done.Status.Succeeded = 1
		now := metav1.Now()
		done.Status.CompletionTime = &now
		c, k8s := rwxTestBackend(nil, boundSharedClaim(true), done)
		require.NoError(t, c.cleanupSharedClaimRequestArtifacts(ctx, job, pvc.Name))
		_, err := k8s.BatchV1().Jobs(rwxTestNamespace).Get(ctx, job.Name, metav1.GetOptions{})
		assert.True(t, errors.IsNotFound(err))
		_, err = k8s.CoreV1().PersistentVolumeClaims(rwxTestNamespace).Get(ctx, pvc.Name, metav1.GetOptions{})
		require.NoError(t, err)
	})
}
