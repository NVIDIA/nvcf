// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package election

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func gpuNode(name, pool, major, minor string, gpus int64) *corev1.Node {
	labels := map[string]string{"pool": pool}
	if major != "" {
		labels[ComputeMajorLabel] = major
	}
	if minor != "" {
		labels[ComputeMinorLabel] = minor
	}
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if gpus > 0 {
		n.Status.Allocatable = corev1.ResourceList{"nvidia.com/gpu": *resource.NewQuantity(gpus, resource.DecimalSI)}
	}
	return n
}

func scope(nodes ...runtime.Object) *NodeArchScope {
	return &NodeArchScope{KubeClient: fake.NewSimpleClientset(nodes...)}
}

func TestNodeArchScope(t *testing.T) {
	h100a, h100b := gpuNode("h1", "h100", "9", "0", 8), gpuNode("h2", "h100", "9", "0", 8)
	a100 := gpuNode("a1", "a100", "8", "0", 8)
	cpu := gpuNode("c1", "cpu", "", "", 0)
	unlabelled := gpuNode("u1", "new", "", "", 4)
	pinned := func(sel map[string]string) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeSelector: sel}}
	}
	cases := []struct {
		name   string
		s      *NodeArchScope
		pod    *corev1.Pod
		cc     string
		wantOK bool
	}{
		{"one architecture", scope(h100a, h100b, cpu), pinned(nil), "9.0", true},
		{"two architectures", scope(h100a, a100), pinned(nil), "", false},
		{"selector narrows to one", scope(h100a, a100), pinned(map[string]string{"pool": "a100"}), "8.0", true},
		{"a GPU node without the labels", scope(h100a, unlabelled), pinned(nil), "", false},
		{"no GPU node admits it", scope(h100a, cpu), pinned(map[string]string{"pool": "cpu"}), "", false},
		{"no client", &NodeArchScope{}, pinned(nil), "", false},
	}
	for _, c := range cases {
		cc, ok, err := c.s.ComputeCapability(context.Background(), c.pod)
		if err != nil || cc != c.cc || ok != c.wantOK {
			t.Errorf("%s: got %q, %v, %v; want %q, %v", c.name, cc, ok, err, c.cc, c.wantOK)
		}
	}
}

func TestAdmits(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"zone": "a", "gen": "9"}}}
	term := func(rs ...corev1.NodeSelectorRequirement) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchExpressions: rs}
	}
	req := func(key string, op corev1.NodeSelectorOperator, vals ...string) corev1.NodeSelectorRequirement {
		return corev1.NodeSelectorRequirement{Key: key, Operator: op, Values: vals}
	}
	withTerms := func(terms ...corev1.NodeSelectorTerm) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms}}}}}
	}
	cases := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"no constraints", &corev1.Pod{}, true},
		{"selector matches", &corev1.Pod{Spec: corev1.PodSpec{NodeSelector: map[string]string{"zone": "a"}}}, true},
		{"selector differs", &corev1.Pod{Spec: corev1.PodSpec{NodeSelector: map[string]string{"zone": "b"}}}, false},
		{"In", withTerms(term(req("zone", corev1.NodeSelectorOpIn, "a", "b"))), true},
		{"In misses", withTerms(term(req("zone", corev1.NodeSelectorOpIn, "b"))), false},
		{"NotIn", withTerms(term(req("zone", corev1.NodeSelectorOpNotIn, "b"))), true},
		{"NotIn on an absent label", withTerms(term(req("rack", corev1.NodeSelectorOpNotIn, "x"))), true},
		{"Exists", withTerms(term(req("gen", corev1.NodeSelectorOpExists))), true},
		{"DoesNotExist", withTerms(term(req("gen", corev1.NodeSelectorOpDoesNotExist))), false},
		{"Gt", withTerms(term(req("gen", corev1.NodeSelectorOpGt, "8"))), true},
		{"Lt", withTerms(term(req("gen", corev1.NodeSelectorOpLt, "8"))), false},
		{"Gt on a non-number", withTerms(term(req("zone", corev1.NodeSelectorOpGt, "1"))), false},
		{"expressions are ANDed", withTerms(term(req("zone", corev1.NodeSelectorOpIn, "a"), req("gen", corev1.NodeSelectorOpIn, "8"))), false},
		{"terms are ORed", withTerms(term(req("zone", corev1.NodeSelectorOpIn, "b")), term(req("gen", corev1.NodeSelectorOpIn, "9"))), true},
		{"an empty term matches nothing", withTerms(corev1.NodeSelectorTerm{}), false},
		{"matchFields on the name", withTerms(corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{req("metadata.name", corev1.NodeSelectorOpIn, "n2")}}), false},
		{"an unknown operator widens", withTerms(term(req("zone", "Near", "a"))), true},
	}
	for _, c := range cases {
		if got := Admits(c.pod, node); got != c.want {
			t.Errorf("%s: Admits = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestArchPin(t *testing.T) {
	for _, cc := range []string{"9.0", "10.3", "8"} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{NodeSelector: ArchPin(cc)}}
		if got := PinnedComputeCapability(pod); got != cc {
			t.Errorf("pin of %q reads back %q", cc, got)
		}
	}
	if PinnedComputeCapability(&corev1.Pod{}) != "" || PinnedComputeCapability(nil) != "" {
		t.Error("a pod without the pin has none")
	}
}
