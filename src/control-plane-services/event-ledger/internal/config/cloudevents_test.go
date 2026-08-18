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

func TestValidateCloudEventsConfig_Production(t *testing.T) {
	tests := []struct {
		name        string
		cfg         CloudEventsConfig
		expectedErr error
	}{
		{
			name: "disabled - skips validation",
			cfg: CloudEventsConfig{
				Enabled: false,
				// Missing required fields, but should pass because disabled
			},
			expectedErr: nil,
		},
		{
			name: "valid config without auth",
			cfg: CloudEventsConfig{
				Enabled:  true,
				Endpoint: "https://example.com/events",
			},
			expectedErr: nil,
		},
		{
			name: "valid config with creds_file",
			cfg: CloudEventsConfig{
				Enabled:                    true,
				Endpoint:                   "https://example.com/events",
				TokenEndpoint:              "https://example.com/token",
				CredentialsFile:            "/path/to/creds.yaml",
				CredentialsRefreshInterval: 300,
			},
			expectedErr: nil,
		},
		{
			name: "missing endpoint",
			cfg: CloudEventsConfig{
				Enabled: true,
			},
			expectedErr: ErrMissingCEEndpoint,
		},
		{
			name: "token auth: missing credentials",
			cfg: CloudEventsConfig{
				Enabled:       true,
				Endpoint:      "https://example.com/events",
				TokenEndpoint: "https://example.com/token",
			},
			expectedErr: ErrMissingCECredentials,
		},
		{
			name: "invalid creds_refresh_interval (zero) with creds_file",
			cfg: CloudEventsConfig{
				Enabled:                    true,
				Endpoint:                   "https://example.com/events",
				TokenEndpoint:              "https://example.com/token",
				CredentialsFile:            "/path/to/creds.yaml",
				CredentialsRefreshInterval: 0,
			},
			expectedErr: ErrInvalidCECredsRefreshInterval,
		},
		{
			name: "invalid creds_refresh_interval (negative) with creds_file",
			cfg: CloudEventsConfig{
				Enabled:                    true,
				Endpoint:                   "https://example.com/events",
				TokenEndpoint:              "https://example.com/token",
				CredentialsFile:            "/path/to/creds.yaml",
				CredentialsRefreshInterval: -1,
			},
			expectedErr: ErrInvalidCECredsRefreshInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCloudEventsConfig(tt.cfg)
			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCloudEventsConfig_Staging(t *testing.T) {
	tests := []struct {
		name        string
		cfg         CloudEventsConfig
		expectedErr error
	}{
		{
			name: "valid with inline credentials",
			cfg: CloudEventsConfig{
				Enabled:       true,
				Endpoint:      "https://example.com/events",
				TokenEndpoint: "https://example.com/token",
				ClientID:      "client-id",
				ClientSecret:  "client-secret",
			},
			expectedErr: nil,
		},
		{
			name: "token auth: missing client_id",
			cfg: CloudEventsConfig{
				Enabled:       true,
				Endpoint:      "https://example.com/events",
				TokenEndpoint: "https://example.com/token",
				ClientSecret:  "client-secret",
			},
			expectedErr: ErrMissingCECredentials,
		},
		{
			name: "token auth: missing client_secret",
			cfg: CloudEventsConfig{
				Enabled:       true,
				Endpoint:      "https://example.com/events",
				TokenEndpoint: "https://example.com/token",
				ClientID:      "client-id",
			},
			expectedErr: ErrMissingCECredentials,
		},
		{
			name: "token auth: missing both inline credentials",
			cfg: CloudEventsConfig{
				Enabled:       true,
				Endpoint:      "https://example.com/events",
				TokenEndpoint: "https://example.com/token",
			},
			expectedErr: ErrMissingCECredentials,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCloudEventsConfig(tt.cfg)
			if tt.expectedErr != nil {
				require.Error(t, err)
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCloudEventsConfig_UseCredentialsFile(t *testing.T) {
	tests := []struct {
		name     string
		cfg      CloudEventsConfig
		expected bool
	}{
		{
			name: "with creds_file",
			cfg: CloudEventsConfig{
				CredentialsFile: "/path/to/creds.yaml",
			},
			expected: true,
		},
		{
			name: "without creds_file",
			cfg: CloudEventsConfig{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
			},
			expected: false,
		},
		{
			name:     "empty config",
			cfg:      CloudEventsConfig{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.cfg.UseCredentialsFile())
		})
	}
}
