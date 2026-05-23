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
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

const (
	InstanceTypeEnvVarLegacy = "INSTANCE_TYPE"
	InstanceTypeNameEnvVar   = "INSTANCE_TYPE_NAME"
	InstanceTypeValueEnvVar  = "INSTANCE_TYPE_VALUE"

	ICMSEnvironmentEnv  = "ICMS_ENVIRONMENT"
	SpotEnvironmentEnv  = "SPOT_ENVIRONMENT"
	GPUNameEnv          = "GPU_NAME"
	GPUCountEnv        = "ATTACHED_GPU_COUNT"

	CloudProviderEnv = "NVCF_CLOUD_PROVIDER"

	// Worker image envs
	UtilsImageEnv             = "UTILS_CONTAINER"
	InitImageEnv              = "INIT_CONTAINER"
	BYOOOTelCollectorImageEnv = "BYOO_OTEL_COLLECTOR_CONTAINER"
	NICLLSUtilsImageEnv       = "NICLLS_CONTAINER"
	ESSAgentContainerEnv      = "ESS_AGENT_CONTAINER"

	// Workload image envs
	ContainerFunctionImageEnv = "INFERENCE_CONTAINER"
	ContainerTaskImageEnv     = "TASK_CONTAINER"

	// Container args/env configuration
	InferenceContainerArgsEnv = "INFERENCE_CONTAINER_ARGS"
	InferenceContainerEnvEnv  = "INFERENCE_CONTAINER_ENV"
	TaskContainerArgsEnv      = "TASK_CONTAINER_ARGS"
	TaskContainerEnvEnv       = "TASK_CONTAINER_ENV"

	// WorkerPullSecretEnv contains the worker pull secret.
	WorkerPullSecretEnv = "SIDECAR_CREDENTIAL" //nolint:gosec // false positive: env var name, not credential

	// NCAIDEnv is the NCA ID.
	NCAIDEnv = "NVCF_NCA_ID"
	// NVCTNCAIDEnv is the NCA ID for the task.
	NVCTNCAIDEnv = "NVCT_NCA_ID"
	// FunctionIDEnv is the function ID.
	FunctionIDEnv = "NVCF_FUNCTION_ID"
	// FunctionVersionIDEnv is the function version ID.
	FunctionVersionIDEnv = "NVCF_FUNCTION_VERSION_ID"
	// FunctionNameEnv is the function name.
	FunctionNameEnv = "NVCF_FUNCTION_NAME"
	// AssetDirEnv is the asset directory.
	AssetDirEnv = "NVCF_ASSET_DIR"
	// LargeOutputDirEnv is the large output directory.
	LargeOutputDirEnv = "NVCF_LARGE_OUTPUT_DIR"
	// BackendEnv is the backend.
	BackendEnv = "NVCF_BACKEND"
	// InstanceTypeEnv is the instance type.
	InstanceTypeEnv = "NVCF_INSTANCETYPE"
	// InstanceTypeValueEnv is the instance type value.
	InstanceTypeValueEnv = "NVCF_INSTANCETYPE_VALUE"
	// RegionEnv is the region.
	RegionEnv = "NVCF_REGION"
	// TaskIDEnv is the task ID.
	TaskIDEnv = "NVCT_TASK_ID"
	// TaskNameEnv is the task name.
	TaskNameEnv = "NVCT_TASK_NAME"
	// EnvironmentEnv is the environment.
	EnvironmentEnv = "NVCF_ENV"

	// FunctionNameEncodedEnvKey is encoded function name env var.
	FunctionNameEncodedEnvKey = "FUNCTION_NAME"

	// TaskNameEncodedEnvKey is encoded task name env var.
	TaskNameEncodedEnvKey = "TASK_NAME"

	// NCAIDEncodedEnvKey is the NCA ID key in encoded environment.
	NCAIDEncodedEnvKey = "NCA_ID"

	// Deprecated: Use CloudProviderEnv. This will be removed once the utils container migrates to using NVCF_ prefixes on env vars
	CloudProviderEnvDep = "CLOUD_PROVIDER"

	// Task result envs
	NVCTProgressFilePathEnvKey = "NVCT_PROGRESS_FILE_PATH"
	NVCTResultsDirEnvKey       = "NVCT_RESULTS_DIR"
	NVCTTaskDir                = "/var/task"
	NVCTTaskResultsDir         = NVCTTaskDir + "/results"
	NVCTTaskProgressFilePath   = NVCTTaskResultsDir + "/progress"
)

var (
	WorkerImageEnvs = []string{
		UtilsImageEnv,
		InitImageEnv,
		BYOOOTelCollectorImageEnv,
		NICLLSUtilsImageEnv,
		ESSAgentContainerEnv,
	}
	WorkloadImageEnvs = []string{
		ContainerFunctionImageEnv,
		ContainerTaskImageEnv,
	}
)

func MakeWorkloadEnvVars(workloadType MessageAction) []corev1.EnvVar {
	workloadEnvVars := []corev1.EnvVar{
		{
			Name: RegionEnv,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['nvcf.nvidia.io/region']"},
			},
		},
		{
			Name: InstanceTypeEnv,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['nvcf.nvidia.io/instance-type-name']"},
			},
		},
		{
			Name: InstanceTypeValueEnv,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['nvcf.nvidia.io/instance-type-value']"},
			},
		},
		{
			Name: BackendEnv,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['nvcf.nvidia.io/backend']"},
			},
		},
		{
			Name: EnvironmentEnv,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['nvcf.nvidia.io/environment']"},
			},
		},
	}

	// Add function-related env vars if the workload is a function creation.
	switch workloadType {
	case FunctionCreationAction:
		workloadEnvVars = append(workloadEnvVars,
			corev1.EnvVar{
				Name: NCAIDEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['nca-id']"},
				},
			},
			corev1.EnvVar{
				Name: FunctionIDEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['function-id']"},
				},
			},
			corev1.EnvVar{
				Name: FunctionVersionIDEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['function-version-id']"},
				},
			},
			corev1.EnvVar{
				Name: FunctionNameEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['function-name']"},
				},
			},
		)
	case TaskCreationAction:
		// Add task-related env vars if the workload is a task creation.
		workloadEnvVars = append(workloadEnvVars,
			corev1.EnvVar{
				Name: NVCTNCAIDEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['nca-id']"},
				},
			},
			corev1.EnvVar{
				Name: TaskIDEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['task-id']"},
				},
			},
			corev1.EnvVar{
				Name: TaskNameEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['task-name']"},
				},
			},
		)
		workloadEnvVars = append(workloadEnvVars, MakeNVCTResultEnvs()...)
	default:
		// If the workload is not a function or task creation, return nil.
		return nil
	}

	return workloadEnvVars
}

func MakeNVCTResultEnvs() (envs []corev1.EnvVar) {
	envs = []corev1.EnvVar{
		{Name: NVCTProgressFilePathEnvKey, Value: NVCTTaskProgressFilePath},
		{Name: NVCTResultsDirEnvKey, Value: NVCTTaskResultsDir},
	}
	return envs
}

// GetEncodedVarByKey decodes the base64 environment variables and returns the encoded variable if found.
func GetEncodedVarByKey(envB64 string, key string) (string, error) {
	envs, err := DecodeEnvironmentB64(envB64, EnvDecoderText)
	if err != nil {
		return "", fmt.Errorf("failed to decode environment: %w", err)
	}
	return envs[key], nil
}
