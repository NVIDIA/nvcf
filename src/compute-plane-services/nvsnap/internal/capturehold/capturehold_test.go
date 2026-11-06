// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package capturehold

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pod(gates []corev1.PodConditionType, conds map[corev1.PodConditionType]corev1.ConditionStatus) *corev1.Pod {
	p := &corev1.Pod{}
	for _, g := range gates {
		p.Spec.ReadinessGates = append(p.Spec.ReadinessGates, corev1.PodReadinessGate{ConditionType: g})
	}
	for t, s := range conds {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: t, Status: s, LastTransitionTime: metav1.NewTime(time.Unix(100, 0))})
	}
	return p
}

const (
	yes = corev1.ConditionTrue
	no  = corev1.ConditionFalse
)

func TestReady(t *testing.T) {
	other := corev1.PodConditionType("nvsnap.io/restored")
	cases := []struct {
		name  string
		pod   *corev1.Pod
		ready bool
		held  bool
	}{
		{"ungated ready", pod(nil, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: yes, corev1.ContainersReady: yes}), true, false},
		{"ungated containers ready only", pod(nil, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: no, corev1.ContainersReady: yes}), false, false},
		{"held and warm", pod([]corev1.PodConditionType{Condition}, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: no, corev1.ContainersReady: yes}), true, true},
		{"held, not warm", pod([]corev1.PodConditionType{Condition}, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: no, corev1.ContainersReady: no}), false, true},
		{"held, another gate false", pod([]corev1.PodConditionType{Condition, other}, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: no, corev1.ContainersReady: yes, other: no}), false, true},
		{"held, another gate missing", pod([]corev1.PodConditionType{Condition, other}, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: no, corev1.ContainersReady: yes}), false, true},
		{"held, another gate true", pod([]corev1.PodConditionType{Condition, other}, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: no, corev1.ContainersReady: yes, other: yes}), true, true},
		{"settled, containers not ready", pod([]corev1.PodConditionType{Condition}, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: no, corev1.ContainersReady: no, Condition: yes}), false, false},
		{"settled and ready", pod([]corev1.PodConditionType{Condition}, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.PodReady: yes, corev1.ContainersReady: yes, Condition: yes}), true, false},
		{"nil", nil, false, false},
	}
	for _, c := range cases {
		if got := Ready(c.pod); got != c.ready {
			t.Errorf("%s: Ready = %v, want %v", c.name, got, c.ready)
		}
		if got := Held(c.pod); got != c.held {
			t.Errorf("%s: Held = %v, want %v", c.name, got, c.held)
		}
	}
}

func TestWarmSince(t *testing.T) {
	p := pod(nil, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.ContainersReady: yes})
	if at, ok := WarmSince(p); !ok || !at.Equal(time.Unix(100, 0)) {
		t.Errorf("WarmSince = %v %v", at, ok)
	}
	if _, ok := WarmSince(pod(nil, map[corev1.PodConditionType]corev1.ConditionStatus{corev1.ContainersReady: no})); ok {
		t.Error("WarmSince of a pod whose containers are not ready")
	}
}
