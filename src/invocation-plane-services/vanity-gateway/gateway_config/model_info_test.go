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
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelInfoYAMLValues(t *testing.T) {
	var cfg GatewayConfig
	err := unmarshalGatewayConfig([]byte(`v2config:
  openai:
    chatCompletions:
      example:
        modelName: example/model
        modelInfoFields:
          custom_flag: true
          disabled: false
          context_window: 1048576
          max_output_tokens: 32768
          large_integer: 18446744073709551615
          larger_integer: 18446744073709551616
          negative_integer: -9223372036854775808
          fraction: 0.12345678901234567890123456789
          exponent: 1.25e+100
          text: '1048576'
          empty: null
          '': empty-key
          'on': 'yes'
          nested: {items: [true, null, 42, 'text', {flag: false}]}
`), &cfg)
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	fields := cfg.OpenAI.ChatCompletions["example"].ModelInfoFields
	for key, expected := range map[string]string{
		"custom_flag": "true", "disabled": "false", "context_window": "1048576", "max_output_tokens": "32768",
		"large_integer": "18446744073709551615", "larger_integer": "18446744073709551616",
		"negative_integer": "-9223372036854775808", "fraction": "0.12345678901234567890123456789",
		"exponent": "1.25e+100", "text": `"1048576"`, "empty": "null", "": `"empty-key"`, "on": `"yes"`,
		"nested": `{"items":[true,null,42,"text",{"flag":false}]}`,
	} {
		require.Equal(t, expected, string(fields[key]), key)
	}
}

func TestModelInfoYAMLRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{
		`[]`, `true`, `"text"`, `42`,
		`{flag: .nan}`, `{flag: .inf}`, `{flag: -.inf}`,
		`{1: value}`, `{flag: {true: value}}`, `{flag: [{2: value}]}`,
		`{flag: !!timestamp 2026-01-01}`, `{flag: !!binary aGVsbG8=}`,
		`{flag: 1, flag: 2}`,
	} {
		t.Run(value, func(t *testing.T) {
			var cfg GatewayConfig
			err := unmarshalGatewayConfig([]byte(fmt.Sprintf(`v2config:
  openai:
    responses:
      example:
        modelName: example/model
        modelInfoFields: %s
`, value)), &cfg)
			require.Error(t, err)
		})
	}
}

func TestModelInfoReservedFields(t *testing.T) {
	for _, section := range modelInfoSectionOrder {
		for _, field := range []string{"id", "object", "created", "owned_by", "supported_endpoints", "visibility", "routes", "ID", "Supported_Endpoints", "Visibility", "ROUTES"} {
			t.Run(section+"/"+field, func(t *testing.T) {
				var cfg GatewayConfig
				var entry ModelFunctionDetails
				require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"modelName":"example/model","modelInfoFields":{%q:null}}`, field)), &entry))
				cfg.OpenAI.ChatCompletions = map[string]ModelFunctionDetails{}
				cfg.OpenAI.Completions = map[string]ModelFunctionDetails{}
				cfg.OpenAI.Embeddings = map[string]ModelFunctionDetails{}
				cfg.OpenAI.Responses = map[string]ModelFunctionDetails{}
				cfg.OpenAI.ImageGenerations = map[string]ModelFunctionDetails{}
				cfg.OpenAI.ImageEdits = map[string]ModelFunctionDetails{}
				cfg.OpenAI.ImageVariations = map[string]ModelFunctionDetails{}
				cfg.AnthropicMessages = map[string]ModelFunctionDetails{}
				cfg.modelSections()[section]["example"] = entry
				err := cfg.Validate()
				require.ErrorContains(t, err, "example/model")
				require.ErrorContains(t, err, field)
				require.ErrorContains(t, err, "reserved")
			})
		}
	}
}

func TestModelInfoAliasMergeOrderAndSnapshot(t *testing.T) {
	var cfg GatewayConfig
	require.NoError(t, unmarshalGatewayConfig([]byte(`v2config:
  openai:
    chatCompletions:
      z-first:
        modelName: example/shared
        modelInfoFields: {winner: first, null_winner: null, nested: {flag: true}}
      a-second:
        modelName: example/shared
        modelInfoFields: {winner: second, null_winner: false, second: true}
    completions:
      entry:
        modelName: example/shared
        modelInfoFields: {winner: completions, completion: true}
    embeddings:
      entry:
        modelName: example/shared
        modelInfoFields: {winner: embeddings, embedding: true}
    responses:
      entry:
        modelName: example/shared
        modelInfoFields: {winner: responses, response: true}
    imageGenerations:
      entry:
        modelName: example/shared
        modelInfoFields: {winner: images, image: true, image_winner: generation}
    imageEdits:
      entry:
        modelName: example/shared
        modelInfoFields: {edit: true, image_winner: edit}
    imageVariations:
      entry:
        modelName: example/shared
        modelInfoFields: {variation: true, image_winner: variation}
  anthropicMessages:
    entry:
      modelName: example/shared
      modelInfoFields: {winner: messages, message: true, image_winner: messages}
`), &cfg))
	require.NoError(t, cfg.Validate())
	fields := cfg.ModelInfoFieldsByName()["example/shared"]
	require.Equal(t, `"first"`, string(fields["winner"]))
	require.Equal(t, "null", string(fields["null_winner"]))
	require.Equal(t, `"generation"`, string(fields["image_winner"]))
	for _, key := range []string{"second", "completion", "embedding", "response", "image", "edit", "variation", "message"} {
		require.Equal(t, "true", string(fields[key]), key)
	}
	fields["nested"][0] = '['
	delete(fields, "winner")
	fresh := cfg.ModelInfoFieldsByName()["example/shared"]
	require.Equal(t, `{"flag":true}`, string(fresh["nested"]))
	require.Equal(t, `"first"`, string(fresh["winner"]))
}

func TestModelInfoAbsentNullAndEmpty(t *testing.T) {
	for _, suffix := range []string{"", `,"modelInfoFields":null`, `,"modelInfoFields":{}`} {
		var cfg GatewayConfig
		require.NoError(t, json.Unmarshal([]byte(`{"v2config":{"openai":{"chatCompletions":{"example":{"modelName":"example/model"`+suffix+`}}}}}`), &cfg))
		require.NoError(t, cfg.Validate())
		require.Empty(t, cfg.ModelInfoFieldsByName())
	}
}

func TestModelInfoSharedFixtures(t *testing.T) {
	data, err := os.ReadFile("../testdata/model_info/valid.yaml")
	require.NoError(t, err)
	var cfg GatewayConfig
	require.NoError(t, unmarshalGatewayConfig(data, &cfg))
	require.NoError(t, cfg.Validate())
	expectedData, err := os.ReadFile("../testdata/model_info/expected.json")
	require.NoError(t, err)
	var expected map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(expectedData, &expected))
	actual := cfg.ModelInfoFieldsByName()
	for name, fields := range expected {
		for key, value := range fields {
			require.JSONEq(t, string(value), string(actual[name][key]), name+"/"+key)
		}
		require.Len(t, actual[name], len(fields), name)
	}
	invalidData, err := os.ReadFile("../testdata/model_info/invalid.json")
	require.NoError(t, err)
	var invalid map[string]string
	require.NoError(t, json.Unmarshal(invalidData, &invalid))
	for name, data := range invalid {
		t.Run(name, func(t *testing.T) {
			var cfg GatewayConfig
			err := unmarshalGatewayConfig([]byte(data), &cfg)
			if err == nil {
				err = cfg.Validate()
			}
			require.Error(t, err)
		})
	}
}

func TestModelInfoYAMLEmptyCompatibility(t *testing.T) {
	for _, field := range []string{"", "        modelInfoFields: null\n", "        modelInfoFields: {}\n"} {
		var cfg GatewayConfig
		require.NoError(t, unmarshalGatewayConfig([]byte("v2config:\n  openai:\n    responses:\n      entry:\n        modelName: example/model\n"+field), &cfg))
		require.NoError(t, cfg.Validate())
		require.Empty(t, cfg.ModelInfoFieldsByName())
	}
}
