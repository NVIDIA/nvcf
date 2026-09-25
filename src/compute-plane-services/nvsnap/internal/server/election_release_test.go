// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
)

const electTestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func quietLog() logrus.FieldLogger {
	l := logrus.New()
	l.SetLevel(logrus.PanicLevel)
	return l
}

func gatedFollower(name string, owned bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fn", UID: types.UID("uid-" + name),
			Labels: map[string]string{election.HashLabel: checkpointstore.ShortHash(electTestHash), election.GatedLabel: "true"}},
		Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: election.GateName}}},
	}
	if owned {
		ctrl := true
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "rs-uid", Controller: &ctrl}}
	}
	return p
}

func leaderPod(phase corev1.PodPhase) *corev1.Pod {
	const uid = "L"
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "leader-" + uid, Namespace: "fn", UID: types.UID(uid),
			Labels: map[string]string{election.HashLabel: checkpointstore.ShortHash(electTestHash)}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func electionLease(leaderUID string, deadline time.Time) *coordinationv1.Lease {
	return &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name: election.LeaseName(electTestHash), Namespace: "nvsnap-system",
		Labels: map[string]string{election.LeaseKindLabel: election.LeaseKindValue},
		Annotations: map[string]string{
			election.HashAnnotation:            electTestHash,
			election.LeaderNamespaceAnnotation: "fn",
			election.LeaderPodAnnotation:       leaderUID,
			election.DeadlineAnnotation:        deadline.UTC().Format(time.RFC3339),
		},
	}}
}

func TestElectionRelease_ReadyUngatesFollowersAndDropsLease(t *testing.T) {
	kc := fake.NewSimpleClientset(gatedFollower("f1", true), gatedFollower("f2", false), electionLease("L", time.Now().Add(time.Hour)))
	r := &electionReleaser{kube: kc, leaseNS: "nvsnap-system", log: quietLog()}
	r.onPromoteState(context.Background(), electTestHash, "ready")
	for _, name := range []string{"f1", "f2"} {
		p, err := kc.CoreV1().Pods("fn").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Spec.SchedulingGates) != 0 || p.Labels[election.GatedLabel] != "false" {
			t.Errorf("%s: gates=%v gated=%q, want released", name, p.Spec.SchedulingGates, p.Labels[election.GatedLabel])
		}
	}
	if _, err := kc.CoordinationV1().Leases("nvsnap-system").Get(context.Background(), election.LeaseName(electTestHash), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("lease must be deleted on ready, got err=%v", err)
	}
}

func TestElectionRelease_FailedEvictsOnlyControllerOwnedFollowers(t *testing.T) {
	kc := fake.NewSimpleClientset(gatedFollower("owned", true), gatedFollower("bare", false), electionLease("L", time.Now().Add(time.Hour)))
	r := &electionReleaser{kube: kc, leaseNS: "nvsnap-system", log: quietLog()}
	r.onPromoteState(context.Background(), electTestHash, "failed")
	if _, err := kc.CoreV1().Pods("fn").Get(context.Background(), "owned", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("controller-owned follower must be deleted for re-election, err=%v", err)
	}
	if _, err := kc.CoreV1().Pods("fn").Get(context.Background(), "bare", metav1.GetOptions{}); err != nil {
		t.Errorf("bare follower must be left in place, err=%v", err)
	}
	if _, err := kc.CoordinationV1().Leases("nvsnap-system").Get(context.Background(), election.LeaseName(electTestHash), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("lease must be deleted on failed, got err=%v", err)
	}
}

func TestElectionRelease_ReconcileLeaderLiveness(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		objs      []*corev1.Pod
		deadline  time.Time
		wantEvict bool
	}{
		"live leader keeps followers":      {[]*corev1.Pod{leaderPod(corev1.PodRunning)}, now.Add(time.Hour), false},
		"pending leader keeps followers":   {[]*corev1.Pod{leaderPod(corev1.PodPending)}, now.Add(time.Hour), false},
		"leader gone evicts":               {nil, now.Add(time.Hour), true},
		"failed leader evicts":             {[]*corev1.Pod{leaderPod(corev1.PodFailed)}, now.Add(time.Hour), true},
		"deadline passed evicts even live": {[]*corev1.Pod{leaderPod(corev1.PodRunning)}, now.Add(-time.Minute), true},
	}
	for name, tc := range cases {
		kc := fake.NewSimpleClientset(gatedFollower("f", true), electionLease("L", tc.deadline))
		for _, p := range tc.objs {
			if _, err := kc.CoreV1().Pods("fn").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		r := &electionReleaser{kube: kc, leaseNS: "nvsnap-system", log: quietLog(), now: func() time.Time { return now }}
		r.reconcile(context.Background())
		_, err := kc.CoreV1().Pods("fn").Get(context.Background(), "f", metav1.GetOptions{})
		evicted := apierrors.IsNotFound(err)
		if evicted != tc.wantEvict {
			t.Errorf("%s: follower evicted=%v, want %v", name, evicted, tc.wantEvict)
		}
	}
}

func TestElectionRelease_NilClientIsNoop(t *testing.T) {
	var r *electionReleaser
	r.onPromoteState(context.Background(), electTestHash, "ready")
	r.reconcile(context.Background())
	(&electionReleaser{log: quietLog()}).onPromoteState(context.Background(), electTestHash, "failed")
}
