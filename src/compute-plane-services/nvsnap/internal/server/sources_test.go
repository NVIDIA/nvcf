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

// newTestServerWithCatalog returns a Server with an in-memory catalog
// DB so peer-routing handlers can be exercised without K8s/agent
// fakes. Reuses newTestServer's K8s client setup but injects a real
// SQLite-in-memory catalog.
func newTestServerWithCatalog(t *testing.T) *Server {
	t.Helper()
	s := newTestServer()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open in-mem catalog: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s.catalog = d
	// Re-register routes so the catalog-aware handlers see s.catalog.
	s.setupRoutes()
	return s
}

func seedCheckpointForServer(t *testing.T, s *Server, id string) {
	t.Helper()
	if err := s.catalog.CreateCheckpoint(&db.Checkpoint{
		ID:           id,
		CheckpointID: id,
		Namespace:    "nvsnap-system",
		PodName:      "vllm-small",
		NodeName:     "node-A",
		Status:       "Completed",
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestPeerAddCheckpoint_RoundTrip — the agent registers itself, then
// /sources reflects the registration in one round-trip.
func TestPeerAddCheckpoint_RoundTrip(t *testing.T) {
	s := newTestServerWithCatalog(t)
	seedCheckpointForServer(t, s, "ckpt-x")

	body, _ := json.Marshal(peerRegisterRequest{
		NodeName: "node-A",
		AgentURL: "http://10.0.0.1:8081",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/ckpt-x/peer-add", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("peer-add status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// Now /sources should list it.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/ckpt-x/sources", http.NoBody)
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("sources status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got sourcesResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.CheckpointID != "ckpt-x" {
		t.Errorf("checkpoint_id = %q", got.CheckpointID)
	}
	if len(got.Peers) != 1 || got.Peers[0].NodeName != "node-A" || got.Peers[0].AgentURL != "http://10.0.0.1:8081" {
		t.Errorf("peers = %+v, want [{node-A, http://10.0.0.1:8081}]", got.Peers)
	}
	if got.BlobURI != "" {
		t.Errorf("blob_uri = %q, want empty (5d.1 only)", got.BlobURI)
	}
}

// TestPeerAddCheckpoint_UnknownCheckpoint — registering for a
// non-existent checkpoint must 404, not silently insert. Catches
// agents with stale local state racing the catalog.
func TestPeerAddCheckpoint_UnknownCheckpoint(t *testing.T) {
	s := newTestServerWithCatalog(t)
	body, _ := json.Marshal(peerRegisterRequest{
		NodeName: "node-A",
		AgentURL: "http://10.0.0.1:8081",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/ghost/peer-add", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestPeerRemoveCheckpoint_Idempotent — removing twice succeeds.
// Required because LRU eviction may race with the catalog's own
// health-check sweep that already pruned the entry.
func TestPeerRemoveCheckpoint_Idempotent(t *testing.T) {
	s := newTestServerWithCatalog(t)
	seedCheckpointForServer(t, s, "ckpt-y")

	// Add then remove twice.
	addBody, _ := json.Marshal(peerRegisterRequest{NodeName: "node-A", AgentURL: "http://x"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/ckpt-y/peer-add", bytes.NewReader(addBody))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("peer-add: %d", rr.Code)
	}

	rmBody, _ := json.Marshal(peerRegisterRequest{NodeName: "node-A"})
	for i := 0; i < 2; i++ {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/ckpt-y/peer-remove", bytes.NewReader(rmBody))
		rr = httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("peer-remove (attempt %d): status = %d, body = %s", i, rr.Code, rr.Body.String())
		}
	}
}

// TestGetCheckpointSources_NoPeers — fresh checkpoint, no peers
// registered. Cascade gets back an empty list and empty blob URI;
// the receiver surfaces the failure. NOT a 500.
func TestGetCheckpointSources_NoPeers(t *testing.T) {
	s := newTestServerWithCatalog(t)
	seedCheckpointForServer(t, s, "ckpt-z")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/ckpt-z/sources", http.NoBody)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got sourcesResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Peers) != 0 {
		t.Errorf("want 0 peers, got %d", len(got.Peers))
	}
}

// TestRegisterCheckpoint_HappyPath — the agent's post-capture
// registration call. After this, peer-add / blob-uploaded /
// /sources all have a row to anchor on. Idempotent on re-call.
func TestRegisterCheckpoint_HappyPath(t *testing.T) {
	s := newTestServerWithCatalog(t)

	body, _ := json.Marshal(registerCheckpointRequest{
		CheckpointID:   "ckpt-register-1",
		Namespace:      "nvsnap-system",
		PodName:        "vllm-small",
		ContainerName:  "vllm",
		ContainerImage: "vllm/vllm-openai:v0.11.2",
		NodeName:       "node-A",
		CheckpointSize: 30 << 30,
		Status:         "Completed",
		HasGPU:         true,
		DurationSecs:   181.5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/register", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// Verify row exists + later catalog ops work against it.
	got, err := s.catalog.GetCheckpoint("ckpt-register-1")
	if err != nil {
		t.Fatalf("GetCheckpoint after register: %v", err)
	}
	if got.PodName != "vllm-small" || got.NodeName != "node-A" || got.Status != "Completed" {
		t.Errorf("row = %+v", got)
	}
	if got.CheckpointSize != 30<<30 {
		t.Errorf("size = %d, want %d", got.CheckpointSize, 30<<30)
	}
}

// TestRegisterCheckpoint_Idempotent — re-registering the same ID
// updates the row (treat as latest known truth from the agent).
// Critical for agent restarts mid-upload that retry the register.
func TestRegisterCheckpoint_Idempotent(t *testing.T) {
	s := newTestServerWithCatalog(t)

	post := func(size int64) int {
		body, _ := json.Marshal(registerCheckpointRequest{
			CheckpointID:   "ckpt-idem",
			Namespace:      "ns",
			PodName:        "pod",
			NodeName:       "node-A",
			CheckpointSize: size,
			Status:         "Completed",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/register", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)
		return rr.Code
	}
	if c := post(100); c != http.StatusNoContent {
		t.Fatalf("first register = %d", c)
	}
	if c := post(200); c != http.StatusNoContent {
		t.Fatalf("second register = %d", c)
	}
	got, _ := s.catalog.GetCheckpoint("ckpt-idem")
	if got == nil || got.CheckpointSize != 200 {
		t.Errorf("size = %d, want 200 (latest)", got.CheckpointSize)
	}
}

// TestRegisterCheckpoint_UnlocksLaterOps — after register, peer-add
// and blob-uploaded both succeed. This is the bug the fix
// addresses: previously they 404'd because no row existed.
func TestRegisterCheckpoint_UnlocksLaterOps(t *testing.T) {
	s := newTestServerWithCatalog(t)
	id := "ckpt-flow"

	regBody, _ := json.Marshal(registerCheckpointRequest{
		CheckpointID: id, Namespace: "ns", PodName: "pod", NodeName: "node-A", Status: "Completed",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/register", bytes.NewReader(regBody))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("register: %d", rr.Code)
	}

	// peer-add should now succeed (was the bug — failed with 404).
	addBody, _ := json.Marshal(peerRegisterRequest{NodeName: "node-A", AgentURL: "http://10.0.0.1:8081"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/"+id+"/peer-add", bytes.NewReader(addBody))
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("peer-add after register: %d (was 404 before fix)", rr.Code)
	}

	// blob-uploaded should also succeed.
	blobBody, _ := json.Marshal(blobUploadedRequest{BlobURI: "http://nvsnap-blobstore:9000"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/"+id+"/blob-uploaded", bytes.NewReader(blobBody))
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("blob-uploaded after register: %d (was 404 before fix)", rr.Code)
	}
}

// TestRegisterCheckpoint_BadRequest — missing required fields → 400.
func TestRegisterCheckpoint_BadRequest(t *testing.T) {
	s := newTestServerWithCatalog(t)
	cases := []string{
		`{not json`,
		`{}`,                                     // missing all
		`{"checkpoint_id":"x","namespace":"ns"}`, // missing pod_name + node_name
		`{"checkpoint_id":"x","namespace":"ns","pod_name":"p"}`, // missing node_name
	}
	for i, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/register", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("case %d: status = %d, want 400", i, rr.Code)
		}
	}
}

// TestBlobUploaded_HappyPath — agent uploader's success callback.
// Sets s3_uri so /sources surfaces it as the tier-3 fallback.
func TestBlobUploaded_HappyPath(t *testing.T) {
	s := newTestServerWithCatalog(t)
	seedCheckpointForServer(t, s, "ckpt-blob")

	body, _ := json.Marshal(blobUploadedRequest{
		BlobURI: "http://nvsnap-blobstore.nvsnap-system.svc.cluster.local:9000",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/checkpoints/ckpt-blob/blob-uploaded", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// /sources should now surface the blob URI.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/ckpt-blob/sources", http.NoBody)
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	var got sourcesResponse
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if got.BlobURI == "" {
		t.Errorf("blob_uri empty after blob-uploaded callback")
	}
}

// TestBlobUploaded_BadRequest — missing blob_uri or malformed JSON →
// 400; checkpoint id unknown → 404. Catches agent bugs early.
func TestBlobUploaded_BadRequest(t *testing.T) {
	s := newTestServerWithCatalog(t)
	seedCheckpointForServer(t, s, "ckpt-known")

	cases := []struct {
		name, path, body string
		want             int
	}{
		{"unknown-id", "/api/v1/checkpoints/ghost/blob-uploaded",
			`{"blob_uri":"http://x"}`, http.StatusNotFound},
		{"empty-uri", "/api/v1/checkpoints/ckpt-known/blob-uploaded",
			`{"blob_uri":""}`, http.StatusBadRequest},
		{"missing-uri", "/api/v1/checkpoints/ckpt-known/blob-uploaded",
			`{}`, http.StatusBadRequest},
		{"bad-json", "/api/v1/checkpoints/ckpt-known/blob-uploaded",
			`{not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			rr := httptest.NewRecorder()
			s.router.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

// TestPeerAdd_BadBody — malformed JSON / missing required fields →
// 400 (not 500). Catches client bugs early without polluting catalog.
func TestPeerAdd_BadBody(t *testing.T) {
	s := newTestServerWithCatalog(t)
	seedCheckpointForServer(t, s, "ckpt-bad")

	cases := []struct {
		name string
		body string
	}{
		{"not-json", "{not json"},
		{"missing-node-name", `{"agent_url":"http://x"}`},
		{"missing-agent-url", `{"node_name":"node-A"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/ckpt-bad/peer-add", bytes.NewBufferString(tc.body))
			rr := httptest.NewRecorder()
			s.router.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rr.Code)
			}
		})
	}
}
