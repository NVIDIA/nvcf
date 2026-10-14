// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func dynamoPod(env ...corev1.EnvVar) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "sr-1", Annotations: map[string]string{DynDiscoveryAnnotation: "etcd"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Env: env}}},
	}
}

func TestDynamoNamespacePatches(t *testing.T) {
	m := &Mutator{DynamoNamespace: "nvcf-nvsnap"}
	local := []corev1.EnvVar{
		{Name: "DYN_NAMESPACE", Value: "sr-1-graph"},
		{Name: "DYN_NAMESPACE_PREFIX", Value: "sr-1-graph"},
		{Name: "ETCD_ENDPOINTS", Value: "mini-service-etcd:2379"},
		{Name: "NATS_SERVER", Value: "nats://mini-service-nats:4222"},
		{Name: "OTHER", Value: "x"},
	}
	pod := dynamoPod(local...)
	got := applyPatches(t, pod, m.dynamoNamespacePatches(pod))
	vals := map[string]string{}
	for _, e := range got.Spec.Containers[0].Env {
		vals[e.Name] = e.Value
	}
	if vals["DYN_NAMESPACE"] != "nvcf-nvsnap" || vals["DYN_NAMESPACE_PREFIX"] != "nvcf-nvsnap" || vals["OTHER"] != "x" {
		t.Fatalf("env after patch: %v", vals)
	}
	if len(got.Spec.Containers[0].Env) != len(local) {
		t.Fatalf("env grew or shrank: %v", got.Spec.Containers[0].Env)
	}

	for name, env := range map[string][]corev1.EnvVar{
		"shared etcd": {{Name: "DYN_NAMESPACE", Value: "a"}, {Name: "ETCD_ENDPOINTS", Value: "etcd.dynamo-system:2379"}, {Name: "NATS_SERVER", Value: "nats://mini-service-nats:4222"}},
		"shared nats": {{Name: "DYN_NAMESPACE", Value: "a"}, {Name: "ETCD_ENDPOINTS", Value: "mini-service-etcd:2379"}, {Name: "NATS_SERVER", Value: "nats://nats.dynamo-system.svc:4222"}},
		"no etcd set": {{Name: "DYN_NAMESPACE", Value: "a"}, {Name: "NATS_SERVER", Value: "nats://mini-service-nats:4222"}},
		"not dynamo":  {{Name: "ETCD_ENDPOINTS", Value: "mini-service-etcd:2379"}},
	} {
		p := dynamoPod(env...)
		if patches := m.dynamoNamespacePatches(p); patches != nil {
			t.Errorf("%s: patched %v", name, patches)
		}
	}
	if p := (&Mutator{}).dynamoNamespacePatches(dynamoPod(local...)); p != nil {
		t.Errorf("profile off: patched %v", p)
	}
}

func TestDynamoDiscovery(t *testing.T) {
	if got := DynamoDiscovery(dynamoPod()); got != "etcd" {
		t.Errorf("annotation: %q", got)
	}
	if got := DynamoDiscovery(dynamoPod(corev1.EnvVar{Name: "DYN_DISCOVERY_BACKEND", Value: "kubernetes"})); got != "kubernetes" {
		t.Errorf("env wins: %q", got)
	}
}

// The profile gives its components a lease that outlasts a capture, unless
// they set their own.
func TestDynamoLeaseTTL(t *testing.T) {
	m := &Mutator{DynamoNamespace: "nvcf-nvsnap", DynamoLeaseTTL: 60}
	base := []corev1.EnvVar{
		{Name: "DYN_NAMESPACE", Value: "sr-1-graph"},
		{Name: "ETCD_ENDPOINTS", Value: "mini-service-etcd:2379"},
		{Name: "NATS_SERVER", Value: "nats://mini-service-nats:4222"},
	}
	env := func(p *corev1.Pod) map[string]string {
		out := map[string]string{}
		for _, e := range p.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
	pod := dynamoPod(base...)
	if got := env(applyPatches(t, pod, m.dynamoNamespacePatches(pod))); got["ETCD_LEASE_TTL"] != "60" {
		t.Errorf("ETCD_LEASE_TTL = %q, want 60", got["ETCD_LEASE_TTL"])
	}
	own := dynamoPod(append(append([]corev1.EnvVar{}, base...), corev1.EnvVar{Name: "ETCD_LEASE_TTL", Value: "20"})...)
	if got := env(applyPatches(t, own, m.dynamoNamespacePatches(own))); got["ETCD_LEASE_TTL"] != "20" {
		t.Errorf("a workload's own ETCD_LEASE_TTL was replaced: %q", got["ETCD_LEASE_TTL"])
	}
	shared := dynamoPod(corev1.EnvVar{Name: "DYN_NAMESPACE", Value: "a"}, corev1.EnvVar{Name: "ETCD_ENDPOINTS", Value: "etcd.dynamo-system:2379"},
		corev1.EnvVar{Name: "NATS_SERVER", Value: "nats://mini-service-nats:4222"})
	if p := m.dynamoNamespacePatches(shared); p != nil {
		t.Errorf("outside the profile (shared etcd): patched %v", p)
	}
}
