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
	"context"
	"sync"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted"
)

// The Job gets what the chart CronJob forwards: the post-install signal on
// every run but --pre, relocated namespaces, the Gateway override and the
// probe image.
func TestClusterValidatorJobEnv(t *testing.T) {
	prevPre := checkPre
	t.Cleanup(func() {
		checkPre = prevPre
		viper.Set("cluster_validator_probe_image", "")
	})
	for _, k := range []string{"NVCF_OPENBAO_NAMESPACE", "NVCF_ENVOY_GATEWAY_NAMESPACE", "NVCF_GATEWAY_NAMES", "NVCF_N2N_PROBE_IMAGE"} {
		t.Setenv(k, "")
	}

	checkPre = false
	env := clusterValidatorJobEnv(selfhosted.StackValues{EnvoyGatewayNamespace: "gateway"})
	assert.Equal(t, "true", env["VALIDATOR_POST_INSTALL"], "a post-install run must say so")
	assert.Equal(t, "gateway", env["NVCF_ENVOY_GATEWAY_NAMESPACE"], "the stack's controller namespace is forwarded")
	assert.NotContains(t, env, "NVCF_OPENBAO_NAMESPACE")

	checkPre = true
	t.Setenv("NVCF_OPENBAO_NAMESPACE", "openbao")
	t.Setenv("NVCF_ENVOY_GATEWAY_NAMESPACE", "edge")
	t.Setenv("NVCF_GATEWAY_NAMES", "gateway/shared-gw")
	viper.Set("cluster_validator_probe_image", "mirror.example/busybox:1.36")
	env = clusterValidatorJobEnv(selfhosted.StackValues{EnvoyGatewayNamespace: "gateway"})
	assert.NotContains(t, env, "VALIDATOR_POST_INSTALL", "--pre is pre-install")
	assert.Equal(t, "openbao", env["NVCF_OPENBAO_NAMESPACE"])
	assert.Equal(t, "edge", env["NVCF_ENVOY_GATEWAY_NAMESPACE"], "an explicit setting beats the stack values")
	assert.Equal(t, "gateway/shared-gw", env["NVCF_GATEWAY_NAMES"])
	assert.Equal(t, "mirror.example/busybox:1.36", env["NVCF_N2N_PROBE_IMAGE"])
}

// The control-plane validator is handed the registries the credential check
// enumerated (here the validator image's), and the forwarded env. A post-install
// --control-plane run must carry VALIDATOR_POST_INSTALL.
func TestCheck_ControlPlaneValidatorGetsRegistriesAndEnv(t *testing.T) {
	resetCheckFlags(t)
	var mu sync.Mutex
	got := map[string]selfhosted.ClusterValidatorParams{}
	prev := newClusterValidatorForSelfHosted
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(_ context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			mu.Lock()
			defer mu.Unlock()
			got[p.Role] = p
			return selfhosted.ClusterValidatorResult{Passed: true}
		}
	}
	t.Cleanup(func() { newClusterValidatorForSelfHosted = prev })
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")

	var errBuf bytes.Buffer
	rootCmd.SetErr(&errBuf)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "check", "--control-plane", "--json",
		"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"})
	_ = rootCmd.Execute()

	cp, ok := got["control-plane"]
	require.True(t, ok, "the control-plane validator must run; output: %s", errBuf.String())
	var regs []string
	for _, r := range cp.Registries {
		regs = append(regs, r.Registry)
	}
	assert.Contains(t, regs, "nvcr.io", "the enumerated registries must reach the validator")
	assert.Equal(t, "true", cp.Env["VALIDATOR_POST_INSTALL"])
}

// NVCF_GATEWAY_NAMES written as a YAML list in the config file reaches the Job
// in the comma form the validator parses, not as an empty string.
func TestClusterValidatorJobEnv_GatewayNamesYAMLList(t *testing.T) {
	t.Setenv("NVCF_GATEWAY_NAMES", "")
	viper.Set("NVCF_GATEWAY_NAMES", []any{"gateway/nvcf-gw", " edge/shared ", ""})
	t.Cleanup(func() { viper.Set("NVCF_GATEWAY_NAMES", nil) })

	env := clusterValidatorJobEnv(selfhosted.StackValues{})
	assert.Equal(t, "gateway/nvcf-gw,edge/shared", env["NVCF_GATEWAY_NAMES"])
}

// --pre in split mode visits both clusters, so each gets its own validator
// role against its own context, both marked pre-install.
func TestCheck_PreSplitRunsBothValidatorRoles(t *testing.T) {
	resetCheckFlags(t)
	var mu sync.Mutex
	got := map[string]selfhosted.ClusterValidatorParams{}
	prev := newClusterValidatorForSelfHosted
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(_ context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			mu.Lock()
			defer mu.Unlock()
			got[p.Role] = p
			return selfhosted.ClusterValidatorResult{Passed: true}
		}
	}
	t.Cleanup(func() { newClusterValidatorForSelfHosted = prev })
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")

	var errBuf bytes.Buffer
	rootCmd.SetErr(&errBuf)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json",
		"--control-plane-context", "cp-ctx", "--compute-plane-context", "gpu-ctx",
		"--icms-url", "https://sis.example.invalid",
		"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"})
	_ = rootCmd.Execute()

	mu.Lock()
	defer mu.Unlock()
	cp, ok := got["control-plane"]
	require.True(t, ok, "the control-plane validator must run; output: %s", errBuf.String())
	gpu, ok := got["compute-plane"]
	require.True(t, ok, "the compute-plane validator must run; output: %s", errBuf.String())
	assert.Equal(t, "cp-ctx", cp.KubeContext)
	assert.Equal(t, "gpu-ctx", gpu.KubeContext)
	assert.NotContains(t, cp.Env, "VALIDATOR_POST_INSTALL", "--pre is pre-install")
}

// NVCF_CLI_CLUSTER_VALIDATOR_REGISTRIES is read, and a comma-separated value is
// split into separate registries rather than parsed as one host.
func TestCheck_RegistriesEnvVarIsSplitOnCommas(t *testing.T) {
	resetCheckFlags(t)
	t.Setenv("NVCF_CLI_CLUSTER_VALIDATOR_REGISTRIES", "harbor.example.com:443, ghcr.io:443")
	var mu sync.Mutex
	var got selfhosted.ClusterValidatorParams
	prev := newClusterValidatorForSelfHosted
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(_ context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			mu.Lock()
			defer mu.Unlock()
			if p.Role == "control-plane" {
				got = p
			}
			return selfhosted.ClusterValidatorResult{Passed: true}
		}
	}
	t.Cleanup(func() { newClusterValidatorForSelfHosted = prev })
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")

	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "check", "--control-plane", "--json",
		"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"})
	_ = rootCmd.Execute()

	mu.Lock()
	defer mu.Unlock()
	var regs []string
	for _, r := range got.Registries {
		regs = append(regs, r.Registry)
	}
	assert.Contains(t, regs, "harbor.example.com")
	assert.Contains(t, regs, "ghcr.io")
}
