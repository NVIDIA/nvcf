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

package function

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreationQueueMessageMetadata(t *testing.T) {
	metadata := common.CreationQueueMessageMetadata{RequestID: "req-1", NCAID: "nca-1"}
	msg := CreationQueueMessage{CreationQueueMessageMetadata: metadata}

	assert.Equal(t, metadata, msg.GetCreationQueueMessageMetadata())
}

func TestModelsUnmarshalJSON(t *testing.T) {
	rawModels := `[{"name":"llama","version":"1","uri":"oci://model"}]`

	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "json", data: []byte(rawModels)},
		{name: "base64", data: []byte(strconvQuote(base64.StdEncoding.EncodeToString([]byte(rawModels))))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var models Models
			require.NoError(t, json.Unmarshal(tt.data, &models))
			require.Len(t, models, 1)
			assert.Equal(t, "llama", models[0].Name)
			assert.Equal(t, "1", models[0].Version)
			assert.Equal(t, "oci://model", models[0].URI)
		})
	}

	var empty Models
	require.NoError(t, empty.UnmarshalJSON(nil))
	assert.Empty(t, empty)

	var invalid Models
	assert.Error(t, invalid.UnmarshalJSON([]byte(strconvQuote("not base64"))))
}

func TestFunctionDeepCopy(t *testing.T) {
	telemetries := &common.TelemetriesLaunchSpecification{}
	telemetries.Telemetries.Metrics = &common.Telemetry{
		Protocol: "http",
		Provider: "DATADOG",
		Endpoint: "datadoghq.com",
		Name:     "metrics",
	}
	src := &LaunchSpecification{
		EnvironmentB64: "env",
		HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
			HelmChartURL: "https://helm.example.com/chart.tgz",
			Values:       []byte("replicas: 1"),
		},
		CacheLaunchSpecification: &common.CacheLaunchSpecification{
			CacheArtifacts: true,
			CacheHandle:    "cache-handle",
			CacheSize:      1 << 30,
		},
		Telemetries: telemetries,
		Models: Models{
			{
				Name: "model-a",
				LLMModelConfig: &LLMModelConfig{
					URIs:      []string{"oci://model-a"},
					Tokenizer: "tokenizer-a",
				},
			},
		},
	}

	copied := src.DeepCopy()
	require.NotNil(t, copied)
	src.HelmChartLaunchSpecification.Values[0] = 'R'
	src.CacheLaunchSpecification.CacheHandle = "changed"
	src.Telemetries.Telemetries.Metrics.Name = "changed"
	src.Models[0].LLMModelConfig.URIs[0] = "changed"

	assert.Equal(t, []byte("replicas: 1"), copied.HelmChartLaunchSpecification.Values)
	assert.Equal(t, "cache-handle", copied.CacheLaunchSpecification.CacheHandle)
	assert.Equal(t, "metrics", copied.Telemetries.Telemetries.Metrics.Name)
	assert.Equal(t, "oci://model-a", copied.Models[0].LLMModelConfig.URIs[0])

	llmConfig := (&LLMModelConfig{URIs: []string{"oci://standalone"}, Tokenizer: "tokenizer"}).DeepCopy()
	require.NotNil(t, llmConfig)
	assert.Equal(t, "oci://standalone", llmConfig.URIs[0])

	artifacts := LaunchArtifacts{
		{Type: LaunchArtifactTypePod, Specification: "pod"},
		{Type: LaunchArtifactTypeSecret, Specification: "secret"},
	}
	copiedArtifacts := artifacts.DeepCopy()
	artifacts[0].Specification = "changed"
	assert.Equal(t, LaunchArtifacts{
		{Type: LaunchArtifactTypePod, Specification: "pod"},
		{Type: LaunchArtifactTypeSecret, Specification: "secret"},
	}, copiedArtifacts)

	model := (&Model{Name: "model-b", LLMModelConfig: &LLMModelConfig{URIs: []string{"oci://model-b"}}}).DeepCopy()
	require.NotNil(t, model)
	assert.Equal(t, "oci://model-b", model.LLMModelConfig.URIs[0])

	models := Models{{Name: "model-c", LLMModelConfig: &LLMModelConfig{URIs: []string{"oci://model-c"}}}}
	copiedModels := models.DeepCopy()
	models[0].LLMModelConfig.URIs[0] = "changed"
	assert.Equal(t, "oci://model-c", copiedModels[0].LLMModelConfig.URIs[0])

	details := (&Details{FunctionID: "function-id", FunctionVersionID: "version-id"}).DeepCopy()
	require.NotNil(t, details)
	assert.Equal(t, "function-id", details.FunctionID)

	var nilDetails *Details
	var nilConfig *LLMModelConfig
	var nilLaunchArtifacts LaunchArtifacts
	var nilLaunchSpec *LaunchSpecification
	var nilModel *Model
	var nilModels Models
	assert.Nil(t, nilDetails.DeepCopy())
	assert.Nil(t, nilConfig.DeepCopy())
	assert.Nil(t, nilLaunchArtifacts.DeepCopy())
	assert.Nil(t, nilLaunchSpec.DeepCopy())
	assert.Nil(t, nilModel.DeepCopy())
	assert.Nil(t, nilModels.DeepCopy())
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
