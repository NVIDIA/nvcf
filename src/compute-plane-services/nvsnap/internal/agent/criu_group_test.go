// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func stsRank(name, ordinal, size string, owner types.UID, ready bool) corev1.Pod {
	isController := true
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fn", UID: types.UID(name),
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "kimi", UID: owner, Controller: &isController}},
			Annotations: map[string]string{cacheURIAnnotation: "cache://k1",
				modelvolume.CacheOrdinalAnnotation: ordinal, modelvolume.CacheGroupSizeAnnotation: size}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "utils"},
			{Name: "engine", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}}},
		}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}}},
	}
}

func TestParseCRIUGroup(t *testing.T) {
	data := map[string]string{"state": "complete", "size": "2", "gpushare": "true",
		"checkpoint.0": "a", "checkpoint.1": "b", "node.0": "n0", "node.1": "n1"}
	g, ok := parseCRIUGroup("k1", data)
	if !ok || !reflect.DeepEqual(g, webhook.CRIUGroup{Key: "k1", Checkpoints: []string{"a", "b"}, Nodes: []string{"n0", "n1"}, GPUShare: true}) {
		t.Fatalf("got %+v %v", g, ok)
	}
	for name, mut := range map[string]func(map[string]string){
		"capturing":       func(d map[string]string) { d["state"] = "capturing" },
		"failed":          func(d map[string]string) { d["state"] = "failed" },
		"missing a rank":  func(d map[string]string) { delete(d, "checkpoint.1") },
		"unreadable size": func(d map[string]string) { d["size"] = "x" },
	} {
		d := map[string]string{}
		for k, v := range data {
			d[k] = v
		}
		mut(d)
		if _, ok := parseCRIUGroup("k1", d); ok {
			t.Errorf("%s: record accepted", name)
		}
	}
}

func TestInstanceRanks(t *testing.T) {
	keep := func(p *corev1.Pod) bool { return true }
	ord, size := modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation
	r1 := stsRank("kimi-1", "1", "2", "sts-a", true)
	r0 := stsRank("kimi-0", "0", "2", "sts-a", true)
	other := stsRank("other-1", "1", "2", "sts-b", true) // another instance of the same configuration
	ranks, err := instanceRanks([]corev1.Pod{other, r1, r0}, &r0, 2, ord, size, keep)
	if err != nil || ranks[0].Name != "kimi-0" || ranks[1].Name != "kimi-1" {
		t.Fatalf("ranks %v err %v", ranks, err)
	}
	if _, err := instanceRanks([]corev1.Pod{other, r0}, &r0, 2, ord, size, keep); err == nil {
		t.Error("another instance's rank completed this one")
	}
	// LeaderWorkerSet: leader and workers have different owners, one group key.
	l, w := stsRank("lws-0", "0", "2", "leader-sts", true), stsRank("lws-0-1", "1", "2", "worker-sts", true)
	l.Labels = map[string]string{lwsGroupKeyLabel: "g1"}
	w.Labels = map[string]string{lwsGroupKeyLabel: "g1"}
	if _, err := instanceRanks([]corev1.Pod{l, w}, &l, 2, ord, size, keep); err != nil {
		t.Errorf("LeaderWorkerSet group: %v", err)
	}
}

func TestCRIUGroupCaptureLeader(t *testing.T) {
	r0 := stsRank("kimi-0", "0", "2", "sts-a", true)
	if key, size, ok := criuGroupCaptureLeader(&r0); !ok || key != "k1" || size != 2 {
		t.Errorf("rank 0: %q %d %v", key, size, ok)
	}
	a := &Agent{}
	if a.captureOptedIn(&r0) {
		t.Error("a multi-pod instance is opted in without an annotation or a match")
	}
	r0.Labels = map[string]string{"function-id": "fn-a"}
	a.config.CaptureOptIn = []CaptureOptIn{{Label: "function-id", Values: []string{"fn-a"}}}
	if !a.captureOptedIn(&r0) {
		t.Error("the function-id opt-in did not match")
	}
	annotated := stsRank("kimi-0", "0", "2", "sts-a", true)
	annotated.Annotations[criuCaptureAnnotation] = "true"
	if !(&Agent{}).captureOptedIn(&annotated) {
		t.Error("the annotation no longer opts in")
	}
	r1 := stsRank("kimi-1", "1", "2", "sts-a", true)
	notReady := stsRank("kimi-0", "0", "2", "sts-a", false)
	restored := stsRank("kimi-0", "0", "2", "sts-a", true)
	restored.Annotations[webhook.CRIURestoreAnnotation] = "a"
	bare := stsRank("src", "0", "1", "", true)
	bare.OwnerReferences = nil
	for name, p := range map[string]corev1.Pod{"rank 1": r1, "not ready": notReady, "restored": restored, "a bare pod": bare} {
		if _, _, ok := criuGroupCaptureLeader(&p); ok {
			t.Errorf("%s drives a capture", name)
		}
	}
}

func TestGroupPlaceholders(t *testing.T) {
	p := stsRank("kimi-0", "0", "2", "sts-a", false)
	p.Annotations[webhook.CRIUGroupAnnotation] = "k1"
	p.Annotations[webhook.CRIUGroupOrdinalAnnotation] = "0"
	p.Annotations[webhook.CRIUGroupSizeAnnotation] = "2"
	p.Annotations[webhook.CRIURestoreAnnotation] = "a"
	p.Annotations[webhook.CRIURestoreContainerAnnotation] = "engine"
	if !isGroupPlaceholder(&p) {
		t.Fatal("not a group placeholder")
	}
	if _, _, ok := criuGroupRestoreLeader(&p); ok {
		t.Error("drives the restore before its container runs")
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "engine", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if key, size, ok := criuGroupRestoreLeader(&p); !ok || key != "k1" || size != 2 {
		t.Errorf("leader: %q %d %v", key, size, ok)
	}
	p.Annotations[webhook.CRIUGroupSizeAnnotation] = "1"
	if isGroupPlaceholder(&p) {
		t.Error("a group of one restores on its own")
	}
}

func TestSetCaptureIdentity(t *testing.T) {
	base := CatalogInfo{Hash: "abc"}
	a, b, a2, again := base, base, base, base
	a.setCaptureIdentity(0, 2, "s1")
	b.setCaptureIdentity(1, 2, "s1")
	a2.setCaptureIdentity(0, 2, "s1")
	again.setCaptureIdentity(0, 2, "s2")
	if a.Hash == b.Hash || a.Hash != a2.Hash || a.Hash == base.Hash || len(a.ShortHash) != 32 {
		t.Errorf("rank hashes: %s %s %s", a.Hash, b.Hash, a2.Hash)
	}
	if again.Hash == a.Hash {
		t.Error("a re-capture of the same rank resolves to the first capture")
	}
}

// A placeholder restored before (by an agent that has since restarted) is
// never a restore target again.
func TestRestoredPlaceholderIsNoTarget(t *testing.T) {
	p := stsRank("kimi-0", "0", "1", "sts-a", false)
	p.Annotations[webhook.CRIURestoreAnnotation] = "a"
	p.Annotations[webhook.CRIURestoreContainerAnnotation] = "engine"
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "engine", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if _, _, ok := criuRestoreTarget(&p); !ok {
		t.Fatal("an unrestored placeholder is not a target")
	}
	p.Annotations[webhook.CRIURestoredAnnotation] = "2026-10-09T21:00:00Z"
	if _, _, ok := criuRestoreTarget(&p); ok {
		t.Error("a restored pod would be restored into again after an agent restart")
	}
}
