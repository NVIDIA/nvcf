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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// How firmly a pod template keeps its own pods off each other's nodes and
// spreads them over zones. Terms against other pods do not count.
func TestRenderedSeparation(t *testing.T) {
	own := map[string]string{haInstanceLabel: "api"}
	term := func(key string, sel map[string]string) corev1.PodAffinityTerm {
		return corev1.PodAffinityTerm{LabelSelector: &metav1.LabelSelector{MatchLabels: sel}, TopologyKey: key}
	}
	spread := func(key string, skew int32, when corev1.UnsatisfiableConstraintAction) corev1.TopologySpreadConstraint {
		return corev1.TopologySpreadConstraint{MaxSkew: skew, TopologyKey: key, WhenUnsatisfiable: when,
			LabelSelector: &metav1.LabelSelector{MatchLabels: own}}
	}
	required := func(ts ...corev1.PodAffinityTerm) *corev1.Affinity {
		return &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: ts}}
	}
	preferred := func(ts ...corev1.PodAffinityTerm) *corev1.Affinity {
		aa := &corev1.PodAntiAffinity{}
		for _, tm := range ts {
			aa.PreferredDuringSchedulingIgnoredDuringExecution = append(
				aa.PreferredDuringSchedulingIgnoredDuringExecution,
				corev1.WeightedPodAffinityTerm{Weight: 100, PodAffinityTerm: tm})
		}
		return &corev1.Affinity{PodAntiAffinity: aa}
	}
	spreads := func(cs ...corev1.TopologySpreadConstraint) []corev1.TopologySpreadConstraint { return cs }
	for name, tc := range map[string]struct {
		affinity       *corev1.Affinity
		spreads        []corev1.TopologySpreadConstraint
		hostname, zone separation
	}{
		"nothing":                 {},
		"required hostname term":  {affinity: required(term(corev1.LabelHostname, own)), hostname: separationHard},
		"preferred hostname term": {affinity: preferred(term(corev1.LabelHostname, own)), hostname: separationSoft},
		"both hostname terms": {affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: required(term(corev1.LabelHostname, own)).PodAntiAffinity.
				RequiredDuringSchedulingIgnoredDuringExecution,
			PreferredDuringSchedulingIgnoredDuringExecution: preferred(term(corev1.LabelHostname, own)).PodAntiAffinity.
				PreferredDuringSchedulingIgnoredDuringExecution,
		}}, hostname: separationHard},
		"another app's term":      {affinity: required(term(corev1.LabelHostname, map[string]string{"app": "x"}))},
		"zone anti-affinity only": {affinity: required(term(corev1.LabelTopologyZone, own))},
		"hard hostname spread": {spreads: spreads(spread(corev1.LabelHostname, 1, corev1.DoNotSchedule)),
			hostname: separationSoft},
		"hostname spread with skew 2": {spreads: spreads(spread(corev1.LabelHostname, 2, corev1.DoNotSchedule)),
			hostname: separationSoft},
		"soft hostname spread": {spreads: spreads(spread(corev1.LabelHostname, 1, corev1.ScheduleAnyway)),
			hostname: separationSoft},
		"hard zone spread": {spreads: spreads(spread(corev1.LabelTopologyZone, 1, corev1.DoNotSchedule)),
			zone: separationHard},
		"soft zone spread": {spreads: spreads(spread(corev1.LabelTopologyZone, 1, corev1.ScheduleAnyway)),
			zone: separationSoft},
		"required term and hard hostname spread": {affinity: required(term(corev1.LabelHostname, own)),
			spreads: spreads(spread(corev1.LabelHostname, 1, corev1.DoNotSchedule)), hostname: separationHard},
		"enforced render": {affinity: required(term(corev1.LabelHostname, own)),
			spreads:  spreads(spread(corev1.LabelTopologyZone, 1, corev1.DoNotSchedule)),
			hostname: separationHard, zone: separationHard},
	} {
		tpl := &corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: own},
			Spec:       corev1.PodSpec{Affinity: tc.affinity, TopologySpreadConstraints: tc.spreads},
		}
		assert.Equal(t, tc.hostname, hostnameSeparation(tpl, "nvcf"), "%s: hostname", name)
		assert.Equal(t, tc.zone, zoneSpread(tpl), "%s: zone", name)
	}
}

// A node is one a pod template can use when it is Ready and not cordoned, its
// labels meet the node selector and one required node affinity term, and every
// NoSchedule or NoExecute taint is tolerated.
func TestSchedulableFor(t *testing.T) {
	node := func() *corev1.Node {
		n := haNode("node-a", "zone-a")
		n.Labels["pool"], n.Labels["gen"] = "nvcf", "5"
		return n
	}
	term := func(reqs ...corev1.NodeSelectorRequirement) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchExpressions: reqs}
	}
	req := func(key string, op corev1.NodeSelectorOperator, values ...string) corev1.NodeSelectorRequirement {
		return corev1.NodeSelectorRequirement{Key: key, Operator: op, Values: values}
	}
	field := func(op corev1.NodeSelectorOperator, values ...string) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{
			{Key: metav1.ObjectNameField, Operator: op, Values: values}}}
	}
	for name, tc := range map[string]struct {
		node  func(*corev1.Node)
		spec  corev1.PodSpec
		terms []corev1.NodeSelectorTerm
		want  bool
	}{
		"plain":            {want: true},
		"cordoned":         {node: func(n *corev1.Node) { n.Spec.Unschedulable = true }},
		"NotReady":         {node: func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse }},
		"selector met":     {spec: corev1.PodSpec{NodeSelector: map[string]string{"pool": "nvcf"}}, want: true},
		"selector not met": {spec: corev1.PodSpec{NodeSelector: map[string]string{"pool": "gpu"}}},
		"In":               {terms: []corev1.NodeSelectorTerm{term(req("pool", "In", "nvcf"))}, want: true},
		"NotIn":            {terms: []corev1.NodeSelectorTerm{term(req("pool", "NotIn", "nvcf"))}},
		"Exists":           {terms: []corev1.NodeSelectorTerm{term(req("pool", "Exists"))}, want: true},
		"DoesNotExist":     {terms: []corev1.NodeSelectorTerm{term(req("pool", "DoesNotExist"))}},
		"Gt":               {terms: []corev1.NodeSelectorTerm{term(req("gen", "Gt", "4"))}, want: true},
		"Lt":               {terms: []corev1.NodeSelectorTerm{term(req("gen", "Lt", "4"))}},
		"unknown operator": {terms: []corev1.NodeSelectorTerm{term(req("pool", "Like", "nvcf"))}},
		"one term of two": {terms: []corev1.NodeSelectorTerm{term(req("pool", "In", "gpu")), field("In", "node-a")},
			want: true},
		"empty term":           {terms: []corev1.NodeSelectorTerm{{}}},
		"no terms":             {terms: []corev1.NodeSelectorTerm{}},
		"named":                {terms: []corev1.NodeSelectorTerm{field("In", "node-a")}, want: true},
		"named out":            {terms: []corev1.NodeSelectorTerm{field("NotIn", "node-a")}},
		"other name":           {terms: []corev1.NodeSelectorTerm{field("In", "node-b")}},
		"other name, NotIn":    {terms: []corev1.NodeSelectorTerm{field("NotIn", "node-b")}, want: true},
		"untolerated taint":    {node: taint(corev1.TaintEffectNoSchedule)},
		"untolerated NoExec":   {node: taint(corev1.TaintEffectNoExecute)},
		"soft taint":           {node: taint(corev1.TaintEffectPreferNoSchedule), want: true},
		"tolerated by key":     {node: taint(corev1.TaintEffectNoSchedule), spec: tolerating("dedicated"), want: true},
		"tolerated by Exists":  {node: taint(corev1.TaintEffectNoExecute), spec: tolerating(""), want: true},
		"tolerating other key": {node: taint(corev1.TaintEffectNoSchedule), spec: tolerating("gpu")},
	} {
		n := node()
		if tc.node != nil {
			tc.node(n)
		}
		spec := tc.spec
		if tc.terms != nil {
			spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: tc.terms}}}
		}
		assert.Equal(t, tc.want, schedulableFor(n, &corev1.PodTemplateSpec{Spec: spec}), name)
	}
}

func taint(effect corev1.TaintEffect) func(*corev1.Node) {
	return func(n *corev1.Node) {
		n.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "nvcf", Effect: effect}}
	}
}

// tolerating tolerates key with any value, or every taint when key is empty.
func tolerating(key string) corev1.PodSpec {
	return corev1.PodSpec{Tolerations: []corev1.Toleration{{Key: key, Operator: corev1.TolerationOpExists}}}
}
