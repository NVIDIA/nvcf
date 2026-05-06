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

package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	logger.Init()
	os.Exit(m.Run())
}

func TestCheckSecretsChanges(t *testing.T) {
	secretFilePath := "test-secrets.json"
	_, err := os.Create(secretFilePath)
	assert.NoError(t, err)
	defer os.Remove(secretFilePath)

	err = os.WriteFile(secretFilePath, []byte("{}"), 0644)
	assert.NoError(t, err)

	lastContent := []byte("{}")
	fileInfo, _ := os.Stat(secretFilePath)
	lastModTime := fileInfo.ModTime()

	// Initial check, no change
	changed, gotContent, gotModTime, err := CheckSecretsChanges(secretFilePath, lastContent, lastModTime)
	assert.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, lastContent, gotContent)
	assert.Equal(t, fileInfo.ModTime(), gotModTime)

	// Simulate a modification to the file
	time.Sleep(10 * time.Millisecond)
	newJSON := []byte("{\"new_key\": \"new_value\"}")
	err = os.WriteFile(secretFilePath, newJSON, 0644)
	assert.NoError(t, err)
	changed, gotContent, gotModTime, err = CheckSecretsChanges(secretFilePath, lastContent, lastModTime)
	assert.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, newJSON, gotContent)
	assert.NotEqual(t, lastModTime, gotModTime)

	// Simulate no content change but modtime update
	lastContent = gotContent
	lastModTime = gotModTime
	time.Sleep(10 * time.Millisecond)
	err = os.Chtimes(secretFilePath, time.Now(), time.Now())
	assert.NoError(t, err)
	changed, _, _, err = CheckSecretsChanges(secretFilePath, lastContent, lastModTime)
	assert.NoError(t, err)
	assert.False(t, changed)
}

func TestValidateAndReadSecretFile(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test-secrets-*.json")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	validJSON := `{"key": "value"}`
	os.WriteFile(tempFile.Name(), []byte(validJSON), 0644)

	content, err := validateAndReadSecretFile(tempFile.Name(), 5*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, validJSON, string(content))

	nonExistentFile := "non-existent-file.json"
	_, err = validateAndReadSecretFile(nonExistentFile, 2*time.Second)
	assert.Error(t, err)
}

func TestRunSecretsExtractor(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-output")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	tempFile, err := os.CreateTemp("", "test-secrets-*.json")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	secrets := map[string]interface{}{
		"key1": "value1",
		"key2": map[string]interface{}{
			"subkey1": "subvalue1",
			"subkey2": "subvalue2",
		},
	}
	secretsJSON, _ := json.Marshal(secrets)
	os.WriteFile(tempFile.Name(), secretsJSON, 0644)

	err = RunSecretsExtractor(tempFile.Name(), tempDir)
	assert.NoError(t, err)

	// Check if files are created correctly
	key1Path := filepath.Join(tempDir, "key1")
	key2Subkey1Path := filepath.Join(tempDir, "key2-subkey1")
	key2Subkey2Path := filepath.Join(tempDir, "key2-subkey2")

	key1Content, err := os.ReadFile(key1Path)
	assert.NoError(t, err)
	assert.Equal(t, "value1", string(key1Content))

	key2Subkey1Content, err := os.ReadFile(key2Subkey1Path)
	assert.NoError(t, err)
	assert.Equal(t, "subvalue1", string(key2Subkey1Content))

	key2Subkey2Content, err := os.ReadFile(key2Subkey2Path)
	assert.NoError(t, err)
	assert.Equal(t, "subvalue2", string(key2Subkey2Content))
}
