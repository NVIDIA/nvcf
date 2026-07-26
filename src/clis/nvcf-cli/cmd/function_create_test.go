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

package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runFunctionCreateWithResponse(t *testing.T, response string, demo bool) (string, error, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v2/nvcf/functions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(response))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	testDir := t.TempDir()
	t.Setenv("HOME", testDir)
	t.Chdir(testDir)
	inputFile := filepath.Join(testDir, "create-function.json")
	require.NoError(t, os.WriteFile(inputFile, []byte(`{
		"name": "function-task-test",
		"containerImage": "registry.example/function:test",
		"inferenceUrl": "/echo",
		"inferencePort": 8000,
		"healthProtocol": "HTTP",
		"healthUri": "/health",
		"healthPort": 8000,
		"healthTimeout": "PT30S",
		"healthExpectedStatus": 200
	}`), 0600))

	oldCreateFlags := createFlags
	oldCfgFile := cfgFile
	oldStateManager := configStateManager
	oldStateManagerKey := configStateManagerKey
	t.Cleanup(func() {
		createFlags = oldCreateFlags
		cfgFile = oldCfgFile
		configStateManager = oldStateManager
		configStateManagerKey = oldStateManagerKey
		jsonOutput = false
		logging.SetJSONOutput(false)
		viper.Reset()
	})

	viper.Reset()
	viper.Set("base_http_url", server.URL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("token", "test-admin-token")
	viper.Set("demo", demo)
	cfgFile = filepath.Join(testDir, "workflow.yaml")
	configStateManager = nil
	configStateManagerKey = ""
	createFlags.inputFile = inputFile
	jsonOutput = true
	logging.SetJSONOutput(true)

	var runErr error
	output := captureStdout(t, func() {
		runErr = runCreate(createCmd, nil)
	})
	return output, runErr, testDir
}

func runTaskCreateWithResponse(t *testing.T, response string) (string, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/nvct/tasks", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(response))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	testDir := t.TempDir()
	t.Setenv("HOME", testDir)
	inputFile := filepath.Join(testDir, "create-task.json")
	require.NoError(t, os.WriteFile(inputFile, []byte(`{
		"name": "task-test",
		"gpuSpecification": {
			"gpu": "H100",
			"instanceType": "NCP.GPU.H100_8x"
		},
		"containerImage": "registry.example/task:test",
		"resultHandlingStrategy": "NONE"
	}`), 0600))

	oldTaskCreateFlags := taskCreateFlags
	oldCfgFile := cfgFile
	oldStateManager := configStateManager
	oldStateManagerKey := configStateManagerKey
	t.Cleanup(func() {
		taskCreateFlags = oldTaskCreateFlags
		cfgFile = oldCfgFile
		configStateManager = oldStateManager
		configStateManagerKey = oldStateManagerKey
		jsonOutput = false
		logging.SetJSONOutput(false)
		viper.Reset()
	})

	viper.Reset()
	viper.Set("base_nvct_url", server.URL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("nvct_api_key", "test-task-api-key")
	cfgFile = filepath.Join(testDir, "workflow.yaml")
	configStateManager = nil
	configStateManagerKey = ""
	taskCreateFlags.inputFile = inputFile
	jsonOutput = true
	logging.SetJSONOutput(true)

	var runErr error
	output := captureStdout(t, func() {
		runErr = runTaskCreate(taskCreateCmd, nil)
	})
	return output, runErr
}

func TestFunctionCreateJSONOutputFromInputFile(t *testing.T) {
	output, err, testDir := runFunctionCreateWithResponse(t, `{
		"function": {
			"id": "function-test",
			"versionId": "version-test",
			"name": "function-task-test",
			"status": "INACTIVE"
		}
	}`, true)
	require.NoError(t, err)

	var parsed client.CreateFunctionResponse
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	assert.Equal(t, "function-test", parsed.Function.ID)
	assert.Equal(t, "version-test", parsed.Function.VersionID)
	assert.Equal(t, "function-task-test", parsed.Function.Name)
	assert.DirExists(t, filepath.Join(testDir, "version-test_demo"))
	assert.FileExists(t, filepath.Join(testDir, "version-test_demo", "deploy.json"))
}

func TestFunctionCreateRejectsEmptyIDs(t *testing.T) {
	tests := []struct {
		name       string
		functionID string
		versionID  string
	}{
		{name: "empty function ID", functionID: "", versionID: "version-test"},
		{name: "empty version ID", functionID: "function-test", versionID: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := json.Marshal(client.CreateFunctionResponse{
				Function: client.FunctionData{
					ID:        test.functionID,
					VersionID: test.versionID,
					Name:      "function-task-test",
				},
			})
			require.NoError(t, err)

			output, runErr, _ := runFunctionCreateWithResponse(t, string(response), false)
			require.Error(t, runErr)
			assert.Contains(t, runErr.Error(), "did not include a function ID and version ID")
			assert.Empty(t, output)
			assert.False(t, HasCurrentFunction())
		})
	}
}

func TestTaskCreateRejectsEmptyID(t *testing.T) {
	output, err := runTaskCreateWithResponse(t, `{
		"task": {
			"id": "",
			"name": "task-test",
			"status": "QUEUED"
		}
	}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not include a task ID")
	assert.Empty(t, output)
	assert.False(t, HasCurrentTask())
}
