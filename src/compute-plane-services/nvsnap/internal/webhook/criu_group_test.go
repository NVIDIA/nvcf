// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		CRIUGroups: func(_ context.Context, key string) (CRIUGroup, bool) {
			asked = key
			return g, key != ""
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
		if got.Spec.Containers[0].Args[0] != criuPlaceholderScript {
			t.Errorf("rank %d engine not idled", i)
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
