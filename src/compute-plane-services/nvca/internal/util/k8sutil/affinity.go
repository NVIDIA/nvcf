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
	corev1 "k8s.io/api/core/v1"
)

// SetInstanceTypeNodeAffinity adds a required node affinity rule that pins
// pods to nodes matching the given instance-type label key/value pair.
func SetInstanceTypeNodeAffinity(pts *corev1.PodSpec, ikey, ival string) {
	if pts.Affinity == nil {
		pts.Affinity = &corev1.Affinity{}
	}
	if pts.Affinity.NodeAffinity == nil {
		pts.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	nodeReqs := pts.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if nodeReqs == nil {
		nodeReqs = &corev1.NodeSelector{}
		pts.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = nodeReqs
	}
	nodeSelReq := corev1.NodeSelectorRequirement{
		Key:      ikey,
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{ival},
	}
	if len(nodeReqs.NodeSelectorTerms) == 0 {
		nodeReqs.NodeSelectorTerms = append(nodeReqs.NodeSelectorTerms, corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{nodeSelReq},
		})
	} else {
		for i, nst := range nodeReqs.NodeSelectorTerms {
			found := false
			for j, expr := range nst.MatchExpressions {
				if expr.Key == ikey {
					nodeReqs.NodeSelectorTerms[i].MatchExpressions[j] = nodeSelReq
					found = true
					break
				}
			}
			if !found {
				nodeReqs.NodeSelectorTerms[i].MatchExpressions = append(
					nodeReqs.NodeSelectorTerms[i].MatchExpressions,
					nodeSelReq,
				)
			}
		}
	}
}

// SetCPUWorkloadNodeAffinity removes any required instance-type affinity and
// adds a soft preference for nodes that do NOT have the instance-type label.
// This steers CPU-only workloads toward non-GPU nodes without hard-blocking GPU nodes.
func SetCPUWorkloadNodeAffinity(pts *corev1.PodSpec, instanceTypeLabelKey string) {
	if pts.Affinity == nil {
		pts.Affinity = &corev1.Affinity{}
	}
	if pts.Affinity.NodeAffinity == nil {
		pts.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}

	RemoveInstanceTypeRequiredAffinity(pts.Affinity.NodeAffinity, instanceTypeLabelKey)

	pts.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
		pts.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
		corev1.PreferredSchedulingTerm{
			Weight: 100,
			Preference: corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      instanceTypeLabelKey,
					Operator: corev1.NodeSelectorOpDoesNotExist,
				}},
			},
		},
	)
}

// RemoveInstanceTypeRequiredAffinity removes any RequiredDuringSchedulingIgnoredDuringExecution
// node selector terms that match instanceTypeLabelKey.
//
// The translate library unconditionally sets instance-type node affinity on all pods
// (including CPU-only pods like utilsPod) during translation. When HelmAllowCPUNodes
// is enabled, CPU-only pods should NOT have GPU instance-type required affinity since
// they should be scheduled on CPU nodes instead. This function clears that pre-existing
// affinity so we can apply the correct CPU workload anti-preference instead.
func RemoveInstanceTypeRequiredAffinity(na *corev1.NodeAffinity, instanceTypeLabelKey string) {
	if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return
	}

	nodeSelector := na.RequiredDuringSchedulingIgnoredDuringExecution
	filteredTerms := make([]corev1.NodeSelectorTerm, 0, len(nodeSelector.NodeSelectorTerms))

	for _, term := range nodeSelector.NodeSelectorTerms {
		filteredExprs := make([]corev1.NodeSelectorRequirement, 0, len(term.MatchExpressions))
		for _, expr := range term.MatchExpressions {
			if expr.Key != instanceTypeLabelKey {
				filteredExprs = append(filteredExprs, expr)
			}
		}
		if len(filteredExprs) > 0 || len(term.MatchFields) > 0 {
			term.MatchExpressions = filteredExprs
			filteredTerms = append(filteredTerms, term)
		}
	}

	if len(filteredTerms) == 0 {
		na.RequiredDuringSchedulingIgnoredDuringExecution = nil
	} else {
		nodeSelector.NodeSelectorTerms = filteredTerms
	}
}
