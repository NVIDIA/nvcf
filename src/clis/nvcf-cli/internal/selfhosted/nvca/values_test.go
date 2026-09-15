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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func marshalValues(t *testing.T, values Values) string {
	t.Helper()
	body, err := yaml.Marshal(values)
	require.NoError(t, err)
	return string(body)
}

// TestTransportTLSOmittedWhenNil verifies that an absent transportTls renders
// no legacy selfManaged.transportTls block.
func TestTransportTLSOmittedWhenNil(t *testing.T) {
	out := marshalValues(t, Values{SelfManaged: SelfManagedValues{IdentitySource: "psat"}})
	require.NotContains(t, out, "transportTls")
}

// TestTransportTLSAgentConfigMergeRendersFingerprintAndPEM verifies bundle mode
// renders the fingerprint and PEM under agentConfig.mergeConfig for NVCA config
// merging rather than under selfManaged.* Helm values.
func TestTransportTLSAgentConfigMergeRendersFingerprintAndPEM(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	out := marshalValues(t, Values{
		SelfManaged: SelfManagedValues{IdentitySource: "psat"},
		AgentConfig: &AgentConfigValues{
			MergeConfig: "workload:\n  transportTLS:\n    trustMode: bundle\n    trustBundleFingerprint: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    trustBundlePem: |\n      " + pem,
		},
	})
	require.Contains(t, out, "agentConfig:")
	require.Contains(t, out, "mergeConfig:")
	require.Contains(t, out, "trustMode: bundle")
	require.Contains(t, out, "trustBundleFingerprint: sha256:0123456789abcdef")
	require.Contains(t, out, "-----BEGIN CERTIFICATE-----")
	require.NotContains(t, out, "transportTls:")
}

// TestAgentRequestRouterAddressRendered verifies agent.llm.requestRouterAddress
// is rendered when set and the agent section is omitted entirely when nil.
func TestAgentRequestRouterAddressRendered(t *testing.T) {
	// Nil agent section is omitted.
	require.NotContains(t, marshalValues(t, Values{SelfManaged: SelfManagedValues{IdentitySource: "psat"}}), "agent:")

	body, err := yaml.Marshal(Values{
		Agent: &AgentValues{LLM: &AgentLLMValues{RequestRouterAddress: "llm-request-router.nvcf-cp.internal:50071"}},
	})
	require.NoError(t, err)
	out := string(body)
	require.Contains(t, out, "agent:")
	require.Contains(t, out, "llm:")
	require.Contains(t, out, "requestRouterAddress: llm-request-router.nvcf-cp.internal:50071")
}

// TestWriteFileRoundTrip verifies WriteFile emits parseable YAML carrying the
// agent config merge block.
func TestWriteFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nvca-values.yaml")
	require.NoError(t, WriteFile(path, Values{
		ClusterID:      "c-1",
		ClusterGroupID: "g-1",
		SelfManaged: SelfManagedValues{
			IdentitySource: "psat",
		},
		AgentConfig: &AgentConfigValues{MergeConfig: "workload:\n  transportTLS:\n    trustMode: system\n"},
	}))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var got Values
	require.NoError(t, yaml.Unmarshal(body, &got))
	require.NotNil(t, got.AgentConfig)
	require.Contains(t, got.AgentConfig.MergeConfig, "trustMode: system")
}
