/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvsnapreconciler "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap/reconciler"
)

func TestPodEligibleForCheckpointOnWarm(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"nil", nil, false},
		{"no annotations", &corev1.Pod{}, false},
		{"annotation false", podWithAnnotations(map[string]string{
			nvsnapreconciler.CheckpointOnWarmAnnotation:  "false",
			nvsnapreconciler.FunctionVersionIDAnnotation: "fv-1",
		}), false},
		{"only checkpoint-on-warm", podWithAnnotations(map[string]string{
			nvsnapreconciler.CheckpointOnWarmAnnotation: "true",
		}), false},
		{"only function-version", podWithAnnotations(map[string]string{
			nvsnapreconciler.FunctionVersionIDAnnotation: "fv-1",
		}), false},
		{"both annotations set", podWithAnnotations(map[string]string{
			nvsnapreconciler.CheckpointOnWarmAnnotation:  "true",
			nvsnapreconciler.FunctionVersionIDAnnotation: "fv-1",
		}), true},
		{"both set + extra unrelated annotations", podWithAnnotations(map[string]string{
			nvsnapreconciler.CheckpointOnWarmAnnotation:  "true",
			nvsnapreconciler.FunctionVersionIDAnnotation: "fv-1",
			"some.other/annotation":                      "x",
		}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PodEligibleForCheckpointOnWarm(tc.pod); got != tc.want {
				t.Errorf("PodEligibleForCheckpointOnWarm = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsPodReady(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"nil", nil, false},
		{"no conditions", &corev1.Pod{}, false},
		{"ready true", &corev1.Pod{Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		}}, true},
		{"ready false", &corev1.Pod{Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		}}, false},
		{"ready unknown", &corev1.Pod{Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionUnknown},
			},
		}}, false},
		{"initialized but not ready", &corev1.Pod{Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
			},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPodReady(tc.pod); got != tc.want {
				t.Errorf("IsPodReady = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewControllerDefaults(t *testing.T) {
	c := NewController(nil, nil)
	if c.Workers != 2 {
		t.Errorf("Workers default = %d, want 2", c.Workers)
	}
}

func podWithAnnotations(ann map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "p",
			Namespace:   "n",
			Annotations: ann,
		},
	}
}
