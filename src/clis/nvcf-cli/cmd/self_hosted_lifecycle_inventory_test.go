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
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type selfHostedLifecycleCommandInventoryEntry struct {
	ownershipGate string
}

var selfHostedLifecycleCommandInventory = map[string]selfHostedLifecycleCommandInventoryEntry{
	"nvcf-cli self-hosted check": {
		ownershipGate: "must read health from one control plane only",
	},
	"nvcf-cli self-hosted compute-plane install": {
		ownershipGate: "must install compute-plane artifacts for one control plane only",
	},
	"nvcf-cli self-hosted compute-plane register": {
		ownershipGate: "must bind the compute-plane registration to one control plane only",
	},
	"nvcf-cli self-hosted control-plane profile export": {
		ownershipGate: "must write the profile for one control plane only",
	},
	"nvcf-cli self-hosted control-plane profile validate": {
		ownershipGate: "must validate the profile for one control plane only",
	},
	"nvcf-cli self-hosted down": {
		ownershipGate: "must tear down objects owned by one control plane only",
	},
	"nvcf-cli self-hosted install": {
		ownershipGate: "must install release sets for one control plane only",
	},
	"nvcf-cli self-hosted status": {
		ownershipGate: "must report status for one control plane only",
	},
	"nvcf-cli self-hosted uninstall": {
		ownershipGate: "must uninstall release sets owned by one control plane only",
	},
	"nvcf-cli self-hosted up": {
		ownershipGate: "must run the one-shot workflow for one control plane only",
	},
}

func TestSelfHostedLifecycleCommandInventory(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"self-hosted"})
	require.NoError(t, err)
	require.NotNil(t, cmd)

	got := runnableCommandPaths(cmd)
	want := sortedSelfHostedLifecycleInventoryPaths()

	assert.Equal(t, want, got)
	for path, entry := range selfHostedLifecycleCommandInventory {
		assert.NotEmpty(t, strings.TrimSpace(entry.ownershipGate), "missing ownership gate for %s", path)
	}
}

func runnableCommandPaths(root *cobra.Command) []string {
	var paths []string
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Runnable() {
			paths = append(paths, cmd.CommandPath())
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	sort.Strings(paths)
	return paths
}

func sortedSelfHostedLifecycleInventoryPaths() []string {
	paths := make([]string, 0, len(selfHostedLifecycleCommandInventory))
	for path := range selfHostedLifecycleCommandInventory {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
