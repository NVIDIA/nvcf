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

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func TestGetOAuthConfig(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected nvidiaiov1.OAuthConfig
	}{
		{
			name: "returns oauthConfig when client ID is set",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID:             "oauth-client-id",
							TokenURL:             "https://auth.example.com/token",
							PublicKeysetEndpoint: "https://auth.example.com/keys",
							ClientSecretKey:      "auth-secret",
							ClientSecretsEnvFile: "/auth/path/to/secrets",
							TokenScope:           "auth-scope",
						},
					},
				},
			},
			expected: nvidiaiov1.OAuthConfig{
				ClientID:             "oauth-client-id",
				TokenURL:             "https://auth.example.com/token",
				PublicKeysetEndpoint: "https://auth.example.com/keys",
				ClientSecretKey:      "auth-secret",
				ClientSecretsEnvFile: "/auth/path/to/secrets",
				TokenScope:           "auth-scope",
			},
		},
		{
			name: "prefer oauthConfig even with partial fields",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID: "oauth-client-id-only",
						},
					},
				},
			},
			expected: nvidiaiov1.OAuthConfig{
				ClientID: "oauth-client-id-only",
			},
		},
		{
			name: "return empty when neither set",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{},
			},
			expected: nvidiaiov1.OAuthConfig{},
		},
		{
			name: "return empty when OAuthConfig has empty ClientID",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID:        "",
							TokenURL:        "https://auth.example.com/token",
							ClientSecretKey: "secret",
						},
					},
				},
			},
			expected: nvidiaiov1.OAuthConfig{},
		},
		{
			name: "return empty when OAuthConfig has empty ClientID",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID: "",
						},
					},
				},
			},
			expected: nvidiaiov1.OAuthConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getOAuthConfig(tt.nb)
			if result.ClientID != tt.expected.ClientID {
				t.Errorf("ClientID = %v, want %v", result.ClientID, tt.expected.ClientID)
			}
			if result.TokenURL != tt.expected.TokenURL {
				t.Errorf("TokenURL = %v, want %v", result.TokenURL, tt.expected.TokenURL)
			}
			if result.PublicKeysetEndpoint != tt.expected.PublicKeysetEndpoint {
				t.Errorf("PublicKeysetEndpoint = %v, want %v", result.PublicKeysetEndpoint, tt.expected.PublicKeysetEndpoint)
			}
			if result.ClientSecretKey != tt.expected.ClientSecretKey {
				t.Errorf("ClientSecretKey = %v, want %v", result.ClientSecretKey, tt.expected.ClientSecretKey)
			}
			if result.ClientSecretsEnvFile != tt.expected.ClientSecretsEnvFile {
				t.Errorf("ClientSecretsEnvFile = %v, want %v", result.ClientSecretsEnvFile, tt.expected.ClientSecretsEnvFile)
			}
			if result.TokenScope != tt.expected.TokenScope {
				t.Errorf("TokenScope = %v, want %v", result.TokenScope, tt.expected.TokenScope)
			}
		})
	}
}

func TestGetOAuthClientMountPath(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "returns oauth client mount path",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						VaultConfig: nvidiaiov1.VaultConfig{
							OAuthClientMountPath: "nvidia/services/oauth/clients/client-id/kv/secret",
						},
					},
				},
			},
			expected: "nvidia/services/oauth/clients/client-id/kv/secret",
		},
		{
			name: "return empty when neither set",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						VaultConfig: nvidiaiov1.VaultConfig{},
					},
				},
			},
			expected: "",
		},
		{
			name: "return empty when both are empty strings",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						VaultConfig: nvidiaiov1.VaultConfig{
							OAuthClientMountPath: "",
						},
					},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getOAuthClientMountPath(tt.nb)
			if result != tt.expected {
				t.Errorf("getOAuthClientMountPath() = %v, want %v", result, tt.expected)
			}
		})
	}
}
