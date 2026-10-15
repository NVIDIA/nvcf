// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
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

// A companion's container has no GPU devices: nothing to declare, not an
// error.
func TestNvidiaDevExternalsWithoutGPUs(t *testing.T) {
	exts, err := nvidiaDevExternals(t.TempDir())
	if err != nil || len(exts) != 0 {
		t.Fatalf("got %v %v, want no externals and no error", exts, err)
	}
}

func TestServiceAliases(t *testing.T) {
	ready, notReady := true, false
	pod := func(name, ip string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{PodIP: ip}}
	}
	members := []*corev1.Pod{pod("worker-0", "10.1.0.10"), pod("etcd-x", "10.1.0.20"), pod("nats-y", "10.1.0.30")}
	svc := func(name, cip string, port, target int32) corev1.Service {
		return corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.ServiceSpec{ClusterIP: cip,
			Ports: []corev1.ServicePort{{Name: "p", Port: port}}}}
	}
	slice := func(svcName string, target int32, eps ...discoveryv1.Endpoint) discoveryv1.EndpointSlice {
		name := "p"
		return discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: svcName}},
			Endpoints: eps, Ports: []discoveryv1.EndpointPort{{Name: &name, Port: &target}}}
	}
	ep := func(pod string, r *bool) discoveryv1.Endpoint {
		return discoveryv1.Endpoint{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: pod}, Conditions: discoveryv1.EndpointConditions{Ready: r}}
	}
	svcs := []corev1.Service{
		svc("etcd", "172.20.0.1", 2379, 2379),
		svc("nats", "172.20.0.2", 4222, 4222),
		svc("web", "172.20.0.3", 80, 8080),   // port translated
		svc("ext", "172.20.0.4", 9000, 9000), // an endpoint outside the instance
		svc("two", "172.20.0.5", 7000, 7000), // two members behind one address
		{ObjectMeta: metav1.ObjectMeta{Name: "headless"}, Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone}},
	}
	slices := []discoveryv1.EndpointSlice{
		slice("etcd", 2379, ep("etcd-x", &ready)),
		slice("nats", 4222, ep("nats-y", nil), ep("old-nats", &notReady)),
		slice("web", 8080, ep("worker-0", &ready)),
		slice("ext", 9000, ep("etcd-x", &ready), ep("someone-else", &ready)),
		slice("two", 7000, ep("etcd-x", &ready), ep("nats-y", &ready)),
	}
	got := serviceAliases(svcs, slices, members)
	if want := "172.20.0.1=10.1.0.20,172.20.0.2=10.1.0.30"; got != want {
		t.Errorf("serviceAliases = %q, want %q", got, want)
	}
}

func TestAliasAddrMap(t *testing.T) {
	got := aliasAddrMap("10.1.0.10=10.2.0.10,10.1.0.20=10.2.0.20", "172.20.0.1=10.1.0.20,172.20.0.9=10.9.9.9")
	if want := "10.1.0.10=10.2.0.10,10.1.0.20=10.2.0.20,172.20.0.1=10.2.0.20"; got != want {
		t.Errorf("aliasAddrMap = %q, want %q", got, want)
	}
	if got := aliasAddrMap("10.1.0.10=10.2.0.10", ""); got != "10.1.0.10=10.2.0.10" {
		t.Errorf("no aliases = %q", got)
	}
}

func TestPickCompanionContainer(t *testing.T) {
	if c, err := pickCompanionContainer(2, []string{"main"}); err != nil || c != "main" {
		t.Errorf("one talking: %q, %v", c, err)
	}
	if _, err := pickCompanionContainer(2, nil); err == nil {
		t.Error("none talking: accepted")
	}
	if _, err := pickCompanionContainer(2, []string{"main", "sidecar"}); err == nil {
		t.Error("two talking: accepted")
	}
}

// The sockets of a container are those of the processes in its cgroup;
// another container's processes in the same pod are left out.
func TestContainerSocketInodes(t *testing.T) {
	proc := t.TempDir()
	mk := func(pid, cgroup string, fds map[string]string) {
		d := filepath.Join(proc, pid)
		if err := os.MkdirAll(filepath.Join(d, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "cgroup"), []byte(cgroup), 0o644); err != nil {
			t.Fatal(err)
		}
		for fd, target := range fds {
			if err := os.Symlink(target, filepath.Join(d, "fd", fd)); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("100", "0::/kubepods/pod1/main\n", map[string]string{"3": "socket:[111]", "4": "/dev/null"})
	mk("101", "0::/kubepods/pod1/main\n", map[string]string{"5": "socket:[112]"})
	mk("200", "0::/kubepods/pod1/sidecar\n", map[string]string{"3": "socket:[222]"})
	if err := os.MkdirAll(filepath.Join(proc, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := containerSocketInodes(proc, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !got[111] || !got[112] || got[222] || len(got) != 2 {
		t.Errorf("inodes = %v, want 111 and 112 only", got)
	}
}

// The record keeps the container of each member, and an older record
// without it parses with empty names.
func TestParseCRIUGroup_Containers(t *testing.T) {
	d := map[string]string{"state": "complete", "size": "2", "checkpoint.0": "c0", "checkpoint.1": "c1",
		"companion.1": "true", "container.0": "main", "container.1": "frontend"}
	g, ok := parseCRIUGroup("k", d)
	if !ok || len(g.Containers) != 2 || g.Containers[0] != "main" || g.Containers[1] != "frontend" {
		t.Fatalf("parsed %+v, %v", g, ok)
	}
	delete(d, "container.0")
	delete(d, "container.1")
	if g, ok := parseCRIUGroup("k", d); !ok || g.Containers[0] != "" || g.Containers[1] != "" {
		t.Errorf("old record: %+v, %v", g.Containers, ok)
	}
}
