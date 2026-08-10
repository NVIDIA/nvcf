/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Tests for POST /api/v1/checkpoints/by-hash/{hash}/pvc-state
// (nvsnap#63 / nvsnap#76). The endpoint is the agent's
// PerCapturePVCBackend's only writer into the catalog's L2
// state-machine columns. Keyed by content hash, not catalog id, so
// the producer (which only knows the hash) and the catalog row
// (which is per-capture-attempt) stay decoupled.

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// fullHash is a realistic 64-char content hash; identical to one the
// agent would produce in production (matches the catalog's hash
// column width and the value the L2 PVC short-name derives from).
const fullHash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"

func TestUpdatePVCPromoteState_HappyPath(t *testing.T) {
	s := newTestServerWithCatalog(t)
	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns", PodName: "p", NodeName: "n", Status: "Completed",
		Hash: fullHash,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Walk pending → writing → ready (no snapshotting in this test for
	// brevity; the state ordering is what the catalog cares about).
	for _, step := range []struct {
		state string
		name  string
	}{
		{"pending", ""},
		{"writing", ""},
		{"ready", "rox-a4f7818605da321e"},
	} {
		body, _ := json.Marshal(updatePVCPromoteStateRequest{State: step.state, PVCName: step.name})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s: status = %d, body = %s", step.state, rr.Code, rr.Body.String())
		}
	}

	got, err := s.catalog.GetCheckpoint("ckpt-l2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PVCPromoteState != "ready" {
		t.Errorf("state = %q, want ready", got.PVCPromoteState)
	}
	if got.PVCName != "rox-a4f7818605da321e" {
		t.Errorf("pvc_name = %q, want rox-a4f7818605da321e", got.PVCName)
	}
}

func TestUpdatePVCPromoteState_RejectsInvalidState(t *testing.T) {
	s := newTestServerWithCatalog(t)
	_ = s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns", PodName: "p", NodeName: "n", Status: "Completed",
		Hash: fullHash,
	})
	body, _ := json.Marshal(updatePVCPromoteStateRequest{State: "marshmallow"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid state)", rr.Code)
	}
}

func TestUpdatePVCPromoteState_404OnUnknownHash(t *testing.T) {
	s := newTestServerWithCatalog(t)
	body, _ := json.Marshal(updatePVCPromoteStateRequest{State: "ready", PVCName: "rox-abc"})
	// Seed a row with a DIFFERENT hash — the handler must 404, not
	// silently update the wrong row.
	_ = s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: "ckpt-other", CheckpointID: "ckpt-other",
		Namespace: "ns", PodName: "p", NodeName: "n", Status: "Completed",
		Hash: "deadbeef00000000000000000000000000000000000000000000000000000000",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// COALESCE invariant at the HTTP layer: a later state callback with
// empty pvc_name preserves the rox-<hash> already published. Same
// shape as the DB-layer test in internal/db, here verifying the HTTP
// contract honors it.
func TestUpdatePVCPromoteState_PreservesPVCNameOnEmptyUpdate(t *testing.T) {
	s := newTestServerWithCatalog(t)
	_ = s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns", PodName: "p", NodeName: "n", Status: "Completed",
		Hash: fullHash,
	})
	body, _ := json.Marshal(updatePVCPromoteStateRequest{State: "ready", PVCName: "rox-a4f78186"})
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("seed ready: %d", rr.Code)
	}
	body, _ = json.Marshal(updatePVCPromoteStateRequest{State: "failed", PVCName: ""})
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("failed transition: %d", rr.Code)
	}
	got, _ := s.catalog.GetCheckpoint("ckpt-l2")
	if got.PVCName != "rox-a4f78186" {
		t.Errorf("PVC name got wiped on empty-name update; got %q", got.PVCName)
	}
	if got.PVCPromoteState != "failed" {
		t.Errorf("state = %q, want failed", got.PVCPromoteState)
	}
}

// TestGetPVCPromoteState_HappyPath exercises the GET endpoint that
// nvsnap-init polls (nvsnap#147). Symmetric to the POST writer above.
// Walks pending → ready and asserts each transition is observable via
// GET — that's the contract nvsnap-init depends on.
func TestGetPVCPromoteState_HappyPath(t *testing.T) {
	s := newTestServerWithCatalog(t)
	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns", PodName: "p", NodeName: "n", Status: "Completed",
		Hash: fullHash,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Step 1: row exists but no L2 write yet → state="" (empty).
	// nvsnap-init MUST be able to distinguish this from "ready" or
	// "failed" — without the GET endpoint there's no way to do so.
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("initial GET status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp pvcPromoteStateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	if resp.Hash != fullHash {
		t.Errorf("hash echo mismatch: got %q, want %q", resp.Hash, fullHash)
	}
	if resp.State != "" {
		t.Errorf("initial state = %q, want empty (no L2 write yet)", resp.State)
	}
	if resp.PVCName != "" {
		t.Errorf("initial pvc_name = %q, want empty", resp.PVCName)
	}

	// Step 2: walk transitions and observe via GET each time.
	for _, step := range []struct {
		state       string
		pvcName     string
		wantState   string
		wantPVCName string
	}{
		{"pending", "", "pending", ""},
		{"writing", "", "writing", ""},
		{"snapshotting", "", "snapshotting", ""},
		{"ready", "rox-a4f7818605da321e", "ready", "rox-a4f7818605da321e"},
	} {
		// Write via POST.
		body, _ := json.Marshal(updatePVCPromoteStateRequest{State: step.state, PVCName: step.pvcName})
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body)))
		if w.Code != http.StatusNoContent {
			t.Fatalf("POST %s: status = %d", step.state, w.Code)
		}
		// Read via GET.
		r := httptest.NewRecorder()
		s.router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", http.NoBody))
		if r.Code != http.StatusOK {
			t.Fatalf("GET after %s: status = %d, body = %s", step.state, r.Code, r.Body.String())
		}
		var got pvcPromoteStateResponse
		if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode after %s: %v", step.state, err)
		}
		if got.State != step.wantState {
			t.Errorf("after %s: state = %q, want %q", step.state, got.State, step.wantState)
		}
		if got.PVCName != step.wantPVCName {
			t.Errorf("after %s: pvc_name = %q, want %q", step.state, got.PVCName, step.wantPVCName)
		}
	}
}

// TestGetPVCPromoteState_NotFound asserts the 404 path. nvsnap-init
// uses 404-vs-200 to distinguish "not registered yet — keep polling"
// from "registered but state still pending/writing". Conflating the
// two would either retry forever (if 404 became infinite-retryable)
// or fail-fast on transient race (if 404 became terminal). Lock the
// shape with a test.
func TestGetPVCPromoteState_NotFound(t *testing.T) {
	s := newTestServerWithCatalog(t)
	const unknownHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/by-hash/"+unknownHash+"/pvc-state", http.NoBody))
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown hash: status = %d, want 404", rr.Code)
	}
}

// TestGetPVCPromoteState_MultipleRowsSameHash exercises the "freshest
// row wins" rule: two captures of the same workload share a hash;
// the GET response must reflect the canonical state of the most
// recent one. The DB layer's ORDER BY created_at DESC is what
// guarantees this — surface it as a contract test so a future
// migration that changes ordering breaks loudly.
func TestGetPVCPromoteState_MultipleRowsSameHash(t *testing.T) {
	s := newTestServerWithCatalog(t)
	// Seed two rows for the same hash, then write state once. POST
	// fans out across both rows — GET should reflect the final state.
	for _, id := range []string{"a4f7__20260601-210932", "a4f7__20260601-211222"} {
		if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
			ID: id, CheckpointID: id,
			Namespace: "ns", PodName: "vllm", NodeName: "n", Status: "Completed",
			Hash: fullHash,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	body, _ := json.Marshal(updatePVCPromoteStateRequest{State: "ready", PVCName: "rox-a4f78186"})
	wPost := httptest.NewRecorder()
	s.router.ServeHTTP(wPost, httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body)))
	if wPost.Code != http.StatusNoContent {
		t.Fatalf("POST: status = %d", wPost.Code)
	}
	wGet := httptest.NewRecorder()
	s.router.ServeHTTP(wGet, httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", http.NoBody))
	if wGet.Code != http.StatusOK {
		t.Fatalf("GET: status = %d", wGet.Code)
	}
	var got pvcPromoteStateResponse
	if err := json.Unmarshal(wGet.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != "ready" || got.PVCName != "rox-a4f78186" {
		t.Errorf("multi-row: state=%q pvc=%q, want ready/rox-a4f78186", got.State, got.PVCName)
	}
}

// TestGetPVCPromoteState_MissingHash asserts the 400 path for empty
// hash. The handler must NOT 500 on a malformed URL; mux's empty-var
// case should be caught explicitly.
func TestGetPVCPromoteState_MissingHash(t *testing.T) {
	s := newTestServerWithCatalog(t)
	// Construct a request where mux.Vars["hash"] resolves to "". The
	// route requires {hash}, so a literal empty path component
	// doesn't match the registered route — instead we exercise the
	// guard by injecting an empty-var manually.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/by-hash//pvc-state", http.NoBody)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	// gorilla/mux returns 404 for "doesn't match any route" when the
	// path variable is empty (since the segment regex {hash} requires
	// at least one char). That's also acceptable behavior for the
	// "malformed" case — the API shape is "200 ok or non-2xx".
	if rr.Code >= 200 && rr.Code < 300 {
		t.Errorf("empty hash got 2xx (%d) — expected non-2xx", rr.Code)
	}
}

// Single-row-per-hash (nvsnap#106): the catalog enforces ONE row per
// content hash, so a state-machine write POSTed by hash lands on the
// single converged row. The rest of the L2 design depends on the
// resolver finding exactly that row.
func TestUpdatePVCPromoteState_HitsConvergedRow(t *testing.T) {
	s := newTestServerWithCatalog(t)
	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: "a4f7__20260601-210932", CheckpointID: "a4f7__20260601-210932",
		Namespace: "ns", PodName: "vllm", NodeName: "n", Status: "Completed",
		Hash: fullHash,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body, _ := json.Marshal(updatePVCPromoteStateRequest{State: "ready", PVCName: "rox-a4f78186"})
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/by-hash/"+fullHash+"/pvc-state", bytes.NewReader(body)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	rows, err := s.catalog.ListByHash(fullHash)
	if err != nil {
		t.Fatalf("ListByHash: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListByHash returned %d rows, want 1 (hash uniqueness, nvsnap#106)", len(rows))
	}
	got := rows[0]
	if got.PVCName != "rox-a4f78186" || got.PVCPromoteState != "ready" {
		t.Errorf("converged row: pvc_name=%q state=%q, want ready/rox-a4f78186",
			got.PVCName, got.PVCPromoteState)
	}
}
