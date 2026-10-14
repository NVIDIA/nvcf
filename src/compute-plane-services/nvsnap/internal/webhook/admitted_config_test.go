// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

// Whatever the webhook's pod-wide patches add to a pod, the identity the
// pod was admitted by stays the identity of the spec as received: a group
// capture records a companion by it and its replacement is admitted by it.
func TestAdmittedConfigSurvivesTheWebhooksOwnPatches(t *testing.T) {
	m := &Mutator{Composer: &rootfsonly.HashInputComposer{CUDADriverMajor: 610}, DynamoNamespace: "nvcf-nvsnap",
		CaptureLivenessBudget: 60}
	tight := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/live"}}, PeriodSeconds: 5, FailureThreshold: 1}
	env := []corev1.EnvVar{
		{Name: "DYN_NAMESPACE", Value: "sr-1-graph"},
		{Name: "ETCD_ENDPOINTS", Value: "mini-service-etcd:2379"},
		{Name: "NATS_SERVER", Value: "nats://mini-service-nats:4222"},
	}
	for name, pod := range map[string]*corev1.Pod{
		"frontend": func() *corev1.Pod {
			p := dynamoPod(env...)
			p.Spec.Containers[0].Image = "nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.4.1"
			p.Spec.Containers[0].LivenessProbe = tight
			return p
		}(),
		"worker": func() *corev1.Pod {
			p := dynamoPod(env...)
			p.Spec.Containers[0].Image = "nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.4.1"
			p.Spec.Containers[0].Args = []string{"--model", "Qwen/Qwen2.5-7B-Instruct"}
			p.Spec.Containers[0].LivenessProbe = tight
			p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
			return p
		}(),
	} {
		admitted, _ := m.cacheURI(pod)
		var patches []PatchOp
		patches = append(patches, m.admittedConfigPatches(pod)...)
		patches = append(patches, m.dynamoNamespacePatches(pod)...)
		patches = append(patches, m.livenessBudgetPatches(pod)...)
		got := applyPatches(t, pod, mergePatchPlan(patches))
		if got.Annotations[AdmittedConfigAnnotation] != admitted {
			t.Errorf("%s: stamped %q, want the admitted identity %q", name, got.Annotations[AdmittedConfigAnnotation], admitted)
		}
		if len(m.dynamoNamespacePatches(pod))+len(m.livenessBudgetPatches(pod)) == 0 {
			t.Errorf("%s: the test pod got no webhook patches; it does not test anything", name)
		}
		// A stamped pod is not stamped again.
		if p := m.admittedConfigPatches(got); p != nil {
			t.Errorf("%s: restamped %v", name, p)
		}
	}
}

// withoutStamp drops the admitted-config stamp (and the annotations map it
// may create) from patches: tests that a pod is otherwise admitted
// unchanged.
func withoutStamp(patches []PatchOp) []PatchOp {
	var out []PatchOp
	for _, p := range patches {
		if p.Path == "/metadata/annotations/"+jsonPointerEscape(AdmittedConfigAnnotation) {
			continue
		}
		if m, ok := p.Value.(map[string]string); ok && p.Path == "/metadata/annotations" && len(m) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}
