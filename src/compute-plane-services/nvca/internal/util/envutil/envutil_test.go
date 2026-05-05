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

package envutil

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

func TestApplyEnvOverrides(t *testing.T) {
	tests := []struct {
		name      string
		envB64    string
		overrides map[string]string
		wantEnvs  map[string]string
		wantErr   bool
	}{
		{
			name:      "empty overrides returns original",
			envB64:    encodeTestEnv(map[string]string{"FOO": "bar"}),
			overrides: nil,
			wantEnvs:  map[string]string{"FOO": "bar"},
			wantErr:   false,
		},
		{
			name:      "empty env with overrides",
			envB64:    "",
			overrides: map[string]string{"INIT_CONTAINER": "custom:v1"},
			wantEnvs:  map[string]string{"INIT_CONTAINER": "custom:v1"},
			wantErr:   false,
		},
		{
			name:      "override existing key",
			envB64:    encodeTestEnv(map[string]string{"INIT_CONTAINER": "original:v1", "OTHER": "value"}),
			overrides: map[string]string{"INIT_CONTAINER": "custom:v2"},
			wantEnvs:  map[string]string{"INIT_CONTAINER": "custom:v2", "OTHER": "value"},
			wantErr:   false,
		},
		{
			name:      "add new key",
			envB64:    encodeTestEnv(map[string]string{"EXISTING": "value"}),
			overrides: map[string]string{"NEW_KEY": "new_value"},
			wantEnvs:  map[string]string{"EXISTING": "value", "NEW_KEY": "new_value"},
			wantErr:   false,
		},
		{
			name:      "multiple overrides",
			envB64:    encodeTestEnv(map[string]string{"A": "1", "B": "2", "C": "3"}),
			overrides: map[string]string{"A": "10", "D": "4"},
			wantEnvs:  map[string]string{"A": "10", "B": "2", "C": "3", "D": "4"},
			wantErr:   false,
		},
		{
			name:      "invalid base64 input",
			envB64:    "not-valid-base64!!!",
			overrides: map[string]string{"KEY": "value"},
			wantErr:   true,
		},
		{
			name:      "value containing equals sign",
			envB64:    encodeTestEnv(map[string]string{"TOKEN": "abc=def=ghi"}),
			overrides: map[string]string{"OTHER": "value"},
			wantEnvs:  map[string]string{"TOKEN": "abc=def=ghi", "OTHER": "value"},
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ApplyEnvOverrides(tt.envB64, tt.overrides)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Decode result using common decoder and verify
			gotEnvs, err := common.DecodeEnvironmentB64(result, common.EnvDecoderText)
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnvs, gotEnvs)
		})
	}
}

// encodeTestEnv encodes env vars in the text format: KEY=value\n
func encodeTestEnv(envs map[string]string) string {
	return EncodeEnvB64(envs)
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		envs map[string]string
	}{
		{
			name: "simple key-value",
			envs: map[string]string{"FOO": "bar"},
		},
		{
			name: "multiple keys",
			envs: map[string]string{"A": "1", "B": "2", "C": "3"},
		},
		{
			name: "value with equals",
			envs: map[string]string{"TOKEN": "abc=123=xyz"},
		},
		{
			name: "container image references",
			envs: map[string]string{
				"INIT_CONTAINER":  "nvcr.io/nvcf-core/init:v1.0",
				"UTILS_CONTAINER": "nvcr.io/nvcf-core/utils:v2.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeEnvB64(tt.envs)
			decoded, err := common.DecodeEnvironmentB64(encoded, common.EnvDecoderText)
			require.NoError(t, err)
			assert.Equal(t, tt.envs, decoded)
		})
	}
}

func TestRawEnvB64Format(t *testing.T) {
	// Test that we can decode actual EnvironmentB64 format from ICMS
	rawEnv := "FOO=bar\nBAZ=qux\n"
	envB64 := base64.StdEncoding.EncodeToString([]byte(rawEnv))

	result, err := ApplyEnvOverrides(envB64, map[string]string{"NEW": "value"})
	require.NoError(t, err)

	decoded, err := common.DecodeEnvironmentB64(result, common.EnvDecoderText)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"FOO": "bar", "BAZ": "qux", "NEW": "value"}, decoded)
}
