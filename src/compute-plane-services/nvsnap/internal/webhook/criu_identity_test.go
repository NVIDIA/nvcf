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

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelid"
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

// Under CRIU capture a model reader gets its read-only landing and the
// instance identity, and none of the cachedir machinery: the caches stay in
// the writable layer the capture carries, HOME stays, no compile-cache set
// is collected or seeded (review: the cachedir ran on the CRIU path for no
// reason the restore needs).
func TestCRIUModelReaderHasNoCachedir(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, "", election.RoleFollower, kc)
	m.CRIUCapture = true
	m.Kube = kc
	completeModelPV(t, kc, scoped("sr-fn", ngcFunctionKey))
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	landing := false
	for _, v := range got.Spec.Volumes {
		switch {
		case v.Name == "ngc-models" && v.PersistentVolumeClaim != nil:
			landing = true
		case v.Name == cacheDirVolumeName || v.Name == cacheSeedVolumeName:
			t.Errorf("CRIU reader got the cache volume %s", v.Name)
		}
	}
	if !landing {
		t.Errorf("CRIU reader lacks its model landing: %v", got.Spec.Volumes)
	}
	for _, e := range got.Spec.Containers[0].Env {
		switch e.Name {
		case "HOME", "TORCHINDUCTOR_CACHE_DIR", "TRITON_CACHE_DIR", "VLLM_CACHE_ROOT", "CUDA_CACHE_PATH":
			t.Errorf("CRIU reader's %s moved to %q", e.Name, e.Value)
		}
	}
	for _, c := range got.Spec.InitContainers {
		if c.Name == cacheSeedInitName {
			t.Error("CRIU reader seeded from a compile-cache set")
		}
	}
	for _, l := range []string{CacheCaptureLabel, modelvolume.CacheKeyLabel} {
		if _, ok := got.Labels[l]; ok {
			t.Errorf("CRIU reader labelled %s", l)
		}
	}
	if got.Annotations[CacheURIAnnotation] == "" || got.Annotations[modelvolume.CacheGroupSizeAnnotation] == "" {
		t.Errorf("CRIU reader lacks the instance identity: %v", got.Annotations)
	}
}

// The model landing at the Hugging Face default path still gets HF_HOME
// pinned and its module sources kept off the read-only view, in the
// writable layer.
func TestCRIUModelEnvKeepsModulesOffTheLanding(t *testing.T) {
	m := &Mutator{CRIUCapture: true}
	main := &corev1.Container{Name: "engine"}
	land := modelid.Landing{Path: modelid.DefaultHFHome}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{*main}}}
	got := applyPatches(t, pod, m.criuModelEnvPatches(main, land))
	env := map[string]string{}
	for _, e := range got.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["HF_HOME"] != modelid.DefaultHFHome || env["HF_MODULES_CACHE"] != criuHFModulesCache {
		t.Errorf("env %v", env)
	}
}

// Under CRIU there is no election and no label-driven cachedir capture.
func TestCRIUHasNoCachedirCapture(t *testing.T) {
	m := &Mutator{CRIUCapture: true, CacheDir: "/opt/nvsnap"}
	pod := gpuRank("engine-0", metav1.OwnerReference{Kind: "StatefulSet", Name: "engine"})
	pod.Labels = map[string]string{CaptureLabel: "true"}
	if p := m.cacheDirCapturePatches(pod); p != nil {
		t.Errorf("cachedir capture under CRIU: %v", p)
	}
	if p, err := m.electionPatches(context.Background(), pod); p != nil || err != nil {
		t.Errorf("election under CRIU: %v %v", p, err)
	}
}
