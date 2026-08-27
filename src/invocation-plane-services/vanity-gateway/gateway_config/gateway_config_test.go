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
	"fmt"
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

func TestGatewayConfigValidateAcceptsPerTargetShadows(t *testing.T) {
	percentage := 10
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:  "facebook/opt-125m",
			FunctionID: "func-id",
			Shadows: []ShadowConfig{
				{
					ModelName:                "private/facebook/opt-125m-shadow-a",
					Percentage:               &percentage,
					SamplingMethod:           ShadowSamplingMethodPerBearerKey,
					CancelOnClientDisconnect: true,
				},
				{
					ModelName:      "private/facebook/opt-125m-shadow-b",
					SamplingMethod: ShadowSamplingMethodRandom,
				},
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
	}

	require.NoError(t, cfg.Validate())
}

func TestGatewayConfigValidateTreatsProgrammaticEmptyShadowsAsAbsent(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:       "facebook/opt-125m",
			FunctionID:      "func-id",
			Shadows:         []ShadowConfig{},
			ShadowModelName: "private/facebook/opt-125m-shadow",
		},
		"shadow": {
			ModelName:  "private/facebook/opt-125m-shadow",
			FunctionID: "shadow-func-id",
		},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []ShadowConfig{{ModelName: "private/facebook/opt-125m-shadow"}},
		cfg.OpenAI.ChatCompletions["primary"].EffectiveShadows())
}

func TestModelFunctionDetailsEffectiveShadowsNormalizesLegacyFields(t *testing.T) {
	percentage := 25
	entry := ModelFunctionDetails{
		ShadowModelName:                "shadow-a",
		ShadowModelNames:               []string{"shadow-b"},
		ShadowPercentage:               &percentage,
		ShadowSamplingMethod:           ShadowSamplingMethodPerBearerKey,
		ShadowCancelOnClientDisconnect: true,
	}

	shadows := entry.EffectiveShadows()
	require.Len(t, shadows, 2)
	assert.Equal(t, "shadow-a", shadows[0].ModelName)
	assert.Equal(t, "shadow-b", shadows[1].ModelName)
	for _, shadow := range shadows {
		require.NotNil(t, shadow.Percentage)
		assert.Equal(t, 25, *shadow.Percentage)
		assert.Equal(t, ShadowSamplingMethodPerBearerKey, shadow.SamplingMethod)
		assert.True(t, shadow.CancelOnClientDisconnect)
	}

	*shadows[0].Percentage = 50
	assert.Equal(t, 25, percentage)
	assert.Equal(t, 25, *shadows[1].Percentage)
}

func TestModelFunctionDetailsEffectiveShadowsReturnsCopy(t *testing.T) {
	percentage := 10
	entry := ModelFunctionDetails{
		Shadows: []ShadowConfig{{ModelName: "shadow", Percentage: &percentage}},
	}

	shadows := entry.EffectiveShadows()
	shadows[0].ModelName = "changed"
	*shadows[0].Percentage = 20

	assert.Equal(t, "shadow", entry.Shadows[0].ModelName)
	assert.Equal(t, 10, *entry.Shadows[0].Percentage)
}

func TestModelFunctionDetailsEffectiveShadowsLegacyAndPerTargetEquivalent(t *testing.T) {
	percentage := 25
	legacy := ModelFunctionDetails{
		ShadowModelName:                "shadow-a",
		ShadowModelNames:               []string{"shadow-b"},
		ShadowPercentage:               &percentage,
		ShadowSamplingMethod:           ShadowSamplingMethodPerBearerKey,
		ShadowCancelOnClientDisconnect: true,
	}
	perTarget := ModelFunctionDetails{Shadows: []ShadowConfig{
		{
			ModelName:                "shadow-a",
			Percentage:               intPtr(25),
			SamplingMethod:           ShadowSamplingMethodPerBearerKey,
			CancelOnClientDisconnect: true,
		},
		{
			ModelName:                "shadow-b",
			Percentage:               intPtr(25),
			SamplingMethod:           ShadowSamplingMethodPerBearerKey,
			CancelOnClientDisconnect: true,
		},
	}}

	assert.Equal(t, perTarget.EffectiveShadows(), legacy.EffectiveShadows())
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

func TestGatewayConfigLoadAcceptsStandaloneLegacyEmptyValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        shadowModelName: ""
        shadowModelNames: []
        shadowPercentage: null
        shadowSamplingMethod: ""
        shadowCancelOnClientDisconnect: false
      null-sampling:
        modelName: facebook/opt-125m-null-sampling
        functionID: null-sampling-func-id
        shadowSamplingMethod: null
`), 0600)
	require.NoError(t, err)

	reloadable, err := SetupConfigWithConfigPath(configPath)
	require.NoError(t, err)

	primary := reloadable.Get().OpenAI.ChatCompletions["primary"]
	assert.Empty(t, primary.EffectiveShadows())
	assert.Empty(t, reloadable.Get().OpenAI.ChatCompletions["null-sampling"].EffectiveShadows())
}

func TestGatewayConfigLoadAcceptsPerTargetShadows(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        shadows:
          - modelName: private/facebook/opt-125m-shadow-a
            percentage: 10
            samplingMethod: perBearerKey
            cancelOnClientDisconnect: true
          - modelName: private/facebook/opt-125m-shadow-b
            percentage: 50
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

	shadows := reloadable.Get().OpenAI.ChatCompletions["primary"].Shadows
	require.Len(t, shadows, 2)
	assert.Equal(t, "private/facebook/opt-125m-shadow-a", shadows[0].ModelName)
	assert.Equal(t, 10, *shadows[0].Percentage)
	assert.Equal(t, ShadowSamplingMethodPerBearerKey, shadows[0].SamplingMethod)
	assert.True(t, shadows[0].CancelOnClientDisconnect)
	assert.Equal(t, "private/facebook/opt-125m-shadow-b", shadows[1].ModelName)
	assert.Equal(t, 50, *shadows[1].Percentage)
}

func TestGatewayConfigLoadRejectsUnknownPerTargetShadowFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{name: "typo", field: "precentage: 10"},
		{name: "nested legacy field", field: "shadowPercentage: 10"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			contents := fmt.Sprintf(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        shadows:
          - modelName: private/facebook/opt-125m-shadow
            %s
`, tc.field)
			err := os.WriteFile(configPath, []byte(contents), 0600)
			require.NoError(t, err)

			_, err = SetupConfigWithConfigPath(configPath)
			require.Error(t, err)
			assert.ErrorContains(t, err, "unknown shadow config field")
		})
	}
}

func TestGatewayConfigLoadRejectsMixedShadowFieldPresence(t *testing.T) {
	tests := []struct {
		name    string
		section string
		fields  string
	}{
		{
			name: "empty legacy single target",
			fields: `        shadows:
          - modelName: private/facebook/opt-125m-shadow
        shadowModelName: ""`,
		},
		{
			name: "empty legacy target list",
			fields: `        shadows:
          - modelName: private/facebook/opt-125m-shadow
        shadowModelNames: []`,
		},
		{
			name: "null legacy percentage",
			fields: `        shadows:
          - modelName: private/facebook/opt-125m-shadow
        shadowPercentage: null`,
		},
		{
			name: "empty legacy sampling method",
			fields: `        shadows:
          - modelName: private/facebook/opt-125m-shadow
        shadowSamplingMethod: ""`,
		},
		{
			name: "false legacy cancellation policy",
			fields: `        shadows:
          - modelName: private/facebook/opt-125m-shadow
        shadowCancelOnClientDisconnect: false`,
		},
		{
			name: "empty per-target list",
			fields: `        shadows: []
        shadowModelName: private/facebook/opt-125m-shadow`,
		},
		{
			name: "null per-target list",
			fields: `        shadows: null
        shadowModelName: private/facebook/opt-125m-shadow`,
		},
		{
			name:    "multipart empty fields",
			section: "imageEdits",
			fields: `        shadows: []
        shadowCancelOnClientDisconnect: false`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			section := tc.section
			if section == "" {
				section = "chatCompletions"
			}
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			contents := fmt.Sprintf(`
v2config:
  openai:
    %s:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
%s
      shadow:
        modelName: private/facebook/opt-125m-shadow
        functionID: shadow-func-id
`, section, tc.fields)
			err := os.WriteFile(configPath, []byte(contents), 0600)
			require.NoError(t, err)

			_, err = SetupConfigWithConfigPath(configPath)
			require.Error(t, err)
			assert.ErrorContains(t, err, "shadows cannot be combined with legacy shadow fields")
		})
	}
}

func TestGatewayConfigLoadAcceptsShadowSamplingMethod(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        shadowModelName: private/facebook/opt-125m-shadow
        shadowSamplingMethod: perBearerKey
      shadow:
        modelName: private/facebook/opt-125m-shadow
        functionID: shadow-func-id
`), 0600)
	require.NoError(t, err)

	reloadable, err := SetupConfigWithConfigPath(configPath)
	require.NoError(t, err)

	cfg := reloadable.Get()
	assert.Equal(t, ShadowSamplingMethodPerBearerKey, cfg.OpenAI.ChatCompletions["primary"].ShadowSamplingMethod)
}

func TestGatewayConfigLoadAcceptsSessionTimeout(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        sessionTimeout: 900
      zero:
        modelName: facebook/opt-125m-zero
        functionID: zero-func-id
        sessionTimeout: 0
`), 0600)
	require.NoError(t, err)

	reloadable, err := SetupConfigWithConfigPath(configPath)
	require.NoError(t, err)

	cfg := reloadable.Get()
	assert.Equal(t, SessionTimeoutSeconds(900), cfg.OpenAI.ChatCompletions["primary"].SessionTimeout)
	assert.Equal(t, SessionTimeoutSeconds(0), cfg.OpenAI.ChatCompletions["zero"].SessionTimeout)
}

func TestGatewayConfigLoadAcceptsCustomHeaders(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        customHeaders:
          X-Provider-Feature: enabled
          X-Request-Source: vanity-gateway
  vanity:
    example:
      host: ai.example.com
      paths:
        sample:
          path: /v1/example/infer
          functionID: vanity-func-id
          customHeaders:
            X-Provider-Feature: enabled
`), 0600)
	require.NoError(t, err)

	reloadable, err := SetupConfigWithConfigPath(configPath)
	require.NoError(t, err)

	cfg := reloadable.Get()
	assert.Equal(t, CustomHeaders{
		"X-Provider-Feature": "enabled",
		"X-Request-Source":   "vanity-gateway",
	}, cfg.OpenAI.ChatCompletions["primary"].CustomHeaders)
	assert.Equal(t, CustomHeaders{
		"X-Provider-Feature": "enabled",
	}, cfg.Vanity["example"].Paths["sample"].CustomHeaders)
}

func TestGatewayConfigLoadAcceptsNullCustomHeaders(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        customHeaders: null
  vanity:
    example:
      host: ai.example.com
      paths:
        sample:
          path: /v1/example/infer
          functionID: vanity-func-id
          customHeaders: null
`), 0600)
	require.NoError(t, err)

	reloadable, err := SetupConfigWithConfigPath(configPath)
	require.NoError(t, err)

	cfg := reloadable.Get()
	assert.Nil(t, cfg.OpenAI.ChatCompletions["primary"].CustomHeaders)
	assert.Nil(t, cfg.Vanity["example"].Paths["sample"].CustomHeaders)
}

func TestGatewayConfigLoadRejectsNonStringCustomHeaderValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: func-id
        customHeaders:
          X-Provider-Feature: true
`), 0600)
	require.NoError(t, err)

	_, err = SetupConfigWithConfigPath(configPath)
	require.Error(t, err)
	assert.ErrorContains(t, err, "customHeaders.X-Provider-Feature must be a string")
}

func TestGatewayConfigValidateRejectsInvalidCustomHeaders(t *testing.T) {
	tests := []struct {
		name          string
		headers       CustomHeaders
		vanity        bool
		errorContains string
	}{
		{
			name:          "empty name",
			headers:       CustomHeaders{"": "value"},
			errorContains: "customHeaders cannot contain empty header names",
		},
		{
			name:          "malformed name",
			headers:       CustomHeaders{"Bad Header": "value"},
			errorContains: "invalid HTTP field name",
		},
		{
			name:          "duplicate name",
			headers:       CustomHeaders{"X-Foo": "first", "x-foo": "second"},
			errorContains: "duplicate header names",
		},
		{
			name:          "nvcf managed header",
			headers:       CustomHeaders{"NVCF-POLL-SECONDS": "value"},
			errorContains: "NVCF-managed header",
		},
		{
			name:          "reserved vanity host",
			headers:       CustomHeaders{"Host": "value"},
			vanity:        true,
			errorContains: "reserved header",
		},
		{
			name:          "reserved authorization",
			headers:       CustomHeaders{"Authorization": "value"},
			errorContains: "reserved header",
		},
		{
			name:          "reserved function id",
			headers:       CustomHeaders{"function-id": "value"},
			errorContains: "reserved header",
		},
		{
			name:          "reserved function version id",
			headers:       CustomHeaders{"function-version-id": "value"},
			errorContains: "reserved header",
		},
		{
			name:          "reserved content length",
			headers:       CustomHeaders{"Content-Length": "value"},
			errorContains: "reserved header",
		},
		{
			name:          "reserved connection",
			headers:       CustomHeaders{"Connection": "value"},
			errorContains: "reserved header",
		},
		{
			name:          "reserved proxy header",
			headers:       CustomHeaders{"Proxy-Authorization": "value"},
			errorContains: "reserved header",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GatewayConfig{}
			if tc.vanity {
				cfg.Vanity = map[string]VanityEntry{
					"example": {
						Host: "ai.example.com",
						Paths: map[string]PathFunctionDetails{
							"sample": {
								Path:          "/v1/example/infer",
								FunctionID:    "func-id",
								CustomHeaders: tc.headers,
							},
						},
					},
				}
			} else {
				cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
					"primary": {
						ModelName:     "facebook/opt-125m",
						FunctionID:    "func-id",
						CustomHeaders: tc.headers,
					},
				}
			}

			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.errorContains)
		})
	}
}

func TestGatewayConfigValidateRejectsNegativeSessionTimeout(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:      "facebook/opt-125m",
			FunctionID:     "func-id",
			SessionTimeout: -1,
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "sessionTimeout must be greater than or equal to 0")
}

func TestGatewayConfigValidateRejectsVanitySessionTimeout(t *testing.T) {
	sessionTimeout := SessionTimeoutSeconds(900)
	cfg := &GatewayConfig{}
	cfg.Vanity = map[string]VanityEntry{
		"example": {
			Host: "ai.example.com",
			Paths: map[string]PathFunctionDetails{
				"sample": {
					Path:           "/v1/example/infer",
					FunctionID:     "func-id",
					SessionTimeout: &sessionTimeout,
				},
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "sessionTimeout is unsupported for vanity routes")
}

func TestGatewayConfigLoadRejectsNullVanitySessionTimeout(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
v2config:
  vanity:
    example:
      host: ai.example.com
      paths:
        sample:
          path: /v1/example/infer
          functionID: func-id
          sessionTimeout: null
`), 0600)
	require.NoError(t, err)

	_, err = SetupConfigWithConfigPath(configPath)
	require.Error(t, err)
	assert.ErrorContains(t, err, "sessionTimeout is unsupported for vanity routes")
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

func TestGatewayConfigValidateRejectsMixedShadowForms(t *testing.T) {
	percentage := 50
	tests := []struct {
		name  string
		apply func(*ModelFunctionDetails)
	}{
		{
			name: "legacy single target",
			apply: func(entry *ModelFunctionDetails) {
				entry.ShadowModelName = "private/facebook/opt-125m-shadow-b"
			},
		},
		{
			name: "legacy target list",
			apply: func(entry *ModelFunctionDetails) {
				entry.ShadowModelNames = []string{"private/facebook/opt-125m-shadow-b"}
			},
		},
		{
			name: "legacy percentage",
			apply: func(entry *ModelFunctionDetails) {
				entry.ShadowPercentage = &percentage
			},
		},
		{
			name: "legacy sampling method",
			apply: func(entry *ModelFunctionDetails) {
				entry.ShadowSamplingMethod = ShadowSamplingMethodRandom
			},
		},
		{
			name: "legacy cancellation policy",
			apply: func(entry *ModelFunctionDetails) {
				entry.ShadowCancelOnClientDisconnect = true
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			primary := ModelFunctionDetails{
				ModelName:  "facebook/opt-125m",
				FunctionID: "func-id",
				Shadows: []ShadowConfig{{
					ModelName: "private/facebook/opt-125m-shadow-a",
				}},
			}
			tc.apply(&primary)

			cfg := &GatewayConfig{}
			cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
				"primary": primary,
				"shadow-a": {
					ModelName:  "private/facebook/opt-125m-shadow-a",
					FunctionID: "shadow-a-func-id",
				},
				"shadow-b": {
					ModelName:  "private/facebook/opt-125m-shadow-b",
					FunctionID: "shadow-b-func-id",
				},
			}

			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, "shadows cannot be combined with legacy shadow fields")
		})
	}
}

func TestGatewayConfigValidateRejectsInvalidPerTargetShadows(t *testing.T) {
	tests := []struct {
		name     string
		shadows  []ShadowConfig
		expected string
	}{
		{
			name:     "empty model name",
			shadows:  []ShadowConfig{{}},
			expected: "shadows[0]: modelName is required",
		},
		{
			name: "duplicate target",
			shadows: []ShadowConfig{
				{ModelName: "private/facebook/opt-125m-shadow-a"},
				{ModelName: "private/facebook/opt-125m-shadow-a"},
			},
			expected: "duplicate shadow target",
		},
		{
			name: "percentage below range",
			shadows: []ShadowConfig{{
				ModelName:  "private/facebook/opt-125m-shadow-a",
				Percentage: intPtr(0),
			}},
			expected: "percentage must be between 1 and 100",
		},
		{
			name: "percentage above range",
			shadows: []ShadowConfig{{
				ModelName:  "private/facebook/opt-125m-shadow-a",
				Percentage: intPtr(101),
			}},
			expected: "percentage must be between 1 and 100",
		},
		{
			name: "invalid sampling method",
			shadows: []ShadowConfig{{
				ModelName:      "private/facebook/opt-125m-shadow-a",
				SamplingMethod: ShadowSamplingMethod("weighted"),
			}},
			expected: "samplingMethod must be",
		},
		{
			name: "self reference",
			shadows: []ShadowConfig{{
				ModelName: "facebook/opt-125m",
			}},
			expected: "shadow target cannot reference the same model",
		},
		{
			name: "missing target",
			shadows: []ShadowConfig{{
				ModelName: "private/facebook/missing-shadow",
			}},
			expected: "shadow target must reference another model in openai.chatCompletions",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GatewayConfig{}
			cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
				"primary": {
					ModelName:  "facebook/opt-125m",
					FunctionID: "func-id",
					Shadows:    tc.shadows,
				},
				"shadow-a": {
					ModelName:  "private/facebook/opt-125m-shadow-a",
					FunctionID: "shadow-a-func-id",
				},
			}

			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.expected)
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

func TestGatewayConfigValidateAcceptsShadowSamplingMethod(t *testing.T) {
	tests := []struct {
		name   string
		method ShadowSamplingMethod
	}{
		{name: "omitted", method: ""},
		{name: "random", method: ShadowSamplingMethodRandom},
		{name: "per bearer key", method: ShadowSamplingMethodPerBearerKey},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GatewayConfig{}
			cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
				"primary": {
					ModelName:            "facebook/opt-125m",
					FunctionID:           "func-id",
					ShadowModelName:      "private/facebook/opt-125m-shadow",
					ShadowSamplingMethod: tc.method,
				},
				"shadow": {
					ModelName:  "private/facebook/opt-125m-shadow",
					FunctionID: "shadow-func-id",
				},
			}

			require.NoError(t, cfg.Validate())
		})
	}
}

func TestGatewayConfigValidateRejectsInvalidShadowSamplingMethod(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:            "facebook/opt-125m",
			FunctionID:           "func-id",
			ShadowModelName:      "private/facebook/opt-125m-shadow",
			ShadowSamplingMethod: ShadowSamplingMethod("weighted"),
		},
		"shadow": {
			ModelName:  "private/facebook/opt-125m-shadow",
			FunctionID: "shadow-func-id",
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadowSamplingMethod")
}

func TestGatewayConfigValidateRejectsShadowSamplingMethodWithoutShadowTarget(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{
		"primary": {
			ModelName:            "facebook/opt-125m",
			FunctionID:           "func-id",
			ShadowSamplingMethod: ShadowSamplingMethodPerBearerKey,
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadowSamplingMethod requires at least one shadow target")
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
			name: "imageEdits per-target shadows",
			applyTo: func(cfg *GatewayConfig) {
				cfg.OpenAI.ImageEdits = map[string]ModelFunctionDetails{
					"edit": {
						ModelName:  "qwen/qwen-image-edit-2511",
						FunctionID: "edit-id",
						Shadows: []ShadowConfig{{
							ModelName: "qwen/qwen-image-edit-shadow",
						}},
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
		{
			name: "imageEdits shadowSamplingMethod",
			applyTo: func(cfg *GatewayConfig) {
				cfg.OpenAI.ImageEdits = map[string]ModelFunctionDetails{
					"edit": {
						ModelName:            "qwen/qwen-image-edit-2511",
						FunctionID:           "edit-id",
						ShadowSamplingMethod: ShadowSamplingMethodPerBearerKey,
					},
				}
			},
		},
		{
			name: "imageVariations shadowSamplingMethod",
			applyTo: func(cfg *GatewayConfig) {
				cfg.OpenAI.ImageVariations = map[string]ModelFunctionDetails{
					"var": {
						ModelName:            "qwen/qwen-image-var",
						FunctionID:           "var-id",
						ShadowSamplingMethod: ShadowSamplingMethodPerBearerKey,
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

func TestGatewayConfigValidateRejectsVanityShadowSamplingMethod(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.Vanity = map[string]VanityEntry{
		"test": {
			Host: "test.host",
			Paths: map[string]PathFunctionDetails{
				"path": {
					Path:                 "/v1/test",
					FunctionID:           "func-id",
					ShadowSamplingMethod: ShadowSamplingMethodPerBearerKey,
				},
			},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "shadow config is unsupported for vanity routes")
}
