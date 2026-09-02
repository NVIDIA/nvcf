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
)

func TestSetupGPUProfilingConfigMap(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: "test-namespace"},
	}
	agentNS := getSystemNamespace(nb)

	t.Run("source absent is a no-op and does not fail reconcile", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients}

		err := bc.setupGPUProfilingConfigMap(ctx, nb)
		require.NoError(t, err)

		_, err = clients.K8s.CoreV1().ConfigMaps(agentNS).Get(ctx, nvcfGPUProfilingConfigMapName, metav1.GetOptions{})
		assert.True(t, k8serr.IsNotFound(err), "no ConfigMap should be mirrored when the source is absent")
	})

	t.Run("source present is mirrored into the agent system namespace", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients}

		src := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nvcfGPUProfilingConfigMapName,
				Namespace: NVCAOperatorNamespace,
			},
			Data: map[string]string{"functionIds": "fn-1,fn-2"},
		}
		_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, src, metav1.CreateOptions{})
		require.NoError(t, err)

		err = bc.setupGPUProfilingConfigMap(ctx, nb)
		require.NoError(t, err)

		mirrored, err := clients.K8s.CoreV1().ConfigMaps(agentNS).Get(ctx, nvcfGPUProfilingConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "fn-1,fn-2", mirrored.Data["functionIds"])
	})
}

func TestNamedControlPlaneConfigMapMirrorsUseOperatorNamespace(t *testing.T) {
	const (
		operatorNS = "plane-a-nvca-operator"
		systemNS   = "plane-a-nvca-system"
	)
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "plane-a", Namespace: operatorNS},
	}
	nb.Spec.ClusterConfig.ControlPlaneID = "plane-a"
	require.Equal(t, systemNS, getSystemNamespace(nb))

	t.Run("required annotations ConfigMap", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients, operatorNamespace: operatorNS}
		src := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: nvcfCustomAnnotationsConfigMapName, Namespace: operatorNS},
			Data:       map[string]string{"annotations": `{"owner":"plane-a"}`},
		}
		_, err := clients.K8s.CoreV1().ConfigMaps(operatorNS).Create(ctx, src, metav1.CreateOptions{})
		require.NoError(t, err)

		require.NoError(t, bc.mirrorConfigMap(ctx, nb, nvcfCustomAnnotationsConfigMapName))
		mirrored, err := clients.K8s.CoreV1().ConfigMaps(systemNS).Get(
			ctx, nvcfCustomAnnotationsConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, src.Data, mirrored.Data)
	})

	t.Run("optional GPU profiling ConfigMap", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients, operatorNamespace: operatorNS}
		src := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: nvcfGPUProfilingConfigMapName, Namespace: operatorNS},
			Data:       map[string]string{"functionIds": "fn-plane-a"},
		}
		_, err := clients.K8s.CoreV1().ConfigMaps(operatorNS).Create(ctx, src, metav1.CreateOptions{})
		require.NoError(t, err)

		require.NoError(t, bc.setupGPUProfilingConfigMap(ctx, nb))
		mirrored, err := clients.K8s.CoreV1().ConfigMaps(systemNS).Get(
			ctx, nvcfGPUProfilingConfigMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, src.Data, mirrored.Data)
	})

	t.Run("does not consume same-named ConfigMaps from foreign or legacy namespaces", func(t *testing.T) {
		ctx := newTestContext()
		clients := mockKubeClientsForIntegrationTests()
		bc := &BackendK8sCache{clients: clients, operatorNamespace: operatorNS}
		for _, namespace := range []string{"plane-b-nvca-operator", NVCAOperatorNamespace} {
			for _, name := range []string{nvcfCustomAnnotationsConfigMapName, nvcfGPUProfilingConfigMapName} {
				_, err := clients.K8s.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Data:       map[string]string{"source": namespace},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			}
		}

		err := bc.mirrorConfigMap(ctx, nb, nvcfCustomAnnotationsConfigMapName)
		assert.True(t, k8serr.IsNotFound(err), "required mirror must not fall back to another plane")
		_, err = clients.K8s.CoreV1().ConfigMaps(systemNS).Get(
			ctx, nvcfCustomAnnotationsConfigMapName, metav1.GetOptions{})
		assert.True(t, k8serr.IsNotFound(err), "foreign annotations must not be mirrored")

		require.NoError(t, bc.setupGPUProfilingConfigMap(ctx, nb))
		_, err = clients.K8s.CoreV1().ConfigMaps(systemNS).Get(
			ctx, nvcfGPUProfilingConfigMapName, metav1.GetOptions{})
		assert.True(t, k8serr.IsNotFound(err), "foreign profiling config must not be mirrored")
	})
}
