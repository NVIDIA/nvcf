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

package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTokenFetcher(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		opts    TokenFetcherOptions
		setup   func() error
		cleanup func()
		wantErr bool
	}{
		{
			name: "success with OAuth client ID and secret",
			opts: TokenFetcherOptions{
				OAuthClientID:        "test-client",
				OAuthClientSecretKey: "test-secret",
				TokenURL:             "https://test.com/token",
				OAuthTokenScope:      "test.scope",
			},
			wantErr: false,
		},
		{
			name: "success with OAuth client ID and secrets file",
			opts: TokenFetcherOptions{
				OAuthClientID:             "test-client",
				OAuthClientSecretsEnvFile: filepath.Join(os.TempDir(), "test-secrets"),
				TokenURL:                  "https://test.com/token",
				OAuthTokenScope:           "test.scope",
			},
			setup: func() error {
				return os.WriteFile(filepath.Join(os.TempDir(), "test-secrets"), []byte("test-secret"), 0600)
			},
			cleanup: func() {
				os.Remove(filepath.Join(os.TempDir(), "test-secrets"))
			},
			wantErr: false,
		},
		{
			name: "success with NGC service API key file",
			opts: TokenFetcherOptions{
				NGCServiceAPIKeyFile: filepath.Join(os.TempDir(), "ngc-key"),
			},
			setup: func() error {
				return os.WriteFile(filepath.Join(os.TempDir(), "ngc-key"), []byte("test-key"), 0600)
			},
			cleanup: func() {
				os.Remove(filepath.Join(os.TempDir(), "ngc-key"))
			},
			wantErr: false,
		},
		{
			name: "success with NGC service API key",
			opts: TokenFetcherOptions{
				NGCServiceAPIKey: "test-key",
			},
			wantErr: false,
		},
		{
			name:    "error - no credentials provided",
			opts:    TokenFetcherOptions{},
			wantErr: true,
		},
		{
			name: "error - OAuth client ID without secret or file",
			opts: TokenFetcherOptions{
				OAuthClientID: "test-client",
			},
			wantErr: true,
		},
		{
			name: "error - invalid secrets file path",
			opts: TokenFetcherOptions{
				OAuthClientID:             "test-client",
				OAuthClientSecretsEnvFile: "/nonexistent/path",
				TokenURL:                  "https://test.com/token",
				OAuthTokenScope:           "test.scope",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				err := tt.setup()
				require.NoError(t, err)
			}

			if tt.cleanup != nil {
				defer tt.cleanup()
			}

			fetcher, healthCheck, err := NewTokenFetcher(ctx, "foo", tt.opts)
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

func TestTokenFetcherOptions_Validation(t *testing.T) {
	tests := []struct {
		name    string
		opts    TokenFetcherOptions
		wantErr bool
	}{
		{
			name: "valid OAuth options",
			opts: TokenFetcherOptions{
				OAuthClientID:        "test-client",
				OAuthClientSecretKey: "test-secret",
				TokenURL:             "https://test.com/token",
				OAuthTokenScope:      "test.scope",
			},
			wantErr: false,
		},
		{
			name: "valid NGC options",
			opts: TokenFetcherOptions{
				NGCServiceAPIKey: "test-key",
			},
			wantErr: false,
		},
		{
			name: "missing required OAuth fields",
			opts: TokenFetcherOptions{
				OAuthClientID: "test-client",
				// Missing secret and secrets file
			},
			wantErr: true,
		},
		{
			name:    "empty options",
			opts:    TokenFetcherOptions{},
			wantErr: true,
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := NewTokenFetcher(ctx, "foo", tt.opts)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
