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
//   - Primary PVs Released without the complete label: a copy died
//     between claim creation and completion. Older than AbandonAfter
//     they are switched to reclaim Delete and removed, freeing the
//     capacity. Completion labels the PV before releasing its claim,
//     so a complete primary is never in this state.
//
// Complete primaries are kept: retention policy is a separate decision.
type Reaper struct {
	Kube kubernetes.Interface
	Log  logrus.FieldLogger
	// AbandonAfter is the minimum age of an incomplete Released primary
	// before it is deleted. Zero means 15 minutes.
	AbandonAfter time.Duration
	now          func() time.Time
}

// Result counts what one sweep removed.
type Result struct {
	ReadOnlyPVs        int
	AbandonedPrimaries int
}

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
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		fields := logrus.Fields{"pv": pv.Name, "model": pv.Labels[IdentityLabel], "phase": pv.Status.Phase}
		switch {
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
	// Primaries the provisioner created for a claim that was deleted
	// before completion carry no labels yet (labels arrive with
	// completion); find them by the claim name they were bound to.
	unlabelled, err := r.Kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list volumes: %w", err)
	}
	for i := range unlabelled.Items {
		pv := &unlabelled.Items[i]
		if _, labelled := pv.Labels[IdentityLabel]; labelled || pv.Spec.ClaimRef == nil {
			continue
		}
		if !strings.HasPrefix(pv.Spec.ClaimRef.Name, "nvsnap-model-") || strings.HasSuffix(pv.Spec.ClaimRef.Name, "-ro") || pv.Status.Phase != corev1.VolumeReleased {
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
		} else if res.ReadOnlyPVs+res.AbandonedPrimaries > 0 {
			r.log().WithFields(logrus.Fields{"read_only_pvs": res.ReadOnlyPVs, "abandoned_primaries": res.AbandonedPrimaries}).Info("model volume reaper: sweep done")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
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
