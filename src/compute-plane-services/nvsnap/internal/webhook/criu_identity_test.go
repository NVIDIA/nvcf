// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

func gpuRank(name string, owner metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "sr-fn", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "engine", Image: "vllm/vllm-openai:v0.11.0",
			Args:      []string{"--model", "Qwen/Qwen3-8B"},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}}}}},
	}
}

// With CRIU capture every GPU rank carries the identity the instance
// capture keys on, from the configuration hash and the group's shape
// alone: no model volume, no cache set (review: the cache set was the only
// writer, and a stock install, model volume off, never captured an instance).
func TestCRIUIdentityPatches(t *testing.T) {
	kc := fake.NewSimpleClientset()
	four := int32(4)
	if _, err := kc.AppsV1().StatefulSets("sr-fn").Create(context.Background(), &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "sr-fn"}, Spec: appsv1.StatefulSetSpec{Replicas: &four}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	m := &Mutator{Composer: &rootfsonly.HashInputComposer{CUDADriverMajor: 610}, CRIUCapture: true, Kube: kc}
	pod := gpuRank("engine-2", metav1.OwnerReference{Kind: "StatefulSet", Name: "engine"})
	got := applyPatches(t, pod, mergePatchPlan(m.criuIdentityPatches(context.Background(), pod)))
	want, _ := m.cacheURI(pod)
	for k, v := range map[string]string{CacheURIAnnotation: want, modelvolume.CacheOrdinalAnnotation: "2", modelvolume.CacheGroupSizeAnnotation: "4"} {
		if got.Annotations[k] != v {
			t.Errorf("%s = %q, want %q", k, got.Annotations[k], v)
		}
	}

	if p := (&Mutator{Composer: m.Composer, Kube: kc}).criuIdentityPatches(context.Background(), pod); p != nil {
		t.Errorf("not a CRIU cluster: stamped %v", p)
	}
	cpu := pod.DeepCopy()
	cpu.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	if p := m.criuIdentityPatches(context.Background(), cpu); p != nil {
		t.Errorf("a pod without GPUs is a companion, not a rank: stamped %v", p)
	}
}

// A StatefulSet's size comes from the agent's client whether or not the
// cache volume is on (review: without it, a multi-pod StatefulSet counted as
// one pod and its group restore was refused for a size mismatch).
func TestGroupSizeWithoutCacheVolume(t *testing.T) {
	kc := fake.NewSimpleClientset()
	two := int32(2)
	if _, err := kc.AppsV1().StatefulSets("sr-fn").Create(context.Background(), &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "sr-fn"}, Spec: appsv1.StatefulSetSpec{Replicas: &two}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := gpuRank("engine-1", metav1.OwnerReference{Kind: "StatefulSet", Name: "engine"})
	if n := (&Mutator{Kube: kc}).groupSize(context.Background(), pod); n != 2 {
		t.Errorf("group size %d, want the StatefulSet's 2", n)
	}
	if n := (&Mutator{}).groupSize(context.Background(), pod); n != 1 {
		t.Errorf("no client: group size %d, want 1", n)
	}
}
