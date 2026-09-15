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

package webhook

import (
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	hostnameLabelKey = "kubernetes.io/hostname"
)

func setHostIsolatedPodTolerations(pod *v1.Pod, ncaIsolation bool) (errs []error) {
	tenantIDKey, tenantIDVal, err := getTenantIDLabelKVs(pod, ncaIsolation)
	if err != nil {
		errs = append(errs, err)
		return errs
	}

	// Add toleration for the tenant ID
	newToleration := v1.Toleration{
		Key:      tenantIDKey,
		Operator: v1.TolerationOpEqual,
		Value:    tenantIDVal,
		Effect:   v1.TaintEffectNoExecute,
	}

	foundTol := false
	for _, currToleration := range pod.Spec.Tolerations {
		if cmp.Equal(currToleration, newToleration, cmpopts.EquateEmpty()) {
			foundTol = true
			break
		}
	}

	if !foundTol {
		pod.Spec.Tolerations = append(pod.Spec.Tolerations, newToleration)
	}

	// Initialize pod anti-affinity
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &v1.Affinity{}
	}
	if pod.Spec.Affinity.PodAntiAffinity == nil {
		pod.Spec.Affinity.PodAntiAffinity = &v1.PodAntiAffinity{}
	}
	podAntiAffinity := pod.Spec.Affinity.PodAntiAffinity

	// Create anti-affinity term that matches pods with different tenant ID
	term := v1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      tenantIDKey,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{tenantIDVal},
				},
				{
					Key:      tenantIDKey,
					Operator: metav1.LabelSelectorOpExists,
				},
			},
		},
		TopologyKey: hostnameLabelKey,
	}

	if ncaIsolation {
		// Required anti-affinity for account isolation
		foundTerm := false
		for _, currTerm := range podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			if cmp.Equal(currTerm, term, cmpopts.EquateEmpty()) {
				foundTerm = true
				break
			}
		}

		if !foundTerm {
			podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(
				podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
				term,
			)
		}
	} else {
		// Preferred anti-affinity for function/task isolation
		weightedTerm := v1.WeightedPodAffinityTerm{
			Weight:          100,
			PodAffinityTerm: term,
		}

		foundTerm := false
		for _, currTerm := range podAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			if cmp.Equal(currTerm, weightedTerm, cmpopts.EquateEmpty()) {
				foundTerm = true
				break
			}
		}

		if !foundTerm {
			podAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
				podAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
				weightedTerm,
			)
		}
	}

	return errs
}

func getTenantIDLabelKVs(pod *v1.Pod, ncaIsolation bool) (key, val string, err error) {
	if ncaIsolation {
		ncaID, hasNCAID := pod.Labels[nvcatypes.NCAIDKey]
		if !hasNCAID {
			return "", "", fmt.Errorf("pod %s/%s has no tenant label cannot set pod tolerations",
				pod.Namespace, pod.Name)
		}
		return nvcatypes.NCAIDKey, ncaID, nil
	}
	// else look at function/task isolation
	funcVerID, hasFuncVerID := pod.Labels[nvcatypes.FunctionVersionIDKey]
	taskID, hasTaskID := pod.Labels[nvcatypes.TaskIDKey]
	if (!hasFuncVerID && !hasTaskID) || (hasFuncVerID && hasTaskID) {
		return "", "", fmt.Errorf("pod %s/%s has no tenant label or conflicting labels, cannot set pod tolerations",
			pod.Namespace, pod.Name)
	}

	if hasFuncVerID {
		return nvcatypes.FunctionVersionIDKey, funcVerID, nil
	}
	return nvcatypes.TaskIDKey, taskID, nil
}
