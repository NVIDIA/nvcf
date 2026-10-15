// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// engine is a pod with a GPU engine container and a sidecar.
func engine(name string, gpus string, command []string, args []string, env ...corev1.EnvVar) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "main", Command: command, Args: args, Env: env,
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse(gpus)}}},
			{Name: "health", Command: []string{"python3", "health.py"}},
		}},
	}
}

func TestInstanceCoversEngine(t *testing.T) {
	// A Dynamo multi-node vLLM worker: TP=8 across two 4-GPU pods, the
	// engine started from a launch script with its command line as args.
	dynamo := func(name string) *corev1.Pod {
		return engine(name, "4", []string{"/bin/bash", "/opt/launch.sh"},
			[]string{"python3 -m dynamo.vllm --model /config/models/m --tensor-parallel-size 8 --pipeline-parallel-size 1"})
	}
	for name, tc := range map[string]struct {
		ranks []*corev1.Pod
		ok    bool
	}{
		"multi-node engine, both pods":    {[]*corev1.Pod{dynamo("ldr"), dynamo("wkr")}, true},
		"multi-node engine, leader only":  {[]*corev1.Pod{dynamo("ldr")}, false},
		"single-pod TP=4":                 {[]*corev1.Pod{engine("p", "4", []string{"vllm"}, []string{"serve", "m", "--tensor-parallel-size=4"})}, true},
		"no parallel flags":               {[]*corev1.Pod{engine("p", "1", []string{"vllm"}, []string{"serve", "m"})}, true},
		"sglang tp-size over the pod":     {[]*corev1.Pod{engine("p", "4", []string{"python3"}, []string{"-m", "sglang.launch_server", "--tp-size", "8"})}, false},
		"NIM env over the pod":            {[]*corev1.Pod{engine("p", "2", nil, nil, corev1.EnvVar{Name: "NIM_TENSOR_PARALLEL_SIZE", Value: "4"})}, false},
		"headless worker as one pod":      {[]*corev1.Pod{engine("p", "4", []string{"vllm"}, []string{"serve", "m", "--headless"})}, false},
		"sglang multi-node as one pod":    {[]*corev1.Pod{engine("p", "8", []string{"python3"}, []string{"-m", "sglang.launch_server", "--tp", "8", "--nnodes", "2"})}, false},
		"tensor x pipeline over two pods": {[]*corev1.Pod{engine("a", "4", nil, []string{"--tp 4 --pp 2"}), engine("b", "4", nil, []string{"--tp 4 --pp 2"})}, true},
	} {
		err := instanceCoversEngine(tc.ranks)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

func TestGroveInstance(t *testing.T) {
	pod := func(name, group, replica, index, controller string) corev1.Pod {
		p := stsRank(name, index, "2", types.UID("uid-"+controller), true)
		p.Labels = map[string]string{webhook.GroveScalingGroupLabel: group, webhook.GroveScalingGroupReplicaLabel: replica,
			webhook.GroveScalingGroupPodLabel: index}
		return p
	}
	// Leader and worker have different controllers (one PodClique each).
	ldr0, wkr0 := pod("w-0-ldr-a", "w", "0", "0", "ldr0"), pod("w-0-wkr-b", "w", "0", "1", "wkr0")
	wkr1 := pod("w-1-wkr-c", "w", "1", "1", "wkr1")
	if !hasInstance(&ldr0) || !sameInstance(&ldr0, &wkr0) || sameInstance(&ldr0, &wkr1) {
		t.Fatal("Grove scaling group replica not recognised as the instance")
	}
	ord, size := "nvsnap.io/cache-ordinal", "nvsnap.io/cache-group-size"
	ranks, err := instanceRanks([]corev1.Pod{wkr1, wkr0, ldr0}, &ldr0, 2, ord, size, func(*corev1.Pod) bool { return true })
	if err != nil || ranks[0].Name != "w-0-ldr-a" || ranks[1].Name != "w-0-wkr-b" {
		t.Fatalf("ranks %v err %v", ranks, err)
	}
}
