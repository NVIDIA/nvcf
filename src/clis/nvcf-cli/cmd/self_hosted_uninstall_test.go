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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetUninstallFlags restores uninstall flag vars to their zero values between
// tests that share rootCmd.
func resetUninstallFlags(t *testing.T) {
	t.Helper()
	selfHostedUninstallCmd.SetContext(nil)
	t.Cleanup(func() {
		uninstallControlPlane = false
		uninstallComputePlane = false
		uninstallClusterName = ""
		uninstallNoApply = false
		uninstallRemovePersistent = false
		uninstallForceWithRegisteredClusters = false
		uninstallKeepNamespaces = false
		uninstallConfirm = false
		selfHostedUninstallCmd.SetContext(nil)
	})
}

// TestUninstall_RequiresPlaneFlag verifies that omitting both --control-plane
// and --compute-plane produces the expected validation error.
func TestUninstall_RequiresPlaneFlag(t *testing.T) {
	resetUninstallFlags(t)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "uninstall"})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one of --control-plane or --compute-plane is required")
}

// TestUninstall_MutuallyExclusive verifies that combining --control-plane and
// --compute-plane produces a mutual-exclusion error.
func TestUninstall_MutuallyExclusive(t *testing.T) {
	resetUninstallFlags(t)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "uninstall",
		"--control-plane",
		"--compute-plane",
		"--cluster-name=test",
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--control-plane and --compute-plane are mutually exclusive")
}

// TestUninstall_ComputePlaneRequiresClusterName verifies that --compute-plane
// without --cluster-name returns the expected error.
func TestUninstall_ComputePlaneRequiresClusterName(t *testing.T) {
	resetUninstallFlags(t)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "uninstall", "--compute-plane"})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--compute-plane requires --cluster-name")
}

// TestUninstall_RemovePersistentRequiresConfirm verifies that --remove-persistent
// without --confirm is rejected.
func TestUninstall_RemovePersistentRequiresConfirm(t *testing.T) {
	resetUninstallFlags(t)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "uninstall",
		"--control-plane",
		"--remove-persistent",
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--remove-persistent requires --confirm")
}

// TestUninstall_HelpListsAllFlags verifies that all documented flags are
// registered on the uninstall subcommand.
func TestUninstall_HelpListsAllFlags(t *testing.T) {
	for _, flagName := range []string{
		"control-plane", "compute-plane", "cluster-name",
		"no-apply", "remove-persistent", "force-with-registered-clusters",
		"keep-namespaces", "confirm",
	} {
		assert.NotNil(t,
			selfHostedUninstallCmd.Flags().Lookup(flagName),
			"missing flag %q on uninstall command", flagName)
	}
}
