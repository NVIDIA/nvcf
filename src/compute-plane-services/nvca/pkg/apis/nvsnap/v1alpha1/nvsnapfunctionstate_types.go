/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NvSnapFunctionStateLocalCacheState enumerates the per-cluster cache
// states for a checkpoint hash. Reflects whether the bytes are
// reachable from this cluster's local nvsnap-blobstore / per-node
// hostPath caches, independent of whether the global hash exists.
type NvSnapFunctionStateLocalCacheState string

const (
	// LocalCacheStateCold = no copy on this cluster. Restore will
	// cold-start until the cascade fetches from the source cluster
	// (cross-cluster replication via S3, when that lands).
	LocalCacheStateCold NvSnapFunctionStateLocalCacheState = "Cold"

	// LocalCacheStateFetching = replication is in progress. NVCA
	// won't stamp nvsnap.io/restore-from while in this state because
	// the bytes might not be local yet by pod-create time.
	LocalCacheStateFetching NvSnapFunctionStateLocalCacheState = "Fetching"

	// LocalCacheStateCapturing = a checkpoint capture is in flight for
	// this function-version on this cluster. Set via an optimistic-
	// concurrency compare-and-swap (Cold→Capturing) before Hook B
	// issues the checkpoint POST, so that when N pods of the same
	// function-version admit cold simultaneously, exactly one reconcile
	// wins the transition and runs the capture; the others observe
	// Capturing and back off rather than firing N parallel captures
	// (nvca#189 capture-once / thundering-herd guard). Bounded by
	// status.CaptureOwner + status.CaptureLeaseExpiry so a crashed
	// capturer's claim expires and a fresh pod can retry rather than
	// the function being pinned Capturing forever.
	LocalCacheStateCapturing NvSnapFunctionStateLocalCacheState = "Capturing"

	// LocalCacheStateWarm = bytes are local (either captured here or
	// fetched from another cluster). Restore-on-create can proceed.
	LocalCacheStateWarm NvSnapFunctionStateLocalCacheState = "Warm"

	// LocalCacheStateFailed = last fetch attempt failed; see
	// status.LastError. Reconciler will retry with backoff.
	LocalCacheStateFailed NvSnapFunctionStateLocalCacheState = "Failed"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NvSnapFunctionState records per-cluster NvSnap state for one NGC
// function-version. Cluster-scoped: the global hash + checkpoint
// existence live in NGC; this CR holds operational state that's
// per-cluster (which cluster captured it, local cache state, retry
// history, opt-outs).
//
// One NvSnapFunctionState per function-version, named after the
// function-version UUID — keeps NGC's canonical id the lookup key.
//
// Lifecycle:
//
//	NVCA pod-creation sees a deploy for function-version V.
//	  → Reads NvSnapFunctionState/V from this cluster's API server.
//	  → If !optOut && status.LocalCacheState == Warm && status.CheckpointHash != "",
//	    stamps nvsnap.io/restore-from = checkpointHash on the pod.
//	  → Otherwise pod cold-starts (existing behavior).
//
//	First pod for V reaches /health 200 + buffer.
//	  → Hook B reconciler POSTs nvsnap-server /api/v1/checkpoints.
//	  → On terminal Completed: updates status.CheckpointHash,
//	    status.CapturedHere=true, status.LocalCacheState=Warm,
//	    writes hash to NGC.
type NvSnapFunctionState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NvSnapFunctionStateSpec   `json:"spec,omitempty"`
	Status NvSnapFunctionStateStatus `json:"status,omitempty"`
}

// +k8s:openapi-gen=true
type NvSnapFunctionStateSpec struct {
	// FunctionVersionID is the NGC function-version UUID this CR
	// tracks. Matches metadata.name; required for callers that read
	// the spec without parsing the name.
	FunctionVersionID string `json:"functionVersionID"`

	// OptOut excludes this function-version from NvSnap checkpoint AND
	// restore on this cluster. Useful for workloads with state that
	// can't be safely restored (e.g., engines that bake host IDs into
	// memory). Operators set this on a per-cluster basis; NGC may
	// expose a global opt-out separately.
	OptOut bool `json:"optOut,omitempty"`

	// WarmupTimeoutSecondsOverride is an optional per-function-version
	// override of NvSnapConfig.WarmupTimeoutSeconds. Useful when a
	// specific workload takes much longer to warm than the cluster
	// default (e.g., a custom TRT-LLM build with long engine compile).
	// Zero = use cluster default.
	WarmupTimeoutSecondsOverride int32 `json:"warmupTimeoutSecondsOverride,omitempty"`

	// WorkloadLookup carries the content-addressed lookup inputs for
	// this function-version, persisted by Hook B at capture time. It
	// lets the controller's periodic CFS recovery sweep find an
	// existing capture in nvsnap-server and flip LocalCacheState=Warm
	// WITHOUT a live source pod — closing the gap where an interrupted
	// reconcile (controller restart, lost goroutine, source pod scaled
	// away) leaves CFS stuck not-Warm forever (nvca#104 durable-warm,
	// see docs/users/nvsnap/DURABLE-WARM-SWEEP.md). Empty ImageRef = not
	// yet persisted; the sweep skips such CFS.
	WorkloadLookup WorkloadLookupSpec `json:"workloadLookup,omitempty"`
}

// WorkloadLookupSpec is the subset of a pod's workload identity the
// controller needs to query nvsnap-server's content-addressed lookup
// (POST /api/v1/checkpoints/lookup) without holding the pod. Mirrors
// the inputs nvsnap_hook.go and recover.go derive from the inference
// container.
//
// +k8s:openapi-gen=true
type WorkloadLookupSpec struct {
	// ImageRef is the inference container's image string.
	ImageRef string `json:"imageRef,omitempty"`
	// ModelID is the model identifier pulled from the container env or
	// --model args (empty = match any model for the image).
	ModelID string `json:"modelId,omitempty"`
}

// +k8s:openapi-gen=true
type NvSnapFunctionStateStatus struct {
	// CheckpointHash is the content-addressed sha256 of the captured
	// state, mirrored from NGC for fast local reads. Empty until
	// the first successful checkpoint anywhere in the fleet
	// (the canonical source is NGC; this is the local cache).
	CheckpointHash string `json:"checkpointHash,omitempty"`

	// CapturedHere is true iff THIS cluster ran the checkpoint that
	// produced CheckpointHash. False = the hash was captured on
	// another cluster and replicated here (or the bytes aren't here
	// yet).
	CapturedHere bool `json:"capturedHere,omitempty"`

	// CapturedAt is the time the checkpoint was committed. RFC3339.
	CapturedAt *metav1.Time `json:"capturedAt,omitempty"`

	// LocalCacheState is the readiness of the checkpoint bytes on
	// this cluster. See LocalCacheState* constants.
	LocalCacheState NvSnapFunctionStateLocalCacheState `json:"localCacheState,omitempty"`

	// LastAttemptAt is the time of the most recent checkpoint
	// attempt (regardless of outcome).
	LastAttemptAt *metav1.Time `json:"lastAttemptAt,omitempty"`

	// AttemptCount is the cumulative number of checkpoint attempts
	// this CR has triggered (resets on successful Completed).
	AttemptCount int32 `json:"attemptCount,omitempty"`

	// LastError is the error message from the most recent failed
	// checkpoint attempt. Cleared on success.
	LastError string `json:"lastError,omitempty"`

	// CaptureOwner is the namespace/name of the pod whose reconcile won
	// the Cold→Capturing compare-and-swap and is running the in-flight
	// capture (nvca#189 capture-once). Set alongside
	// LocalCacheState=Capturing; cleared on any terminal write (Warm on
	// success, or back to the prior state on failure). Lets a
	// re-reconcile of the SAME owning pod proceed re-entrantly and lets
	// observers attribute the in-flight claim.
	CaptureOwner string `json:"captureOwner,omitempty"`

	// CaptureLeaseExpiry bounds how long a Capturing claim is honored.
	// If the owning reconcile dies mid-capture (controller restart,
	// lost leader election, ctx cancel) the claim would otherwise pin
	// the function Capturing forever; once now > CaptureLeaseExpiry any
	// pod may steal the claim and retry. Set to now + a TTL that
	// provably exceeds the maximum legitimate capture duration
	// (warmup buffer + checkpoint timeout + L2 promote timeout +
	// margin) so a valid in-flight capture is never stolen. RFC3339.
	CaptureLeaseExpiry *metav1.Time `json:"captureLeaseExpiry,omitempty"`

	// ColdStartPioneer is the namespace/name of the ICMSRequest whose
	// MiniService-creation reconcile won the cold-start pioneer election
	// (serialized-herd cold start). It backs the same compare-and-swap
	// idea as CaptureOwner but applied one step earlier — at MiniService
	// creation rather than at capture: when N replicas of one
	// function-version deploy cold (no Warm checkpoint yet), exactly one
	// "pioneer" is allowed to create its MiniService and cold-start; the
	// others defer (requeue) until that pioneer's capture flips
	// LocalCacheState=Warm, then they proceed and warm-restore via Hook A.
	// Empty when no pioneer is elected. See TryClaimColdStartPioneer in
	// pkg/nvca/nvsnap/reconciler/state.go.
	// +optional
	ColdStartPioneer string `json:"coldStartPioneer,omitempty"`

	// ColdStartPioneerExpiry bounds how long a ColdStartPioneer claim is
	// honored (serialized-herd cold start). If the pioneer's reconcile
	// dies before its capture warms the function, the claim would
	// otherwise defer every other replica forever; once
	// now > ColdStartPioneerExpiry any replica may steal the pioneer slot
	// and cold-start itself. Set to now + the same lease TTL as
	// CaptureLeaseExpiry (it must cover cold-start + warmup + capture).
	// RFC3339.
	// +optional
	ColdStartPioneerExpiry *metav1.Time `json:"coldStartPioneerExpiry,omitempty"`

	// Conditions follow the K8s standard format. Recommended:
	//   - type: Ready, status: True iff checkpoint is usable on this cluster
	//   - type: CaptureInProgress, status: True while Hook B is mid-flight
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type NvSnapFunctionStateList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []NvSnapFunctionState `json:"items"`
}
