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
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"nvcf-cli/internal/selfhosted"
)

// The Job gets what the chart CronJob forwards: the post-install signal on
// every run but --pre, relocated namespaces, the Gateway override and the
// probe image.
func TestClusterValidatorJobEnv(t *testing.T) {
	resetCheckFlags(t)
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
	require.NoError(t, selfHostedCheckCmd.Flags().Set("cluster-validator-probe-image", "mirror.example/busybox:1.36"))
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
// role against its own context. A bare --pre is pre-install for both; --all
// checks both as installed, and a role's own flag checks that role as
// installed while the other stays pre-install.
func TestCheck_PreSplitRunsBothValidatorRoles(t *testing.T) {
	for _, tc := range []struct {
		name            string
		extra           []string
		cpPost, gpuPost bool
	}{
		{name: "bare pre", extra: []string{"--icms-url", "https://sis.example.invalid"}},
		{name: "pre all", extra: []string{"--all", "--cluster-name", "gpu"}, cpPost: true, gpuPost: true},
		{name: "pre control-plane", extra: []string{"--control-plane"}, cpPost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			rootCmd.SetArgs(append([]string{"self-hosted", "check", "--pre", "--json",
				"--control-plane-context", "cp-ctx", "--compute-plane-context", "gpu-ctx",
				"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"},
				tc.extra...))
			_ = rootCmd.Execute()

			mu.Lock()
			defer mu.Unlock()
			cp, ok := got["control-plane"]
			require.True(t, ok, "the control-plane validator must run; output: %s", errBuf.String())
			gpu, ok := got["compute-plane"]
			require.True(t, ok, "the compute-plane validator must run; output: %s", errBuf.String())
			assert.Equal(t, "cp-ctx", cp.KubeContext)
			assert.Equal(t, "gpu-ctx", gpu.KubeContext)
			assert.Equal(t, tc.cpPost, cp.Env["VALIDATOR_POST_INSTALL"] == "true", "control-plane post-install")
			assert.Equal(t, tc.gpuPost, gpu.Env["VALIDATOR_POST_INSTALL"] == "true", "compute-plane post-install")
		})
	}
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

// The stack's Gateways reach the Job; an explicit setting wins. No HA mode is
// forwarded: the validator reads it from the anti-affinity the stack renders.
func TestClusterValidatorJobEnv_ForwardsStackGateways(t *testing.T) {
	t.Setenv("NVCF_GATEWAY_NAMES", "")
	t.Setenv("NVCF_HA_MODE", "preferred")
	stack := selfhosted.StackValues{Gateways: []string{"gw/grpc-gw", "gw/shared-gw"}}
	env := clusterValidatorJobEnv(stack)
	assert.Equal(t, "gw/grpc-gw,gw/shared-gw", env["NVCF_GATEWAY_NAMES"])
	assert.NotContains(t, env, "NVCF_HA_MODE")

	t.Setenv("NVCF_GATEWAY_NAMES", "edge/nvcf-gw")
	env = clusterValidatorJobEnv(stack)
	assert.Equal(t, "edge/nvcf-gw", env["NVCF_GATEWAY_NAMES"])
}

// --cluster-validator-tolerations reaches the validator Job of both roles, so
// a cluster whose nodes carry other taints can schedule it, as the chart's
// clusterValidator.tolerations allows. A malformed entry fails the command
// before anything runs.
func TestCheck_ValidatorTolerationsReachBothRoles(t *testing.T) {
	resetCheckFlags(t)
	var mu sync.Mutex
	got := map[string][]corev1.Toleration{}
	prev := newClusterValidatorForSelfHosted
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(_ context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			mu.Lock()
			defer mu.Unlock()
			got[p.Role] = p.Tolerations
			return selfhosted.ClusterValidatorResult{Passed: true}
		}
	}
	t.Cleanup(func() { newClusterValidatorForSelfHosted = prev })
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")

	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json",
		"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0",
		"--cluster-validator-tolerations", "dedicated=infra:NoSchedule,special"})
	_ = rootCmd.Execute()

	want := []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "infra", Effect: corev1.TaintEffectNoSchedule},
		{Key: "special", Operator: corev1.TolerationOpExists},
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, want, got["control-plane"])
	assert.Equal(t, want, got["compute-plane"])

	resetCheckFlags(t)
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json",
		"--cluster-validator-tolerations", "dedicated:Sometimes"})
	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "effect must be NoSchedule, PreferNoSchedule or NoExecute")
}

func TestParseToleration(t *testing.T) {
	for in, want := range map[string]corev1.Toleration{
		"dedicated=infra:NoSchedule": {Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "infra",
			Effect: corev1.TaintEffectNoSchedule},
		"example.com/role:noexecute": {Key: "example.com/role", Operator: corev1.TolerationOpExists,
			Effect: corev1.TaintEffectNoExecute},
		"special": {Key: "special", Operator: corev1.TolerationOpExists},
	} {
		got, err := parseToleration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{
		":NoSchedule", "=v", "k:Never",
		"dedicated=infra team:NoSchedule", "bad key", "key=v=w", "a/b/c", "-key", "k=-v",
	} {
		_, err := parseToleration(in)
		assert.Error(t, err, in)
	}
}

// A toleration the apiserver would reject fails the command as a usage error,
// before the run creates anything for a validator Job that cannot be created.
func TestCheck_MalformedTolerationFailsBeforeTheRun(t *testing.T) {
	var calls atomic.Int32
	prev := newClusterValidatorForSelfHosted
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(context.Context, selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			calls.Add(1)
			return selfhosted.ClusterValidatorResult{Passed: true}
		}
	}
	t.Cleanup(func() { newClusterValidatorForSelfHosted = prev })
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	for _, entry := range []string{"dedicated=infra team:NoSchedule", "bad key", "key=v=w", "a/b/c"} {
		resetCheckFlags(t)
		rootCmd.SetArgs([]string{"self-hosted", "check", "--control-plane", "--json",
			"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0",
			"--cluster-validator-tolerations", entry})
		err := rootCmd.Execute()
		require.Error(t, err, entry)
		assert.Contains(t, err.Error(), "--cluster-validator-tolerations", entry)
		var exitErr *ExitCodeError
		assert.False(t, errors.As(err, &exitErr), "%s: a usage error, not a failed run", entry)
	}
	assert.Zero(t, calls.Load(), "no validator runs")
}

// The inotify probe pods use the configured probe image, so a cluster that
// pulls from a mirror can run them; with none set the probe keeps its default.
func TestCheck_InotifyProbeUsesTheProbeImage(t *testing.T) {
	for _, tc := range []struct {
		name, flag, env, want string
	}{
		{name: "flag", flag: "mirror.example/busybox:1.36", want: "mirror.example/busybox:1.36"},
		{name: "validator setting", env: "mirror.example/busybox:1.36", want: "mirror.example/busybox:1.36"},
		{name: "unset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetCheckFlags(t)
			t.Setenv("NVCF_N2N_PROBE_IMAGE", tc.env)
			var got []string
			prev := newInotifyProberForSelfHosted
			newInotifyProberForSelfHosted = func(image string) selfhosted.NodeInotifyProber {
				got = append(got, image)
				return func(context.Context, string) ([]selfhosted.NodeInotifyLimits, error) { return nil, nil }
			}
			t.Cleanup(func() { newInotifyProberForSelfHosted = prev })
			rootCmd.SetErr(&bytes.Buffer{})
			rootCmd.SetOut(&bytes.Buffer{})
			args := []string{"self-hosted", "check", "--pre", "--json"}
			if tc.flag != "" {
				args = append(args, "--cluster-validator-probe-image", tc.flag)
			}
			rootCmd.SetArgs(args)
			_ = rootCmd.Execute()
			assert.Equal(t, []string{tc.want}, got)
		})
	}
}

// Stack values reach the validator only when the install's environment file
// was read from the stack the command points at. helmfile refuses to install
// without that file, so base.yaml alone does not describe the install, and a
// stack found above the working directory is not a remote stack the command
// was given. Another stack's Gateways would have the validator fail ones the
// install never created.
func TestCheck_StackGatewaysNeedTheEnvironmentFile(t *testing.T) {
	t.Setenv("HELMFILE_ENV", "")
	t.Setenv("NVCF_GATEWAY_NAMES", "")
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	stack := t.TempDir()
	envDir := filepath.Join(stack, "environments")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "base.yaml"), []byte(`
ingress:
  gatewayApi:
    enabled: true
    gateways:
      shared: {name: nvcf-gateway, namespace: envoy-gateway}
      grpc: {name: nvcf-gateway, namespace: envoy-gateway}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "local.yaml"), []byte("global: {}\n"), 0o644))
	t.Setenv("NVCF_CLI_DEFAULT_CONTROL_PLANE_STACK", "")
	prev := newClusterValidatorForSelfHosted
	t.Cleanup(func() { newClusterValidatorForSelfHosted = prev })
	run := func(env, stackFlag string) map[string]string {
		resetCheckFlags(t)
		var got map[string]string
		newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
			return func(_ context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
				got = p.Env
				return selfhosted.ClusterValidatorResult{Passed: true}
			}
		}
		rootCmd.SetErr(&bytes.Buffer{})
		rootCmd.SetOut(&bytes.Buffer{})
		args := []string{"self-hosted", "check", "--control-plane", "--json",
			"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"}
		if env != "" {
			args = append(args, "--env", env)
		}
		if stackFlag != "" {
			args = append(args, "--control-plane-stack", stackFlag)
		}
		rootCmd.SetArgs(args)
		_ = rootCmd.Execute()
		require.NotNil(t, got, "the validator ran")
		return got
	}

	assert.NotContains(t, run("prod", stack), "NVCF_GATEWAY_NAMES", "base.yaml without prod.yaml is not the install")
	assert.NotContains(t, run("", stack), "NVCF_GATEWAY_NAMES",
		"the defaulted --env is not the install's environment, even with a local.yaml in the stack")

	require.NoError(t, os.WriteFile(filepath.Join(envDir, "prod.yaml"), []byte("global: {domain: example.com}\n"), 0o644))
	assert.Equal(t, "envoy-gateway/nvcf-gateway", run("prod", stack)["NVCF_GATEWAY_NAMES"])

	// The same stack found above the working directory is not the remote
	// stack the command was given, nor a local one without environments,
	// nor the built-in stack a release CLI installs from.
	t.Chdir(stack)
	assert.NotContains(t, run("prod", "oci://registry.example.com/nvcf/stack:1.0.0"), "NVCF_GATEWAY_NAMES")
	assert.NotContains(t, run("prod", t.TempDir()), "NVCF_GATEWAY_NAMES")
	assert.NotContains(t, run("prod", "file://"+stack), "NVCF_GATEWAY_NAMES",
		"a file:// stack is cloned from its committed HEAD, which the working tree can differ from")
	t.Setenv("NVCF_CLI_DEFAULT_CONTROL_PLANE_STACK", "oci://registry.example.com/nvcf/stack:1.0.0")
	assert.NotContains(t, run("prod", ""), "NVCF_GATEWAY_NAMES",
		"a CLI with a built-in stack installs from it, not from the working directory")
}

// The stack dependencies the stack leaves out reach the validator as
// NVCF_EXTERNAL_COMPONENTS; an explicit setting wins over them.
func TestClusterValidatorJobEnv_ForwardsExternalComponents(t *testing.T) {
	resetCheckFlags(t)
	t.Setenv("NVCF_EXTERNAL_COMPONENTS", "")
	stack := selfhosted.StackValues{ExternalComponents: []string{"nats", "cassandra"}}
	assert.Equal(t, "nats,cassandra", clusterValidatorJobEnv(stack)["NVCF_EXTERNAL_COMPONENTS"])

	t.Setenv("NVCF_EXTERNAL_COMPONENTS", "openbao")
	assert.Equal(t, "openbao", clusterValidatorJobEnv(stack)["NVCF_EXTERNAL_COMPONENTS"])

	t.Setenv("NVCF_EXTERNAL_COMPONENTS", "")
	assert.NotContains(t, clusterValidatorJobEnv(selfhosted.StackValues{}), "NVCF_EXTERNAL_COMPONENTS")
}
