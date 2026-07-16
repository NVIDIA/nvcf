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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
)

// recoverablePod is inferencePod + an image (the lookup key) on the
// inference container.
func recoverablePod(podName, ns, fvID, image string) *corev1.Pod {
	p := inferencePod(podName, ns, fvID)
	p.Spec.Containers[0].Image = image
	return p
}

// coldCFS seeds a NvSnapFunctionState that exists but is NOT Warm.
func coldCFS(fvID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": fvID},
		"spec":       map[string]any{"functionVersionID": fvID},
	}}
}

// nvca#104: if a usable capture already exists, the reconcile must flip
// CFS=Warm via the recovery short-circuit and must NOT POST a new
// capture (which would be the duplicate-capture re-dump).
func TestReconcileRecoversExistingCapture(t *testing.T) {
	const fvID, image, hash = "fv-rec", "ngc.io/fn:1", "deadbeefcafe0001"
	pod := recoverablePod("p-rec", "ns1", fvID, image)
	dyn := newFakeDynamic(coldCFS(fvID))

	var capturePosted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints/lookup"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"matches": []map[string]any{{"hash": hash, "checkpointId": "ck-rec", "imageRef": image}},
			})
		case strings.Contains(r.URL.Path, "/pvc-state"):
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": hash, "state": "ready", "pvc_name": "rox-deadbeef"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints"):
			capturePosted = true // recovery should have prevented this
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ck-new", "phase": "InProgress"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := newTestReconciler(t, pod, srv, dyn)
	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if capturePosted {
		t.Error("recovery should NOT POST a new capture when a usable one already exists")
	}
	got, _ := dyn.Resource(CFSResource).Get(context.Background(), fvID, metav1.GetOptions{})
	s := readStatus(got)
	if s.LocalCacheState != nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("CFS not flipped Warm: got %q", s.LocalCacheState)
	}
	if s.CheckpointHash != hash {
		t.Errorf("CheckpointHash: got %q, want %q", s.CheckpointHash, hash)
	}
}

// A "failed" L2 promote on the matched capture is NOT recoverable —
// the reconcile must fall through to the normal capture flow (POST).
func TestReconcileRecovery_SkipsFailedPromote(t *testing.T) {
	const fvID, image, hash = "fv-rec2", "ngc.io/fn:2", "deadbeefcafe0002"
	pod := recoverablePod("p-rec2", "ns1", fvID, image)
	dyn := newFakeDynamic(coldCFS(fvID))

	var capturePosted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints/lookup"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"matches": []map[string]any{{"hash": hash, "imageRef": image}},
			})
		case strings.Contains(r.URL.Path, "/pvc-state"):
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": hash, "state": "failed"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints"):
			capturePosted = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ck-new", "phase": "InProgress"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/v1/checkpoints/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ck-new", "phase": "Completed", "hash": hash})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := newTestReconciler(t, pod, srv, dyn)
	// promote poll for the fresh capture would block; the new capture's
	// pvc-state also returns "failed", which the normal flow records as
	// a failure (no Warm). We only assert recovery did NOT short-circuit.
	_ = r.Reconcile(context.Background(), pod)
	if !capturePosted {
		t.Error("a failed-promote match must NOT be recovered; reconcile should fall through to a fresh capture POST")
	}
}
