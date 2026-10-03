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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted"
)

// stackWithEnvs creates a stack directory holding base.yaml and one file per
// named environment.
func stackWithEnvs(t *testing.T, envs ...string) string {
	t.Helper()
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	for _, name := range append([]string{"base"}, envs...) {
		require.NoError(t, os.WriteFile(filepath.Join(envDir, name+".yaml"), []byte("global: {}\n"), 0o644))
	}
	return dir
}

// withEnvFlag sets --env as if the operator passed it, and restores it.
func withEnvFlag(t *testing.T, value string, changed bool) {
	t.Helper()
	f := selfHostedCmd.PersistentFlags().Lookup("env")
	require.NotNil(t, f)
	prevValue, prevChanged := selfHostedEnv, f.Changed
	selfHostedEnv, f.Changed = value, changed
	t.Cleanup(func() { selfHostedEnv, f.Changed = prevValue, prevChanged })
}

// withStackFlag sets a stack flag's value as if the operator passed it.
func withStackFlag(t *testing.T, flag *string, src string) {
	t.Helper()
	prev := *flag
	*flag = src
	t.Cleanup(func() { *flag = prev })
}

// helmfile layers the environment file over base.yaml, so both are returned,
// base first. Returning only the first file that exists dropped whichever
// half the other one set.
func TestStackValuesForRun_LayersEnvOverBase(t *testing.T) {
	stack := stackWithEnvs(t, "prod")
	withStackFlag(t, &selfHostedControlPlaneStack, stack)
	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "prod", true)

	got, ok := stackValuesForRun(controlPlaneStackTarget())
	require.True(t, ok)
	require.Len(t, got, 2)
	assert.Equal(t, "base.yaml", filepath.Base(got[0]))
	assert.Equal(t, "prod.yaml", filepath.Base(got[1]))
}

// --env's default is not evidence of the environment the install used, so
// nothing is read until the operator names one, with --env or HELMFILE_ENV.
// base.yaml alone does not describe an install either.
func TestStackValuesForRun_NeedsTheNamedEnvironmentFile(t *testing.T) {
	stack := stackWithEnvs(t, "local")
	withStackFlag(t, &selfHostedControlPlaneStack, stack)
	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "local", false)
	_, ok := stackValuesForRun(controlPlaneStackTarget())
	assert.False(t, ok, "the defaulted --env is not the install's environment")

	t.Setenv("HELMFILE_ENV", "local")
	_, ok = stackValuesForRun(controlPlaneStackTarget())
	assert.True(t, ok, "HELMFILE_ENV names the environment")

	withEnvFlag(t, "prod", true)
	_, ok = stackValuesForRun(controlPlaneStackTarget())
	assert.False(t, ok, "base.yaml without prod.yaml does not describe the install")
}

// An operator who exports HELMFILE_ENV and runs helmfile directly gets that
// environment; an explicit --env still wins over it.
func TestResolveStackEnv_Precedence(t *testing.T) {
	t.Setenv("HELMFILE_ENV", "airgap")
	withEnvFlag(t, "local", false)
	assert.Equal(t, "airgap", resolveStackEnv(), "HELMFILE_ENV beats the --env default")

	withEnvFlag(t, "staging", true)
	assert.Equal(t, "staging", resolveStackEnv(), "an explicit --env beats HELMFILE_ENV")

	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "local", false)
	assert.Equal(t, "local", resolveStackEnv())
}

// A stack the install fetched is not read from anywhere else: file:// and
// git stacks are cloned from what is committed, and an oci:// stack nobody
// extracted here has nothing to read. A checkout above the working directory
// does not stand in for any of them.
func TestStackValuesForRun_RemoteStacksAreNotGuessed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stack := stackWithEnvs(t, "prod")
	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "prod", true)
	t.Chdir(stack)
	for _, src := range []string{
		"file://" + stack, "file://" + stack + "@main", "git@example.com:org/stack.git",
		"https://example.com/org/stack.git", "oci://registry.example.com/nvcf/stack:1.0.0",
	} {
		withStackFlag(t, &selfHostedControlPlaneStack, src)
		_, ok := stackValuesForRun(controlPlaneStackTarget())
		assert.False(t, ok, src)
	}
}

// An oci:// stack an earlier command extracted is read from that extraction,
// which is what the install read: the built-in stack when no flag is set.
func TestStackValuesForRun_ReadsTheExtractedOCIStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	const ref = "oci://registry.example.com/nvcf/stack:1.0.0@sha256:0123456789abcdef"
	extracted := filepath.Join(cache, "nvcf-cli", "stacks", "oci-0123456789ab")
	require.NoError(t, os.MkdirAll(filepath.Join(extracted, "environments"), 0o755))
	for _, name := range []string{"base", "prod"} {
		require.NoError(t, os.WriteFile(filepath.Join(extracted, "environments", name+".yaml"), []byte("{}\n"), 0o644))
	}
	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "prod", true)

	withStackFlag(t, &selfHostedControlPlaneStack, ref)
	_, ok := stackValuesForRun(controlPlaneStackTarget())
	assert.False(t, ok, "a partial extraction is not read")

	require.NoError(t, os.WriteFile(filepath.Join(extracted, ".extraction-complete"), nil, 0o644))
	files, ok := stackValuesForRun(controlPlaneStackTarget())
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(files[1], extracted), files[1])

	withStackFlag(t, &selfHostedControlPlaneStack, "")
	t.Setenv("NVCF_CLI_DEFAULT_CONTROL_PLANE_STACK", ref)
	files, ok = stackValuesForRun(controlPlaneStackTarget())
	require.True(t, ok, "with no flag the built-in stack is the install's")
	assert.True(t, strings.HasPrefix(files[1], extracted), files[1])
}

// With neither a stack flag nor a built-in stack, the stack checkout above
// the working directory is read, each plane from its own stack. A CLI with a
// built-in stack installs from that, so the checkout is then not read.
func TestStackValuesForRun_CheckoutAboveTheWorkingDirectory(t *testing.T) {
	t.Setenv("NVCF_CLI_DEFAULT_CONTROL_PLANE_STACK", "")
	t.Setenv("NVCF_CLI_DEFAULT_COMPUTE_PLANE_STACK", "")
	repo := t.TempDir()
	envDir := func(stack string) string {
		dir := filepath.Join(repo, "deploy", "stacks", stack, "environments")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for _, name := range []string{"base", "prod"} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte("{}\n"), 0o644))
		}
		return dir
	}
	cpDir, gpuDir := envDir("self-managed"), envDir("nvcf-compute-plane")
	sub := filepath.Join(repo, "src", "x")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	t.Chdir(sub)
	withStackFlag(t, &selfHostedControlPlaneStack, "")
	withStackFlag(t, &selfHostedComputePlaneStack, "")
	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "prod", true)

	files, ok := stackValuesForRun(controlPlaneStackTarget())
	require.True(t, ok)
	assert.Equal(t, filepath.Join(cpDir, "prod.yaml"), files[1])
	files, ok = stackValuesForRun(computePlaneStackTarget())
	require.True(t, ok)
	assert.Equal(t, filepath.Join(gpuDir, "prod.yaml"), files[1])

	t.Setenv("NVCF_CLI_DEFAULT_CONTROL_PLANE_STACK", "oci://registry.example.com/nvcf/stack:9")
	_, ok = stackValuesForRun(controlPlaneStackTarget())
	assert.False(t, ok, "a CLI with a built-in stack installs from it, not from the working directory")
}

// registryProbes runs check with args and returns the registries the local
// credential check probed, keyed registry or registry/scope, with whether
// each was critical.
func registryProbes(t *testing.T, args ...string) map[string]bool {
	t.Helper()
	resetCheckFlags(t)
	var mu sync.Mutex
	got := map[string]bool{}
	prev := newRegistryCredentialCheckerForSelfHosted
	newRegistryCredentialCheckerForSelfHosted = func() selfhosted.RegistryCredentialChecker {
		return func(_ context.Context, registry, repo string, critical bool) error {
			mu.Lock()
			defer mu.Unlock()
			got[strings.TrimSuffix(registry+"/"+repo, "/")] = critical
			return nil
		}
	}
	t.Cleanup(func() { newRegistryCredentialCheckerForSelfHosted = prev })
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs(append([]string{"self-hosted", "check", "--json", "--skip-cluster-validation"}, args...))
	_ = rootCmd.Execute()
	return got
}

// mirroredStack writes a stack whose prod environment pulls from a mirror.
func mirroredStack(t *testing.T) string {
	t.Helper()
	stack := t.TempDir()
	envDir := filepath.Join(stack, "environments")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "base.yaml"),
		[]byte("global:\n  image:\n    registry: nvcr.io\n    repository: YOUR_ORG/YOUR_TEAM\n"+
			"certManager:\n  enabled: true\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "prod.yaml"),
		[]byte("global:\n  image:\n    registry: harbor.corp.example\n    repository: nvcf\n"+
			"certManager:\n  acmesolver:\n    image:\n      repository: harbor.corp.example/jetstack/acmesolver\n"), 0o644))
	return stack
}

// Registry enumeration follows the same rule as the Gateways: the stack's
// registries, their criticality and the quay.io gate are used only when the
// named environment's file was read from the stack the command points at.
// Otherwise nvcr.io is a non-critical guess, never a critical row a mirrored
// install fails on.
func TestCheck_RegistriesNeedTheEnvironmentFile(t *testing.T) {
	t.Setenv("HELMFILE_ENV", "")
	t.Setenv("NVCF_CLI_DEFAULT_CONTROL_PLANE_STACK", "")
	stack := mirroredStack(t)

	got := registryProbes(t, "--control-plane", "--env", "prod", "--control-plane-stack", stack)
	assert.Equal(t, map[string]bool{"harbor.corp.example/nvcf": false}, got,
		"the install's mirror is probed, and neither nvcr.io nor quay.io is")

	for name, args := range map[string][]string{
		"no --env":                {"--control-plane", "--control-plane-stack", stack},
		"base.yaml only":          {"--control-plane", "--env", "staging", "--control-plane-stack", stack},
		"stack without env dir":   {"--control-plane", "--env", "prod", "--control-plane-stack", t.TempDir()},
		"oci stack in a checkout": {"--control-plane", "--env", "prod", "--control-plane-stack", "oci://r.example.com/s:1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(stack)
			got := registryProbes(t, args...)
			assert.Contains(t, got, "nvcr.io")
			assert.False(t, got["nvcr.io"], "a guessed nvcr.io is never critical")
			assert.NotContains(t, got, "harbor.corp.example/nvcf")
		})
	}
}

// A compute-plane-only run reads the compute-plane stack, under the same rule.
func TestCheck_ComputeOnlyRunReadsTheComputePlaneStack(t *testing.T) {
	t.Setenv("HELMFILE_ENV", "")
	t.Setenv("NVCF_CLI_DEFAULT_COMPUTE_PLANE_STACK", "")
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	gpuStack := t.TempDir()
	envDir := filepath.Join(gpuStack, "environments")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "prod.yaml"),
		[]byte("global:\n  image:\n    registry: gpu-mirror.example.com\n    repository: site\n"), 0o644))

	got := registryProbes(t, "--compute-plane", "--cluster-name", "gpu", "--env", "prod",
		"--control-plane-stack", mirroredStack(t), "--compute-plane-stack", gpuStack)
	assert.Equal(t, map[string]bool{"gpu-mirror.example.com/site": false}, got)
}

// A malformed --cluster-validator-registries entry fails the command naming
// it, rather than vanishing with no row.
func TestCheck_MalformedValidatorRegistryFailsTheCommand(t *testing.T) {
	resetCheckFlags(t)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--cluster-validator-registries", "harbor.example.com:44x"})
	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"harbor.example.com:44x": expected host[:port]`)

	got := registryProbes(t, "--control-plane", "--cluster-validator-registries", "harbor.example.com:5000/nvcf")
	assert.Contains(t, got, "harbor.example.com:5000/nvcf", "a path is the probe's scope")
}

// The NGC key is checked ahead of the docker login only before a local
// install, where up mints its pull secrets from it; every other scope checks
// an installed stack, where a rejected key is only this machine's.
func TestCheckScope_NGCKeyOrderAndPostInstall(t *testing.T) {
	t.Setenv("HELMFILE_ENV", "")
	for _, tc := range []struct {
		name                       string
		pre, cp, gpu, all, prodEnv bool
		prefer, postInstall        bool
	}{
		{name: "bare --pre", pre: true, prefer: true},
		{name: "--pre --env prod", pre: true, prodEnv: true},
		{name: "--pre --control-plane", pre: true, cp: true, postInstall: true},
		{name: "--control-plane", cp: true, postInstall: true},
		{name: "--compute-plane", gpu: true, postInstall: true},
		{name: "--pre --all", pre: true, all: true, postInstall: true},
	} {
		resetCheckFlags(t)
		checkPre, checkControlPlane, checkComputePlane, checkAll = tc.pre, tc.cp, tc.gpu, tc.all
		withEnvFlag(t, map[bool]string{true: "prod", false: "local"}[tc.prodEnv], tc.prodEnv)
		assert.Equal(t, tc.prefer, preferNGCKey(), tc.name)
		assert.Equal(t, tc.postInstall, checkScopeIsPostInstall(), tc.name)
	}
}
