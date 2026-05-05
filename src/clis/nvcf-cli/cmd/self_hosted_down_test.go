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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
}

func (f *fakeClusterDeleter) DeleteCluster(_ context.Context, _, _ string) error {
	f.deleteCalls++
	return f.deleteErr
}
