/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package featureflag

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_newBool(t *testing.T) {
	require.True(t, *newBool(true))
	require.False(t, *newBool(false))
}

func TestFeatureFlags(t *testing.T) {
	_ = parseFlags("")
	assert.True(t, HelmRBACEnforcement.Enabled())
	assert.True(t, DynamicGPUDiscovery.Enabled())
	assert.True(t, MultipleGPUTypesAllowed.Enabled())
	assert.True(t, UniformInstanceLabels.Enabled())
	assert.True(t, AutoPurgeDegradedWorkers.Enabled())
	assert.True(t, ClusterTargeting.Enabled())
	assert.True(t, HelmSharedStorage.Enabled())

	// Disable/Enable cluster targeting
	_ = parseFlags(fmt.Sprintf("-%s", ClusterTargeting.Key))
	assert.False(t, ClusterTargeting.Enabled())
	_ = parseFlags(fmt.Sprintf("+%s", ClusterTargeting.Key))
	assert.True(t, ClusterTargeting.Enabled())
	_ = parseFlags(fmt.Sprintf("-%s", ClusterTargeting.Key))
	assert.False(t, ClusterTargeting.Enabled())
	_ = parseFlags(ClusterTargeting.Key)
	assert.True(t, ClusterTargeting.Enabled())

	// Test deprecation
	_ = parseFlags("+NVCA2.0")
	assert.False(t, NVMeshEncryption.Enabled())

	// Test deprecation
	_ = parseFlags("-NVCA2.0")
	assert.False(t, NVMeshEncryption.Enabled())

	// Test unknown flag path no panic
	_ = parseFlags("ThisFlagDoesNotExist")

	// check if flag is found
	flag, err := Get("ThisFlagDoesNotExist")
	assert.Error(t, err)
	assert.Nil(t, flag)

	// Retrieve existing flag and check value
	flag, err = Get(LogPosting.Key)
	require.NoError(t, err)
	assert.Equal(t, LogPosting, flag)
}

func TestMaintenanceModeFeatureFlags(t *testing.T) {
	// Test default values - reset all flags first
	_ = parseFlags("-CordonMaintenance,-CordonAndDrainMaintenance")
	assert.False(t, CordonMaintenance.Enabled())
	assert.False(t, CordonAndDrainMaintenance.Enabled())

	// Test enabling CordonMaintenance only
	_ = parseFlags("CordonMaintenance,-CordonAndDrainMaintenance")
	assert.True(t, CordonMaintenance.Enabled())
	assert.False(t, CordonAndDrainMaintenance.Enabled())

	// Test enabling CordonAndDrainMaintenance only
	_ = parseFlags("-CordonMaintenance,CordonAndDrainMaintenance")
	assert.False(t, CordonMaintenance.Enabled())
	assert.True(t, CordonAndDrainMaintenance.Enabled())

	// Test disabling both
	_ = parseFlags("-CordonMaintenance,-CordonAndDrainMaintenance")
	assert.False(t, CordonMaintenance.Enabled())
	assert.False(t, CordonAndDrainMaintenance.Enabled())

	// Test retrieving flags
	flag, err := Get(CordonMaintenance.Key)
	require.NoError(t, err)
	assert.Equal(t, CordonMaintenance, flag)

	flag, err = Get(CordonAndDrainMaintenance.Key)
	require.NoError(t, err)
	assert.Equal(t, CordonAndDrainMaintenance, flag)
}

func TestHelmAllowCPUNodesFeatureFlag(t *testing.T) {
	// Reset flags to defaults
	_ = parseFlags("-HelmAllowCPUNodes,-HelmResourceConstraints")

	// Test default value is false
	assert.False(t, HelmAllowCPUNodes.Enabled(), "HelmAllowCPUNodes should be disabled by default")

	// Test enabling HelmAllowCPUNodes when HelmResourceConstraints is disabled
	err := parseFlags("+HelmAllowCPUNodes,-HelmResourceConstraints")
	require.NoError(t, err)
	assert.True(t, HelmAllowCPUNodes.Enabled(), "HelmAllowCPUNodes should be enabled")
	assert.False(t, HelmResourceConstraints.Enabled(), "HelmResourceConstraints should be disabled")

	// Test retrieving flag
	flag, err := Get(HelmAllowCPUNodes.Key)
	require.NoError(t, err)
	assert.Equal(t, HelmAllowCPUNodes, flag)

	// Reset for other tests
	_ = parseFlags("-HelmAllowCPUNodes,+HelmResourceConstraints")
}

func TestHelmAllowCPUNodes_MutualExclusion(t *testing.T) {
	tests := []struct {
		name        string
		flagsInput  string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "both disabled - no error",
			flagsInput:  "-HelmAllowCPUNodes,-HelmResourceConstraints",
			expectError: false,
		},
		{
			name:        "HelmAllowCPUNodes enabled, HelmResourceConstraints disabled - no error",
			flagsInput:  "+HelmAllowCPUNodes,-HelmResourceConstraints",
			expectError: false,
		},
		{
			name:        "HelmAllowCPUNodes disabled, HelmResourceConstraints enabled - no error",
			flagsInput:  "-HelmAllowCPUNodes,+HelmResourceConstraints",
			expectError: false,
		},
		{
			name:        "both enabled - error",
			flagsInput:  "+HelmAllowCPUNodes,+HelmResourceConstraints",
			expectError: true,
			errorMsg:    "HelmAllowCPUNodes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseFlags(tt.flagsInput)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Contains(t, err.Error(), "cannot be enabled")
			} else {
				require.NoError(t, err)
			}
		})
	}

	// Reset for other tests
	_ = parseFlags("-HelmAllowCPUNodes,+HelmResourceConstraints")
}
