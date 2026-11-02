// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNamesOwnInstance(t *testing.T) {
	gpu := corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}
	pod := func(ns string, gpuEnv, cpuEnv []corev1.EnvVar) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns}, Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "frontend", Env: cpuEnv},
			{Name: "main", Resources: gpu, Env: gpuEnv},
		}}}
	}
	ns := "sr-90586a25-a597-4f6f-99a1-e6b2101ce771"
	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"instance in a literal", pod(ns, []corev1.EnvVar{{Name: "A", Value: "x"}, {Name: "DYN_NAMESPACE", Value: ns + "-graph"}}, nil), "DYN_NAMESPACE"},
		{"service name with the namespace", pod(ns, []corev1.EnvVar{{Name: "SVC", Value: "graph-0." + ns}}, nil), "SVC"},
		{"downward API reference", pod(ns, []corev1.EnvVar{{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}}}, nil), ""},
		{"no instance in the configuration", pod(ns, []corev1.EnvVar{{Name: "MODEL", Value: "llama"}}, nil), ""},
		{"only a container without GPUs names it", pod(ns, nil, []corev1.EnvVar{{Name: "DYN_NAMESPACE", Value: ns}}), ""},
		{"namespace inside a longer word", pod("vllm", []corev1.EnvVar{{Name: "IMAGE", Value: "myvllm2"}}, nil), ""},
		{"a name derived from a short namespace counts", pod("vllm", []corev1.EnvVar{{Name: "IMAGE", Value: "vllm-openai"}}, nil), "IMAGE"},
		{"namespace as a whole word", pod("vllm", []corev1.EnvVar{{Name: "SVC", Value: "engine.vllm.svc"}}, nil), "SVC"},
		{"no namespace", pod("", []corev1.EnvVar{{Name: "A", Value: "x"}}, nil), ""},
	} {
		if got := NamesOwnInstance(tc.pod); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
