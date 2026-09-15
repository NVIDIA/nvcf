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
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

var scheme = runtime.NewScheme()

func init() {
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
}

const (
	FunctionTypeDefault   = "DEFAULT"
	FunctionTypeStreaming = "STREAMING"
	FunctionTypeLLM       = "LLM"

	// Volume mount paths
	InferenceDirPath = "/var/inf"
	ConfigDirPath    = "/config/shared"

	trueVal = "true"
)

const (
	inferenceContainerName = "inference"
)

type CreationQueueMessage struct {
	common.CreationQueueMessageMetadata `json:",inline"`

	Details Details `json:"functionDetails"`

	LaunchArtifacts     LaunchArtifacts      `json:"launchArtifacts,omitempty"`
	LaunchSpecification *LaunchSpecification `json:"launchSpecification,omitempty"`
}

func (m CreationQueueMessage) GetCreationQueueMessageMetadata() common.CreationQueueMessageMetadata {
	return m.CreationQueueMessageMetadata
}

// +k8s:deepcopy-gen=true
type Details struct {
	FunctionID        string `json:"functionId"`
	FunctionVersionID string `json:"functionVersionId"`
	FunctionType      string `json:"functionType"`
}

// +k8s:deepcopy-gen=true
type LaunchSpecification struct {
	// Container function details are baked into the environment.
	EnvironmentB64  string `json:"environment"`
	ICMSEnvironment string `json:"icmsEnvironment"`
	CloudProvider   string `json:"cloudProvider"`
	GPUName         string `json:"gpuName"`

	// Helm chart function components of the launch spec.
	*common.HelmChartLaunchSpecification `json:",inline"`
	// Cache object configuration metadata of the launch spec.
	*common.CacheLaunchSpecification `json:",inline"`
	// Telemetry configuration metadata.
	Telemetries *common.TelemetriesLaunchSpecification `json:"telemetries,omitempty"`

	// Models launch specification metadata.
	// This is only used by the translate lib for LLM functions currently.
	Models Models `json:"models,omitempty"`
}

// Models is the launch specification for function models.
// +k8s:deepcopy-gen=true
type Models []Model

var (
	_ json.Unmarshaler = (*Models)(nil)
)

func (v *Models) UnmarshalJSON(data []byte) error {
	if v == nil || len(data) == 0 {
		return nil
	}
	if !common.HasJSONPrefix(data) {
		dataB64Str, err := strconv.Unquote(string(data))
		if err != nil {
			dataB64Str = string(data)
		}
		if data, err = base64.StdEncoding.DecodeString(dataB64Str); err != nil {
			return err
		}
	}
	// Use a temporary type to avoid recursion
	var tmp []Model
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*v = tmp
	return nil
}

// Model is a specification for a specific model.
// +k8s:deepcopy-gen=true
type Model struct {
	Name           string          `json:"name"`
	Version        string          `json:"version"`
	URI            string          `json:"uri"`
	LLMModelConfig *LLMModelConfig `json:"llmConfig,omitempty"`
}

// LLMModelConfig is the LLM function type-specific configuration.
// +k8s:deepcopy-gen=true
type LLMModelConfig struct {
	URIs           []string `json:"uris"`
	Tokenizer      string   `json:"tokenizer"`
	TokenRateLimit string   `json:"tokenRateLimit"`
	RoutingMethod  string   `json:"routingMethod"`
}
