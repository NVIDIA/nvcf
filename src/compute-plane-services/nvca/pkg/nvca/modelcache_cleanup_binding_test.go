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
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type regularCleanupTestFixture struct {
	backend K8sComputeBackend
	req     *nvcav2beta1.ICMSRequest
	binding *nvcav2beta1.ModelCacheBinding
	rwPVC   *corev1.PersistentVolumeClaim
	job     *batchv1.Job
	pv      *corev1.PersistentVolume
}

func newRegularCleanupTestFixture(t *testing.T) *regularCleanupTestFixture {
	t.Helper()
	resolved := resolvedSelectionForSetup(t)
	raw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionDurable, resolved)
	backend := testContainerModelCacheBackend(fakek8sclient.NewSimpleClientset())
	backend.bk8s.k8sTimeConfig = (&k8sutil.TimeConfig{}).Complete()
	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "request", Namespace: RequestsNamespace},
	}
	binding := installActiveRegularModelCacheBinding(t, &backend, req, raw)
	labels := map[string]string{nvcastorage.ModelCacheBindingUIDLabelKey: string(binding.UID)}
	storageClassName := nvcastorage.DefaultModelCacheStorageClassName
	rwPVC := &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            binding.Spec.Resources.PersistentVolumeClaimNames[0],
			Namespace:       binding.Spec.Resources.WriterNamespace,
			UID:             types.UID("rw-pvc-uid"),
			ResourceVersion: "rw-pvc-rv",
			Labels:          labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClassName,
			VolumeName:       "cache-pv",
		},
	}
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            binding.Spec.Resources.JobNames[0],
			Namespace:       binding.Spec.Resources.WriterNamespace,
			UID:             types.UID("writer-job-uid"),
			ResourceVersion: "writer-job-rv",
			Labels:          labels,
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever},
		}},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "cache-pv",
			UID:             types.UID("cache-pv-uid"),
			ResourceVersion: "cache-pv-rv",
			Labels:          labels,
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName:              storageClassName,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       binding.Spec.Decision.Provisioner,
					VolumeHandle: "cache-volume-handle",
				},
			},
			ClaimRef: &corev1.ObjectReference{
				Namespace: rwPVC.Namespace,
				Name:      rwPVC.Name,
				UID:       rwPVC.UID,
			},
		},
	}
	return &regularCleanupTestFixture{
		backend: backend,
		req:     req,
		binding: binding,
		rwPVC:   rwPVC,
		job:     job,
		pv:      pv,
	}
}

func newRWXRegularCleanupTestFixture(t *testing.T) *regularCleanupTestFixture {
	t.Helper()
	runtimeFixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	runtimeFixture.backend.bk8s.k8sTimeConfig = (&k8sutil.TimeConfig{}).Complete()
	rwPVC := runtimeFixture.boundPVC(false)
	job := runtimeFixture.writerJob()
	pv := runtimeFixture.boundPV(rwPVC)
	pv.UID = types.UID("cache-pv-uid")
	pv.ResourceVersion = "cache-pv-rv"
	return &regularCleanupTestFixture{
		backend: runtimeFixture.backend,
		req:     runtimeFixture.req,
		binding: runtimeFixture.binding,
		rwPVC:   rwPVC,
		job:     job,
		pv:      pv,
	}
}

func (f *regularCleanupTestFixture) useK8sObjects(objects ...runtime.Object) *fakek8sclient.Clientset {
	k8sClient := fakek8sclient.NewSimpleClientset(objects...)
	f.backend.clients.K8s = k8sClient
	return k8sClient
}

func encodeCleanupObject(t *testing.T, obj runtime.Object) string {
	t.Helper()
	raw, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}

func setCleanupArtifacts(
	t *testing.T,
	req *nvcav2beta1.ICMSRequest,
	rwPVC *corev1.PersistentVolumeClaim,
	job *batchv1.Job,
) {
	t.Helper()
	req.Spec.CreationMsgInfo.LaunchArtifacts = function.LaunchArtifacts{
		{Type: function.LaunchArtifactTypeBlockDevice, Specification: encodeCleanupObject(t, rwPVC)},
		{Type: function.LaunchArtifactTypeInitCacheJob, Specification: encodeCleanupObject(t, job)},
	}
}

func requireDeleteIdentity(
	t *testing.T,
	actions []k8stesting.Action,
	resource string,
	name string,
	wantUID types.UID,
	wantResourceVersion string,
) {
	t.Helper()
	for _, action := range actions {
		if action.GetVerb() != "delete" || action.GetResource().Resource != resource {
			continue
		}
		deleteAction := action.(k8stesting.DeleteAction)
		if deleteAction.GetName() != name {
			continue
		}
		preconditions := deleteAction.GetDeleteOptions().Preconditions
		require.NotNil(t, preconditions)
		require.NotNil(t, preconditions.UID)
		assert.Equal(t, wantUID, *preconditions.UID)
		require.NotNil(t, preconditions.ResourceVersion)
		assert.Equal(t, wantResourceVersion, *preconditions.ResourceVersion)
		return
	}
	t.Fatalf("delete action for %s %s not found", resource, name)
}

func TestCleanupModelCachingResourcesRejectsUnownedTargetsBeforeWrites(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*regularCleanupTestFixture)
		wantErr string
	}{
		{
			name: "missing PVC binding label",
			mutate: func(f *regularCleanupTestFixture) {
				f.rwPVC.Labels = nil
			},
			wantErr: `has binding UID "", want "binding-uid"`,
		},
		{
			name: "wrong Job binding label",
			mutate: func(f *regularCleanupTestFixture) {
				f.job.Labels = map[string]string{nvcastorage.ModelCacheBindingUIDLabelKey: "other-binding"}
			},
			wantErr: `has binding UID "other-binding", want "binding-uid"`,
		},
		{
			name: "missing PV binding label",
			mutate: func(f *regularCleanupTestFixture) {
				f.pv.Labels = nil
			},
			wantErr: `has binding UID "", want "binding-uid"`,
		},
		{
			name: "stale PV claim owner UID",
			mutate: func(f *regularCleanupTestFixture) {
				f.pv.Spec.ClaimRef.UID = types.UID("stale-pvc-uid")
			},
			wantErr: `claimRef UID "stale-pvc-uid" does not match PVC UID "rw-pvc-uid"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRegularCleanupTestFixture(t)
			tt.mutate(fixture)
			k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

			err := fixture.backend.CleanupModelCachingResources(
				newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name)
			require.ErrorContains(t, err, tt.wantErr)
			assertNoKubernetesWrites(t, k8sClient.Actions())
		})
	}
}

func TestCleanupModelCachingResourcesSkipsSharedBinding(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	stored, err := fixture.backend.clients.BART.NvcaV2beta1().ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	stored.Status.RequestReferences = append(stored.Status.RequestReferences,
		nvcav2beta1.ModelCacheBindingRequestReference{
			Namespace: fixture.req.Namespace,
			Name:      "other-request",
			UID:       types.UID("other-request-uid"),
		})
	_, err = fixture.backend.clients.BART.NvcaV2beta1().ModelCacheBindings(stored.Namespace).
		UpdateStatus(t.Context(), stored, metav1.UpdateOptions{})
	require.NoError(t, err)
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

	require.NoError(t, fixture.backend.CleanupModelCachingResources(
		newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name))
	assert.Empty(t, k8sClient.Actions(), "another exact request reference must preserve the shared writer")
}

func TestCleanupModelCachingResourcesResumesExactRetiringBinding(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	stored, err := fixture.backend.clients.BART.NvcaV2beta1().ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	stored.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
	_, err = fixture.backend.clients.BART.NvcaV2beta1().ModelCacheBindings(stored.Namespace).
		UpdateStatus(t.Context(), stored, metav1.UpdateOptions{})
	require.NoError(t, err)
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

	require.NoError(t, fixture.backend.CleanupModelCachingResources(
		newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name))
	_, err = k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	_, err = k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestCleanupModelCachingResourcesResumesAfterPVPolicyUpdate(t *testing.T) {
	tests := []struct {
		name    string
		fixture func(*testing.T) *regularCleanupTestFixture
	}{
		{name: "NVMesh", fixture: newRegularCleanupTestFixture},
		{name: "rwxReadOnly", fixture: newRWXRegularCleanupTestFixture},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := tt.fixture(t)
			k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)
			pvcDeleteAttempts := 0
			k8sClient.Fake.PrependReactor(
				"delete", "persistentvolumeclaims",
				func(k8stesting.Action) (bool, runtime.Object, error) {
					pvcDeleteAttempts++
					if pvcDeleteAttempts == 1 {
						return true, nil, apierrors.NewServiceUnavailable(
							"transient PVC delete failure")
					}
					return false, nil, nil
				})

			err := fixture.backend.CleanupModelCachingResources(
				newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name)
			require.Error(t, err)
			assert.True(t, apierrors.IsServiceUnavailable(err))

			updatedPV, getErr := k8sClient.CoreV1().PersistentVolumes().
				Get(t.Context(), fixture.pv.Name, metav1.GetOptions{})
			require.NoError(t, getErr)
			assert.Equal(t, corev1.PersistentVolumeReclaimDelete,
				updatedPV.Spec.PersistentVolumeReclaimPolicy)
			_, getErr = k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
				Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
			require.NoError(t, getErr, "the first transient delete must leave the PVC for retry")

			require.NoError(t, fixture.backend.CleanupModelCachingResources(
				newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name))
			assert.Equal(t, 2, pvcDeleteAttempts)
			_, getErr = k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
				Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
			assert.True(t, apierrors.IsNotFound(getErr))
		})
	}
}

func TestCleanupModelCachingResourcesRejectsIdentityDriftAfterPVPolicyUpdate(t *testing.T) {
	fixture := newRWXRegularCleanupTestFixture(t)
	stored, err := fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	stored.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
	_, err = fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(stored.Namespace).
		UpdateStatus(t.Context(), stored, metav1.UpdateOptions{})
	require.NoError(t, err)
	fixture.pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
	fixture.pv.Spec.CSI.Driver = "foreign.csi.example.com"
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

	err = fixture.backend.CleanupModelCachingResources(
		newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name)
	require.ErrorContains(t, err, "CSI driver")
	assertNoKubernetesWrites(t, k8sClient.Actions())
	_, getErr := k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	require.NoError(t, getErr)
	_, getErr = k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	require.NoError(t, getErr)
}

func TestSetupModelCachingForRequestResumesRetiringCleanupWithoutServingReaders(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	stored, err := fixture.backend.clients.BART.NvcaV2beta1().ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	stored.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
	_, err = fixture.backend.clients.BART.NvcaV2beta1().ModelCacheBindings(stored.Namespace).
		UpdateStatus(t.Context(), stored, metav1.UpdateOptions{})
	require.NoError(t, err)
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

	state, roPVCName, err := fixture.backend.SetupModelCachingForRequest(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
		fixture.req, false, noOpRegularModelCacheMutation)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, roPVCName)
	require.ErrorIs(t, err, errRegularModelCacheBindingRetiring)
	assert.True(t, nvcaerrors.IsTerminal(err))
	_, getErr := k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr))
	_, getErr = k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr))
}

func TestCleanupModelCachingResourcesDeletesOnlyExactOwner(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

	require.NoError(t, fixture.backend.CleanupModelCachingResources(
		newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name))
	stored, err := fixture.backend.clients.BART.NvcaV2beta1().ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseRetiring, stored.Status.Phase)

	_, err = k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	_, err = k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	pv, err := k8sClient.CoreV1().PersistentVolumes().Get(t.Context(), fixture.pv.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.PersistentVolumeReclaimDelete, pv.Spec.PersistentVolumeReclaimPolicy)
	requireDeleteIdentity(t, k8sClient.Actions(), "jobs", fixture.job.Name,
		fixture.job.UID, fixture.job.ResourceVersion)
	requireDeleteIdentity(t, k8sClient.Actions(), "persistentvolumeclaims", fixture.rwPVC.Name,
		fixture.rwPVC.UID, fixture.rwPVC.ResourceVersion)
}

func TestCleanupModelCachingSetupArtifactsUsesPersistedBindingAcrossGateDrift(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	fixture.backend.bk8s.cachingSupportEnabled = false
	setCleanupArtifacts(t, fixture.req, fixture.rwPVC, fixture.job)
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

	require.NoError(t, fixture.backend.CleanupModelCachingSetupArtifacts(newTestContext(), fixture.req))
	updatedPV, err := k8sClient.CoreV1().PersistentVolumes().Get(
		t.Context(), fixture.pv.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.PersistentVolumeReclaimDelete, updatedPV.Spec.PersistentVolumeReclaimPolicy)
	requireDeleteIdentity(t, k8sClient.Actions(), "jobs", fixture.job.Name,
		fixture.job.UID, fixture.job.ResourceVersion)
	requireDeleteIdentity(t, k8sClient.Actions(), "persistentvolumeclaims", fixture.rwPVC.Name,
		fixture.rwPVC.UID, fixture.rwPVC.ResourceVersion)
}

func TestCleanupModelCachingSetupArtifactsRejectsNameOutsideBindingIntent(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	wrongPVC := fixture.rwPVC.DeepCopy()
	wrongPVC.Name = "rw-pvc-wrong-cache"
	setCleanupArtifacts(t, fixture.req, wrongPVC, fixture.job)
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job)

	err := fixture.backend.CleanupModelCachingSetupArtifacts(newTestContext(), fixture.req)
	require.ErrorContains(t, err, "outside binding resource intent")
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestCleanupModelCachingResourcesLegacyRequestRemainsCompatible(t *testing.T) {
	rwPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-pvc-legacy", Namespace: RequestsNamespace},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "legacy-pv"},
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "writer-job-legacy", Namespace: RequestsNamespace}}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-pv"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
		},
	}
	k8sClient := fakek8sclient.NewSimpleClientset(rwPVC, job, pv)
	backend := testContainerModelCacheBackend(k8sClient)
	req := &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{
		Name: "legacy-request", Namespace: RequestsNamespace,
	}}

	require.NoError(t, backend.CleanupModelCachingResources(
		newTestContext(), req, rwPVC.DeepCopy(), job.Name))
	updatedPV, err := k8sClient.CoreV1().PersistentVolumes().Get(t.Context(), pv.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.PersistentVolumeReclaimDelete, updatedPV.Spec.PersistentVolumeReclaimPolicy)
	_, err = k8sClient.CoreV1().PersistentVolumeClaims(rwPVC.Namespace).
		Get(t.Context(), rwPVC.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestComputeCleanupCacheReferencesLeavesBindingOwnedPVCToBindingLifecycle(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.pv)

	require.NoError(t, fixture.backend.ComputeCleanupCacheReferences(
		newTestContext(), []string{fixture.rwPVC.Name}))
	assertNoKubernetesWrites(t, k8sClient.Actions())
	gotPVC, err := k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, fixture.rwPVC.UID, gotPVC.UID)
	gotPV, err := k8sClient.CoreV1().PersistentVolumes().Get(t.Context(), fixture.pv.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.PersistentVolumeReclaimRetain, gotPV.Spec.PersistentVolumeReclaimPolicy)
}

func TestMakePVLabelSelectorUsesPersistedBindingUID(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)

	selector, err := makePVLabelSelectorForCacheRequest(fixture.req)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%s=%s", nvcastorage.ModelCacheBindingUIDLabelKey, fixture.binding.UID), selector)

	legacy := &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{
		FunctionDetails: function.Details{FunctionVersionID: "legacy-version"},
	}}
	selector, err = makePVLabelSelectorForCacheRequest(legacy)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%s=legacy-version", fnVersionIDLabelString), selector)
}

func TestSetupInitCacheJobBlockDeviceValidatesClassOnlyBeforeFirstWriterCreate(t *testing.T) {
	t.Run("missing writer and drifted class fails terminal before create", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		drifted := selectionStorageClass()
		drifted.Parameters["pool"] = "drifted"
		k8sClient := fixture.useK8sObjects(drifted)
		className := nvcastorage.DefaultModelCacheStorageClassName
		fixture.rwPVC.Spec.StorageClassName = &className
		fixture.job.Spec.Template.Labels = map[string]string{
			nvcastorage.ModelCacheBindingUIDLabelKey: string(fixture.binding.UID),
		}

		err := fixture.backend.SetupInitCacheJobBlockDevice(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(), fixture.req)
		require.ErrorContains(t, err, "configuration digest changed")
		assert.True(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("missing writer and missing class fails terminal before create", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		k8sClient := fixture.useK8sObjects()
		className := nvcastorage.DefaultModelCacheStorageClassName
		fixture.rwPVC.Spec.StorageClassName = &className
		fixture.job.Spec.Template.Labels = map[string]string{
			nvcastorage.ModelCacheBindingUIDLabelKey: string(fixture.binding.UID),
		}

		err := fixture.backend.SetupInitCacheJobBlockDevice(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(), fixture.req)
		require.ErrorContains(t, err, "get selected model cache StorageClass")
		assert.True(t, apierrors.IsNotFound(err))
		assert.True(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("existing exact writer survives live class deletion", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		className := nvcastorage.DefaultModelCacheStorageClassName
		fixture.rwPVC.Spec.StorageClassName = &className
		fixture.job.Spec.Template.Labels = map[string]string{
			nvcastorage.ModelCacheBindingUIDLabelKey: string(fixture.binding.UID),
		}
		k8sClient := fixture.useK8sObjects(fixture.rwPVC)

		require.NoError(t, fixture.backend.SetupInitCacheJobBlockDevice(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(), fixture.req))
		for _, action := range k8sClient.Actions() {
			assert.NotEqual(t, "storageclasses", action.GetResource().Resource,
				"an existing exact writer must not re-read the live StorageClass")
		}
		_, err := k8sClient.BatchV1().Jobs(fixture.job.Namespace).
			Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
		require.NoError(t, err)
	})

	t.Run("encrypted derived writer does not read base class", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(
			fixture.req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey])
		require.NoError(t, err)
		selection.EncryptionRequired = true
		selection.BindingName = ""
		selection.BindingUID = ""
		raw, err := selection.Marshal()
		require.NoError(t, err)
		fixture.binding = installActiveRegularModelCacheBinding(t, &fixture.backend, fixture.req, raw)
		derivedClassName := fixture.binding.Spec.Resources.StorageClassNames[0]
		fixture.rwPVC.Spec.StorageClassName = &derivedClassName
		fixture.job.Spec.Template.Labels = map[string]string{
			nvcastorage.ModelCacheBindingUIDLabelKey: string(fixture.binding.UID),
		}
		k8sClient := fixture.useK8sObjects()

		require.NoError(t, fixture.backend.SetupInitCacheJobBlockDevice(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(), fixture.req))
		for _, action := range k8sClient.Actions() {
			assert.NotEqual(t, "storageclasses", action.GetResource().Resource,
				"an encrypted derived writer must not read the base StorageClass")
		}
	})
}

func TestSetupPVCForReadersRejectsStaleSameNamePVCUID(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	fixture.req.Spec.FunctionDetails.FunctionVersionID = "function-version"
	fixture.pv.Spec.ClaimRef.UID = types.UID("stale-pvc-uid")
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.pv)

	phase, err := fixture.backend.SetupPVCForReaders(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.Name, fixture.req, nil)
	assert.Equal(t, ROPVCSetupFailed, phase)
	require.ErrorContains(t, err, `claimRef UID "stale-pvc-uid" does not match PVC UID "rw-pvc-uid"`)
	assertNoKubernetesWrites(t, k8sClient.Actions())
}
func boundReaderForRegularCleanupFixture(
	fixture *regularCleanupTestFixture,
) *corev1.PersistentVolumeClaim {
	reader := fixture.rwPVC.DeepCopy()
	reader.Name = ROPVCPrefix + strings.TrimPrefix(reader.Name, RWPVCPrefix)
	reader.UID = types.UID("ro-pvc-uid")
	reader.ResourceVersion = "ro-pvc-rv"
	reader.Spec.AccessModes = ROAccessMode
	reader.Status.Phase = corev1.ClaimBound
	return reader
}

func boundReaderPVForRegularCleanupFixture(
	fixture *regularCleanupTestFixture,
	reader *corev1.PersistentVolumeClaim,
) *corev1.PersistentVolume {
	pv := fixture.pv.DeepCopy()
	pv.Spec.AccessModes = ROAccessMode
	pv.Spec.ClaimRef = &corev1.ObjectReference{
		Namespace: reader.Namespace,
		Name:      reader.Name,
		UID:       reader.UID,
	}
	return pv
}

func noOpRegularModelCacheMutation(client.Object) {}

func TestSetupInitCacheJobBlockDevicePreservesTransientStorageClassError(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	className := nvcastorage.DefaultModelCacheStorageClassName
	fixture.rwPVC.Spec.StorageClassName = &className
	fixture.job.Spec.Template.Labels = map[string]string{
		nvcastorage.ModelCacheBindingUIDLabelKey: string(fixture.binding.UID),
	}
	k8sClient := fixture.useK8sObjects()
	k8sClient.Fake.PrependReactor(
		"get", "storageclasses",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("storage API unavailable")
		})

	err := fixture.backend.SetupInitCacheJobBlockDevice(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(), fixture.req)
	require.Error(t, err)
	assert.True(t, apierrors.IsServiceUnavailable(err))
	assert.False(t, nvcaerrors.IsTerminal(err))
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestSetupModelCachingForRequestPropagatesTransientReads(t *testing.T) {
	t.Run("reader PVC Get", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		k8sClient := fixture.useK8sObjects()
		roPVCName := ROPVCPrefix + strings.TrimPrefix(fixture.rwPVC.Name, RWPVCPrefix)
		k8sClient.Fake.PrependReactor(
			"get", "persistentvolumeclaims",
			func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.(k8stesting.GetAction).GetName() != roPVCName {
					return false, nil, nil
				}
				return true, nil, apierrors.NewServiceUnavailable("PVC API unavailable")
			})

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingInProgress, state)
		require.Error(t, err)
		assert.True(t, apierrors.IsServiceUnavailable(err))
		assert.False(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("init Job Get", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		k8sClient := fixture.useK8sObjects()
		k8sClient.Fake.PrependReactor(
			"get", "jobs",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("Job API unavailable")
			})

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingInProgress, state)
		require.Error(t, err)
		assert.True(t, apierrors.IsServiceUnavailable(err))
		assert.False(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("PV List", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		k8sClient := fixture.useK8sObjects()
		k8sClient.Fake.PrependReactor(
			"list", "persistentvolumes",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("PV API unavailable")
			})

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingInProgress, state)
		require.Error(t, err)
		assert.True(t, apierrors.IsServiceUnavailable(err))
		assert.False(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("PV Get during reader transition", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		reader := boundReaderForRegularCleanupFixture(fixture)
		k8sClient := fixture.useK8sObjects(reader)
		k8sClient.Fake.PrependReactor(
			"get", "persistentvolumes",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("PV API unavailable")
			})

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingInProgress, state)
		require.Error(t, err)
		assert.True(t, apierrors.IsServiceUnavailable(err))
		assert.False(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("cleanup PV Get", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		fixture.rwPVC.Status.Phase = corev1.ClaimBound
		fixture.job.Status.Failed = 7
		k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)
		k8sClient.Fake.PrependReactor(
			"get", "persistentvolumes",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("PV API unavailable")
			})

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingInProgress, state)
		require.Error(t, err)
		assert.True(t, apierrors.IsServiceUnavailable(err))
		assert.False(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("encryption API", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		k8sClient := fixture.useK8sObjects()
		k8sClient.Fake.PrependReactor(
			"get", "secrets",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("Secret API unavailable")
			})

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, true, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingInProgress, state)
		require.Error(t, err)
		assert.True(t, apierrors.IsServiceUnavailable(err))
		assert.False(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("Forbidden reader PVC Get remains a reconcile error", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		k8sClient := fixture.useK8sObjects()
		roPVCName := ROPVCPrefix + strings.TrimPrefix(fixture.rwPVC.Name, RWPVCPrefix)
		k8sClient.Fake.PrependReactor(
			"get", "persistentvolumeclaims",
			func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.(k8stesting.GetAction).GetName() != roPVCName {
					return false, nil, nil
				}
				return true, nil, apierrors.NewForbidden(
					corev1.Resource("persistentvolumeclaims"), roPVCName, fmt.Errorf("forbidden"))
			})

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingInProgress, state)
		require.Error(t, err)
		assert.True(t, apierrors.IsForbidden(err))
		assert.False(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})
}

func TestSetupPVCForReadersPreservesTransientWriterGet(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	k8sClient := fixture.useK8sObjects()
	k8sClient.Fake.PrependReactor(
		"get", "persistentvolumeclaims",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.(k8stesting.GetAction).GetName() != fixture.rwPVC.Name {
				return false, nil, nil
			}
			return true, nil, apierrors.NewServiceUnavailable("PVC API unavailable")
		})

	phase, err := fixture.backend.SetupPVCForReaders(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.Name,
		fixture.req, noOpRegularModelCacheMutation)
	assert.Equal(t, ROPVCSetupQueryFailed, phase)
	require.Error(t, err)
	assert.True(t, apierrors.IsServiceUnavailable(err))
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestSetupPVCForReadersRejectsUnlabeledPVWhenWriterIsAbsent(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	fixture.req.Spec.FunctionDetails.FunctionVersionID = "function-version"
	unlabeledPV := fixture.pv.DeepCopy()
	unlabeledPV.Labels = map[string]string{fnVersionIDLabelString: "function-version"}
	listedPV := fixture.pv.DeepCopy()
	k8sClient := fixture.useK8sObjects(listedPV)
	k8sClient.Fake.PrependReactor(
		"get", "persistentvolumes",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.(k8stesting.GetAction).GetName() != unlabeledPV.Name {
				return false, nil, nil
			}
			return true, unlabeledPV, nil
		})

	phase, err := fixture.backend.SetupPVCForReaders(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.Name,
		fixture.req, noOpRegularModelCacheMutation)
	assert.Equal(t, ROPVCSetupFailed, phase)
	require.ErrorContains(t, err, "writer PVC is absent")
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestSetupModelCachingForRequestRejectsForeignTransitionArtifacts(t *testing.T) {
	t.Run("foreign same-name Job", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		reader := boundReaderForRegularCleanupFixture(fixture)
		readerPV := boundReaderPVForRegularCleanupFixture(fixture, reader)
		foreignJob := fixture.job.DeepCopy()
		foreignJob.Labels = map[string]string{
			nvcastorage.ModelCacheBindingUIDLabelKey: "foreign-binding",
		}
		k8sClient := fixture.useK8sObjects(reader, foreignJob, readerPV)

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingFailed, state)
		require.ErrorContains(t, err, "foreign-binding")
		assert.True(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("missing Job resourceVersion", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		reader := boundReaderForRegularCleanupFixture(fixture)
		readerPV := boundReaderPVForRegularCleanupFixture(fixture, reader)
		fixture.job.ResourceVersion = ""
		k8sClient := fixture.useK8sObjects(reader, fixture.job, readerPV)

		state, _, err := fixture.backend.SetupModelCachingForRequest(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
			fixture.req, false, noOpRegularModelCacheMutation)
		assert.Equal(t, ModelCachingFailed, state)
		require.ErrorContains(t, err, "incomplete delete identity")
		assert.True(t, nvcaerrors.IsTerminal(err))
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})
}

func TestSuccessfulWriterTransitionIgnoresOtherBindingReferences(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	stored, err := fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	stored.Status.RequestReferences = append(stored.Status.RequestReferences,
		nvcav2beta1.ModelCacheBindingRequestReference{
			Namespace: fixture.req.Namespace,
			Name:      "other-request",
			UID:       types.UID("other-request-uid"),
		})
	_, err = fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(stored.Namespace).
		UpdateStatus(t.Context(), stored, metav1.UpdateOptions{})
	require.NoError(t, err)

	reader := boundReaderForRegularCleanupFixture(fixture)
	readerPV := boundReaderPVForRegularCleanupFixture(fixture, reader)
	k8sClient := fixture.useK8sObjects(reader, fixture.job, readerPV)
	state, roPVCName, err := fixture.backend.SetupModelCachingForRequest(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
		fixture.req, false, noOpRegularModelCacheMutation)
	require.NoError(t, err)
	assert.Equal(t, ModelCachingCompleted, state)
	assert.Equal(t, reader.Name, roPVCName)
	requireDeleteIdentity(t, k8sClient.Actions(), "jobs", fixture.job.Name,
		fixture.job.UID, fixture.job.ResourceVersion)
}

func TestSuccessfulWriterTransitionDeleteConflictIsRetryable(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	reader := boundReaderForRegularCleanupFixture(fixture)
	readerPV := boundReaderPVForRegularCleanupFixture(fixture, reader)
	k8sClient := fixture.useK8sObjects(reader, fixture.job, readerPV)
	k8sClient.Fake.PrependReactor(
		"delete", "jobs",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			deleteAction := action.(k8stesting.DeleteAction)
			preconditions := deleteAction.GetDeleteOptions().Preconditions
			require.NotNil(t, preconditions)
			require.NotNil(t, preconditions.UID)
			assert.Equal(t, fixture.job.UID, *preconditions.UID)
			require.NotNil(t, preconditions.ResourceVersion)
			assert.Equal(t, fixture.job.ResourceVersion, *preconditions.ResourceVersion)
			return true, nil, apierrors.NewConflict(
				corev1.Resource("jobs"), fixture.job.Name, fmt.Errorf("delete race"))
		})

	state, _, err := fixture.backend.SetupModelCachingForRequest(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
		fixture.req, false, noOpRegularModelCacheMutation)
	assert.Equal(t, ModelCachingInProgress, state)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err))
	assert.False(t, nvcaerrors.IsTerminal(err))
	_, getErr := k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	require.NoError(t, getErr)
}

func TestSetupPVCForReadersRejectsForeignSameNameArtifacts(t *testing.T) {
	t.Run("writer PVC", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		foreignWriter := fixture.rwPVC.DeepCopy()
		foreignWriter.Labels = map[string]string{
			nvcastorage.ModelCacheBindingUIDLabelKey: "foreign-binding",
		}
		k8sClient := fixture.useK8sObjects(foreignWriter, fixture.job, fixture.pv)

		phase, err := fixture.backend.SetupPVCForReaders(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.Name,
			fixture.req, noOpRegularModelCacheMutation)
		assert.Equal(t, ROPVCSetupFailed, phase)
		require.ErrorContains(t, err, "foreign-binding")
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("reader PVC wins AlreadyExists race", func(t *testing.T) {
		fixture := newRegularCleanupTestFixture(t)
		fixture.req.Spec.FunctionDetails.FunctionVersionID = "function-version"
		fixture.pv.Labels[fnVersionIDLabelString] = "function-version"
		foreignReader := boundReaderForRegularCleanupFixture(fixture)
		foreignReader.Labels = map[string]string{
			nvcastorage.ModelCacheBindingUIDLabelKey: "foreign-binding",
		}
		foreignReader.UID = types.UID("foreign-reader-uid")
		foreignReader.ResourceVersion = "foreign-reader-rv"
		k8sClient := fixture.useK8sObjects(
			fixture.rwPVC, fixture.job, fixture.pv, foreignReader)

		roPVCGets := 0
		k8sClient.Fake.PrependReactor(
			"get", "persistentvolumeclaims",
			func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.(k8stesting.GetAction).GetName() != foreignReader.Name {
					return false, nil, nil
				}
				roPVCGets++
				if roPVCGets == 1 {
					return true, nil, apierrors.NewNotFound(
						corev1.Resource("persistentvolumeclaims"), foreignReader.Name)
				}
				return false, nil, nil
			})

		previousSkip := skipVolumeDetachCheck
		skipVolumeDetachCheck = true
		t.Cleanup(func() { skipVolumeDetachCheck = previousSkip })

		phase, err := fixture.backend.SetupPVCForReaders(
			newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.Name,
			fixture.req, noOpRegularModelCacheMutation)
		assert.Equal(t, ROPVCSetupFailed, phase)
		require.ErrorContains(t, err, "foreign-binding")
		got, getErr := k8sClient.CoreV1().PersistentVolumeClaims(foreignReader.Namespace).
			Get(t.Context(), foreignReader.Name, metav1.GetOptions{})
		require.NoError(t, getErr)
		assert.Equal(t, foreignReader.UID, got.UID)
		for _, action := range k8sClient.Actions() {
			if action.GetVerb() == "delete" &&
				action.GetResource().Resource == "persistentvolumeclaims" {
				assert.NotEqual(t, foreignReader.Name,
					action.(k8stesting.DeleteAction).GetName())
			}
		}
	})
}
func TestSetupPVCForReadersRevalidatesPVClaimInsideUpdateRetry(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	fixture.req.Spec.FunctionDetails.FunctionVersionID = "function-version"
	fixture.pv.Labels[fnVersionIDLabelString] = "function-version"
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)

	pvGets := 0
	k8sClient.Fake.PrependReactor(
		"get", "persistentvolumes",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			pvGets++
			if pvGets != 3 {
				return false, nil, nil
			}
			racedPV := fixture.pv.DeepCopy()
			racedPV.Spec.ClaimRef.Name = "foreign-reader"
			return true, racedPV, nil
		})

	previousSkip := skipVolumeDetachCheck
	skipVolumeDetachCheck = true
	t.Cleanup(func() { skipVolumeDetachCheck = previousSkip })

	phase, err := fixture.backend.SetupPVCForReaders(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.Name,
		fixture.req, noOpRegularModelCacheMutation)
	assert.Equal(t, ROPVUpdateFailed, phase)
	require.ErrorContains(t, err, "outside binding intent")
	for _, action := range k8sClient.Actions() {
		if action.GetVerb() == "update" &&
			action.GetResource().Resource == "persistentvolumes" {
			t.Fatalf("PV ownership changed during retry; refusing update was required")
		}
	}
}

func TestSetupModelCachingForRequestRejectsUnownedBoundReaderPV(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*corev1.PersistentVolume)
		wantErr string
	}{
		{
			name: "foreign PV binding",
			mutate: func(pv *corev1.PersistentVolume) {
				pv.Labels[nvcastorage.ModelCacheBindingUIDLabelKey] = "foreign-binding"
			},
			wantErr: "foreign-binding",
		},
		{
			name: "stale reader claim UID",
			mutate: func(pv *corev1.PersistentVolume) {
				pv.Spec.ClaimRef.UID = types.UID("stale-reader-uid")
			},
			wantErr: "claimRef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRegularCleanupTestFixture(t)
			reader := boundReaderForRegularCleanupFixture(fixture)
			readerPV := boundReaderPVForRegularCleanupFixture(fixture, reader)
			tt.mutate(readerPV)
			k8sClient := fixture.useK8sObjects(reader, readerPV)

			state, _, err := fixture.backend.SetupModelCachingForRequest(
				newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
				fixture.req, false, noOpRegularModelCacheMutation)
			assert.Equal(t, ModelCachingFailed, state)
			require.ErrorContains(t, err, tt.wantErr)
			assert.True(t, nvcaerrors.IsTerminal(err))
			assertNoKubernetesWrites(t, k8sClient.Actions())
		})
	}
}

func TestSetupModelCachingForRequestAllowsPendingOwnedReader(t *testing.T) {
	fixture := newRegularCleanupTestFixture(t)
	reader := boundReaderForRegularCleanupFixture(fixture)
	reader.Status.Phase = corev1.ClaimPending
	reader.CreationTimestamp = metav1.Now()
	k8sClient := fixture.useK8sObjects(reader)

	state, _, err := fixture.backend.SetupModelCachingForRequest(
		newTestContext(), fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy(),
		fixture.req, false, noOpRegularModelCacheMutation)
	require.NoError(t, err)
	assert.Equal(t, ModelCachingInProgress, state)
	for _, action := range k8sClient.Actions() {
		assert.NotEqual(t, "persistentvolumes", action.GetResource().Resource,
			"pending reader validation must wait for Kubernetes to bind the PV")
	}
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestRequestDeletionResumesRetiringCleanupAfterReferenceRelease(t *testing.T) {
	fixture := newRWXRegularCleanupTestFixture(t)
	fixture.rwPVC.APIVersion = "v1"
	fixture.rwPVC.Kind = "PersistentVolumeClaim"
	fixture.job.APIVersion = "batch/v1"
	fixture.job.Kind = "Job"
	setCleanupArtifacts(t, fixture.req, fixture.rwPVC, fixture.job)
	fixture.backend.bk8s.k8sArtifactHelper = fixture.backend
	k8sClient := fixture.useK8sObjects(fixture.rwPVC, fixture.job, fixture.pv)
	pvcDeleteAttempts := 0
	k8sClient.Fake.PrependReactor(
		"delete", "persistentvolumeclaims",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			pvcDeleteAttempts++
			if pvcDeleteAttempts == 1 {
				return true, nil, apierrors.NewServiceUnavailable(
					"transient PVC delete failure")
			}
			return false, nil, nil
		})

	err := fixture.backend.CleanupModelCachingResources(
		newTestContext(), fixture.req, fixture.rwPVC.DeepCopy(), fixture.job.Name)
	require.Error(t, err)
	assert.True(t, apierrors.IsServiceUnavailable(err))
	require.NoError(t, fixture.backend.bk8s.releaseModelCacheBindingReference(
		t.Context(), fixture.req.DeepCopy()))

	stored, getErr := fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseRetiring, stored.Status.Phase)
	assert.Empty(t, stored.Status.RequestReferences)

	require.NoError(t, fixture.backend.bk8s.resumeRetiringRegularModelCacheCleanup(
		newTestContext(), fixture.req.DeepCopy()))
	assert.Equal(t, 2, pvcDeleteAttempts)
	_, getErr = k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr))
}
