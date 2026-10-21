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
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

// lwsRank is rank i of a two-pod LeaderWorkerSet instance.
func lwsRank(i string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "kimi-0-" + i, Namespace: "fn",
			Labels:      map[string]string{lwsWorkerIndexLabel: i},
			Annotations: map[string]string{lwsSizeAnnotation: "2"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "engine", Image: "vllm", Command: []string{"vllm"}, Args: []string{"serve", "m"},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}},
		}}},
	}
}

func groupMutator(t *testing.T, g CRIUGroup, blocked string) (*Mutator, *string) {
	var asked string
	m := &Mutator{Backend: newBackend(t), Composer: &rootfsonly.HashInputComposer{CUDADriverMajor: 610},
		CheckpointHostRoot: "/var/lib/containerd/nvsnap-checkpoints",
		CRIUGroups: func(_ context.Context, ns, uri string) (CRIUGroup, bool) {
			asked = "key-of/" + ns + "/" + uri
			g.Key = asked
			return g, uri != ""
		},
		CRIURestoreBlocked: func(_ context.Context, id string) bool { return id == blocked },
	}
	return m, &asked
}

func TestCRIUGroupRestore_EachRankRestoresItsCheckpoint(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"r0__1", "r1__1"}, Nodes: []string{"n0", "n1"}, GPUShare: true}
	m, asked := groupMutator(t, g, "")
	for i, want := range g.Checkpoints {
		pod := lwsRank([]string{"0", "1"}[i])
		patches, err := m.Mutate(context.Background(), pod)
		if err != nil {
			t.Fatal(err)
		}
		got := applyPatches(t, pod, patches)
		a := got.Annotations
		if a[CRIURestoreAnnotation] != want || a[CRIUGroupOrdinalAnnotation] != []string{"0", "1"}[i] || a[CRIUGroupSizeAnnotation] != "2" || a[CRIUGroupAnnotation] != *asked {
			t.Fatalf("rank %d annotations: %v", i, a)
		}
		if c := got.Spec.Containers[0]; !reflect.DeepEqual(c.Command, []string{IdleBin}) || len(c.Args) != 0 {
			t.Errorf("rank %d engine not idled: %v %v", i, c.Command, c.Args)
		}
		var store string
		for _, v := range got.Spec.Volumes {
			if v.Name == gpushareStoreVolume {
				store = v.HostPath.Path
			}
		}
		if store != "/var/lib/containerd/nvsnap-checkpoints/"+want+"/gpushare" {
			t.Errorf("rank %d chunk store = %q, want the checkpoint's", i, store)
		}
	}
}

func TestCRIUGroupRestore_ColdStarts(t *testing.T) {
	two := CRIUGroup{Checkpoints: []string{"r0__1", "r1__1"}}
	three := CRIUGroup{Checkpoints: []string{"r0__1", "r1__1", "r2__1"}}
	for name, tc := range map[string]struct {
		g       CRIUGroup
		blocked string
	}{
		"different instance size": {three, ""},
		"a blocked checkpoint":    {two, "r1__1"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := groupMutator(t, tc.g, tc.blocked)
			if p := m.criuGroupRestorePatches(context.Background(), lwsRank("0")); p != nil {
				t.Errorf("want a cold start, got %d patches", len(p))
			}
		})
	}
}

func TestGroveRankAndSize(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "k3-0-vllmworker-7-vllmworker-wkr-x6xtx",
			Labels: map[string]string{GroveScalingGroupLabel: "k3-0-vllmworker", GroveScalingGroupReplicaLabel: "7", GroveScalingGroupPodLabel: "1"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Env: []corev1.EnvVar{{Name: "GROVE_PCSG_TEMPLATE_NUM_PODS", Value: "2"}}}}},
	}
	m := &Mutator{}
	if o, s := podOrdinal(pod), m.groupSize(context.Background(), pod); o != 1 || s != 2 {
		t.Errorf("ordinal %d size %d, want 1 and 2", o, s)
	}
}

// A pod whose instance cannot restore as a group (one rank replaced while
// the others run, or ranks created in order) is not a placeholder: it
// starts cold, with the reason logged.
func TestCRIUGroupRestore_RefusedInstanceStartsCold(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"r0__1", "r1__1"}, Nodes: []string{"n0", "n1"}}
	m, _ := groupMutator(t, g, "")
	var asked string
	m.CRIUGroupAdmits = func(_ context.Context, pod *corev1.Pod) (bool, string) {
		asked = pod.Name
		return false, "pod kimi-0-0 of the instance already runs"
	}
	pod := lwsRank("1")
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if asked != pod.Name {
		t.Fatalf("admission check not consulted for %s", pod.Name)
	}
	if got := applyPatches(t, pod, patches); got.Annotations[CRIURestoreAnnotation] != "" || got.Annotations[CRIUGroupAnnotation] != "" {
		t.Errorf("refused pod must start cold, got annotations %v", got.Annotations)
	}
}

// A single-pod instance restores alone: the group admission check, which
// is about ranks arriving together, is not consulted, so an ordered
// StatefulSet of one pod still restores.
func TestCRIUGroupRestore_SinglePodSkipsGroupAdmission(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"r0__1"}, Nodes: []string{"n0"}}
	m, _ := groupMutator(t, g, "")
	m.CRIUGroupAdmits = func(context.Context, *corev1.Pod) (bool, string) {
		t.Error("group admission consulted for a single pod")
		return false, "ordered"
	}
	pod := lwsRank("0")
	pod.Annotations[lwsSizeAnnotation] = "1"
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if got := applyPatches(t, pod, patches); got.Annotations[CRIURestoreAnnotation] != "r0__1" {
		t.Errorf("single pod must restore, got annotations %v", got.Annotations)
	}
}

// A rank's restore placeholder gets the model landing its captured pod had
// (the restored engine's mounts are bound from it) and the configuration
// stamp the agent's trust check reads, but nothing a fresh engine needs to
// start: no download, no prewarm, no cache seed, no capture labels
// (review: it re-ran the whole cold-start pipeline before every restore).
func TestCRIUGroupRestore_PlaceholderGetsOnlyTheLanding(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, "", election.RoleFollower, kc)
	m.CRIUCapture = true
	m.CheckpointHostRoot = "/var/lib/containerd/nvsnap-checkpoints"
	m.CRIUGroups = func(_ context.Context, ns, uri string) (CRIUGroup, bool) {
		return CRIUGroup{Key: "key-of/" + ns + "/" + uri, Checkpoints: []string{"r0__1"}, GPUShare: true}, true
	}
	completeModelPV(t, kc, ngcFunctionKey)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	if got.Annotations[CRIURestoreAnnotation] != "r0__1" {
		t.Fatalf("not admitted as the restore placeholder: %v", got.Annotations)
	}
	if want, _ := m.cacheURI(pod); got.Annotations[CacheURIAnnotation] != want {
		t.Errorf("cache-uri %q, want the pod's configuration %q", got.Annotations[CacheURIAnnotation], want)
	}
	landing := false
	for _, v := range got.Spec.Volumes {
		if v.Name == "ngc-models" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == modelvolume.ReadOnlyClaimName(ngcFunctionKey) {
			landing = true
		}
		if v.Name == cacheSeedVolumeName || v.Name == cacheDirVolumeName {
			t.Errorf("placeholder got the cache volume %s", v.Name)
		}
	}
	if !landing {
		t.Errorf("placeholder lacks the model's read-only view at the captured landing: %v", got.Spec.Volumes)
	}
	for _, c := range got.Spec.InitContainers {
		switch c.Name {
		case injectedDownloadInit, modelPrewarmName, cacheSeedInitName, "nvsnap-prewarm":
			t.Errorf("placeholder got the cold-start init %s", c.Name)
		}
	}
	for _, l := range []string{modelvolume.CaptureLabel, CacheCaptureLabel, modelvolume.CacheKeyLabel, modelvolume.PendingLabel} {
		if _, ok := got.Labels[l]; ok {
			t.Errorf("placeholder labelled %s", l)
		}
	}
	if jobs, _ := kc.BatchV1().Jobs("sr-fn").List(context.Background(), metav1.ListOptions{}); len(jobs.Items) != 0 {
		t.Errorf("admitting a placeholder created %d download Job(s)", len(jobs.Items))
	}
}

// The model the captured pod read is gone (retired, never completed): the
// instance starts cold rather than downloading it before a restore.
func TestCRIUGroupRestore_PlaceholderWithoutTheModelStartsCold(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, "", election.RoleFollower, kc)
	m.CRIUCapture = true
	m.CRIUGroups = func(_ context.Context, ns, uri string) (CRIUGroup, bool) {
		return CRIUGroup{Key: "k", Checkpoints: []string{"r0__1"}, GPUShare: true}, true
	}
	pod := ngcFunctionPod()
	if p := m.criuGroupRestorePatches(context.Background(), pod); p != nil {
		t.Errorf("admitted as a placeholder without its model landing: %v", p)
	}
	if jobs, _ := kc.BatchV1().Jobs("sr-fn").List(context.Background(), metav1.ListOptions{}); len(jobs.Items) != 0 {
		t.Errorf("created %d download Job(s) for a restore", len(jobs.Items))
	}
}
