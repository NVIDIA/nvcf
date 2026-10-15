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
	"errors"
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
	// CaptureOwnerUID is the UID of the CaptureOwner pod. Inference pods
	// have deterministic names (request name plus instance index), so a
	// replacement pod can carry the same namespace/name as a dead
	// claimant; the UID tells them apart.
	CaptureOwnerUID string
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
	out.CaptureOwnerUID, _, _ = unstructured.NestedString(cfs.Object, "status", "captureOwnerUID")
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
// claimToken identifies the reconcile that holds the capture claim: the
// owner pod's namespace/name and UID. Terminal status writes carry it so
// a reconcile whose claim was taken over (its pod began terminating and
// a peer stole the claim while it was still polling) cannot release or
// overwrite the new owner's claim. The zero token writes unfenced, for
// paths that never held a claim (recovery before claiming, the sweep).
type claimToken struct {
	Owner string
	UID   string
	// ExpectUnclaimed fences a write from a path that holds no claim but
	// observed none on the object (the sweep): the write is rejected if a
	// claim appeared in between, so a fresh capture is never released by
	// a recovery that read stale state.
	ExpectUnclaimed bool
	// ObservedResourceVersion, when set, rejects the write if the object
	// changed at all since the writer observed it. Owner and unclaimed
	// checks compare states and cannot see a claim that opened and closed
	// in between, or a lease the same pod refreshed; the version can.
	ObservedResourceVersion string
}

func (t claimToken) empty() bool {
	return t.Owner == "" && !t.ExpectUnclaimed && t.ObservedResourceVersion == ""
}

// ErrClaimSuperseded is returned by writeStatus when the caller's claim
// token no longer matches the claim on the object: another reconcile owns
// the capture now and this one must not touch the status.
var ErrClaimSuperseded = errors.New("capture claim superseded by another owner")

// supersedes reports whether the claim recorded on the object belongs to
// someone other than token. An object with no claim never supersedes.
func (st cfsStatus) supersedes(token claimToken) bool {
	if token.empty() {
		return false
	}
	if token.ExpectUnclaimed {
		return st.CaptureOwner != ""
	}
	if token.Owner == "" {
		return false // version-only fence, checked by the caller
	}
	if st.CaptureOwner == "" {
		// The writer's claim is gone: another terminal write (a recovery
		// or a takeover that finished) already closed this capture, so
		// the writer's outcome must not replace it.
		return true
	}
	if st.CaptureOwner != token.Owner {
		return true
	}
	return st.CaptureOwnerUID != "" && token.UID != "" && st.CaptureOwnerUID != token.UID
}

func writeStatus(ctx context.Context, dc dynamic.Interface, fvID string, upd statusUpdate, token claimToken) error {
	cur, err := dc.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	if token.ObservedResourceVersion != "" && cur.GetResourceVersion() != token.ObservedResourceVersion {
		return ErrClaimSuperseded
	}
	if readStatus(cur).supersedes(token) {
		return ErrClaimSuperseded
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
	delete(status, "captureOwnerUID")
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
	return tryClaimCaptureLive(ctx, dc, fvID, owner, "", leaseExpiry, now, nil)
}

// OwnerAliveFunc reports whether the pod named "namespace/name" that holds a
// capture claim still exists and is not terminating. nil means "unknown",
// which keeps the claim.
type OwnerAliveFunc func(ctx context.Context, owner, ownerUID string) bool

// tryClaimCaptureLive is tryClaimCapture with a liveness check on the
// current owner. A live lease normally protects an in-flight capture from a
// second pioneer, but the lease outlives its owner: a pod that dies mid
// capture (evicted, replaced by a redeploy, its node agent restarted under
// it) leaves the version Capturing with nobody working on it until the
// lease expires, roughly 50 minutes. If the owner is gone, the claim is
// stealable at once.
func tryClaimCaptureLive(ctx context.Context, dc dynamic.Interface, fvID, owner, ownerUID string, leaseExpiry, now time.Time, ownerAlive OwnerAliveFunc) (bool, error) {
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
		// The same namespace/name with a different UID is a replacement
		// pod, not the claimant: treat it as foreign.
		sameOwner := st.CaptureOwner == owner && (st.CaptureOwnerUID == "" || ownerUID == "" || st.CaptureOwnerUID == ownerUID)
		if leaseLive && !sameOwner {
			if ownerAlive == nil || ownerAlive(ctx, st.CaptureOwner, st.CaptureOwnerUID) {
				return false, nil // another pod holds a live claim
			}
			// The owner is gone: nothing will finish or release this
			// claim. Steal it now rather than at lease expiry.
		}
		// expired lease, dead owner (steal) or our own claim (re-entrant) → fall through
	}

	// Patch only the keys we manage; preserve checkpointHash/attemptCount/etc.
	status, _, _ := unstructured.NestedMap(cur.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["localCacheState"] = string(nvsnapv1alpha1.LocalCacheStateCapturing)
	status["captureOwner"] = owner
	if ownerUID != "" {
		status["captureOwnerUID"] = ownerUID
	} else {
		delete(status, "captureOwnerUID")
	}
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

// releaseCaptureClaim drops the Capturing claim without recording a failure,
// restoring the status this reconcile observed before it claimed.
//
// Reconcile stamps LocalCacheState=Capturing + captureOwner +
// captureLeaseExpiry when it wins the claim, and writeStatus is the only code
// that clears those. Any path that gives up AFTER claiming and returns nil has
// to come through here, or the CFS stays Capturing under a pod that is no
// longer capturing and every peer pod of the function version is gated at the
// claim until the ~50 min lease expires (nvca#208 review).
//
// Distinct from recordFailure on purpose: this does not set LastError or
// increment AttemptCount, so a path whose contract is "treat it as if the
// capture never happened" leaves no CFS-level error surface behind.
func releaseCaptureClaim(ctx context.Context, dc dynamic.Interface, fvID string, prev cfsStatus, token claimToken) error {
	upd := statusUpdate{
		CheckpointHash:  prev.CheckpointHash,
		CapturedHere:    prev.CapturedHere,
		LocalCacheState: prev.LocalCacheState,
		AttemptCount:    prev.AttemptCount,
		LastError:       prev.LastError,
	}
	if prev.CapturedAt != nil {
		upd.CapturedAt = prev.CapturedAt.Time
	}
	if prev.LastAttemptAt != nil {
		upd.LastAttemptAt = prev.LastAttemptAt.Time
	}
	return writeStatus(ctx, dc, fvID, upd, token)
}
