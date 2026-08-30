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

package v2beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

func TestModelCacheBindingSchemeRegistration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))

	gvks, unversioned, err := scheme.ObjectKinds(&ModelCacheBinding{})
	require.NoError(t, err)
	assert.False(t, unversioned)
	assert.Contains(t, gvks, SchemeGroupVersion.WithKind("ModelCacheBinding"))

	gvks, unversioned, err = scheme.ObjectKinds(&ModelCacheBindingList{})
	require.NoError(t, err)
	assert.False(t, unversioned)
	assert.Contains(t, gvks, SchemeGroupVersion.WithKind("ModelCacheBindingList"))
}

func TestModelCacheBindingDeepCopy(t *testing.T) {
	now := metav1.Now()
	original := &ModelCacheBinding{
		Spec: ModelCacheBindingSpec{
			Decision: ModelCacheBindingDecision{
				RequiredAccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			},
			Resources: ModelCacheBindingResourceIntent{
				PersistentVolumeClaimNames: []string{"writer-pvc"},
				PersistentVolumeNames:      []string{"reader-pv"},
				JobNames:                   []string{"writer-job"},
				StorageClassNames:          []string{"encrypted-sc"},
				SecretNames:                []string{"encrypted-secret"},
			},
		},
		Status: ModelCacheBindingStatus{
			LastPhaseTransitionTime: &now,
			RequestReferences: []ModelCacheBindingRequestReference{
				{Namespace: "request-ns", Name: "request", UID: "request-uid"},
			},
			Realized: &ModelCacheBindingRealizedState{ProviderDataIdentity: "provider-id"},
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Populated", Message: "ready"},
			},
		},
	}

	copy := original.DeepCopy()
	copy.Spec.Decision.RequiredAccessModes[0] = corev1.ReadOnlyMany
	copy.Spec.Resources.PersistentVolumeClaimNames[0] = "changed-pvc"
	copy.Spec.Resources.PersistentVolumeNames[0] = "changed-pv"
	copy.Spec.Resources.JobNames[0] = "changed-job"
	copy.Spec.Resources.StorageClassNames[0] = "changed-sc"
	copy.Spec.Resources.SecretNames[0] = "changed-secret"
	copy.Status.RequestReferences[0].Name = "changed-request"
	copy.Status.Realized.ProviderDataIdentity = "changed-provider-id"
	copy.Status.Conditions[0].Message = "changed"

	assert.Equal(t, corev1.ReadWriteOnce, original.Spec.Decision.RequiredAccessModes[0])
	assert.Equal(t, "writer-pvc", original.Spec.Resources.PersistentVolumeClaimNames[0])
	assert.Equal(t, "reader-pv", original.Spec.Resources.PersistentVolumeNames[0])
	assert.Equal(t, "writer-job", original.Spec.Resources.JobNames[0])
	assert.Equal(t, "encrypted-sc", original.Spec.Resources.StorageClassNames[0])
	assert.Equal(t, "encrypted-secret", original.Spec.Resources.SecretNames[0])
	assert.Equal(t, "request", original.Status.RequestReferences[0].Name)
	assert.Equal(t, "provider-id", original.Status.Realized.ProviderDataIdentity)
	assert.Equal(t, "ready", original.Status.Conditions[0].Message)
}

func TestModelCacheBindingOpenAPIDefinitions(t *testing.T) {
	definitions := GetOpenAPIDefinitions(func(string) spec.Ref { return spec.Ref{} })
	for _, typeName := range []string{
		"ModelCacheBinding",
		"ModelCacheBindingDecision",
		"ModelCacheBindingIdentity",
		"ModelCacheBindingList",
		"ModelCacheBindingRealizedState",
		"ModelCacheBindingRequestReference",
		"ModelCacheBindingResourceIntent",
		"ModelCacheBindingSpec",
		"ModelCacheBindingStatus",
	} {
		assert.Contains(t, definitions,
			"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1."+typeName)
	}
}
