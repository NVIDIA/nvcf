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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int {
	return &v
}

func drainSharedNotifications() {
	for {
		select {
		case <-sharedNotifications:
		default:
			return
		}
	}
}

func TestNotifySharedReloadDropsPendingNotification(t *testing.T) {
	drainSharedNotifications()
	defer drainSharedNotifications()

	notifySharedReload()
	notifySharedReload()

	select {
	case <-sharedNotifications:
	default:
		t.Fatal("expected pending notification")
	}

	select {
	case <-sharedNotifications:
		t.Fatal("expected duplicate notification to be dropped")
	default:
	}
}

func TestGatewayConfigValidateAcceptsOpenAIShadowDefaults(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:       "facebook/opt-125m",
			FunctionID:      "func-id",
			ShadowModelName: "private/facebook/opt-125m-shadow",
		},
		"shadow": {
			ModelName:  "private/facebook/opt-125m-shadow",
			FunctionID: "shadow-func-id",
		},
	}

	require.NoError(t, cfg.Validate())
}

func TestGatewayConfigValidateAcceptsMultipleOpenAIShadows(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:       "facebook/opt-125m",
			FunctionID:      "func-id",
			ShadowModelName: "private/facebook/opt-125m-shadow-a",
			ShadowModelNames: []string{
				"private/facebook/opt-125m-shadow-b",
				"private/facebook/opt-125m-shadow-c",
			},
		},
		"shadow-a": {
			ModelName:  "private/facebook/opt-125m-shadow-a",
			FunctionID: "shadow-a-func-id",
		},
		"shadow-b": {
			ModelName:  "private/facebook/opt-125m-shadow-b",
			FunctionID: "shadow-b-func-id",
		},
		"shadow-c": {
			ModelName:  "private/facebook/opt-125m-shadow-c",
			FunctionID: "shadow-c-func-id",
		},
	}

	require.NoError(t, cfg.Validate())
}

func TestGatewayConfigLoadAcceptsLegacyAndPluralShadowModelNames(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        shadowModelName: private/facebook/opt-125m-shadow-a
        shadowModelNames:
          - private/facebook/opt-125m-shadow-b
      shadow-a:
        modelName: private/facebook/opt-125m-shadow-a
        functionID: shadow-a-func-id
      shadow-b:
        modelName: private/facebook/opt-125m-shadow-b
        functionID: shadow-b-func-id
`), 0600)
	require.NoError(t, err)

	reloadable, err := SetupConfigWithConfigPath(configPath)
	require.NoError(t, err)

	cfg := reloadable.Get()
	primary := cfg.OpenAI.ChatCompletions["primary"]
	assert.Equal(t, "private/facebook/opt-125m-shadow-a", primary.ShadowModelName)
	assert.Equal(t, []string{"private/facebook/opt-125m-shadow-b"}, primary.ShadowModelNames)
}

func TestGatewayConfigValidateRejectsDuplicateOpenAIShadows(t *testing.T) {
	tests := []struct {
		name    string
		primary ModelFunctionDetails
	}{
		{
			name: "duplicate in shadowModelNames",
			primary: ModelFunctionDetails{
				ModelName:  "facebook/opt-125m",
				FunctionID: "func-id",
				ShadowModelNames: []string{
					"private/facebook/opt-125m-shadow-a",
					"private/facebook/opt-125m-shadow-a",
				},
			},
		},
		{
			name: "duplicate legacy shadowModelName and shadowModelNames",
			primary: ModelFunctionDetails{
				ModelName:       "facebook/opt-125m",
				FunctionID:      "func-id",
				ShadowModelName: "private/facebook/opt-125m-shadow-a",
				ShadowModelNames: []string{
					"private/facebook/opt-125m-shadow-a",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GatewayConfig{}
			cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
				"primary": tc.primary,
				"shadow-a": {
					ModelName:  "private/facebook/opt-125m-shadow-a",
					FunctionID: "shadow-a-func-id",
				},
			}

			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, "duplicate shadow target")
		})
	}
}

func TestGatewayConfigValidateRejectsInvalidOpenAIShadowPercentage(t *testing.T) {
	tests := []struct {
		name       string
		percentage int
	}{
		{name: "zero", percentage: 0},
		{name: "negative", percentage: -1},
		{name: "too large", percentage: 101},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GatewayConfig{}
			cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
				"primary": {
					ModelName:        "facebook/opt-125m",
					FunctionID:       "func-id",
					ShadowModelName:  "private/facebook/opt-125m-shadow",
					ShadowPercentage: intPtr(tc.percentage),
				},
				"shadow": {
					ModelName:  "private/facebook/opt-125m-shadow",
					FunctionID: "shadow-func-id",
				},
			}

			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, "shadowPercentage must be between 1 and 100")
		})
	}
}

func TestGatewayConfigValidateRejectsShadowPercentageWithoutShadowModel(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:        "facebook/opt-125m",
			FunctionID:       "func-id",
			ShadowPercentage: intPtr(50),
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadowPercentage requires at least one shadow target")
}

func TestGatewayConfigValidateRejectsShadowPercentageWithoutShadowTarget(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:        "facebook/opt-125m",
			FunctionID:       "func-id",
			ShadowPercentage: intPtr(50),
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadowPercentage requires at least one shadow target")
}

func TestGatewayConfigValidateRejectsShadowCancelOnClientDisconnectWithoutShadowModel(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:                      "facebook/opt-125m",
			FunctionID:                     "func-id",
			ShadowCancelOnClientDisconnect: true,
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadowCancelOnClientDisconnect requires at least one shadow target")
}

func TestGatewayConfigValidateRejectsEmptyShadowModelName(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:  "facebook/opt-125m",
			FunctionID: "func-id",
			ShadowModelNames: []string{
				"",
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadowModelNames cannot contain empty model names")
}

func TestGatewayConfigValidateRejectsMissingShadowModelName(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:  "facebook/opt-125m",
			FunctionID: "func-id",
			ShadowModelNames: []string{
				"missing-shadow-model",
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadow target must reference another model in openai.chatCompletions")
}

func TestGatewayConfigValidateRejectsSelfReferenceInShadowModelNames(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:  "facebook/opt-125m",
			FunctionID: "func-id",
			ShadowModelNames: []string{
				"facebook/opt-125m",
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadow target cannot reference the same model")
}

func TestGatewayConfigValidateRejectsMissingShadowTarget(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:       "facebook/opt-125m",
			FunctionID:      "func-id",
			ShadowModelName: "missing-shadow-model",
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadow target must reference another model in openai.chatCompletions")
}

func TestGatewayConfigValidateRejectsSelfReference(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:       "facebook/opt-125m",
			FunctionID:      "func-id",
			ShadowModelName: "facebook/opt-125m",
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadow target cannot reference the same model")
}

func TestGatewayConfigValidateRejectsCrossSectionReference(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:       "facebook/opt-125m",
			FunctionID:      "func-id",
			ShadowModelName: "microsoft/phi-2-shadow",
		},
	}
	cfg.OpenAI.Completions = map[string]ModelFunctionDetails{
		"shadow": {
			ModelName:  "microsoft/phi-2-shadow",
			FunctionID: "shadow-func-id",
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadow target must reference another model in openai.chatCompletions")
}

func TestGatewayConfigValidateAcceptsImageSections(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ImageGenerations = map[string]ModelFunctionDetails{
		"gen": {ModelName: "qwen/qwen-image-gen", FunctionID: "gen-id"},
	}
	cfg.OpenAI.ImageEdits = map[string]ModelFunctionDetails{
		"edit": {ModelName: "qwen/qwen-image-edit-2511", FunctionID: "edit-id"},
	}
	cfg.OpenAI.ImageVariations = map[string]ModelFunctionDetails{
		"var": {ModelName: "qwen/qwen-image-var", FunctionID: "var-id"},
	}

	require.NoError(t, cfg.Validate())
}

func TestGatewayConfigValidateRejectsShadowOnMultipartImageSections(t *testing.T) {
	tests := []struct {
		name    string
		applyTo func(cfg *GatewayConfig)
	}{
		{
			name: "imageEdits shadowModelName",
			applyTo: func(cfg *GatewayConfig) {
				cfg.OpenAI.ImageEdits = map[string]ModelFunctionDetails{
					"edit": {
						ModelName:       "qwen/qwen-image-edit-2511",
						FunctionID:      "edit-id",
						ShadowModelName: "qwen/qwen-image-edit-shadow",
					},
				}
			},
		},
		{
			name: "imageVariations shadowPercentage",
			applyTo: func(cfg *GatewayConfig) {
				cfg.OpenAI.ImageVariations = map[string]ModelFunctionDetails{
					"var": {
						ModelName:        "qwen/qwen-image-var",
						FunctionID:       "var-id",
						ShadowPercentage: intPtr(50),
					},
				}
			},
		},
		{
			name: "imageEdits shadowCancelOnClientDisconnect",
			applyTo: func(cfg *GatewayConfig) {
				cfg.OpenAI.ImageEdits = map[string]ModelFunctionDetails{
					"edit": {
						ModelName:                      "qwen/qwen-image-edit-2511",
						FunctionID:                     "edit-id",
						ShadowCancelOnClientDisconnect: true,
					},
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GatewayConfig{}
			tc.applyTo(cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, "shadow config is unsupported for multipart image endpoints")
		})
	}
}

func TestGatewayConfigValidateAcceptsImageGenerationsShadow(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ImageGenerations = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:       "qwen/qwen-image-gen",
			FunctionID:      "gen-id",
			ShadowModelName: "qwen/qwen-image-gen-shadow",
		},
		"shadow": {
			ModelName:  "qwen/qwen-image-gen-shadow",
			FunctionID: "shadow-id",
		},
	}

	require.NoError(t, cfg.Validate())
}

func TestGatewayConfigValidateRejectsVanityShadowConfig(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.Vanity = map[string]VanityEntry{
		"test": {
			Host: "test.host",
			Paths: map[string]PathFunctionDetails{
				"path": {
					Path:             "/v1/test",
					FunctionID:       "func-id",
					ShadowFunctionID: "shadow-func-id",
				},
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadow config is unsupported for vanity routes")
}
