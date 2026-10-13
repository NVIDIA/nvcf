// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func etcdPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mini-service-etcd-abc", Namespace: "fn"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "etcd", Image: "quay.io/coreos/etcd:v3.5.17", Command: []string{"etcd", "--data-dir=/var/lib/etcd"},
		}}},
	}
}

func singleGPUPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-abc", Namespace: "fn"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "main", Image: "vllm", Command: []string{"vllm"}, Args: []string{"serve", "m"},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}},
		}}},
	}
}

func TestCRIUGroupRestore_Companion(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"w__1", "etcd__1"}, Nodes: []string{"n0", "n1"}, GPUShare: true,
		Companion: []bool{false, true}, Slot: 1}
	m, asked := groupMutator(t, g, "")
	pod := etcdPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	a := got.Annotations
	if a[CRIURestoreAnnotation] != "etcd__1" || a[CRIUGroupOrdinalAnnotation] != "1" || a[CRIUGroupSizeAnnotation] != "2" ||
		a[CRIUGroupCompanionAnnotation] != "true" || a[CRIUGroupAnnotation] != *asked {
		t.Fatalf("companion annotations: %v", a)
	}
	if c := got.Spec.Containers[0]; !reflect.DeepEqual(c.Command, []string{IdleBin}) {
		t.Errorf("companion not idled: %v", c.Command)
	}
	for _, v := range got.Spec.Volumes {
		if v.Name == gpushareStoreVolume {
			t.Errorf("a companion has no GPU state, but got the gpushare store %v", v)
		}
	}

	m.CRIUCompanionAdmits = func(context.Context, *corev1.Pod) (bool, string) { return false, "GPU pod runs" }
	if p := m.criuGroupRestorePatches(context.Background(), etcdPod()); p != nil {
		t.Errorf("a companion of a running instance must start, got %d patches", len(p))
	}
}

func TestCRIUGroupRestore_RankOfInstanceWithCompanions(t *testing.T) {
	// The GPU rank finds the record through its own configuration: no slot.
	g := CRIUGroup{Checkpoints: []string{"w__1", "etcd__1"}, Companion: []bool{false, true}, Slot: -1}
	m, _ := groupMutator(t, g, "")
	pod := singleGPUPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	a := applyPatches(t, pod, patches).Annotations
	if a[CRIURestoreAnnotation] != "w__1" || a[CRIUGroupOrdinalAnnotation] != "0" || a[CRIUGroupSizeAnnotation] != "2" || a[CRIUGroupCompanionAnnotation] != "" {
		t.Fatalf("rank annotations: %v", a)
	}

	// A pod without GPUs that the record does not name as a companion is
	// left alone.
	if p := m.criuGroupRestorePatches(context.Background(), etcdPod()); p != nil {
		t.Errorf("a pod without GPUs restored as a rank: %d patches", len(p))
	}
}

func TestCRIUGroupRanks(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"a", "b", "c"}, Companion: []bool{false, false, true}}
	if g.Ranks() != 2 {
		t.Errorf("ranks = %d, want 2", g.Ranks())
	}
	if (CRIUGroup{Checkpoints: []string{"a", "b"}}).Ranks() != 2 {
		t.Error("a record without companions: every member is a rank")
	}
}
