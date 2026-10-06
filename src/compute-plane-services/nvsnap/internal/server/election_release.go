// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
)

// Election release: the webhook gates follower pods of a hash until the
// leader's capture is promoted (docs/proposals/helm-chart-cache-election.md).
// nvsnap-server is the one component that learns about the promote (the
// agent posts pvc-state), so it removes the gates on ready and, when the
// leader fails or overruns its deadline, evicts the followers so their
// controller recreates them and a new election runs.

// electionReleaser holds the cluster access the release needs; both the
// HTTP handler and the reconciler use it.
type electionReleaser struct {
	kube    kubernetes.Interface
	leaseNS string
	log     logrus.FieldLogger
	now     func() time.Time
	// promoteState reads the durable promote state of a hash from the
	// catalog ("" when no capture row has one). Reconcile uses it to
	// decide the fate of gated followers whose election is gone. nil
	// disables that sweep.
	promoteState func(hash string) (string, error)
}

// leaseRetireBackoff bounds the retries of a failed election's Lease
// deletion; the followers are only evicted once it is gone.
var leaseRetireBackoff = wait.Backoff{Steps: 5, Duration: 200 * time.Millisecond, Factor: 2, Jitter: 0.1}

// errElectionReplaced reports that the Lease of hash now belongs to a
// different election than the one being retired.
var errElectionReplaced = errors.New("election lease replaced by a new election")

func followerSelector(hash string) string {
	return fmt.Sprintf("%s=%s,%s=true", election.HashLabel, checkpointstore.ShortHash(hash), election.GatedLabel)
}

// listGated returns the gated followers of hash across all namespaces.
func (r *electionReleaser) listGated(ctx context.Context, hash string) ([]corev1.Pod, error) {
	pods, err := r.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: followerSelector(hash)})
	if err != nil {
		return nil, fmt.Errorf("list gated followers: %w", err)
	}
	return pods.Items, nil
}

// release drops the scheduling gate on every gated follower of hash and
// deletes the election Lease; the rox is Bound at this point so the pods
// schedule straight into a warm start. Idempotent.
func (r *electionReleaser) release(ctx context.Context, hash string) (released int, err error) {
	pods, err := r.listGated(ctx, hash)
	if err != nil {
		return 0, err
	}
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"labels": map[string]string{election.GatedLabel: "false"}},
		"spec":     map[string]any{"schedulingGates": []any{}},
	})
	for i := range pods {
		p := &pods[i]
		if _, err := r.kube.CoreV1().Pods(p.Namespace).Patch(ctx, p.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return released, fmt.Errorf("ungate %s/%s: %w", p.Namespace, p.Name, err)
		}
		released++
	}
	r.deleteLease(ctx, hash)
	return released, nil
}

// evict deletes the gated followers of hash that a controller will
// recreate, so the recreated pods run a fresh election. A follower's
// volumes name a claim that will now never bind and pod volumes are
// immutable, so recreation is the only way to change its fate. Pods
// without a controller owner are left in place and logged.
//
// The failed election's Lease (failed, or the current one when nil) is
// retired before any follower is deleted. A controller replaces a
// deleted pod at once; admitted while that Lease still existed, the
// replacement would join an election that can no longer finish, and
// would be missing from the list below. With the Lease gone first, it
// starts a new election instead. Only the followers listed before the
// retirement are deleted, so a new election's pods are never touched.
func (r *electionReleaser) evict(ctx context.Context, hash, reason string, failed *coordinationv1.Lease) (evicted int, err error) {
	pods, err := r.listGated(ctx, hash)
	if err != nil {
		return 0, err
	}
	if err := r.retireLease(ctx, hash, failed); err != nil {
		if errors.Is(err, errElectionReplaced) {
			r.log.WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "reason": reason}).
				Info("election: a new election replaced the failed one; its followers are left to it")
			return 0, nil
		}
		return 0, fmt.Errorf("retire election lease: %w", err)
	}
	for i := range pods {
		p := &pods[i]
		if metav1.GetControllerOf(p) == nil {
			r.log.WithFields(logrus.Fields{"pod": p.Namespace + "/" + p.Name, "hash": checkpointstore.ShortHash(hash)}).
				Warn("election: gated follower has no controller; left gated (delete it by hand)")
			continue
		}
		if err := r.kube.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return evicted, fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, err)
		}
		evicted++
	}
	r.log.WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "evicted": evicted, "reason": reason}).
		Info("election: leader gone; followers evicted for re-election")
	return evicted, nil
}

// retireLease deletes the Lease of the failed election, preconditioned
// on its UID and resourceVersion so a Lease a new election created under
// the same name is never deleted. A conflict is re-read: the same
// election (renewed) is retried, a different one is errElectionReplaced.
// Transient errors are retried; a Lease already gone is success.
func (r *electionReleaser) retireLease(ctx context.Context, hash string, failed *coordinationv1.Lease) error {
	leases := r.kube.CoordinationV1().Leases(r.leaseNS)
	name := election.LeaseName(hash)
	target := failed
	return retry.OnError(leaseRetireBackoff, func(err error) bool {
		return ctx.Err() == nil && !errors.Is(err, errElectionReplaced)
	}, func() error {
		if target == nil {
			cur, err := leases.Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			target = cur
		}
		uid, rv := target.UID, target.ResourceVersion
		err := leases.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
		if err == nil || apierrors.IsNotFound(err) {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
		cur, gerr := leases.Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(gerr):
			return nil
		case gerr != nil:
			return gerr
		case cur.UID != uid:
			return errElectionReplaced
		}
		target = cur
		return err
	})
}

func (r *electionReleaser) deleteLease(ctx context.Context, hash string) {
	if err := r.kube.CoordinationV1().Leases(r.leaseNS).Delete(ctx, election.LeaseName(hash), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		r.log.WithError(err).WithField("hash", checkpointstore.ShortHash(hash)).Warn("election: lease delete failed")
	}
}

// onPromoteState reacts to the agent's promote state for hash.
func (r *electionReleaser) onPromoteState(ctx context.Context, hash, state string) {
	if r == nil || r.kube == nil {
		return
	}
	switch state {
	case "ready":
		n, err := r.release(ctx, hash)
		if err != nil {
			r.log.WithError(err).WithField("hash", checkpointstore.ShortHash(hash)).Warn("election: release failed")
			return
		}
		r.log.WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "released": n}).Info("election: capture promoted; followers released")
	case "failed":
		n, err := r.evict(ctx, hash, "promote failed", nil)
		if err != nil {
			r.log.WithError(err).WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "evicted": n}).Warn("election: evict incomplete")
		}
	}
}

// reconcile checks every live election: a leader pod that is gone or has
// terminated without a promote, or a Lease past its deadline, means the
// followers will never be released; evict them so a new election runs.
func (r *electionReleaser) reconcile(ctx context.Context) {
	if r == nil || r.kube == nil {
		return
	}
	leases, err := r.kube.CoordinationV1().Leases(r.leaseNS).List(ctx, metav1.ListOptions{
		LabelSelector: election.LeaseKindLabel + "=" + election.LeaseKindValue,
	})
	if err != nil {
		r.log.WithError(err).Warn("election: list leases failed")
		return
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	for i := range leases.Items {
		l := &leases.Items[i]
		hash := l.Annotations[election.HashAnnotation]
		if hash == "" {
			continue
		}
		if dl, err := time.Parse(time.RFC3339, l.Annotations[election.DeadlineAnnotation]); err == nil && now.After(dl) {
			r.evictLogged(ctx, hash, "deadline passed", l)
			continue
		}
		if !r.leaderAlive(ctx, l.Annotations[election.LeaderNamespaceAnnotation], l.Annotations[election.LeaderIDAnnotation], hash) {
			r.evictLogged(ctx, hash, "leader pod gone or terminated", l)
		}
	}
	r.reconcileOrphans(ctx)
}

func (r *electionReleaser) evictLogged(ctx context.Context, hash, reason string, failed *coordinationv1.Lease) {
	if n, err := r.evict(ctx, hash, reason, failed); err != nil {
		r.log.WithError(err).WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "evicted": n, "reason": reason}).
			Warn("election: evict incomplete")
	}
}

// reconcileOrphans settles gated followers whose election is gone. A
// follower can persist after the ready notification listed the gated
// pods and deleted the Lease (its admission was still in flight), and
// the Lease loop above never sees it again. The durable promote state
// decides: ready releases it, failed or none evicts it for re-election,
// and a promote still in progress is left to deliver its own ready.
// The Leases are listed after the pods, so a follower admitted to a live
// election is never taken for an orphan.
func (r *electionReleaser) reconcileOrphans(ctx context.Context) {
	if r.promoteState == nil {
		return
	}
	pods, err := r.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: election.GatedLabel + "=true"})
	if err != nil {
		r.log.WithError(err).Warn("election: list gated followers failed")
		return
	}
	hashes := map[string]bool{}
	for i := range pods.Items {
		if h := pods.Items[i].Annotations[election.HashAnnotation]; h != "" {
			hashes[h] = true
		}
	}
	if len(hashes) == 0 {
		return
	}
	leases, err := r.kube.CoordinationV1().Leases(r.leaseNS).List(ctx, metav1.ListOptions{
		LabelSelector: election.LeaseKindLabel + "=" + election.LeaseKindValue,
	})
	if err != nil {
		r.log.WithError(err).Warn("election: list leases failed")
		return
	}
	for i := range leases.Items {
		delete(hashes, leases.Items[i].Annotations[election.HashAnnotation])
	}
	for hash := range hashes {
		log := r.log.WithField("hash", checkpointstore.ShortHash(hash))
		state, err := r.promoteState(hash)
		if err != nil {
			// Cannot tell; act on the next tick rather than guess.
			log.WithError(err).Warn("election: read promote state for orphaned followers failed")
			continue
		}
		switch state {
		case "ready":
			n, err := r.release(ctx, hash)
			if err != nil {
				log.WithError(err).Warn("election: release of orphaned followers failed")
				continue
			}
			log.WithField("released", n).Info("election: followers admitted after the release were released")
		case "failed", "":
			r.evictLogged(ctx, hash, "followers outlived their election", nil)
		default:
			log.WithField("state", state).Debug("election: orphaned followers wait for the promote in progress")
		}
	}
}

// leaderAlive finds the leader by its election id among the pods stamped
// with the hash in its namespace and reports whether it can still capture.
func (r *electionReleaser) leaderAlive(ctx context.Context, ns, id, hash string) bool {
	pods, err := r.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: election.HashLabel + "=" + checkpointstore.ShortHash(hash),
	})
	if err != nil {
		// Cannot tell; do not evict on a transient API error.
		r.log.WithError(err).Warn("election: list leader candidates failed")
		return true
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Annotations[election.ElectionIDAnnotation] != id {
			continue
		}
		return p.Status.Phase != corev1.PodFailed && p.Status.Phase != corev1.PodSucceeded && p.DeletionTimestamp == nil
	}
	return false
}
