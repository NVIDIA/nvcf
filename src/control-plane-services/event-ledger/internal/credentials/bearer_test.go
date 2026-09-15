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
package credentials

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func writeSecretsFile(t *testing.T, path string, contents map[string]any) {
	t.Helper()
	data, err := json.Marshal(contents)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
}

func TestReadTokenFromFile(t *testing.T) {
	tests := []struct {
		name      string
		contents  map[string]any
		tokenKey  string
		wantToken string
		wantErr   bool
	}{
		{
			name:      "reads token successfully",
			contents:  map[string]any{"my-token": "abc123"},
			tokenKey:  "my-token",
			wantToken: "abc123",
		},
		{
			name:     "key not found",
			contents: map[string]any{"other-key": "value"},
			tokenKey: "my-token",
			wantErr:  true,
		},
		{
			name:     "empty token value",
			contents: map[string]any{"my-token": ""},
			tokenKey: "my-token",
			wantErr:  true,
		},
		{
			name:     "wrong type",
			contents: map[string]any{"my-token": 12345},
			tokenKey: "my-token",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets.json")
			writeSecretsFile(t, path, tc.contents)

			token, err := ReadTokenFromFile(path, tc.tokenKey)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantToken, token)
			}
		})
	}

	t.Run("file does not exist", func(t *testing.T) {
		_, err := ReadTokenFromFile("/nonexistent/secrets.json", "key")
		assert.Error(t, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secrets.json")
		require.NoError(t, os.WriteFile(path, []byte("not-json"), 0600))
		_, err := ReadTokenFromFile(path, "key")
		assert.Error(t, err)
	})
}

func TestNewBearerTokenReader(t *testing.T) {
	t.Run("reads initial token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secrets.json")
		writeSecretsFile(t, path, map[string]any{"token": "initial"})

		r, err := NewBearerTokenReader(path, "token")
		require.NoError(t, err)
		defer r.Close()

		assert.Equal(t, "initial", r.Token())
	})

	t.Run("error when file missing", func(t *testing.T) {
		_, err := NewBearerTokenReader("/nonexistent/secrets.json", "token")
		assert.Error(t, err)
	})

	t.Run("error when key missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secrets.json")
		writeSecretsFile(t, path, map[string]any{"other": "value"})

		_, err := NewBearerTokenReader(path, "token")
		assert.Error(t, err)
	})

	t.Run("refreshes token on file change", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secrets.json")
		writeSecretsFile(t, path, map[string]any{"token": "initial"})

		r, err := NewBearerTokenReader(path, "token")
		require.NoError(t, err)
		defer r.Close()

		assert.Equal(t, "initial", r.Token())

		writeSecretsFile(t, path, map[string]any{"token": "updated"})

		// Give the watcher goroutine time to pick up the change.
		assert.Eventually(t, func() bool {
			return r.Token() == "updated"
		}, 2*time.Second, 50*time.Millisecond)
	})
}
