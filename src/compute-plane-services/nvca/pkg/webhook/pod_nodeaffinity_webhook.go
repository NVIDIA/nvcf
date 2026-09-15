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
	"context"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerror "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
)

var (
	_ admission.CustomDefaulter = (*podAffinityWebhook)(nil)
)

type podAffinityWebhook struct {
	PodAffinityOptions
}

func (v *podAffinityWebhook) Default(ctx context.Context, obj runtime.Object) (err error) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		return fmt.Errorf("expected *v1.Pod, got: %T", obj)
	}

	var errs []error

	if v.SharedClusterOn != nil && v.SharedClusterOn.Load() {
		errs = append(errs, setSharedClusterNodeAffinityRequirement(pod)...)
	}
	if v.UniformInstanceLabels {
		errs = append(errs, setUniformInstanceLabelPodAffinityRequirement(ctx, pod)...)
	}
	if v.HostIsolation {
		errs = append(errs, setHostIsolatedPodTolerations(pod, false)...)
	}
	if v.AccountIsolation {
		errs = append(errs, setHostIsolatedPodTolerations(pod, true)...)
	}

	return utilerror.NewAggregate(errs)
}

func setNodeAffinityRequirements(pod *v1.Pod) *v1.NodeSelector {
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &v1.Affinity{}
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		pod.Spec.Affinity.NodeAffinity = &v1.NodeAffinity{}
	}
	nodeAffinity := pod.Spec.Affinity.NodeAffinity
	if nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &v1.NodeSelector{}
	}
	return nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
}

func getNodeAffinityRequirementInstanceTypeValues(pod *v1.Pod) ([]string, bool) {
	if pod.Spec.Affinity == nil {
		return nil, false
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		return nil, false
	}
	nodeAffinity := pod.Spec.Affinity.NodeAffinity
	if nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return nil, false
	}
	for _, nst := range nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, matchExpr := range nst.MatchExpressions {
			if matchExpr.Key == nodefeatures.DeprecatedInstanceTypeLabelKey ||
				matchExpr.Key == instanceTypeLK {
				return matchExpr.Values, true
			}
		}
	}
	return nil, false
}

const trueVal = "true"

func setSharedClusterNodeAffinityRequirement(pod *v1.Pod) (errs []error) {
	nodeReqs := setNodeAffinityRequirements(pod)

	// Ensure pods are only scheduled on specific nodes.
	for i, nodeSelTerm := range nodeReqs.NodeSelectorTerms {
		keyFound := false
		for _, matchExpr := range nodeSelTerm.MatchExpressions {
			if matchExpr.Key == sharedcluster.ScheduleLabelKey {
				if matchExpr.Operator == v1.NodeSelectorOpIn &&
					len(matchExpr.Values) == 1 &&
					matchExpr.Values[0] == trueVal {
					keyFound = true
				} else {
					errs = append(errs,
						fmt.Errorf("match expression key %q exists, but either operation "+
							"is not %q or values are not %+q",
							sharedcluster.ScheduleLabelKey, v1.NodeSelectorOpIn, []string{trueVal},
						))
				}
				break
			}
		}
		if !keyFound {
			nodeReqs.NodeSelectorTerms[i].MatchExpressions = append(nodeReqs.NodeSelectorTerms[i].MatchExpressions,
				v1.NodeSelectorRequirement{
					Key:      sharedcluster.ScheduleLabelKey,
					Operator: v1.NodeSelectorOpIn,
					Values:   []string{trueVal},
				},
			)
		}
	}
	if len(nodeReqs.NodeSelectorTerms) == 0 {
		nodeReqs.NodeSelectorTerms = append(nodeReqs.NodeSelectorTerms, v1.NodeSelectorTerm{
			MatchExpressions: []v1.NodeSelectorRequirement{{
				Key:      sharedcluster.ScheduleLabelKey,
				Operator: v1.NodeSelectorOpIn,
				Values:   []string{trueVal},
			}},
		})
	}

	return errs
}

func setUniformInstanceLabelPodAffinityRequirement(ctx context.Context, pod *v1.Pod) (errs []error) {
	log := core.GetLogger(ctx)

	nodeSelFound := false
	if pod.Spec.NodeSelector != nil {
		if instanceTypeVal, ok := pod.Spec.NodeSelector[nodefeatures.DeprecatedInstanceTypeLabelKey]; ok {
			pod.Spec.NodeSelector[instanceTypeLK] = instanceTypeVal
			delete(pod.Spec.NodeSelector, nodefeatures.DeprecatedInstanceTypeLabelKey)
			nodeSelFound = true
		}
	}

	nodeAffinityFound := false
	if instanceTypeValues, found := getNodeAffinityRequirementInstanceTypeValues(pod); found {
		// Only set node requirements if instance type label(s) were found
		// since the apiserver expects at least one selector term to be set.
		nodeReqs := setNodeAffinityRequirements(pod)
		nodeAffinityFound = true
		for i, nodeSelTerm := range nodeReqs.NodeSelectorTerms {
			hasUniformITExpr := false
			depITExprIdx := -1
			for j := 0; j < len(nodeSelTerm.MatchExpressions); j++ {
				matchExpr := nodeSelTerm.MatchExpressions[j]
				switch matchExpr.Key {
				case nodefeatures.DeprecatedInstanceTypeLabelKey:
					depITExprIdx = j
				case instanceTypeLK:
					hasUniformITExpr = true
				}
			}
			if !hasUniformITExpr {
				if depITExprIdx != -1 {
					// The deprecated label was found.
					nodeReqs.NodeSelectorTerms[i].MatchExpressions[depITExprIdx] = v1.NodeSelectorRequirement{
						Key:      instanceTypeLK,
						Operator: v1.NodeSelectorOpIn,
						Values:   instanceTypeValues,
					}
				} else {
					// Neither were found.
					nodeReqs.NodeSelectorTerms[i].MatchExpressions = append(nodeReqs.NodeSelectorTerms[i].MatchExpressions,
						v1.NodeSelectorRequirement{
							Key:      instanceTypeLK,
							Operator: v1.NodeSelectorOpIn,
							Values:   instanceTypeValues,
						},
					)
				}
			}
		}
	}

	if !nodeSelFound && !nodeAffinityFound {
		// Only log this error for Helm mini service-generated Pods
		// since they will not have these selectors/affinities set.
		log.Debugf("Neither node selector or affinity for instance type were found on Pod %s", pod.Name)
	}

	return errs
}
