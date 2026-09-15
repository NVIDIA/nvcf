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
