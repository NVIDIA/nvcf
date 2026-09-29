/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
)

// capturingCFS seeds a NvSnapFunctionState already in the Capturing
// state, owned by `owner` with the given lease expiry — the shape the
// capture-once guard (nvca#189) writes when a reconcile wins the claim.
func capturingCFS(fvID, owner string, leaseExpiry time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": fvID},
		"spec":       map[string]any{"functionVersionID": fvID},
		"status": map[string]any{
			"localCacheState":    string(nvsnapv1alpha1.LocalCacheStateCapturing),
			"captureOwner":       owner,
			"captureLeaseExpiry": leaseExpiry.UTC().Format(time.RFC3339),
		},
	}}
}

// TestTryClaimCapture covers the compare-and-swap state machine that
// makes capture single-flight per function-version (nvca#189).
func TestTryClaimCapture(t *testing.T) {
	const fvID = "fv-claim"
	ctx := context.Background()
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	lease := now.Add(30 * time.Minute)

	t.Run("cold is claimable; stamps Capturing+owner+lease", func(t *testing.T) {
		dyn := newFakeDynamic(coldCFS(fvID))
		claimed, err := tryClaimCapture(ctx, dyn, fvID, "ns1/podA", lease, now)
		if err != nil {
			t.Fatalf("tryClaimCapture: %v", err)
		}
		if !claimed {
			t.Fatal("expected cold CFS to be claimable")
		}
		cur, err := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		st := readStatus(cur)
		if st.LocalCacheState != nvsnapv1alpha1.LocalCacheStateCapturing {
			t.Errorf("state = %q, want Capturing", st.LocalCacheState)
		}
		if st.CaptureOwner != "ns1/podA" {
			t.Errorf("owner = %q, want ns1/podA", st.CaptureOwner)
		}
		if st.CaptureLeaseExpiry == nil || !st.CaptureLeaseExpiry.Time.Equal(lease) {
			t.Errorf("leaseExpiry = %v, want %v", st.CaptureLeaseExpiry, lease)
		}
	})

	t.Run("a live claim by another owner is NOT claimable", func(t *testing.T) {
		dyn := newFakeDynamic(capturingCFS(fvID, "ns1/podA", lease))
		claimed, err := tryClaimCapture(ctx, dyn, fvID, "ns1/podB", lease, now)
		if err != nil {
			t.Fatalf("tryClaimCapture: %v", err)
		}
		if claimed {
			t.Fatal("podB must NOT claim while podA holds a live lease (thundering-herd guard)")
		}
		// Owner must be unchanged.
		cur, _ := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
		if owner := readStatus(cur).CaptureOwner; owner != "ns1/podA" {
			t.Errorf("owner = %q, want unchanged ns1/podA", owner)
		}
	})

	t.Run("an EXPIRED claim is stealable by another owner", func(t *testing.T) {
		expired := now.Add(-1 * time.Minute) // lease already elapsed
		dyn := newFakeDynamic(capturingCFS(fvID, "ns1/podA", expired))
		claimed, err := tryClaimCapture(ctx, dyn, fvID, "ns1/podB", lease, now)
		if err != nil {
			t.Fatalf("tryClaimCapture: %v", err)
		}
		if !claimed {
			t.Fatal("expired claim must be stealable (crash recovery)")
		}
		cur, _ := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
		if owner := readStatus(cur).CaptureOwner; owner != "ns1/podB" {
			t.Errorf("owner = %q, want stolen to ns1/podB", owner)
		}
	})

	t.Run("the owning pod re-claims re-entrantly (lease refresh)", func(t *testing.T) {
		dyn := newFakeDynamic(capturingCFS(fvID, "ns1/podA", lease))
		newLease := now.Add(45 * time.Minute)
		claimed, err := tryClaimCapture(ctx, dyn, fvID, "ns1/podA", newLease, now)
		if err != nil {
			t.Fatalf("tryClaimCapture: %v", err)
		}
		if !claimed {
			t.Fatal("the owner must be able to re-claim its own live lease")
		}
		cur, _ := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
		if exp := readStatus(cur).CaptureLeaseExpiry; exp == nil || !exp.Time.Equal(newLease) {
			t.Errorf("lease not refreshed: got %v want %v", exp, newLease)
		}
	})

	t.Run("Warm is never claimable", func(t *testing.T) {
		warm := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
			"kind":       "NvSnapFunctionState",
			"metadata":   map[string]any{"name": fvID},
			"spec":       map[string]any{"functionVersionID": fvID},
			"status":     map[string]any{"localCacheState": string(nvsnapv1alpha1.LocalCacheStateWarm)},
		}}
		dyn := newFakeDynamic(warm)
		claimed, err := tryClaimCapture(ctx, dyn, fvID, "ns1/podB", lease, now)
		if err != nil {
			t.Fatalf("tryClaimCapture: %v", err)
		}
		if claimed {
			t.Fatal("Warm CFS must not be claimed for capture")
		}
	})
}

// TestWriteStatusReleasesClaim verifies a terminal status write clears
// the Capturing claim — without this a completed/failed capture would
// leave captureOwner/leaseExpiry set and either pin the function or let
// readStatus mis-report a stale owner.
// A live lease held by a pod that no longer exists is stealable at once:
// the owner died mid-capture (dev1 2026-09-28: the node agent restarted
// under it and the pod was replaced) and nothing would release the claim
// before the lease expired.
func TestTryClaimCapture_DeadOwnerIsStealable(t *testing.T) {
	ctx := context.Background()
	fvID := "fv-dead"
	now := time.Now()
	lease := now.Add(50 * time.Minute)
	dyn := newFakeDynamic(coldCFS(fvID))
	if claimed, err := tryClaimCapture(ctx, dyn, fvID, "ns1/podA", lease, now); err != nil || !claimed {
		t.Fatalf("podA claim: claimed=%v err=%v", claimed, err)
	}
	dead := func(context.Context, string) bool { return false }
	alive := func(context.Context, string) bool { return true }
	if claimed, _ := tryClaimCaptureLive(ctx, dyn, fvID, "ns1/podB", lease, now, alive); claimed {
		t.Fatal("podB must not steal a live claim from a live owner")
	}
	if claimed, _ := tryClaimCaptureLive(ctx, dyn, fvID, "ns1/podB", lease, now, nil); claimed {
		t.Fatal("unknown liveness (nil) must keep the claim")
	}
	claimed, err := tryClaimCaptureLive(ctx, dyn, fvID, "ns1/podB", lease, now, dead)
	if err != nil || !claimed {
		t.Fatalf("podB must steal the claim from a dead owner: claimed=%v err=%v", claimed, err)
	}
	cur, err := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st := readStatus(cur); st.CaptureOwner != "ns1/podB" {
		t.Errorf("owner after steal = %q, want ns1/podB", st.CaptureOwner)
	}
}

func TestWriteStatusReleasesClaim(t *testing.T) {
	const fvID = "fv-release"
	ctx := context.Background()
	dyn := newFakeDynamic(capturingCFS(fvID, "ns1/podA", time.Now().Add(30*time.Minute)))

	if err := writeStatus(ctx, dyn, fvID, statusUpdate{
		CheckpointHash:  "feedface",
		CapturedHere:    true,
		LocalCacheState: nvsnapv1alpha1.LocalCacheStateWarm,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	cur, _ := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	st := readStatus(cur)
	if st.LocalCacheState != nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("state = %q, want Warm", st.LocalCacheState)
	}
	if st.CaptureOwner != "" || st.CaptureLeaseExpiry != nil {
		t.Errorf("claim not released: owner=%q lease=%v", st.CaptureOwner, st.CaptureLeaseExpiry)
	}
}

// TestReconcileSkipsWhenCaptureInFlight is the end-to-end thundering-
// herd assertion: with a function-version already Capturing (owned by
// another pod, lease live), a second pod's reconcile must NOT POST a
// duplicate capture — it backs off on the capture-once claim (nvca#189).
func TestReconcileSkipsWhenCaptureInFlight(t *testing.T) {
	const fvID = "fv-herd"
	pod := inferencePod("podB", "ns1", fvID) // Ready, checkpoint-on-warm

	var capturePosted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Recovery short-circuit (gate 0b) queries lookup; return no
		// match so the reconcile falls through to the claim gate
		// rather than recovering an existing capture.
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints/lookup"):
			_ = json.NewEncoder(w).Encode(map[string]any{"matches": []map[string]any{}})
		// A capture POST here would be the duplicate we must prevent.
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints"):
			capturePosted = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ck-dup", "phase": "InProgress"})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer srv.Close()

	// podA already holds a live Capturing claim on this fvID.
	dyn := newFakeDynamic(capturingCFS(fvID, "ns1/podA", time.Now().Add(30*time.Minute)))
	r := newTestReconciler(t, pod, srv, dyn)
	// podA, the claim owner, is alive: the in-flight guard must hold.
	if _, err := r.KubeClient.CoreV1().Pods("ns1").Create(context.Background(), inferencePod("podA", "ns1", fvID), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if capturePosted {
		t.Fatal("podB POSTed a duplicate capture while podA held the in-flight claim — thundering-herd guard failed")
	}
	// CFS must remain Capturing under podA (untouched by podB).
	cur, _ := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	if st := readStatus(cur); st.CaptureOwner != "ns1/podA" {
		t.Errorf("claim owner = %q, want unchanged ns1/podA", st.CaptureOwner)
	}
}

// Same scenario, but the claim owner no longer exists: the claim is dead
// and podB must capture instead of waiting out the lease.
func TestReconcileStealsClaimFromDeadOwner(t *testing.T) {
	fvID := "fv-dead-owner"
	posted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints/lookup"):
			_ = json.NewEncoder(w).Encode(map[string]any{"matches": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints"):
			posted++
			w.WriteHeader(http.StatusInternalServerError) // stop the flow here; the claim is what is under test
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	pod := inferencePod("podB", "ns1", fvID)
	dyn := newFakeDynamic(capturingCFS(fvID, "ns1/podA", time.Now().Add(30*time.Minute)))
	r := newTestReconciler(t, pod, srv, dyn)
	_ = r.Reconcile(context.Background(), pod)
	if posted != 1 {
		t.Fatalf("podB must take over a claim whose owner pod is gone; POSTs=%d", posted)
	}
}
