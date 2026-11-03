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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

func TestStorageControllerTypes(t *testing.T) {
	// Shared-storage and internal-persistent-storage controllers always
	// register; the model-cache controller is gated on caching being enabled.
	base := storageControllerTypes(false)
	assert.Equal(t, []nvcav1new.StorageRequestType{
		nvcav1new.SharedStorageRequest,
		nvcav1new.InternalPersistentStorageRequest,
	}, base, "caching off must not register the model-cache controller")
	assert.NotContains(t, base, nvcav1new.ModelCacheRequest)

	withCache := storageControllerTypes(true)
	assert.Contains(t, withCache, nvcav1new.ModelCacheRequest,
		"caching on must register the model-cache controller")
	assert.Equal(t, []nvcav1new.StorageRequestType{
		nvcav1new.SharedStorageRequest,
		nvcav1new.InternalPersistentStorageRequest,
		nvcav1new.ModelCacheRequest,
	}, withCache)
}

func TestAgentManagerCacheOptionsStripManagedFields(t *testing.T) {
	opts := agentManagerCacheOptions()
	require.NotNil(t, opts.DefaultTransform)

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "pod-1",
		ManagedFields: []metav1.ManagedFieldsEntry{
			{Manager: "creator", Operation: metav1.ManagedFieldsOperationUpdate},
		},
	}}
	out, err := opts.DefaultTransform(pod)
	require.NoError(t, err)
	assert.Nil(t, out.(*corev1.Pod).ManagedFields)

	// A ByObject entry with a transform of its own would not inherit DefaultTransform.
	for obj, byObject := range opts.ByObject {
		assert.Nil(t, byObject.Transform, "%T sets its own transform", obj)
	}
}
