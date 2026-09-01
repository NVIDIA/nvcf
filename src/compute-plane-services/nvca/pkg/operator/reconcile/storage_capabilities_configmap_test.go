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
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func TestMirrorStorageCapabilitiesConfigMap(t *testing.T) {
	const operatorNamespace = "custom-operator-system"
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: operatorNamespace},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{SystemNamespace: "custom-agent-system"},
			},
		},
	}

	t.Run("mirrors from the configured operator namespace and updates the agent copy", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients, operatorNamespace: operatorNamespace}

		src := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storage.StorageCapabilityConfigMapName,
				Namespace: operatorNamespace,
			},
			Data: map[string]string{storage.StorageCapabilityConfigMapKey: "version: v1alpha1"},
		}
		_, err := clients.K8s.CoreV1().ConfigMaps(operatorNamespace).Create(ctx, src, metav1.CreateOptions{})
		require.NoError(t, err)

		err = bc.mirrorConfigMap(ctx, nb, storage.StorageCapabilityConfigMapName)
		require.NoError(t, err)

		agentNamespace := getSystemNamespace(nb)
		mirrored, err := clients.K8s.CoreV1().ConfigMaps(agentNamespace).Get(
			ctx, storage.StorageCapabilityConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, src.Data, mirrored.Data)

		src.Data[storage.StorageCapabilityConfigMapKey] = "version: v1alpha1\nproviders: []"
		_, err = clients.K8s.CoreV1().ConfigMaps(operatorNamespace).Update(ctx, src, metav1.UpdateOptions{})
		require.NoError(t, err)

		err = bc.mirrorConfigMap(ctx, nb, storage.StorageCapabilityConfigMapName)
		require.NoError(t, err)
		mirrored, err = clients.K8s.CoreV1().ConfigMaps(agentNamespace).Get(
			ctx, storage.StorageCapabilityConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, src.Data, mirrored.Data)

		lastGood := mirrored.Data
		require.NoError(t, clients.K8s.CoreV1().ConfigMaps(operatorNamespace).Delete(
			ctx, storage.StorageCapabilityConfigMapName, metav1.DeleteOptions{}))
		err = bc.mirrorConfigMap(ctx, nb, storage.StorageCapabilityConfigMapName)
		assert.True(t, k8serr.IsNotFound(err))
		mirrored, err = clients.K8s.CoreV1().ConfigMaps(agentNamespace).Get(
			ctx, storage.StorageCapabilityConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, lastGood, mirrored.Data)
	})

	t.Run("missing source fails closed", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients, operatorNamespace: operatorNamespace}

		err := bc.mirrorConfigMap(ctx, nb, storage.StorageCapabilityConfigMapName)
		assert.True(t, k8serr.IsNotFound(err))
	})
}
