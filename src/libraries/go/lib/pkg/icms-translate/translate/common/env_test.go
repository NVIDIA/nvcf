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

package common

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestMakeWorkloadEnvVars(t *testing.T) {
	// Helper to build expected env var with FieldRef
	fieldRefEnv := func(name, fieldPath string) corev1.EnvVar {
		return corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: fieldPath},
			},
		}
	}
	// Helper to build expected env var with static Value
	valueEnv := func(name, value string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, Value: value}
	}

	// Common env vars shared by all workload types
	commonEnvs := []corev1.EnvVar{
		fieldRefEnv(RegionEnv, "metadata.annotations['nvcf.nvidia.io/region']"),
		fieldRefEnv(InstanceTypeEnv, "metadata.annotations['nvcf.nvidia.io/instance-type-name']"),
		fieldRefEnv(InstanceTypeValueEnv, "metadata.annotations['nvcf.nvidia.io/instance-type-value']"),
		fieldRefEnv(BackendEnv, "metadata.annotations['nvcf.nvidia.io/backend']"),
		fieldRefEnv(EnvironmentEnv, "metadata.annotations['nvcf.nvidia.io/environment']"),
	}

	tests := []struct {
		name         string
		workloadType MessageAction
		expectedEnvs []corev1.EnvVar
		expectedNil  bool
	}{
		{
			name:         "FunctionCreationAction",
			workloadType: FunctionCreationAction,
			expectedEnvs: append(commonEnvs,
				fieldRefEnv(NCAIDEnv, "metadata.annotations['nca-id']"),
				fieldRefEnv(FunctionIDEnv, "metadata.labels['function-id']"),
				fieldRefEnv(FunctionVersionIDEnv, "metadata.labels['function-version-id']"),
				fieldRefEnv(FunctionNameEnv, "metadata.annotations['function-name']"),
			),
			expectedNil: false,
		},
		{
			name:         "TaskCreationAction",
			workloadType: TaskCreationAction,
			expectedEnvs: append(commonEnvs,
				fieldRefEnv(NVCTNCAIDEnv, "metadata.annotations['nca-id']"),
				fieldRefEnv(TaskIDEnv, "metadata.labels['task-id']"),
				fieldRefEnv(TaskNameEnv, "metadata.annotations['task-name']"),
				valueEnv(NVCTProgressFilePathEnvKey, NVCTTaskProgressFilePath),
				valueEnv(NVCTResultsDirEnvKey, NVCTTaskResultsDir),
			),
			expectedNil: false,
		},
		{
			name:         "TerminationAction",
			workloadType: TerminationAction,
			expectedNil:  true,
		},
		{
			name:         "Unknown action",
			workloadType: MessageAction("UnknownAction"),
			expectedNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MakeWorkloadEnvVars(tt.workloadType)

			if tt.expectedNil {
				assert.Nil(t, result, "Expected nil result for workloadType %s", tt.workloadType)
				return
			}

			assert.NotNil(t, result, "Expected non-nil result")
			assert.Len(t, result, len(tt.expectedEnvs), "Env var count mismatch")

			// Build a map of actual env vars for order-independent lookup
			actualEnvs := make(map[string]corev1.EnvVar)
			for _, env := range result {
				actualEnvs[env.Name] = env
			}

			// Verify each expected env var exists with correct values (order-independent)
			for _, expected := range tt.expectedEnvs {
				actual, exists := actualEnvs[expected.Name]
				assert.True(t, exists, "Expected env var %q not found", expected.Name)
				if !exists {
					continue
				}

				if expected.ValueFrom != nil && expected.ValueFrom.FieldRef != nil {
					assert.NotNil(t, actual.ValueFrom, "ValueFrom should not be nil for %q", expected.Name)
					if actual.ValueFrom != nil {
						assert.NotNil(t, actual.ValueFrom.FieldRef, "FieldRef should not be nil for %q", expected.Name)
						if actual.ValueFrom.FieldRef != nil {
							assert.Equal(t, expected.ValueFrom.FieldRef.FieldPath, actual.ValueFrom.FieldRef.FieldPath,
								"FieldPath mismatch for %q", expected.Name)
						}
					}
				} else if expected.Value != "" {
					assert.Equal(t, expected.Value, actual.Value, "Value mismatch for %q", expected.Name)
				}
			}
		})
	}
}

// TestEnvConstants verifies that the environment variable constants are defined correctly
func TestEnvConstants(t *testing.T) {
	// Test some key constants to ensure they're defined correctly
	assert.Equal(t, "INSTANCE_TYPE", InstanceTypeEnvVarLegacy)
	assert.Equal(t, "INSTANCE_TYPE_NAME", InstanceTypeNameEnvVar)
	assert.Equal(t, "INSTANCE_TYPE_VALUE", InstanceTypeValueEnvVar)
	assert.Equal(t, "ICMS_ENVIRONMENT", ICMSEnvironmentEnv)
	assert.Equal(t, "GPU_NAME", GPUNameEnv)
	assert.Equal(t, "ATTACHED_GPU_COUNT", GPUCountEnv)

	// NVCF prefixed environment variables
	assert.Equal(t, "NVCF_NCA_ID", NCAIDEnv)
	assert.Equal(t, "NVCF_FUNCTION_ID", FunctionIDEnv)
	assert.Equal(t, "NVCF_FUNCTION_VERSION_ID", FunctionVersionIDEnv)
	assert.Equal(t, "NVCF_FUNCTION_NAME", FunctionNameEnv)
	assert.Equal(t, "NVCF_ASSET_DIR", AssetDirEnv)
	assert.Equal(t, "NVCF_LARGE_OUTPUT_DIR", LargeOutputDirEnv)
	assert.Equal(t, "NVCF_BACKEND", BackendEnv)
	assert.Equal(t, "NVCF_INSTANCETYPE", InstanceTypeEnv)
	assert.Equal(t, "NVCF_INSTANCETYPE_VALUE", InstanceTypeValueEnv)
	assert.Equal(t, "NVCF_REGION", RegionEnv)

	// NVCT prefixed environment variables
	assert.Equal(t, "NVCT_NCA_ID", NVCTNCAIDEnv)
	assert.Equal(t, "NVCT_TASK_ID", TaskIDEnv)
	assert.Equal(t, "NVCT_TASK_NAME", TaskNameEnv)

	// Encoded environment variables
	assert.Equal(t, "FUNCTION_NAME", FunctionNameEncodedEnvKey)
	assert.Equal(t, "TASK_NAME", TaskNameEncodedEnvKey)

	// Deprecated constants
	assert.Equal(t, "CLOUD_PROVIDER", CloudProviderEnvDep)
	assert.Equal(t, "NVCF_CLOUD_PROVIDER", CloudProviderEnv)

	assert.Equal(t, "NVCF_ENV", EnvironmentEnv)
}

func TestGetEncodedVarByKey(t *testing.T) {
	tests := []struct {
		name    string
		envB64  string
		key     string
		want    string
		wantErr bool
	}{
		{
			name:    "valid function name",
			envB64:  base64.StdEncoding.EncodeToString([]byte("FUNCTION_NAME=test-function")),
			key:     FunctionNameEncodedEnvKey,
			want:    "test-function",
			wantErr: false,
		},
		{
			name:    "valid task name",
			envB64:  base64.StdEncoding.EncodeToString([]byte("TASK_NAME=test-task")),
			key:     TaskNameEncodedEnvKey,
			want:    "test-task",
			wantErr: false,
		},
		{
			name:    "empty value",
			envB64:  base64.StdEncoding.EncodeToString([]byte("OTHER_VAR=value")),
			key:     FunctionNameEncodedEnvKey,
			want:    "",
			wantErr: false,
		},
		{
			name:    "invalid base64",
			envB64:  "||||||||DKLDJFKDKLJF invalid-base64",
			key:     FunctionNameEncodedEnvKey,
			want:    "",
			wantErr: true,
		},
		{
			name:    "multiple env vars",
			envB64:  base64.StdEncoding.EncodeToString([]byte("FUNCTION_NAME=test-function\nTASK_NAME=test-task\nOTHER_VAR=value")),
			key:     FunctionNameEncodedEnvKey,
			want:    "test-function",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetEncodedVarByKey(tt.envB64, tt.key)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
