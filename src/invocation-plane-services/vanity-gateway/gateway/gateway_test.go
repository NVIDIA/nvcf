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

package gateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewNVCFGatewayRequiresAPIEndpoint(t *testing.T) {
	gateway, err := NewNVCFGateway(nil, Config{MappingPath: "config.yaml"})

	require.Nil(t, gateway)
	require.Error(t, err)
	require.Contains(t, err.Error(), "NVCF_API_ENDPOINT is required")
}

func TestParseMappingLoadTimeout(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantTimeout time.Duration
		wantErr     string
	}{
		{
			name:        "empty uses default",
			raw:         "",
			wantTimeout: 0,
		},
		{
			name:        "duration",
			raw:         "2m",
			wantTimeout: 2 * time.Minute,
		},
		{
			name:    "invalid",
			raw:     "120",
			wantErr: "MAPPING_LOAD_TIMEOUT must be a valid duration",
		},
		{
			name:    "zero",
			raw:     "0s",
			wantErr: "MAPPING_LOAD_TIMEOUT must be greater than 0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMappingLoadTimeout(tc.raw)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantTimeout, got)
		})
	}
}
