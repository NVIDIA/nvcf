// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package modelvolume

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Reaper removes the PersistentVolume objects the model volume flow
// leaves behind once nothing references them. Every artifact here is a
// Retain PV, so deleting a read-only PV object never touches storage:
// the backing volume belongs to the primary PV. Only an abandoned
// primary (a copy that never completed) is deleted with its storage.
//
//   - Read-only PVs (one per reader namespace) whose claim is gone: the
//     PV is Released, or its namespace no longer exists. The next reader
//     in that namespace mints a fresh one from the primary.
//
//   - Primary PVs Released without the complete label: a copy died
//     between claim creation and completion. Older than AbandonAfter
//     they are switched to reclaim Delete and removed, freeing the
//     capacity. Completion labels the PV before releasing its claim,
//     so a complete primary is never in this state.
//
//   - Complete primaries (model or cache) are retired with their storage
//     once nothing has used them for Retention: no read-only PV of theirs
//     is bound in a live namespace and the last-used annotation (set at
//     completion and on every admission against the volume) is older
//     than Retention. Zero Retention keeps them forever.
type Reaper struct {
	Kube kubernetes.Interface
	Log  logrus.FieldLogger
	// AbandonAfter is the minimum age of an incomplete Released primary
	// before it is deleted. Zero means 15 minutes.
	AbandonAfter time.Duration
	// Retention is how long a complete primary is kept after its last
	// use. Zero means never retire.
	Retention time.Duration
	now       func() time.Time
}

// Result counts what one sweep removed.
type Result struct {
	ReadOnlyPVs        int
	AbandonedPrimaries int
	OrphanClaims       int
	RetiredPrimaries   int
}

// OrphanClaimAfter is how long a read-only claim may go without any pod
// in its namespace referencing it before it is deleted. Mount-holder pods
// are owned by the claim and go with it, so hostPath-mode attachments end
// when the last reader leaves.
const OrphanClaimAfter = 10 * time.Minute

// Sweep runs one pass. Errors on individual objects are logged and do
// not stop the pass; the returned error covers only the listing.
func (r *Reaper) Sweep(ctx context.Context) (Result, error) {
	res := Result{}
	log := r.log()
	if r.now == nil {
		r.now = time.Now
	}
	abandonAfter := r.AbandonAfter
	if abandonAfter == 0 {
		abandonAfter = 15 * time.Minute
	}
	pvs, err := r.Kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{LabelSelector: IdentityLabel})
	if err != nil {
		return res, fmt.Errorf("list model volumes: %w", err)
	}
	caches, err := r.Kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{LabelSelector: CacheLabel})
	if err != nil {
		return res, fmt.Errorf("list cache volumes: %w", err)
	}
	pvs.Items = append(pvs.Items, caches.Items...)
	// A PV deleted while a VolumeAttachment still references it stays
	// behind the attacher's finalizer and the detach never completes
	// (dev1 2026-09-29: readers gone, node not yet unstaged). Wait for the
	// detach; the next sweep gets it.
	vas, err := r.Kube.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list VolumeAttachments: %w", err)
	}
	attached := map[string]bool{}
	for i := range vas.Items {
		if src := vas.Items[i].Spec.Source.PersistentVolumeName; src != nil {
			attached[*src] = true
		}
	}
	namespaces := map[string]bool{}
	// Only a definite NotFound counts as gone: an RBAC or transport error
	// must never read as "namespace deleted" and take a live reader's PV.
	nsExists := func(ns string) bool {
		if v, ok := namespaces[ns]; ok {
			return v
		}
		_, gerr := r.Kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		exists := !apierrors.IsNotFound(gerr)
		if gerr != nil && exists {
			log.WithField("namespace", ns).WithError(gerr).Warn("model volume reaper: namespace lookup failed; keeping its volumes")
		}
		namespaces[ns] = exists
		return exists
	}
	// A primary with a read-only view bound in a namespace that still
	// exists has a live reader; retention never touches it. Views name
	// their primary (SourcePVLabel) so generations of one set are judged
	// apart; a view without the label keeps its whole identity alive.
	live := map[string]bool{}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		if isReadOnlyModelPV(pv) && pv.Status.Phase == corev1.VolumeBound && pv.Spec.ClaimRef != nil && nsExists(pv.Spec.ClaimRef.Namespace) {
			live[viewSourceKey(pv)] = true
		}
	}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		fields := logrus.Fields{"pv": pv.Name, "model": pv.Labels[IdentityLabel], "cache": pv.Labels[CacheLabel], "phase": pv.Status.Phase}
		switch {
		case isPrimaryModelPV(pv) && pv.Labels[CompleteLabel] == "true" && pv.Status.Phase == corev1.VolumeReleased && r.Retention > 0:
			last := lastUsed(pv)
			if r.now().Sub(last) < r.Retention || live[pv.Name] || live[identityKey(pv)] || attached[pv.Name] {
				continue
			}
			// Read-only views of this primary first: they are Retain PV
			// objects over the same storage and would be left dangling.
			for j := range pvs.Items {
				ro := &pvs.Items[j]
				if !isReadOnlyModelPV(ro) || !viewOf(ro, pv) || attached[ro.Name] {
					continue
				}
				if derr := r.deletePV(ctx, ro.Name); derr != nil {
					log.WithFields(fields).WithField("read_only_pv", ro.Name).WithError(derr).Warn("model volume reaper: delete read-only PV of retired primary failed")
				}
			}
			if derr := r.deleteWithStorage(ctx, pv); derr != nil {
				log.WithFields(fields).WithError(derr).Warn("model volume reaper: retire primary failed")
				continue
			}
			res.RetiredPrimaries++
			log.WithFields(fields).WithFields(logrus.Fields{"last_used": last.Format(time.RFC3339), "retention": r.Retention.String()}).Info("model volume reaper: retired primary unused past retention; storage freed")
		case isReadOnlyModelPV(pv):
			orphan := pv.Status.Phase == corev1.VolumeReleased
			if pv.Status.Phase == corev1.VolumeBound && pv.Spec.ClaimRef != nil && !nsExists(pv.Spec.ClaimRef.Namespace) {
				orphan = true
			}
			if !orphan {
				continue
			}
			if attached[pv.Name] {
				log.WithFields(fields).Info("model volume reaper: read-only PV still attached; waiting for detach")
				continue
			}
			if derr := r.deletePV(ctx, pv.Name); derr != nil {
				log.WithFields(fields).WithError(derr).Warn("model volume reaper: delete read-only PV failed")
				continue
			}
			res.ReadOnlyPVs++
			log.WithFields(fields).Info("model volume reaper: removed read-only PV without a claim")
		case isPrimaryModelPV(pv) && pv.Labels[CompleteLabel] != "true" && pv.Status.Phase == corev1.VolumeReleased:
			if r.now().Sub(pv.CreationTimestamp.Time) < abandonAfter || attached[pv.Name] {
				continue
			}
			if derr := r.deleteWithStorage(ctx, pv); derr != nil {
				log.WithFields(fields).WithError(derr).Warn("model volume reaper: delete abandoned primary failed")
				continue
			}
			res.AbandonedPrimaries++
			log.WithFields(fields).Info("model volume reaper: removed abandoned primary and its storage")
		}
	}
	res.OrphanClaims = r.reapOrphanClaims(ctx, log)
	// Primaries the provisioner created for a claim that was deleted
	// before completion carry no labels yet (labels arrive with
	// completion); find them by the claim name they were bound to.
	unlabelled, err := r.Kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list volumes: %w", err)
	}
	for i := range unlabelled.Items {
		pv := &unlabelled.Items[i]
		_, isModel := pv.Labels[IdentityLabel]
		_, isCache := pv.Labels[CacheLabel]
		if isModel || isCache || pv.Spec.ClaimRef == nil {
			continue
		}
		name := pv.Spec.ClaimRef.Name
		if (!strings.HasPrefix(name, "nvsnap-model-") && !strings.HasPrefix(name, "nvsnap-cache-")) || strings.HasSuffix(name, "-ro") || pv.Status.Phase != corev1.VolumeReleased {
			continue
		}
		if r.now().Sub(pv.CreationTimestamp.Time) < abandonAfter || attached[pv.Name] {
			continue
		}
		fields := logrus.Fields{"pv": pv.Name, "claim": pv.Spec.ClaimRef.Namespace + "/" + pv.Spec.ClaimRef.Name}
		if derr := r.deleteWithStorage(ctx, pv); derr != nil {
			log.WithFields(fields).WithError(derr).Warn("model volume reaper: delete abandoned primary failed")
			continue
		}
		res.AbandonedPrimaries++
		log.WithFields(fields).Info("model volume reaper: removed abandoned primary and its storage")
	}
	return res, nil
}

// reapOrphanClaims deletes read-only claims (model or cache) that no pod
// in their namespace references. PVC-mode claims normally die with their
// namespace; hostPath-mode claims and the stock-chart tests in the nvsnap
// namespace do not, and their mount-holders kept volumes attached for
// days (dev1 2026-09-29). Owner references take the holders down with
// the claim; the PV goes Released and the sweep above removes it after
// the detach.
func (r *Reaper) reapOrphanClaims(ctx context.Context, log logrus.FieldLogger) int {
	n := 0
	for _, sel := range []string{"nvsnap.io/role=reader," + IdentityLabel, "nvsnap.io/role=reader," + CacheLabel} {
		claims, err := r.Kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			log.WithError(err).Warn("model volume reaper: list read-only claims failed")
			continue
		}
		byNS := map[string]*corev1.PodList{}
		for i := range claims.Items {
			pvc := &claims.Items[i]
			if r.now().Sub(pvc.CreationTimestamp.Time) < OrphanClaimAfter {
				continue
			}
			pods, ok := byNS[pvc.Namespace]
			if !ok {
				pods, err = r.Kube.CoreV1().Pods(pvc.Namespace).List(ctx, metav1.ListOptions{})
				if err != nil {
					log.WithError(err).WithField("namespace", pvc.Namespace).Warn("model volume reaper: list pods failed; keeping claims")
					continue
				}
				byNS[pvc.Namespace] = pods
			}
			if claimReferenced(pods, pvc.Name) {
				continue
			}
			if err := r.Kube.CoreV1().PersistentVolumeClaims(pvc.Namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				log.WithError(err).WithField("claim", pvc.Namespace+"/"+pvc.Name).Warn("model volume reaper: delete orphan claim failed")
				continue
			}
			// The claim's own mount-holders keep it Terminating behind
			// pvc-protection, and owner-reference GC only fires once the
			// claim is gone: a deadlock unless the holders go first.
			for j := range pods.Items {
				h := &pods.Items[j]
				if h.Labels["app.kubernetes.io/component"] != "mount-holder" || !podMountsClaim(h, pvc.Name) {
					continue
				}
				if err := r.Kube.CoreV1().Pods(h.Namespace).Delete(ctx, h.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
					log.WithError(err).WithField("holder", h.Namespace+"/"+h.Name).Warn("model volume reaper: delete mount-holder failed")
				}
			}
			n++
			log.WithField("claim", pvc.Namespace+"/"+pvc.Name).Info("model volume reaper: removed read-only claim no pod references")
		}
	}
	return n
}

// claimReferenced reports whether any pod other than a mount-holder
// mounts the claim.
func claimReferenced(pods *corev1.PodList, claim string) bool {
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Labels["app.kubernetes.io/component"] == "mount-holder" {
			continue
		}
		if podMountsClaim(p, claim) {
			return true
		}
	}
	return false
}

func podMountsClaim(p *corev1.Pod, claim string) bool {
	for j := range p.Spec.Volumes {
		if pvc := p.Spec.Volumes[j].PersistentVolumeClaim; pvc != nil && pvc.ClaimName == claim {
			return true
		}
	}
	return false
}

// Run sweeps at interval until ctx is done. Every agent may run one; the
// deletes are idempotent and NotFound is not an error.
func (r *Reaper) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if res, err := r.Sweep(ctx); err != nil {
			r.log().WithError(err).Warn("model volume reaper: sweep failed")
		} else if res.ReadOnlyPVs+res.AbandonedPrimaries+res.OrphanClaims+res.RetiredPrimaries > 0 {
			r.log().WithFields(logrus.Fields{"read_only_pvs": res.ReadOnlyPVs, "abandoned_primaries": res.AbandonedPrimaries, "orphan_claims": res.OrphanClaims, "retired_primaries": res.RetiredPrimaries}).Info("model volume reaper: sweep done")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// identityKey is the volume's key in its label namespace, so a model and
// a cache with the same short hash never collide.
// viewSourceKey is what a bound read-only view keeps alive: its source
// primary by name when labelled, else its whole identity.
func viewSourceKey(ro *corev1.PersistentVolume) string {
	if src := ro.Labels[SourcePVLabel]; src != "" {
		return src
	}
	return identityKey(ro)
}

// viewOf reports whether ro is a read-only view of primary pv: by source
// label when present, else by identity (views minted before the label).
func viewOf(ro, pv *corev1.PersistentVolume) bool {
	if src := ro.Labels[SourcePVLabel]; src != "" {
		return src == pv.Name
	}
	return identityKey(ro) == identityKey(pv)
}

func identityKey(pv *corev1.PersistentVolume) string {
	if k := pv.Labels[CacheLabel]; k != "" {
		return "cache:" + k
	}
	return "model:" + pv.Labels[IdentityLabel]
}

// lastUsed is the last-used annotation, or the creation time for a
// primary that predates it.
func lastUsed(pv *corev1.PersistentVolume) time.Time {
	if v := pv.Annotations[LastUsedAnnotation]; v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return pv.CreationTimestamp.Time
}

func isReadOnlyModelPV(pv *corev1.PersistentVolume) bool {
	if pv.Labels["nvsnap.io/role"] == "reader-shared" {
		return true
	}
	return pv.Spec.CSI != nil && pv.Spec.CSI.ReadOnly
}

func isPrimaryModelPV(pv *corev1.PersistentVolume) bool {
	return !isReadOnlyModelPV(pv)
}

func (r *Reaper) deletePV(ctx context.Context, name string) error {
	if err := r.Kube.CoreV1().PersistentVolumes().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// deleteWithStorage flips the reclaim policy to Delete so the CSI driver
// frees the backing volume when the PV object goes.
func (r *Reaper) deleteWithStorage(ctx context.Context, pv *corev1.PersistentVolume) error {
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
		pv = pv.DeepCopy()
		pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		if _, err := r.Kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("set reclaim=Delete: %w", err)
		}
	}
	return r.deletePV(ctx, pv.Name)
}

func (r *Reaper) log() logrus.FieldLogger {
	if r.Log != nil {
		return r.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "modelvolume.reaper")
}
