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
	"testing"

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
			"create":                   false,
			"list":                     false,
			"bulk":                     false,
			"get [taskId]":             false,
			"delete [taskId]":          false,
			"cancel [taskId]":          false,
			"events [taskId]":          false,
			"results [taskId]":         false,
			"update-secrets [taskId]":  false,
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

// --- validateTaskCreateConfig ----------------------------------------------

func minValidTaskCfg() *TaskCreateConfig {
	return &TaskCreateConfig{
		Name:             "t",
		GpuSpecification: &TaskGpuSpecificationInput{GPU: "H100", InstanceType: "NCP.GPU.H100_8x"},
	}
}

func TestValidateTaskCreateConfig(t *testing.T) {
	t.Run("missing name is rejected", func(t *testing.T) {
		cfg := &TaskCreateConfig{GpuSpecification: &TaskGpuSpecificationInput{GPU: "H100", InstanceType: "NCP.GPU.H100_8x"}}
		require.ErrorContains(t, validateTaskCreateConfig(cfg), "task name is required")
	})

	t.Run("missing gpuSpecification is rejected", func(t *testing.T) {
		cfg := &TaskCreateConfig{Name: "t"}
		require.ErrorContains(t, validateTaskCreateConfig(cfg), "gpuSpecification is required")
	})

	t.Run("valid ISO 8601 durations pass", func(t *testing.T) {
		for _, dur := range []string{"PT4H", "PT72H", "P1DT30M", "PT1H30M", "PT0S"} {
			cfg := minValidTaskCfg()
			cfg.MaxRuntimeDuration = dur
			assert.NoErrorf(t, validateTaskCreateConfig(cfg), "expected %q to be valid", dur)
		}
	})

	t.Run("invalid duration rejected with flag name", func(t *testing.T) {
		cfg := minValidTaskCfg()
		cfg.MaxRuntimeDuration = "4hours"
		err := validateTaskCreateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--max-runtime")
		assert.Contains(t, err.Error(), "4hours")
	})

	t.Run("invalid max-queued duration rejected", func(t *testing.T) {
		cfg := minValidTaskCfg()
		cfg.MaxQueuedDuration = "notaduration"
		err := validateTaskCreateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--max-queued")
	})

	t.Run("invalid termination-grace duration rejected", func(t *testing.T) {
		cfg := minValidTaskCfg()
		cfg.TerminationGracePeriodDuration = "xx"
		err := validateTaskCreateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--termination-grace")
	})

	t.Run("valid result-strategy values pass", func(t *testing.T) {
		for _, s := range []string{"UPLOAD", "NONE", "upload", "none", "Upload"} {
			cfg := minValidTaskCfg()
			cfg.ResultHandlingStrategy = s
			assert.NoErrorf(t, validateTaskCreateConfig(cfg), "expected %q to be valid", s)
		}
	})

	t.Run("invalid result-strategy rejected with flag name", func(t *testing.T) {
		cfg := minValidTaskCfg()
		cfg.ResultHandlingStrategy = "BOGUS"
		err := validateTaskCreateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--result-strategy")
		assert.Contains(t, err.Error(), "BOGUS")
	})

	t.Run("case-insensitive result-strategy is normalized to uppercase", func(t *testing.T) {
		cfg := minValidTaskCfg()
		cfg.ResultHandlingStrategy = "upload"
		require.NoError(t, validateTaskCreateConfig(cfg))
		assert.Equal(t, "UPLOAD", cfg.ResultHandlingStrategy)
	})
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
