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
