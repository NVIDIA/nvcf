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

func TestNewHelmManagedClient(t *testing.T) {
	dummyFetcher := func(ctx context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "test-namespace",
			},
			Data: map[string]string{
				"cluster-dto.yaml": "clusterId: dummy\nclusterName: dummy",
			},
		}, nil
	}

	c := NewHelmManagedClient(nvidiaiov1.EnvTypeProd, dummyFetcher, DefaultVaultOAuthClientMountPathTemplate)
	require.NotNil(t, c)
	assert.Equal(t, nvidiaiov1.EnvTypeProd, c.ConfigMapClient.envType)

	c = NewHelmManagedClient(nvidiaiov1.EnvTypeStage, dummyFetcher, DefaultVaultOAuthClientMountPathTemplate)
	require.NotNil(t, c)
	assert.Equal(t, nvidiaiov1.EnvTypeStage, c.ConfigMapClient.envType)
}

func TestHelmManagedClient_GetCluster(t *testing.T) {
	ctx := context.Background()

	validClusterYAML := `
clusterId: "test-cluster"
clusterName: "test-backend"
clusterDescription: "Test Cluster"
clusterGroupName: "test-group"
clusterGroupId: "grp-1"
ncaID: "test-nca"
nvcaVersion: "1.0.0"
oAuthClientId: "oauth-client-1"
icmsConfig:
  tokenURL: "https://oauth.example.test/token"
  publicKeysetEndpoint: "https://oauth.example.test/.well-known/jwks.json"
cloudProvider: "aws"
region: "us-west"
attributes: ["attr1", "attr2=value2"]
capabilities: ["LogPosting", "CachingSupport"]
agent:
  helmReValStageOAuthTokenURL: "https://stage-reval-oauth.example.test/token"
  helmReValStageOAuthPublicKeysetEndpoint: "https://stage-reval-oauth.example.test/.well-known/jwks.json"
  helmReValProdOAuthTokenURL: "https://prod-reval-oauth.example.test/token"
  helmReValProdOAuthPublicKeysetEndpoint: "https://prod-reval-oauth.example.test/.well-known/jwks.json"
  functionDeploymentStagesStageOAuthTokenURL: "https://stage-fnds-oauth.example.test/token"
  functionDeploymentStagesStageOAuthPublicKeysetEndpoint: "https://stage-fnds-oauth.example.test/.well-known/jwks.json"
  functionDeploymentStagesProdOAuthTokenURL: "https://prod-fnds-oauth.example.test/token"
  functionDeploymentStagesProdOAuthPublicKeysetEndpoint: "https://prod-fnds-oauth.example.test/.well-known/jwks.json"
  rolloverServiceStageOAuthTokenURL: "https://stage-ros-oauth.example.test/token"
  rolloverServiceStageOAuthPublicKeysetEndpoint: "https://stage-ros-oauth.example.test/.well-known/jwks.json"
  rolloverServiceProdOAuthTokenURL: "https://prod-ros-oauth.example.test/token"
  rolloverServiceProdOAuthPublicKeysetEndpoint: "https://prod-ros-oauth.example.test/.well-known/jwks.json"
`

	fetcher := func(ctx context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "test-namespace",
			},
			Data: map[string]string{
				"cluster-dto.yaml": validClusterYAML,
			},
		}, nil
	}

	client := NewHelmManagedClient(nvidiaiov1.EnvTypeProd, fetcher, DefaultVaultOAuthClientMountPathTemplate)
	cluster, err := client.GetCluster(ctx, "test-cluster")
	require.NoError(t, err)
	require.NotNil(t, cluster)

	nb := cluster.NVCFBackend
	assert.Equal(t, "test-backend", nb.Name)
	assert.Equal(t, "1.0.0", nb.Spec.Version)
	assert.Equal(t, "test-nca", nb.Spec.AccountConfig.NCAID)
	// attribute normalization should add =true for keys without value and sort later
	assert.ElementsMatch(t, []string{"attr1=true", "attr2=value2"}, nb.Spec.ClusterConfig.Attributes)

	// Defaults applied (prod)
	assert.Equal(t, "https://icms.nvcf.nvidia.com", nb.Spec.ICMSConfig.ICMSServiceURL)
	assert.Equal(t, "https://oauth.example.test/token", nb.Spec.ICMSConfig.TokenURL)
	assert.Equal(t, "https://stage-reval-oauth.example.test/token", nb.Spec.AgentConfig.HelmReValStageOAuthTokenURL)
	assert.Equal(t, "https://stage-reval-oauth.example.test/.well-known/jwks.json", nb.Spec.AgentConfig.HelmReValStageOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://prod-reval-oauth.example.test/token", nb.Spec.AgentConfig.HelmReValProdOAuthTokenURL)
	assert.Equal(t, "https://prod-reval-oauth.example.test/.well-known/jwks.json", nb.Spec.AgentConfig.HelmReValProdOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://stage-fnds-oauth.example.test/token", nb.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthTokenURL)
	assert.Equal(t, "https://stage-fnds-oauth.example.test/.well-known/jwks.json", nb.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://prod-fnds-oauth.example.test/token", nb.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthTokenURL)
	assert.Equal(t, "https://prod-fnds-oauth.example.test/.well-known/jwks.json", nb.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://stage-ros-oauth.example.test/token", nb.Spec.AgentConfig.RolloverServiceStageOAuthTokenURL)
	assert.Equal(t, "https://stage-ros-oauth.example.test/.well-known/jwks.json", nb.Spec.AgentConfig.RolloverServiceStageOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://prod-ros-oauth.example.test/token", nb.Spec.AgentConfig.RolloverServiceProdOAuthTokenURL)
	assert.Equal(t, "https://prod-ros-oauth.example.test/.well-known/jwks.json", nb.Spec.AgentConfig.RolloverServiceProdOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://:443", nb.Spec.VaultConfig.Address)
	assert.True(t, nb.Spec.VaultConfig.Enabled)
}

func TestHelmManagedClient_GetCluster_InvalidYAML(t *testing.T) {
	ctx := context.Background()

	fetcher := func(ctx context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{},
			Data: map[string]string{
				"cluster-dto.yaml": `invalid: yaml: {`,
			},
		}, nil
	}

	client := NewHelmManagedClient(nvidiaiov1.EnvTypeProd, fetcher, DefaultVaultOAuthClientMountPathTemplate)
	_, err := client.GetCluster(ctx, "test-cluster")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to convert CR")
}

func TestHelmManagedClient_GetCluster_FetchError(t *testing.T) {
	ctx := context.Background()

	fetcher := func(ctx context.Context) (*corev1.ConfigMap, error) {
		return nil, errors.New("fetch error")
	}

	client := NewHelmManagedClient(nvidiaiov1.EnvTypeProd, fetcher, DefaultVaultOAuthClientMountPathTemplate)
	_, err := client.GetCluster(ctx, "test-cluster")
	require.Error(t, err)
	assert.Equal(t, "fetch error", errors.Unwrap(err).Error())
}

// Ensure we return an error when the expected key is missing in the ConfigMap
func TestHelmManagedClient_GetCluster_MissingClusterDTO(t *testing.T) {
	ctx := context.Background()

	fetcher := func(ctx context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "test-namespace",
			},
			Data: map[string]string{}, // intentionally empty
		}, nil
	}

	client := NewHelmManagedClient(nvidiaiov1.EnvTypeProd, fetcher, DefaultVaultOAuthClientMountPathTemplate)
	_, err := client.GetCluster(ctx, "test-cluster")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster-dto.yaml not found")
}

// Ensure we return an error when the key is present but the content is empty / whitespace
func TestHelmManagedClient_GetCluster_EmptyClusterDTO(t *testing.T) {
	ctx := context.Background()

	fetcher := func(ctx context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "test-namespace",
			},
			Data: map[string]string{
				"cluster-dto.yaml": "   \n\t  ", // whitespace only
			},
		}, nil
	}

	client := NewHelmManagedClient(nvidiaiov1.EnvTypeProd, fetcher, DefaultVaultOAuthClientMountPathTemplate)
	_, err := client.GetCluster(ctx, "test-cluster")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster-dto.yaml not found")
}
