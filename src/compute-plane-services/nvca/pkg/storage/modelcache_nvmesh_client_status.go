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

package storage

import corev1 "k8s.io/api/core/v1"

const (
	// NVMeshClientStatusNodeLabel is set to NVMeshClientStatusNodeReady on any node that can mount NVMesh volumes.
	// All init cache job pods *must* be scheduled on one of these nodes.
	// All workload pods that request caching *should* be scheduled on one of these nodes.
	NVMeshClientStatusNodeLabel = "nvmesh-csi/client-status"
	NVMeshClientStatusNodeReady = "ready"
)

// SetNVMeshClientStatusSchedulingRequirement sets the NVMeshClientStatusNodeLabel=NVMeshClientStatusNodeReady
// requirement for scheduling on ps.
func SetNVMeshClientStatusSchedulingRequirement(ps *corev1.PodSpec) {
	if ps.Affinity == nil {
		ps.Affinity = &corev1.Affinity{}
	}
	if ps.Affinity.NodeAffinity == nil {
		ps.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	nodeAffinity := ps.Affinity.NodeAffinity
	if nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	sparams := nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

	for _, selTerm := range sparams.NodeSelectorTerms {
		for _, expr := range selTerm.MatchExpressions {
			if expr.Key == NVMeshClientStatusNodeLabel {
				return
			}
		}
	}
	sparams.NodeSelectorTerms = append(sparams.NodeSelectorTerms, corev1.NodeSelectorTerm{
		MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key:      NVMeshClientStatusNodeLabel,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{NVMeshClientStatusNodeReady},
		}},
	})
}
