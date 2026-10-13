// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func cpuPod(name, ip string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fn"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
		Status: corev1.PodStatus{PodIP: ip, PodIPs: []corev1.PodIP{{IP: ip}}}}
}

func gpuPod(name, ip string) corev1.Pod {
	p := cpuPod(name, ip)
	p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
	return p
}

func TestMatchCompanions(t *testing.T) {
	worker := gpuPod("worker", "10.0.0.2")
	pods := []corev1.Pod{worker, gpuPod("other-gpu", "10.0.0.3"), cpuPod("etcd", "10.0.0.5"), cpuPod("nats", "10.0.0.6"), cpuPod("frontend", "10.0.0.7")}
	svcs := []corev1.Service{
		{ObjectMeta: metav1.ObjectMeta{Name: "mini-service-nats"}, Spec: corev1.ServiceSpec{ClusterIP: "172.20.0.9"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "frontend"}, Spec: corev1.ServiceSpec{ClusterIP: "172.20.0.10"}},
	}
	slice := func(svc, pod string) discoveryv1.EndpointSlice {
		return discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: svc}},
			Endpoints: []discoveryv1.Endpoint{{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: pod}}}}
	}
	slices := []discoveryv1.EndpointSlice{slice("mini-service-nats", "nats"), slice("frontend", "frontend")}
	// The worker reaches etcd by its pod address, NATS through its Service,
	// another GPU pod and the internet directly.
	peers := map[string]bool{"10.0.0.5": true, "172.20.0.9": true, "10.0.0.3": true, "8.8.8.8": true}
	found := matchCompanions(peers, pods, svcs, slices, []*corev1.Pod{&worker})
	var names []string
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"etcd", "nats"}) {
		t.Errorf("companions = %v, want [etcd nats] (no GPU pod, no unreached Service, no outside address)", names)
	}
}

func TestGroupMember(t *testing.T) {
	ldr := gpuPod("ldr", "10.0.0.2")
	ldr.Annotations = map[string]string{webhook.CRIUGroupAnnotation: "k1"}
	ldr.OwnerReferences = []metav1.OwnerReference{{Controller: ptrTrue(), UID: "sts", Kind: "StatefulSet", Name: "w"}}
	companion := cpuPod("etcd", "10.0.0.5")
	companion.Annotations = map[string]string{webhook.CRIUGroupAnnotation: "k1", webhook.CRIUGroupCompanionAnnotation: "true"}
	other := companion.DeepCopy()
	other.Annotations[webhook.CRIUGroupAnnotation] = "k2"
	plain := cpuPod("frontend", "10.0.0.7")
	if !groupMember(&ldr, &companion) || groupMember(&ldr, other) || groupMember(&ldr, &plain) {
		t.Error("only a companion of the same group record joins the instance")
	}
}

func ptrTrue() *bool { b := true; return &b }

func TestKeepAddresses(t *testing.T) {
	keep := keepAddresses("10.1.0.9", "10.0.0.2=10.1.0.2,10.0.0.5=10.1.0.5")
	for _, ip := range []string{"10.1.0.9", "10.1.0.2", "10.1.0.5"} {
		if !keep[ip] {
			t.Errorf("%s dropped", ip)
		}
	}
	if keep["10.0.0.2"] || keep["172.20.0.9"] {
		t.Errorf("an old address or a Service address kept: %v", keep)
	}
}

func dynamoRank(dynNS, discovery string) *corev1.Pod {
	p := gpuPod("worker", "10.0.0.2")
	p.Namespace = "sr-abc"
	p.Annotations = map[string]string{webhook.DynDiscoveryAnnotation: discovery}
	p.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "DYN_NAMESPACE", Value: dynNS},
		{Name: "GROVE_HEADLESS_SERVICE", Value: "graph-0.sr-abc.svc.cluster.local"},
		{Name: "ETCD_ENDPOINTS", Value: "mini-service-etcd:2379"},
		{Name: "NATS_SERVER", Value: "nats://mini-service-nats:4222"},
	}
	return &p
}

func TestIdentityBlocksCapture(t *testing.T) {
	a := &Agent{}
	a.config.Webhook.DynamoNamespace = "nvcf-nvsnap"
	if n := a.identityBlocksCapture(dynamoRank("nvcf-nvsnap", "etcd")); n != "" {
		t.Errorf("a Dynamo rank under the fixed namespace with etcd discovery is restorable, got blocked by %s", n)
	}
	for name, p := range map[string]*corev1.Pod{
		"instance-derived Dynamo namespace": dynamoRank("sr-abc-graph", "etcd"),
		"Kubernetes discovery":              dynamoRank("nvcf-nvsnap", "kubernetes"),
	} {
		if a.identityBlocksCapture(p) == "" {
			t.Errorf("%s: captured", name)
		}
	}
	plain := gpuPod("vllm", "10.0.0.2")
	plain.Namespace = "sr-abc"
	if n := a.identityBlocksCapture(&plain); n != "" {
		t.Errorf("a pod that names no instance: blocked by %s", n)
	}
	if n := (&Agent{}).identityBlocksCapture(dynamoRank("nvcf-nvsnap", "etcd")); n == "" {
		t.Error("without the Dynamo profile, a rank naming its instance must not be captured")
	}
}

func TestCompanionAliasNamesItsSlot(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New()}
	c := a.criuRestorer()
	uris := []string{"cache://worker", "cache://etcd"}
	key := criuGroupKey(instanceConfigURIOf(uris), "fv")
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key)},
		Data: map[string]string{"state": "complete", "size": "2", "checkpoint.0": "w", "checkpoint.1": "e", "companion.1": "true"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.writeGroupAliases(ctx, key, "fv", uris, 1, logrus.NewEntry(logrus.New()))
	g, ok := a.lookupCRIUGroup(ctx, criuGroupKey("cache://etcd", "fv"))
	if !ok || g.Key != key || g.Slot != 1 || !g.Companion[1] || g.Ranks() != 1 {
		t.Fatalf("the companion's configuration must find the record and its slot: %+v %v", g, ok)
	}
	if g, ok := a.lookupCRIUGroup(ctx, criuGroupKey("cache://worker", "fv")); !ok || g.Slot != -1 {
		t.Fatalf("the rank's alias names no slot: %+v %v", g, ok)
	}
}

func TestGPUShareGroupInfoCompanion(t *testing.T) {
	g := gpushareGroupInfo(CheckpointRequest{Companion: true, GroupSession: "s1", GPUShareGroupIndex: 1, GPUShareGroupSize: 2})
	if g == nil || g.Session != "s1" || g.Index != 1 || g.Size != 2 || !g.Companion {
		t.Fatalf("companion group info: %+v", g)
	}
	if gpushareGroupInfo(CheckpointRequest{}) != nil {
		t.Error("a capture outside a group has no group info")
	}
}

func TestDuplicateURI(t *testing.T) {
	if duplicateURI([]string{"a", "b"}) != "" || duplicateURI([]string{"a", "b", "a"}) != "a" {
		t.Error("duplicateURI")
	}
}
