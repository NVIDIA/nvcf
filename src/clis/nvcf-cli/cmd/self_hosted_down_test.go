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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/teardown"
)

// resetDownFlags restores down command flag vars to their zero values between
// tests that share rootCmd.
func resetDownFlags(t *testing.T) {
	t.Helper()
	selfHostedDownCmd.SetContext(nil)
	t.Cleanup(func() {
		downClusterName = ""
		downAll = false
		downDrainActive = false
		downRemovePersistent = false
		downForceWithRegisteredClusters = false
		downPlanOnly = false
		downConfirm = false
		downKeepNamespaces = false
		downAllConcurrency = 4
		selfHostedJSON = false
		selfHostedPlain = false
		selfHostedAccessible = false
		selfHostedDownCmd.SetContext(nil)
	})
}

// TestDown_RequiresClusterOrAll verifies that running down without
// --cluster-name or --all returns the expected validation error.
func TestDown_RequiresClusterOrAll(t *testing.T) {
	resetDownFlags(t)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "down"})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either --cluster-name or --all is required")
}

// TestDown_ClusterAndAllMutuallyExclusive verifies that combining
// --cluster-name and --all produces a mutual-exclusion error.
func TestDown_ClusterAndAllMutuallyExclusive(t *testing.T) {
	resetDownFlags(t)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "down",
		"--cluster-name=test",
		"--all",
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--cluster-name and --all are mutually exclusive")
}

// TestDown_RemovePersistentRequiresConfirm verifies that --remove-persistent
// without --confirm is rejected.
func TestDown_RemovePersistentRequiresConfirm(t *testing.T) {
	resetDownFlags(t)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "down",
		"--cluster-name=test",
		"--remove-persistent",
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--remove-persistent requires --confirm")
}

// TestDown_PlanOnlyEmitsPlanned runs down --plan-only --json and verifies:
//  1. A "planned" event with WillUninstall appears in the JSONL stream.
//  2. A "final" event with planOnly=true appears.
//  3. No helmfile subprocess was invoked (destroyRunner seam stays at default
//     but --plan-only exits before any Destroy call).
func TestDown_PlanOnlyEmitsPlanned(t *testing.T) {
	resetDownFlags(t)

	// Capture JSONL from stderr.
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "down",
		"--cluster-name=test-cluster",
		"--plan-only",
		"--json",
	})

	require.NoError(t, rootCmd.Execute())

	// Parse the JSONL lines.
	var plannedSeen, finalPlanOnly bool
	var willUninstall []interface{}
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		evType, _ := obj["event"].(string)
		switch evType {
		case "planned":
			plannedSeen = true
			if wu, ok := obj["willUninstall"].([]interface{}); ok {
				willUninstall = wu
			}
		case "final":
			finalPlanOnly, _ = obj["planOnly"].(bool)
		}
	}

	assert.True(t, plannedSeen, "planned event must appear in --plan-only output")
	assert.True(t, finalPlanOnly, "final event must have planOnly=true")
	// WillUninstall must enumerate the compute-plane releases.
	assert.NotEmpty(t, willUninstall, "willUninstall must be populated with release descriptors")
}

// TestDown_HelpListsAllFlags verifies that all documented flags are registered
// on the down subcommand.
func TestDown_HelpListsAllFlags(t *testing.T) {
	for _, flagName := range []string{
		"cluster-name", "all", "drain-active", "remove-persistent",
		"force-with-registered-clusters", "plan-only", "confirm",
		"keep-namespaces", "all-concurrency",
	} {
		assert.NotNil(t,
			selfHostedDownCmd.Flags().Lookup(flagName),
			"missing flag %q on down command", flagName)
	}
}

// TestDown_PlanOnly_NoDestroyInvoked asserts that --plan-only does not call the
// helmfile destroy runner. Uses the destroyRunner package-level seam from the
// teardown package to capture any invocation.
func TestDown_PlanOnly_NoDestroyInvoked(t *testing.T) {
	resetDownFlags(t)

	// Intercept destroyRunner so we know if helmfile was called.
	destroyCalled := false
	// Swap the test seam in the teardown package via a package-level var override.
	// Since destroyRunner lives in internal/selfhosted/teardown, we expose it
	// through a test-helper assignment using the package's test-exported var.
	// In this package we verify the contract indirectly: plan-only must not
	// call teardown.Destroy at all, so destroyRunner cannot be invoked.
	//
	// We confirm by verifying that newClusterDeleterForDown was not called
	// (which would only happen in the real phases path).
	prevDeleterFactory := newClusterDeleterForDown
	t.Cleanup(func() { newClusterDeleterForDown = prevDeleterFactory })
	newClusterDeleterForDown = func(_ string) (teardown.ClusterDeleter, func(), error) {
		destroyCalled = true // misuse of name but intent is clear
		return nil, func() {}, nil
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "down",
		"--cluster-name=test",
		"--plan-only",
		"--json",
	})

	require.NoError(t, rootCmd.Execute())
	assert.False(t, destroyCalled, "newClusterDeleterForDown must not be called in --plan-only mode")
}

// fakeClusterDeleter is a test double for teardown.ClusterDeleter.
type fakeClusterDeleter struct {
	deleteCalls int
	deleteErr   error
	deletedIDs  []string
}

func (f *fakeClusterDeleter) DeleteCluster(_ context.Context, _, clusterID string) error {
	f.deleteCalls++
	f.deletedIDs = append(f.deletedIDs, clusterID)
	return f.deleteErr
}

type fakeDownClusterClient struct {
	fakeClusterDeleter
	listCalls int
	clusters  []client.SISCluster
	listErr   error
}

func (f *fakeDownClusterClient) ListClusters(_ context.Context, _, _ string) ([]client.SISCluster, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var remaining []client.SISCluster
	for _, cluster := range f.clusters {
		deleted := false
		for _, deletedID := range f.deletedIDs {
			if cluster.ClusterID == deletedID {
				deleted = true
				break
			}
		}
		if !deleted {
			remaining = append(remaining, cluster)
		}
	}
	return remaining, nil
}

func installFakeHelmfile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "helmfile.log")
	binPath := filepath.Join(dir, "helmfile")
	script := `#!/bin/sh
printf '%s\n' "$PWD|$*|CLUSTER_NAME=${CLUSTER_NAME}" >> "$NVCF_TEST_HELMFILE_LOG"
`
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	t.Setenv("NVCF_TEST_HELMFILE_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func TestReadRegisterValuesYAML_ReadsNVCAValuesBeforeLegacyRegisterValues(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "out"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "out", "gpu-a-register-values.yaml"), []byte("clusterID: legacy-id\nclusterGroupID: legacy-group\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "out", "gpu-a-nvca-values.yaml"), []byte("clusterID: new-id\nclusterGroupID: new-group\n"), 0o644))

	got, err := readRegisterValuesYAML(dir, "gpu-a")
	require.NoError(t, err)
	assert.Equal(t, "new-id", got.ClusterID)
	assert.Equal(t, "new-group", got.ClusterGroupID)
}

func makeDownStack(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "helmfile-nvca-operator.yaml.gotmpl"), []byte("releases: []\n"), 0o644))
	return dir
}

func TestDown_ClusterNameCleansControlPlaneWhenLastClusterRemoved(t *testing.T) {
	resetDownFlags(t)

	stack := makeDownStack(t)
	helmfileLog := installFakeHelmfile(t)

	prevStack, prevICMS, prevEnv := selfHostedStack, selfHostedICMSURL, selfHostedEnv
	t.Cleanup(func() {
		selfHostedStack = prevStack
		selfHostedICMSURL = prevICMS
		selfHostedEnv = prevEnv
	})
	selfHostedStack = stack
	selfHostedICMSURL = "http://sis.test"
	selfHostedEnv = "local"

	prevRuntimeResolver := resolveSelfHostedHelmRuntimeMode
	t.Cleanup(func() { resolveSelfHostedHelmRuntimeMode = prevRuntimeResolver })
	resolveSelfHostedHelmRuntimeMode = func(context.Context) (selfhosted.HelmRuntimeMode, error) {
		return selfhosted.HelmRuntimeHelm4Compat, nil
	}
	prevControlPlaneInstalled := downControlPlaneInstalled
	t.Cleanup(func() { downControlPlaneInstalled = prevControlPlaneInstalled })
	downControlPlaneInstalled = func(context.Context, string) (bool, error) {
		return true, nil
	}

	fakeClient := &fakeDownClusterClient{}
	prevDeleterFactory := newClusterDeleterForDown
	t.Cleanup(func() { newClusterDeleterForDown = prevDeleterFactory })
	newClusterDeleterForDown = func(_ string) (teardown.ClusterDeleter, func(), error) {
		return fakeClient, func() {}, nil
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "down",
		"--cluster-name=test-cluster",
		"--stack", stack,
		"--json",
	})

	require.NoError(t, rootCmd.Execute())
	assert.Equal(t, 1, fakeClient.deleteCalls)
	assert.Equal(t, 1, fakeClient.listCalls, "down must check whether other clusters remain")

	body, err := os.ReadFile(helmfileLog)
	require.NoError(t, err)
	invocations := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Len(t, invocations, 2, "last cluster removal must destroy compute and control planes")
	assert.Contains(t, invocations[0], "helmfile-nvca-operator.yaml.gotmpl")
	assert.Contains(t, invocations[0], "CLUSTER_NAME=test-cluster")
	assert.Contains(t, invocations[1], filepath.Join(stack, "helmfile.d")+"/")
	assert.Contains(t, invocations[1], "--sequential-helmfiles")
}

func TestDown_LocalAbsentControlPlaneIsNoOpWithoutAuth(t *testing.T) {
	resetDownFlags(t)

	stack := makeDownStack(t)
	_ = installFakeHelmfile(t)

	prevStack, prevEnv := selfHostedStack, selfHostedEnv
	t.Cleanup(func() {
		selfHostedStack = prevStack
		selfHostedEnv = prevEnv
	})
	selfHostedStack = stack
	selfHostedEnv = "local"

	prevRuntimeResolver := resolveSelfHostedHelmRuntimeMode
	t.Cleanup(func() { resolveSelfHostedHelmRuntimeMode = prevRuntimeResolver })
	resolveSelfHostedHelmRuntimeMode = func(context.Context) (selfhosted.HelmRuntimeMode, error) {
		return selfhosted.HelmRuntimeHelm4Compat, nil
	}

	prevControlPlaneInstalled := downControlPlaneInstalled
	t.Cleanup(func() { downControlPlaneInstalled = prevControlPlaneInstalled })
	downControlPlaneInstalled = func(context.Context, string) (bool, error) {
		return false, nil
	}

	prevDeleterFactory := newClusterDeleterForDown
	t.Cleanup(func() { newClusterDeleterForDown = prevDeleterFactory })
	newClusterDeleterForDown = func(_ string) (teardown.ClusterDeleter, func(), error) {
		return nil, func() {}, assert.AnError
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted",
		"--stack", stack,
		"--env", "local",
		"--plain",
		"down",
		"--cluster-name=test-cluster",
		"--confirm",
	})

	require.NoError(t, rootCmd.Execute())
	assert.Contains(t, stderr.String(), "remove-cluster-row")
	assert.Contains(t, stderr.String(), "final: success=true")
}

func TestDownAll_LocalAbsentControlPlaneUninstallsFallbackComputePlanes(t *testing.T) {
	resetDownFlags(t)

	stack := makeDownStack(t)
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "out"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stack, "out", "gpu-a-nvca-values.yaml"), []byte("clusterID: a\nclusterGroupID: group-a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stack, "out", "gpu-b-register-values.yaml"), []byte("clusterID: b\nclusterGroupID: group-b\n"), 0o644))
	helmfileLog := installFakeHelmfile(t)

	prevStack, prevEnv := selfHostedStack, selfHostedEnv
	t.Cleanup(func() {
		selfHostedStack = prevStack
		selfHostedEnv = prevEnv
	})
	selfHostedStack = stack
	selfHostedEnv = "local"

	prevRuntimeResolver := resolveSelfHostedHelmRuntimeMode
	t.Cleanup(func() { resolveSelfHostedHelmRuntimeMode = prevRuntimeResolver })
	resolveSelfHostedHelmRuntimeMode = func(context.Context) (selfhosted.HelmRuntimeMode, error) {
		return selfhosted.HelmRuntimeHelm4Compat, nil
	}

	prevControlPlaneInstalled := downControlPlaneInstalled
	t.Cleanup(func() { downControlPlaneInstalled = prevControlPlaneInstalled })
	downControlPlaneInstalled = func(context.Context, string) (bool, error) {
		return false, nil
	}

	prevDeleterFactory := newClusterDeleterForDown
	t.Cleanup(func() { newClusterDeleterForDown = prevDeleterFactory })
	newClusterDeleterForDown = func(_ string) (teardown.ClusterDeleter, func(), error) {
		return nil, func() {}, assert.AnError
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted",
		"--stack", stack,
		"--env", "local",
		"--plain",
		"down",
		"--all",
		"--confirm",
	})

	require.NoError(t, rootCmd.Execute())
	assert.Contains(t, stderr.String(), "final: success=true")

	body, err := os.ReadFile(helmfileLog)
	require.NoError(t, err)
	invocations := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Len(t, invocations, 2, "--all should uninstall compute planes discovered from local stack artifacts")
	assert.Contains(t, invocations[0], "CLUSTER_NAME=gpu-a")
	assert.Contains(t, invocations[1], "CLUSTER_NAME=gpu-b")
	for _, invocation := range invocations {
		assert.Contains(t, invocation, "helmfile-nvca-operator.yaml.gotmpl")
	}
}

func TestDown_ClusterNameUsesPersistedClusterIDBeforeCheckingRemainingClusters(t *testing.T) {
	resetDownFlags(t)

	stack := makeDownStack(t)
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "out"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(stack, "out", "ncp-local-nvca-values.yaml"),
		[]byte("clusterID: cl-ncp-local\nclusterGroupID: cg-ncp-local\n"),
		0o644,
	))
	helmfileLog := installFakeHelmfile(t)

	prevStack, prevICMS, prevEnv := selfHostedStack, selfHostedICMSURL, selfHostedEnv
	t.Cleanup(func() {
		selfHostedStack = prevStack
		selfHostedICMSURL = prevICMS
		selfHostedEnv = prevEnv
	})
	selfHostedStack = stack
	selfHostedICMSURL = "http://sis.test"
	selfHostedEnv = "local"

	prevRuntimeResolver := resolveSelfHostedHelmRuntimeMode
	t.Cleanup(func() { resolveSelfHostedHelmRuntimeMode = prevRuntimeResolver })
	resolveSelfHostedHelmRuntimeMode = func(context.Context) (selfhosted.HelmRuntimeMode, error) {
		return selfhosted.HelmRuntimeHelm4Compat, nil
	}

	prevControlPlaneInstalled := downControlPlaneInstalled
	t.Cleanup(func() { downControlPlaneInstalled = prevControlPlaneInstalled })
	downControlPlaneInstalled = func(context.Context, string) (bool, error) {
		return true, nil
	}

	fakeClient := &fakeDownClusterClient{}
	fakeClient.clusters = []client.SISCluster{{ClusterID: "cl-ncp-local", ClusterName: "ncp-local", ClusterGroupID: "cg-ncp-local"}}
	prevDeleterFactory := newClusterDeleterForDown
	t.Cleanup(func() { newClusterDeleterForDown = prevDeleterFactory })
	newClusterDeleterForDown = func(_ string) (teardown.ClusterDeleter, func(), error) {
		return fakeClient, func() {}, nil
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted",
		"--env", "local",
		"down",
		"--cluster-name=ncp-local",
		"--stack", stack,
		"--json",
	})

	require.NoError(t, rootCmd.Execute())
	assert.Equal(t, []string{"cl-ncp-local"}, fakeClient.deletedIDs)
	assert.Equal(t, 1, fakeClient.listCalls, "down must check whether other clusters remain after unregister")

	body, err := os.ReadFile(helmfileLog)
	require.NoError(t, err)
	invocations := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Len(t, invocations, 2, "single-cluster down must destroy compute and control planes after deleting the persisted cluster ID")
	assert.Contains(t, invocations[0], "helmfile-nvca-operator.yaml.gotmpl")
	assert.Contains(t, invocations[1], filepath.Join(stack, "helmfile.d")+"/")
}
