// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"errors"
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
		Companion: []bool{false, true}, Slots: []int{1}}
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
	g := CRIUGroup{Checkpoints: []string{"w__1", "etcd__1"}, Companion: []bool{false, true}}
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

// Identical companions: the pod gets the slot the claim hands it, and
// without a claim (or with every slot taken) it starts cold.
func TestCRIUGroupRestore_IdenticalCompanions(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"w__1", "fe__1", "fe__2"}, Nodes: []string{"n0", "n1", "n2"},
		Companion: []bool{false, true, true}, Slots: []int{1, 2}}
	m, _ := groupMutator(t, g, "")
	if p := m.criuGroupRestorePatches(context.Background(), etcdPod()); p != nil {
		t.Fatalf("identical companions without slot claims must start cold, got %d patches", len(p))
	}
	var offered []int
	m.CRIUCompanionSlot = func(_ context.Context, _ *corev1.Pod, _ CRIUGroup, slots []int) (int, error) {
		offered = slots
		return 2, nil
	}
	pod := etcdPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	a := applyPatches(t, pod, patches).Annotations
	if a[CRIURestoreAnnotation] != "fe__2" || a[CRIUGroupOrdinalAnnotation] != "2" || a[CRIUGroupCompanionAnnotation] != "true" {
		t.Fatalf("companion annotations: %v", a)
	}
	if !reflect.DeepEqual(offered, []int{1, 2}) {
		t.Errorf("slots offered = %v, want [1 2]", offered)
	}
	m.CRIUCompanionSlot = func(context.Context, *corev1.Pod, CRIUGroup, []int) (int, error) {
		return 0, errors.New("every slot taken")
	}
	if p := m.criuGroupRestorePatches(context.Background(), etcdPod()); p != nil {
		t.Errorf("a companion without a slot must start cold, got %d patches", len(p))
	}
	m.CRIUCompanionSlot = func(context.Context, *corev1.Pod, CRIUGroup, []int) (int, error) {
		t.Error("a refused companion must not claim a slot")
		return 1, nil
	}
	m.CRIUCompanionAdmits = func(context.Context, *corev1.Pod) (bool, string) { return false, "GPU pod runs" }
	_ = m.criuGroupRestorePatches(context.Background(), etcdPod())
}

func TestCompanionSlots(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"a", "b", "c"}, Companion: []bool{false, true, true}}
	for _, tc := range []struct {
		slots []int
		want  []int
	}{{nil, nil}, {[]int{1}, []int{1}}, {[]int{1, 2}, []int{1, 2}}, {[]int{0}, nil}, {[]int{1, 3}, nil}, {[]int{-1}, nil}} {
		g.Slots = tc.slots
		if got := g.CompanionSlots(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("slots %v: got %v, want %v", tc.slots, got, tc.want)
		}
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

// A placeholder whose containers share one pid namespace starts with the
// pid-reserve step, ahead of every other init container, after the whole
// admission has run.
func TestCRIUGroupRestore_SharedPIDNamespaceReservesFirst(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"w__1"}, Companion: []bool{false}}
	m, _ := groupMutator(t, g, "")
	pod := singleGPUPod()
	share, always := true, corev1.ContainerRestartPolicyAlways
	pod.Spec.ShareProcessNamespace = &share
	pod.Spec.InitContainers = []corev1.Container{{Name: "wait-for-nats", Image: "curl", Command: []string{"wait"}},
		{Name: "beacon", Image: "img", Command: []string{"serve"}, RestartPolicy: &always}}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	if got.Annotations[CRIURestoreAnnotation] != "w__1" {
		t.Fatalf("not admitted as a placeholder: %v", got.Annotations)
	}
	if len(got.Spec.InitContainers) == 0 || got.Spec.InitContainers[0].Name != PIDReserveInit {
		names := make([]string, 0, len(got.Spec.InitContainers))
		for _, c := range got.Spec.InitContainers {
			names = append(names, c.Name)
		}
		t.Fatalf("init containers %v, want %s first", names, PIDReserveInit)
	}
}

// A placeholder mounts, for the restored process, the bundle version its
// capture recorded; the helper comes from the current one.
func TestCRIUGroupRestore_MountsTheRecordedBundle(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"w__1"}, Companion: []bool{false}, GPUShare: true, Bundles: []string{"0ld0"}}
	m, _ := groupMutator(t, g, "")
	m.BundleID = "cafe"
	m.HostBundleRoot = "/b"
	pod := singleGPUPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	paths := map[string]string{}
	for _, v := range got.Spec.Volumes {
		if v.HostPath != nil {
			paths[v.Name] = v.HostPath.Path
		}
	}
	if paths[gpushareLibVolume] != "/b/versions/0ld0" {
		t.Errorf("library volume %q, want the recorded version", paths[gpushareLibVolume])
	}
	if ids := PodBundleIDs(got); len(ids) == 0 || ids[0] != "0ld0" {
		t.Errorf("PodBundleIDs = %v, want the library's version first", ids)
	}
	if b := (&Mutator{HostBundleRoot: "/b"}).bundleDir(""); b != "/b/nvsnap" {
		t.Errorf("no version: %q, want the legacy tree", b)
	}
}
