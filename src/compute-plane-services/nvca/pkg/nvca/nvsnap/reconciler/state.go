/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package reconciler

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
)

// CFSResource is the GroupVersionResource the reconciler reads/writes
// on. Lives here so the reconciler and Hook A share one source of
// truth.
var CFSResource = schema.GroupVersionResource{
	Group:    nvsnapv1alpha1.SchemeGroupVersion.Group,
	Version:  nvsnapv1alpha1.SchemeGroupVersion.Version,
	Resource: "nvsnapfunctionstates",
}

// cfsStatus captures the fields the reconciler reads from /writes to
// status on NvSnapFunctionState. Using a struct rather than poking
// unstructured.NestedX directly at every call site keeps the state
// machine in reconciler.go readable.
type cfsStatus struct {
	CheckpointHash  string
	CapturedHere    bool
	LocalCacheState nvsnapv1alpha1.NvSnapFunctionStateLocalCacheState
	AttemptCount    int64
	LastError       string
	CapturedAt      *metav1.Time
	// LastAttemptAt is the wall-clock time of the most recent
	// CheckpointRequest the reconciler issued for this CFS — set on
	// both successful and failed terminal attempts. Used by the
	// reconciler's per-function backoff gate (nvca#167) to suppress
	// re-attempts on pod-ready events that arrive too quickly after
	// a previous failure. nil = never attempted.
	LastAttemptAt *metav1.Time
	// CaptureOwner / CaptureLeaseExpiry back the capture-once claim
	// (nvca#189). CaptureOwner is the namespace/name of the pod holding
	// an in-flight Capturing claim; CaptureLeaseExpiry bounds it for
	// crash recovery. Both zero-valued when not Capturing.
	CaptureOwner       string
	CaptureLeaseExpiry *metav1.Time
	// ColdStartPioneer / ColdStartPioneerExpiry back the serialized-herd
	// cold-start pioneer election. ColdStartPioneer is the namespace/name
	// of the ICMSRequest that won the right to cold-start + capture while
	// its peers defer; ColdStartPioneerExpiry bounds it for crash
	// recovery. Both zero-valued when no pioneer is elected. Analogous to
	// CaptureOwner/CaptureLeaseExpiry but claimed one step earlier (at
	// MiniService creation, not at capture).
	ColdStartPioneer       string
	ColdStartPioneerExpiry *metav1.Time
}

// getOrCreateCFS reads NvSnapFunctionState/<fvID>. If missing, creates
// an empty one (cold state) and returns it. The reconciler needs the
// object to exist so it can update status; this is the standard
// "controller materializes its CR on first touch" pattern.
func getOrCreateCFS(ctx context.Context, dc dynamic.Interface, fvID string) (*unstructured.Unstructured, error) {
	cfs, err := dc.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if err == nil {
		return cfs, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	// Create with minimal spec; Hook B will populate status as it
	// progresses through Cold → Fetching → Warm.
	fresh := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
			"kind":       "NvSnapFunctionState",
			"metadata":   map[string]any{"name": fvID},
			"spec": map[string]any{
				"functionVersionID": fvID,
			},
			"status": map[string]any{
				"localCacheState": string(nvsnapv1alpha1.LocalCacheStateCold),
			},
		},
	}
	created, err := dc.Resource(CFSResource).Create(ctx, fresh, metav1.CreateOptions{})
	if err != nil {
		// If somebody else created it between our Get and Create
		// (race with a parallel reconciler), re-read instead of
		// failing — the state we want exists, just not from us.
		if apierrors.IsAlreadyExists(err) {
			return dc.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
		}
		return nil, fmt.Errorf("create NvSnapFunctionState %s: %w", fvID, err)
	}
	return created, nil
}

// readStatus extracts the typed status fields from the unstructured
// object. Unset fields return their zero value; the reconciler
// already treats those correctly (empty hash = no checkpoint yet,
// empty state = Cold, etc.).
func readStatus(cfs *unstructured.Unstructured) cfsStatus {
	var out cfsStatus
	if cfs == nil {
		return out
	}
	out.CheckpointHash, _, _ = unstructured.NestedString(cfs.Object, "status", "checkpointHash")
	out.CapturedHere, _, _ = unstructured.NestedBool(cfs.Object, "status", "capturedHere")
	stateStr, _, _ := unstructured.NestedString(cfs.Object, "status", "localCacheState")
	out.LocalCacheState = nvsnapv1alpha1.NvSnapFunctionStateLocalCacheState(stateStr)
	out.AttemptCount, _, _ = unstructured.NestedInt64(cfs.Object, "status", "attemptCount")
	out.LastError, _, _ = unstructured.NestedString(cfs.Object, "status", "lastError")
	if rfc, found, _ := unstructured.NestedString(cfs.Object, "status", "capturedAt"); found {
		if t, err := time.Parse(time.RFC3339, rfc); err == nil {
			mt := metav1.NewTime(t)
			out.CapturedAt = &mt
		}
	}
	if rfc, found, _ := unstructured.NestedString(cfs.Object, "status", "lastAttemptAt"); found {
		if t, err := time.Parse(time.RFC3339, rfc); err == nil {
			mt := metav1.NewTime(t)
			out.LastAttemptAt = &mt
		}
	}
	out.CaptureOwner, _, _ = unstructured.NestedString(cfs.Object, "status", "captureOwner")
	if rfc, found, _ := unstructured.NestedString(cfs.Object, "status", "captureLeaseExpiry"); found {
		if t, err := time.Parse(time.RFC3339, rfc); err == nil {
			mt := metav1.NewTime(t)
			out.CaptureLeaseExpiry = &mt
		}
	}
	out.ColdStartPioneer, _, _ = unstructured.NestedString(cfs.Object, "status", "coldStartPioneer")
	if rfc, found, _ := unstructured.NestedString(cfs.Object, "status", "coldStartPioneerExpiry"); found {
		if t, err := time.Parse(time.RFC3339, rfc); err == nil {
			mt := metav1.NewTime(t)
			out.ColdStartPioneerExpiry = &mt
		}
	}
	return out
}

// optedOut returns true iff spec.optOut is set on the CR.
func optedOut(cfs *unstructured.Unstructured) bool {
	if cfs == nil {
		return false
	}
	v, _, _ := unstructured.NestedBool(cfs.Object, "spec", "optOut")
	return v
}

// readWorkloadLookup extracts spec.workloadLookup. Empty imageRef means
// it was never persisted (the CFS predates this writer, or Hook B died
// before reaching the persist step) — the sweep skips such CFS.
func readWorkloadLookup(cfs *unstructured.Unstructured) (imageRef, modelID string) {
	if cfs == nil {
		return "", ""
	}
	imageRef, _, _ = unstructured.NestedString(cfs.Object, "spec", "workloadLookup", "imageRef")
	modelID, _, _ = unstructured.NestedString(cfs.Object, "spec", "workloadLookup", "modelId")
	return imageRef, modelID
}

// writeWorkloadLookup persists spec.workloadLookup so the controller's
// CFS recovery sweep can look up an existing capture without a live pod
// (nvca#104 durable-warm). No-op when imageRef is empty or the value is
// already current — avoids a needless write on every reconcile. Writes
// the spec subtree via Update (not UpdateStatus); only the
// workloadLookup keys are touched, so a concurrent optOut toggle on a
// different key survives the read-modify-write.
func writeWorkloadLookup(ctx context.Context, dc dynamic.Interface, fvID, imageRef, modelID string) error {
	if imageRef == "" {
		return nil
	}
	cur, err := dc.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	curImage, _, _ := unstructured.NestedString(cur.Object, "spec", "workloadLookup", "imageRef")
	curModel, _, _ := unstructured.NestedString(cur.Object, "spec", "workloadLookup", "modelId")
	if curImage == imageRef && curModel == modelID {
		return nil // already current
	}
	lookup := map[string]any{"imageRef": imageRef}
	if modelID != "" {
		lookup["modelId"] = modelID
	}
	if err := unstructured.SetNestedMap(cur.Object, lookup, "spec", "workloadLookup"); err != nil {
		return fmt.Errorf("set spec.workloadLookup: %w", err)
	}
	if _, err := dc.Resource(CFSResource).Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update NvSnapFunctionState %s spec: %w", fvID, err)
	}
	return nil
}

// statusUpdate is what writeStatus marshals onto the CR. Only the
// fields the reconciler actually writes — leaves everything else
// (Conditions, LastAttemptAt) untouched so future writers can extend.
type statusUpdate struct {
	CheckpointHash  string
	CapturedHere    bool
	CapturedAt      time.Time // zero = don't set
	LocalCacheState nvsnapv1alpha1.NvSnapFunctionStateLocalCacheState
	AttemptCount    int64
	LastError       string
	LastAttemptAt   time.Time // zero = don't set
}

// writeStatus patches the status subresource via the dynamic client's
// UpdateStatus. NvSnapFunctionState exposes /status (kubebuilder marker
// status:status added in a follow-up CRD manifest PR); until that
// lands, Update is the only path.
//
// Known TOCTOU on the Update fallback (Greptile P2 on nvca!1748): if
// the operator toggles spec.optOut between our Get and our Update,
// our Update will overwrite spec back to the pre-toggle value. The
// fallback is only taken when the CRD doesn't have its /status
// subresource registered yet — once the follow-up CRD-manifest PR
// lands (registering subresource:status), UpdateStatus succeeds and
// only the /status subtree is written, eliminating the race.
//
// We can't conditionally skip the fallback today because the
// in-tree CRD manifest used in tests + nvsnap-h100-a doesn't have
// /status, and the alternative — failing every status write —
// would break Hook B entirely. Tracking removal: nvca-nvsnap
// status-subresource-registration follow-up.
func writeStatus(ctx context.Context, dc dynamic.Interface, fvID string, upd statusUpdate) error {
	cur, err := dc.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	// Read the existing status map and patch only the keys we manage.
	// SetNestedField(...,"status") would otherwise replace the whole
	// subtree, silently wiping unmanaged keys like `conditions` that
	// other controllers (or future versions of this one) may write.
	status, _, _ := unstructured.NestedMap(cur.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["checkpointHash"] = upd.CheckpointHash
	status["capturedHere"] = upd.CapturedHere
	status["localCacheState"] = string(upd.LocalCacheState)
	status["attemptCount"] = upd.AttemptCount
	status["lastError"] = upd.LastError
	if !upd.CapturedAt.IsZero() {
		status["capturedAt"] = upd.CapturedAt.UTC().Format(time.RFC3339)
	}
	if !upd.LastAttemptAt.IsZero() {
		status["lastAttemptAt"] = upd.LastAttemptAt.UTC().Format(time.RFC3339)
	}
	// Release the capture-once claim (nvca#189). writeStatus is only
	// called on terminal transitions (Warm on success, or back to the
	// prior state on failure) — in both cases the in-flight capture is
	// over, so the Capturing claim must be dropped. Leaving these set
	// would either pin the function until lease expiry or let readStatus
	// mis-report a stale owner.
	delete(status, "captureOwner")
	delete(status, "captureLeaseExpiry")
	// Release the cold-start pioneer claim too (serialized-herd). A
	// terminal status write means the pioneer's cold-start + capture is
	// over: on success the function is now Warm (deferred replicas
	// proceed and warm-restore), on failure it falls back to a
	// non-Warm state where a fresh pioneer should be electable. Either
	// way the slot must be vacated so it doesn't linger past its purpose.
	delete(status, "coldStartPioneer")
	delete(status, "coldStartPioneerExpiry")
	if err := unstructured.SetNestedField(cur.Object, status, "status"); err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	if _, err := dc.Resource(CFSResource).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		// Some test envs (and CRDs without status subresource) reject
		// UpdateStatus — fall back to Update for those.
		if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
			if _, err := dc.Resource(CFSResource).Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update NvSnapFunctionState %s: %w", fvID, err)
			}
			return nil
		}
		return fmt.Errorf("update status %s: %w", fvID, err)
	}
	return nil
}

// tryClaimCapture is the capture-once / thundering-herd guard
// (nvca#189). It atomically transitions a function-version's CFS to
// LocalCacheStateCapturing — claiming the right to run the single
// in-flight checkpoint — using Kubernetes optimistic concurrency: the
// object Get carries a resourceVersion, and the subsequent Update is
// admitted by the API server only if no one else mutated the object in
// between. When N pods of the same function-version reconcile cold at
// once, exactly one Update succeeds; the rest get a 409 Conflict and
// observe claimed=false. No double-capture.
//
// Claimability (evaluated against the freshly-read object):
//   - Warm / Fetching: NOT claimable — capture is unnecessary (Warm is
//     handled by the reconciler's already-Warm gate before we get here;
//     Fetching means cross-cluster replication owns the bytes).
//   - Capturing held by ANOTHER owner with an unexpired lease: NOT
//     claimable — that pod is mid-capture; back off.
//   - everything else (Cold / Failed / empty / Capturing with an
//     EXPIRED lease / Capturing already owned by us): claimable. The
//     expired-lease case lets a fresh pod steal a crashed capturer's
//     claim; the own-owner case makes a re-reconcile of the capturing
//     pod re-entrant and refreshes (heartbeats) its lease.
//
// Returns (true, nil) iff this caller now owns the claim. (false, nil)
// means another caller owns it (lost the race or unexpired foreign
// claim) — a non-error, expected outcome under concurrency. A non-nil
// error is a real API failure the caller should surface/requeue on.
//
// owner is the claiming pod's namespace/name; leaseExpiry is the
// wall-clock deadline to stamp; now is injected for deterministic
// tests.
func tryClaimCapture(ctx context.Context, dc dynamic.Interface, fvID, owner string, leaseExpiry, now time.Time) (bool, error) {
	cur, err := dc.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	st := readStatus(cur)

	switch st.LocalCacheState {
	case nvsnapv1alpha1.LocalCacheStateWarm, nvsnapv1alpha1.LocalCacheStateFetching:
		return false, nil
	case nvsnapv1alpha1.LocalCacheStateCapturing:
		leaseLive := st.CaptureLeaseExpiry != nil && now.Before(st.CaptureLeaseExpiry.Time)
		if leaseLive && st.CaptureOwner != owner {
			return false, nil // another pod holds a live claim
		}
		// expired lease (steal) or our own claim (re-entrant) → fall through
	}

	// Patch only the keys we manage; preserve checkpointHash/attemptCount/etc.
	status, _, _ := unstructured.NestedMap(cur.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["localCacheState"] = string(nvsnapv1alpha1.LocalCacheStateCapturing)
	status["captureOwner"] = owner
	status["captureLeaseExpiry"] = leaseExpiry.UTC().Format(time.RFC3339)
	if err := unstructured.SetNestedField(cur.Object, status, "status"); err != nil {
		return false, fmt.Errorf("set status: %w", err)
	}

	// Update carries cur's resourceVersion → optimistic concurrency.
	// A concurrent winner bumps the resourceVersion, so our Update
	// returns Conflict — that's the lost-race signal, not an error.
	if _, err := dc.Resource(CFSResource).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
			if _, err := dc.Resource(CFSResource).Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
				if apierrors.IsConflict(err) {
					return false, nil
				}
				return false, fmt.Errorf("claim update NvSnapFunctionState %s: %w", fvID, err)
			}
			return true, nil
		}
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, fmt.Errorf("claim updateStatus %s: %w", fvID, err)
	}
	return true, nil
}

// TryClaimColdStartPioneer is the serialized-herd cold-start guard
// (pioneer election). It is the exact analogue of tryClaimCapture
// (nvca#189) applied one step earlier — at MiniService creation rather
// than at capture: when N replicas of one function-version deploy cold
// (no Warm checkpoint yet), exactly one reconcile is allowed to claim
// the "pioneer" slot and cold-start; the others observe a live foreign
// claim and back off (the gate in pkg/nvca defers their MiniService
// creation). Once the pioneer's capture flips LocalCacheState=Warm the
// deferred replicas proceed and warm-restore via Hook A.
//
// Exported so the MiniService-creation gate (package nvca) can call it;
// the gate can't reach the unexported tryClaimCapture but the
// compare-and-swap mechanics are identical (the API server's optimistic
// concurrency on UpdateStatus elects exactly one winner under a herd).
//
// Claimability (evaluated against the freshly-read object):
//   - Warm / Failed: NOT claimable, returns (false, nil). The caller
//     treats both as "proceed" (Warm => warm-restore; Failed =>
//     fail-open cold) — not as a pioneer claim. Keeping the policy in
//     the caller (rather than claiming here) lets the gate log the two
//     cases distinctly.
//   - ColdStartPioneer held by ANOTHER owner with an unexpired lease:
//     NOT claimable — that replica is the pioneer; defer.
//   - everything else (no claim / our own claim / expired lease /
//     Cold / Fetching / Capturing / absent-status): claimable. The
//     expired-lease case lets a fresh replica steal a crashed pioneer's
//     slot so the function isn't deferred forever; the own-owner case
//     makes a re-reconcile of the pioneer re-entrant (lease refresh).
//
// STRICT single pioneer (K=1) for v1.
// TODO(nvsnap): K>1 multi-pioneer slots — the MaxConcurrentColdStarts
// config knob exists but v1 honors K=1 only; multi-slot election needs
// a slot-count field on status, not a single owner string.
//
// Returns (true, nil) iff this caller now owns the pioneer slot.
// (false, nil) means proceed-or-defer per the cases above — a non-error
// outcome. A non-nil error is a real API failure to surface/requeue on.
//
// owner is the claiming ICMSRequest's namespace/name; leaseExpiry is the
// wall-clock deadline to stamp; now is injected for deterministic tests.
func TryClaimColdStartPioneer(ctx context.Context, dc dynamic.Interface, fvID, owner string, leaseExpiry, now time.Time) (bool, error) {
	cur, err := dc.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	st := readStatus(cur)

	switch st.LocalCacheState {
	case nvsnapv1alpha1.LocalCacheStateWarm, nvsnapv1alpha1.LocalCacheStateFailed:
		// Caller proceeds (warm-restore or fail-open cold) — not a claim.
		return false, nil
	}

	leaseLive := st.ColdStartPioneerExpiry != nil && now.Before(st.ColdStartPioneerExpiry.Time)
	if leaseLive && st.ColdStartPioneer != "" && st.ColdStartPioneer != owner {
		return false, nil // another replica is the pioneer
	}
	// no claim / our own claim / expired lease → fall through and claim.

	// Patch only the keys we manage; preserve every other status field.
	status, _, _ := unstructured.NestedMap(cur.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["coldStartPioneer"] = owner
	status["coldStartPioneerExpiry"] = leaseExpiry.UTC().Format(time.RFC3339)
	if err := unstructured.SetNestedField(cur.Object, status, "status"); err != nil {
		return false, fmt.Errorf("set status: %w", err)
	}

	// Update carries cur's resourceVersion → optimistic concurrency. A
	// concurrent winner bumps the resourceVersion, so our Update returns
	// Conflict — the lost-race signal, not an error.
	if _, err := dc.Resource(CFSResource).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
			if _, err := dc.Resource(CFSResource).Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
				if apierrors.IsConflict(err) {
					return false, nil
				}
				return false, fmt.Errorf("pioneer claim update NvSnapFunctionState %s: %w", fvID, err)
			}
			return true, nil
		}
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, fmt.Errorf("pioneer claim updateStatus %s: %w", fvID, err)
	}
	return true, nil
}
