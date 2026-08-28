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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"nvcf-cli/internal/client"

	"github.com/spf13/cobra"
)

func TestCreateConfigParsesLLMInvocationConfigFromJSON(t *testing.T) {
	t.Parallel()

	var config CreateConfig
	err := json.Unmarshal([]byte(`{
		"llmInvocationConfig": {
			"priority": {
				"defaultPriority": 0,
				"perAccountPriority": {
					"nca-1": 3
				}
			}
		}
	}`), &config)
	if err != nil {
		t.Fatalf("unmarshal create config: %v", err)
	}

	assertPriorityConfig(t, config.LLMInvocationConfig, 0, map[string]uint32{"nca-1": 3})
}

func TestCreateConfigRejectsInvalidRequestPrioritiesFromJSON(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		payload string
	}{
		{
			name:    "floating_point_default",
			payload: `{"llmInvocationConfig":{"priority":{"defaultPriority":1.5}}}`,
		},
		{
			name:    "negative_default",
			payload: `{"llmInvocationConfig":{"priority":{"defaultPriority":-1}}}`,
		},
		{
			name:    "default_above_uint32",
			payload: `{"llmInvocationConfig":{"priority":{"defaultPriority":4294967296}}}`,
		},
		{
			name:    "floating_point_override",
			payload: `{"llmInvocationConfig":{"priority":{"defaultPriority":7,"perAccountPriority":{"nca-1":1.5}}}}`,
		},
		{
			name:    "override_above_uint32",
			payload: `{"llmInvocationConfig":{"priority":{"defaultPriority":7,"perAccountPriority":{"nca-1":4294967296}}}}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var config CreateConfig
			if err := json.Unmarshal([]byte(test.payload), &config); err == nil {
				t.Fatalf("json.Unmarshal(%s) succeeded, want error", test.payload)
			}
		})
	}
}

func TestUpdateConfigPreservesEmptyLLMInvocationConfigFromJSON(t *testing.T) {
	t.Parallel()

	var config UpdateConfig
	if err := json.Unmarshal([]byte(`{"llmInvocationConfig": {}}`), &config); err != nil {
		t.Fatalf("unmarshal update config: %v", err)
	}
	if config.LLMInvocationConfig == nil {
		t.Fatal("llmInvocationConfig is nil, want non-nil empty config for clear")
	}
	if config.LLMInvocationConfig.Priority != nil {
		t.Fatalf("priority = %#v, want nil", config.LLMInvocationConfig.Priority)
	}
}

func TestLoadCreateConfigAppliesRequestPriorityFlags(t *testing.T) {
	originalFlags := createFlags
	t.Cleanup(func() { createFlags = originalFlags })

	cmd := newPriorityFlagTestCommand()
	if err := cmd.Flags().Set("llm-default-priority", "0"); err != nil {
		t.Fatalf("set default priority: %v", err)
	}
	if err := cmd.Flags().Set("llm-per-account-priority", "nca-1:3"); err != nil {
		t.Fatalf("set per-account priority: %v", err)
	}
	createFlags.llmDefaultPriority = 0
	createFlags.llmPerAccountPriorities = []string{"nca-1:3"}

	config, err := loadCreateConfig(cmd)
	if err != nil {
		t.Fatalf("load create config: %v", err)
	}
	assertPriorityConfig(t, config.LLMInvocationConfig, 0, map[string]uint32{"nca-1": 3})
}

func TestLoadUpdateConfigAppliesRequestPriorityFlags(t *testing.T) {
	originalFlags := updateFlags
	t.Cleanup(func() { updateFlags = originalFlags })

	cmd := newPriorityFlagTestCommand()
	if err := cmd.Flags().Set("llm-default-priority", "7"); err != nil {
		t.Fatalf("set default priority: %v", err)
	}
	if err := cmd.Flags().Set("llm-per-account-priority", "nca-1:3"); err != nil {
		t.Fatalf("set per-account priority: %v", err)
	}
	updateFlags.llmDefaultPriority = 7
	updateFlags.llmPerAccountPriorities = []string{"nca-1:3"}

	config, err := loadUpdateConfig(cmd)
	if err != nil {
		t.Fatalf("load update config: %v", err)
	}
	assertPriorityConfig(t, config.LLMInvocationConfig, 7, map[string]uint32{"nca-1": 3})
}

func TestLoadUpdateConfigClearsRequestPriority(t *testing.T) {
	originalFlags := updateFlags
	t.Cleanup(func() { updateFlags = originalFlags })

	cmd := newPriorityFlagTestCommand()
	if err := cmd.Flags().Set("clear-llm-priority", "true"); err != nil {
		t.Fatalf("set clear priority: %v", err)
	}
	updateFlags.clearLLMPriority = true

	config, err := loadUpdateConfig(cmd)
	if err != nil {
		t.Fatalf("load update config: %v", err)
	}
	if config.LLMInvocationConfig == nil {
		t.Fatal("llmInvocationConfig is nil, want empty config")
	}
	if config.LLMInvocationConfig.Priority != nil {
		t.Fatalf("priority = %#v, want nil", config.LLMInvocationConfig.Priority)
	}
}

func TestLoadUpdateConfigRejectsClearWithPriorityFlags(t *testing.T) {
	originalFlags := updateFlags
	t.Cleanup(func() { updateFlags = originalFlags })

	cmd := newPriorityFlagTestCommand()
	if err := cmd.Flags().Set("clear-llm-priority", "true"); err != nil {
		t.Fatalf("set clear priority: %v", err)
	}
	if err := cmd.Flags().Set("llm-default-priority", "7"); err != nil {
		t.Fatalf("set default priority: %v", err)
	}
	updateFlags.clearLLMPriority = true
	updateFlags.llmDefaultPriority = 7

	_, err := loadUpdateConfig(cmd)
	if err == nil || !strings.Contains(err.Error(), "cannot be used with") {
		t.Fatalf("error = %v, want mutually exclusive flags error", err)
	}
}

func TestRequestPriorityFlagsPreserveInputFileFieldsWhenOmitted(t *testing.T) {
	t.Parallel()

	config := &CreateConfig{LLMInvocationConfig: priorityConfig(7, map[string]uint32{"nca-1": 3})}
	if err := applyCreateRequestPriorityFlagOverrides(&cobra.Command{}, config); err != nil {
		t.Fatalf("apply priority flags: %v", err)
	}
	assertPriorityConfig(t, config.LLMInvocationConfig, 7, map[string]uint32{"nca-1": 3})
}

func TestValidateLLMInvocationConfigRejectsPerAccountWithoutDefault(t *testing.T) {
	t.Parallel()

	err := validateLLMInvocationConfig(&LLMInvocationConfigInput{
		Priority: &PriorityInput{PerAccountPriority: map[string]uint32{"nca-1": 3}},
	})
	if err == nil || !strings.Contains(err.Error(), "defaultPriority is required") {
		t.Fatalf("error = %v, want defaultPriority required", err)
	}
}

func TestValidateLLMInvocationConfigRejectsEmptyNCAID(t *testing.T) {
	t.Parallel()

	config := priorityConfig(7, map[string]uint32{"": 3})
	err := validateLLMInvocationConfig(config)
	if err == nil || !strings.Contains(err.Error(), "NCA ID must not be empty") {
		t.Fatalf("error = %v, want empty NCA ID error", err)
	}
}

func TestValidateLLMInvocationConfigRejectsMoreThan64Overrides(t *testing.T) {
	t.Parallel()

	overrides := make(map[string]uint32, 65)
	for i := 0; i < 65; i++ {
		overrides[fmt.Sprintf("nca-%d", i)] = uint32(i)
	}
	err := validateLLMInvocationConfig(priorityConfig(7, overrides))
	if err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Fatalf("error = %v, want maximum override count error", err)
	}
}

func TestParsePerAccountPriorityRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		":1",
		"nca-1:-1",
		"nca-1:1.5",
		"nca-1:4294967296",
		"nca-1:not-a-number",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if _, _, err := parsePerAccountPriority(value); err == nil {
				t.Fatalf("parsePerAccountPriority(%q) succeeded, want error", value)
			}
		})
	}
}

func TestDefaultPriorityFlagRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"-1", "1.5", "4294967296"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			cmd := newPriorityFlagTestCommand()
			if err := cmd.Flags().Set("llm-default-priority", value); err == nil {
				t.Fatalf("setting default priority to %q succeeded, want error", value)
			}
		})
	}
}

func TestParsePerAccountPrioritiesRejectsDuplicateNCAID(t *testing.T) {
	t.Parallel()

	_, err := parsePerAccountPriorities([]string{"nca-1:3", "nca-1:7"})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want duplicate NCA ID error", err)
	}
}

func TestValidateUpdateConfigAcceptsEmptyLLMInvocationConfig(t *testing.T) {
	t.Parallel()

	err := validateUpdateConfig(&UpdateConfig{
		FunctionID:          "func-123",
		VersionID:           "ver-456",
		LLMInvocationConfig: &LLMInvocationConfigInput{},
	})
	if err != nil {
		t.Fatalf("validate clear request: %v", err)
	}
}

func TestBuildCreateFunctionRequestIncludesLLMInvocationConfig(t *testing.T) {
	t.Parallel()

	request, _, err := buildCreateFunctionRequest(&CreateConfig{
		Name:                "priority-test",
		InferenceURL:        "/v1/chat/completions",
		InferencePort:       8000,
		FunctionType:        "LLM",
		LLMInvocationConfig: priorityConfig(7, map[string]uint32{"nca-1": 3}),
	})
	if err != nil {
		t.Fatalf("build create request: %v", err)
	}
	if request.LLMInvocationConfig == nil || request.LLMInvocationConfig.Priority == nil {
		t.Fatalf("request llmInvocationConfig = %#v", request.LLMInvocationConfig)
	}
	if got := request.LLMInvocationConfig.Priority.DefaultPriority; got == nil || *got != 7 {
		t.Fatalf("defaultPriority = %v, want 7", got)
	}
}

func TestUpdateConfigToClientRequestPreservesEmptyLLMInvocationConfig(t *testing.T) {
	t.Parallel()

	request, err := updateConfigToClientRequest(&UpdateConfig{
		LLMInvocationConfig: &LLMInvocationConfigInput{},
	})
	if err != nil {
		t.Fatalf("build update request: %v", err)
	}
	if request.LLMInvocationConfig == nil {
		t.Fatal("request llmInvocationConfig is nil, want empty config")
	}
	if request.LLMInvocationConfig.Priority != nil {
		t.Fatalf("request priority = %#v, want nil", request.LLMInvocationConfig.Priority)
	}
}

func TestPrintRequestPriority(t *testing.T) {
	t.Parallel()

	defaultPriority := uint32(7)
	var output bytes.Buffer
	printRequestPriority(&output, &client.LLMInvocationConfigDto{
		Priority: &client.PriorityDto{
			DefaultPriority: &defaultPriority,
			PerAccountPriority: map[string]uint32{
				"nca-z": 9,
				"nca-a": 3,
			},
		},
	})

	want := "\nRequest Priority:\n=================\n" +
		"Default Priority: 7\n" +
		"Per-Account Priorities:\n" +
		"  nca-a: 3\n" +
		"  nca-z: 9\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func newPriorityFlagTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Uint32("llm-default-priority", 0, "")
	cmd.Flags().StringArray("llm-per-account-priority", nil, "")
	cmd.Flags().Bool("clear-llm-priority", false, "")
	return cmd
}

func priorityConfig(defaultPriority uint32, overrides map[string]uint32) *LLMInvocationConfigInput {
	return &LLMInvocationConfigInput{
		Priority: &PriorityInput{
			DefaultPriority:    &defaultPriority,
			PerAccountPriority: overrides,
		},
	}
}

func assertPriorityConfig(t *testing.T, config *LLMInvocationConfigInput, defaultPriority uint32, overrides map[string]uint32) {
	t.Helper()
	if config == nil || config.Priority == nil {
		t.Fatalf("llmInvocationConfig = %#v, want priority", config)
	}
	if config.Priority.DefaultPriority == nil || *config.Priority.DefaultPriority != defaultPriority {
		t.Fatalf("defaultPriority = %v, want %d", config.Priority.DefaultPriority, defaultPriority)
	}
	if len(config.Priority.PerAccountPriority) != len(overrides) {
		t.Fatalf("perAccountPriority = %#v, want %#v", config.Priority.PerAccountPriority, overrides)
	}
	for ncaID, want := range overrides {
		if got := config.Priority.PerAccountPriority[ncaID]; got != want {
			t.Fatalf("perAccountPriority[%q] = %d, want %d", ncaID, got, want)
		}
	}
}
