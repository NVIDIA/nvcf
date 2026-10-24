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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestSetCPUWorkloadNodeAffinity_DedupesMultiExpressionPreference(t *testing.T) {
	const instanceTypeKey = "nvca.nvcf.nvidia.io/instance-type"

	podSpec := &corev1.PodSpec{
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
					{
						Weight: 100,
						Preference: corev1.NodeSelectorTerm{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "topology.kubernetes.io/zone",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"us-west"},
								},
								{
									Key:      instanceTypeKey,
									Operator: corev1.NodeSelectorOpDoesNotExist,
								},
								{
									Key:      "kubernetes.io/arch",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"amd64"},
								},
							},
							MatchFields: []corev1.NodeSelectorRequirement{{
								Key:      "metadata.name",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"node-a"},
							}},
						},
					},
				},
			},
		},
	}

	SetCPUWorkloadNodeAffinity(podSpec, instanceTypeKey)
	SetCPUWorkloadNodeAffinity(podSpec, instanceTypeKey)

	preferred := podSpec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	require.Len(t, preferred, 1, "should not append a duplicate soft anti-affinity term")
	assert.Equal(t, int32(100), preferred[0].Weight)
	assert.Len(t, preferred[0].Preference.MatchExpressions, 3)
	assert.Len(t, preferred[0].Preference.MatchFields, 1)

	found := false
	for _, expr := range preferred[0].Preference.MatchExpressions {
		if expr.Key == instanceTypeKey &&
			expr.Operator == corev1.NodeSelectorOpDoesNotExist &&
			len(expr.Values) == 0 {
			found = true
		}
	}
	assert.True(t, found)
}
