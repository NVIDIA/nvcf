// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestLivenessBudgetPatches(t *testing.T) {
	probe := func(period, threshold int32) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/live"}},
			PeriodSeconds: period, FailureThreshold: threshold}
	}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "tight", LivenessProbe: probe(5, 1)},  // the Dynamo worker's: 5 s of silence
		{Name: "loose", LivenessProbe: probe(10, 9)}, // already 90 s
		{Name: "defaults", LivenessProbe: probe(0, 0)},
		{Name: "none"},
	}}}
	m := &Mutator{CaptureLivenessBudget: 60}
	got := applyPatches(t, pod, m.livenessBudgetPatches(pod))
	want := map[string]int32{"tight": 12, "loose": 9, "defaults": 6}
	for _, c := range got.Spec.Containers {
		if c.LivenessProbe == nil {
			continue
		}
		if c.LivenessProbe.FailureThreshold != want[c.Name] {
			t.Errorf("%s: failureThreshold %d, want %d", c.Name, c.LivenessProbe.FailureThreshold, want[c.Name])
		}
	}
	if p := (&Mutator{}).livenessBudgetPatches(pod); p != nil {
		t.Errorf("budget off: patched %v", p)
	}
}

func TestLivenessTolerance(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "a", LivenessProbe: &corev1.Probe{PeriodSeconds: 10, FailureThreshold: 30}},
		{Name: "b", LivenessProbe: &corev1.Probe{}}, // API defaults: 10 s x 3
		{Name: "c"},
	}}}
	if got, ok := LivenessTolerance(pod); !ok || got != 30 {
		t.Errorf("got %d %v, want the tightest probe's 30 s", got, ok)
	}
	if _, ok := LivenessTolerance(&corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}}); ok {
		t.Error("a pod without a liveness probe has no tolerance")
	}
}

func TestDynamoLeaseTTLOf(t *testing.T) {
	c := func(env ...corev1.EnvVar) corev1.Container { return corev1.Container{Name: "x", Env: env} }
	dyn := corev1.EnvVar{Name: "DYN_NAMESPACE", Value: "ns"}
	for _, tc := range []struct {
		name string
		cs   []corev1.Container
		want int
		ok   bool
	}{
		{"not Dynamo", []corev1.Container{c()}, 0, false},
		{"default", []corev1.Container{c(dyn)}, 10, true},
		{"set", []corev1.Container{c(dyn, corev1.EnvVar{Name: "ETCD_LEASE_TTL", Value: "300"})}, 300, true},
		{"unreadable", []corev1.Container{c(dyn, corev1.EnvVar{Name: "ETCD_LEASE_TTL", Value: "x"})}, 10, true},
		{"shortest", []corev1.Container{c(dyn, corev1.EnvVar{Name: "ETCD_LEASE_TTL", Value: "300"}), c(dyn, corev1.EnvVar{Name: "ETCD_LEASE_TTL", Value: "60"})}, 60, true},
	} {
		got, ok := DynamoLeaseTTL(&corev1.Pod{Spec: corev1.PodSpec{Containers: tc.cs}})
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %d %v, want %d %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
