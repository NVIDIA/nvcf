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

package ros

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
)

func TestNewTokenFetcher(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name          string
		opts          nvcaauth.TokenFetcherOptions
		rosServiceURL string
		envSetup      map[string]string
		wantErr       bool
	}{
		{
			name: "stage environment",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "test-client-id",
				SSAClientSecretKey: "test-secret-key",
			},
			rosServiceURL: "https://stage.stg.nvidia.com",
			envSetup:      map[string]string{},
			wantErr:       false,
		},
		{
			name: "prod environment",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "test-client-id",
				SSAClientSecretKey: "test-secret-key",
			},
			rosServiceURL: "https://prod.nvidia.com",
			envSetup:      map[string]string{},
			wantErr:       false,
		},
		{
			name: "environment variables override options",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "default-client-id",
				SSAClientSecretKey: "default-secret-key",
			},
			rosServiceURL: "https://stage.nvidia.com",
			envSetup: map[string]string{
				ssaClientIDEnvVarKey:        "env-client-id",
				ssaClientSecretKeyEnvVarKey: "env-secret-key",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment variables
			for k, v := range tt.envSetup {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			fetcher, healthCheck, err := NewTokenFetcher(ctx, tt.opts, tt.rosServiceURL)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, fetcher)
				assert.Nil(t, healthCheck)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, fetcher)
			assert.NotNil(t, healthCheck)
		})
	}
}

func TestGetFNDSTokenFetcherOptions(t *testing.T) {
	tests := []struct {
		name          string
		opts          nvcaauth.TokenFetcherOptions
		rosServiceURL string
		envSetup      map[string]string
		want          nvcaauth.TokenFetcherOptions
	}{
		{
			name: "stage environment - no env vars",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "test-client-id",
				SSAClientSecretKey: "test-secret-key",
			},
			rosServiceURL: "https://stage.stg.nvidia.com",
			envSetup:      map[string]string{},
			want: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "test-client-id",
				SSAClientSecretKey:      "test-secret-key",
				TokenURL:                stageTokenURL,
				SSATokenScope:           "instance_status",
				SSAPublicKeysetEndpoint: stageJWKSURL,
			},
		},
		{
			name: "stage environment - .stg. in middle of URL",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "test-client-id",
				SSAClientSecretKey: "test-secret-key",
			},
			rosServiceURL: "https://something.stg.something-else.nvidia.com",
			envSetup:      map[string]string{},
			want: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "test-client-id",
				SSAClientSecretKey:      "test-secret-key",
				TokenURL:                stageTokenURL,
				SSATokenScope:           "instance_status",
				SSAPublicKeysetEndpoint: stageJWKSURL,
			},
		},
		{
			name: "stage environment - stg. prefix in URL",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "test-client-id",
				SSAClientSecretKey: "test-secret-key",
			},
			rosServiceURL: "https://stg.api.ros.nvidia.com",
			envSetup:      map[string]string{},
			want: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "test-client-id",
				SSAClientSecretKey:      "test-secret-key",
				TokenURL:                stageTokenURL,
				SSATokenScope:           "instance_status",
				SSAPublicKeysetEndpoint: stageJWKSURL,
			},
		},
		{
			name: "stage environment - with token URL overrides",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "test-client-id",
				SSAClientSecretKey: "test-secret-key",
			},
			rosServiceURL: "https://stage.stg.nvidia.com",
			envSetup: map[string]string{
				stageTokenURLEnvVarKey: "https://custom.stage.token.url",
				stageJWKSURLEnvVarKey:  "https://custom.stage.jwks.url",
			},
			want: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "test-client-id",
				SSAClientSecretKey:      "test-secret-key",
				TokenURL:                "https://custom.stage.token.url",
				SSATokenScope:           "instance_status",
				SSAPublicKeysetEndpoint: "https://custom.stage.jwks.url",
			},
		},
		{
			name: "prod environment - with env vars",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "default-client-id",
				SSAClientSecretKey: "default-secret-key",
			},
			rosServiceURL: "https://prod.nvidia.com",
			envSetup: map[string]string{
				ssaClientIDEnvVarKey:        "env-client-id",
				ssaClientSecretKeyEnvVarKey: "env-secret-key",
			},
			want: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "env-client-id",
				SSAClientSecretKey:      "env-secret-key",
				TokenURL:                prodTokenURL,
				SSATokenScope:           "instance_status",
				SSAPublicKeysetEndpoint: prodJWKSURL,
			},
		},
		{
			name: "prod environment - with token URL overrides",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:        "test-client-id",
				SSAClientSecretKey: "test-secret-key",
			},
			rosServiceURL: "https://prod.nvidia.com",
			envSetup: map[string]string{
				prodTokenURLEnvVarKey: "https://custom.prod.token.url",
				prodJWKSURLEnvVarKey:  "https://custom.prod.jwks.url",
			},
			want: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "test-client-id",
				SSAClientSecretKey:      "test-secret-key",
				TokenURL:                "https://custom.prod.token.url",
				SSATokenScope:           "instance_status",
				SSAPublicKeysetEndpoint: "https://custom.prod.jwks.url",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment variables
			for k, v := range tt.envSetup {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			got := getROSTokenFetcherOptions(tt.opts, tt.rosServiceURL)

			assert.Equal(t, tt.want.SSAClientID, got.SSAClientID)
			assert.Equal(t, tt.want.SSAClientSecretKey, got.SSAClientSecretKey)
			assert.Equal(t, tt.want.TokenURL, got.TokenURL)
			assert.Equal(t, tt.want.SSATokenScope, got.SSATokenScope)
			assert.Equal(t, tt.want.SSAPublicKeysetEndpoint, got.SSAPublicKeysetEndpoint)
		})
	}
}

func TestGetFNDSTokenFetcherOptions_WithEnvVars(t *testing.T) {
	// Save original env vars
	origClientID := os.Getenv(ssaClientIDEnvVarKey)
	origSecretKey := os.Getenv(ssaClientSecretKeyEnvVarKey)
	origSecretsFile := os.Getenv(ssaClientSecretsEnvFileEnvVarKey)

	// Restore env vars after test
	defer func() {
		os.Setenv(ssaClientIDEnvVarKey, origClientID)
		os.Setenv(ssaClientSecretKeyEnvVarKey, origSecretKey)
		os.Setenv(ssaClientSecretsEnvFileEnvVarKey, origSecretsFile)
	}()

	tests := []struct {
		name            string
		opts            nvcaauth.TokenFetcherOptions
		envVars         map[string]string
		wantClientID    string
		wantSecretKey   string
		wantSecretsFile string
	}{
		{
			name: "all env vars override options",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "opt-client",
				SSAClientSecretKey:      "opt-secret",
				SSAClientSecretsEnvFile: "/opt/secrets",
			},
			envVars: map[string]string{
				ssaClientIDEnvVarKey:             "env-client",
				ssaClientSecretKeyEnvVarKey:      "env-secret",
				ssaClientSecretsEnvFileEnvVarKey: "/env/secrets",
			},
			wantClientID:    "env-client",
			wantSecretKey:   "env-secret",
			wantSecretsFile: "/env/secrets",
		},
		{
			name: "fallback to options when env vars empty",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "opt-client",
				SSAClientSecretKey:      "opt-secret",
				SSAClientSecretsEnvFile: "/opt/secrets",
			},
			envVars: map[string]string{
				ssaClientIDEnvVarKey:             "",
				ssaClientSecretKeyEnvVarKey:      "",
				ssaClientSecretsEnvFileEnvVarKey: "",
			},
			wantClientID:    "opt-client",
			wantSecretKey:   "opt-secret",
			wantSecretsFile: "/opt/secrets",
		},
		{
			name: "mixed env vars and options",
			opts: nvcaauth.TokenFetcherOptions{
				SSAClientID:             "opt-client",
				SSAClientSecretKey:      "opt-secret",
				SSAClientSecretsEnvFile: "/opt/secrets",
			},
			envVars: map[string]string{
				ssaClientIDEnvVarKey:             "env-client",
				ssaClientSecretKeyEnvVarKey:      "",
				ssaClientSecretsEnvFileEnvVarKey: "/env/secrets",
			},
			wantClientID:    "env-client",
			wantSecretKey:   "opt-secret",
			wantSecretsFile: "/env/secrets",
		},
		{
			name: "all empty",
			opts: nvcaauth.TokenFetcherOptions{},
			envVars: map[string]string{
				ssaClientIDEnvVarKey:             "",
				ssaClientSecretKeyEnvVarKey:      "",
				ssaClientSecretsEnvFileEnvVarKey: "",
			},
			wantClientID:    "",
			wantSecretKey:   "",
			wantSecretsFile: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set env vars for test
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			result := getROSTokenFetcherOptions(tt.opts, "https://test.nvidia.com")

			// Verify env var overrides
			assert.Equal(t, tt.wantClientID, result.SSAClientID)
			assert.Equal(t, tt.wantSecretKey, result.SSAClientSecretKey)
			assert.Equal(t, tt.wantSecretsFile, result.SSAClientSecretsEnvFile)

			// Verify other fields remain unchanged
			assert.Equal(t, "instance_status", result.SSATokenScope)
			assert.Equal(t, prodTokenURL, result.TokenURL)
			assert.Equal(t, prodJWKSURL, result.SSAPublicKeysetEndpoint)
			assert.Equal(t, tt.opts.SSATokenFetchFailureThreshold, result.SSATokenFetchFailureThreshold)
		})
	}
}
