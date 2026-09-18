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

package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

// The chart renders nvcf-storage-capabilities into the operator's namespace;
// the agent reads it from its own. A 3.7.1 cluster with the chart fully synced
// still failed every cache-requesting deployment because nothing copied it.
func TestSetupStorageCapabilityCatalogConfigMap(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: NVCAOperatorNamespace},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{SystemNamespace: "custom-agent-system"},
			},
		},
	}
	agentNamespace := getSystemNamespace(nb)
	require.NotEqual(t, NVCAOperatorNamespace, agentNamespace)

	t.Run("mirrors the chart ConfigMap into the agent namespace and follows updates", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients}

		src := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nvcastorage.StorageCapabilityConfigMapName,
				Namespace: NVCAOperatorNamespace,
			},
			Data: map[string]string{nvcastorage.StorageCapabilityConfigMapKey: "apiVersion: storage.nvcf.nvidia.com/v1alpha1"},
		}
		_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, src, metav1.CreateOptions{})
		require.NoError(t, err)

		require.NoError(t, bc.setupStorageCapabilityCatalogConfigMap(ctx, nb))
		mirrored, err := clients.K8s.CoreV1().ConfigMaps(agentNamespace).Get(
			ctx, nvcastorage.StorageCapabilityConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, src.Data, mirrored.Data)

		src.Data[nvcastorage.StorageCapabilityConfigMapKey] += "\ndrivers: []"
		_, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Update(ctx, src, metav1.UpdateOptions{})
		require.NoError(t, err)
		require.NoError(t, bc.setupStorageCapabilityCatalogConfigMap(ctx, nb))
		mirrored, err = clients.K8s.CoreV1().ConfigMaps(agentNamespace).Get(
			ctx, nvcastorage.StorageCapabilityConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, src.Data, mirrored.Data, "an edit to the chart ConfigMap must reach the agent copy")
	})

	t.Run("absent source is skipped and an existing agent copy is kept", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients}

		require.NoError(t, bc.setupStorageCapabilityCatalogConfigMap(ctx, nb),
			"a chart that has not converged must not fail the NVCFBackend reconcile")
		_, err := clients.K8s.CoreV1().ConfigMaps(agentNamespace).Get(
			ctx, nvcastorage.StorageCapabilityConfigMapName, metav1.GetOptions{})
		assert.Error(t, err, "nothing to mirror, nothing created")

		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.StorageCapabilityConfigMapName, Namespace: agentNamespace},
			Data:       map[string]string{nvcastorage.StorageCapabilityConfigMapKey: "last good"},
		}
		_, err = clients.K8s.CoreV1().ConfigMaps(agentNamespace).Create(ctx, existing, metav1.CreateOptions{})
		require.NoError(t, err)
		require.NoError(t, bc.setupStorageCapabilityCatalogConfigMap(ctx, nb))
		kept, err := clients.K8s.CoreV1().ConfigMaps(agentNamespace).Get(
			ctx, nvcastorage.StorageCapabilityConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, existing.Data, kept.Data)
	})

	t.Run("an update to the chart ConfigMap forces an NVCA reconcile", func(t *testing.T) {
		assert.True(t, configMapUpdateForcesNVCAReconcile(nvcastorage.StorageCapabilityConfigMapName))
	})
}
