// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"slices"
	"sort"
	"testing"

	"github.com/vishvananda/netlink"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func withEnv(p corev1.Pod, container, env string) corev1.Pod {
	p.Spec.Containers = []corev1.Container{{Name: container, Env: []corev1.EnvVar{{Name: env, Value: "x"}}}}
	return p
}

func TestNvcaPlatformPod(t *testing.T) {
	inFn := func(p corev1.Pod) corev1.Pod { p.Namespace = "sr-0123"; return p }
	cases := []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{"utils pod of a function", inFn(cpuPod("utils", "10.0.0.9")), true},
		{"a pod named utils outside a function namespace", cpuPod("utils", "10.0.0.9"), false},
		{"utils container with the instance ID", withEnv(cpuPod("worker-abc", "10.0.0.9"), "utils", "INSTANCE_ID"), true},
		{"llm-worker container with the instance ID", withEnv(cpuPod("worker-abc", "10.0.0.9"), "llm-worker", "INSTANCE_ID"), true},
		{"utils container without the instance ID", withEnv(cpuPod("worker-abc", "10.0.0.9"), "utils", "OTHER"), false},
		{"SMB server", inFn(cpuPod("nvcf-smb-server", "10.0.0.8")), true},
		{"an etcd of the function", inFn(cpuPod("mini-service-etcd-0", "10.0.0.5")), false},
	}
	for _, c := range cases {
		if got := nvcaPlatformPod(&c.pod); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// NVCA's pods are never companions, even when a rank connects to them.
func TestMatchCompanions_LeavesOutNVCAPods(t *testing.T) {
	inFn := func(p corev1.Pod) corev1.Pod { p.Namespace = "sr-0123"; return p }
	worker := inFn(gpuPod("worker", "10.0.0.2"))
	pods := []corev1.Pod{worker, inFn(cpuPod("utils", "10.0.0.9")), inFn(cpuPod("nvcf-smb-server", "10.0.0.8")), inFn(cpuPod("etcd", "10.0.0.5"))}
	peers := map[string]bool{"10.0.0.9": true, "10.0.0.8": true, "10.0.0.5": true}
	found := matchCompanions(peers, pods, nil, nil, []*corev1.Pod{&worker})
	if len(found) != 1 || found["etcd"] == nil {
		t.Errorf("companions = %v, want only the etcd", keys(found))
	}
}

// The pods of a Dynamo instance at its capture on 2026-10-18 (ct1-e2e39):
// the workers connect to etcd and NATS through their Services; the
// frontend connects to those Services too, and to the workers; NVCA's
// request proxy connects to the frontend only.
func TestClientsOf_TheFrontendSharesTheCompanionsState(t *testing.T) {
	etcd, nats := cpuPod("mini-service-etcd", "10.1.195.212"), cpuPod("mini-service-nats", "10.1.202.86")
	frontend, proxy := cpuPod("frontend", "10.1.107.125"), cpuPod("proxy", "10.1.78.79")
	pods := []corev1.Pod{gpuPod("worker-ldr", "10.1.32.245"), gpuPod("worker-wkr", "10.1.134.219"), etcd, nats, frontend, proxy}
	svcs := []corev1.Service{
		{ObjectMeta: metav1.ObjectMeta{Name: "mini-service-etcd"}, Spec: corev1.ServiceSpec{ClusterIP: "172.20.121.36"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "mini-service-nats"}, Spec: corev1.ServiceSpec{ClusterIP: "172.20.233.101"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "frontend"}, Spec: corev1.ServiceSpec{ClusterIP: "172.20.9.9"}},
	}
	slice := func(svc, pod string) discoveryv1.EndpointSlice {
		return discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: svc}},
			Endpoints: []discoveryv1.Endpoint{{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: pod}}}}
	}
	sl := []discoveryv1.EndpointSlice{slice("mini-service-etcd", "mini-service-etcd"), slice("mini-service-nats", "mini-service-nats"), slice("frontend", "frontend")}
	companions := map[string]*corev1.Pod{"mini-service-etcd": &etcd, "mini-service-nats": &nats}

	reach := companionAddresses(companions, svcs, sl)
	want := []string{"10.1.195.212", "10.1.202.86", "172.20.121.36", "172.20.233.101"}
	if got := keys(reach); !slices.Equal(got, want) {
		t.Errorf("companion addresses = %v, want %v", got, want)
	}
	peersOf := map[string][]string{
		"frontend": {"172.20.121.36", "172.20.233.101", "10.1.32.245"},
		"proxy":    {"172.20.9.9", "10.1.107.125"},
	}
	if got := keys(clientsOf(reach, peersOf, pods)); !slices.Equal(got, []string{"frontend"}) {
		t.Errorf("clients = %v, want [frontend]; the proxy only reaches the frontend", got)
	}
}

// Both directions: a companion's connections from the ranks are ones the
// ranks opened, so the container that holds them is found only with both.
func TestTCPPeers_Both(t *testing.T) {
	engine := func(s *netlink.Socket) bool { return s.INode != 9 }
	var got []string
	for _, ip := range tcpPeers(rankSockets(), true, engine) {
		got = append(got, ip.String())
	}
	slices.Sort(got)
	if want := []string{"10.1.50.5", "10.1.62.66", "10.1.78.10"}; !slices.Equal(got, want) {
		t.Errorf("peers = %v, want %v (the client too; no loopback, no sidecar's)", got, want)
	}
	var out []string
	for _, ip := range tcpPeers(rankSockets(), false, engine) {
		out = append(out, ip.String())
	}
	slices.Sort(out)
	if want := []string{"10.1.50.5", "10.1.78.10"}; !slices.Equal(out, want) {
		t.Errorf("outbound peers = %v, want %v", out, want)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
