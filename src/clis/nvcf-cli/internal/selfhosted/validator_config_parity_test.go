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

package selfhosted

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite golden files")

// networkCheckConfig mirrors NetworkCheckConfig in the validator's
// internal/clustervalidator/config.go, the schema its LoadNetworkCheckConfig
// decodes the ConfigMap into. That decode is not strict: a misspelt key is
// dropped silently, and a renamed endpoints header would lose every
// reachability endpoint with no error.
type networkCheckConfig struct {
	Reachability *struct {
		Endpoints []struct {
			Name     string `json:"name"`
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			URL      string `json:"url,omitempty"`
			Critical bool   `json:"critical,omitempty"`
		} `json:"endpoints"`
	} `json:"reachability,omitempty"`
	NetworkPolicies *struct {
		Pairs []json.RawMessage `json:"pairs"`
	} `json:"networkPolicies,omitempty"`
	Enforcement *struct {
		Enabled        bool   `json:"enabled"`
		TestImage      string `json:"testImage,omitempty"`
		TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
		Critical       bool   `json:"critical,omitempty"`
	} `json:"enforcement,omitempty"`
}

// The ConfigMap the CLI writes decodes strictly into the validator's schema,
// with every endpoint passing the validator's own checks, every one of them
// non-critical, and enforcement disabled. The golden file is the fixture a
// validator-side test can load with LoadNetworkCheckConfig.
func TestBuildControlPlaneValidatorConfig_MatchesTheValidatorsSchema(t *testing.T) {
	registries := []RegistryEntry{{Registry: "nvcr.io"}, {Registry: "harbor.example.com:8443"},
		{Registry: "[fd00::1]:5000"}}
	got := buildControlPlaneValidatorConfig(registries)

	const golden = "testdata/validator_network_checks.golden.yaml"
	if *updateGolden {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), got, "golden mismatch (run with -update-golden to refresh): %s", golden)

	var cfg networkCheckConfig
	require.NoError(t, yaml.UnmarshalStrict([]byte(got), &cfg), "every key must be one the validator reads")
	require.NotNil(t, cfg.Reachability)
	require.Len(t, cfg.Reachability.Endpoints, len(registries))
	for _, ep := range cfg.Reachability.Endpoints {
		assert.NotEmpty(t, ep.Name)
		assert.NotEmpty(t, ep.Host)
		assert.True(t, ep.Port > 0 && ep.Port <= 65535, ep.Name)
		assert.Contains(t, []string{"https", "tcp", "tcp+tls"}, strings.ToLower(ep.Protocol), ep.Name)
		assert.False(t, ep.Critical, "an in-pod dial has no proxy, so a registry is never critical: %s", ep.Name)
	}
	require.NotNil(t, cfg.Enforcement)
	assert.False(t, cfg.Enforcement.Enabled, "the CLI path never runs the enforcement test")
	assert.Nil(t, cfg.NetworkPolicies)
}
