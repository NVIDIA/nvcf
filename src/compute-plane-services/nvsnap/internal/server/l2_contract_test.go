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

// Cross-component contract test for the L2 PVC state-machine writer
// (nvsnap#76). The wire contract — URL path, HTTP method, JSON body
// shape, identifier semantics — must match between the producer
// (internal/agent.NewPVCStateHTTPWriter) and the consumer (this
// package's updatePVCPromoteStateByHash handler). Same-package tests
// on either side can't catch drift, so this file pins the contract
// by wiring the real producer against the real router and exercising
// a real in-memory catalog.
//
// The original bug (nvsnap#76): the producer passed the content
// hash in the URL path while the consumer looked up the row by
// catalog id, returning 404 on every transition. Both sides had
// passing tests in isolation because they each used the same
// identifier value throughout. The TestL2HashContract_* cases below
// fail loudly if that drift ever re-appears.

package server

import (
	"net/http/httptest"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/agent"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

const (
	// l2ContractHash is what the agent's PerCapturePVCBackend would
	// pass — a real 64-char content hash. The seeded row's id is the
	// agent's local "<short-hash>__<timestamp>" form, deliberately
	// different so a test failure can only mean URL/lookup drift.
	l2ContractHash       = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"
	l2ContractCheckpoint = "a4f7818605da321e__20260601-210932"
)

// newL2ContractRig wires a real nvsnap-server router around an
// in-memory catalog, fronted by httptest, paired with the real
// agent-side writer pointed at the same URL the agent would use in
// production.
func newL2ContractRig(t *testing.T) (writer interface {
	UpdatePVCPromoteState(hash, state, pvcName string) error
}, catalog *db.DB, teardown func()) {
	t.Helper()
	s := newTestServerWithCatalog(t) // real router + real db.DB
	ts := httptest.NewServer(s.Handler())
	w := agent.NewPVCStateHTTPWriter(ts.URL)
	return w, s.catalog, ts.Close
}

// TestL2HashContract_HappyPath_HashKeyedRouting is the regression
// test for nvsnap#76. The producer (real pvcStateHTTPWriter) walks
// the state machine using the content hash. The consumer (real
// router) must look the row up by hash, not by catalog id. Any drift
// here resurfaces as a 404 from the writer.
func TestL2HashContract_HappyPath_HashKeyedRouting(t *testing.T) {
	w, catalog, done := newL2ContractRig(t)
	defer done()

	if err := catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: l2ContractCheckpoint, CheckpointID: l2ContractCheckpoint,
		Namespace: "nvsnap-system", PodName: "vllm", NodeName: "node-A",
		Status: "Completed",
		Hash:   l2ContractHash,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, state := range []string{"pending", "writing", "snapshotting"} {
		if err := w.UpdatePVCPromoteState(l2ContractHash, state, ""); err != nil {
			t.Fatalf("transition %s: %v", state, err)
		}
	}
	if err := w.UpdatePVCPromoteState(l2ContractHash, "ready", "rox-a4f7818605da321e"); err != nil {
		t.Fatalf("transition ready: %v", err)
	}

	got, err := catalog.GetCheckpoint(l2ContractCheckpoint)
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

// TestL2HashContract_HitsConvergedRow pins the single-row-per-hash
// semantic (nvsnap#106): the catalog enforces ONE row per content
// hash, so a transition write from the producer lands on the single
// converged row. The resolver finds exactly one row by hash, and its
// L2 state must reflect the write.
func TestL2HashContract_HitsConvergedRow(t *testing.T) {
	w, catalog, done := newL2ContractRig(t)
	defer done()

	if err := catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: l2ContractCheckpoint, CheckpointID: l2ContractCheckpoint,
		Namespace: "nvsnap-system", PodName: "vllm", NodeName: "node-A",
		Status: "Completed",
		Hash:   l2ContractHash,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := w.UpdatePVCPromoteState(l2ContractHash, "ready", "rox-a4f7818605da321e"); err != nil {
		t.Fatalf("transition ready: %v", err)
	}

	rows, err := catalog.ListByHash(l2ContractHash)
	if err != nil {
		t.Fatalf("ListByHash: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListByHash returned %d rows, want 1 (hash uniqueness, nvsnap#106)", len(rows))
	}
	got := rows[0]
	if got.PVCName != "rox-a4f7818605da321e" || got.PVCPromoteState != "ready" {
		t.Errorf("converged row: pvc_name=%q state=%q, want ready/rox-a4f7818605da321e",
			got.PVCName, got.PVCPromoteState)
	}
}

// TestL2HashContract_404OnUnknownHash exercises the failure surface
// the producer sees when the catalog has no row for the hash —
// either because /register hasn't run yet, or the row was deleted
// out from under an in-flight Backend.Put. The producer must observe
// a real non-nil error so the Backend can mark state=failed and log.
func TestL2HashContract_404OnUnknownHash(t *testing.T) {
	w, _, done := newL2ContractRig(t)
	defer done()

	err := w.UpdatePVCPromoteState(l2ContractHash, "ready", "rox-a4f7818605da321e")
	if err == nil {
		t.Fatal("expected error from missing-row 404, got nil")
	}
}
