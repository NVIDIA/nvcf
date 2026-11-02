// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func candidate(yes bool) func(context.Context, *corev1.Pod) bool {
	return func(context.Context, *corev1.Pod) bool { return yes }
}

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
	m := &Mutator{CaptureLivenessBudget: 60, CaptureCandidate: candidate(true)}
	got := applyPatches(t, pod, m.livenessBudgetPatches(context.Background(), pod))
	want := map[string]int32{"tight": 12, "loose": 9, "defaults": 6}
	for _, c := range got.Spec.Containers {
		if c.LivenessProbe == nil {
			continue
		}
		if c.LivenessProbe.FailureThreshold != want[c.Name] {
			t.Errorf("%s: failureThreshold %d, want %d", c.Name, c.LivenessProbe.FailureThreshold, want[c.Name])
		}
	}
	if p := (&Mutator{CaptureCandidate: candidate(true)}).livenessBudgetPatches(context.Background(), pod); p != nil {
		t.Errorf("budget off: patched %v", p)
	}
}

// Only a pod a capture may freeze gets the budget: an ordinary service keeps
// the recovery its probe asks for (review: a CPU service with a 5 s probe
// was widened to five minutes cluster-wide).
func TestLivenessBudgetPatches_OnlyPodsACaptureMayFreeze(t *testing.T) {
	tight := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/live"}},
		PeriodSeconds: 5, FailureThreshold: 1}
	cpu := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "svc", LivenessProbe: tight}}}}
	gpu := cpu.DeepCopy()
	gpu.Spec.Containers[0].Resources.Limits = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
	ctx := context.Background()
	for _, c := range []struct {
		name      string
		candidate func(context.Context, *corev1.Pod) bool
		pod       *corev1.Pod
		widened   bool
	}{
		{"not CRIU: no capture freezes anything", nil, gpu, false},
		{"service that no capture covers", candidate(false), cpu, false},
		{"companion of an opted-in instance", candidate(true), cpu, true},
		{"GPU pod", candidate(false), gpu, true},
	} {
		m := &Mutator{CaptureLivenessBudget: 300, CaptureCandidate: c.candidate}
		if got := len(m.livenessBudgetPatches(ctx, c.pod)) > 0; got != c.widened {
			t.Errorf("%s: widened %v, want %v", c.name, got, c.widened)
		}
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
