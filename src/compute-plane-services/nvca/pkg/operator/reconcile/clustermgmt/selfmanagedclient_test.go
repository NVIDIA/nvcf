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

package clustermgmt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

func TestNewSelfManagedClient(t *testing.T) {
	t.Run("creates client with both fetchers", func(t *testing.T) {
		configMapFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvcfbackend-self-managed"},
				Data: map[string]string{
					"cluster-dto.yaml": `
clusterName: test-cluster
clusterGroupName: test-group
region: us-west-2
nvcaVersion: "2.50.0"
miniService:
  helmReValServiceURL: "http://reval.nvcf.svc.cluster.local:8080"
`,
				},
			}, nil
		}

		registrationFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvca-cluster-registration"},
				Data: map[string]string{
					"clusterId":      "bootstrap-cluster-id",
					"clusterGroupId": "bootstrap-group-id",
				},
			}, nil
		}

		client := NewSelfManagedClient(nvidiaiov1.EnvTypeProd, configMapFetcher, registrationFetcher,
			DefaultVaultOAuthClientMountPathTemplate)
		require.NotNil(t, client)

		cluster, err := client.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		// Verify cluster source is set to self-hosted
		assert.Equal(t, types.ClusterSourceSelfHosted, cluster.NVCFBackend.Spec.ClusterSource)

		// Verify cluster IDs are populated from registration ConfigMap
		assert.Equal(t, "bootstrap-cluster-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "bootstrap-group-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)

		// Verify other config from main ConfigMap
		assert.Equal(t, "test-cluster", cluster.NVCFBackend.Spec.ClusterConfig.ClusterName)
		assert.Equal(t, "test-group", cluster.NVCFBackend.Spec.ClusterConfig.ClusterGroupName)
		assert.Equal(t, "us-west-2", cluster.NVCFBackend.Spec.ClusterConfig.Region)

		// Verify miniService config is mapped from DTO
		require.NotNil(t, cluster.NVCFBackend.Spec.ClusterConfig.MiniService)
		assert.Equal(t, "http://reval.nvcf.svc.cluster.local:8080", cluster.NVCFBackend.Spec.ClusterConfig.MiniService.HelmReValServiceURL)
	})

	t.Run("handles registration ConfigMap not found", func(t *testing.T) {
		configMapFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvcfbackend-self-managed"},
				Data: map[string]string{
					"cluster-dto.yaml": `
clusterName: test-cluster
clusterId: existing-id
clusterGroupId: existing-group-id
nvcaVersion: "2.50.0"
`,
				},
			}, nil
		}

		registrationFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return nil, apierrors.NewNotFound(
				schema.GroupResource{Group: "", Resource: "configmaps"},
				"nvca-cluster-registration",
			)
		}

		client := NewSelfManagedClient(nvidiaiov1.EnvTypeProd, configMapFetcher, registrationFetcher,
			DefaultVaultOAuthClientMountPathTemplate)
		require.NotNil(t, client)

		cluster, err := client.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		// Should still work, using IDs from main ConfigMap if present
		assert.Equal(t, types.ClusterSourceSelfHosted, cluster.NVCFBackend.Spec.ClusterSource)
		assert.Equal(t, "existing-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "existing-group-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)
	})

	t.Run("registration ConfigMap overrides main ConfigMap IDs", func(t *testing.T) {
		configMapFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvcfbackend-self-managed"},
				Data: map[string]string{
					"cluster-dto.yaml": `
clusterName: test-cluster
clusterId: old-cluster-id
clusterGroupId: old-group-id
nvcaVersion: "2.50.0"
`,
				},
			}, nil
		}

		registrationFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvca-cluster-registration"},
				Data: map[string]string{
					"clusterId":      "new-cluster-id",
					"clusterGroupId": "new-group-id",
				},
			}, nil
		}

		client := NewSelfManagedClient(nvidiaiov1.EnvTypeProd, configMapFetcher, registrationFetcher,
			DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := client.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		// Registration ConfigMap values should take precedence
		assert.Equal(t, "new-cluster-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "new-group-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)
	})

	t.Run("empty registration ConfigMap values do not override", func(t *testing.T) {
		configMapFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvcfbackend-self-managed"},
				Data: map[string]string{
					"cluster-dto.yaml": `
clusterName: test-cluster
clusterId: original-cluster-id
clusterGroupId: original-group-id
nvcaVersion: "2.50.0"
`,
				},
			}, nil
		}

		registrationFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvca-cluster-registration"},
				Data: map[string]string{
					"clusterId":      "", // Empty - should not override
					"clusterGroupId": "", // Empty - should not override
				},
			}, nil
		}

		client := NewSelfManagedClient(nvidiaiov1.EnvTypeProd, configMapFetcher, registrationFetcher,
			DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := client.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		// Original values should be preserved
		assert.Equal(t, "original-cluster-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "original-group-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)
	})

	t.Run("works with stage environment", func(t *testing.T) {
		configMapFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvcfbackend-self-managed"},
				Data: map[string]string{
					"cluster-dto.yaml": `
clusterName: stage-cluster
nvcaVersion: "2.50.0"
`,
				},
			}, nil
		}

		registrationFetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "nvca-cluster-registration"},
				Data: map[string]string{
					"clusterId":      "stage-cluster-id",
					"clusterGroupId": "stage-group-id",
				},
			}, nil
		}

		client := NewSelfManagedClient(nvidiaiov1.EnvTypeStage, configMapFetcher, registrationFetcher,
			DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := client.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		assert.Equal(t, types.ClusterSourceSelfHosted, cluster.NVCFBackend.Spec.ClusterSource)
		assert.Equal(t, "stage-cluster-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "stage-group-id", cluster.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)
	})
}
