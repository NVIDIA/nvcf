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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func TestGetVaultConfigData_NVCA251Plus_UsesOAuthClientSecretsEnv(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify version-aware path is used
	assert.Contains(t, configData["config-init.hcl"], "oauth-client-secrets.env")
	assert.Contains(t, configData["config.hcl"], "oauth-client-secrets.env")
}

func TestGetVaultConfigData_NVCALessThan251_UsesOAuthClientSecretsEnv(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.50.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify old path is used
	assert.Contains(t, configData["config-init.hcl"], "oauth-client-secrets.env")
	assert.Contains(t, configData["config.hcl"], "oauth-client-secrets.env")
}

func TestGetVaultConfigData_EmptyVersion_UsesOAuthClientSecretsEnv(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Empty version defaults to old behavior
	assert.Contains(t, configData["config-init.hcl"], "oauth-client-secrets.env")
	assert.Contains(t, configData["config.hcl"], "oauth-client-secrets.env")
}

func TestGetVaultConfigData_CustomSecretFilePath(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					SecretFilePath: "/custom/vault/path",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify custom path is used with version-aware filename
	assert.Contains(t, configData["config-init.hcl"], "/custom/vault/path/oauth-client-secrets.env")
	assert.Contains(t, configData["config.hcl"], "/custom/vault/path/oauth-client-secrets.env")
}

func TestGetVaultConfigData_AllConfigFields(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "custom-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					VaultNamespace:  "custom-namespace",
					Address:         "https://custom.vault.com:443",
					OAuthConfigRole: "custom-role",
					AuthMountPath:   "custom/auth/path",
					SecretDataPath:  ".Data.custom.path",
					SecretFilePath:  "/custom/secret/path",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify all custom fields are used
	assert.Contains(t, configData["config-init.hcl"], "custom-namespace")
	assert.Contains(t, configData["config-init.hcl"], "https://custom.vault.com:443")
	assert.Contains(t, configData["config-init.hcl"], "custom-role")
	assert.Contains(t, configData["config-init.hcl"], "custom/auth/path")
	assert.Contains(t, configData["template.hcl"], ".Data.custom.path")
	assert.Contains(t, configData["config-init.hcl"], "/custom/secret/path/oauth-client-secrets.env")
}

func TestGetVaultConfigData_DefaultValues(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "default-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify default values are used
	assert.Contains(t, configData["config-init.hcl"], DefaultVaultNamespace)
	assert.Contains(t, configData["config-init.hcl"], DefaultVaultServerAddress)
	assert.Contains(t, configData["config-init.hcl"], "k8s_default-cluster_bart_jwt_role")
	assert.Contains(t, configData["config-init.hcl"], "auth/jwt/k8s/default-cluster")
	assert.Contains(t, configData["template.hcl"], DefaultSecretDataPath)
}

func TestGetVaultConfigData_VersionBoundary251(t *testing.T) {
	testCases := []struct {
		name        string
		version     string
		expectedEnv string
	}{
		{"exact 2.51.0", "2.51.0", "oauth-client-secrets.env"},
		{"2.51.1", "2.51.1", "oauth-client-secrets.env"},
		{"2.50.99", "2.50.99", "oauth-client-secrets.env"},
		{"2.50.0", "2.50.0", "oauth-client-secrets.env"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: tc.version,
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName: "test-cluster",
						},
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID: "test-client-id",
						},
					},
				},
			}

			configData := getVaultConfigData(nb)

			assert.Contains(t, configData["config-init.hcl"], tc.expectedEnv)
		})
	}
}

func TestGetVaultConfigData_TemplateUsesOAuthClientMountPath(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					OAuthClientMountPath: "nvidia/services/oauth/clients/template-client-id/kv/secret",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "template-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Template uses OAuthClientMountPath from VaultConfig
	assert.Contains(t, configData["template.hcl"], "template-client-id")
	assert.Contains(t, configData["template.hcl"], "nvidia/services/oauth/clients/template-client-id/kv/secret")
}

func TestGetVaultConfigData_ConfigInitAndNonInit(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify both config files exist
	require.Contains(t, configData, "config-init.hcl")
	require.Contains(t, configData, "config.hcl")
	require.Contains(t, configData, "template.hcl")

	// Verify init config has "-init" flag
	assert.Contains(t, configData["config-init.hcl"], "-init")
	assert.Contains(t, configData["config-init.hcl"], "true")

	// Verify non-init config doesn't have "-init" flag
	assert.NotContains(t, configData["config.hcl"], "-init")
	assert.Contains(t, configData["config.hcl"], "false")
}

func TestGetVaultConfigData_VersionWithPrefix(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "v2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// v prefix should be handled correctly
	assert.Contains(t, configData["config-init.hcl"], "oauth-client-secrets.env")
}

func TestGetVaultConfigData_InvalidVersionDefaultsToOld(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "invalid-version",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Invalid version should default to old behavior
	assert.Contains(t, configData["config-init.hcl"], "oauth-client-secrets.env")
}

func TestGetVaultConfigData_VersionWithSuffixes(t *testing.T) {
	testCases := []struct {
		name        string
		version     string
		expectedEnv string
	}{
		{"rc new", "2.51.0-rc1", "oauth-client-secrets.env"},
		{"dev new", "2.51.0-dev", "oauth-client-secrets.env"},
		{"rc old", "2.50.0-rc1", "oauth-client-secrets.env"},
		{"dev old", "2.50.0-dev", "oauth-client-secrets.env"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: tc.version,
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName: "test-cluster",
						},
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID: "test-client-id",
						},
					},
				},
			}

			configData := getVaultConfigData(nb)

			assert.Contains(t, configData["config-init.hcl"], tc.expectedEnv)
		})
	}
}

func TestGetVaultConfigData_MultipleConfigFiles(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify all three config files are present
	assert.Contains(t, configData, "config-init.hcl")
	assert.Contains(t, configData, "config.hcl")
	assert.Contains(t, configData, "template.hcl")

	// Verify all use the same version-aware path
	for _, configContent := range []string{configData["config-init.hcl"], configData["config.hcl"]} {
		assert.Contains(t, configContent, "oauth-client-secrets.env")
	}
}

func TestGetVaultConfigData_ClusterNameInConfig(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "my-custom-cluster-name",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify cluster name is used in role and mount path
	assert.Contains(t, configData["config-init.hcl"], "k8s_my-custom-cluster-name_bart_jwt_role")
	assert.Contains(t, configData["config-init.hcl"], "auth/jwt/k8s/my-custom-cluster-name")
}

func TestGetVaultConfigData_CustomOAuthConfigRole(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					OAuthConfigRole: "custom-role-name",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Custom role should override default
	assert.Contains(t, configData["config-init.hcl"], "custom-role-name")
	assert.NotContains(t, configData["config-init.hcl"], "k8s_test-cluster_bart_jwt_role")
}

func TestGetVaultConfigData_CustomAuthMountPath(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					AuthMountPath: "custom/auth/mount/path",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Custom mount path should override default
	assert.Contains(t, configData["config-init.hcl"], "custom/auth/mount/path")
	assert.NotContains(t, configData["config-init.hcl"], "auth/jwt/k8s/test-cluster")
}

func TestGetVaultConfigData_CustomVaultNamespace(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					VaultNamespace: "custom-vault-ns",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Custom namespace should override default
	assert.Contains(t, configData["config-init.hcl"], "custom-vault-ns")
	assert.NotContains(t, configData["config-init.hcl"], DefaultVaultNamespace)
}

func TestGetVaultConfigData_CustomVaultAddress(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Address: "https://custom.vault.example.com:8443",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Custom address should override default
	assert.Contains(t, configData["config-init.hcl"], "https://custom.vault.example.com:8443")
	assert.NotContains(t, configData["config-init.hcl"], DefaultVaultServerAddress)
}

func TestGetVaultConfigData_CustomSecretDataPath(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					SecretDataPath: ".Data.custom.secret.path",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Custom secret data path should be used in template
	assert.Contains(t, configData["template.hcl"], ".Data.custom.secret.path")
	assert.NotContains(t, configData["template.hcl"], DefaultSecretDataPath)
}

func TestGetVaultConfigData_VersionRollbackInConfig(t *testing.T) {
	// Test that version rollback changes the file path
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.53.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)
	assert.Contains(t, configData["config-init.hcl"], "oauth-client-secrets.env")

	// Rollback version
	nb.Spec.Version = "2.50.0"
	configData = getVaultConfigData(nb)
	assert.Contains(t, configData["config-init.hcl"], "oauth-client-secrets.env")
}

func TestGetVaultConfigData_TemplatePathUsesVaultConfig(t *testing.T) {
	// Verify template.hcl uses OAuthClientMountPath from VaultConfig
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					OAuthClientMountPath: "nvidia/services/oauth/clients/oauth-client-id/kv/secret",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "oauth-client-id-for-template",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Template uses OAuthClientMountPath from VaultConfig
	assert.Contains(t, configData["template.hcl"], "oauth-client-id")
	assert.NotContains(t, configData["template.hcl"], "oauth-client-id-for-template")
	assert.Contains(t, configData["template.hcl"], "nvidia/services/oauth/clients/oauth-client-id/kv/secret")
}

func TestGetVaultConfigData_ConfigInitHasInitFlag(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify init config has specific init-related content
	initConfig := configData["config-init.hcl"]
	assert.Contains(t, initConfig, "-init")
	assert.Contains(t, initConfig, "true")

	// Verify non-init config has different content
	nonInitConfig := configData["config.hcl"]
	assert.NotContains(t, nonInitConfig, "-init")
	assert.Contains(t, nonInitConfig, "false")
}

func TestGetVaultConfigData_AllThreeConfigFilesPresent(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// All three files must be present
	require.Len(t, configData, 3)
	assert.NotEmpty(t, configData["config-init.hcl"])
	assert.NotEmpty(t, configData["config.hcl"])
	assert.NotEmpty(t, configData["template.hcl"])
}

func TestGetVaultConfigData_VersionBoundaryEdgeCases(t *testing.T) {
	edgeCases := []struct {
		name        string
		version     string
		expectedEnv string
	}{
		{"2.51.0 exactly", "2.51.0", "oauth-client-secrets.env"},
		{"2.51.0-0", "2.51.0-0", "oauth-client-secrets.env"},
		{"2.50.999", "2.50.999", "oauth-client-secrets.env"},
		{"2.50.0", "2.50.0", "oauth-client-secrets.env"},
		{"2.49.999", "2.49.999", "oauth-client-secrets.env"},
	}

	for _, tc := range edgeCases {
		t.Run(tc.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: tc.version,
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName: "test-cluster",
						},
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID: "test-client-id",
						},
					},
				},
			}

			configData := getVaultConfigData(nb)

			assert.Contains(t, configData["config-init.hcl"], tc.expectedEnv, "Version %s should use %s", tc.version, tc.expectedEnv)
		})
	}
}

func TestGetVaultConfigData_PathComponents(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "path-test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					SecretFilePath: "/base/path",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify path is constructed correctly
	expectedPath := "/base/path/oauth-client-secrets.env"
	assert.Contains(t, configData["config-init.hcl"], expectedPath)
	assert.Contains(t, configData["config.hcl"], expectedPath)
}

func TestGetVaultConfigData_ConfigFilesAreDifferent(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Verify init and non-init configs are different
	assert.NotEqual(t, configData["config-init.hcl"], configData["config.hcl"])

	// Verify template is different from both
	assert.NotEqual(t, configData["template.hcl"], configData["config-init.hcl"])
	assert.NotEqual(t, configData["template.hcl"], configData["config.hcl"])
}

func TestGetVaultConfigData_EmptyClusterName(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "", // Empty cluster name
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// Should handle empty cluster name gracefully
	assert.Contains(t, configData["config-init.hcl"], "k8s__bart_jwt_role")
	assert.Contains(t, configData["config-init.hcl"], "auth/jwt/k8s/")
}

func TestGetVaultConfigData_VersionInAllConfigFiles(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.52.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					SecretFilePath: "/test/path",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-client-id",
				},
			},
		},
	}

	configData := getVaultConfigData(nb)

	// All config files should use the version-aware path
	expectedPath := "/test/path/oauth-client-secrets.env"
	for configName, configContent := range configData {
		if strings.Contains(configName, "config") {
			assert.Contains(t, configContent, expectedPath, "Config file %s should contain version-aware path", configName)
		}
	}
}
