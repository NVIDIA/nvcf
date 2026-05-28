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
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCreateConfigParsesLLMConfigFromJSON(t *testing.T) {
	t.Parallel()

	var config CreateConfig
	err := json.Unmarshal([]byte(`{
		"models": [{
			"name": "dummy-model",
			"llmConfig": {
				"uris": ["/v1/chat/completions", "/v1/responses", "/v1/embeddings"],
				"routingMethod": "round_robin",
				"tokenRateLimit": "1000-M"
			}
		}]
	}`), &config)
	if err != nil {
		t.Fatalf("unmarshal create config: %v", err)
	}

	if len(config.Models) != 1 {
		t.Fatalf("models length = %d, want 1", len(config.Models))
	}
	model := config.Models[0]
	if model.LLMConfig == nil {
		t.Fatal("llmConfig is nil")
	}
	assertStringSlice(t, model.LLMConfig.URIs, []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings"})
	if got := stringValue(model.LLMConfig.RoutingMethod); got != "round_robin" {
		t.Fatalf("routingMethod = %q, want round_robin", got)
	}
	if got := stringValue(model.LLMConfig.TokenRateLimit); got != "1000-M" {
		t.Fatalf("tokenRateLimit = %q, want 1000-M", got)
	}
}

func TestCreateConfigModelLLMConfigRoundTrip(t *testing.T) {
	t.Parallel()

	original := CreateConfig{
		Name: "llm-smoke",
		Models: []ArtifactConfig{{
			Name: "dummy-model",
			LLMConfig: &LLMConfigInput{
				URIs:           []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings"},
				RoutingMethod:  optionalString("round_robin"),
				TokenRateLimit: optionalString("1000-M"),
			},
		}},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal create config: %v", err)
	}

	body := string(encoded)
	for _, want := range []string{
		`"llmConfig":`,
		`"uris":["/v1/chat/completions","/v1/responses","/v1/embeddings"]`,
		`"routingMethod":"round_robin"`,
		`"tokenRateLimit":"1000-M"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("encoded payload %s missing %s", body, want)
		}
	}

	var decoded CreateConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal create config: %v", err)
	}

	if len(decoded.Models) != 1 {
		t.Fatalf("models length = %d, want 1", len(decoded.Models))
	}
	got := decoded.Models[0]
	if got.Name != "dummy-model" {
		t.Fatalf("name = %q, want dummy-model", got.Name)
	}
	if got.LLMConfig == nil {
		t.Fatal("llmConfig is nil after round-trip")
	}
	assertStringSlice(t, got.LLMConfig.URIs, original.Models[0].LLMConfig.URIs)
	if rm := stringValue(got.LLMConfig.RoutingMethod); rm != "round_robin" {
		t.Fatalf("routingMethod = %q, want round_robin", rm)
	}
	if trl := stringValue(got.LLMConfig.TokenRateLimit); trl != "1000-M" {
		t.Fatalf("tokenRateLimit = %q, want 1000-M", trl)
	}
}

func TestCreateConfigModelLLMConfigOmittedWhenNil(t *testing.T) {
	t.Parallel()

	config := CreateConfig{
		Name: "non-llm-function",
		Models: []ArtifactConfig{{
			Name:    "plain-model",
			Version: "1",
			URI:     "registry/model:1",
		}},
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal create config: %v", err)
	}

	if strings.Contains(string(encoded), `"llmConfig"`) {
		t.Fatalf("encoded payload %s should omit llmConfig when nil", string(encoded))
	}
}

func TestParseLLMModelString(t *testing.T) {
	t.Parallel()

	model, err := parseLLMModelString("name=dummy-model,uris=/v1/chat/completions|/v1/responses|/v1/embeddings,routingMethod=power_of_two,tokenRateLimit=1000-M")
	if err != nil {
		t.Fatalf("parse llm model: %v", err)
	}

	if model.Name != "dummy-model" {
		t.Fatalf("name = %q, want dummy-model", model.Name)
	}
	if model.LLMConfig == nil {
		t.Fatal("llmConfig is nil")
	}
	assertStringSlice(t, model.LLMConfig.URIs, []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings"})
	if got := stringValue(model.LLMConfig.RoutingMethod); got != "power_of_two" {
		t.Fatalf("routingMethod = %q, want power_of_two", got)
	}
}

func TestParseLLMModelStringAcceptsAdvancedRoutingMethods(t *testing.T) {
	t.Parallel()

	for _, routingMethod := range []string{"groq_multiregion", "pulsar"} {
		t.Run(routingMethod, func(t *testing.T) {
			t.Parallel()

			model, err := parseLLMModelString("name=dummy-model,uris=/v1/chat/completions,routingMethod=" + routingMethod)
			if err != nil {
				t.Fatalf("parse llm model: %v", err)
			}
			if got := stringValue(model.LLMConfig.RoutingMethod); got != routingMethod {
				t.Fatalf("routingMethod = %q, want %q", got, routingMethod)
			}
		})
	}
}

func TestParseLLMModelStringRejectsInvalidRoutingMethod(t *testing.T) {
	t.Parallel()

	_, err := parseLLMModelString("name=dummy-model,uris=/v1/chat/completions,routingMethod=sticky")
	if err == nil {
		t.Fatal("expected invalid routing method error")
	}
}

func TestValidateLLMTokenRateLimit(t *testing.T) {
	t.Parallel()

	valid := []string{
		"",
		"100-S",
		"9000-M",
		"25000-H",
		"100000-D",
		"500000-W",
		"100-S,9000-M,25000-H,100000-D,500000-W",
	}
	for _, value := range valid {
		value := value
		t.Run("valid_"+value, func(t *testing.T) {
			t.Parallel()

			if err := validateLLMTokenRateLimit(value); err != nil {
				t.Fatalf("validate tokenRateLimit %q: %v", value, err)
			}
		})
	}

	invalid := []string{
		"100-A",
		"100",
		"0-S",
		"-1-S",
		"100-s",
		"100-S,200-S",
		"100-S,",
		",100-S",
	}
	for _, value := range invalid {
		value := value
		t.Run("invalid_"+value, func(t *testing.T) {
			t.Parallel()

			err := validateLLMTokenRateLimit(value)
			assertTokenRateLimitError(t, err)
		})
	}
}

func TestParseLLMModelStringRejectsInvalidTokenRateLimit(t *testing.T) {
	t.Parallel()

	_, err := parseLLMModelString("name=dummy-model,uris=/v1/chat/completions,tokenRateLimit=100-A")
	assertTokenRateLimitError(t, err)
}

func TestArtifactConfigToClientRejectsInvalidTokenRateLimit(t *testing.T) {
	t.Parallel()

	_, err := artifactConfigToClientArtifact(ArtifactConfig{
		Name: "dummy-model",
		LLMConfig: &LLMConfigInput{
			URIs:           []string{"/v1/chat/completions"},
			TokenRateLimit: optionalString("100-A"),
		},
	})
	assertTokenRateLimitError(t, err)
}

func TestLoadCreateConfigAppendsLLMModelFlag(t *testing.T) {
	originalFlags := createFlags
	t.Cleanup(func() {
		createFlags = originalFlags
	})

	cmd := &cobra.Command{}
	cmd.Flags().StringArray("llm-model", nil, "")
	if err := cmd.Flags().Set("llm-model", "name=dummy-model,uris=/v1/chat/completions|/v1/embeddings,routingMethod=round_robin"); err != nil {
		t.Fatalf("set llm-model flag: %v", err)
	}
	createFlags = struct {
		inputFile string

		name           string
		containerImage string
		inferenceURL   string
		inferencePort  int

		description string
		tags        []string

		healthURI            string
		healthProtocol       string
		healthPort           int
		healthTimeout        string
		healthExpectedStatus int

		functionType         string
		apiBodyFormat        string
		containerArgs        string
		containerEnvironment []string

		helmChart            string
		helmChartServiceName string

		secrets []string

		models    []string
		llmModels []string
		resources []string

		rateLimit         string
		rateLimitExempted []string
		rateLimitSync     bool

		logsTelemetryId    string
		metricsTelemetryId string
		tracesTelemetryId  string
	}{
		llmModels: []string{"name=dummy-model,uris=/v1/chat/completions|/v1/embeddings,routingMethod=round_robin"},
	}

	config, err := loadCreateConfig(cmd)
	if err != nil {
		t.Fatalf("load create config: %v", err)
	}

	if len(config.Models) != 1 {
		t.Fatalf("models length = %d, want 1", len(config.Models))
	}
	model := config.Models[0]
	if model.LLMConfig == nil {
		t.Fatal("llmConfig is nil")
	}
	if got := stringValue(model.LLMConfig.RoutingMethod); got != "round_robin" {
		t.Fatalf("routingMethod = %q, want round_robin", got)
	}
	assertStringSlice(t, model.LLMConfig.URIs, []string{"/v1/chat/completions", "/v1/embeddings"})
}

func TestUpdateConfigParsesLLMModelUpdatesFromJSON(t *testing.T) {
	t.Parallel()

	var config UpdateConfig
	err := json.Unmarshal([]byte(`{
		"functionId": "func-123",
		"versionId": "ver-456",
		"modelUpdates": [{
			"modelName": "dummy-model",
			"llmConfig": {
				"routingMethod": "round_robin",
				"tokenRateLimit": "1000-M"
			}
		}]
	}`), &config)
	if err != nil {
		t.Fatalf("unmarshal update config: %v", err)
	}

	if len(config.ModelUpdates) != 1 {
		t.Fatalf("modelUpdates length = %d, want 1", len(config.ModelUpdates))
	}
	update := config.ModelUpdates[0]
	if update.ModelName != "dummy-model" {
		t.Fatalf("modelName = %q, want dummy-model", update.ModelName)
	}
	if update.LLMConfig == nil {
		t.Fatal("llmConfig is nil")
	}
	if got := stringValue(update.LLMConfig.RoutingMethod); got != "round_robin" {
		t.Fatalf("routingMethod = %q, want round_robin", got)
	}
	if got := stringValue(update.LLMConfig.TokenRateLimit); got != "1000-M" {
		t.Fatalf("tokenRateLimit = %q, want 1000-M", got)
	}
}

func TestParseLLMModelUpdateString(t *testing.T) {
	t.Parallel()

	update, err := parseLLMModelUpdateString("name=dummy-model,routingMethod=power_of_two,tokenRateLimit=1000-M")
	if err != nil {
		t.Fatalf("parse llm model update: %v", err)
	}

	if update.ModelName != "dummy-model" {
		t.Fatalf("modelName = %q, want dummy-model", update.ModelName)
	}
	if update.LLMConfig == nil {
		t.Fatal("llmConfig is nil")
	}
	if got := stringValue(update.LLMConfig.RoutingMethod); got != "power_of_two" {
		t.Fatalf("routingMethod = %q, want power_of_two", got)
	}
	if got := stringValue(update.LLMConfig.TokenRateLimit); got != "1000-M" {
		t.Fatalf("tokenRateLimit = %q, want 1000-M", got)
	}
}

func TestParseLLMModelUpdateStringAcceptsAdvancedRoutingMethods(t *testing.T) {
	t.Parallel()

	for _, routingMethod := range []string{"groq_multiregion", "pulsar"} {
		t.Run(routingMethod, func(t *testing.T) {
			t.Parallel()

			update, err := parseLLMModelUpdateString("name=dummy-model,routingMethod=" + routingMethod)
			if err != nil {
				t.Fatalf("parse llm model update: %v", err)
			}
			if got := stringValue(update.LLMConfig.RoutingMethod); got != routingMethod {
				t.Fatalf("routingMethod = %q, want %q", got, routingMethod)
			}
		})
	}
}

func TestParseLLMModelUpdateStringAcceptsTokenRateLimitOnly(t *testing.T) {
	t.Parallel()

	update, err := parseLLMModelUpdateString("name=dummy-model,tokenRateLimit=1000-M")
	if err != nil {
		t.Fatalf("parse llm model update: %v", err)
	}

	if update.ModelName != "dummy-model" {
		t.Fatalf("modelName = %q, want dummy-model", update.ModelName)
	}
	if update.LLMConfig == nil {
		t.Fatal("llmConfig is nil")
	}
	if got := stringValue(update.LLMConfig.RoutingMethod); got != "" {
		t.Fatalf("routingMethod = %q, want empty", got)
	}
	if got := stringValue(update.LLMConfig.TokenRateLimit); got != "1000-M" {
		t.Fatalf("tokenRateLimit = %q, want 1000-M", got)
	}
}

func TestParseLLMModelUpdateStringRejectsMissingModelName(t *testing.T) {
	t.Parallel()

	_, err := parseLLMModelUpdateString("routingMethod=round_robin")
	if err == nil {
		t.Fatal("expected missing name error")
	}
}

func TestParseLLMModelUpdateStringRejectsInvalidRoutingMethod(t *testing.T) {
	t.Parallel()

	_, err := parseLLMModelUpdateString("name=dummy-model,routingMethod=sticky")
	if err == nil {
		t.Fatal("expected invalid routing method error")
	}
}

func TestParseLLMModelUpdateStringRejectsInvalidTokenRateLimit(t *testing.T) {
	t.Parallel()

	_, err := parseLLMModelUpdateString("name=dummy-model,tokenRateLimit=100-A")
	assertTokenRateLimitError(t, err)
}

func TestModelUpdateConfigToClientRejectsInvalidTokenRateLimit(t *testing.T) {
	t.Parallel()

	_, err := modelUpdateConfigToClient(ModelUpdateConfig{
		ModelName: "dummy-model",
		LLMConfig: &LLMConfigUpdateInput{
			TokenRateLimit: optionalString("100-A"),
		},
	})
	assertTokenRateLimitError(t, err)
}

func TestLoadUpdateConfigAppendsLLMModelUpdateFlag(t *testing.T) {
	t.Parallel()

	originalFlags := updateFlags
	t.Cleanup(func() {
		updateFlags = originalFlags
	})

	cmd := &cobra.Command{}
	cmd.Flags().StringArray("llm-model-update", nil, "")
	if err := cmd.Flags().Set("llm-model-update", "name=dummy-model,routingMethod=round_robin"); err != nil {
		t.Fatalf("set llm-model-update flag: %v", err)
	}
	updateFlags.llmModelUpdates = []string{"name=dummy-model,routingMethod=round_robin"}

	config, err := loadUpdateConfig(cmd)
	if err != nil {
		t.Fatalf("load update config: %v", err)
	}

	if len(config.ModelUpdates) != 1 {
		t.Fatalf("modelUpdates length = %d, want 1", len(config.ModelUpdates))
	}
	update := config.ModelUpdates[0]
	if update.ModelName != "dummy-model" {
		t.Fatalf("modelName = %q, want dummy-model", update.ModelName)
	}
	if got := stringValue(update.LLMConfig.RoutingMethod); got != "round_robin" {
		t.Fatalf("routingMethod = %q, want round_robin", got)
	}
}

func TestValidateUpdateConfigRejectsNoUpdates(t *testing.T) {
	t.Parallel()

	err := validateUpdateConfig(&UpdateConfig{FunctionID: "func-123", VersionID: "ver-456"})
	if err == nil {
		t.Fatal("expected no updates error")
	}
}

func TestLoadInvokeConfigAppliesInferenceURLFlag(t *testing.T) {
	originalFlags := invokeFlags
	t.Cleanup(func() {
		invokeFlags = originalFlags
	})

	cmd := &cobra.Command{}
	cmd.Flags().String("inference-url", "", "")
	if err := cmd.Flags().Set("inference-url", "/v1/embeddings"); err != nil {
		t.Fatalf("set inference-url flag: %v", err)
	}
	invokeFlags.inferenceURL = "/v1/embeddings"

	config, err := loadInvokeConfig(cmd)
	if err != nil {
		t.Fatalf("load invoke config: %v", err)
	}
	if got, want := config.InferenceURL, "/v1/embeddings"; got != want {
		t.Fatalf("InferenceURL = %q, want %q", got, want)
	}
}

func TestLoadInvokeConfigAppliesModelNameFlag(t *testing.T) {
	originalFlags := invokeFlags
	t.Cleanup(func() {
		invokeFlags = originalFlags
	})

	cmd := &cobra.Command{}
	cmd.Flags().String("model-name", "", "")
	if err := cmd.Flags().Set("model-name", "dummy-model"); err != nil {
		t.Fatalf("set model-name flag: %v", err)
	}
	invokeFlags.modelName = "dummy-model"

	config, err := loadInvokeConfig(cmd)
	if err != nil {
		t.Fatalf("load invoke config: %v", err)
	}
	if got, want := config.ModelName, "dummy-model"; got != want {
		t.Fatalf("ModelName = %q, want %q", got, want)
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func assertTokenRateLimitError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected tokenRateLimit validation error")
	}
	if !strings.Contains(err.Error(), "invalid tokenRateLimit") {
		t.Fatalf("err = %v, want invalid tokenRateLimit", err)
	}
}

func assertStringSlice(t *testing.T, got []string, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("uris = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uris = %#v, want %#v", got, want)
		}
	}
}
