// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func groveRankPod(index string, env ...corev1.EnvVar) *corev1.Pod {
	env = append([]corev1.EnvVar{{Name: groveScalingGroupSizeEnv, Value: "2"}}, env...)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "ns", Labels: map[string]string{
			GroveScalingGroupLabel: "g", GroveScalingGroupPodLabel: index}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "main", Env: env,
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}},
		}}},
	}
}

func TestSnapshotPointStaysOnRankZero(t *testing.T) {
	m := &Mutator{}
	point := corev1.EnvVar{Name: SnapshotControlDirEnv, Value: "/snapshot-control"}

	if p := m.snapshotPointPatches(context.Background(), groveRankPod("0", point)); p != nil {
		t.Fatalf("rank 0 patched: %v", p)
	}
	if p := m.snapshotPointPatches(context.Background(), groveRankPod("1")); p != nil {
		t.Fatalf("rank 1 without a point patched: %v", p)
	}
	pod := groveRankPod("1", corev1.EnvVar{Name: "A", Value: "a"}, point, corev1.EnvVar{Name: "B", Value: "b"})
	got := applyPatches(t, pod, m.snapshotPointPatches(context.Background(), pod))
	env := got.Spec.Containers[0].Env
	if len(env) != len(pod.Spec.Containers[0].Env) {
		t.Fatalf("env length changed: %v", env)
	}
	for _, e := range env {
		if e.Name == SnapshotControlDirEnv && e.Value != "" {
			t.Errorf("rank 1 keeps %s=%q", e.Name, e.Value)
		}
		if e.Name == "B" && e.Value != "b" {
			t.Errorf("a neighbouring variable changed: %v", e)
		}
	}
}
