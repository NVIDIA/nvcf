// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package election

import (
	"context"
	"errors"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func pod(uid string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "fn", GenerateName: "w-", UID: types.UID(uid)}}
}

const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Exactly one of N admissions for the same hash is the leader, and it is
// the one whose UID the Lease records.
func TestLeaseElector_OneLeaderPerHash(t *testing.T) {
	kc := fake.NewSimpleClientset()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	ids := []string{"a", "b", "c", "d"}
	e := &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system", Deadline: 30 * time.Minute,
		Now: func() time.Time { return now }, NewID: func() string { id := ids[0]; ids = ids[1:]; return id }}

	roles := map[Role]int{}
	var leaderID string
	for range 4 {
		r, id, err := e.Elect(context.Background(), hash, pod(""))
		if err != nil {
			t.Fatal(err)
		}
		roles[r]++
		if r == RoleLeader {
			leaderID = id
		} else if id != "" {
			t.Errorf("follower must not receive an id, got %q", id)
		}
	}
	if roles[RoleLeader] != 1 || roles[RoleFollower] != 3 {
		t.Fatalf("roles = %v, want 1 leader 3 followers", roles)
	}
	lease, err := kc.CoordinationV1().Leases("nvsnap-system").Get(context.Background(), LeaseName(hash), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if leaderID != "a" || *lease.Spec.HolderIdentity != "a" || lease.Annotations[LeaderIDAnnotation] != "a" || lease.Annotations[LeaderNamespaceAnnotation] != "fn" {
		t.Errorf("lease must record the first admission's id as leader (got %q): %+v", leaderID, lease.ObjectMeta)
	}
	if lease.Annotations[DeadlineAnnotation] != now.Add(30*time.Minute).Format(time.RFC3339) {
		t.Errorf("deadline = %q, want admission + 30m", lease.Annotations[DeadlineAnnotation])
	}
	if lease.Labels[LeaseKindLabel] != LeaseKindValue || lease.Labels[HashLabel] == "" || lease.Annotations[HashAnnotation] != hash {
		t.Errorf("lease must be selectable by kind and hash: %+v", lease.ObjectMeta)
	}
	// A different hash is a separate election.
	other := "ffff" + hash[4:]
	ids = []string{"z"}
	if r, _, _ := e.Elect(context.Background(), other, pod("")); r != RoleLeader {
		t.Errorf("first admission of another hash must lead, got %s", r)
	}
}

func TestLeaseElector_Errors(t *testing.T) {
	e := &LeaseElector{KubeClient: fake.NewSimpleClientset(), Namespace: "nvsnap-system"}
	if _, _, err := e.Elect(context.Background(), hash, nil); err == nil {
		t.Error("nil pod; want error")
	}
	// Pods have no UID at CREATE admission; the elector must not need one.
	if r, id, err := e.Elect(context.Background(), hash, pod("")); err != nil || r != RoleLeader || id == "" {
		t.Errorf("uid-less pod must be electable with a minted id, got role=%s id=%q err=%v", r, id, err)
	}
	kc := fake.NewSimpleClientset()
	kc.PrependReactor("create", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	e = &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system"}
	if _, _, err := e.Elect(context.Background(), hash, pod("a")); err == nil {
		t.Error("a non-AlreadyExists create error must surface so the webhook fails open")
	}
	if (&LeaseElector{Namespace: "x"}).deadline() != DefaultDeadline {
		t.Error("zero Deadline must default")
	}
	var _ *coordinationv1.Lease
}

func TestPodIdentity(t *testing.T) {
	if got := PodIdentity(pod("u")); got != "fn/w-*(u)" {
		t.Errorf("generateName pod identity = %q", got)
	}
	if got := PodIdentity(pod("")); got != "fn/w-*" {
		t.Errorf("admission-time pod (no uid) identity = %q", got)
	}
	named := pod("u")
	named.Name = "w-abc"
	if got := PodIdentity(named); got != "fn/w-abc(u)" {
		t.Errorf("named pod identity = %q", got)
	}
	if PodIdentity(nil) != "" {
		t.Error("nil pod must yield empty identity")
	}
}

// modelPod is a pod running the model container the election tests elect.
func modelPod(uid string) *corev1.Pod {
	p := pod(uid)
	p.Spec.Containers = []corev1.Container{{Name: "engine", Image: "vllm/vllm-openai:v1", Command: []string{"vllm"}, Args: []string{"serve", "org/model"}}}
	return p
}

// The watcher takes a pod's stamped hash only from the elected leader on
// the architecture it was elected for. Everything a pod can set on itself
// (the hash, the election id, the pin) is checked against the Lease,
// which it cannot write.
func TestLeaseElector_VerifyStamp(t *testing.T) {
	created := metav1.NewTime(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	leader := func() *corev1.Pod {
		p := modelPod("u1")
		p.Name = "w-leader"
		p.CreationTimestamp = created
		p.Labels = map[string]string{HashLabel: checkpointstore.ShortHash(hash)}
		p.Annotations = map[string]string{HashAnnotation: hash, ElectionIDAnnotation: "leader-id"}
		p.Spec.NodeSelector = ArchPin("9.0")
		return p
	}
	other := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	cases := map[string]struct {
		pod    func() *corev1.Pod
		hash   string
		nodeCC string
		ok     bool
	}{
		"the leader on its architecture": {leader, hash, "9.0", true},
		"another model's hash":           {leader, other, "9.0", false},
		"a forged election id": {func() *corev1.Pod {
			p := leader()
			p.Annotations[ElectionIDAnnotation] = "guess"
			return p
		}, hash, "9.0", false},
		"no election id": {func() *corev1.Pod {
			p := leader()
			delete(p.Annotations, ElectionIDAnnotation)
			return p
		}, hash, "9.0", false},
		"another namespace": {func() *corev1.Pod {
			p := leader()
			p.Namespace = "tenant-b"
			return p
		}, hash, "9.0", false},
		"not pinned": {func() *corev1.Pod {
			p := leader()
			p.Spec.NodeSelector = nil
			return p
		}, hash, "9.0", false},
		"another architecture":      {leader, hash, "8.0", false},
		"node architecture unknown": {leader, hash, "", false},
		// A pod that copies every stamp off the leader is still not it.
		"the stamps copied onto another pod": {func() *corev1.Pod {
			p := leader()
			p.Name, p.UID = "w-copy", "u2"
			return p
		}, hash, "9.0", false},
		"another image": {func() *corev1.Pod {
			p := leader()
			p.Spec.Containers[0].Image = "busybox"
			return p
		}, hash, "9.0", false},
	}
	for name, c := range cases {
		kc := fake.NewSimpleClientset(leader())
		e := &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system", NewID: func() string { return "leader-id" }}
		if _, _, err := e.Elect(context.Background(), hash, modelPod("")); err != nil {
			t.Fatal(err)
		}
		err := e.VerifyStamp(context.Background(), c.pod(), c.hash, c.nodeCC)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", name, err, c.ok)
		}
	}
	// The election gone (released, failed or never held): nothing vouches.
	gone := &LeaseElector{KubeClient: fake.NewSimpleClientset(leader()), Namespace: "nvsnap-system"}
	if err := gone.VerifyStamp(context.Background(), leader(), hash, "9.0"); err == nil {
		t.Error("a stamp with no live election verified")
	}
}

// A pod that copies the leader's election id, hash and pin is refused
// whether it asks before or after the leader, and the Lease keeps the
// leader's UID once bound even if the leader pod goes away.
func TestLeaseElector_VerifyStamp_CopiedStamps(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	stamped := func(name, uid string, at time.Time) *corev1.Pod {
		p := modelPod(uid)
		p.Name = name
		p.CreationTimestamp = metav1.NewTime(at)
		p.Labels = map[string]string{HashLabel: checkpointstore.ShortHash(hash)}
		p.Annotations = map[string]string{HashAnnotation: hash, ElectionIDAnnotation: "leader-id"}
		p.Spec.NodeSelector = ArchPin("9.0")
		return p
	}
	ctx := context.Background()
	leader := stamped("w-leader", "u1", t0)
	copied := stamped("w-copy", "u2", t0.Add(3*time.Second))
	kc := fake.NewSimpleClientset(leader, copied)
	e := &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system", NewID: func() string { return "leader-id" }}
	if _, _, err := e.Elect(ctx, hash, modelPod("")); err != nil {
		t.Fatal(err)
	}
	// The copy turns Ready first, long before the leader has downloaded.
	if err := e.VerifyStamp(ctx, copied, hash, "9.0"); err == nil {
		t.Fatal("a pod carrying copied stamps verified")
	}
	lease, err := kc.CoordinationV1().Leases("nvsnap-system").Get(ctx, LeaseName(hash), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Annotations[LeaderUIDAnnotation] != "u1" {
		t.Fatalf("the Lease must bind the earliest pod carrying the id, got %q", lease.Annotations[LeaderUIDAnnotation])
	}
	if err := e.VerifyStamp(ctx, leader, hash, "9.0"); err != nil {
		t.Fatalf("the leader must verify: %v", err)
	}
	// Once bound, the leader pod going away does not hand the election to
	// the copy.
	if err := kc.CoreV1().Pods("fn").Delete(ctx, "w-leader", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.VerifyStamp(ctx, copied, hash, "9.0"); err == nil {
		t.Error("the copy verified after the bound leader was deleted")
	}
}

// Two pods created in the same second carrying the id cannot be told
// apart: neither is vouched for.
func TestLeaseElector_VerifyStamp_Ambiguous(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	pods := make([]runtime.Object, 0, 2)
	for _, n := range []string{"a", "b"} {
		p := modelPod("u-" + n)
		p.Name, p.CreationTimestamp = "w-"+n, at
		p.Labels = map[string]string{HashLabel: checkpointstore.ShortHash(hash)}
		p.Annotations = map[string]string{HashAnnotation: hash, ElectionIDAnnotation: "leader-id"}
		p.Spec.NodeSelector = ArchPin("9.0")
		pods = append(pods, p)
	}
	kc := fake.NewSimpleClientset(pods...)
	e := &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system", NewID: func() string { return "leader-id" }}
	if _, _, err := e.Elect(context.Background(), hash, modelPod("")); err != nil {
		t.Fatal(err)
	}
	for _, p := range pods {
		if err := e.VerifyStamp(context.Background(), p.(*corev1.Pod), hash, "9.0"); err == nil {
			t.Errorf("%s verified though it cannot be told from its twin", p.(*corev1.Pod).Name)
		}
	}
	lease, _ := kc.CoordinationV1().Leases("nvsnap-system").Get(context.Background(), LeaseName(hash), metav1.GetOptions{})
	if lease.Annotations[LeaderUIDAnnotation] != "" {
		t.Errorf("an ambiguous leader must not be bound, got %q", lease.Annotations[LeaderUIDAnnotation])
	}
}

// A concurrent check that binds the leader first wins; the loser re-reads
// and uses its binding.
func TestLeaseElector_VerifyStamp_BindConflict(t *testing.T) {
	leader := modelPod("u1")
	leader.Name = "w-leader"
	leader.Labels = map[string]string{HashLabel: checkpointstore.ShortHash(hash)}
	leader.Annotations = map[string]string{HashAnnotation: hash, ElectionIDAnnotation: "leader-id"}
	leader.Spec.NodeSelector = ArchPin("9.0")
	kc := fake.NewSimpleClientset(leader)
	e := &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system", NewID: func() string { return "leader-id" }}
	if _, _, err := e.Elect(context.Background(), hash, modelPod("")); err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	kc.PrependReactor("update", "leases", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts > 0 {
			return false, nil, nil
		}
		conflicts++
		// Another agent binds the leader between this check's read and
		// its update.
		cur, err := kc.Tracker().Get(coordinationv1.SchemeGroupVersion.WithResource("leases"), "nvsnap-system", LeaseName(hash))
		if err != nil {
			return true, nil, err
		}
		l := cur.(*coordinationv1.Lease).DeepCopy()
		l.Annotations[LeaderUIDAnnotation] = "u1"
		if err := kc.Tracker().Update(coordinationv1.SchemeGroupVersion.WithResource("leases"), l, "nvsnap-system"); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewConflict(coordinationv1.Resource("leases"), LeaseName(hash), errors.New("modified"))
	})
	if err := e.VerifyStamp(context.Background(), leader, hash, "9.0"); err != nil {
		t.Errorf("the leader must verify after a binding conflict: %v", err)
	}
	if conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", conflicts)
	}
}

func TestImageRepo(t *testing.T) {
	for ref, want := range map[string]string{
		"busybox":                        "busybox",
		"busybox:1.36":                   "busybox",
		"nvcr.io/org/img:v1":             "nvcr.io/org/img",
		"reg:5000/org/img":               "reg:5000/org/img",
		"reg:5000/org/img:v1":            "reg:5000/org/img",
		"reg:5000/org/img@sha256:abc":    "reg:5000/org/img",
		"reg:5000/org/img:v1@sha256:abc": "reg:5000/org/img",
		"":                               "",
	} {
		if got := imageRepo(ref); got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestSpecIdentity(t *testing.T) {
	base := SpecIdentity(modelPod("a"), 0)
	if base == "" || SpecIdentity(modelPod("b"), 0) != base {
		t.Fatal("the identity must depend on what the pod runs, not on which pod it is")
	}
	for name, mutate := range map[string]func(*corev1.Container){
		"name":    func(c *corev1.Container) { c.Name = "other" },
		"image":   func(c *corev1.Container) { c.Image = "busybox" },
		"command": func(c *corev1.Container) { c.Command = []string{"sh"} },
		"args":    func(c *corev1.Container) { c.Args = []string{"serve", "org/other"} },
	} {
		p := modelPod("a")
		mutate(&p.Spec.Containers[0])
		if SpecIdentity(p, 0) == base {
			t.Errorf("a different %s must change the identity", name)
		}
	}
	p := modelPod("a")
	p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "HF_HOME", Value: "/opt/nvsnap/hf"}}
	if SpecIdentity(p, 0) != base {
		t.Error("env the webhook injects must not change the identity")
	}
	for _, image := range []string{
		"vllm/vllm-openai:v2",
		"vllm/vllm-openai@sha256:0123456789abcdef",
		"vllm/vllm-openai:v1@sha256:0123456789abcdef",
		"vllm/vllm-openai",
	} {
		p := modelPod("a")
		p.Spec.Containers[0].Image = image
		if SpecIdentity(p, 0) != base {
			t.Errorf("a tag or digest pinned after admission (%s) must not change the identity", image)
		}
	}
	if SpecIdentity(nil, 0) != "" || SpecIdentity(modelPod("a"), 1) != "" || SpecIdentity(modelPod("a"), -1) != "" {
		t.Error("no model container must yield an empty identity")
	}
}
