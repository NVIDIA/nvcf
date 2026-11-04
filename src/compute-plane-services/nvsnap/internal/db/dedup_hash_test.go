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

package db

import "testing"

const testHash = "3c64187da0ab80d1e2d6f3744df6243d5373b8ad8dd21bd580383b7904c04e1f"

// The partial UNIQUE index (migration 6) is the hard backstop: the store
// must reject a second row for one content hash even on a raw insert that
// bypasses the Go convergence helpers.
func TestHashUniqueIndex_RejectsRawDuplicate(t *testing.T) {
	d := openTestDB(t)
	const ins = `INSERT INTO checkpoints (id, checkpoint_id, pod_name, node_name, hash)
		VALUES (?, ?, 'p', 'n', ?)`
	if _, err := d.db.Exec(ins, "id-1", "id-1", testHash); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := d.db.Exec(ins, "id-2", "id-2", testHash); err == nil {
		t.Fatal("second insert with the same hash succeeded; UNIQUE(hash) backstop missing")
	} else if !isHashUniqueViolation(err) {
		t.Fatalf("second insert failed with unexpected error: %v", err)
	}
}

// The unique index is PARTIAL (WHERE hash <> ”), so hash-less rows are
// exempt — multiple in-progress/legacy rows with empty hash must coexist
// and must NOT be collapsed into one.
func TestHashUniqueIndex_EmptyHashExempt(t *testing.T) {
	d := openTestDB(t)
	for _, id := range []string{"stub-a", "stub-b"} {
		if err := d.CreateCheckpoint(&Checkpoint{
			ID: id, CheckpointID: id, Namespace: "ns", PodName: "p", NodeName: "n",
			Status: "InProgress", // CreateCheckpoint never sets hash
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, id := range []string{"stub-a", "stub-b"} {
		if _, err := d.GetCheckpoint(id); err != nil {
			t.Fatalf("empty-hash row %s should survive: %v", id, err)
		}
	}
}

// Completing a stub whose hash is already owned by a canonical row folds
// the completion into that row and drops the stub, rather than colliding
// on the UNIQUE(hash) backstop (the InProgress-stub + agent-register-row
// lifecycle that produced the original duplicate).
func TestUpdateCheckpointStatus_FoldsIntoCanonicalRow(t *testing.T) {
	d := openTestDB(t)

	// Canonical row already exists for the hash (e.g. agent register).
	if err := d.UpsertCheckpoint(&Checkpoint{
		ID: testHash, CheckpointID: testHash, Namespace: "ns", PodName: "p",
		NodeName: "n", Status: "Completed", Hash: testHash, CheckpointSize: 100,
	}); err != nil {
		t.Fatalf("canonical upsert: %v", err)
	}
	// A separate in-progress stub (server createCheckpoint path: hash empty).
	if err := d.CreateCheckpoint(&Checkpoint{
		ID: "p-1782232235", CheckpointID: "p-1782232235", Namespace: "ns",
		PodName: "p", NodeName: "n", Status: "InProgress",
	}); err != nil {
		t.Fatalf("stub create: %v", err)
	}

	// Completing the stub with the same hash must fold, not duplicate.
	if err := d.UpdateCheckpointStatus("p-1782232235", "Completed", "done",
		"/var/lib/nvsnap/checkpoints/p-1782232235", testHash, 200, 12.5); err != nil {
		t.Fatalf("complete stub: %v", err)
	}

	rows, err := d.ListByHash(testHash)
	if err != nil {
		t.Fatalf("list by hash: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows for hash = %d, want 1 (stub folded into canonical)", len(rows))
	}
	if _, err := d.GetCheckpoint("p-1782232235"); err == nil {
		t.Error("stub row still present; should have been folded + deleted")
	}
	// Completion data landed on the surviving row.
	if rows[0].CheckpointSize != 200 || rows[0].Status != "Completed" {
		t.Errorf("folded row size=%d status=%q, want 200/Completed", rows[0].CheckpointSize, rows[0].Status)
	}
}
