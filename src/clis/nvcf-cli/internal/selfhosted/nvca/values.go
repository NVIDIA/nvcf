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

package nvca

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Values struct {
	ClusterName    string             `yaml:"clusterName,omitempty"`
	ClusterID      string             `yaml:"clusterID"`
	ClusterGroupID string             `yaml:"clusterGroupID"`
	NCAID          string             `yaml:"ncaID"`
	Region         string             `yaml:"region"`
	SelfManaged    SelfManagedValues  `yaml:"selfManaged"`
	Agent          *AgentValues       `yaml:"agent,omitempty"`
	AgentConfig    *AgentConfigValues `yaml:"agentConfig,omitempty"`
}

// AgentValues carries the top-level agent.* nvca-operator values rendered by the
// CLI. It is a pointer so an empty agent section is omitted entirely.
type AgentValues struct {
	LLM *AgentLLMValues `yaml:"llm,omitempty"`
}

// AgentLLMValues carries agent.llm.* values. RequestRouterAddress is the default
// host:port LLM request router (Stargate) address for LLM workloads, consumed by
// the nvca-operator chart under agent.llm.requestRouterAddress.
type AgentLLMValues struct {
	RequestRouterAddress string `yaml:"requestRouterAddress,omitempty"`
}

// AgentConfigValues carries the top-level agentConfig.* nvca-operator values
// rendered by the CLI. MergeConfig is a literal NVCA config YAML fragment.
type AgentConfigValues struct {
	MergeConfig string `yaml:"mergeConfig,omitempty"`
}

type SelfManagedValues struct {
	IdentitySource                 string `yaml:"identitySource"`
	ICMSServiceURL                 string `yaml:"icmsServiceURL,omitempty"`
	ICMSServiceHostHeaderOverride  string `yaml:"icmsServiceHostHeaderOverride,omitempty"`
	ReValServiceURL                string `yaml:"revalServiceURL,omitempty"`
	ReValServiceHostHeaderOverride string `yaml:"revalServiceHostHeaderOverride,omitempty"`
	NATSURL                        string `yaml:"natsURL,omitempty"`
	NATSHostOverride               string `yaml:"natsHostOverride,omitempty"`
}

func WriteFile(path string, values Values) error {
	body, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshal NVCA values: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
