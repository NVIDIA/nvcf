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

// Tests for the pod-independent CFS recovery sweep (nvca#104
// durable-warm). The sweep must flip a not-Warm CFS to Warm from an
// existing usable capture WITHOUT a live pod, and must skip CFS that
// are already Warm, opted out, have no persisted lookup inputs, or have
// no usable capture in nvsnap-server.

package reconciler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	fakedynamic "k8s.io/client-go/dynamic/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"
)

// cfsWithLookup seeds a not-Warm CFS carrying persisted workload-lookup
// inputs (what Hook B writes before the risky poll). extra lets a test
// set additional spec/status keys (optOut, an already-Warm state).
func cfsWithLookup(fvID, image, model string, mutate func(obj map[string]any)) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "nvsnap.nvcf.nvidia.io/v1alpha1",
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": fvID},
		"spec": map[string]any{
			"functionVersionID": fvID,
			"workloadLookup":    map[string]any{"imageRef": image, "modelId": model},
		},
	}
	if mutate != nil {
		mutate(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

// sweepReconciler builds a Reconciler pointed at srv + dyn, with no pod
// (the sweep never needs one).
func sweepReconciler(srv *httptest.Server, dyn *fakedynamic.FakeDynamicClient) *Reconciler {
	return &Reconciler{
		DynClient:    dyn,
		NvSnapClient: nvsnap.NewClient(nvsnap.WithBaseURL(srv.URL)),
		Log:          logrus.NewEntry(logrus.New()),
	}
}

// usableCaptureServer answers lookup with one match whose pvc-state is
// "ready" — a recoverable capture.
func usableCaptureServer(image, hash string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints/lookup"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"matches": []map[string]any{{"hash": hash, "checkpointId": "ck", "imageRef": image}},
			})
		case strings.Contains(r.URL.Path, "/pvc-state"):
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": hash, "state": "ready", "pvc_name": "rox-" + hash})
		default:
			http.NotFound(w, r)
		}
	}))
}

// The core fix: a not-Warm CFS with a usable capture in nvsnap-server is
// flipped to Warm by the sweep, no pod involved.
func TestSweep_FlipsWarmFromUsableCapture(t *testing.T) {
	const fvID, image, hash = "fv-sweep", "ngc.io/fn:1", "deadbeefcafe1001"
	srv := usableCaptureServer(image, hash)
	defer srv.Close()
	dyn := newFakeDynamic(cfsWithLookup(fvID, image, "", nil))

	sweepReconciler(srv, dyn).SweepOnce(context.Background())

	got, err := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.LocalCacheState != "Warm" {
		t.Errorf("CFS not flipped Warm: got %q", s.LocalCacheState)
	}
	if s.CheckpointHash != hash {
		t.Errorf("hash: got %q want %q", s.CheckpointHash, hash)
	}
	if !s.CapturedHere {
		t.Error("capturedHere should be true (this cluster holds the L2 rox)")
	}
}

// An already-Warm CFS must be left untouched — and the sweep must not
// even hit nvsnap-server for it.
func TestSweep_SkipsAlreadyWarm(t *testing.T) {
	const fvID, image = "fv-warm", "ngc.io/fn:2"
	var lookupCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookupCalled = true
		http.NotFound(w, r)
	}))
	defer srv.Close()
	dyn := newFakeDynamic(cfsWithLookup(fvID, image, "", func(obj map[string]any) {
		obj["status"] = map[string]any{"localCacheState": "Warm", "checkpointHash": "existing"}
	}))

	sweepReconciler(srv, dyn).SweepOnce(context.Background())

	if lookupCalled {
		t.Error("sweep must not query nvsnap-server for an already-Warm CFS")
	}
	got, _ := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	if h := readStatus(got).CheckpointHash; h != "existing" {
		t.Errorf("already-Warm hash mutated: got %q", h)
	}
}

// A CFS with no persisted workloadLookup can't be looked up — skip it
// (no panic, no nvsnap-server call).
func TestSweep_SkipsWhenNoLookupInputs(t *testing.T) {
	const fvID = "fv-nolookup"
	var lookupCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookupCalled = true
		http.NotFound(w, r)
	}))
	defer srv.Close()
	dyn := newFakeDynamic(coldCFS(fvID)) // no workloadLookup

	sweepReconciler(srv, dyn).SweepOnce(context.Background())

	if lookupCalled {
		t.Error("sweep must not query nvsnap-server when workloadLookup is unset")
	}
	got, _ := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	if s := readStatus(got).LocalCacheState; s == "Warm" {
		t.Error("CFS without lookup inputs must not be flipped Warm")
	}
}

// An opted-out CFS must never be flipped Warm, even with a usable
// capture present.
func TestSweep_SkipsOptedOut(t *testing.T) {
	const fvID, image, hash = "fv-optout", "ngc.io/fn:3", "deadbeefcafe1003"
	srv := usableCaptureServer(image, hash)
	defer srv.Close()
	dyn := newFakeDynamic(cfsWithLookup(fvID, image, "", func(obj map[string]any) {
		obj["spec"].(map[string]any)["optOut"] = true
	}))

	sweepReconciler(srv, dyn).SweepOnce(context.Background())

	got, _ := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	if s := readStatus(got).LocalCacheState; s == "Warm" {
		t.Error("opted-out CFS must not be flipped Warm")
	}
}

// No usable capture yet (lookup returns empty) → CFS stays not-Warm,
// the sweep will retry next tick.
func TestSweep_LeavesNotWarmWhenNoCapture(t *testing.T) {
	const fvID, image = "fv-nocap", "ngc.io/fn:4"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints/lookup") {
			_ = json.NewEncoder(w).Encode(map[string]any{"matches": []map[string]any{}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	dyn := newFakeDynamic(cfsWithLookup(fvID, image, "", nil))

	sweepReconciler(srv, dyn).SweepOnce(context.Background())

	got, _ := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	if s := readStatus(got).LocalCacheState; s == "Warm" {
		t.Error("CFS must stay not-Warm when no usable capture exists")
	}
}

// writeWorkloadLookup persists the inputs and is a no-op when already
// current (the reconciler calls it on every reconcile).
func TestWriteWorkloadLookup_PersistsAndIdempotent(t *testing.T) {
	const fvID, image, model = "fv-wl", "ngc.io/fn:5", "meta-llama/x"
	dyn := newFakeDynamic(coldCFS(fvID))
	ctx := context.Background()

	if err := writeWorkloadLookup(ctx, dyn, fvID, image, model); err != nil {
		t.Fatalf("writeWorkloadLookup: %v", err)
	}
	got, _ := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	gotImage, gotModel := readWorkloadLookup(got)
	if gotImage != image || gotModel != model {
		t.Fatalf("persisted lookup: got (%q,%q) want (%q,%q)", gotImage, gotModel, image, model)
	}
	rv := got.GetResourceVersion()

	// Second call with identical values must not write (resourceVersion
	// unchanged).
	if err := writeWorkloadLookup(ctx, dyn, fvID, image, model); err != nil {
		t.Fatalf("writeWorkloadLookup (2nd): %v", err)
	}
	got2, _ := dyn.Resource(CFSResource).Get(ctx, fvID, metav1.GetOptions{})
	if got2.GetResourceVersion() != rv {
		t.Errorf("idempotent write should not bump resourceVersion: %q -> %q", rv, got2.GetResourceVersion())
	}

	// Empty imageRef is a no-op.
	if err := writeWorkloadLookup(ctx, dyn, fvID, "", ""); err != nil {
		t.Errorf("empty imageRef should be a no-op, got %v", err)
	}
}
