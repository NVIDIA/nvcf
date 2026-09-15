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

// Tests for the L2 per-capture PVC state machine columns (nvsnap#63,
// nvsnap#76). Four invariants the rest of the L2 design hangs on:
//
//   1. The schema has the columns and they round-trip through Upsert/Get.
//   2. UpdatePVCPromoteStateByHash walks the state machine without
//      clobbering a Bound rox-<hash> on a subsequent "writing" /
//      "snapshotting" callback (COALESCE semantics, same shape as
//      nvsnap#61).
//   3. UpsertCheckpoint (the agent's register path) NEVER overwrites
//      pvc_name / pvc_promote_state. That's the contract that lets the
//      Backend's Put state machine own the L2 lifecycle, independent
//      of the agent's register call timing.
//   4. The write fans out across every row sharing a hash. The L2
//      artifact rox-<short-hash> is hash-keyed, and re-capture of the
//      same workload produces multiple rows that all reference the
//      same PVC — the state-machine write must touch all of them.

package db

import (
	"testing"
)

func TestPVCPromoteState_RoundTrip(t *testing.T) {
	d := openTestDB(t)

	// Seed a Completed checkpoint via Upsert (the agent register path).
	c := &Checkpoint{
		ID:           "ckpt-l2",
		CheckpointID: "ckpt-l2",
		Namespace:    "ns1",
		PodName:      "vllm",
		NodeName:     "node-1",
		Status:       "Completed",
		Hash:         "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b",
	}
	if err := d.UpsertCheckpoint(c); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Initial state: both columns empty (Backend hasn't run yet).
	got, err := d.GetCheckpoint("ckpt-l2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PVCName != "" || got.PVCPromoteState != "" {
		t.Errorf("fresh row should have empty L2 columns; got pvc_name=%q state=%q",
			got.PVCName, got.PVCPromoteState)
	}
}

func TestPVCPromoteStateByHash_LifecycleWalk(t *testing.T) {
	d := openTestDB(t)
	const hash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"
	_ = d.UpsertCheckpoint(&Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns1", PodName: "vllm", NodeName: "node-1",
		Status: "Completed",
		Hash:   hash,
	})

	// The Backend.Put state machine walks: pending → writing →
	// snapshotting → ready. pvc_name only appears on the final transition.
	for _, step := range []struct {
		state, pvcName string
		wantState      string
		wantPVCName    string
	}{
		{PVCPromoteStatePending, "", PVCPromoteStatePending, ""},
		{PVCPromoteStateWriting, "", PVCPromoteStateWriting, ""},
		{PVCPromoteStateSnapshotting, "", PVCPromoteStateSnapshotting, ""},
		{PVCPromoteStateReady, "rox-a4f7818605da321e", PVCPromoteStateReady, "rox-a4f7818605da321e"},
	} {
		n, err := d.UpdatePVCPromoteStateByHash(hash, step.state, step.pvcName)
		if err != nil {
			t.Fatalf("step %s: %v", step.state, err)
		}
		if n != 1 {
			t.Errorf("step %s: rows affected = %d, want 1", step.state, n)
		}
		got, err := d.GetCheckpoint("ckpt-l2")
		if err != nil {
			t.Fatalf("get after %s: %v", step.state, err)
		}
		if got.PVCPromoteState != step.wantState {
			t.Errorf("after %s: state = %q, want %q", step.state, got.PVCPromoteState, step.wantState)
		}
		if got.PVCName != step.wantPVCName {
			t.Errorf("after %s: pvc_name = %q, want %q", step.state, got.PVCName, step.wantPVCName)
		}
	}
}

// COALESCE invariant: a later state callback that doesn't carry the
// pvc_name (e.g., a "failed" transition after a transient retry-able
// snapshot error in a later iteration) must NOT wipe the rox-<hash>
// already written on a prior "ready" transition. Same shape as the
// nvsnap#61 COALESCE pattern on UpdateCheckpointStatus.
func TestPVCPromoteStateByHash_PreservesPVCNameOnEmptyUpdate(t *testing.T) {
	d := openTestDB(t)
	const hash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"
	_ = d.UpsertCheckpoint(&Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns1", PodName: "vllm", NodeName: "node-1",
		Status: "Completed",
		Hash:   hash,
	})

	_, _ = d.UpdatePVCPromoteStateByHash(hash, PVCPromoteStateReady, "rox-a4f78186")
	// Now a subsequent transition arrives with empty pvc_name — e.g.
	// a state=failed write from a later re-Put on the same hash.
	if _, err := d.UpdatePVCPromoteStateByHash(hash, PVCPromoteStateFailed, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := d.GetCheckpoint("ckpt-l2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PVCName != "rox-a4f78186" {
		t.Errorf("pvc_name got wiped on empty-name update; got %q, want preserved", got.PVCName)
	}
	if got.PVCPromoteState != PVCPromoteStateFailed {
		t.Errorf("state = %q, want %q (state IS overwritten — only pvc_name is preserved)",
			got.PVCPromoteState, PVCPromoteStateFailed)
	}
}

// UpsertCheckpoint MUST NOT touch the L2 columns. The agent's register
// path runs alongside the Backend.Put state machine — if Upsert
// clobbered pvc_promote_state, a re-registration from a retry would
// race against the writer's state writes.
func TestUpsertCheckpoint_PreservesPVCColumns(t *testing.T) {
	d := openTestDB(t)
	const hash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"
	_ = d.UpsertCheckpoint(&Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns1", PodName: "vllm", NodeName: "node-1",
		Status: "Completed",
		Hash:   hash,
	})
	_, _ = d.UpdatePVCPromoteStateByHash(hash, PVCPromoteStateReady, "rox-a4f78186")

	// Now the agent re-registers (e.g., on agent restart mid-capture).
	// The Upsert payload doesn't carry L2 fields.
	if err := d.UpsertCheckpoint(&Checkpoint{
		ID: "ckpt-l2", CheckpointID: "ckpt-l2",
		Namespace: "ns1", PodName: "vllm-still", NodeName: "node-1",
		Status: "Completed",
		Hash:   hash,
		// pvc_name + pvc_promote_state intentionally NOT set
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	got, _ := d.GetCheckpoint("ckpt-l2")
	if got.PVCName != "rox-a4f78186" {
		t.Errorf("Upsert clobbered pvc_name; got %q, want preserved", got.PVCName)
	}
	if got.PVCPromoteState != PVCPromoteStateReady {
		t.Errorf("Upsert clobbered pvc_promote_state; got %q, want %q",
			got.PVCPromoteState, PVCPromoteStateReady)
	}
	if got.PodName != "vllm-still" {
		t.Errorf("non-L2 columns SHOULD still update via Upsert; got pod_name = %q", got.PodName)
	}
}

// Hash-keyed fan-out: a re-capture of the same workload produces
// multiple catalog rows that share a content hash. They all reference
// the SAME L2 artifact (rox-<short-hash>) — so a single state-machine
// write must update every row. This is the contract the agent's
// Backend.Put relies on when it idempotently re-Puts a hash whose
// rox PVC is already Bound.
func TestPVCPromoteStateByHash_HitsConvergedRow(t *testing.T) {
	d := openTestDB(t)
	const hash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"

	// Two captures of the same workload — different ids, same hash.
	// nvsnap#106: the content hash is the identity, so these collapse to
	// a single row (the second upsert adopts the first's id) instead of
	// the duplicate siblings the catalog used to accumulate.
	for _, id := range []string{"a4f7__20260601-210932", "a4f7__20260601-211222"} {
		if err := d.UpsertCheckpoint(&Checkpoint{
			ID: id, CheckpointID: id,
			Namespace: "ns1", PodName: "vllm", NodeName: "node-1",
			Status: "Completed",
			Hash:   hash,
		}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}

	rows, err := d.ListByHash(hash)
	if err != nil {
		t.Fatalf("list by hash: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows for hash = %d, want 1 (converged to a single content-addressed row)", len(rows))
	}

	// The promote-state update hits the single converged row.
	n, err := d.UpdatePVCPromoteStateByHash(hash, PVCPromoteStateReady, "rox-a4f78186")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if n != 1 {
		t.Errorf("rows affected = %d, want 1 (single converged row)", n)
	}
	got, err := d.GetCheckpoint(rows[0].ID)
	if err != nil {
		t.Fatalf("get %s: %v", rows[0].ID, err)
	}
	if got.PVCName != "rox-a4f78186" || got.PVCPromoteState != PVCPromoteStateReady {
		t.Errorf("converged row %s: pvc_name=%q state=%q, want ready/rox-a4f78186",
			rows[0].ID, got.PVCName, got.PVCPromoteState)
	}
}

// 0 rows affected means no row matches the hash — the HTTP handler
// uses this to return 404 (caller's catalog write must have failed,
// or the row was deleted out from under the in-flight Backend.Put).
func TestPVCPromoteStateByHash_NoRowsForUnknownHash(t *testing.T) {
	d := openTestDB(t)
	n, err := d.UpdatePVCPromoteStateByHash("0000000000000000000000000000000000000000000000000000000000000000", PVCPromoteStateReady, "rox-nope")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if n != 0 {
		t.Errorf("rows affected = %d, want 0 (no row with that hash)", n)
	}
}
