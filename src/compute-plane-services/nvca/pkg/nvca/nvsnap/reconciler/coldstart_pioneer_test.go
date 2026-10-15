/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconciler

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
)

// mustGet reads CFS/<fvID> off the fake dynamic client or fails the test.
func mustGet(t *testing.T, dyn dynamic.Interface, fvID string) *unstructured.Unstructured {
	t.Helper()
	cur, err := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get NvSnapFunctionState %s: %v", fvID, err)
	}
	return cur
}

// pioneerCFS seeds a NvSnapFunctionState already holding a cold-start
// pioneer claim owned by `owner` with the given lease expiry — the shape
// TryClaimColdStartPioneer writes when a replica wins the election.
func pioneerCFS(fvID, owner string, leaseExpiry time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": fvID},
		"spec":       map[string]any{"functionVersionID": fvID},
		"status": map[string]any{
			"localCacheState":        string(nvsnapv1alpha1.LocalCacheStateCold),
			"coldStartPioneer":       owner,
			"coldStartPioneerExpiry": leaseExpiry.UTC().Format(time.RFC3339),
		},
	}}
}

// stateCFS seeds a NvSnapFunctionState in a given localCacheState with no
// pioneer claim.
func stateCFS(fvID string, state nvsnapv1alpha1.NvSnapFunctionStateLocalCacheState) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": fvID},
		"spec":       map[string]any{"functionVersionID": fvID},
		"status":     map[string]any{"localCacheState": string(state)},
	}}
}

// TestTryClaimColdStartPioneer covers the serialized-herd compare-and-swap
// that elects exactly one cold-start pioneer per function-version.
func TestTryClaimColdStartPioneer(t *testing.T) {
	const fvID = "fv-pioneer"
	ctx := context.Background()
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	lease := now.Add(50 * time.Minute)

	t.Run("cold is claimable; stamps pioneer+expiry", func(t *testing.T) {
		dyn := newFakeDynamic(coldCFS(fvID))
		claimed, err := TryClaimColdStartPioneer(ctx, dyn, fvID, "ns1/reqA", lease, now)
		if err != nil {
			t.Fatalf("TryClaimColdStartPioneer: %v", err)
		}
		if !claimed {
			t.Fatal("expected cold CFS to be claimable as pioneer")
		}
		st := readStatus(mustGet(t, dyn, fvID))
		if st.ColdStartPioneer != "ns1/reqA" {
			t.Errorf("pioneer = %q, want ns1/reqA", st.ColdStartPioneer)
		}
		if st.ColdStartPioneerExpiry == nil || !st.ColdStartPioneerExpiry.Time.Equal(lease) {
			t.Errorf("pioneerExpiry = %v, want %v", st.ColdStartPioneerExpiry, lease)
		}
	})

	t.Run("a live claim by another owner is NOT claimable", func(t *testing.T) {
		dyn := newFakeDynamic(pioneerCFS(fvID, "ns1/reqA", lease))
		claimed, err := TryClaimColdStartPioneer(ctx, dyn, fvID, "ns1/reqB", lease, now)
		if err != nil {
			t.Fatalf("TryClaimColdStartPioneer: %v", err)
		}
		if claimed {
			t.Fatal("reqB must NOT claim while reqA holds a live pioneer lease (serialized-herd)")
		}
		if p := readStatus(mustGet(t, dyn, fvID)).ColdStartPioneer; p != "ns1/reqA" {
			t.Errorf("pioneer = %q, want unchanged ns1/reqA", p)
		}
	})

	t.Run("an EXPIRED claim is stealable by another owner", func(t *testing.T) {
		expired := now.Add(-1 * time.Minute)
		dyn := newFakeDynamic(pioneerCFS(fvID, "ns1/reqA", expired))
		claimed, err := TryClaimColdStartPioneer(ctx, dyn, fvID, "ns1/reqB", lease, now)
		if err != nil {
			t.Fatalf("TryClaimColdStartPioneer: %v", err)
		}
		if !claimed {
			t.Fatal("expired pioneer claim must be stealable (crash recovery)")
		}
		if p := readStatus(mustGet(t, dyn, fvID)).ColdStartPioneer; p != "ns1/reqB" {
			t.Errorf("pioneer = %q, want stolen to ns1/reqB", p)
		}
	})

	t.Run("the owning request re-claims re-entrantly (lease refresh)", func(t *testing.T) {
		dyn := newFakeDynamic(pioneerCFS(fvID, "ns1/reqA", lease))
		newLease := now.Add(75 * time.Minute)
		claimed, err := TryClaimColdStartPioneer(ctx, dyn, fvID, "ns1/reqA", newLease, now)
		if err != nil {
			t.Fatalf("TryClaimColdStartPioneer: %v", err)
		}
		if !claimed {
			t.Fatal("the pioneer must be able to re-claim its own live lease")
		}
		if exp := readStatus(mustGet(t, dyn, fvID)).ColdStartPioneerExpiry; exp == nil || !exp.Time.Equal(newLease) {
			t.Errorf("lease not refreshed: got %v want %v", exp, newLease)
		}
	})

	t.Run("Warm is never claimable (caller proceeds to warm-restore)", func(t *testing.T) {
		dyn := newFakeDynamic(stateCFS(fvID, nvsnapv1alpha1.LocalCacheStateWarm))
		claimed, err := TryClaimColdStartPioneer(ctx, dyn, fvID, "ns1/reqB", lease, now)
		if err != nil {
			t.Fatalf("TryClaimColdStartPioneer: %v", err)
		}
		if claimed {
			t.Fatal("Warm CFS must not be claimed as pioneer")
		}
	})

	t.Run("Failed is never claimable (caller fails open to cold)", func(t *testing.T) {
		dyn := newFakeDynamic(stateCFS(fvID, nvsnapv1alpha1.LocalCacheStateFailed))
		claimed, err := TryClaimColdStartPioneer(ctx, dyn, fvID, "ns1/reqB", lease, now)
		if err != nil {
			t.Fatalf("TryClaimColdStartPioneer: %v", err)
		}
		if claimed {
			t.Fatal("Failed CFS must not be claimed as pioneer")
		}
	})
}

// TestWriteStatusReleasesPioneerClaim verifies a terminal status write
// vacates the cold-start pioneer slot — without this a warmed function
// would keep a stale pioneer owner on status.
func TestWriteStatusReleasesPioneerClaim(t *testing.T) {
	const fvID = "fv-pioneer-release"
	ctx := context.Background()
	dyn := newFakeDynamic(pioneerCFS(fvID, "ns1/reqA", time.Now().Add(50*time.Minute)))

	if err := writeStatus(ctx, dyn, fvID, statusUpdate{
		CheckpointHash:  "feedface",
		CapturedHere:    true,
		LocalCacheState: nvsnapv1alpha1.LocalCacheStateWarm,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	st := readStatus(mustGet(t, dyn, fvID))
	if st.ColdStartPioneer != "" || st.ColdStartPioneerExpiry != nil {
		t.Errorf("pioneer claim not released: owner=%q expiry=%v", st.ColdStartPioneer, st.ColdStartPioneerExpiry)
	}
}
