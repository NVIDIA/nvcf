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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func dummyFetcher(yaml string, err error) func(context.Context) (*corev1.ConfigMap, error) {
	return func(ctx context.Context) (*corev1.ConfigMap, error) {
		if err != nil {
			return nil, err
		}
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cm"},
			Data:       map[string]string{"cluster-dto.yaml": yaml},
		}, nil
	}
}

func TestConfigMapClient_GetCluster_Success(t *testing.T) {
	sampleYAML := "clusterId: cid\nclusterName: name"

	called := false
	extra := func(ctx context.Context, _ nvidiaiov1.EnvType, _ *clusterDTO, dest *Cluster) error {
		called = true
		dest.NVCFBackend.Name = "patched"
		return nil
	}

	c := newConfigMapClient(nvidiaiov1.EnvTypeStage, dummyFetcher(sampleYAML, nil), DefaultVaultOAuthClientMountPathTemplate, extra)
	cluster, err := c.GetCluster(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, cluster)
	assert.True(t, called, "extra mapper should be called")
	assert.Equal(t, "patched", cluster.NVCFBackend.Name)
}

func TestConfigMapClient_GetCluster_FetchError(t *testing.T) {
	fetchErr := errors.New("fetch fail")
	c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher("", fetchErr), DefaultVaultOAuthClientMountPathTemplate)
	_, err := c.GetCluster(context.Background(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, fetchErr))
}

func TestConfigMapClient_GetCluster_MissingKey(t *testing.T) {
	f := func(ctx context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm"}, Data: map[string]string{}}, nil
	}
	c := newConfigMapClient(nvidiaiov1.EnvTypeProd, f, DefaultVaultOAuthClientMountPathTemplate)
	_, err := c.GetCluster(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster-dto.yaml not found")
}

func TestConfigMapClient_GetCluster_InvalidYAML(t *testing.T) {
	c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher("not: [yaml", nil), DefaultVaultOAuthClientMountPathTemplate)
	_, err := c.GetCluster(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to convert CR")
}

func TestAgentConfigFromClusterDTO(t *testing.T) {
	t.Run("maps agent service oauth endpoints", func(t *testing.T) {
		raw := `
agent:
  helmReValStageOAuthTokenURL: "https://chart-stage-reval-oauth.example.test/token"
  helmReValStageOAuthPublicKeysetEndpoint: "https://chart-stage-reval-oauth.example.test/.well-known/jwks.json"
  helmReValProdOAuthTokenURL: "https://chart-prod-reval-oauth.example.test/token"
  helmReValProdOAuthPublicKeysetEndpoint: "https://chart-prod-reval-oauth.example.test/.well-known/jwks.json"
  functionDeploymentStagesStageOAuthTokenURL: "https://chart-stage-fnds-oauth.example.test/token"
  functionDeploymentStagesStageOAuthPublicKeysetEndpoint: "https://chart-stage-fnds-oauth.example.test/.well-known/jwks.json"
  functionDeploymentStagesProdOAuthTokenURL: "https://chart-prod-fnds-oauth.example.test/token"
  functionDeploymentStagesProdOAuthPublicKeysetEndpoint: "https://chart-prod-fnds-oauth.example.test/.well-known/jwks.json"
`

		cfg, found, err := AgentConfigFromClusterDTO(context.Background(), raw)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "https://chart-stage-reval-oauth.example.test/token", cfg.HelmReValStageOAuthTokenURL)
		assert.Equal(t, "https://chart-stage-reval-oauth.example.test/.well-known/jwks.json", cfg.HelmReValStageOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://chart-prod-reval-oauth.example.test/token", cfg.HelmReValProdOAuthTokenURL)
		assert.Equal(t, "https://chart-prod-reval-oauth.example.test/.well-known/jwks.json", cfg.HelmReValProdOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://chart-stage-fnds-oauth.example.test/token", cfg.FunctionDeploymentStagesStageOAuthTokenURL)
		assert.Equal(t, "https://chart-stage-fnds-oauth.example.test/.well-known/jwks.json", cfg.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://chart-prod-fnds-oauth.example.test/token", cfg.FunctionDeploymentStagesProdOAuthTokenURL)
		assert.Equal(t, "https://chart-prod-fnds-oauth.example.test/.well-known/jwks.json", cfg.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint)
	})

	t.Run("empty service oauth endpoints are not defaults", func(t *testing.T) {
		cfg, found, err := AgentConfigFromClusterDTO(context.Background(), `agent:
  helmReValStageOAuthTokenURL: ""
`)
		require.NoError(t, err)
		assert.False(t, found)
		assert.Empty(t, cfg.HelmReValStageOAuthTokenURL)
	})

	t.Run("invalid yaml returns an error", func(t *testing.T) {
		_, found, err := AgentConfigFromClusterDTO(context.Background(), "agent: [")
		require.Error(t, err)
		assert.False(t, found)
	})
}

func TestConfigMapClient_GetCluster_OTelCollectorConfig(t *testing.T) {
	t.Run("maps OTel collector config when present in ConfigMap", func(t *testing.T) {
		yamlWithOTel := `
clusterId: test-cluster-id
clusterName: test-cluster
otelCollector:
  imageConfig:
    repository: nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib
    tag: "0.139.0"
`
		c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithOTel, nil), DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := c.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		assert.NotNil(t, cluster.NVCFBackend.Spec.OTelCollector)
		assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib", cluster.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository)
		assert.Equal(t, "0.139.0", cluster.NVCFBackend.Spec.OTelCollector.ImageConfig.Tag)
	})

	t.Run("sets nil when OTel collector not present in ConfigMap", func(t *testing.T) {
		yamlWithoutOTel := `
clusterId: test-cluster-id
clusterName: test-cluster
`
		c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithoutOTel, nil), DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := c.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		assert.Nil(t, cluster.NVCFBackend.Spec.OTelCollector)
	})

	t.Run("handles empty OTel collector imageConfig", func(t *testing.T) {
		yamlWithEmptyOTel := `
clusterId: test-cluster-id
clusterName: test-cluster
otelCollector:
  imageConfig:
    repository: ""
    tag: ""
`
		c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithEmptyOTel, nil), DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := c.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		assert.NotNil(t, cluster.NVCFBackend.Spec.OTelCollector)
		assert.Empty(t, cluster.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository)
		assert.Empty(t, cluster.NVCFBackend.Spec.OTelCollector.ImageConfig.Tag)
	})

	t.Run("maps OTel collector alongside other configs", func(t *testing.T) {
		yamlWithMultipleConfigs := `
clusterId: test-cluster-id
clusterName: test-cluster
nvcaVersion: "2.47.0"
imageCredentialHelper:
  imageConfig:
    repository: nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper
    tag: "1.0.0"
otelCollector:
  imageConfig:
    repository: nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib
    tag: "0.139.0"
`
		c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithMultipleConfigs, nil), DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := c.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		// Verify OTel collector config
		assert.NotNil(t, cluster.NVCFBackend.Spec.OTelCollector)
		assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib", cluster.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository)
		assert.Equal(t, "0.139.0", cluster.NVCFBackend.Spec.OTelCollector.ImageConfig.Tag)

		// Verify ImageCredHelper config is also mapped
		assert.NotNil(t, cluster.NVCFBackend.Spec.ImageCredHelper)
		assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper", cluster.NVCFBackend.Spec.ImageCredHelper.ImageConfig.Repository)
		assert.Equal(t, "1.0.0", cluster.NVCFBackend.Spec.ImageCredHelper.ImageConfig.Tag)

		// Verify other fields are mapped
		assert.Equal(t, "2.47.0", cluster.NVCFBackend.Spec.Version)
	})
}

func TestConfigMapClient_GetCluster_MiniServiceConfig(t *testing.T) {
	t.Run("maps miniService helmReValServiceURL when present", func(t *testing.T) {
		yamlWithMiniService := `
clusterId: test-cluster-id
clusterName: test-cluster
miniService:
  helmReValServiceURL: "http://reval.nvcf.svc.cluster.local:8080"
  helmReValServiceHostHeaderOverride: "reval.gateway.example.test"
`
		c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithMiniService, nil), DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := c.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		require.NotNil(t, cluster.NVCFBackend.Spec.ClusterConfig.MiniService)
		assert.Equal(t, "http://reval.nvcf.svc.cluster.local:8080", cluster.NVCFBackend.Spec.ClusterConfig.MiniService.HelmReValServiceURL)
		assert.Equal(t, "reval.gateway.example.test", cluster.NVCFBackend.Spec.ClusterConfig.MiniService.HelmReValServiceHostHeaderOverride)
	})

	t.Run("leaves MiniService nil when not present in DTO", func(t *testing.T) {
		yamlWithoutMiniService := `
clusterId: test-cluster-id
clusterName: test-cluster
`
		c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithoutMiniService, nil), DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := c.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		assert.Nil(t, cluster.NVCFBackend.Spec.ClusterConfig.MiniService)
	})

	t.Run("leaves MiniService nil when helmReValServiceURL is empty", func(t *testing.T) {
		yamlWithEmptyURL := `
clusterId: test-cluster-id
clusterName: test-cluster
miniService:
  helmReValServiceURL: ""
`
		c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithEmptyURL, nil), DefaultVaultOAuthClientMountPathTemplate)
		cluster, err := c.GetCluster(context.Background(), "")
		require.NoError(t, err)
		require.NotNil(t, cluster)

		assert.Nil(t, cluster.NVCFBackend.Spec.ClusterConfig.MiniService)
	})
}

func TestConfigMapClient_GetCluster_Tolerations(t *testing.T) {
	yamlWithTolerations := `
clusterId: test-cluster-id
clusterName: test-cluster
agent:
  tolerations:
    - key: agent-taint
      operator: Exists
      effect: NoSchedule
`

	c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithTolerations, nil), DefaultVaultOAuthClientMountPathTemplate)
	cluster, err := c.GetCluster(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, cluster)

	assert.Equal(t, []corev1.Toleration{{
		Key:      "agent-taint",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}}, cluster.NVCFBackend.Spec.AgentConfig.DeploymentConfig.Tolerations)
}

func TestConfigMapClient_GetCluster_AgentNATSURL(t *testing.T) {
	yamlWithAgentNATSURL := `
clusterId: test-cluster-id
clusterName: test-cluster
agent:
  natsURL: "nats://nats.localhost:14222"
  natsHostOverride: "nats.gateway.example.test"
`

	c := newConfigMapClient(nvidiaiov1.EnvTypeProd, dummyFetcher(yamlWithAgentNATSURL, nil), DefaultVaultOAuthClientMountPathTemplate)
	cluster, err := c.GetCluster(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, cluster)

	require.NotNil(t, cluster.NVCFBackend.Spec.AgentConfig.NATSURL)
	assert.Equal(t, "nats://nats.localhost:14222", *cluster.NVCFBackend.Spec.AgentConfig.NATSURL)
	require.NotNil(t, cluster.NVCFBackend.Spec.AgentConfig.NATSHostOverride)
	assert.Equal(t, "nats.gateway.example.test", *cluster.NVCFBackend.Spec.AgentConfig.NATSHostOverride)
}
