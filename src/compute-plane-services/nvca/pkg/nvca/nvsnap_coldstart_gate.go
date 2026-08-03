/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package nvca

import (
	"context"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvsnapreconciler "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap/reconciler"
)

// maxConcurrentColdStarts is the number of cold-start pioneers allowed to
// proceed concurrently per function-version (the "K" in the serialized-
// herd design). Hidden internal knob — not surfaced in any helm value or
// UI. v1 honors K=1 only (strict single pioneer); the field exists so
// the call sites read as configurable.
//
// TODO(nvsnap): expose this (per-cluster ClusterConfig.NvSnap) and honor
// K>1 once TryClaimColdStartPioneer grows multi-slot election. Until
// then this MUST stay 1 — the election machinery elects exactly one
// pioneer regardless, so any other value would be a silent lie.
const maxConcurrentColdStarts = 1

// deferColdStartReplica is the sentinel-style non-terminal error the
// MiniService-creation gate returns to requeue a deferred replica.
// applyMiniServiceCreationMessage's caller (backendk8scache.go's
// ApplyCreationMessage path) requeues on any non-terminal error, so
// returning this (wrapped, NOT a nvcaerrors.TerminalError) parks the
// replica until the pioneer warms the function. Mirrors the
// "...will be requeued, err: %w" requeue convention in that path.
func deferColdStartReplica(fvID, pioneer string) error {
	return fmt.Errorf("nvsnap: deferring replica until cold-start pioneer warms "+
		"(fvID=%s, pioneer=%s); MiniService creation will be requeued", fvID, pioneer)
}

// shouldDeferColdStart is the serialized-herd cold-start gate (pioneer
// election), evaluated immediately BEFORE the MiniService is created in
// applyMiniServiceCreationMessage. It is the exact analogue of the
// capture-once guard (nvca#189) applied one step earlier: when N replicas
// of one function-version deploy cold (no Warm checkpoint yet), exactly
// one "pioneer" is allowed through to cold-start + capture; the rest are
// deferred (requeued) until that pioneer's capture flips
// LocalCacheState=Warm, at which point they proceed and warm-restore via
// Hook A (stampNvSnapAnnotations).
//
// Returns a non-nil non-terminal error ONLY when this replica must defer.
// In every other case it returns nil ("proceed") — the gate is an
// optimization, never a hard gate on deployment:
//
//   - NvSnap integration disabled (feature flag off): proceed.
//   - fvID empty (no function-version on the request): proceed.
//   - CFS lookup hits a real API error: proceed (fail-open). A transient
//     API blip must not block a deployment.
//   - CFS Warm: proceed (warm-restore via Hook A).
//   - CFS Failed: proceed (cold; fail-open — a prior capture failed, do
//     not pin the herd on a slot that can't warm).
//   - CFS Cold / Fetching / Capturing / absent: run the pioneer
//     election. Win the slot (or an expired-lease steal) → proceed as
//     pioneer; lose to a live foreign pioneer → defer.
//
// Replica count: N replicas of one function-version arrive as N
// independent ICMSRequests sharing an fvID — the per-request
// InstanceCount is not the fleet-wide replica count, so the gate does
// NOT try to know N. The election is correct without it: it lets exactly
// one request through and defers the others, which is the desired
// behavior whether N is 2 or 200. A genuinely single-replica function
// simply wins its own election immediately (one pioneer, nobody to
// defer) — no added latency.
func (c K8sComputeBackend) shouldDeferColdStart(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
	log := core.GetLogger(ctx)

	if !featureflag.NvSnapCheckpointRestore.Enabled() {
		return nil
	}

	fvID := req.Spec.FunctionDetails.FunctionVersionID
	if fvID == "" {
		return nil // no function-version, nothing to serialize on
	}

	cfsObj, err := c.dynClient.Resource(nvsnapFunctionStateGVR).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		// Real API error (not a plain 404) — fail open. Blocking a
		// deployment on a transient CFS read failure is never correct;
		// NvSnap is an optimization.
		log.WithError(err).WithField("functionVersionID", fvID).
			Info("nvsnap: cold-start gate CFS lookup failed; proceeding (fail-open)")
		return nil
	}

	// Read state (NotFound → empty object → Cold-equivalent, falls into
	// the election branch below where an absent CFS is claimable).
	var state string
	if cfsObj != nil {
		state, _, _ = unstructured.NestedString(cfsObj.Object, "status", "localCacheState")
	}

	switch nvsnapv1alpha1.NvSnapFunctionStateLocalCacheState(state) {
	case nvsnapv1alpha1.LocalCacheStateWarm:
		return nil // warm-restore via Hook A
	case nvsnapv1alpha1.LocalCacheStateFailed:
		return nil // fail-open cold; don't pin the herd on a failed slot
	}

	// Cold / Fetching / Capturing / absent → elect a pioneer. owner is
	// the requesting ICMSRequest's namespace/name (its identity for the
	// claim; mirrors how the capture-once guard keys CaptureOwner by the
	// pod's namespace/name).
	owner := req.Namespace + "/" + req.Name
	now := core.GetCurrentTime(ctx)
	leaseExpiry := now.Add(nvsnapreconciler.DefaultColdStartPioneerLeaseTTL())

	claimed, err := nvsnapreconciler.TryClaimColdStartPioneer(ctx, c.dynClient, fvID, owner, leaseExpiry, now)
	if err != nil {
		// CAS itself errored (API failure) — fail open rather than block.
		log.WithError(err).WithFields(map[string]any{
			"functionVersionID": fvID, "owner": owner,
		}).Info("nvsnap: cold-start pioneer claim failed; proceeding (fail-open)")
		return nil
	}
	if claimed {
		coldStartPioneersElected.Inc()
		log.WithFields(map[string]any{
			"functionVersionID": fvID, "owner": owner,
		}).Info("nvsnap: elected cold-start pioneer; proceeding to cold-start + capture")
		return nil
	}

	// Lost the election to a live foreign pioneer → defer (requeue).
	pioneer, _, _ := unstructured.NestedString(cfsObj.Object, "status", "coldStartPioneer")
	coldStartReplicasDeferred.Inc()
	log.WithFields(map[string]any{
		"functionVersionID": fvID, "owner": owner, "pioneer": pioneer,
	}).Info("nvsnap: deferring replica until cold-start pioneer warms the function")
	return deferColdStartReplica(fvID, pioneer)
}
