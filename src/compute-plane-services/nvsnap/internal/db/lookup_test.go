// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// nvsnap#59: content-addressed checkpoint lookup tests.

package db

import (
	"testing"
	"time"
)

// seedCatalogRow inserts a fully-populated CatalogInfo row for tests.
// Keeps the per-test boilerplate small.
func seedCatalogRow(t *testing.T, d *DB, id string, c Checkpoint) {
	t.Helper()
	c.ID = id
	c.CheckpointID = id
	if c.Status == "" {
		c.Status = "Completed"
	}
	if c.PodName == "" {
		c.PodName = "p1"
	}
	if c.NodeName == "" {
		c.NodeName = "node-1"
	}
	if c.Namespace == "" {
		c.Namespace = "ns1"
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if err := d.UpsertCheckpoint(&c); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestLookupCheckpoints_ImageRefRequired(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.LookupCheckpoints(LookupCriteria{}); err == nil {
		t.Fatal("expected error when ImageRef empty")
	}
}

func TestLookupCheckpoints_HappyPath(t *testing.T) {
	d := openTestDB(t)

	// One matching row + one with a different image.
	seedCatalogRow(t, d, "ck-match", Checkpoint{
		Hash:        "deadbeef",
		ImageRef:    "nvcr.io/foo:1.2",
		ImageDigest: "sha256:abc",
		ModelID:     "meta/llama",
		EngineFlags: []string{"--port", "8000"},
		DriverMajor: 550,
	})
	seedCatalogRow(t, d, "ck-other-image", Checkpoint{
		Hash:        "feedface",
		ImageRef:    "nvcr.io/other:1.0",
		ModelID:     "meta/llama",
		EngineFlags: []string{"--port", "8000"},
		DriverMajor: 550,
	})

	matches, err := d.LookupCheckpoints(LookupCriteria{
		ImageRef:    "nvcr.io/foo:1.2",
		ModelID:     "meta/llama",
		EngineFlags: []string{"--port", "8000"},
		DriverMajor: 550,
	})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].Hash != "deadbeef" {
		t.Errorf("hash = %q, want deadbeef", matches[0].Hash)
	}
}

func TestLookupCheckpoints_NoMatch(t *testing.T) {
	d := openTestDB(t)
	seedCatalogRow(t, d, "ck-1", Checkpoint{
		Hash:     "deadbeef",
		ImageRef: "nvcr.io/foo:1.2",
		ModelID:  "meta/llama",
	})
	// Different image.
	matches, err := d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/bar:1.0"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0", len(matches))
	}
}

func TestLookupCheckpoints_OnlyCompletedRowsWithHash(t *testing.T) {
	d := openTestDB(t)

	// Completed + hash → match.
	seedCatalogRow(t, d, "ck-good", Checkpoint{
		Hash: "hash-good", ImageRef: "nvcr.io/foo:1.2", Status: "Completed",
	})
	// Completed but empty hash → must NOT match (can't restore from it).
	seedCatalogRow(t, d, "ck-no-hash", Checkpoint{
		Hash: "", ImageRef: "nvcr.io/foo:1.2", Status: "Completed",
	})
	// In-progress with hash → must NOT match (might be abandoned).
	seedCatalogRow(t, d, "ck-inprog", Checkpoint{
		Hash: "hash-inprog", ImageRef: "nvcr.io/foo:1.2", Status: "InProgress",
	})

	matches, err := d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/foo:1.2"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].ID != "ck-good" {
		t.Errorf("got %s, want ck-good", matches[0].ID)
	}
}

func TestLookupCheckpoints_FreshestFirst(t *testing.T) {
	d := openTestDB(t)

	older := time.Now().UTC().Add(-2 * time.Hour)
	newer := time.Now().UTC().Add(-30 * time.Minute)

	seedCatalogRow(t, d, "ck-old", Checkpoint{
		Hash: "hash-old", ImageRef: "nvcr.io/foo:1.2", CreatedAt: older,
	})
	seedCatalogRow(t, d, "ck-new", Checkpoint{
		Hash: "hash-new", ImageRef: "nvcr.io/foo:1.2", CreatedAt: newer,
	})

	matches, err := d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/foo:1.2"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	if matches[0].ID != "ck-new" {
		t.Errorf("matches[0] = %s, want ck-new (freshest first)", matches[0].ID)
	}
}

func TestLookupCheckpoints_EngineFlagsCanonicalMatch(t *testing.T) {
	d := openTestDB(t)

	// Seed with flags in one order — should still match a query that
	// passes them in different order, because both get canonicalized
	// to the same sorted JSON-array string.
	seedCatalogRow(t, d, "ck-flags", Checkpoint{
		Hash:        "h1",
		ImageRef:    "nvcr.io/foo:1.2",
		EngineFlags: []string{"--port", "8000", "--max-batches", "32"},
	})

	cases := []struct {
		name string
		req  []string
		want int
	}{
		{"same order", []string{"--port", "8000", "--max-batches", "32"}, 1},
		{"different order", []string{"--max-batches", "32", "--port", "8000"}, 1},
		{"missing a flag", []string{"--port", "8000"}, 0},
		{"extra flag", []string{"--port", "8000", "--max-batches", "32", "--extra"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matches, err := d.LookupCheckpoints(LookupCriteria{
				ImageRef:    "nvcr.io/foo:1.2",
				EngineFlags: tc.req,
			})
			if err != nil {
				t.Fatalf("lookup: %v", err)
			}
			if len(matches) != tc.want {
				t.Errorf("matches = %d, want %d", len(matches), tc.want)
			}
		})
	}
}

func TestLookupCheckpoints_ModelIDFilter(t *testing.T) {
	d := openTestDB(t)
	seedCatalogRow(t, d, "ck-llama", Checkpoint{
		Hash: "h-llama", ImageRef: "nvcr.io/foo:1.2", ModelID: "meta/llama",
	})
	seedCatalogRow(t, d, "ck-mistral", Checkpoint{
		Hash: "h-mistral", ImageRef: "nvcr.io/foo:1.2", ModelID: "mistral/7b",
	})

	matches, err := d.LookupCheckpoints(LookupCriteria{
		ImageRef: "nvcr.io/foo:1.2",
		ModelID:  "meta/llama",
	})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != "ck-llama" {
		t.Errorf("got %v, want only ck-llama", matches)
	}

	// Empty ModelID means "match any model" — both rows come back.
	all, _ := d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/foo:1.2"})
	if len(all) != 2 {
		t.Errorf("with empty ModelID got %d, want 2", len(all))
	}
}

func TestLookupCheckpoints_DriverMajorFilter(t *testing.T) {
	d := openTestDB(t)
	seedCatalogRow(t, d, "ck-d550", Checkpoint{
		Hash: "h-550", ImageRef: "nvcr.io/foo:1.2", DriverMajor: 550,
	})
	seedCatalogRow(t, d, "ck-d535", Checkpoint{
		Hash: "h-535", ImageRef: "nvcr.io/foo:1.2", DriverMajor: 535,
	})

	matches, _ := d.LookupCheckpoints(LookupCriteria{
		ImageRef:    "nvcr.io/foo:1.2",
		DriverMajor: 550,
	})
	if len(matches) != 1 || matches[0].DriverMajor != 550 {
		t.Errorf("driver filter failed: %+v", matches)
	}

	// DriverMajor=0 means "match any driver".
	all, _ := d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/foo:1.2"})
	if len(all) != 2 {
		t.Errorf("with DriverMajor=0 got %d, want 2", len(all))
	}
}

func TestLookupCheckpoints_LimitClamped(t *testing.T) {
	d := openTestDB(t)
	for i := 0; i < 15; i++ {
		seedCatalogRow(t, d, "ck-"+string(rune('a'+i)), Checkpoint{
			Hash:      "h-" + string(rune('a'+i)),
			ImageRef:  "nvcr.io/foo:1.2",
			CreatedAt: time.Now().UTC().Add(-time.Duration(i) * time.Minute),
		})
	}

	// Default limit is 10.
	matches, _ := d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/foo:1.2"})
	if len(matches) != 10 {
		t.Errorf("default limit: got %d, want 10", len(matches))
	}

	// Explicit limit 3.
	matches, _ = d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/foo:1.2", Limit: 3})
	if len(matches) != 3 {
		t.Errorf("limit=3: got %d, want 3", len(matches))
	}

	// Limit > 100 clamps to 100 — we only have 15 here so it returns all.
	matches, _ = d.LookupCheckpoints(LookupCriteria{ImageRef: "nvcr.io/foo:1.2", Limit: 999})
	if len(matches) != 15 {
		t.Errorf("limit=999 (clamped) on 15 rows: got %d, want 15", len(matches))
	}
}

func TestUpsertCheckpoint_PopulatesCatalogFields(t *testing.T) {
	d := openTestDB(t)

	c := &Checkpoint{
		ID:                "ck-cat",
		CheckpointID:      "ck-cat",
		Namespace:         "ns1",
		PodName:           "p1",
		NodeName:          "node-1",
		Status:            "Completed",
		Hash:              "85ec4d75",
		ImageRef:          "nvcr.io/foo:1.2",
		ImageDigest:       "sha256:abc",
		ModelID:           "meta/llama",
		EngineFlags:       []string{"--port", "8000"},
		GPUType:           "NVIDIA-H100-80GB-HBM3",
		GPUCount:          1,
		DriverVersion:     "550.90.07",
		CUDAVersion:       "12.4",
		CPUArchitecture:   "amd64",
		FunctionName:      "hhuxtest",
		FunctionVersionID: "fv-1",
	}
	if err := d.UpsertCheckpoint(c); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := d.GetCheckpoint("ck-cat")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Hash != "85ec4d75" {
		t.Errorf("Hash = %q", got.Hash)
	}
	if got.DriverMajor != 550 {
		t.Errorf("DriverMajor should be derived from DriverVersion; got %d", got.DriverMajor)
	}
	if got.ImageDigest != "sha256:abc" {
		t.Errorf("ImageDigest = %q", got.ImageDigest)
	}
	if got.GPUType != "NVIDIA-H100-80GB-HBM3" {
		t.Errorf("GPUType = %q", got.GPUType)
	}
	if len(got.EngineFlags) != 2 || got.EngineFlags[0] != "--port" || got.EngineFlags[1] != "8000" {
		t.Errorf("EngineFlags round-trip: got %v", got.EngineFlags)
	}
	if got.FunctionVersionID != "fv-1" {
		t.Errorf("FunctionVersionID = %q", got.FunctionVersionID)
	}
}

func TestUpsertCheckpoint_RefreshesCatalogFields(t *testing.T) {
	d := openTestDB(t)

	// Initial upsert with partial info (older agent).
	if err := d.UpsertCheckpoint(&Checkpoint{
		ID:           "ck-1",
		CheckpointID: "ck-1",
		Namespace:    "ns1",
		PodName:      "p1",
		NodeName:     "node-1",
		Status:       "Completed",
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Second upsert with full CatalogInfo (newer agent rebooted, sent updated payload).
	if err := d.UpsertCheckpoint(&Checkpoint{
		ID:            "ck-1",
		CheckpointID:  "ck-1",
		Namespace:     "ns1",
		PodName:       "p1",
		NodeName:      "node-1",
		Status:        "Completed",
		Hash:          "85ec4d75",
		ImageRef:      "nvcr.io/foo:1.2",
		DriverVersion: "550.90.07",
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, _ := d.GetCheckpoint("ck-1")
	if got.Hash != "85ec4d75" {
		t.Errorf("re-upsert didn't refresh Hash: %q", got.Hash)
	}
	if got.ImageRef != "nvcr.io/foo:1.2" {
		t.Errorf("re-upsert didn't refresh ImageRef: %q", got.ImageRef)
	}
	if got.DriverMajor != 550 {
		t.Errorf("re-upsert didn't refresh DriverMajor: %d", got.DriverMajor)
	}
}

func TestCanonicalizeEngineFlags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"strips --model token + sorts",
			[]string{"--port", "8000", "--model", "foo/bar", "--tp", "1"},
			[]string{"--port", "--tp", "1", "8000"},
		},
		{"strips --model= form",
			[]string{"--model=foo/bar", "--port=8000"},
			[]string{"--port=8000"},
		},
		{"strips --model-path",
			[]string{"--model-path", "/m", "--port", "8000"},
			[]string{"--port", "8000"},
		},
		{"empty in → empty out (not nil — for stable JSON)",
			nil,
			[]string{},
		},
		{"order-stable",
			[]string{"--b", "--a"},
			[]string{"--a", "--b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalizeEngineFlags(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len got %d, want %d (got=%v want=%v)", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("idx %d: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseDriverMajor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"550.90.07", 550},
		{"535", 535},
		{"", 0},
		{"notanumber", 0},
		{"550.bad", 550}, // partial parse OK on first segment
	}
	for _, tc := range cases {
		got := parseDriverMajor(tc.in)
		if got != tc.want {
			t.Errorf("parseDriverMajor(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
