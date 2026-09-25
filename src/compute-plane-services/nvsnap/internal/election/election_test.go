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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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
	e := &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system", Deadline: 30 * time.Minute, Now: func() time.Time { return now }}

	roles := map[Role]int{}
	for _, uid := range []string{"a", "b", "c", "d"} {
		r, err := e.Elect(context.Background(), hash, pod(uid))
		if err != nil {
			t.Fatal(err)
		}
		roles[r]++
	}
	if roles[RoleLeader] != 1 || roles[RoleFollower] != 3 {
		t.Fatalf("roles = %v, want 1 leader 3 followers", roles)
	}
	lease, err := kc.CoordinationV1().Leases("nvsnap-system").Get(context.Background(), LeaseName(hash), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *lease.Spec.HolderIdentity != "a" || lease.Annotations[LeaderPodAnnotation] != "a" || lease.Annotations[LeaderNamespaceAnnotation] != "fn" {
		t.Errorf("lease must record the first admission as leader: %+v", lease.ObjectMeta)
	}
	if lease.Annotations[DeadlineAnnotation] != now.Add(30*time.Minute).Format(time.RFC3339) {
		t.Errorf("deadline = %q, want admission + 30m", lease.Annotations[DeadlineAnnotation])
	}
	if lease.Labels[LeaseKindLabel] != LeaseKindValue || lease.Labels[HashLabel] == "" || lease.Annotations[HashAnnotation] != hash {
		t.Errorf("lease must be selectable by kind and hash: %+v", lease.ObjectMeta)
	}
	// A different hash is a separate election.
	other := "ffff" + hash[4:]
	if r, _ := e.Elect(context.Background(), other, pod("z")); r != RoleLeader {
		t.Errorf("first admission of another hash must lead, got %s", r)
	}
}

func TestLeaseElector_Errors(t *testing.T) {
	e := &LeaseElector{KubeClient: fake.NewSimpleClientset(), Namespace: "nvsnap-system"}
	if _, err := e.Elect(context.Background(), hash, pod("")); err == nil {
		t.Error("a pod without UID cannot hold a lease; want error")
	}
	kc := fake.NewSimpleClientset()
	kc.PrependReactor("create", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	e = &LeaseElector{KubeClient: kc, Namespace: "nvsnap-system"}
	if _, err := e.Elect(context.Background(), hash, pod("a")); err == nil {
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
	named := pod("u")
	named.Name = "w-abc"
	if got := PodIdentity(named); got != "fn/w-abc(u)" {
		t.Errorf("named pod identity = %q", got)
	}
	if PodIdentity(nil) != "" {
		t.Error("nil pod must yield empty identity")
	}
}
