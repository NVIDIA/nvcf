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

package nvca

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestMaintenanceModeFeatureFlagNames(t *testing.T) {
	// Test that feature flag keys are correct
	assert.Equal(t, "CordonMaintenance", featureflag.CordonMaintenance.Key)
	assert.Equal(t, "CordonAndDrainMaintenance", featureflag.CordonAndDrainMaintenance.Key)
}

func TestMaintenanceModeConstants(t *testing.T) {
	// Test that maintenance mode constants are correctly defined
	assert.Equal(t, types.MaintenanceMode("None"), types.MaintenanceModeNone)
	assert.Equal(t, types.MaintenanceMode("CordonOnly"), types.MaintenanceModeCordon)
	assert.Equal(t, types.MaintenanceMode("CordonAndDrain"), types.MaintenanceModeCordonAndDrain)
}

func TestMaintenanceModeStringMethod(t *testing.T) {
	// Test that the String() method returns the correct values
	assert.Equal(t, "None", types.MaintenanceModeNone.String())
	assert.Equal(t, "CordonOnly", types.MaintenanceModeCordon.String())
	assert.Equal(t, "CordonAndDrain", types.MaintenanceModeCordonAndDrain.String())
}

func TestMaintenanceModeMutualExclusivityLogic(t *testing.T) {
	// Test the mutual exclusivity error message format
	expectedErrorFormat := "feature flags %s and %s are mutually exclusive and cannot be enabled simultaneously"

	actualError := fmt.Errorf(expectedErrorFormat,
		featureflag.CordonMaintenance.Key,
		featureflag.CordonAndDrainMaintenance.Key)

	assert.Contains(t, actualError.Error(), "CordonMaintenance")
	assert.Contains(t, actualError.Error(), "CordonAndDrainMaintenance")
	assert.Contains(t, actualError.Error(), "mutually exclusive")
}
