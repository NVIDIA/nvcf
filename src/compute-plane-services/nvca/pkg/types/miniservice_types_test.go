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
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestMiniserviceMetadata_ToConfigMapData_Roundtrip(t *testing.T) {
	gpSec := int64(300)
	meta := MiniserviceMetadata{
		MessageAction: common.FunctionCreationAction,
		Annotations: map[string]string{
			"icms-request-id": "req-004",
			"nca-id":          "nca-x",
		},
		Labels: map[string]string{
			"function-id":         "fn-001",
			"function-version-id": "ver-002",
			MiniserviceNameLabel:  "inst-003",
		},
		PodAnnotations: map[string]string{
			"pod-anno-key": "pod-anno-value",
		},
		PodLabels: map[string]string{
			"pod-label-key":       "pod-label-value",
			"kai.scheduler/queue": "default-queue",
		},
		EnvVars: []corev1.EnvVar{
			{Name: "NVCF_FUNCTION_ID", Value: "fn-001"},
			{Name: "EXTRA_ENV", Value: "extra-value"},
		},
		OTelCollectorEnvVars: []corev1.EnvVar{
			{Name: "BYOO_LOG_CHUNK_MAX_PAYLOAD_BYTES", Value: "983040"},
		},
		NodeAffinityKey:               "nvca.nvcf.nvidia.io/instance-type",
		NodeAffinityValue:             "ON-PREM.GPU.A100x2",
		ServiceAccountName:            "miniservice-instance-permissions",
		Tolerations:                   []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
		ImagePullSecretNames:          []string{"secret-a", "secret-b"},
		TerminationGracePeriodSeconds: &gpSec,
		SchedulerName:                 "kai-scheduler",
	}

	data, err := meta.ToConfigMapData()
	require.NoError(t, err)

	got, err := FromConfigMapData(data)
	require.NoError(t, err)

	assert.Equal(t, meta.MessageAction, got.MessageAction)
	assert.Equal(t, meta.Annotations, got.Annotations)
	assert.Equal(t, meta.Labels, got.Labels)
	assert.Equal(t, meta.PodAnnotations, got.PodAnnotations)
	assert.Equal(t, meta.PodLabels, got.PodLabels)
	require.Len(t, got.EnvVars, 2)
	assert.Equal(t, "NVCF_FUNCTION_ID", got.EnvVars[0].Name)
	assert.Equal(t, "fn-001", got.EnvVars[0].Value)
	assert.Equal(t, "EXTRA_ENV", got.EnvVars[1].Name)
	assert.Equal(t, "extra-value", got.EnvVars[1].Value)
	assert.Equal(t, meta.OTelCollectorEnvVars, got.OTelCollectorEnvVars)
	assert.Equal(t, meta.NodeAffinityKey, got.NodeAffinityKey)
	assert.Equal(t, meta.NodeAffinityValue, got.NodeAffinityValue)
	assert.Equal(t, meta.ServiceAccountName, got.ServiceAccountName)
	require.Len(t, got.Tolerations, 1)
	assert.Equal(t, corev1.TolerationOpExists, got.Tolerations[0].Operator)
	assert.Equal(t, meta.ImagePullSecretNames, got.ImagePullSecretNames)
	require.NotNil(t, got.TerminationGracePeriodSeconds)
	assert.Equal(t, int64(300), *got.TerminationGracePeriodSeconds)
	assert.Equal(t, meta.SchedulerName, got.SchedulerName)
}

func TestMiniserviceMetadata_ToConfigMapData_TaskMode(t *testing.T) {
	meta := MiniserviceMetadata{
		MessageAction: common.TaskCreationAction,
		Labels: map[string]string{
			"task-id": "task-007",
		},
	}

	data, err := meta.ToConfigMapData()
	require.NoError(t, err)

	got, err := FromConfigMapData(data)
	require.NoError(t, err)

	assert.Equal(t, common.TaskCreationAction, got.MessageAction)
	assert.Equal(t, "task-007", got.Labels["task-id"])
}

func TestMiniserviceMetadata_ToConfigMapData_EmptyComplexFields(t *testing.T) {
	meta := MiniserviceMetadata{
		MessageAction: common.FunctionCreationAction,
	}

	data, err := meta.ToConfigMapData()
	require.NoError(t, err)

	assert.NotContains(t, data, "annotations")
	assert.NotContains(t, data, "labels")
	assert.NotContains(t, data, "envVars")
	assert.NotContains(t, data, "nodeAffinityKey")
	assert.NotContains(t, data, "nodeAffinityValue")

	got, err := FromConfigMapData(data)
	require.NoError(t, err)

	assert.Nil(t, got.Annotations)
	assert.Nil(t, got.Labels)
	assert.Nil(t, got.EnvVars)
	assert.Nil(t, got.OTelCollectorEnvVars)
	assert.Empty(t, got.NodeAffinityKey)
	assert.Empty(t, got.NodeAffinityValue)
}
