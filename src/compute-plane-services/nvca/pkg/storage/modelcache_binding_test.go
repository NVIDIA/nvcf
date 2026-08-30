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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func durableBindingSelection(t *testing.T) *PersistedModelCacheStorageSelection {
	t.Helper()
	selection, err := NewPersistedModelCacheStorageSelection(
		ModelCacheWorkflowHelm,
		ModelCacheSelectionDurable,
		testResolvedModelCacheStorage(ModelCacheTransitionNVMesh),
	)
	require.NoError(t, err)
	return selection
}

func durableRWXReadOnlyBindingSelection(t *testing.T) *PersistedModelCacheStorageSelection {
	t.Helper()
	selection, err := NewPersistedModelCacheStorageSelection(
		ModelCacheWorkflowRegular,
		ModelCacheSelectionDurable,
		testResolvedModelCacheStorage(ModelCacheTransitionRWXReadOnly),
	)
	require.NoError(t, err)
	return selection
}

func TestNewModelCacheBinding(t *testing.T) {
	selection := durableBindingSelection(t)
	binding, err := NewModelCacheBinding(selection, "nca-a", "cache-handle", ModelCacheInitNamespace)
	require.NoError(t, err)

	assert.Equal(t, ModelCacheBindingName("cache-handle"), binding.Name)
	assert.Equal(t, ModelCacheInitNamespace, binding.Namespace)
	assert.Equal(t, []string{nvcav2beta1.ModelCacheBindingFinalizer}, binding.Finalizers)
	assert.Equal(t, nvcav2beta1.ModelCacheWorkflowHelm, binding.Spec.Identity.Workflow)
	assert.Equal(t, digestBindingValue("nca-a"), binding.Spec.Identity.SharingDomainDigest)
	assert.Equal(t, digestBindingValue("cache-handle"), binding.Spec.Identity.CacheHandleDigest)
	assert.Equal(t, NVMeshStorageClassProvisioner, binding.Spec.Decision.Provisioner)
	assert.Equal(t,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadOnlyMany},
		binding.Spec.Decision.RequiredAccessModes)
	assert.Equal(t, corev1.PersistentVolumeReclaimRetain, binding.Spec.StorageClass.ReclaimPolicy)
	assert.Equal(t, []string{"rw-pvc-cache-handle"}, binding.Spec.Resources.PersistentVolumeClaimNames)
	assert.Equal(t, []string{"writer-job-cache-handle"}, binding.Spec.Resources.JobNames)
	assert.Equal(t, "modelcache-init-cache-handle", binding.Spec.Resources.LeaseName)
	assert.Empty(t, binding.Spec.Resources.StorageClassNames)
	assert.Empty(t, binding.Spec.Resources.SecretNames)
}

func TestNewRWXReadOnlyModelCacheBindingRecordsOneClaim(t *testing.T) {
	selection := durableRWXReadOnlyBindingSelection(t)
	binding, err := NewModelCacheBinding(selection, "nca-a", "cache-handle", "pod-instances")
	require.NoError(t, err)

	assert.Equal(t, ModelCacheTransitionRWXReadOnly, binding.Spec.Decision.Transition)
	assert.Equal(t,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		binding.Spec.Decision.RequiredAccessModes)
	assert.False(t, binding.Spec.Decision.EncryptionRequired)
	assert.Equal(t,
		[]string{"rw-pvc-cache-handle"},
		binding.Spec.Resources.PersistentVolumeClaimNames)
	assert.Equal(t, []string{"writer-job-cache-handle"}, binding.Spec.Resources.JobNames)
	assert.Empty(t, binding.Spec.Resources.LeaseName)
	assert.Empty(t, binding.Spec.Resources.StorageClassNames)
	assert.Empty(t, binding.Spec.Resources.SecretNames)
}

func TestNewEncryptedModelCacheBindingRecordsNamesOnly(t *testing.T) {
	selection := durableBindingSelection(t)
	selection.EncryptionRequired = true
	binding, err := NewModelCacheBinding(selection, "nca-a", "cache-handle", ModelCacheInitNamespace)
	require.NoError(t, err)

	assert.True(t, binding.Spec.Decision.EncryptionRequired)
	assert.Equal(t, []string{buildStorageClassName("nca-a")}, binding.Spec.Resources.StorageClassNames)
	assert.Equal(t, []string{buildStorageClassSecretName("nca-a")}, binding.Spec.Resources.SecretNames)
}

func TestNewEncryptedRegularModelCacheBindingRecordsLegacyNames(t *testing.T) {
	selection := durableBindingSelection(t)
	selection.Workflow = ModelCacheWorkflowRegular
	selection.EncryptionRequired = true
	binding, err := NewModelCacheBinding(selection, "nca-a", "cache-handle", "pod-instances")
	require.NoError(t, err)

	domainHash := hashNCAID("nca-a")
	assert.Equal(t,
		[]string{"rw-pvc-cache-handle", "ro-pvc-cache-handle"},
		binding.Spec.Resources.PersistentVolumeClaimNames)
	assert.Empty(t, binding.Spec.Resources.LeaseName)
	assert.Equal(t, []string{domainHash + "-sc"}, binding.Spec.Resources.StorageClassNames)
	assert.Equal(t, []string{domainHash}, binding.Spec.Resources.SecretNames)
}

func TestModelCacheBindingHandleCollisionFailsIntentValidation(t *testing.T) {
	selection := durableBindingSelection(t)
	binding, err := NewModelCacheBinding(selection, "nca-a", "same-handle", ModelCacheInitNamespace)
	require.NoError(t, err)
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive

	err = ValidateModelCacheBinding(binding, selection, "nca-b", "same-handle", ModelCacheInitNamespace)
	require.ErrorContains(t, err, "immutable spec does not match")
	assert.Equal(t, ModelCacheBindingName("same-handle"), binding.Name,
		"the handle-scoped name forces another sharing domain to collide and fail closed")
}

func TestValidateModelCacheBinding(t *testing.T) {
	selection := durableBindingSelection(t)
	binding, err := NewModelCacheBinding(selection, "nca-a", "cache-handle", ModelCacheInitNamespace)
	require.NoError(t, err)
	binding.UID = types.UID("binding-uid")
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive

	require.NoError(t, ValidateModelCacheBinding(
		binding, selection, "nca-a", "cache-handle", ModelCacheInitNamespace))

	t.Run("request reference", func(t *testing.T) {
		selected := *selection
		selected.BindingName = binding.Name
		selected.BindingUID = binding.UID
		require.NoError(t, ValidateModelCacheBinding(
			binding, &selected, "nca-a", "cache-handle", ModelCacheInitNamespace))

		selected.BindingUID = types.UID("other")
		err := ValidateModelCacheBinding(
			binding, &selected, "nca-a", "cache-handle", ModelCacheInitNamespace)
		require.ErrorContains(t, err, "binding reference changed")
	})

	t.Run("retiring", func(t *testing.T) {
		changed := binding.DeepCopy()
		changed.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
		err := ValidateModelCacheBinding(
			changed, selection, "nca-a", "cache-handle", ModelCacheInitNamespace)
		require.ErrorContains(t, err, "is not Active")
	})

	t.Run("missing finalizer", func(t *testing.T) {
		changed := binding.DeepCopy()
		changed.Finalizers = nil
		err := ValidateModelCacheBinding(
			changed, selection, "nca-a", "cache-handle", ModelCacheInitNamespace)
		require.ErrorContains(t, err, "has no protection finalizer")
	})

	t.Run("spec drift", func(t *testing.T) {
		changed := binding.DeepCopy()
		changed.Spec.Decision.Provider = "other"
		err := ValidateModelCacheBinding(
			changed, selection, "nca-a", "cache-handle", ModelCacheInitNamespace)
		require.ErrorContains(t, err, "immutable spec does not match")
	})
}

func TestModelCacheBindingHasRequestReference(t *testing.T) {
	binding := &nvcav2beta1.ModelCacheBinding{
		Status: nvcav2beta1.ModelCacheBindingStatus{
			RequestReferences: []nvcav2beta1.ModelCacheBindingRequestReference{
				{Namespace: "requests", Name: "request", UID: types.UID("uid")},
			},
		},
	}
	assert.True(t, ModelCacheBindingHasRequestReference(
		binding, "requests", "request", types.UID("uid")))
	assert.False(t, ModelCacheBindingHasRequestReference(
		binding, "requests", "request", types.UID("other")))
}

func TestNewModelCacheBindingRejectsInvalidInput(t *testing.T) {
	selection := durableBindingSelection(t)
	for _, tt := range []struct {
		name      string
		domain    string
		handle    string
		namespace string
		want      string
	}{
		{name: "domain", handle: "cache", namespace: "writers", want: "sharing domain is empty"},
		{name: "handle", domain: "nca", namespace: "writers", want: "cache handle is empty"},
		{name: "namespace", domain: "nca", handle: "cache", namespace: "INVALID", want: "writer namespace"},
		{name: "resource name", domain: "nca", handle: "INVALID", namespace: "writers", want: "name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewModelCacheBinding(selection, tt.domain, tt.handle, tt.namespace)
			require.ErrorContains(t, err, tt.want)
		})
	}
}
