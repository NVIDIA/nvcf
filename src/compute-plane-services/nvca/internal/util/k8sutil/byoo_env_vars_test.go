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

package k8sutil

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestAddBYOOEnvVarsToPodSpecMutatesOnlyBYOOCollectorContainer(t *testing.T) {
	envs := []corev1.EnvVar{
		{Name: nvcaconfig.BYOOLogChunkMaxBodyBytesEnv, Value: "983040"},
		{Name: nvcaconfig.BYOOLogExporterBatchMaxSizeBytesEnv, Value: "1000000"},
		{Name: nvcaconfig.BYOOSREMetricsEnabledEnv, Value: "true"},
	}
	expectedEnv := []corev1.EnvVar{
		{Name: nvcaconfig.BYOOLogChunkMaxBodyBytesEnv, Value: "983040"},
		{Name: nvcaconfig.BYOOLogExporterBatchMaxSizeBytesEnv, Value: "1000000"},
		{Name: nvcaconfig.BYOOSREMetricsEnabledEnv, Value: "true"},
	}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: common.ByooOTelCollectorPodNameBase,
					Env: []corev1.EnvVar{
						{Name: nvcaconfig.BYOOLogChunkMaxBodyBytesEnv, Value: "1000000"},
					},
				},
				{Name: "inference"},
			},
		},
	}

	AddBYOOEnvVarsToPodSpec(&pod.Spec, envs)

	assert.Equal(t, expectedEnv, pod.Spec.Containers[0].Env)
	assert.Empty(t, pod.Spec.Containers[1].Env)
}
