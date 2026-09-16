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
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Command Structure ------------------------------------------------------

func TestTaskCommandStructure(t *testing.T) {
	t.Run("top-level command is task", func(t *testing.T) {
		assert.Equal(t, "task", taskCmd.Use)
	})

	t.Run("registers all expected subcommands", func(t *testing.T) {
		expected := map[string]bool{
			"create":                  false,
			"list":                    false,
			"bulk":                    false,
			"get [taskId]":            false,
			"delete [taskId]":         false,
			"cancel [taskId]":         false,
			"events [taskId]":         false,
			"results [taskId]":        false,
			"update-secrets [taskId]": false,
		}
		for _, sub := range taskCmd.Commands() {
			if _, ok := expected[sub.Use]; ok {
				expected[sub.Use] = true
			}
		}
		for use, found := range expected {
			assert.Truef(t, found, "expected subcommand %q to be registered", use)
		}
	})

}

func TestTaskCreateCommandFlagSurface(t *testing.T) {
	mustHave := []string{
		"input-file", "name", "gpu", "instance-type", "backend", "clusters",
		"image", "container-args", "container-env",
		"tags", "description",
		"max-runtime", "max-queued", "termination-grace",
		"result-strategy", "results-location",
		"helm-chart",
		"logs-telemetry-id", "metrics-telemetry-id", "traces-telemetry-id",
		"models", "resources", "secrets",
	}
	for _, name := range mustHave {
		t.Run(name, func(t *testing.T) {
			assert.NotNilf(t, taskCreateCmd.Flags().Lookup(name), "task create should expose --%s", name)
		})
	}
}

func TestTaskGetCommandFlagSurface(t *testing.T) {
	assert.NotNil(t, taskGetCmd.Flags().Lookup("include-secrets"))
	assert.NotNil(t, taskGetCmd.Flags().Lookup("timeout"))
}

func TestTaskEventsCommandFlagSurface(t *testing.T) {
	assert.NotNil(t, taskEventsCmd.Flags().Lookup("limit"))
	assert.NotNil(t, taskEventsCmd.Flags().Lookup("cursor"))
	assert.NotNil(t, taskEventsCmd.Flags().Lookup("timeout"))
}

func TestTaskResultsCommandFlagSurface(t *testing.T) {
	assert.NotNil(t, taskResultsCmd.Flags().Lookup("limit"))
	assert.NotNil(t, taskResultsCmd.Flags().Lookup("cursor"))
	assert.NotNil(t, taskResultsCmd.Flags().Lookup("timeout"))
}

func TestNewTaskRequestContext(t *testing.T) {
	t.Run("rejects negative timeout", func(t *testing.T) {
		_, _, err := newTaskRequestContext(-1)
		require.Error(t, err)
	})

	t.Run("rejects timeout that overflows duration", func(t *testing.T) {
		timeoutSeconds := int64(maxTaskRequestTimeoutSeconds + 1)
		if int64(int(timeoutSeconds)) != timeoutSeconds {
			t.Skip("int cannot represent an overflowing time.Duration in seconds")
		}

		_, _, err := newTaskRequestContext(int(timeoutSeconds))
		require.Error(t, err)
	})

	t.Run("uses caller timeout", func(t *testing.T) {
		start := time.Now()
		ctx, cancel, err := newTaskRequestContext(1)
		require.NoError(t, err)
		defer cancel()

		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.WithinDuration(t, start.Add(time.Second), deadline, 100*time.Millisecond)
	})

	t.Run("keeps default timeout", func(t *testing.T) {
		ctx, cancel, err := newTaskRequestContext(0)
		require.NoError(t, err)
		defer cancel()

		_, ok := ctx.Deadline()
		assert.False(t, ok)
		assert.NoError(t, ctx.Err())
		assert.NotEqual(t, context.Canceled, ctx.Err())
	})
}

func TestTaskEventsTimeoutCancelsHTTPRequest(t *testing.T) {
	cancelObserved := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/nvct/tasks/task-test/events", r.URL.Path)
			select {
			case <-r.Context().Done():
				close(cancelObserved)
				return
			case <-time.After(5 * time.Second):
				t.Error("task events request was not canceled")
			}
		},
	))
	t.Cleanup(server.Close)

	oldCfgFile := cfgFile
	oldStateManager := configStateManager
	oldStateManagerKey := configStateManagerKey
	oldTaskEventsFlags := taskPaginationFlags
	timeoutFlag := taskEventsCmd.Flags().Lookup("timeout")
	require.NotNil(t, timeoutFlag)
	oldTimeoutValue := timeoutFlag.Value.String()
	oldTimeoutChanged := timeoutFlag.Changed
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		configStateManager = oldStateManager
		configStateManagerKey = oldStateManagerKey
		taskPaginationFlags = oldTaskEventsFlags
		assert.NoError(t, taskEventsCmd.Flags().Set("timeout", oldTimeoutValue))
		timeoutFlag.Changed = oldTimeoutChanged
		viper.Reset()
	})

	viper.Reset()
	viper.Set("base_nvct_url", server.URL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("api_key", "test-function-api-key")
	viper.Set("nvct_api_key", "test-task-api-key")
	cfgFile = filepath.Join(t.TempDir(), "workflow.yaml")
	configStateManager = nil
	configStateManagerKey = ""
	require.NoError(t, taskEventsCmd.Flags().Set("timeout", "1"))

	err := runTaskEvents(taskEventsCmd, []string{"task-test"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-cancelObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("task events request cancellation was not observed")
	}
}

func TestTaskResultsTimeoutCancelsHTTPRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/nvct/tasks/task-test/results", r.URL.Path)
			select {
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
				t.Error("task results request was not canceled")
			}
		},
	))
	t.Cleanup(server.Close)

	oldCfgFile := cfgFile
	oldStateManager := configStateManager
	oldStateManagerKey := configStateManagerKey
	oldTaskResultsFlags := taskResultsPaginationFlags
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		configStateManager = oldStateManager
		configStateManagerKey = oldStateManagerKey
		taskResultsPaginationFlags = oldTaskResultsFlags
		viper.Reset()
	})

	viper.Reset()
	viper.Set("base_nvct_url", server.URL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("api_key", "test-function-api-key")
	viper.Set("nvct_api_key", "test-task-api-key")
	cfgFile = filepath.Join(t.TempDir(), "workflow.yaml")
	configStateManager = nil
	configStateManagerKey = ""
	taskResultsPaginationFlags.timeoutSeconds = 1

	start := time.Now()
	err := runTaskResults(taskResultsCmd, []string{"task-test"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestTaskGetTimeoutCancelsHTTPRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/nvct/tasks/task-test", r.URL.Path)
			select {
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
				t.Error("task get request was not canceled")
			}
		},
	))
	t.Cleanup(server.Close)

	oldCfgFile := cfgFile
	oldStateManager := configStateManager
	oldStateManagerKey := configStateManagerKey
	oldTaskGetFlags := taskGetFlags
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		configStateManager = oldStateManager
		configStateManagerKey = oldStateManagerKey
		taskGetFlags = oldTaskGetFlags
		viper.Reset()
	})

	viper.Reset()
	viper.Set("base_nvct_url", server.URL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("api_key", "test-function-api-key")
	viper.Set("nvct_api_key", "test-task-api-key")
	cfgFile = filepath.Join(t.TempDir(), "workflow.yaml")
	configStateManager = nil
	configStateManagerKey = ""
	taskGetFlags.timeoutSeconds = 1

	start := time.Now()
	err := runTaskGet(taskGetCmd, []string{"task-test"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}

// --- Helpers ----------------------------------------------------------------

func TestParseSecretsListCLI(t *testing.T) {
	secrets, err := parseSecretsList(nil, []string{"FOO=bar", "QUX=quux"})
	require.NoError(t, err)
	require.Len(t, secrets, 2)
	assert.Equal(t, "FOO", secrets[0].Name)
	assert.Equal(t, "bar", secrets[0].Value)
	assert.Equal(t, "QUX", secrets[1].Name)
	assert.Equal(t, "quux", secrets[1].Value)
}

func TestParseSecretsListInvalid(t *testing.T) {
	_, err := parseSecretsList(nil, []string{"missing-equals"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be name=value")
}

func TestParseSecretsListJSON(t *testing.T) {
	// Mimic the shape we'd get from json.Unmarshal into interface{}.
	raw := []interface{}{
		"FOO=bar",
		map[string]interface{}{"name": "API_KEY", "value": "abc"},
		map[string]interface{}{"name": "STRUCT_VAL", "value": map[string]interface{}{"nested": true}},
	}
	secrets, err := parseSecretsList(raw, nil)
	require.NoError(t, err)
	require.Len(t, secrets, 3)
	assert.Equal(t, "FOO", secrets[0].Name)
	assert.Equal(t, "bar", secrets[0].Value)
	assert.Equal(t, "API_KEY", secrets[1].Name)
	assert.Equal(t, "abc", secrets[1].Value)
	assert.Equal(t, "STRUCT_VAL", secrets[2].Name)
	nested, ok := secrets[2].Value.(map[string]interface{})
	require.True(t, ok, "expected map secret value")
	assert.Equal(t, true, nested["nested"])
}

func TestParseSecretsListSecretConfigSlice(t *testing.T) {
	raw := []SecretConfig{
		{Name: "A", Value: "alpha"},
		{Name: "B", Value: 42},
	}
	secrets, err := parseSecretsList(raw, nil)
	require.NoError(t, err)
	require.Len(t, secrets, 2)
	assert.Equal(t, "A", secrets[0].Name)
	assert.Equal(t, "alpha", secrets[0].Value)
	assert.Equal(t, "B", secrets[1].Name)
	assert.Equal(t, 42, secrets[1].Value)
}

func TestParseArtifactsList(t *testing.T) {
	artifacts, err := parseArtifactsList(
		[]ArtifactConfig{{Name: "model-a", Version: "1.0", URI: "s3://bucket/a"}},
		[]string{"model-b:2.0:s3://bucket/b"},
	)
	require.NoError(t, err)
	require.Len(t, artifacts, 2)
	assert.Equal(t, "model-a", artifacts[0].Name)
	assert.Equal(t, "1.0", artifacts[0].Version)
	assert.Equal(t, "s3://bucket/a", artifacts[0].URI)
	assert.Equal(t, "model-b", artifacts[1].Name)
	assert.Equal(t, "2.0", artifacts[1].Version)
	assert.Equal(t, "s3://bucket/b", artifacts[1].URI)
}

func TestParseArtifactsListInvalid(t *testing.T) {
	_, err := parseArtifactsList(nil, []string{"too:few"})
	require.Error(t, err)
}

// --- resolveTaskID ---------------------------------------------------------

func TestResolveTaskIDFromArgs(t *testing.T) {
	got, err := resolveTaskID([]string{"explicit-id"})
	require.NoError(t, err)
	assert.Equal(t, "explicit-id", got)
}

func TestResolveTaskIDFromEmptyArgsNoState(t *testing.T) {
	// Save and restore default state so we don't affect other tests/users.
	original := *GetStateManagerForCurrentCommand().GetState()
	GetStateManagerForCurrentCommand().ClearTask()
	t.Cleanup(func() { *GetStateManagerForCurrentCommand().GetState() = original })

	_, err := resolveTaskID(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task ID is required")
}

func TestResolveTaskIDFromState(t *testing.T) {
	sm := GetStateManagerForCurrentCommand()
	original := *sm.GetState()
	sm.SetTask("state-task", "saved-task")
	t.Cleanup(func() { *sm.GetState() = original })

	got, err := resolveTaskID(nil)
	require.NoError(t, err)
	assert.Equal(t, "state-task", got)
}
