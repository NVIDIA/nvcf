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

package kata

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// initRuntimeClasses reinitializes the runtime class names based on environment variables
func initRuntimeClasses() {
	if rtcNameOverride := os.Getenv("NVCA_KATA_RUNTIME_CLASS"); rtcNameOverride != "" {
		RuntimeClassNameGPU = rtcNameOverride
	} else if rtcNameOverride := os.Getenv("NVCA_KATA_RUNTIME_CLASS_GPU"); rtcNameOverride != "" {
		RuntimeClassNameGPU = rtcNameOverride
	}
	if rtcNameOverride := os.Getenv("NVCA_KATA_RUNTIME_CLASS_NON_GPU"); rtcNameOverride != "" {
		RuntimeClassNameNonGPU = rtcNameOverride
	}
}

func TestRuntimeClassOverrides(t *testing.T) {
	// Save original values
	originalGPU := RuntimeClassNameGPU
	originalNonGPU := RuntimeClassNameNonGPU

	// Test cases
	tests := []struct {
		name           string
		envVars        map[string]string
		expectedGPU    string
		expectedNonGPU string
	}{
		{
			name:           "default values",
			envVars:        map[string]string{},
			expectedGPU:    "kata-qemu-nvidia-gpu",
			expectedNonGPU: "kata-qemu",
		},
		{
			name: "override both classes",
			envVars: map[string]string{
				"NVCA_KATA_RUNTIME_CLASS":         "custom-gpu",
				"NVCA_KATA_RUNTIME_CLASS_NON_GPU": "custom-non-gpu",
			},
			expectedGPU:    "custom-gpu",
			expectedNonGPU: "custom-non-gpu",
		},
		{
			name: "override only GPU class",
			envVars: map[string]string{
				"NVCA_KATA_RUNTIME_CLASS_GPU": "custom-gpu-only",
			},
			expectedGPU:    "custom-gpu-only",
			expectedNonGPU: "kata-qemu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset environment
			os.Clearenv()
			RuntimeClassNameGPU = originalGPU
			RuntimeClassNameNonGPU = originalNonGPU

			// Set test environment variables
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			// Re-run init
			initRuntimeClasses()

			// Verify results
			assert.Equal(t, tt.expectedGPU, RuntimeClassNameGPU)
			assert.Equal(t, tt.expectedNonGPU, RuntimeClassNameNonGPU)
		})
	}

	// Restore original values
	RuntimeClassNameGPU = originalGPU
	RuntimeClassNameNonGPU = originalNonGPU
}

func TestNewStatusGetter(t *testing.T) {
	tests := []struct {
		name           string
		setupRuntime   bool
		expectedStatus types.HealthStatus
		expectError    bool
	}{
		{
			name:           "runtime class exists",
			setupRuntime:   true,
			expectedStatus: types.HealthStatusHealthy,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake k8s client
			client := fake.NewSimpleClientset()

			ctx := context.Background()

			// Setup runtime class if needed
			if tt.setupRuntime {
				_, err := client.NodeV1().RuntimeClasses().Create(ctx, &nodev1.RuntimeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: RuntimeClassNameGPU,
					},
				}, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			// Create status getter
			statusGetter := NewStatusGetter(client)

			// Get status
			health, err := statusGetter.GetComponentStatus(ctx)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, types.HealthStatusUnhealthy, health.Components[ComponentName].Status)
				assert.Equal(t, types.StatusLevelError, health.Components[ComponentName].StatusLevel)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, types.HealthStatusHealthy, health.Components[ComponentName].Status)
				assert.Equal(t, types.StatusLevelWarn, health.Components[ComponentName].StatusLevel)
			}
		})
	}
}
