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

func TestNewPSATTokenFetcher_EmptyPath(t *testing.T) {
	fetcher, err := NewPSATTokenFetcher("")
	require.Error(t, err)
	assert.Nil(t, fetcher)
	assert.Contains(t, err.Error(), "token file path is required")
}

func TestNewPSATTokenFetcher_ValidPath(t *testing.T) {
	fetcher, err := NewPSATTokenFetcher("/some/path/token")
	require.NoError(t, err)
	assert.NotNil(t, fetcher)
}

func TestPSATTokenFetcher_ReadValidToken(t *testing.T) {
	tokenContent := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test-payload.signature"
	tokenFile := writeTestTokenFile(t, tokenContent)

	fetcher, err := NewPSATTokenFetcher(tokenFile)
	require.NoError(t, err)

	token, err := fetcher.FetchToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, tokenContent, token)
}

func TestPSATTokenFetcher_FileNotExists(t *testing.T) {
	fetcher, err := NewPSATTokenFetcher("/nonexistent/path/token")
	require.NoError(t, err)

	token, err := fetcher.FetchToken(context.Background())
	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "read PSAT token")
}

func TestPSATTokenFetcher_EmptyFile(t *testing.T) {
	tokenFile := writeTestTokenFile(t, "")

	fetcher, err := NewPSATTokenFetcher(tokenFile)
	require.NoError(t, err)

	token, err := fetcher.FetchToken(context.Background())
	require.NoError(t, err)
	assert.Empty(t, token)
}

func TestPSATTokenFetcher_TrimsWhitespace(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "trailing newline",
			content:  "my-token\n",
			expected: "my-token",
		},
		{
			name:     "trailing carriage return and newline",
			content:  "my-token\r\n",
			expected: "my-token",
		},
		{
			name:     "leading and trailing spaces",
			content:  "  my-token  ",
			expected: "my-token",
		},
		{
			name:     "multiple trailing newlines",
			content:  "my-token\n\n\n",
			expected: "my-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenFile := writeTestTokenFile(t, tt.content)

			fetcher, err := NewPSATTokenFetcher(tokenFile)
			require.NoError(t, err)

			token, err := fetcher.FetchToken(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.expected, token)
		})
	}
}

func TestPSATTokenFetcher_ReReadsOnEachCall(t *testing.T) {
	tokenFile := writeTestTokenFile(t, "token-v1")

	fetcher, err := NewPSATTokenFetcher(tokenFile)
	require.NoError(t, err)

	token, err := fetcher.FetchToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "token-v1", token)

	// Simulate kubelet token rotation
	require.NoError(t, os.WriteFile(tokenFile, []byte("token-v2"), 0600))

	token, err = fetcher.FetchToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "token-v2", token)
}

func TestPSATTokenFetcher_ImplementsTokenFetcher(t *testing.T) {
	fetcher, err := NewPSATTokenFetcher("/some/path")
	require.NoError(t, err)
	var _ TokenFetcher = fetcher
}

func writeTestTokenFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}
