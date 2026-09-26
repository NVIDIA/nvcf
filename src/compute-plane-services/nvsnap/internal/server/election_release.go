// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

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
}

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
// recreate, and the Lease, so the recreated pods run a fresh election. A
// follower's volumes name a claim that will now never bind and pod volumes
// are immutable, so recreation is the only way to change its fate. Pods
// without a controller owner are left in place and logged.
func (r *electionReleaser) evict(ctx context.Context, hash, reason string) (evicted int, err error) {
	pods, err := r.listGated(ctx, hash)
	if err != nil {
		return 0, err
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
	r.deleteLease(ctx, hash)
	r.log.WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "evicted": evicted, "reason": reason}).
		Info("election: leader gone; followers evicted for re-election")
	return evicted, nil
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
		n, err := r.evict(ctx, hash, "promote failed")
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
			_, _ = r.evict(ctx, hash, "deadline passed")
			continue
		}
		if !r.leaderAlive(ctx, l.Annotations[election.LeaderNamespaceAnnotation], l.Annotations[election.LeaderIDAnnotation], hash) {
			_, _ = r.evict(ctx, hash, "leader pod gone or terminated")
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
