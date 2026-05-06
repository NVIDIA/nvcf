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

package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusLevelEnum(t *testing.T) {
	// Test that the enum values are correctly defined and ordered
	assert.Equal(t, StatusLevelError, StatusLevel(0))
	assert.Equal(t, StatusLevelWarn, StatusLevel(1))

	// Test that the values are properly ordered with expected severity
	assert.True(t, StatusLevelWarn > StatusLevelError, "Warn should have higher value than Error")
}

func TestComponentHealth_IsHealthy(t *testing.T) {
	tests := []struct {
		name            string
		componentHealth ComponentHealth
		expected        bool
	}{
		{
			name: "healthy component without errors",
			componentHealth: ComponentHealth{
				Status: HealthStatusHealthy,
				Errors: nil,
			},
			expected: true,
		},
		{
			name: "healthy component with empty errors",
			componentHealth: ComponentHealth{
				Status: HealthStatusHealthy,
				Errors: []string{},
			},
			expected: true,
		},
		{
			name: "healthy status with errors",
			componentHealth: ComponentHealth{
				Status: HealthStatusHealthy,
				Errors: []string{"some error"},
			},
			expected: false,
		},
		{
			name: "unhealthy component without errors",
			componentHealth: ComponentHealth{
				Status: HealthStatusUnhealthy,
				Errors: nil,
			},
			expected: false,
		},
		{
			name: "unhealthy component with errors",
			componentHealth: ComponentHealth{
				Status: HealthStatusUnhealthy,
				Errors: []string{"some error"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.componentHealth.IsHealthy()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestComponentHealth_StatusLevel(t *testing.T) {
	tests := []struct {
		name            string
		componentHealth ComponentHealth
		expectedLevel   StatusLevel
	}{
		{
			name: "component with error level",
			componentHealth: ComponentHealth{
				Status:      HealthStatusUnhealthy,
				Errors:      []string{"critical error"},
				StatusLevel: StatusLevelError,
			},
			expectedLevel: StatusLevelError,
		},
		{
			name: "component with warning level",
			componentHealth: ComponentHealth{
				Status:      HealthStatusUnhealthy,
				Errors:      []string{"non-critical warning"},
				StatusLevel: StatusLevelWarn,
			},
			expectedLevel: StatusLevelWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedLevel, tt.componentHealth.StatusLevel)
		})
	}
}

func TestStatusLevel_JSON(t *testing.T) {
	// StatusLevel isn't serialized, so we don't need to test JSON serialization/deserialization
	// This is verified by the ComponentHealth having the StatusLevel field marked with json:"-"
	ch := ComponentHealth{
		Status:      HealthStatusHealthy,
		Errors:      []string{},
		StatusLevel: StatusLevelWarn,
	}

	data, err := json.Marshal(ch)
	assert.NoError(t, err)

	// Unmarshal back to a map to verify the StatusLevel field is not included
	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	assert.NoError(t, err)

	// Check that StatusLevel is not in the JSON
	_, hasStatusLevel := result["StatusLevel"]
	assert.False(t, hasStatusLevel, "StatusLevel should not be included in JSON")
}

func TestMaintenanceMode_String(t *testing.T) {
	tests := []struct {
		name     string
		mode     MaintenanceMode
		expected string
	}{
		{
			name:     "normal mode",
			mode:     MaintenanceModeNone,
			expected: "None",
		},
		{
			name:     "cordon mode",
			mode:     MaintenanceModeCordon,
			expected: "CordonOnly",
		},
		{
			name:     "cordon and drain mode",
			mode:     MaintenanceModeCordonAndDrain,
			expected: "CordonAndDrain",
		},
		{
			name:     "unknown mode",
			mode:     MaintenanceMode("unknown"),
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.mode.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMaintenanceMode_Constants(t *testing.T) {
	// Test that the maintenance mode constants are correctly defined
	assert.Equal(t, MaintenanceMode("None"), MaintenanceModeNone)
	assert.Equal(t, MaintenanceMode("CordonOnly"), MaintenanceModeCordon)
	assert.Equal(t, MaintenanceMode("CordonAndDrain"), MaintenanceModeCordonAndDrain)
}
