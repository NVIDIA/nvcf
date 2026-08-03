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
package credentials

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBearerTokenCredentials(t *testing.T) {
	logger, _ := zap.NewDevelopmentConfig().Build()
	zap.ReplaceGlobals(logger)

	// Create a temporary file with test credentials
	dir := t.TempDir()

	secretsPath := filepath.Join(dir, "test-secrets.json")
	tokenKey := "test_api_token"
	initialToken := "initial-token-value"

	// Write initial token
	err := writeSecretsFile(secretsPath, tokenKey, initialToken)
	require.NoError(t, err)

	// Create credentials
	creds, err := NewBearerTokenCredentials(secretsPath, tokenKey, true)
	require.NoError(t, err)
	defer creds.Close()

	// Test initial token
	metadata, err := creds.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+initialToken, metadata["authorization"])

	// Test security setting
	assert.False(t, creds.RequireTransportSecurity())

	// Test token update
	updatedToken := "updated-token-value"
	err = writeSecretsFile(secretsPath, tokenKey, updatedToken)
	require.NoError(t, err)

	// Wait for the watcher to detect changes
	time.Sleep(100 * time.Millisecond)

	// Verify token was updated
	metadata, err = creds.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+updatedToken, metadata["authorization"])
}

func TestNewBearerTokenCredentialsError(t *testing.T) {
	// Test with non-existent file
	_, err := NewBearerTokenCredentials("/non/existent/path.json", "token_key", false)
	assert.Error(t, err)

	// Create a temporary file with invalid token
	dir := t.TempDir()

	secretsPath := filepath.Join(dir, "test-secrets.json")

	// Write file with missing token
	err = writeSecretsFile(secretsPath, "different_key", "some-value")
	require.NoError(t, err)

	// Should error because token key doesn't exist
	_, err = NewBearerTokenCredentials(secretsPath, "token_key", false)
	assert.Error(t, err)
}

func TestCloseTwice(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "test-secrets.json")
	tokenKey := "test_api_token"

	err := writeSecretsFile(secretsPath, tokenKey, "token-value")
	require.NoError(t, err)

	creds, err := NewBearerTokenCredentials(secretsPath, tokenKey, true)
	require.NoError(t, err)

	assert.NoError(t, creds.Close())
	assert.NoError(t, creds.Close())
}

func writeSecretsFile(path, tokenKey, tokenValue string) error {
	secrets := map[string]any{
		tokenKey: tokenValue,
	}
	data, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	err = os.WriteFile(path, data, 0600)
	if err != nil {
		return err
	}
	return SyncFS()
}
