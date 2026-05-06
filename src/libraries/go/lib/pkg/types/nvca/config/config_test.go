// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nvcaconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

func TestConfig_Init(t *testing.T) {
	cfg := Config{
		Environment: EnvironmentProduction,
		Cluster: NVCFClusterConfig{
			ID: "foo",
		},
		Agent: AgentConfig{
			LogLevel:     "debug",
			FeatureFlags: []string{"Foo"},
			NamespaceLabels: map[string]string{
				"foo":                    "bar",
				"app.kubernetes.io/name": "baz",
			},
			AgentTimeConfig: AgentTimeConfig{
				CredRenewInterval: 2 * time.Millisecond,
			},
			NATSURL:           "nats://nats.localhost:14222",
			SkipSelfDestruct:  false,
			ForceSelfDestruct: true,
		},
		Authz: AuthzConfig{
			ClientSecretKey: "shouldnotbewritten",
		},
		Workload: WorkloadConfig{
			WorkloadTimeConfig: WorkloadTimeConfig{
				WorkerDegradationTimeout: 2 * time.Hour,
			},
		},
	}

	expConfigStr := `
agent:
  NATSURL: nats://nats.localhost:14222
  credRenewInterval: 2ms
  featureFlags:
  - Foo
  forceSelfDestruct: true
  logLevel: debug
  namespaceLabels:
    app.kubernetes.io/name: baz
    foo: bar
cluster:
  id: foo
environment: prod
workload:
  workerDegradationTimeout: 2h0m0s
`

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(cfgPath, []byte(expConfigStr), 0600)
	require.NoError(t, err)

	gotCfg, err := Init(cfgPath)
	require.NoError(t, err)

	assert.Empty(t, gotCfg.Authz.ClientSecretKey)
	cfg.Authz.ClientSecretKey = ""
	assert.Equal(t, cfg, gotCfg)
}

func TestConfig_EncodeDecode(t *testing.T) {
	cfg := Config{
		Environment: EnvironmentProduction,
		Cluster: NVCFClusterConfig{
			ID: "foo",
		},
		Agent: AgentConfig{
			LogLevel:     "debug",
			FeatureFlags: []string{"Foo"},
			NamespaceLabels: map[string]string{
				"foo":                    "bar",
				"app.kubernetes.io/name": "baz",
			},
			AgentTimeConfig: AgentTimeConfig{
				CredRenewInterval: 2 * time.Millisecond,
			},
			NATSURL:           "nats://nats.localhost:14222",
			SkipSelfDestruct:  false,
			ForceSelfDestruct: true,
		},
		Authz: AuthzConfig{
			ClientSecretKey: "shouldnotbewritten",
		},
		Workload: WorkloadConfig{
			WorkloadTimeConfig: WorkloadTimeConfig{
				WorkerDegradationTimeout: 2 * time.Hour,
			},
		},
	}

	expConfigStr := `
agent:
  NATSURL: nats://nats.localhost:14222
  credRenewInterval: 2ms
  featureFlags:
  - Foo
  forceSelfDestruct: true
  logLevel: debug
  namespaceLabels:
    app.kubernetes.io/name: baz
    foo: bar
cluster:
  id: foo
environment: prod
workload:
  workerDegradationTimeout: 2h0m0s
`

	gotConfigBytes, err := EncodeConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, strings.TrimPrefix(expConfigStr, "\n"), string(gotConfigBytes))

	gotDecodedCfg, err := DecodeConfig([]byte(expConfigStr))
	require.NoError(t, err)
	expCfg := cfg
	expCfg.Authz.ClientSecretKey = ""
	assert.Equal(t, expCfg, gotDecodedCfg)

	// Check env override.
	t.Setenv("NVCA_WORKLOAD_WORKER_DEGRADATION_TIMEOUT", "1h")
	t.Setenv("NVCA_AUTHZ_CLIENT_SECRET_KEY", "shouldalsonotbewritten")
	expCfg.Workload.WorkerDegradationTimeout = 1 * time.Hour

	gotDecodedCfg, err = DecodeConfig([]byte(expConfigStr))
	require.NoError(t, err)
	assert.Equal(t, expCfg, gotDecodedCfg)
}

func TestConfig_EncodeDecode_ServiceOAuthEndpoints(t *testing.T) {
	cfg := Config{
		Agent: AgentConfig{
			HelmReValServiceURL:                                    "http://reval.localhost:8080",
			HelmReValStageOAuthTokenURL:                            "https://stage-reval-oauth.example.test/token",
			HelmReValStageOAuthPublicKeysetEndpoint:                "https://stage-reval-oauth.example.test/.well-known/jwks.json",
			HelmReValProdOAuthTokenURL:                             "https://prod-reval-oauth.example.test/token",
			HelmReValProdOAuthPublicKeysetEndpoint:                 "https://prod-reval-oauth.example.test/.well-known/jwks.json",
			FunctionDeploymentStagesServiceURL:                     "https://deployment-stages.stg.nvcf.nvidia.com",
			FunctionDeploymentStagesStageOAuthTokenURL:             "https://stage-fnds-oauth.example.test/token",
			FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint: "https://stage-fnds-oauth.example.test/.well-known/jwks.json",
			FunctionDeploymentStagesProdOAuthTokenURL:              "https://prod-fnds-oauth.example.test/token",
			FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint:  "https://prod-fnds-oauth.example.test/.well-known/jwks.json",
			RolloverServiceURL:                                     "https://stg.api.ros.nvidia.com",
			RolloverServiceStageOAuthTokenURL:                      "https://stage-ros-oauth.example.test/token",
			RolloverServiceStageOAuthPublicKeysetEndpoint:          "https://stage-ros-oauth.example.test/.well-known/jwks.json",
			RolloverServiceProdOAuthTokenURL:                       "https://prod-ros-oauth.example.test/token",
			RolloverServiceProdOAuthPublicKeysetEndpoint:           "https://prod-ros-oauth.example.test/.well-known/jwks.json",
		},
	}

	expConfigStr := `
agent:
  functionDeploymentStagesProdOAuthPublicKeysetEndpoint: https://prod-fnds-oauth.example.test/.well-known/jwks.json
  functionDeploymentStagesProdOAuthTokenURL: https://prod-fnds-oauth.example.test/token
  functionDeploymentStagesServiceURL: https://deployment-stages.stg.nvcf.nvidia.com
  functionDeploymentStagesStageOAuthPublicKeysetEndpoint: https://stage-fnds-oauth.example.test/.well-known/jwks.json
  functionDeploymentStagesStageOAuthTokenURL: https://stage-fnds-oauth.example.test/token
  helmReValProdOAuthPublicKeysetEndpoint: https://prod-reval-oauth.example.test/.well-known/jwks.json
  helmReValProdOAuthTokenURL: https://prod-reval-oauth.example.test/token
  helmReValServiceURL: http://reval.localhost:8080
  helmReValStageOAuthPublicKeysetEndpoint: https://stage-reval-oauth.example.test/.well-known/jwks.json
  helmReValStageOAuthTokenURL: https://stage-reval-oauth.example.test/token
  rolloverServiceProdOAuthPublicKeysetEndpoint: https://prod-ros-oauth.example.test/.well-known/jwks.json
  rolloverServiceProdOAuthTokenURL: https://prod-ros-oauth.example.test/token
  rolloverServiceStageOAuthPublicKeysetEndpoint: https://stage-ros-oauth.example.test/.well-known/jwks.json
  rolloverServiceStageOAuthTokenURL: https://stage-ros-oauth.example.test/token
  rolloverServiceURL: https://stg.api.ros.nvidia.com
`

	gotConfigBytes, err := EncodeConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, strings.TrimPrefix(expConfigStr, "\n"), string(gotConfigBytes))

	gotDecodedCfg, err := DecodeConfig([]byte(expConfigStr))
	require.NoError(t, err)
	assert.Equal(t, cfg, gotDecodedCfg)
}

func TestConfig_EncodeDecode_Tolerations(t *testing.T) {
	cfg := Config{
		Agent: AgentConfig{
			Tolerations: []corev1.Toleration{{
				Key:      "dedicated",
				Operator: corev1.TolerationOpEqual,
				Value:    "nvca",
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		},
		Workload: WorkloadConfig{
			Tolerations: []corev1.Toleration{{
				Key:      "workload",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoExecute,
			}},
		},
	}

	encoded, err := EncodeConfig(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "tolerations:")
	assert.Contains(t, string(encoded), "dedicated")
	assert.Contains(t, string(encoded), "workload")

	decoded, err := DecodeConfig(encoded)
	require.NoError(t, err)
	assert.Equal(t, cfg.Agent.Tolerations, decoded.Agent.Tolerations)
	assert.Equal(t, cfg.Workload.Tolerations, decoded.Workload.Tolerations)
}

func TestConfig_Merge(t *testing.T) {
	t.Setenv("NVCA_AUTHZ_CLIENT_SECRET_KEY", "shouldalsonotbewritten")

	oldCfg := Config{
		Environment: EnvironmentProduction,
		Cluster: NVCFClusterConfig{
			ID: "foo",
		},
		Agent: AgentConfig{
			LogLevel:     "debug",
			FeatureFlags: []string{"Foo"},
			NamespaceLabels: map[string]string{
				"foo":                    "bar",
				"app.kubernetes.io/name": "baz",
			},
			AgentTimeConfig: AgentTimeConfig{
				CredRenewInterval: 2 * time.Millisecond,
			},
			SkipSelfDestruct:  false,
			ForceSelfDestruct: true,
		},
		Authz: AuthzConfig{
			ClientSecretKey: "shouldnotbewritten",
		},
		Workload: WorkloadConfig{
			WorkloadTimeConfig: WorkloadTimeConfig{
				WorkerDegradationTimeout: 2 * time.Hour,
			},
		},
	}
	oldConfigStr := `
agent:
  credRenewInterval: 2ms
  featureFlags:
  - Foo
  forceSelfDestruct: true
  logLevel: debug
  namespaceLabels:
    app.kubernetes.io/name: baz
    foo: bar
cluster:
  id: foo
environment: prod
workload:
  workerDegradationTimeout: 2h0m0s
`

	expCfg := oldCfg
	expCfg.Authz.ClientSecretKey = ""
	expCfg.Workload.WorkerDegradationTimeout = 1 * time.Hour
	expCfg.Agent.LogLevel = "info"
	expCfg.Agent.AdditionalResourceOverhead = ResourceList{
		corev1.ResourceCPU:              resource.MustParse("1"),
		corev1.ResourceMemory:           resource.MustParse("2Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("4Gi"),
	}

	expConfigStr := `
agent:
  credRenewInterval: 2ms
  featureFlags:
  - Foo
  forceSelfDestruct: true
  logLevel: info
  namespaceLabels:
    app.kubernetes.io/name: baz
    foo: bar
  additionalResourceOverhead:
    cpu: "1"
    memory: 2Gi
    ephemeral-storage: 4Gi
cluster:
  id: foo
environment: prod
workload:
  workerDegradationTimeout: 1h0m0s
`
	expConfigBytesJSON, err := yaml.YAMLToJSON([]byte(expConfigStr))
	require.NoError(t, err)

	gotConfigBytes, err := EncodeConfig(oldCfg, Config{
		Agent: AgentConfig{
			LogLevel: "info",
			AdditionalResourceOverhead: ResourceList{
				corev1.ResourceCPU:              resource.MustParse("1"),
				corev1.ResourceMemory:           resource.MustParse("2Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("4Gi"),
			},
		},
		Workload: WorkloadConfig{
			WorkloadTimeConfig: WorkloadTimeConfig{
				WorkerDegradationTimeout: 1 * time.Hour,
			},
		},
	})
	require.NoError(t, err)
	godConfigBytesJSON, err := yaml.YAMLToJSON([]byte(gotConfigBytes))
	require.NoError(t, err)
	assert.JSONEq(t, string(expConfigBytesJSON), string(godConfigBytesJSON))

	// Check env override
	t.Setenv("NVCA_WORKLOAD_WORKER_DEGRADATION_TIMEOUT", "1h")
	gotDecodedCfg, err := DecodeConfig([]byte(oldConfigStr), []byte(`
agent:
  logLevel: info
  additionalResourceOverhead:
    cpu: "1"
    memory: 2Gi
    ephemeral-storage: 4Gi
`))
	require.NoError(t, err)
	assert.Equal(t, expCfg, gotDecodedCfg)
}

func TestDecodeConfig(t *testing.T) {
	t.Run("basic_decode", func(t *testing.T) {
		data := []byte(`
environment: stg
cluster:
  name: test-cluster
  id: cluster-123
agent:
  logLevel: debug
`)
		cfg, err := DecodeConfig(data)
		require.NoError(t, err)
		assert.Equal(t, EnvironmentStaging, cfg.Environment)
		assert.Equal(t, "test-cluster", cfg.Cluster.Name)
		assert.Equal(t, "cluster-123", cfg.Cluster.ID)
		assert.Equal(t, "debug", cfg.Agent.LogLevel)
	})

	t.Run("merge_extra_configs", func(t *testing.T) {
		base := []byte(`
cluster:
  name: base-cluster
agent:
  logLevel: info
`)
		extra := []byte(`
agent:
  logLevel: debug
  systemNamespace: nvca-system
`)
		cfg, err := DecodeConfig(base, extra)
		require.NoError(t, err)
		assert.Equal(t, "base-cluster", cfg.Cluster.Name)
		assert.Equal(t, "debug", cfg.Agent.LogLevel)
		assert.Equal(t, "nvca-system", cfg.Agent.SystemNamespace)
	})

	t.Run("invalid_yaml", func(t *testing.T) {
		data := []byte(`invalid: yaml: content:`)
		_, err := DecodeConfig(data)
		assert.Error(t, err)
	})

	t.Run("camelCase_alias", func(t *testing.T) {
		// Test that camelCase keys work via aliases
		data := []byte(`
agent:
  logLevel: debug
  svcAddress: ":8080"
`)
		cfg, err := DecodeConfig(data)
		require.NoError(t, err)
		assert.Equal(t, "debug", cfg.Agent.LogLevel)
		assert.Equal(t, ":8080", cfg.Agent.SvcAddress)
	})

	t.Run("duration_parsing", func(t *testing.T) {
		data := []byte(`
agent:
  credRenewInterval: 30m
  heartbeatInterval: 5m
workload:
  maxRunningTimeout: 3h
`)
		cfg, err := DecodeConfig(data)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Minute, cfg.Agent.CredRenewInterval)
		assert.Equal(t, 5*time.Minute, cfg.Agent.HeartbeatInterval)
		assert.Equal(t, 3*time.Hour, cfg.Workload.MaxRunningTimeout)
	})
}

func TestEncodeConfig(t *testing.T) {
	t.Run("omits_zero_values", func(t *testing.T) {
		cfg := Config{
			Cluster: NVCFClusterConfig{
				Name: "my-cluster",
			},
		}
		data, err := EncodeConfig(cfg)
		require.NoError(t, err)
		assert.Contains(t, string(data), "name: my-cluster")
		assert.NotContains(t, string(data), "id:")
	})

	t.Run("excludes_mapstructure_dash_fields", func(t *testing.T) {
		cfg := Config{
			Authz: AuthzConfig{
				ClientID:        "secret-id",
				ClientSecretKey: "secret-key",
				TokenURL:        "https://auth.example.com",
			},
		}
		data, err := EncodeConfig(cfg)
		require.NoError(t, err)
		// These should be excluded due to `mapstructure:"-"`
		assert.NotContains(t, string(data), "secret-id")
		assert.NotContains(t, string(data), "secret-key")
		// This should be included
		assert.Contains(t, string(data), "tokenURL")
	})

	t.Run("slice_values", func(t *testing.T) {
		cfg := Config{
			Cluster: NVCFClusterConfig{
				Attributes: []string{"gpu", "nvlink"},
			},
		}
		data, err := EncodeConfig(cfg)
		require.NoError(t, err)
		assert.Contains(t, string(data), "- gpu")
		assert.Contains(t, string(data), "- nvlink")
	})

	t.Run("map_values", func(t *testing.T) {
		cfg := Config{
			Agent: AgentConfig{
				NamespaceLabels: map[string]string{
					"app": "nvca",
					"env": "prod",
				},
			},
		}
		data, err := EncodeConfig(cfg)
		require.NoError(t, err)
		assert.Contains(t, string(data), "app: nvca")
		assert.Contains(t, string(data), "env: prod")
	})
}

func TestNewViperDecoderConfig(t *testing.T) {
	// Verify the decoder config is created without error
	opt := NewViperDecoderConfig()
	assert.NotNil(t, opt)
}
