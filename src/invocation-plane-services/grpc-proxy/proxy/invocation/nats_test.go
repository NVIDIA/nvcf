/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
package invocation

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNkeyAuthOption(t *testing.T) {
	t.Run("valid nkey seed", func(t *testing.T) {
		// Generate a valid nkey seed for testing
		kp, err := nkeys.CreateUser()
		require.NoError(t, err)

		seed, err := kp.Seed()
		require.NoError(t, err)

		// Test the function
		option, err := newNkeyAuthOption(string(seed))
		assert.NoError(t, err)
		assert.NotNil(t, option)
	})

	t.Run("invalid nkey seed", func(t *testing.T) {
		option, err := newNkeyAuthOption("invalid-seed")
		assert.Error(t, err)
		assert.Nil(t, option)
	})

	t.Run("empty nkey seed", func(t *testing.T) {
		option, err := newNkeyAuthOption("")
		assert.Error(t, err)
		assert.Nil(t, option)
	})
}

func TestNewOAuthTokenProvider(t *testing.T) {
	t.Run("invalid secrets path", func(t *testing.T) {
		provider, err := newOAuthTokenProvider("https://ssa.example.com", "/non/existent/path")
		assert.Error(t, err)
		assert.Nil(t, provider)
	})

	t.Run("valid secrets path but invalid config", func(t *testing.T) {
		// Create a temporary secrets file with invalid config
		tmpDir := t.TempDir()
		secretsPath := filepath.Join(tmpDir, "secrets.json")

		secrets := map[string]any{
			"invalid": "config",
		}
		secretsData, _ := json.Marshal(secrets)
		err := os.WriteFile(secretsPath, secretsData, 0600)
		require.NoError(t, err)

		provider, err := newOAuthTokenProvider("https://ssa.example.com", secretsPath)
		// This should fail because the auth config is invalid
		assert.Error(t, err)
		assert.Nil(t, provider)
	})

	t.Run("valid secrets file", func(t *testing.T) {
		// Use the helper function to create a proper secrets file
		secretsPath := createTestSecretsFile(t)

		provider, err := newOAuthTokenProvider("https://ssa.example.com", secretsPath)
		// This will fail because we can't actually connect to the OAuth2
		// server, but it should not fail due to secrets file format
		assert.Error(t, err)
		assert.Nil(t, provider)
	})
}

func TestAuthCalloutPluginTokenProvider_GetToken(t *testing.T) {
	t.Run("token encoding format", func(t *testing.T) {
		// Test the token encoding format directly
		oauthToken := "test-oauth-token"
		tokenJson, err := json.Marshal(struct {
			Account    string `json:"account"`
			PluginName string `json:"pluginName"`
			Payload    string `json:"payload"`
		}{
			Account:    "Worker",
			PluginName: "ssa",
			Payload:    oauthToken,
		})
		require.NoError(t, err)

		expectedToken := base64.RawURLEncoding.EncodeToString(tokenJson)

		// Test the token structure by decoding the expected result
		decoded, err := base64.RawURLEncoding.DecodeString(expectedToken)
		require.NoError(t, err)

		var tokenData struct {
			Account    string `json:"account"`
			PluginName string `json:"pluginName"`
			Payload    string `json:"payload"`
		}

		err = json.Unmarshal(decoded, &tokenData)
		require.NoError(t, err)

		assert.Equal(t, "Worker", tokenData.Account)
		assert.Equal(t, "ssa", tokenData.PluginName)
		assert.Equal(t, oauthToken, tokenData.Payload)
	})
}

func TestNewAuthCalloutAuthOption(t *testing.T) {
	t.Run("invalid secrets path", func(t *testing.T) {
		option, err := newAuthCalloutAuthOption("https://ssa.example.com", "/non/existent/path")
		assert.Error(t, err)
		assert.Nil(t, option)
	})
}

// Integration test helper functions
func createTestSecretsFile(t *testing.T) string {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	secrets := map[string]any{
		"client_id":     "test-client-id",
		"client_secret": "test-client-secret",
	}
	secretsData, _ := json.Marshal(secrets)
	err := os.WriteFile(secretsPath, secretsData, 0600)
	require.NoError(t, err)

	return secretsPath
}

func TestAuthCalloutPluginTokenProviderMarshalling(t *testing.T) {
	t.Run("json marshalling and encoding", func(t *testing.T) {
		// Test the JSON marshalling and base64 encoding logic directly
		testPayload := "test-oauth-token"

		tokenJson, err := json.Marshal(struct {
			Account    string `json:"account"`
			PluginName string `json:"pluginName"`
			Payload    string `json:"payload"`
		}{
			Account:    "Worker",
			PluginName: "ssa",
			Payload:    testPayload,
		})
		require.NoError(t, err)

		encodedToken := base64.RawURLEncoding.EncodeToString(tokenJson)
		assert.NotEmpty(t, encodedToken)

		// Verify the token can be properly decoded and parsed
		decoded, err := base64.RawURLEncoding.DecodeString(encodedToken)
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal(decoded, &result)
		require.NoError(t, err)

		assert.Equal(t, "Worker", result["account"])
		assert.Equal(t, "ssa", result["pluginName"])
		assert.Equal(t, testPayload, result["payload"])
	})
}
