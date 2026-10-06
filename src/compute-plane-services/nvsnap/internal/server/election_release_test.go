// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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
			Labels:      map[string]string{election.HashLabel: checkpointstore.ShortHash(electTestHash), election.GatedLabel: "true"},
			Annotations: map[string]string{election.HashAnnotation: electTestHash, election.RoleAnnotation: string(election.RoleFollower)}},
		Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: election.GateName}}},
	}
	if owned {
		ctrl := true
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "rs-uid", Controller: &ctrl}}
	}
	return p
}

func leaderPod(phase corev1.PodPhase) *corev1.Pod {
	const id = "L"
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "leader-" + id, Namespace: "fn", UID: types.UID("uid-" + id),
			Labels:      map[string]string{election.HashLabel: checkpointstore.ShortHash(electTestHash)},
			Annotations: map[string]string{election.ElectionIDAnnotation: id}},
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
			election.LeaderIDAnnotation:        leaderUID,
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

// gatedWithoutLease lists the gated followers of the test hash when no
// election Lease exists for it: pods nothing will ever release.
func gatedWithoutLease(t *testing.T, kc *fake.Clientset) []string {
	t.Helper()
	ctx := context.Background()
	if _, err := kc.CoordinationV1().Leases("nvsnap-system").Get(ctx, election.LeaseName(electTestHash), metav1.GetOptions{}); err == nil {
		return nil
	}
	pods, err := kc.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: election.GatedLabel + "=true"})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := range pods.Items {
		out = append(out, pods.Items[i].Name)
	}
	return out
}

// A follower elected before the manifest is visible can persist after
// the ready notification listed the gated pods and deleted the Lease.
// Reconcile must still release it from the durable promote state, even
// past the election deadline and with no Lease left.
func TestElectionRelease_ReconcileReleasesFollowerPersistedAfterReady(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	kc := fake.NewSimpleClientset(electionLease("L", now.Add(time.Minute)))
	r := &electionReleaser{kube: kc, leaseNS: "nvsnap-system", log: quietLog(), now: func() time.Time { return now.Add(time.Hour) }}
	setPromoteState(r, map[string]string{electTestHash: "ready"})
	ctx := context.Background()
	r.onPromoteState(ctx, electTestHash, "ready")
	if _, err := kc.CoreV1().Pods("fn").Create(ctx, gatedFollower("late", true), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	r.reconcile(ctx)
	p, err := kc.CoreV1().Pods("fn").Get(ctx, "late", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("a follower of a promoted capture must be released, not evicted: %v", err)
	}
	if p.Labels[election.GatedLabel] != "false" || len(p.Spec.SchedulingGates) != 0 {
		t.Errorf("late follower still gated: labels %v gates %v", p.Labels, p.Spec.SchedulingGates)
	}
}

// When the leader is gone the followers are evicted. Their controller
// replaces each deleted pod at once; a replacement admitted while the
// failed Lease still exists becomes a follower of an election nobody can
// finish. Retiring the Lease first makes the replacement start a new
// election instead, so no gated pod is ever left without one.
func TestElectionRelease_EvictRetiresLeaseBeforeDeletingFollowers(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	kc := fake.NewSimpleClientset(gatedFollower("f1", true), gatedFollower("f2", true), electionLease("L", now.Add(time.Hour)))
	leaseGVR := coordinationv1.SchemeGroupVersion.WithResource("leases")
	podGVR := corev1.SchemeGroupVersion.WithResource("pods")
	n := 0
	// The ReplicaSet and the webhook, reacting to each follower deletion:
	// the replacement is a gated follower while the Lease exists, and the
	// new leader (with a new Lease) once it is gone. Reactors run under
	// the fake's lock, so this talks to the tracker directly.
	kc.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		n++
		name := fmt.Sprintf("repl-%d", n)
		if _, err := kc.Tracker().Get(leaseGVR, "nvsnap-system", election.LeaseName(electTestHash)); err == nil {
			return false, nil, kc.Tracker().Create(podGVR, gatedFollower(name, true), "fn")
		}
		lead := leaderPod(corev1.PodPending)
		lead.Name = name
		if err := kc.Tracker().Create(podGVR, lead, "fn"); err != nil {
			return false, nil, err
		}
		return false, nil, kc.Tracker().Create(leaseGVR, electionLease("new", now.Add(time.Hour)), "nvsnap-system")
	})
	r := &electionReleaser{kube: kc, leaseNS: "nvsnap-system", log: quietLog(), now: func() time.Time { return now }}
	r.reconcile(context.Background()) // leader pod "L" does not exist: evict
	if left := gatedWithoutLease(t, kc); len(left) > 0 {
		t.Errorf("gated followers left with no election: %v", left)
	}
}

// setPromoteState gives the releaser a catalog of promote states by hash.
func setPromoteState(r *electionReleaser, states map[string]string) {
	r.promoteState = func(hash string) (string, error) { return states[hash], nil }
}

// Retiring a failed election must never delete the Lease of a newer one
// that took the same name: the delete is preconditioned on the failed
// Lease's UID, and the followers listed for it are then left alone.
func TestElectionRelease_EvictLeavesNewerElectionAlone(t *testing.T) {
	failed := electionLease("L", time.Now().Add(-time.Minute))
	failed.UID = "old"
	newer := electionLease("N", time.Now().Add(time.Hour))
	newer.UID = "new"
	kc := fake.NewSimpleClientset(gatedFollower("f", true), newer)
	leaseGVR := coordinationv1.SchemeGroupVersion.WithResource("leases")
	// The API server's precondition check, which the fake does not do.
	kc.PrependReactor("delete", "leases", func(a k8stesting.Action) (bool, runtime.Object, error) {
		d := a.(k8stesting.DeleteActionImpl)
		obj, err := kc.Tracker().Get(leaseGVR, d.GetNamespace(), d.GetName())
		if err != nil {
			return false, nil, nil
		}
		if pre := d.DeleteOptions.Preconditions; pre != nil && pre.UID != nil && *pre.UID != obj.(*coordinationv1.Lease).UID {
			return true, nil, apierrors.NewConflict(leaseGVR.GroupResource(), d.GetName(), fmt.Errorf("precondition failed: UID"))
		}
		return false, nil, nil
	})
	r := &electionReleaser{kube: kc, leaseNS: "nvsnap-system", log: quietLog()}
	if _, err := r.evict(context.Background(), electTestHash, "deadline passed", failed); err != nil {
		t.Fatal(err)
	}
	if _, err := kc.CoordinationV1().Leases("nvsnap-system").Get(context.Background(), election.LeaseName(electTestHash), metav1.GetOptions{}); err != nil {
		t.Errorf("the newer election's Lease must survive: %v", err)
	}
	if _, err := kc.CoreV1().Pods("fn").Get(context.Background(), "f", metav1.GetOptions{}); err != nil {
		t.Errorf("followers that may belong to the newer election must not be evicted: %v", err)
	}
}

// Gated followers with no election left: the durable promote state
// decides between release, eviction for re-election, and waiting.
func TestElectionRelease_ReconcileOrphansByPromoteState(t *testing.T) {
	for state, want := range map[string]string{
		"ready":   "released",
		"failed":  "evicted",
		"":        "evicted",
		"writing": "gated",
	} {
		kc := fake.NewSimpleClientset(gatedFollower("f", true))
		r := &electionReleaser{kube: kc, leaseNS: "nvsnap-system", log: quietLog()}
		setPromoteState(r, map[string]string{electTestHash: state})
		r.reconcile(context.Background())
		got := "gated"
		p, err := kc.CoreV1().Pods("fn").Get(context.Background(), "f", metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			got = "evicted"
		case err != nil:
			t.Fatal(err)
		case p.Labels[election.GatedLabel] == "false":
			got = "released"
		}
		if got != want {
			t.Errorf("promote state %q: follower %s, want %s", state, got, want)
		}
	}
}
