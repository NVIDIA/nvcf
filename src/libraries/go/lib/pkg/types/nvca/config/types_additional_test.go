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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestResourceList_MarshalYAML(t *testing.T) {
	t.Run("marshals_nil_ResourceList", func(t *testing.T) {
		var rl ResourceList
		result, err := rl.MarshalYAML()
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("marshals_empty_ResourceList", func(t *testing.T) {
		rl := ResourceList{}
		result, err := rl.MarshalYAML()
		require.NoError(t, err)
		assert.NotNil(t, result)
		resultMap := result.(map[string]string)
		assert.Empty(t, resultMap)
	})

	t.Run("marshals_ResourceList_with_resources", func(t *testing.T) {
		rl := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		}
		result, err := rl.MarshalYAML()
		require.NoError(t, err)
		assert.NotNil(t, result)
		resultMap := result.(map[string]string)
		assert.Equal(t, "2", resultMap["cpu"])
		assert.Equal(t, "4Gi", resultMap["memory"])
	})

	t.Run("marshals_ResourceList_with_gpu", func(t *testing.T) {
		rl := ResourceList{
			corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("4"),
		}
		result, err := rl.MarshalYAML()
		require.NoError(t, err)
		assert.NotNil(t, result)
		resultMap := result.(map[string]string)
		assert.Equal(t, "4", resultMap["nvidia.com/gpu"])
	})
}

func TestResourceList_UnmarshalMapstructure(t *testing.T) {
	t.Run("unmarshals_valid_resource_map", func(t *testing.T) {
		rl := ResourceList{}
		input := map[string]any{
			"cpu":    "2",
			"memory": "4Gi",
		}
		err := rl.UnmarshalMapstructure(input)
		require.NoError(t, err)
		assert.Equal(t, resource.MustParse("2"), rl[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("4Gi"), rl[corev1.ResourceMemory])
	})

	t.Run("returns_error_on_invalid_type", func(t *testing.T) {
		rl := ResourceList{}
		input := "not a map"
		err := rl.UnmarshalMapstructure(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected resource list type")
	})

	t.Run("returns_error_on_non_string_resource_value", func(t *testing.T) {
		rl := ResourceList{}
		input := map[string]any{
			"cpu": 123,
		}
		err := rl.UnmarshalMapstructure(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected string type for resource")
	})

	t.Run("returns_error_on_invalid_quantity", func(t *testing.T) {
		rl := ResourceList{}
		input := map[string]any{
			"cpu": "invalid-quantity",
		}
		err := rl.UnmarshalMapstructure(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode resource")
	})

	t.Run("handles_nil_pointer", func(t *testing.T) {
		var rl *ResourceList
		err := rl.UnmarshalMapstructure(map[string]any{})
		require.NoError(t, err)
	})

	t.Run("unmarshals_multiple_resources", func(t *testing.T) {
		rl := ResourceList{}
		input := map[string]any{
			"cpu":               "4",
			"memory":            "8Gi",
			"nvidia.com/gpu":    "2",
			"ephemeral-storage": "100Gi",
		}
		err := rl.UnmarshalMapstructure(input)
		require.NoError(t, err)
		assert.Len(t, rl, 4)
		assert.Equal(t, resource.MustParse("4"), rl[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("8Gi"), rl[corev1.ResourceMemory])
		assert.Equal(t, resource.MustParse("2"), rl[corev1.ResourceName("nvidia.com/gpu")])
		assert.Equal(t, resource.MustParse("100Gi"), rl[corev1.ResourceEphemeralStorage])
	})
}

func TestSharedStorageConfig(t *testing.T) {
	t.Run("creates_default_SharedStorageConfig", func(t *testing.T) {
		cfg := SharedStorageConfig{}
		assert.Empty(t, cfg.Server.Image)
		assert.Nil(t, cfg.TaskData.StorageClassName)
	})

	t.Run("sets_SharedStorageConfig_fields", func(t *testing.T) {
		storageClass := "fast-ssd"
		cfg := SharedStorageConfig{
			Server: SharedStorageServerConfig{
				Image: "smb-server:latest",
				ContainerResources: ResourceRequirements{
					Limits: ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
				},
			},
			TaskData: SharedStorageTaskDataConfig{
				StorageClassName: &storageClass,
				PVMountOptions:   []string{"rw", "sync"},
				StorageCapacity:  resource.MustParse("100Gi"),
			},
		}
		assert.Equal(t, "smb-server:latest", cfg.Server.Image)
		assert.Equal(t, storageClass, *cfg.TaskData.StorageClassName)
		assert.Equal(t, []string{"rw", "sync"}, cfg.TaskData.PVMountOptions)
		assert.Equal(t, resource.MustParse("100Gi"), cfg.TaskData.StorageCapacity)
	})
}

func TestInternalPersistentStorageConfig(t *testing.T) {
	t.Run("creates_default_InternalPersistentStorageConfig", func(t *testing.T) {
		cfg := InternalPersistentStorageConfig{}
		assert.Empty(t, cfg.StorageClassName)
		assert.Nil(t, cfg.HardResourceQuota)
	})

	t.Run("sets_InternalPersistentStorageConfig_fields", func(t *testing.T) {
		cfg := InternalPersistentStorageConfig{
			StorageClassName: "gold",
			HardResourceQuota: ResourceList{
				corev1.ResourceStorage: resource.MustParse("500Gi"),
			},
		}
		assert.Equal(t, "gold", cfg.StorageClassName)
		assert.Equal(t, resource.MustParse("500Gi"), cfg.HardResourceQuota[corev1.ResourceStorage])
	})
}

func TestWebhookConfig(t *testing.T) {
	t.Run("creates_default_WebhookConfig", func(t *testing.T) {
		cfg := WebhookConfig{}
		assert.Empty(t, cfg.SvcAddress)
		assert.Empty(t, cfg.TLSKeyFile)
		assert.Empty(t, cfg.TLSCertFile)
		assert.Empty(t, cfg.TLSSecretName)
		assert.Nil(t, cfg.DCGMAnnotations)
	})

	t.Run("sets_WebhookConfig_fields", func(t *testing.T) {
		cfg := WebhookConfig{
			SvcAddress:    "webhook-svc:443",
			TLSKeyFile:    "/certs/tls.key",
			TLSCertFile:   "/certs/tls.crt",
			TLSSecretName: "webhook-certs",
			DCGMAnnotations: map[string]string{
				"dcgm.enable": "true",
			},
		}
		assert.Equal(t, "webhook-svc:443", cfg.SvcAddress)
		assert.Equal(t, "/certs/tls.key", cfg.TLSKeyFile)
		assert.Equal(t, "/certs/tls.crt", cfg.TLSCertFile)
		assert.Equal(t, "webhook-certs", cfg.TLSSecretName)
		assert.Equal(t, "true", cfg.DCGMAnnotations["dcgm.enable"])
	})
}

func TestTracingConfig(t *testing.T) {
	t.Run("creates_default_TracingConfig", func(t *testing.T) {
		cfg := TracingConfig{}
		assert.Equal(t, NoExporter, cfg.Exporter)
		assert.Empty(t, cfg.LightstepServiceName)
		assert.Empty(t, cfg.LightstepAccessToken)
	})

	t.Run("sets_TracingConfig_with_Lightstep", func(t *testing.T) {
		cfg := TracingConfig{
			Exporter:             OTELExporter(LightstepExporter),
			LightstepServiceName: "my-service",
			LightstepAccessToken: "secret-token",
		}
		assert.Equal(t, OTELExporter(LightstepExporter), cfg.Exporter)
		assert.Equal(t, "my-service", cfg.LightstepServiceName)
		assert.Equal(t, "secret-token", cfg.LightstepAccessToken)
	})
}

func TestWorkloadConfig_Complete_WithEnvOverrides(t *testing.T) {
	t.Run("preserves_Tolerations", func(t *testing.T) {
		cfg := WorkloadConfig{
			Tolerations: []corev1.Toleration{{
				Key:      "dedicated",
				Operator: corev1.TolerationOpEqual,
				Value:    "nvca",
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		}
		completed := cfg.Complete()
		require.Len(t, completed.Tolerations, 1)
		assert.Equal(t, cfg.Tolerations, completed.Tolerations)
	})

	t.Run("preserves_FunctionEnvOverrides", func(t *testing.T) {
		cfg := WorkloadConfig{
			FunctionEnvOverrides: map[string]string{
				"INIT_CONTAINER":  "custom-init-image",
				"UTILS_CONTAINER": "custom-utils-image",
			},
		}
		completed := cfg.Complete()
		assert.Equal(t, "custom-init-image", completed.FunctionEnvOverrides["INIT_CONTAINER"])
		assert.Equal(t, "custom-utils-image", completed.FunctionEnvOverrides["UTILS_CONTAINER"])
	})

	t.Run("preserves_TaskEnvOverrides", func(t *testing.T) {
		cfg := WorkloadConfig{
			TaskEnvOverrides: map[string]string{
				"INIT_CONTAINER": "task-init-image",
			},
		}
		completed := cfg.Complete()
		assert.Equal(t, "task-init-image", completed.TaskEnvOverrides["INIT_CONTAINER"])
	})
}

func TestAgentConfig_Complete_AdditionalFields(t *testing.T) {
	t.Run("preserves_Tolerations", func(t *testing.T) {
		cfg := AgentConfig{
			Tolerations: []corev1.Toleration{{
				Key:      "dedicated",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		}
		completed := cfg.Complete(EnvironmentProduction)
		require.Len(t, completed.Tolerations, 1)
		assert.Equal(t, cfg.Tolerations, completed.Tolerations)
	})

	t.Run("preserves_SharedStorage_config", func(t *testing.T) {
		storageClass := "fast"
		cfg := AgentConfig{
			SharedStorage: SharedStorageConfig{
				Server: SharedStorageServerConfig{
					Image: "smb:v1",
				},
				TaskData: SharedStorageTaskDataConfig{
					StorageClassName: &storageClass,
				},
			},
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "smb:v1", completed.SharedStorage.Server.Image)
		assert.Equal(t, storageClass, *completed.SharedStorage.TaskData.StorageClassName)
	})

	t.Run("preserves_InternalPersistentStorage_config", func(t *testing.T) {
		cfg := AgentConfig{
			InternalPersistentStorage: InternalPersistentStorageConfig{
				StorageClassName: "standard",
			},
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "standard", completed.InternalPersistentStorage.StorageClassName)
	})

	t.Run("preserves_FeatureFlags", func(t *testing.T) {
		cfg := AgentConfig{
			FeatureFlags: []string{"enable-gpu", "enable-monitoring"},
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, []string{"enable-gpu", "enable-monitoring"}, completed.FeatureFlags)
	})

	t.Run("preserves_NamespaceLabels", func(t *testing.T) {
		cfg := AgentConfig{
			NamespaceLabels: map[string]string{
				"env":  "prod",
				"team": "ml",
			},
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "prod", completed.NamespaceLabels["env"])
		assert.Equal(t, "ml", completed.NamespaceLabels["team"])
	})

	t.Run("preserves_ComputeBackend", func(t *testing.T) {
		cfg := AgentConfig{
			ComputeBackend: "kubernetes",
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "kubernetes", completed.ComputeBackend)
	})

	t.Run("preserves_StaticGPUCapacity", func(t *testing.T) {
		cfg := AgentConfig{
			StaticGPUCapacity: 8,
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, uint64(8), completed.StaticGPUCapacity)
	})

	t.Run("preserves_MinHealthcheckRefreshWait", func(t *testing.T) {
		cfg := AgentConfig{
			MinHealthcheckRefreshWait: 30 * time.Second,
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, 30*time.Second, completed.MinHealthcheckRefreshWait)
	})

	t.Run("preserves_MaintenanceMode", func(t *testing.T) {
		cfg := AgentConfig{
			MaintenanceMode: MaintenanceModeCordon,
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, MaintenanceModeCordon, completed.MaintenanceMode)
	})

	t.Run("preserves_all_URL_fields", func(t *testing.T) {
		cfg := AgentConfig{
			HelmRepositoryPrefix:               "https://helm.example.com",
			HelmReValServiceURL:                "https://reval.example.com",
			NATSURL:                            "nats://nats.example.com:4222",
			RolloverServiceURL:                 "https://ros.example.com",
			FunctionDeploymentStagesServiceURL: "https://fnds.example.com",
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "https://helm.example.com", completed.HelmRepositoryPrefix)
		assert.Equal(t, "https://reval.example.com", completed.HelmReValServiceURL)
		assert.Equal(t, "nats://nats.example.com:4222", completed.NATSURL)
		assert.Equal(t, "https://ros.example.com", completed.RolloverServiceURL)
		assert.Equal(t, "https://fnds.example.com", completed.FunctionDeploymentStagesServiceURL)
	})

	t.Run("preserves_CSIVolumeMountOptions", func(t *testing.T) {
		cfg := AgentConfig{
			CSIVolumeMountOptions: []string{"rw", "sync", "noatime"},
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, []string{"rw", "sync", "noatime"}, completed.CSIVolumeMountOptions)
	})

	t.Run("preserves_version_fields", func(t *testing.T) {
		cfg := AgentConfig{
			OperatorVersion:           "v1.2.3",
			KubernetesVersionOverride: "v1.28.0",
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "v1.2.3", completed.OperatorVersion)
		assert.Equal(t, "v1.28.0", completed.KubernetesVersionOverride)
	})

	t.Run("preserves_ImageCredentialHelperImage", func(t *testing.T) {
		cfg := AgentConfig{
			ImageCredentialHelperImage: "nvcf/cred-helper:v1",
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "nvcf/cred-helper:v1", completed.ImageCredentialHelperImage)
	})

	t.Run("preserves_self_destruct_flags", func(t *testing.T) {
		cfg := AgentConfig{
			SkipSelfDestruct:  true,
			ForceSelfDestruct: false,
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.True(t, completed.SkipSelfDestruct)
		assert.False(t, completed.ForceSelfDestruct)
	})

	t.Run("preserves_SecretMirror_fields", func(t *testing.T) {
		cfg := AgentConfig{
			SecretMirrorSourceNamespace: "source-ns",
			SecretMirrorLabelSelector:   "app=mirror",
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "source-ns", completed.SecretMirrorSourceNamespace)
		assert.Equal(t, "app=mirror", completed.SecretMirrorLabelSelector)
	})

	t.Run("preserves_resource_overhead_fields", func(t *testing.T) {
		cfg := AgentConfig{
			UtilsResources: ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			AdditionalResourceOverhead: ResourceList{
				corev1.ResourceCPU: resource.MustParse("200m"),
			},
			BYOOResources: ResourceRequirements{
				Requests: ResourceList{
					corev1.ResourceCPU: resource.MustParse("50m"),
				},
			},
			BYOOFluentBitResources: ResourceRequirements{
				Limits: ResourceList{
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, resource.MustParse("100m"), completed.UtilsResources[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("256Mi"), completed.UtilsResources[corev1.ResourceMemory])
		assert.Equal(t, resource.MustParse("200m"), completed.AdditionalResourceOverhead[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("50m"), completed.BYOOResources.Requests[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("128Mi"), completed.BYOOFluentBitResources.Limits[corev1.ResourceMemory])
	})
}

func TestAuthzConfig_Complete_AdditionalFields(t *testing.T) {
	t.Run("preserves_all_Authz_fields", func(t *testing.T) {
		cfg := AuthzConfig{
			ClientID:                        "client-123",
			ClientSecretKey:                 "secret-key",
			ClientSecretsEnvFile:            "/secrets/env",
			TokenURL:                        "https://token.example.com",
			TokenScope:                      "openid profile",
			PublicKeysetEndpoint:            "https://keys.example.com",
			NGCServiceAPIKeyFile:            "/keys/ngc",
			NGCServiceAPIKey:                "ngc-key",
			SelfManagedVaultSecretsJSONPath: "/vault/secrets.json",
		}
		completed := cfg.Complete()
		assert.Equal(t, "client-123", completed.ClientID)
		assert.Equal(t, "secret-key", completed.ClientSecretKey)
		assert.Equal(t, "/secrets/env", completed.ClientSecretsEnvFile)
		assert.Equal(t, "https://token.example.com", completed.TokenURL)
		assert.Equal(t, "openid profile", completed.TokenScope)
		assert.Equal(t, "https://keys.example.com", completed.PublicKeysetEndpoint)
		assert.Equal(t, "/keys/ngc", completed.NGCServiceAPIKeyFile)
		assert.Equal(t, "ngc-key", completed.NGCServiceAPIKey)
		assert.Equal(t, "/vault/secrets.json", completed.SelfManagedVaultSecretsJSONPath)
	})
}

func TestNVCFClusterConfig_WithValidationPolicy(t *testing.T) {
	t.Run("sets_ValidationPolicy", func(t *testing.T) {
		cfg := NVCFClusterConfig{
			ValidationPolicy: &ValidationPolicyConfig{
				Name: "Default",
				AllowedExtraKubernetesTypes: []AllowedExtraKubernetesTypeConfig{
					{
						Group:    "custom.io",
						Version:  "v1",
						Kind:     "CustomResource",
						Resource: "customresources",
					},
				},
			},
		}
		assert.NotNil(t, cfg.ValidationPolicy)
		assert.Equal(t, "Default", cfg.ValidationPolicy.Name)
		assert.Len(t, cfg.ValidationPolicy.AllowedExtraKubernetesTypes, 1)
		assert.Equal(t, "custom.io", cfg.ValidationPolicy.AllowedExtraKubernetesTypes[0].Group)
	})
}
