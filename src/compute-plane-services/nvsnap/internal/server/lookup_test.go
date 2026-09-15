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
// nvsnap#59: HTTP handler tests for POST /api/v1/checkpoints/lookup.

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

func seedLookupRow(t *testing.T, s *Server, id string, c db.Checkpoint) {
	t.Helper()
	c.ID = id
	c.CheckpointID = id
	if c.Status == "" {
		c.Status = "Completed"
	}
	if c.Namespace == "" {
		c.Namespace = "ns1"
	}
	if c.PodName == "" {
		c.PodName = "p1"
	}
	if c.NodeName == "" {
		c.NodeName = "node-1"
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if err := s.catalog.UpsertCheckpoint(&c); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func postLookup(t *testing.T, s *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/lookup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	return rr
}

func TestLookupCheckpoint_HappyPath(t *testing.T) {
	s := newTestServerWithCatalog(t)
	seedLookupRow(t, s, "ck-match", db.Checkpoint{
		Hash:          "85ec4d75",
		ImageRef:      "nvcr.io/foo:1.2",
		ImageDigest:   "sha256:abc",
		ModelID:       "meta/llama",
		EngineFlags:   []string{"--port", "8000"},
		DriverVersion: "550.90.07",
		GPUType:       "NVIDIA-H100-80GB-HBM3",
	})

	rr := postLookup(t, s, lookupCheckpointRequest{
		ImageRef:    "nvcr.io/foo:1.2",
		ModelID:     "meta/llama",
		EngineFlags: []string{"--port", "8000"},
		DriverMajor: 550,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp lookupCheckpointResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Fatalf("matches=%d, want 1; body=%s", len(resp.Matches), rr.Body.String())
	}
	m := resp.Matches[0]
	if m.Hash != "85ec4d75" || m.CheckpointID != "ck-match" {
		t.Errorf("hash/id wrong: %+v", m)
	}
	if m.GPUType != "NVIDIA-H100-80GB-HBM3" || m.DriverVersion != "550.90.07" {
		t.Errorf("display fields missing: %+v", m)
	}
}

func TestLookupCheckpoint_NoMatchReturnsEmptyList(t *testing.T) {
	s := newTestServerWithCatalog(t)
	rr := postLookup(t, s, lookupCheckpointRequest{ImageRef: "nvcr.io/nothing:1.0"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty matches != 404)", rr.Code)
	}
	var resp lookupCheckpointResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	// must be empty array, not null, so JSON clients can iterate without nil check
	if resp.Matches == nil {
		t.Error("matches should be empty array []; got nil/null")
	}
	if len(resp.Matches) != 0 {
		t.Errorf("matches=%d, want 0", len(resp.Matches))
	}
}

func TestLookupCheckpoint_MissingImageRefIs400(t *testing.T) {
	s := newTestServerWithCatalog(t)
	rr := postLookup(t, s, lookupCheckpointRequest{ModelID: "meta/llama"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestLookupCheckpoint_InvalidJSON(t *testing.T) {
	s := newTestServerWithCatalog(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/lookup",
		bytes.NewReader([]byte("not json")))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on invalid JSON", rr.Code)
	}
}

func TestLookupCheckpoint_FreshestFirst(t *testing.T) {
	s := newTestServerWithCatalog(t)

	older := time.Now().UTC().Add(-2 * time.Hour)
	newer := time.Now().UTC().Add(-30 * time.Minute)

	seedLookupRow(t, s, "ck-old", db.Checkpoint{
		Hash: "hash-old", ImageRef: "nvcr.io/foo:1.2", CreatedAt: older,
	})
	seedLookupRow(t, s, "ck-new", db.Checkpoint{
		Hash: "hash-new", ImageRef: "nvcr.io/foo:1.2", CreatedAt: newer,
	})

	rr := postLookup(t, s, lookupCheckpointRequest{ImageRef: "nvcr.io/foo:1.2"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp lookupCheckpointResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Matches) != 2 || resp.Matches[0].CheckpointID != "ck-new" {
		t.Errorf("expected ck-new first; got %+v", resp.Matches)
	}
}

// nvca#14 + nvsnap#59: rows with empty hash must not be returned (NVCA
// can't restore from "no-hash" — Hook A would stamp an empty
// nvsnap.io/restore-from annotation).
func TestLookupCheckpoint_RowsWithoutHashExcluded(t *testing.T) {
	s := newTestServerWithCatalog(t)

	seedLookupRow(t, s, "ck-good", db.Checkpoint{
		Hash: "hash-real", ImageRef: "nvcr.io/foo:1.2",
	})
	seedLookupRow(t, s, "ck-no-hash", db.Checkpoint{
		// empty Hash on purpose — pre-nvsnap#56 capture
		ImageRef: "nvcr.io/foo:1.2",
	})

	rr := postLookup(t, s, lookupCheckpointRequest{ImageRef: "nvcr.io/foo:1.2"})
	var resp lookupCheckpointResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Matches) != 1 || resp.Matches[0].CheckpointID != "ck-good" {
		t.Errorf("got %+v, want only ck-good", resp.Matches)
	}
}

// Round-trip: register a row with full CatalogInfo through the HTTP
// /register endpoint, then look it up via /lookup. Catches schema-vs-
// handler mismatches that pure DB tests would miss.
func TestRegister_Then_Lookup_RoundTrip(t *testing.T) {
	s := newTestServerWithCatalog(t)

	regBody, _ := json.Marshal(registerCheckpointRequest{
		CheckpointID:      "ck-rt",
		Namespace:         "nvcf-backend",
		PodName:           "0-sr-pod",
		NodeName:          "node-1",
		Status:            "Completed",
		Hash:              "85ec4d75ee57c1be444dd19733f63cfd",
		ImageRef:          "nvcr.io/.../foo:1.2",
		ImageDigest:       "sha256:deadbeef",
		ModelID:           "meta/llama-3.1-8b",
		EngineFlags:       []string{"--port", "8000"},
		DriverVersion:     "550.90.07",
		CUDAVersion:       "12.4",
		CPUArchitecture:   "amd64",
		FunctionName:      "hhuxtest",
		FunctionVersionID: "fv-cd111",
	})
	regReq := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/register",
		bytes.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regRR := httptest.NewRecorder()
	s.router.ServeHTTP(regRR, regReq)
	if regRR.Code != http.StatusNoContent {
		t.Fatalf("register: %d %s", regRR.Code, regRR.Body.String())
	}

	rr := postLookup(t, s, lookupCheckpointRequest{
		ImageRef:    "nvcr.io/.../foo:1.2",
		ModelID:     "meta/llama-3.1-8b",
		EngineFlags: []string{"--port", "8000"},
		DriverMajor: 550,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("lookup: %d %s", rr.Code, rr.Body.String())
	}
	var resp lookupCheckpointResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Matches) != 1 {
		t.Fatalf("matches=%d, want 1", len(resp.Matches))
	}
	m := resp.Matches[0]
	if m.Hash != "85ec4d75ee57c1be444dd19733f63cfd" {
		t.Errorf("Hash = %q", m.Hash)
	}
	if m.ImageDigest != "sha256:deadbeef" {
		t.Errorf("ImageDigest = %q", m.ImageDigest)
	}
}
