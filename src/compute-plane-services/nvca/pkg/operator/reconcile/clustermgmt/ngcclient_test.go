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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"

	"encoding/base64"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

type mockTokenFetcher struct {
	token string
	err   error
}

func (m *mockTokenFetcher) FetchToken(ctx context.Context) (string, error) {
	return m.token, m.err
}

// Helper to create OTelCollector config for clusterDTO in tests
func newOTelCollectorConfig(enabled bool, repo, tag string) *struct {
	Enabled     bool `json:"enabled"`
	ImageConfig struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
	} `json:"imageConfig,omitempty"`
} {
	return &struct {
		Enabled     bool `json:"enabled"`
		ImageConfig struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"imageConfig,omitempty"`
	}{
		Enabled: enabled,
		ImageConfig: struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		}{
			Repository: repo,
			Tag:        tag,
		},
	}
}

func TestNewNGCManagedClient(t *testing.T) {
	ctx := context.Background()
	tokFetcher := &mockTokenFetcher{
		token: "abcd-1234",
	}
	client, err := NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeProd)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, DefaultRootNGCAPIURL, client.rootNGCAPIURL)

	client, err = NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeProd, WithRootNGCAPIURL(""))
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, DefaultRootNGCAPIURL, client.rootNGCAPIURL)

	client, err = NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeProd, WithRootNGCAPIURL("http://localhost:1234"))
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "http://localhost:1234", client.rootNGCAPIURL)
}

func TestGetCluster(t *testing.T) {
	ctx := context.Background()
	sisClusterResp := &atomic.Value{}
	sisClusterErr := &atomic.Value{}
	sisServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.Header.Get("Authorization"), "Bearer abcd-1234")
		assert.Contains(t, r.URL.Path, "/v2/sis/clusters")

		err := sisClusterErr.Load()
		if err != nil {
			err1 := err.(error)
			http.Error(w, err1.Error(), http.StatusInternalServerError)
			return
		}

		sisCluster := sisClusterResp.Load().(clusterDTO)
		err1 := json.NewEncoder(w).Encode(sisCluster)
		if err1 != nil {
			http.Error(w, err1.Error(), http.StatusInternalServerError)
			return
		}
	}))
	t.Cleanup(func() { sisServer.Close() })

	tokFetcher := &mockTokenFetcher{
		token: "abcd-1234",
	}
	client, err := NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeStage, WithRootNGCAPIURL(sisServer.URL))
	require.NoError(t, err)
	require.NotNil(t, client)

	// Setup initial happy path with dynamic GPU discovery
	expectedClusterDTO := clusterDTO{
		ID:        uuid.NewString(),
		Name:      "my-cluster",
		GroupName: "my-cluster-group",
		GroupID:   uuid.NewString(),
		Status:    types.ClusterStatusReady,

		NCAID:         uuid.NewString(),
		NVCAVersion:   "1.0.0",
		Capabilities:  []string{string(DynamicGPUDiscovery), "LogPosting", "CachingSupport", "RandomCapability"},
		OAuthClientID: uuid.NewString(),
		Attributes:    []string{"some_attribute", "some_attribute_with_value=some_value", "foo=bar"},
	}
	expectedClusterDTO.VaultConfig.Address = "http://localhost:8000"

	// test duplicate attributes
	expectedClusterDTO.Attributes = []string{"some_attribute=true", "some_attribute=false", "foo=bar"}
	sisClusterResp.Store(expectedClusterDTO)
	actualCluster, err := client.GetCluster(ctx, expectedClusterDTO.ID)
	require.EqualError(t, err, `duplicate attributes found: ["some_attribute"] in ["some_attribute=true" "some_attribute=false" "foo=bar"]`)
	assert.Nil(t, actualCluster)

	// good attributes
	expectedClusterDTO.Attributes = []string{"some_attribute", "some_attribute_with_value=some_value", "foo=bar"}
	sisClusterResp.Store(expectedClusterDTO)
	actualCluster, err = client.GetCluster(ctx, expectedClusterDTO.ID)
	require.NoError(t, err)
	assert.NotNil(t, actualCluster)
	assert.Equal(t, len(actualCluster.NVCFBackend.Spec.FeatureGate.Values), 3)
	assert.ElementsMatch(t, actualCluster.NVCFBackend.Spec.FeatureGate.Values,
		[]string{"LogPosting", "CachingSupport", "RandomCapability"})
	assert.Equal(t, expectedClusterDTO.VaultConfig.Address, actualCluster.NVCFBackend.Spec.VaultConfig.Address)
	assert.Equal(t, fmt.Sprintf("k8s_%s_bart_jwt_role", expectedClusterDTO.Name), actualCluster.NVCFBackend.Spec.VaultConfig.OAuthConfigRole)
	assert.Equal(t, fmt.Sprintf("auth/jwt/k8s/%s", expectedClusterDTO.Name), actualCluster.NVCFBackend.Spec.VaultConfig.AuthMountPath)
	assert.Equal(t, fmt.Sprintf("nvidia/services/oauth/clients/%s/kv/secret", expectedClusterDTO.getClientID()), actualCluster.NVCFBackend.Spec.VaultConfig.OAuthClientMountPath)
	assert.Nil(t, actualCluster.NVCFBackend.Spec.ClusterConfig.GPUDiscovery.Static)
	assert.Equal(t, []string{"foo=bar", "some_attribute=true", "some_attribute_with_value=some_value"}, actualCluster.NVCFBackend.Spec.ClusterConfig.Attributes)

	// Perform again with static GPU information
	expectedClusterDTO.Capabilities = nil
	expectedClusterDTO.GPUs = []registrationGPU{
		{
			Name:     "A100",
			Capacity: 8,
			InstanceTypes: []registrationInstanceType{
				{
					Name:         "BM.GPU.A100-v2.8_8x",
					Value:        "BM.GPU.A100-v2.8",
					Description:  "Eight A100 GPU",
					Default:      true,
					CPUCores:     4,
					SystemMemory: "16G",
					GPUMemory:    "14G",
					GPUCount:     8,
				},
			},
		},
	}
	sisClusterResp.Store(expectedClusterDTO)
	actualCluster, err = client.GetCluster(ctx, expectedClusterDTO.ID)
	require.NoError(t, err)
	assert.NotNil(t, actualCluster)
	assert.NotNil(t, actualCluster.NVCFBackend.Spec.ClusterConfig.GPUDiscovery.Static)
	assert.NotEmpty(t, actualCluster.NVCFBackend.Spec.ClusterConfig.GPUDiscovery.Static.GPUConfig)

	// Error with a failed token fetcher
	tokFetcher.err = errors.New("token-error")
	actualCluster, err = client.GetCluster(ctx, expectedClusterDTO.ID)
	require.Error(t, err)
	assert.Nil(t, actualCluster)
	assert.Equal(t, "token-error", err.Error())

	tokFetcher.err = nil
	sisClusterErr.Store(errors.New("icms-error"))
	actualCluster, err = client.GetCluster(ctx, expectedClusterDTO.ID)
	require.Error(t, err)
	assert.Nil(t, actualCluster)
	assert.Contains(t, err.Error(), "giving up")
}

func TestGetCluster_StageDefaultsAndGPUB64(t *testing.T) {
	ctx := context.Background()

	sisClusterResp := &atomic.Value{}
	sisServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sisClusterResp.Load().(clusterDTO))
	}))
	t.Cleanup(func() { sisServer.Close() })

	tokFetcher := &mockTokenFetcher{token: "tok"}
	client, err := NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeStage, WithRootNGCAPIURL(sisServer.URL))
	require.NoError(t, err)

	// cluster with GPUsB64 and CSI disabled
	gpuCfg := []registrationGPU{{Name: "L4", Capacity: 4}}
	gpuJSON, _ := json.Marshal(gpuCfg)
	gpuB64 := base64.StdEncoding.EncodeToString(gpuJSON)

	dto := clusterDTO{
		ID:            uuid.NewString(),
		Name:          "stage-cluster",
		NVCAVersion:   "2.0.0",
		Region:        "us-east-1",
		Capabilities:  []string{},
		GPUsB64:       gpuB64,
		OAuthClientID: uuid.NewString(),
	}
	dto.ICMSConfig.TokenURL = "https://stage-oauth.example.test/token"
	dto.ICMSConfig.ICMSServiceHostHeaderOverride = "sis.gateway.example.test"
	sisClusterResp.Store(dto)

	cluster, err := client.GetCluster(ctx, dto.ID)
	require.NoError(t, err)

	nb := cluster.NVCFBackend
	// Stage defaults
	assert.Equal(t, "https://stg.spot.gdn.nvidia.com", nb.Spec.ICMSConfig.ICMSServiceURL)
	assert.Equal(t, "sis.gateway.example.test", nb.Spec.ICMSConfig.ICMSServiceHostHeaderOverride)
	assert.Equal(t, "https://stage-oauth.example.test/token", nb.Spec.ICMSConfig.TokenURL)
	assert.Equal(t, "https://stg.vault.nvidia.com:443", nb.Spec.VaultConfig.Address)

	// GPU Discovery static from B64
	assert.NotNil(t, nb.Spec.ClusterConfig.GPUDiscovery.Static)
	assert.Contains(t, nb.Spec.ClusterConfig.GPUDiscovery.Static.GPUConfig, "L4")
	assert.Equal(t, uint64(4), nb.Spec.ClusterConfig.GPUDiscovery.Static.AllocatedGPUCapacity)
}

func TestGetCluster_MergeAttributes(t *testing.T) {
	ctx := context.Background()
	sisClusterResp := &atomic.Value{}
	sisServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.Header.Get("Authorization"), "Bearer abcd-1234")
		assert.Contains(t, r.URL.Path, "/v2/sis/clusters")

		sisCluster := sisClusterResp.Load().(clusterDTO)
		err := json.NewEncoder(w).Encode(sisCluster)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	t.Cleanup(func() { sisServer.Close() })

	tokFetcher := &mockTokenFetcher{
		token: "abcd-1234",
	}
	client, err := NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeStage, WithRootNGCAPIURL(sisServer.URL))
	require.NoError(t, err)
	require.NotNil(t, client)

	// Test merging of Attributes and CustomAttributes
	expectedClusterDTO := clusterDTO{
		ID:               uuid.NewString(),
		Name:             "my-cluster",
		NVCAVersion:      "1.0.0",
		Capabilities:     []string{string(DynamicGPUDiscovery)},
		OAuthClientID:    uuid.NewString(),
		Attributes:       []string{"key1=val1", "key2"},
		CustomAttributes: []string{"key3=val3", "key4"},
	}
	sisClusterResp.Store(expectedClusterDTO)

	actualCluster, err := client.GetCluster(ctx, expectedClusterDTO.ID)
	require.NoError(t, err)
	assert.NotNil(t, actualCluster)

	expectedAttributes := []string{"key1=val1", "key2=true", "key3=val3", "key4=true"}
	sort.Strings(expectedAttributes)

	assert.Equal(t, expectedAttributes, actualCluster.NVCFBackend.Spec.ClusterConfig.Attributes)

	// Test for duplicate keys between Attributes and CustomAttributes
	expectedClusterDTO.CustomAttributes = []string{"key1=different-val"}
	sisClusterResp.Store(expectedClusterDTO)

	actualCluster, err = client.GetCluster(ctx, expectedClusterDTO.ID)
	require.NoError(t, err)
	assert.NotNil(t, actualCluster)

	expectedAttributes = []string{"key1=different-val", "key2=true"}
	sort.Strings(expectedAttributes)
	assert.Equal(t, expectedAttributes, actualCluster.NVCFBackend.Spec.ClusterConfig.Attributes)
}

func TestGetCluster_OTelCollectorConfig(t *testing.T) {
	ctx := context.Background()

	sisClusterResp := &atomic.Value{}
	sisServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sisClusterResp.Load().(clusterDTO))
	}))
	t.Cleanup(func() { sisServer.Close() })

	tokFetcher := &mockTokenFetcher{token: "tok"}
	client, err := NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeProd, WithRootNGCAPIURL(sisServer.URL))
	require.NoError(t, err)

	t.Run("maps OTel collector config from ICMS response", func(t *testing.T) {
		dto := clusterDTO{
			ID:            uuid.NewString(),
			Name:          "test-cluster",
			NVCAVersion:   "2.47.0",
			Capabilities:  []string{string(DynamicGPUDiscovery)},
			OAuthClientID: uuid.NewString(),
			OTelCollector: newOTelCollectorConfig(true, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib", "0.139.0"),
		}
		sisClusterResp.Store(dto)

		cluster, err := client.GetCluster(ctx, dto.ID)
		require.NoError(t, err)

		nb := cluster.NVCFBackend
		assert.NotNil(t, nb.Spec.OTelCollector)
		assert.True(t, nb.Spec.OTelCollector.Enabled)
		assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib", nb.Spec.OTelCollector.ImageConfig.Repository)
		assert.Equal(t, "0.139.0", nb.Spec.OTelCollector.ImageConfig.Tag)
	})

	t.Run("handles nil OTel collector config from ICMS response", func(t *testing.T) {
		dto := clusterDTO{
			ID:            uuid.NewString(),
			Name:          "test-cluster-no-otel",
			NVCAVersion:   "2.47.0",
			Capabilities:  []string{string(DynamicGPUDiscovery)},
			OAuthClientID: uuid.NewString(),
		}
		sisClusterResp.Store(dto)

		cluster, err := client.GetCluster(ctx, dto.ID)
		require.NoError(t, err)

		nb := cluster.NVCFBackend
		assert.Nil(t, nb.Spec.OTelCollector)
	})
}

func TestGetCluster_NotFound(t *testing.T) {
	ctx := context.Background()

	sisServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(func() { sisServer.Close() })

	tokFetcher := &mockTokenFetcher{token: "tok"}
	client, err := NewNGCManagedClient(ctx, tokFetcher, "nvca-operator", nvidiaiov1.EnvTypeProd, WithRootNGCAPIURL(sisServer.URL))
	require.NoError(t, err)

	_, err = client.GetCluster(ctx, "non-existent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWithClusterSourceMapper(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		clusterSource  types.ClusterSource
		initialSource  types.ClusterSource
		expectedSource types.ClusterSource
		expectError    bool
	}{
		{
			name:           "set self-managed cluster source",
			clusterSource:  types.ClusterSourceSelfHosted,
			initialSource:  "",
			expectedSource: types.ClusterSourceSelfHosted,
			expectError:    false,
		},
		{
			name:           "set ngc-managed cluster source",
			clusterSource:  types.ClusterSourceNGCManaged,
			initialSource:  "",
			expectedSource: types.ClusterSourceNGCManaged,
			expectError:    false,
		},
		{
			name:           "set helm-managed cluster source",
			clusterSource:  types.ClusterSourceHelmManaged,
			initialSource:  "",
			expectedSource: types.ClusterSourceHelmManaged,
			expectError:    false,
		},
		{
			name:           "override existing cluster source",
			clusterSource:  types.ClusterSourceSelfHosted,
			initialSource:  types.ClusterSourceNGCManaged,
			expectedSource: types.ClusterSourceSelfHosted,
			expectError:    false,
		},
		{
			name:           "set empty cluster source",
			clusterSource:  "",
			initialSource:  types.ClusterSourceHelmManaged,
			expectedSource: "",
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a Cluster with initial state
			cluster := &Cluster{
				NVCFBackend: &nvidiaiov1.NVCFBackend{
					Spec: nvidiaiov1.NVCFBackendSpec{
						NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
							ClusterSource: tt.initialSource,
						},
					},
				},
			}

			// Create the mapper with the test cluster source
			mapper := withClusterSourceMapper(tt.clusterSource)

			// Apply the mapper
			err := mapper(ctx, nvidiaiov1.EnvTypeProd, nil, cluster)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedSource, cluster.NVCFBackend.Spec.ClusterSource)
			}
		})
	}
}

func TestWithClusterSourceMapper_Integration(t *testing.T) {
	ctx := context.Background()

	t.Run("mapper does not modify other fields", func(t *testing.T) {
		// Create a fully populated cluster
		initialCluster := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version:       "2.47.3",
						ClusterSource: types.ClusterSourceNGCManaged,
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-nca-id",
						},
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName:      "test-cluster",
							ClusterGroupName: "test-group",
							CloudProvider:    "AWS",
							Region:           "us-west-2",
						},
						ICMSConfig: nvidiaiov1.ICMSConfig{
							ICMSServiceURL: "https://icms.nvidia.com",
						},
					},
				},
			},
		}

		// Apply mapper to change only ClusterSource
		mapper := withClusterSourceMapper(types.ClusterSourceSelfHosted)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, nil, initialCluster)
		require.NoError(t, err)

		// Verify ClusterSource changed
		assert.Equal(t, types.ClusterSourceSelfHosted, initialCluster.NVCFBackend.Spec.ClusterSource)

		// Verify other fields remain unchanged
		assert.Equal(t, "2.47.3", initialCluster.NVCFBackend.Spec.Version)
		assert.Equal(t, "test-nca-id", initialCluster.NVCFBackend.Spec.AccountConfig.NCAID)
		assert.Equal(t, "test-cluster", initialCluster.NVCFBackend.Spec.ClusterConfig.ClusterName)
		assert.Equal(t, "test-group", initialCluster.NVCFBackend.Spec.ClusterConfig.ClusterGroupName)
		assert.Equal(t, "AWS", initialCluster.NVCFBackend.Spec.ClusterConfig.CloudProvider)
		assert.Equal(t, "us-west-2", initialCluster.NVCFBackend.Spec.ClusterConfig.Region)
		assert.Equal(t, "https://icms.nvidia.com", initialCluster.NVCFBackend.Spec.ICMSConfig.ICMSServiceURL)
	})

	t.Run("mapper works with nil clusterDTO", func(t *testing.T) {
		cluster := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withClusterSourceMapper(types.ClusterSourceHelmManaged)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, nil, cluster)
		require.NoError(t, err)
		assert.Equal(t, types.ClusterSourceHelmManaged, cluster.NVCFBackend.Spec.ClusterSource)
	})

	t.Run("mapper ignores envType parameter", func(t *testing.T) {
		cluster := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withClusterSourceMapper(types.ClusterSourceSelfHosted)

		// Test with different envTypes
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, nil, cluster)
		require.NoError(t, err)
		assert.Equal(t, types.ClusterSourceSelfHosted, cluster.NVCFBackend.Spec.ClusterSource)

		cluster.NVCFBackend.Spec.ClusterSource = ""
		err = mapper(ctx, nvidiaiov1.EnvTypeStage, nil, cluster)
		require.NoError(t, err)
		assert.Equal(t, types.ClusterSourceSelfHosted, cluster.NVCFBackend.Spec.ClusterSource)
	})

	t.Run("multiple mappers can be chained", func(t *testing.T) {
		cluster := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		// Apply first mapper
		mapper1 := withClusterSourceMapper(types.ClusterSourceNGCManaged)
		err := mapper1(ctx, nvidiaiov1.EnvTypeProd, nil, cluster)
		require.NoError(t, err)
		assert.Equal(t, types.ClusterSourceNGCManaged, cluster.NVCFBackend.Spec.ClusterSource)

		// Apply second mapper (simulating override)
		mapper2 := withClusterSourceMapper(types.ClusterSourceSelfHosted)
		err = mapper2(ctx, nvidiaiov1.EnvTypeProd, nil, cluster)
		require.NoError(t, err)
		assert.Equal(t, types.ClusterSourceSelfHosted, cluster.NVCFBackend.Spec.ClusterSource)
	})
}

func TestWithOTelCollectorMapper(t *testing.T) {
	ctx := context.Background()

	t.Run("maps OTel collector config when present in source", func(t *testing.T) {
		src := &clusterDTO{
			OTelCollector: newOTelCollectorConfig(true, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib", "0.139.0"),
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{},
		}

		mapper := withOTelCollectorMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		assert.NotNil(t, dest.NVCFBackend.Spec.OTelCollector)
		assert.True(t, dest.NVCFBackend.Spec.OTelCollector.Enabled)
		assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib", dest.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository)
		assert.Equal(t, "0.139.0", dest.NVCFBackend.Spec.OTelCollector.ImageConfig.Tag)
	})

	t.Run("sets nil when OTel collector not present in source", func(t *testing.T) {
		src := &clusterDTO{
			OTelCollector: nil,
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{},
		}

		mapper := withOTelCollectorMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		assert.Nil(t, dest.NVCFBackend.Spec.OTelCollector)
	})

	t.Run("handles empty repository and tag", func(t *testing.T) {
		src := &clusterDTO{
			OTelCollector: newOTelCollectorConfig(false, "", ""),
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{},
		}

		mapper := withOTelCollectorMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		assert.NotNil(t, dest.NVCFBackend.Spec.OTelCollector)
		assert.False(t, dest.NVCFBackend.Spec.OTelCollector.Enabled)
		assert.Empty(t, dest.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository)
		assert.Empty(t, dest.NVCFBackend.Spec.OTelCollector.ImageConfig.Tag)
	})

	t.Run("mapper ignores envType parameter", func(t *testing.T) {
		src := &clusterDTO{
			OTelCollector: newOTelCollectorConfig(true, "test-repo", "test-tag"),
		}

		// Test with Prod env
		destProd := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{},
		}

		mapper := withOTelCollectorMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, destProd)
		require.NoError(t, err)

		// Test with Stage env
		destStage := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{},
		}

		err = mapper(ctx, nvidiaiov1.EnvTypeStage, src, destStage)
		require.NoError(t, err)

		// Both should have same result regardless of envType
		assert.Equal(t, destProd.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository, destStage.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository)
		assert.Equal(t, destProd.NVCFBackend.Spec.OTelCollector.ImageConfig.Tag, destStage.NVCFBackend.Spec.OTelCollector.ImageConfig.Tag)
	})

	t.Run("does not modify other fields", func(t *testing.T) {
		src := &clusterDTO{
			OTelCollector: newOTelCollectorConfig(true, "otel-repo", "otel-tag"),
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version:       "2.47.3",
						ClusterSource: types.ClusterSourceNGCManaged,
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-nca-id",
						},
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName: "test-cluster",
						},
					},
				},
			},
		}

		mapper := withOTelCollectorMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		// Verify OTel collector was set
		assert.Equal(t, "otel-repo", dest.NVCFBackend.Spec.OTelCollector.ImageConfig.Repository)

		// Verify other fields remain unchanged
		assert.Equal(t, "2.47.3", dest.NVCFBackend.Spec.Version)
		assert.Equal(t, types.ClusterSourceNGCManaged, dest.NVCFBackend.Spec.ClusterSource)
		assert.Equal(t, "test-nca-id", dest.NVCFBackend.Spec.AccountConfig.NCAID)
		assert.Equal(t, "test-cluster", dest.NVCFBackend.Spec.ClusterConfig.ClusterName)
	})
}

func TestWithAgentConfigMapper(t *testing.T) {
	ctx := context.Background()

	t.Run("maps all cluster configurations correctly", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: CaseInsensitiveClusterConfigMap{
				AgentNodeSelectorLabelKeyConfig:          "node-pool",
				AgentNodeSelectorLabelValueConfig:        "gpu-pool",
				AgentPriorityClassNameConfig:             "high-priority",
				ModelCacheVolumeMountOptionEnabledConfig: "enabled",
				ModelCacheVolumeMountOptionsConfig:       "rw,noatime",
				NVCFWorkerDegradationPeriodMinutesConfig: "30",
				NVCASecretMirrorSourceNamespaceConfig:    "nvca-secrets",
				NVCASecretMirrorLabelSelectorConfig:      "app=nvca",
			},
			Agent: &agentDTO{
				Tolerations: []corev1.Toleration{{
					Key:      "agent-taint",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				}},
				NATSURL:                                                "nats://nats.localhost:14222",
				NATSHostOverride:                                       "nats.gateway.example.test",
				HelmReValStageOAuthTokenURL:                            "https://stage-reval-oauth.example.test/token",
				HelmReValStageOAuthPublicKeysetEndpoint:                "https://stage-reval-oauth.example.test/.well-known/jwks.json",
				HelmReValProdOAuthTokenURL:                             "https://prod-reval-oauth.example.test/token",
				HelmReValProdOAuthPublicKeysetEndpoint:                 "https://prod-reval-oauth.example.test/.well-known/jwks.json",
				FunctionDeploymentStagesStageOAuthTokenURL:             "https://stage-fnds-oauth.example.test/token",
				FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint: "https://stage-fnds-oauth.example.test/.well-known/jwks.json",
				FunctionDeploymentStagesProdOAuthTokenURL:              "https://prod-fnds-oauth.example.test/token",
				FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint:  "https://prod-fnds-oauth.example.test/.well-known/jwks.json",
			},
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		// Verify DeploymentConfig
		assert.Equal(t, "high-priority", dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.PriorityClassName)
		assert.Equal(t, "node-pool", dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.NodeSelectorKey)
		assert.Equal(t, "gpu-pool", dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.NodeSelectorValue)
		assert.Equal(t, "nvca-secrets", dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.SecretMirrorSourceNamespace)
		assert.Equal(t, "app=nvca", dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.SecretMirrorLabelSelector)
		assert.Equal(t, []corev1.Toleration{{
			Key:      "agent-taint",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		}}, dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.Tolerations)
		require.NotNil(t, dest.NVCFBackend.Spec.AgentConfig.NATSURL)
		assert.Equal(t, "nats://nats.localhost:14222", *dest.NVCFBackend.Spec.AgentConfig.NATSURL)
		require.NotNil(t, dest.NVCFBackend.Spec.AgentConfig.NATSHostOverride)
		assert.Equal(t, "nats.gateway.example.test", *dest.NVCFBackend.Spec.AgentConfig.NATSHostOverride)
		assert.Equal(t, "https://stage-reval-oauth.example.test/token", dest.NVCFBackend.Spec.AgentConfig.HelmReValStageOAuthTokenURL)
		assert.Equal(t, "https://stage-reval-oauth.example.test/.well-known/jwks.json", dest.NVCFBackend.Spec.AgentConfig.HelmReValStageOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://prod-reval-oauth.example.test/token", dest.NVCFBackend.Spec.AgentConfig.HelmReValProdOAuthTokenURL)
		assert.Equal(t, "https://prod-reval-oauth.example.test/.well-known/jwks.json", dest.NVCFBackend.Spec.AgentConfig.HelmReValProdOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://stage-fnds-oauth.example.test/token", dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthTokenURL)
		assert.Equal(t, "https://stage-fnds-oauth.example.test/.well-known/jwks.json", dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://prod-fnds-oauth.example.test/token", dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthTokenURL)
		assert.Equal(t, "https://prod-fnds-oauth.example.test/.well-known/jwks.json", dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint)

		// Verify NVCFWorkerConfig
		assert.True(t, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptionsEnabled)
		assert.Equal(t, "rw,noatime", dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptions)
		assert.Equal(t, 30*60*1000000000, int(dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.WorkerDegradationPeriod))
	})

	t.Run("handles cache mount option enabled variations", func(t *testing.T) {
		testCases := []struct {
			name     string
			value    string
			expected bool
		}{
			{"enabled", "enabled", true},
			{"Enabled", "Enabled", true},
			{"ENABLED", "ENABLED", true},
			{"disabled", "disabled", false},
			{"false", "false", false},
			{"true", "true", false},
			{"True", "True", false},
			{"TRUE", "TRUE", false},
			{"1", "1", false},
			{"0", "0", false},
			{"empty", "", false}, // Key present but empty = disabled
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				src := &clusterDTO{
					ClusterConfigurations: CaseInsensitiveClusterConfigMap{
						ModelCacheVolumeMountOptionEnabledConfig: tc.value,
					},
				}

				dest := &Cluster{
					NVCFBackend: &nvidiaiov1.NVCFBackend{
						Spec: nvidiaiov1.NVCFBackendSpec{
							NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
						},
					},
				}

				mapper := withAgentConfigMapper()
				err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
				require.NoError(t, err)
				assert.Equal(t, tc.expected, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptionsEnabled)
			})
		}
	})

	t.Run("handles missing cache mount option config - defaults to enabled for upgrades", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: CaseInsensitiveClusterConfigMap{},
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)
		// Should default to true for upgrade compatibility when key is missing
		assert.True(t, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptionsEnabled)
		// Should also default to standard read-only options
		assert.Equal(t, types.DefaultCacheCSIMountOptions, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptions)
	})

	t.Run("handles missing enabled config with explicit options - uses provided options", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: CaseInsensitiveClusterConfigMap{
				ModelCacheVolumeMountOptionsConfig: "rw,noatime",
			},
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)
		// Should default to true when enabled config is missing
		assert.True(t, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptionsEnabled)
		// Should use the explicitly provided options, not the default
		assert.Equal(t, "rw,noatime", dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptions)
	})

	t.Run("handles explicitly disabled cache mount options", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: CaseInsensitiveClusterConfigMap{
				ModelCacheVolumeMountOptionEnabledConfig: "disabled",
				ModelCacheVolumeMountOptionsConfig:       "rw,noatime",
			},
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)
		// Should respect explicit disabled setting
		assert.False(t, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptionsEnabled)
		// Should still preserve the options value even when disabled
		assert.Equal(t, "rw,noatime", dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptions)
	})

	t.Run("handles missing worker degradation period", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: CaseInsensitiveClusterConfigMap{},
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)
		assert.Equal(t, int64(0), int64(dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.WorkerDegradationPeriod))
	})

	t.Run("returns error for invalid worker degradation period", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: CaseInsensitiveClusterConfigMap{
				NVCFWorkerDegradationPeriodMinutesConfig: "invalid",
			},
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse worker degradation period minutes")
	})

	t.Run("handles nil cluster configurations", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: nil,
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)
		// All fields should be empty/zero except for upgrade defaults
		assert.Empty(t, dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.PriorityClassName)
		assert.Empty(t, dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.NodeSelectorKey)
		// Should default to enabled for upgrade compatibility
		assert.True(t, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptionsEnabled)
		assert.Equal(t, types.DefaultCacheCSIMountOptions, dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.CacheMountOptions)
	})

	t.Run("handles partial configuration", func(t *testing.T) {
		src := &clusterDTO{
			ClusterConfigurations: CaseInsensitiveClusterConfigMap{
				AgentPriorityClassNameConfig:             "high-priority",
				NVCFWorkerDegradationPeriodMinutesConfig: "15",
			},
		}

		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withAgentConfigMapper()
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)
		assert.Equal(t, "high-priority", dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.PriorityClassName)
		assert.Empty(t, dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig.NodeSelectorKey)
		assert.Equal(t, 15*60*1000000000, int(dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig.WorkerDegradationPeriod))
	})
}

func TestCaseInsensitiveClusterConfigMap(t *testing.T) {
	t.Run("unmarshals keys case-insensitively", func(t *testing.T) {
		// Test JSON with various case variations
		testCases := []struct {
			name     string
			jsonData string
			expected CaseInsensitiveClusterConfigMap
		}{
			{
				name: "lowercase keys",
				jsonData: `{
					"agentnodeSelectorlabelkey": "node-pool",
					"agentnodeSelectorlabelvalue": "gpu-pool",
					"agentpriorityclassname": "high-priority"
				}`,
				expected: CaseInsensitiveClusterConfigMap{
					AgentNodeSelectorLabelKeyConfig:   "node-pool",
					AgentNodeSelectorLabelValueConfig: "gpu-pool",
					AgentPriorityClassNameConfig:      "high-priority",
				},
			},
			{
				name: "uppercase keys",
				jsonData: `{
					"AGENTNODESELECTORLABELKEY": "node-pool",
					"MODELCACHEVOLUMEMOUNTOPTIONS": "rw,noatime"
				}`,
				expected: CaseInsensitiveClusterConfigMap{
					AgentNodeSelectorLabelKeyConfig:    "node-pool",
					ModelCacheVolumeMountOptionsConfig: "rw,noatime",
				},
			},
			{
				name: "mixed case keys",
				jsonData: `{
					"AgentNodeSelectorLabelKey": "node-pool",
					"agentNodeSelectorLabelValue": "gpu-pool",
					"AGENTPRIORITYCLASSNAME": "high-priority",
					"modelCacheVolumeMountOptions": "rw,noatime",
					"NVCFWorkerDegradationPeriodMinutes": "30"
				}`,
				expected: CaseInsensitiveClusterConfigMap{
					AgentNodeSelectorLabelKeyConfig:          "node-pool",
					AgentNodeSelectorLabelValueConfig:        "gpu-pool",
					AgentPriorityClassNameConfig:             "high-priority",
					ModelCacheVolumeMountOptionsConfig:       "rw,noatime",
					NVCFWorkerDegradationPeriodMinutesConfig: "30",
				},
			},
			{
				name: "exact canonical case",
				jsonData: `{
					"AgentNodeSelectorLabelKey": "node-pool",
					"AgentNodeSelectorLabelValue": "gpu-pool"
				}`,
				expected: CaseInsensitiveClusterConfigMap{
					AgentNodeSelectorLabelKeyConfig:   "node-pool",
					AgentNodeSelectorLabelValueConfig: "gpu-pool",
				},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				var config CaseInsensitiveClusterConfigMap
				err := json.Unmarshal([]byte(tc.jsonData), &config)
				require.NoError(t, err)
				assert.Equal(t, tc.expected, config)
			})
		}
	})

	t.Run("marshals correctly", func(t *testing.T) {
		config := CaseInsensitiveClusterConfigMap{
			AgentNodeSelectorLabelKeyConfig:   "node-pool",
			AgentNodeSelectorLabelValueConfig: "gpu-pool",
			AgentPriorityClassNameConfig:      "high-priority",
		}

		data, err := json.Marshal(config)
		require.NoError(t, err)

		// Unmarshal to verify it's valid JSON and contains expected keys
		var result map[string]string
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		assert.Equal(t, "node-pool", result["AgentNodeSelectorLabelKey"])
		assert.Equal(t, "gpu-pool", result["AgentNodeSelectorLabelValue"])
		assert.Equal(t, "high-priority", result["AgentPriorityClassName"])
	})

	t.Run("round trip marshaling preserves data", func(t *testing.T) {
		original := CaseInsensitiveClusterConfigMap{
			AgentNodeSelectorLabelKeyConfig:          "node-pool",
			AgentNodeSelectorLabelValueConfig:        "gpu-pool",
			AgentPriorityClassNameConfig:             "high-priority",
			ModelCacheVolumeMountOptionsConfig:       "rw,noatime",
			NVCFWorkerDegradationPeriodMinutesConfig: "30",
		}

		// Marshal to JSON
		data, err := json.Marshal(original)
		require.NoError(t, err)

		// Unmarshal back
		var result CaseInsensitiveClusterConfigMap
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		assert.Equal(t, original, result)
	})

	t.Run("handles unknown keys", func(t *testing.T) {
		jsonData := `{
			"AgentNodeSelectorLabelKey": "node-pool",
			"UnknownCustomKey": "custom-value",
			"AnotherUnknownKey": "another-value"
		}`

		var config CaseInsensitiveClusterConfigMap
		err := json.Unmarshal([]byte(jsonData), &config)
		require.NoError(t, err)

		// Known key should be normalized
		assert.Equal(t, "node-pool", config[AgentNodeSelectorLabelKeyConfig])

		// Unknown keys should be stored as-is for forward compatibility
		assert.Equal(t, "custom-value", config[ClusterConfigurationKeyType("UnknownCustomKey")])
		assert.Equal(t, "another-value", config[ClusterConfigurationKeyType("AnotherUnknownKey")])
	})

	t.Run("handles empty JSON object", func(t *testing.T) {
		jsonData := `{}`

		var config CaseInsensitiveClusterConfigMap
		err := json.Unmarshal([]byte(jsonData), &config)
		require.NoError(t, err)
		assert.Empty(t, config)
	})
}

func TestClusterNetworkCIDRParsing(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		k8sNetworkCIDRs []string
		cidrRangeConfig string
		expectedCIDRs   []string
		expectError     bool
	}{
		{
			name:            "no ClusterNetworkCIDRAllowedRange config",
			k8sNetworkCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"},
			cidrRangeConfig: "",
			expectedCIDRs:   []string{"10.0.0.0/8", "172.16.0.0/12"},
		},
		{
			name:            "single CIDR in config",
			k8sNetworkCIDRs: []string{"10.0.0.0/8"},
			cidrRangeConfig: "1.2.3.4/32",
			expectedCIDRs:   []string{"10.0.0.0/8", "1.2.3.4/32"},
		},
		{
			name:            "comma-separated CIDRs without quotes",
			k8sNetworkCIDRs: []string{"10.0.0.0/8"},
			cidrRangeConfig: "1.2.3.4/32,5.6.7.8/32",
			expectedCIDRs:   []string{"10.0.0.0/8", "1.2.3.4/32", "5.6.7.8/32"},
		},
		{
			name:            "comma-separated CIDRs with double quotes",
			k8sNetworkCIDRs: []string{"10.0.0.0/8"},
			cidrRangeConfig: `"1.2.3.4/32","5.6.7.8/32"`,
			expectedCIDRs:   []string{"10.0.0.0/8", "1.2.3.4/32", "5.6.7.8/32"},
		},
		{
			name:            "comma-separated CIDRs with mixed quotes (NGC format - double quotes inside single)",
			k8sNetworkCIDRs: []string{"10.0.0.0/8"},
			cidrRangeConfig: `'"1.2.3.4/32","5.6.7.8/32"'`,
			expectedCIDRs:   []string{"10.0.0.0/8", "1.2.3.4/32", "5.6.7.8/32"},
		},
		{
			name:            "comma-separated CIDRs with single quotes around each CIDR",
			k8sNetworkCIDRs: []string{"10.0.0.0/8"},
			cidrRangeConfig: "'1.2.3.4/32','5.6.7.8/32'",
			expectedCIDRs:   []string{"10.0.0.0/8", "1.2.3.4/32", "5.6.7.8/32"},
		},
		{
			name:            "comma-separated CIDRs with spaces",
			k8sNetworkCIDRs: []string{"10.0.0.0/8"},
			cidrRangeConfig: "1.2.3.4/32, 5.6.7.8/32, 9.10.11.12/32",
			expectedCIDRs:   []string{"10.0.0.0/8", "1.2.3.4/32", "5.6.7.8/32", "9.10.11.12/32"},
		},
		{
			name:            "empty k8sNetworkCIDRs with config",
			k8sNetworkCIDRs: []string{},
			cidrRangeConfig: `'"1.2.3.4/32","5.6.7.8/32"'`,
			expectedCIDRs:   []string{"1.2.3.4/32", "5.6.7.8/32"},
		},
		{
			name:            "handles empty strings in comma-separated list",
			k8sNetworkCIDRs: []string{"10.0.0.0/8"},
			cidrRangeConfig: "1.2.3.4/32,,5.6.7.8/32",
			expectedCIDRs:   []string{"10.0.0.0/8", "1.2.3.4/32", "5.6.7.8/32"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create source cluster DTO
			src := &clusterDTO{
				ID:                     uuid.NewString(),
				Name:                   "test-cluster",
				NVCAVersion:            "1.0.0",
				K8sClusterNetworkCIDRs: tt.k8sNetworkCIDRs,
			}

			if tt.cidrRangeConfig != "" {
				src.ClusterConfigurations = CaseInsensitiveClusterConfigMap{
					ClusterNetworkCIDRAllowedRangeConfig: tt.cidrRangeConfig,
				}
			}

			// Create destination cluster
			dest := &Cluster{
				NVCFBackend: &nvidiaiov1.NVCFBackend{
					Spec: nvidiaiov1.NVCFBackendSpec{
						NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
					},
				},
			}

			// Apply the root mapper which includes the CIDR parsing logic
			mapper := withRootNVCFBackendMapper()
			err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.ElementsMatch(t, tt.expectedCIDRs, dest.NVCFBackend.Spec.ClusterConfig.K8sClusterNetworkCIDRs,
					"Expected CIDRs to match. Got: %v, Expected: %v",
					dest.NVCFBackend.Spec.ClusterConfig.K8sClusterNetworkCIDRs, tt.expectedCIDRs)
			}
		})
	}
}

func TestWithSharedStorageImageMapper(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		sharedStorage *struct{ ImageRepository, ImageTag string }
		expectNil     bool
		expectRepo    string
		expectTag     string
	}{
		{
			name:          "nil sharedStorage results in nil SharedStorageImage",
			sharedStorage: nil,
			expectNil:     true,
		},
		{
			name:          "with sharedStorage sets SharedStorageImage",
			sharedStorage: &struct{ ImageRepository, ImageTag string }{"nvcr.io/nvidia/shared-storage", "1.0.0"},
			expectNil:     false,
			expectRepo:    "nvcr.io/nvidia/shared-storage",
			expectTag:     "1.0.0",
		},
		{
			name:          "with empty values results in nil SharedStorageImage",
			sharedStorage: &struct{ ImageRepository, ImageTag string }{"", ""},
			expectNil:     true,
		},
		{
			name:          "with empty repository results in nil SharedStorageImage",
			sharedStorage: &struct{ ImageRepository, ImageTag string }{"", "1.0.0"},
			expectNil:     true,
		},
		{
			name:          "with empty tag results in nil SharedStorageImage",
			sharedStorage: &struct{ ImageRepository, ImageTag string }{"nvcr.io/nvidia/shared-storage", ""},
			expectNil:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &clusterDTO{}
			if tt.sharedStorage != nil {
				src.SharedStorage = &struct {
					ImageRepository string `json:"imageRepository"`
					ImageTag        string `json:"imageTag"`
				}{
					ImageRepository: tt.sharedStorage.ImageRepository,
					ImageTag:        tt.sharedStorage.ImageTag,
				}
			}

			dest := &Cluster{
				NVCFBackend: &nvidiaiov1.NVCFBackend{},
			}

			mapper := withSharedStorageImageMapper()
			err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
			require.NoError(t, err)

			if tt.expectNil {
				assert.Nil(t, dest.NVCFBackend.Spec.SharedStorageImage)
			} else {
				require.NotNil(t, dest.NVCFBackend.Spec.SharedStorageImage)
				assert.Equal(t, tt.expectRepo, dest.NVCFBackend.Spec.SharedStorageImage.Repository)
				assert.Equal(t, tt.expectTag, dest.NVCFBackend.Spec.SharedStorageImage.Tag)
			}
		})
	}
}

func TestWithBootstrapRegistrationMapper(t *testing.T) {
	ctx := context.Background()

	t.Run("sets cluster IDs from ConfigMap", func(t *testing.T) {
		fetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				Data: map[string]string{
					"clusterId":      "test-cluster-id",
					"clusterGroupId": "test-group-id",
				},
			}, nil
		}

		src := &clusterDTO{
			ID:      "",
			GroupID: "",
		}
		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withBootstrapRegistrationMapper(fetcher)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		assert.Equal(t, "test-cluster-id", src.ID)
		assert.Equal(t, "test-group-id", src.GroupID)
		assert.Equal(t, "test-cluster-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "test-group-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)
	})

	t.Run("does not override existing values with empty ConfigMap data", func(t *testing.T) {
		fetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				Data: map[string]string{
					"clusterId":      "",
					"clusterGroupId": "",
				},
			}, nil
		}

		src := &clusterDTO{
			ID:      "existing-cluster-id",
			GroupID: "existing-group-id",
		}
		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterID:      "existing-cluster-id",
							ClusterGroupID: "existing-group-id",
						},
					},
				},
			},
		}

		mapper := withBootstrapRegistrationMapper(fetcher)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		// Values should remain unchanged
		assert.Equal(t, "existing-cluster-id", src.ID)
		assert.Equal(t, "existing-group-id", src.GroupID)
		assert.Equal(t, "existing-cluster-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "existing-group-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)
	})

	t.Run("handles ConfigMap not found gracefully", func(t *testing.T) {
		fetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return nil, apierrors.NewNotFound(
				schema.GroupResource{Group: "", Resource: "configmaps"},
				"nvca-cluster-registration",
			)
		}

		src := &clusterDTO{
			ID:      "existing-id",
			GroupID: "existing-group",
		}
		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withBootstrapRegistrationMapper(fetcher)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		// Values should remain unchanged
		assert.Equal(t, "existing-id", src.ID)
		assert.Equal(t, "existing-group", src.GroupID)
	})

	t.Run("returns error for other fetcher errors", func(t *testing.T) {
		fetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return nil, errors.New("connection refused")
		}

		src := &clusterDTO{}
		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withBootstrapRegistrationMapper(fetcher)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection refused")
	})

	t.Run("handles nil Data map in ConfigMap", func(t *testing.T) {
		fetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				Data: nil,
			}, nil
		}

		src := &clusterDTO{
			ID:      "existing-id",
			GroupID: "existing-group",
		}
		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
				},
			},
		}

		mapper := withBootstrapRegistrationMapper(fetcher)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		// Values should remain unchanged
		assert.Equal(t, "existing-id", src.ID)
		assert.Equal(t, "existing-group", src.GroupID)
	})

	t.Run("partial ConfigMap data - only clusterId present", func(t *testing.T) {
		fetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				Data: map[string]string{
					"clusterId": "new-cluster-id",
				},
			}, nil
		}

		src := &clusterDTO{
			ID:      "",
			GroupID: "existing-group-id",
		}
		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterGroupID: "existing-group-id",
						},
					},
				},
			},
		}

		mapper := withBootstrapRegistrationMapper(fetcher)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		assert.Equal(t, "new-cluster-id", src.ID)
		assert.Equal(t, "existing-group-id", src.GroupID)
		assert.Equal(t, "new-cluster-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "existing-group-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)
	})

	t.Run("does not modify other cluster config fields", func(t *testing.T) {
		fetcher := func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{
				Data: map[string]string{
					"clusterId":      "new-cluster-id",
					"clusterGroupId": "new-group-id",
				},
			}, nil
		}

		src := &clusterDTO{
			Name:        "test-cluster",
			GroupName:   "test-group",
			Region:      "us-west-2",
			NVCAVersion: "2.50.0",
		}
		dest := &Cluster{
			NVCFBackend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "2.50.0",
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName:      "test-cluster",
							ClusterGroupName: "test-group",
							Region:           "us-west-2",
						},
					},
				},
			},
		}

		mapper := withBootstrapRegistrationMapper(fetcher)
		err := mapper(ctx, nvidiaiov1.EnvTypeProd, src, dest)
		require.NoError(t, err)

		// Verify cluster IDs were set
		assert.Equal(t, "new-cluster-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterID)
		assert.Equal(t, "new-group-id", dest.NVCFBackend.Spec.ClusterConfig.ClusterGroupID)

		// Verify other fields remain unchanged
		assert.Equal(t, "test-cluster", src.Name)
		assert.Equal(t, "test-group", src.GroupName)
		assert.Equal(t, "us-west-2", src.Region)
		assert.Equal(t, "test-cluster", dest.NVCFBackend.Spec.ClusterConfig.ClusterName)
		assert.Equal(t, "test-group", dest.NVCFBackend.Spec.ClusterConfig.ClusterGroupName)
		assert.Equal(t, "us-west-2", dest.NVCFBackend.Spec.ClusterConfig.Region)
	})
}
