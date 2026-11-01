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

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func TestIsNVCAVersion251OrNewer(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		// Versions >= 2.51
		{"exact match", "2.51.0", true},
		{"newer minor", "2.52.0", true},
		{"newer patch", "2.51.1", true},
		{"much newer", "2.53.0", true},
		{"future major", "3.0.0", true},

		// Versions < 2.51
		{"older minor", "2.50.0", false},
		{"older major", "1.99.0", false},
		{"2.49.9", "2.49.9", false},

		// Versions with suffixes
		{"2.51.0-rc1", "2.51.0-rc1", true},
		{"2.51.0-dev", "2.51.0-dev", true},
		{"2.52.0-rc1", "2.52.0-rc1", true},
		{"2.50.0-rc1", "2.50.0-rc1", false},
		{"v2.51.0-dev", "v2.51.0-dev", true},
		{"2.51-rc1", "2.51-rc1", true},

		// Edge cases
		{"empty string", "", false},
		{"invalid format", "invalid", false},
		{"no patch", "2.51", true},
		{"with v prefix", "v2.51.0", true},
		{"with v prefix old", "v2.50.0", false},
		{"just major", "2", false},
		{"malformed patch", "2.51.x", true}, // Accepts as >= 2.51 since major.minor parse correctly
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNVCAVersion251OrNewer(tt.version)
			assert.Equal(t, tt.want, got, "isNVCAVersion251OrNewer(%q) = %v, want %v", tt.version, got, tt.want)
		})
	}
}

func TestGetClientSecretsEnvFile(t *testing.T) {
	basePath := "/vault/secrets"

	tests := []struct {
		name    string
		version string
		want    string
	}{
		// NVCA 2.51+ should use new file name
		{"2.51.0", "2.51.0", "/vault/secrets/oauth-client-secrets.env"},
		{"2.52.0", "2.52.0", "/vault/secrets/oauth-client-secrets.env"},
		{"2.53.0", "2.53.0", "/vault/secrets/oauth-client-secrets.env"},
		{"3.0.0", "3.0.0", "/vault/secrets/oauth-client-secrets.env"},

		// NVCA < 2.51 should use old file name
		{"2.50.0", "2.50.0", "/vault/secrets/oauth-client-secrets.env"},
		{"2.49.0", "2.49.0", "/vault/secrets/oauth-client-secrets.env"},
		{"1.99.0", "1.99.0", "/vault/secrets/oauth-client-secrets.env"},

		// Versions with suffixes
		{"2.51.0-rc1", "2.51.0-rc1", "/vault/secrets/oauth-client-secrets.env"},
		{"2.51.0-dev", "2.51.0-dev", "/vault/secrets/oauth-client-secrets.env"},
		{"2.50.0-rc1", "2.50.0-rc1", "/vault/secrets/oauth-client-secrets.env"},

		// Edge cases - default to old behavior
		{"empty", "", "/vault/secrets/oauth-client-secrets.env"},
		{"invalid", "invalid", "/vault/secrets/oauth-client-secrets.env"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getClientSecretsEnvFile(basePath, tt.version)
			assert.Equal(t, tt.want, got, "getClientSecretsEnvFile(%q, %q) = %q, want %q", basePath, tt.version, got, tt.want)
		})
	}
}

func TestGetAuthEnvVars(t *testing.T) {
	tests := []struct {
		name            string
		version         string
		vaultEnabled    bool
		clientID        string
		clientSecret    string
		wantEnvVarNames []string
		wantSecretNames []string
	}{
		{
			name:            "2.51.0 with vault",
			version:         "2.51.0",
			vaultEnabled:    true,
			clientID:        "test-id",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{},
			wantSecretNames: []string{},
		},
		{
			name:            "2.51.0 without vault",
			version:         "2.51.0",
			vaultEnabled:    false,
			clientID:        "test-id",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{"OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET_KEY"},
			wantSecretNames: []string{OAuthClientIDSecretName, OAuthClientKeySecretName},
		},
		{
			name:            "2.50.0 with vault",
			version:         "2.50.0",
			vaultEnabled:    true,
			clientID:        "test-id",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{},
			wantSecretNames: []string{},
		},
		{
			name:            "2.50.0 without vault",
			version:         "2.50.0",
			vaultEnabled:    false,
			clientID:        "test-id",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{"OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET_KEY"},
			wantSecretNames: []string{OAuthClientIDSecretName, OAuthClientKeySecretName},
		},
		{
			name:            "2.51.0-rc1 with vault",
			version:         "2.51.0-rc1",
			vaultEnabled:    true,
			clientID:        "test-id",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{},
			wantSecretNames: []string{},
		},
		{
			name:            "2.51.0-dev without vault",
			version:         "2.51.0-dev",
			vaultEnabled:    false,
			clientID:        "test-id",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{"OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET_KEY"},
			wantSecretNames: []string{OAuthClientIDSecretName, OAuthClientKeySecretName},
		},
		{
			name:            "2.50.0-rc1 with vault",
			version:         "2.50.0-rc1",
			vaultEnabled:    true,
			clientID:        "test-id",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{},
			wantSecretNames: []string{},
		},
		{
			name:            "empty clientID",
			version:         "2.51.0",
			vaultEnabled:    false,
			clientID:        "",
			clientSecret:    "test-secret",
			wantEnvVarNames: []string{},
			wantSecretNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: tt.version,
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: tt.vaultEnabled,
						},
					},
				},
			}

			oauthConfig := nvidiaiov1.OAuthConfig{
				ClientID:        tt.clientID,
				ClientSecretKey: tt.clientSecret,
			}

			envVars := getOAuthEnvVars(nb, oauthConfig)

			assert.Len(t, envVars, len(tt.wantEnvVarNames), "expected %d env vars, got %d", len(tt.wantEnvVarNames), len(envVars))

			for i, envVar := range envVars {
				assert.Equal(t, tt.wantEnvVarNames[i], envVar.Name, "env var %d name mismatch", i)
				assert.NotNil(t, envVar.ValueFrom, "env var %d ValueFrom is nil", i)
				assert.NotNil(t, envVar.ValueFrom.SecretKeyRef, "env var %d SecretKeyRef is nil", i)
				assert.Equal(t, tt.wantSecretNames[i], envVar.ValueFrom.SecretKeyRef.Name, "env var %d secret name mismatch", i)
			}
		})
	}
}

func TestGetAuthEnvVarsVaultBehavior(t *testing.T) {
	// When Vault is enabled, credentials come from ClientSecretsEnvFile (Vault agent output).
	// Do not add SecretKeyRef env vars since setupOAuthClientSecrets skips creating those secrets.
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: true,
				},
			},
		},
	}

	oauthConfig := nvidiaiov1.OAuthConfig{
		ClientID:        "test-id",
		ClientSecretKey: "test-secret",
	}

	envVars := getOAuthEnvVars(nb, oauthConfig)

	assert.Len(t, envVars, 0, "with vault enabled, no SecretKeyRef env vars (credentials from ClientSecretsEnvFile)")
}

func TestGetAuthEnvVarsSecretKeyRef(t *testing.T) {
	// Test the full secret reference structure
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	oauthConfig := nvidiaiov1.OAuthConfig{
		ClientID:        "test-id",
		ClientSecretKey: "test-secret",
	}

	envVars := getOAuthEnvVars(nb, oauthConfig)

	// Check OAUTH_CLIENT_ID structure
	clientIDVar := envVars[0]
	assert.Equal(t, "OAUTH_CLIENT_ID", clientIDVar.Name)
	assert.Equal(t, OAuthClientIDSecretName, clientIDVar.ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, OAuthClientIDSecretDataKey, clientIDVar.ValueFrom.SecretKeyRef.Key)

	// Check OAUTH_CLIENT_SECRET_KEY structure
	secretKeyVar := envVars[1]
	assert.Equal(t, "OAUTH_CLIENT_SECRET_KEY", secretKeyVar.Name)
	assert.Equal(t, OAuthClientKeySecretName, secretKeyVar.ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, OAuthClientKeySecretDataKey, secretKeyVar.ValueFrom.SecretKeyRef.Key)
}

func TestVersionDetectionRollbackScenario(t *testing.T) {
	// Simulate a version rollback scenario: 2.53 -> 2.50
	basePath := "/vault/secrets"

	// Initially running 2.53
	v253 := "2.53.0"
	assert.True(t, isNVCAVersion251OrNewer(v253))
	assert.Equal(t, "/vault/secrets/oauth-client-secrets.env", getClientSecretsEnvFile(basePath, v253))

	nb253 := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: v253,
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}
	oauthConfig := nvidiaiov1.OAuthConfig{
		ClientID:        "test-id",
		ClientSecretKey: "test-secret",
	}
	envVars253 := getOAuthEnvVars(nb253, oauthConfig)
	assert.Equal(t, "OAUTH_CLIENT_ID", envVars253[0].Name)
	assert.Equal(t, "OAUTH_CLIENT_SECRET_KEY", envVars253[1].Name)

	// Roll back to 2.50
	v250 := "2.50.0"
	assert.False(t, isNVCAVersion251OrNewer(v250))
	assert.Equal(t, "/vault/secrets/oauth-client-secrets.env", getClientSecretsEnvFile(basePath, v250))

	nb250 := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: v250,
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}
	envVars250 := getOAuthEnvVars(nb250, oauthConfig)
	assert.Equal(t, "OAUTH_CLIENT_ID", envVars250[0].Name)
	assert.Equal(t, "OAUTH_CLIENT_SECRET_KEY", envVars250[1].Name)
}
