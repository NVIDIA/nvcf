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

import (
	"testing"
	"time"
)

const hex64 = "9d29edbf548946de66dfb422e7de631b5f291b79598b8362e8aebb639cd7f0c4"

func TestLooksLikeHash(t *testing.T) {
	cases := map[string]bool{
		hex64: true,
		"0-sr-65aa1100-8fac-4e0f-97cc-92ac05bc5fea-1781124620": false, // NVCA pod id
		"":              false,
		"abc":           false,
		hex64[:63]:      false, // 63 chars
		hex64 + "a":     false, // 65 chars
		"g" + hex64[1:]: false, // non-hex char
	}
	for in, want := range cases {
		if got := looksLikeHash(in); got != want {
			t.Errorf("looksLikeHash(%q)=%v, want %v", in, got, want)
		}
	}
}

func TestNormalizeCheckpointHash(t *testing.T) {
	// hash-less row keyed by hash-as-id → hash backfilled from id.
	c := Checkpoint{ID: hex64, Hash: ""}
	normalizeCheckpointHash(&c)
	if c.Hash != hex64 {
		t.Errorf("hash not backfilled: %q", c.Hash)
	}
	// row keyed by pod-id with hash already set → unchanged.
	c2 := Checkpoint{ID: "0-sr-abc-123", Hash: hex64}
	normalizeCheckpointHash(&c2)
	if c2.Hash != hex64 {
		t.Errorf("hash should be unchanged: %q", c2.Hash)
	}
	// pod-id row, no hash → stays empty (nothing to recover).
	c3 := Checkpoint{ID: "0-sr-abc-123", Hash: ""}
	normalizeCheckpointHash(&c3)
	if c3.Hash != "" {
		t.Errorf("must not invent a hash: %q", c3.Hash)
	}
}

// The core fix: the two rows a rootfs capture produces — one keyed by the NVCA
// pod-id (hash column set), one keyed by hash-as-id (hash column empty) —
// collapse to ONE entry, keeping the Completed/sized row.
func TestDedupeCheckpointsByHash_CollapsesDoubleWrite(t *testing.T) {
	now := time.Now().UTC()
	rows := []Checkpoint{
		{ID: "0-sr-fv1-100", Hash: hex64, Status: "Completed", CheckpointSize: 57821845465, WorkloadType: "vllm", CreatedAt: now},
		{ID: hex64, Hash: "", Status: "Completed", CheckpointSize: 0, CreatedAt: now.Add(-time.Minute)}, // hash-id stub, hash backfilled to hex64 → same identity
	}
	out := DedupeCheckpointsByHash(rows)
	if len(out) != 1 {
		t.Fatalf("expected 1 deduped row, got %d", len(out))
	}
	if out[0].CheckpointSize != 57821845465 || out[0].WorkloadType != "vllm" {
		t.Errorf("kept the wrong (less-complete) row: %+v", out[0])
	}
}

func TestDedupeCheckpointsByHash_DistinctKept_NewestFirst(t *testing.T) {
	now := time.Now().UTC()
	h2 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rows := []Checkpoint{
		{ID: "a", Hash: hex64, Status: "Completed", CreatedAt: now.Add(-time.Hour)},
		{ID: "b", Hash: h2, Status: "Completed", CreatedAt: now},
	}
	out := DedupeCheckpointsByHash(rows)
	if len(out) != 2 {
		t.Fatalf("two distinct captures must both survive, got %d", len(out))
	}
	if out[0].Hash != h2 { // newest first
		t.Errorf("not sorted newest-first: %+v", out)
	}
}

// Detail lookup must resolve the same capture whether queried by the
// displayed (hash) id or the original pod-id, and return the complete row —
// the bug being "shows in catalog list but detail 404s".
func TestMatchCheckpointByIDOrHash(t *testing.T) {
	rows := []Checkpoint{
		{ID: "0-sr-fv1-100", Hash: hex64, Status: "Completed", CheckpointSize: 999, WorkloadType: "vllm"},
		{ID: hex64, Hash: "", Status: "Completed", CheckpointSize: 0}, // hash-as-id stub (hash backfilled on match)
	}
	// by hash (what the UI's deduped list shows as id) → complete row wins.
	if c, ok := MatchCheckpointByIDOrHash(rows, hex64); !ok || c.WorkloadType != "vllm" || c.CheckpointSize != 999 {
		t.Errorf("by-hash match wrong/absent: %+v ok=%v", c, ok)
	}
	// by original pod-id → same row.
	if c, ok := MatchCheckpointByIDOrHash(rows, "0-sr-fv1-100"); !ok || c.Hash != hex64 {
		t.Errorf("by-pod-id match wrong/absent: %+v ok=%v", c, ok)
	}
	// unknown id → not found (404).
	if _, ok := MatchCheckpointByIDOrHash(rows, "nope"); ok {
		t.Errorf("unknown id must not match")
	}
	// empty id → not found.
	if _, ok := MatchCheckpointByIDOrHash(rows, ""); ok {
		t.Errorf("empty id must not match")
	}
}

// A hash-less, non-hash-id row (e.g. a CRD stub keyed by pod-id with no hash)
// keys on its id so it isn't merged into an unrelated capture.
func TestDedupeCheckpointsByHash_HashlessKeysOnID(t *testing.T) {
	rows := []Checkpoint{
		{ID: "0-sr-x-1", Hash: ""},
		{ID: "0-sr-y-2", Hash: ""},
	}
	if out := DedupeCheckpointsByHash(rows); len(out) != 2 {
		t.Errorf("distinct hash-less rows must not merge: got %d", len(out))
	}
}

// TestGetCheckpoint_DerivesHashForLegacyRows guards the delete-resurrection
// fix: rows ingested by the rootfs Reconciler set id=hash but leave the hash
// column empty. GetCheckpoint must derive Hash from the id so the delete
// cascade's hash-keyed cleanup (capture manifest CM, L2 PVCs, siblings) runs
// instead of the id-only path that leaves the CM behind (→ Reconciler
// resurrects the row).
func TestGetCheckpoint_DerivesHashForLegacyRows(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()

	if cerr := d.CreateCheckpoint(&Checkpoint{ID: hex64, CheckpointID: hex64, Status: "Completed"}); cerr != nil {
		t.Fatalf("create: %v", cerr)
	}
	// Simulate a legacy row whose hash column was never populated.
	if _, eerr := d.db.Exec("UPDATE checkpoints SET hash = '' WHERE id = ?", hex64); eerr != nil {
		t.Fatalf("blank hash: %v", eerr)
	}

	got, err := d.GetCheckpoint(hex64)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Hash != hex64 {
		t.Errorf("Hash = %q, want it derived from id %q", got.Hash, hex64)
	}
}

// TestDeleteByHash_MatchesLegacyIDRows guards the second half of the
// delete-resurrection fix: legacy rows have an empty hash column (id=hash),
// so DeleteByHash must match the id too or the catalog row never deletes.
func TestDeleteByHash_MatchesLegacyIDRows(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()
	if cerr := d.CreateCheckpoint(&Checkpoint{ID: hex64, CheckpointID: hex64, Status: "Completed"}); cerr != nil {
		t.Fatalf("create: %v", cerr)
	}
	if _, eerr := d.db.Exec("UPDATE checkpoints SET hash = '' WHERE id = ?", hex64); eerr != nil {
		t.Fatalf("blank hash: %v", eerr)
	}
	n, err := d.DeleteByHash(hex64)
	if err != nil {
		t.Fatalf("DeleteByHash: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteByHash deleted %d rows, want 1 (legacy id match)", n)
	}
}

// TestLegacyRow_DeleteFlow is the interaction regression test for the
// delete-resurrection saga: it exercises the exact sequence the delete
// cascade runs — GetCheckpoint (which derives Hash from id) followed by
// DeleteByHash(derivedHash) — against a legacy empty-hash-column row, and
// asserts the row is actually removed. Either half alone passing wasn't
// enough: v0.0.24 fixed GetCheckpoint but DeleteByHash still matched only
// the empty column, so the row leaked (UI delete returned 204, row stayed).
func TestLegacyRow_DeleteFlow(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()
	if cerr := d.CreateCheckpoint(&Checkpoint{ID: hex64, CheckpointID: hex64, Status: "Completed"}); cerr != nil {
		t.Fatalf("create: %v", cerr)
	}
	if _, eerr := d.db.Exec("UPDATE checkpoints SET hash = '' WHERE id = ?", hex64); eerr != nil {
		t.Fatalf("blank hash: %v", eerr)
	}

	// 1. delete cascade reads the row...
	c, err := d.GetCheckpoint(hex64)
	if err != nil || c == nil {
		t.Fatalf("get: %v", err)
	}
	if c.Hash == "" {
		t.Fatal("GetCheckpoint left Hash empty — cascade would skip the hash path")
	}
	// 2. ...and deletes by the (derived) hash.
	if n, err := d.DeleteByHash(c.Hash); err != nil || n != 1 {
		t.Fatalf("DeleteByHash deleted %d (err=%v), want 1", n, err)
	}
	// 3. row is gone for good.
	if got, _ := d.GetCheckpoint(hex64); got != nil {
		t.Errorf("row survived delete — this is the leak that broke UI deletes")
	}
}
