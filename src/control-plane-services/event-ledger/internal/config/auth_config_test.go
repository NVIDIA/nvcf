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

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAuthConfig_Disabled(t *testing.T) {
	cfg := AuthConfig{
		Enabled: false,
	}

	err := ValidateAuthConfig(cfg, false)
	assert.NoError(t, err)
}

func TestValidateAuthConfig_JWTProvider(t *testing.T) {
	tests := []struct {
		name        string
		cfg         AuthConfig
		expectedErr error
	}{
		{
			name: "valid jwt config",
			cfg: AuthConfig{
				Enabled:     true,
				Provider:    "jwt",
				JWKSetUrl:   "https://example.com/.well-known/jwks.json",
				Issuer:      "https://issuer.example.com",
				Audience:    "event-ledger",
				TenantClaim: "tenant_id",
			},
			expectedErr: nil,
		},
		{
			name: "jwt missing jwk-set-url",
			cfg: AuthConfig{
				Enabled:  true,
				Provider: "jwt",
			},
			expectedErr: ErrMissingJWKSetURL,
		},
		{
			name: "jwt missing issuer",
			cfg: AuthConfig{
				Enabled:     true,
				Provider:    "jwt",
				JWKSetUrl:   "https://example.com/.well-known/jwks.json",
				Audience:    "event-ledger",
				TenantClaim: "tenant_id",
			},
			expectedErr: ErrMissingJWTIssuer,
		},
		{
			name: "jwt missing audience",
			cfg: AuthConfig{
				Enabled:     true,
				Provider:    "jwt",
				JWKSetUrl:   "https://example.com/.well-known/jwks.json",
				Issuer:      "https://issuer.example.com",
				TenantClaim: "tenant_id",
			},
			expectedErr: ErrMissingJWTAudience,
		},
		{
			name: "jwt missing tenant claim",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "jwt",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Issuer:    "https://issuer.example.com",
				Audience:  "event-ledger",
			},
			expectedErr: ErrMissingJWTTenantClaim,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAuthConfig(tt.cfg, false)
			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateAuthConfig_PolicyProvider(t *testing.T) {
	tests := []struct {
		name                 string
		cfg                  AuthConfig
		selfManaged bool
		expectedErr          error
	}{
		{
			name: "valid policy config",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					CredsFile:                  "/path/to/creds.yaml",
					TokenIssuerAddr:            "https://issuer.example.com",
					PolicyEvaluatorAddr:        "https://pdp.example.com",
					Namespace:                  "test",
					PolicyFQDN:                 "test.policy",
					CredentialsRefreshInterval: 300,
				},
			},
			expectedErr: nil,
		},
		{
			name: "policy missing creds-file",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					TokenIssuerAddr:     "https://issuer.example.com",
					PolicyEvaluatorAddr: "https://pdp.example.com",
					Namespace:           "test",
					PolicyFQDN:          "test.policy",
				},
			},
			expectedErr: ErrMissingPolicyCredsFile,
		},
		{
			name: "policy missing jwk-set-url",
			cfg: AuthConfig{
				Enabled:  true,
				Provider: "policy",
				Policy: PolicyConfig{
					CredsFile:           "/path/to/creds.yaml",
					TokenIssuerAddr:     "https://issuer.example.com",
					PolicyEvaluatorAddr: "https://pdp.example.com",
				},
			},
			expectedErr: ErrMissingJWKSetURL,
		},
		{
			name: "policy missing token-issuer-addr",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					CredsFile:           "/path/to/creds.yaml",
					PolicyEvaluatorAddr: "https://pdp.example.com",
					Namespace:           "test",
					PolicyFQDN:          "test.policy",
				},
			},
			expectedErr: ErrMissingPolicyTokenIssuerAddr,
		},
		{
			name: "policy missing policy-evaluator-addr",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					CredsFile:       "/path/to/creds.yaml",
					TokenIssuerAddr: "https://issuer.example.com",
				},
			},
			expectedErr: ErrMissingPolicyEvaluatorAddr,
		},
		{
			name: "policy missing namespace",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					CredsFile:           "/path/to/creds.yaml",
					TokenIssuerAddr:     "https://issuer.example.com",
					PolicyEvaluatorAddr: "https://pdp.example.com",
					PolicyFQDN:          "test.policy",
				},
			},
			expectedErr: ErrMissingPolicyNamespace,
		},
		{
			name: "policy missing policy-fqdn",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					CredsFile:           "/path/to/creds.yaml",
					TokenIssuerAddr:     "https://issuer.example.com",
					PolicyEvaluatorAddr: "https://pdp.example.com",
					Namespace:           "test",
				},
			},
			expectedErr: ErrMissingPolicyFQDN,
		},
		{
			name: "policy invalid creds-refresh-interval (zero)",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					CredsFile:                  "/path/to/creds.yaml",
					TokenIssuerAddr:            "https://issuer.example.com",
					PolicyEvaluatorAddr:        "https://pdp.example.com",
					Namespace:                  "test",
					PolicyFQDN:                 "test.policy",
					CredentialsRefreshInterval: 0,
				},
			},
			expectedErr: ErrInvalidPolicyCredsRefreshInterval,
		},
		{
			name: "policy invalid creds-refresh-interval (negative)",
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					CredsFile:                  "/path/to/creds.yaml",
					TokenIssuerAddr:            "https://issuer.example.com",
					PolicyEvaluatorAddr:        "https://pdp.example.com",
					Namespace:                  "test",
					PolicyFQDN:                 "test.policy",
					CredentialsRefreshInterval: -1,
				},
			},
			expectedErr: ErrInvalidPolicyCredsRefreshInterval,
		},
		{
			// In self-managed mode, OAuth2 fields are not required.
			name:                 "valid config in self-managed mode - oauth2 fields not required",
			selfManaged: true,
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					PolicyEvaluatorAddr: "https://pdp.example.com",
					Namespace:           "test",
					PolicyFQDN:          "test.policy",
				},
			},
			expectedErr: nil,
		},
		{
			// Even in self-managed mode, always-required fields are still checked.
			name:                 "self-managed mode does not bypass namespace check",
			selfManaged: true,
			cfg: AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://example.com/.well-known/jwks.json",
				Policy: PolicyConfig{
					PolicyEvaluatorAddr: "https://pdp.example.com",
					PolicyFQDN:          "test.policy",
				},
			},
			expectedErr: ErrMissingPolicyNamespace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAuthConfig(tt.cfg, tt.selfManaged)
			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateAuthConfig_InvalidProvider(t *testing.T) {
	tests := []struct {
		name        string
		cfg         AuthConfig
		expectedErr error
	}{
		{
			name: "empty provider when auth enabled",
			cfg: AuthConfig{
				Enabled:  true,
				Provider: "",
			},
			expectedErr: ErrMissingAuthProvider,
		},
		{
			name: "invalid provider",
			cfg: AuthConfig{
				Enabled:  true,
				Provider: "unknown",
			},
			expectedErr: ErrInvalidAuthProvider,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAuthConfig(tt.cfg, false)
			require.Error(t, err)
			assert.Equal(t, tt.expectedErr, err)
		})
	}
}

func TestValidateEndpointAuthConfig(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		expectedErr error
	}{
		{
			name: "jwt with v3-only endpoints",
			cfg: Config{
				Auth:               AuthConfig{Enabled: true, Provider: "jwt"},
				DeprecateEndpoints: true,
			},
		},
		{
			name: "jwt with legacy endpoints",
			cfg: Config{
				Auth: AuthConfig{Enabled: true, Provider: "jwt"},
			},
			expectedErr: ErrJWTLegacyEndpointsUnsupported,
		},
		{
			name: "policy with legacy endpoints",
			cfg: Config{
				Auth: AuthConfig{Enabled: true, Provider: "policy"},
			},
		},
		{
			name: "authentication disabled with legacy endpoints",
			cfg: Config{
				Auth: AuthConfig{Enabled: false, Provider: "jwt"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, ValidateEndpointAuthConfig(tt.cfg), tt.expectedErr)
		})
	}
}
