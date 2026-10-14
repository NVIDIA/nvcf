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

package clustervalidator

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/klog/v2"
)

// separation is how firmly a pod template keeps its own pods apart across a
// topology: not at all, when it can (soft), or always (hard).
type separation int

const (
	separationNone separation = iota
	separationSoft
	separationHard
)

// hostnameSeparation is how firmly tpl, in namespace ns, keeps its own pods
// off each other's nodes: a required pod anti-affinity term is hard; a
// preferred term or a hostname spread is soft. Even a DoNotSchedule spread of
// maxSkew 1 puts two pods on one node when there are fewer nodes than pods.
// Terms against other pods do not count.
func hostnameSeparation(tpl *corev1.PodTemplateSpec, ns string) separation {
	own := labels.Set(tpl.Labels)
	sep := separationNone
	if a := tpl.Spec.Affinity; a != nil && a.PodAntiAffinity != nil {
		for i := range a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			if separatesPeers(ns, own, &a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[i]) {
				return separationHard
			}
		}
		for i := range a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			if separatesPeers(ns, own, &a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[i].
				PodAffinityTerm) {
				sep = separationSoft
			}
		}
	}
	for i := range tpl.Spec.TopologySpreadConstraints {
		c := &tpl.Spec.TopologySpreadConstraints[i]
		if c.TopologyKey == corev1.LabelHostname && selectsLabels(c.LabelSelector, own) {
			sep = separationSoft
		}
	}
	return sep
}

// zoneSpread is how firmly tpl spreads its own pods over
// topology.kubernetes.io/zone: DoNotSchedule is hard, ScheduleAnyway soft.
func zoneSpread(tpl *corev1.PodTemplateSpec) separation {
	sep := separationNone
	for i := range tpl.Spec.TopologySpreadConstraints {
		c := &tpl.Spec.TopologySpreadConstraints[i]
		if c.TopologyKey != corev1.LabelTopologyZone || !selectsLabels(c.LabelSelector, labels.Set(tpl.Labels)) {
			continue
		}
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			return separationHard
		}
		sep = separationSoft
	}
	return sep
}

// schedulableFor reports whether tpl's pods can be placed on n: Ready, not
// cordoned, and admitting them by nodeAdmits.
func schedulableFor(n *corev1.Node, tpl *corev1.PodTemplateSpec) bool {
	return isNodeReady(n) && !n.Spec.Unschedulable &&
		nodeAdmits(n, tpl.Spec.Tolerations, tpl.Spec.NodeSelector, requiredNodeAffinity(&tpl.Spec))
}

// requiredNodeAffinity is spec's required node affinity, nil when it has none.
func requiredNodeAffinity(spec *corev1.PodSpec) *corev1.NodeSelector {
	if a := spec.Affinity; a != nil && a.NodeAffinity != nil {
		return a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	}
	return nil
}

// nodeAdmits reports whether n's labels match nodeSelector and, when set, one
// term of the required node affinity, and whether tolerations tolerate each of
// its NoSchedule and NoExecute taints.
func nodeAdmits(n *corev1.Node, tolerations []corev1.Toleration, nodeSelector map[string]string,
	affinity *corev1.NodeSelector) bool {
	if !labels.SelectorFromSet(nodeSelector).Matches(labels.Set(n.Labels)) {
		return false
	}
	if affinity != nil && !slices.ContainsFunc(affinity.NodeSelectorTerms,
		func(t corev1.NodeSelectorTerm) bool { return nodeSelectorTermMatches(&t, n) }) {
		return false
	}
	for i := range n.Spec.Taints {
		t := &n.Spec.Taints[i]
		if t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if !slices.ContainsFunc(tolerations, func(tol corev1.Toleration) bool {
			return tol.ToleratesTaint(klog.Background(), t, true)
		}) {
			return false
		}
	}
	return true
}

// nodeSelectorOperators maps node selector operators to label selector ones.
var nodeSelectorOperators = map[corev1.NodeSelectorOperator]selection.Operator{
	corev1.NodeSelectorOpIn: selection.In, corev1.NodeSelectorOpNotIn: selection.NotIn,
	corev1.NodeSelectorOpExists: selection.Exists, corev1.NodeSelectorOpDoesNotExist: selection.DoesNotExist,
	corev1.NodeSelectorOpGt: selection.GreaterThan, corev1.NodeSelectorOpLt: selection.LessThan,
}

// nodeSelectorTermMatches reports whether n meets every requirement of t, as
// the scheduler reads it: a term with no requirement matches no node, and the
// only field a term can name is metadata.name, with In or NotIn.
func nodeSelectorTermMatches(t *corev1.NodeSelectorTerm, n *corev1.Node) bool {
	if len(t.MatchExpressions) == 0 && len(t.MatchFields) == 0 {
		return false
	}
	for _, r := range t.MatchExpressions {
		op, known := nodeSelectorOperators[r.Operator]
		req, err := labels.NewRequirement(r.Key, op, r.Values)
		if !known || err != nil || !req.Matches(labels.Set(n.Labels)) {
			return false
		}
	}
	for _, r := range t.MatchFields {
		named := slices.Contains(r.Values, n.Name)
		if r.Key != metav1.ObjectNameField || (r.Operator != corev1.NodeSelectorOpIn || !named) &&
			(r.Operator != corev1.NodeSelectorOpNotIn || named) {
			return false
		}
	}
	return true
}
