// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/capturehold"
)

func holdGates(p *corev1.Pod) int {
	n := 0
	for _, g := range p.Spec.ReadinessGates {
		if g.ConditionType == capturehold.Condition {
			n++
		}
	}
	return n
}

// A pod admitted under the gpushare library, which a capture may freeze,
// waits on the capture hold; with the hold off, or not under gpushare, or
// with no CRIU capture (CaptureCandidate nil), it does not.
func TestMutate_CaptureHold(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name      string
		hold      bool
		candidate func(context.Context, *corev1.Pod) bool
		gpushare  bool
		gates     []corev1.PodReadinessGate
		want      int
	}{
		{"held", true, candidate(true), true, nil, 1},
		{"held beside the pod's own gate", true, candidate(true), true, []corev1.PodReadinessGate{{ConditionType: "example.com/lb"}}, 1},
		{"hold off", false, candidate(true), true, nil, 0},
		{"not CRIU", true, nil, true, nil, 0},
		{"not under gpushare", true, candidate(true), false, nil, 0},
	} {
		m := &Mutator{Backend: newBackend(t), CaptureHold: c.hold, CaptureCandidate: c.candidate}
		pod := gpushareTestPod(c.gpushare, nil)
		pod.Spec.ReadinessGates = c.gates
		ops, err := m.Mutate(ctx, pod)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := applyPatches(t, pod, ops)
		if n := holdGates(got); n != c.want {
			t.Errorf("%s: %d capture-hold gates, want %d", c.name, n, c.want)
		}
		if len(got.Spec.ReadinessGates) != len(c.gates)+c.want {
			t.Errorf("%s: readiness gates %v: the pod's own were not kept", c.name, got.Spec.ReadinessGates)
		}
	}
	// Admitted twice (a reinvocation), the gate is not added again.
	m := &Mutator{Backend: newBackend(t), CaptureHold: true, CaptureCandidate: candidate(true)}
	pod := gpushareTestPod(true, nil)
	pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: capturehold.Condition}}
	ops, err := m.Mutate(ctx, pod)
	if err != nil {
		t.Fatal(err)
	}
	if n := holdGates(applyPatches(t, pod, ops)); n != 1 {
		t.Errorf("reinvoked: %d capture-hold gates, want 1", n)
	}
}

// A restore placeholder serves once its restore is done
// (CRIURestoredCondition); it is never held for a capture.
func TestMutate_CaptureHoldSkipsRestorePlaceholders(t *testing.T) {
	man := criuCaptureRecord()
	man.SourcePodMeta["gpushare"] = "true"
	m := criuMutator(t, man)
	m.GPUShareByDefault = true
	m.CaptureHold = true
	m.CaptureCandidate = candidate(true)
	pod := podWithAnnotation("abc123")
	pod.Spec = criuFunctionPod().Spec
	pod.Spec.Containers[1].Resources.Limits = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
	ops, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, ops)
	if !HasRestoredGate(got) {
		t.Fatal("not admitted as the restore placeholder")
	}
	if n := holdGates(got); n != 0 {
		t.Errorf("the restore placeholder is held for a capture")
	}
}

// A pod whose spec already places the library (the webhook adds nothing
// for it) is held all the same: the agent captures it.
func TestMutate_CaptureHoldExistingPlacement(t *testing.T) {
	m := &Mutator{Backend: newBackend(t), CaptureHold: true, CaptureCandidate: candidate(true)}
	placed := applyPatches(t, gpushareTestPod(true, nil), m.gpusharePatches(context.Background(), gpushareTestPod(true, nil)))
	if len(m.gpusharePatches(context.Background(), placed)) != 0 {
		t.Fatal("the fixture's placement is not complete")
	}
	if n := holdGates(applyPatches(t, placed, m.captureHoldPatches(context.Background(), placed, false))); n != 1 {
		t.Errorf("a pod already under gpushare is not held: %d gates", n)
	}
}

// A pod CaptureHoldAdmits refuses (a StatefulSet that creates its pods in
// order) is not held.
func TestMutate_CaptureHoldRefused(t *testing.T) {
	m := &Mutator{Backend: newBackend(t), CaptureHold: true, CaptureCandidate: candidate(true),
		CaptureHoldAdmits: func(context.Context, *corev1.Pod) (bool, string) { return false, "ordered" }}
	pod := gpushareTestPod(true, nil)
	ops, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if n := holdGates(applyPatches(t, pod, ops)); n != 0 {
		t.Errorf("a refused pod is held")
	}
}
