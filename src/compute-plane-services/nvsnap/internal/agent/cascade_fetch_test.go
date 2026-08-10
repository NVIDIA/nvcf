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

package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// shaHex computes sha256 hex of b — handy in cascade tests for
// constructing matched (sha, body) pairs for the fake blob store.
func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// newPeerAgent stands up a real httptest.Server backed by a fully-
// populated checkpoint dir on disk. Tests use this as the "source of
// truth" peer that EnsureLocal should fetch from.
func newPeerAgent(t *testing.T) (peerURL, checkpointID string, total int64) {
	t.Helper()
	dir := t.TempDir()
	id, totalSize, _ := makeCheckpointDir(t, dir)
	a := newTestAgent(t, dir)
	router := mux.NewRouter()
	router.HandleFunc("/v1/checkpoints/{id}/manifest", a.checkpointManifestHandler).Methods("GET")
	router.HandleFunc("/v1/checkpoints/{id}/file", a.readCheckpointFileHandler).Methods("GET")
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv.URL, id, totalSize
}

// fakeCatalog returns a lightweight nvsnap-server stand-in. Its sole
// job is to serve /api/v1/checkpoints/{id}/sources with the supplied
// peer list and absorb /peer-add posts. The peerAdds counter lets
// tests verify the receiver registered itself after a successful
// fetch (the load-bearing 5d.1 invariant: every successful restore
// expands the fanout fan).
func fakeCatalog(t *testing.T, peers []map[string]string, blobURI string) (catalogURL string, peerAdds *int32) {
	t.Helper()
	var added int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/checkpoints/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sources") && r.Method == http.MethodGet:
			parts := strings.Split(r.URL.Path, "/")
			id := parts[len(parts)-2]
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"checkpoint_id": id,
				"peers":         peers,
				"blob_uri":      blobURI,
			})
		case strings.HasSuffix(r.URL.Path, "/peer-add") && r.Method == http.MethodPost:
			atomic.AddInt32(&added, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &added
}

// receiverAgent constructs an Agent configured as a fetcher (empty
// CheckpointDir, catalog URL set, NodeIP/NodeName populated so
// peer-add can build a self URL). This is the agent under test in
// every cascade scenario.
func receiverAgent(t *testing.T, catalogURL string) (agent *Agent, dumpDir string) {
	t.Helper()
	dir := t.TempDir()
	log := logrus.New()
	log.SetOutput(io.Discard)
	a := &Agent{log: log}
	a.config.CheckpointDir = dir
	a.config.CatalogURL = catalogURL
	a.config.NodeName = "receiver-node"
	a.config.NodeIP = "10.0.0.99"
	a.config.ListenAddr = ":8081"
	return a, dir
}

// TestEnsureLocal_SameNodeShortCircuit — checkpoint already on disk
// (capture-source node, or a previous restore deposited it). Cascade
// must NOT call /sources (no peer iteration needed). It IS allowed
// to fire peer-add as a self-heal in case the catalog forgot us
// during an agent restart.
func TestEnsureLocal_SameNodeShortCircuit(t *testing.T) {
	var sourcesCalls int32
	tripwire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sources"):
			atomic.AddInt32(&sourcesCalls, 1)
			t.Errorf("unexpected /sources call %s — same-node should short-circuit cascade", r.URL.Path)
			http.Error(w, "should not be called", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/peer-add"):
			// Allowed: self-heal registration on every successful
			// EnsureLocal so the catalog converges on truth.
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer tripwire.Close()

	a, dir := receiverAgent(t, tripwire.URL)
	id, _, _ := makeCheckpointDir(t, dir)

	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}
	if atomic.LoadInt32(&sourcesCalls) != 0 {
		t.Errorf("/sources called %d times; same-node should short-circuit", sourcesCalls)
	}
	// inventory.img must still exist (we didn't blow it away).
	if _, err := os.Stat(filepath.Join(dir, id, "inventory.img")); err != nil {
		t.Errorf("local file gone after EnsureLocal: %v", err)
	}
}

// TestEnsureLocal_PeerSuccess — cascade's happy path. A single peer is
// alive; we fetch from it, dest dir mirrors the source, peer-add fires.
func TestEnsureLocal_PeerSuccess(t *testing.T) {
	peerURL, id, peerTotal := newPeerAgent(t)

	catalogURL, peerAdds := fakeCatalog(t, []map[string]string{
		{"node_name": "node-A", "agent_url": peerURL},
	}, "")

	a, dir := receiverAgent(t, catalogURL)
	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}

	// Walk dest dir and sum bytes; must equal peer's total.
	var got int64
	_ = filepath.Walk(filepath.Join(dir, id), func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			got += info.Size()
		}
		return nil
	})
	if got != peerTotal {
		t.Errorf("dest total = %d, want %d", got, peerTotal)
	}

	if atomic.LoadInt32(peerAdds) != 1 {
		t.Errorf("peer-add fired %d times, want 1 (every successful fetch must register self)", atomic.LoadInt32(peerAdds))
	}

	// Spot-check nested file landed in the right place.
	if _, err := os.Stat(filepath.Join(dir, id, "rootfs-diff", "etc", "hostname")); err != nil {
		t.Errorf("nested file missing: %v", err)
	}
}

// TestEnsureLocal_PeerFallover — first peer is unreachable, second
// peer succeeds. Validates the load-bearing claim that one dead node
// in the catalog doesn't take down restore.
func TestEnsureLocal_PeerFallover(t *testing.T) {
	goodURL, id, _ := newPeerAgent(t)

	// First peer points at a closed listener — connection refused.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close() // immediate close: future GETs get connection refused

	catalogURL, peerAdds := fakeCatalog(t, []map[string]string{
		{"node_name": "dead-node", "agent_url": deadURL},
		{"node_name": "good-node", "agent_url": goodURL},
	}, "")

	a, dir := receiverAgent(t, catalogURL)
	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v (expected fallover to good peer)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id, "inventory.img")); err != nil {
		t.Errorf("inventory.img missing after fallover: %v", err)
	}
	if atomic.LoadInt32(peerAdds) != 1 {
		t.Errorf("peer-add count = %d, want 1", atomic.LoadInt32(peerAdds))
	}
}

// TestEnsureLocal_AllPeersFail — every peer dead, no blob URI. Cascade
// must surface a clear error, NOT silently leave a partial dir behind.
func TestEnsureLocal_AllPeersFail(t *testing.T) {
	deadSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close()

	id := "ckpt-allfail"
	catalogURL, peerAdds := fakeCatalog(t, []map[string]string{
		{"node_name": "dead-A", "agent_url": deadURL},
		{"node_name": "dead-B", "agent_url": deadURL + "0"}, // also unreachable
	}, "")

	a, dir := receiverAgent(t, catalogURL)
	err := a.EnsureLocal(t.Context(), id)
	if err == nil {
		t.Fatal("EnsureLocal: want error, got nil")
	}
	if !strings.Contains(err.Error(), "all 2 peers failed") {
		t.Errorf("error = %q, should mention 'all 2 peers failed'", err.Error())
	}
	if atomic.LoadInt32(peerAdds) != 0 {
		t.Errorf("peer-add fired %d times after total failure; want 0", atomic.LoadInt32(peerAdds))
	}
	// Critically: no partial dest dir left. (Avoids future runs
	// thinking we already have it.)
	if _, err := os.Stat(filepath.Join(dir, id)); err == nil {
		t.Errorf("dest dir left behind after total failure")
	}
}

// fakeBlobStore stands up an httptest server that mimics
// nvsnap-blobstore: serves manifest + per-sha blobs from an
// in-memory map. Stage 5d.2 cascade tier-3 fallback hits this
// when no peer can serve.
func fakeBlobStore(t *testing.T, blobs map[string][]byte, manifest blobStoreManifest) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/capture/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest)
	})
	mux.HandleFunc("/v1/blob/", func(w http.ResponseWriter, r *http.Request) {
		sha := strings.TrimPrefix(r.URL.Path, "/v1/blob/")
		body, ok := blobs[sha]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestEnsureLocal_BlobStoreFallback — happy path for tier-3.
// All peers fail, catalog has a blob URI, blobstore serves
// manifest + blobs successfully → restored dir mirrors source.
func TestEnsureLocal_BlobStoreFallback(t *testing.T) {
	// Two blobs that compose a single capture.
	body1 := []byte("inv-data")
	body2 := []byte("pages-data")
	sha1 := shaHex(body1)
	sha2 := shaHex(body2)
	blobs := map[string][]byte{sha1: body1, sha2: body2}
	manifest := blobStoreManifest{}
	manifest.Files = []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}{
		{Path: "inventory.img", SHA256: sha1, Size: int64(len(body1))},
		{Path: "pages-1.img", SHA256: sha2, Size: int64(len(body2))},
	}
	blobURL := fakeBlobStore(t, blobs, manifest)

	id := "ckpt-blob-fallback"
	catalogURL, peerAdds := fakeCatalog(t, []map[string]string{}, blobURL)

	a, dir := receiverAgent(t, catalogURL)
	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}

	got1, err := os.ReadFile(filepath.Join(dir, id, "inventory.img"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got1, body1) {
		t.Errorf("inventory.img mismatch: got %q want %q", got1, body1)
	}
	got2, err := os.ReadFile(filepath.Join(dir, id, "pages-1.img"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, body2) {
		t.Errorf("pages-1.img mismatch")
	}

	if atomic.LoadInt32(peerAdds) != 1 {
		t.Errorf("peer-add count = %d, want 1 (blob fallback should still register)", atomic.LoadInt32(peerAdds))
	}
}

// TestEnsureLocal_BlobStoreManifestMissing — cascade reaches
// tier-3 but the blobstore doesn't have the manifest (capture
// was deleted between catalog query and our fetch). Error
// surfaced cleanly; partial dest dir cleaned up.
func TestEnsureLocal_BlobStoreManifestMissing(t *testing.T) {
	// Empty blobstore — every GET returns 404.
	mux := http.NewServeMux()
	mux.HandleFunc("/", http.NotFound)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	id := "ckpt-no-manifest"
	catalogURL, _ := fakeCatalog(t, []map[string]string{}, srv.URL)
	a, dir := receiverAgent(t, catalogURL)
	err := a.EnsureLocal(t.Context(), id)
	if err == nil {
		t.Fatal("want error from tier-3, got nil")
	}
	if !strings.Contains(err.Error(), "blob fallback failed") {
		t.Errorf("error = %q, should mention 'blob fallback failed'", err.Error())
	}
	if _, err := os.Stat(filepath.Join(dir, id)); err == nil {
		t.Errorf("partial dir left behind after blob fallback failure")
	}
}

// TestEnsureLocal_SkipsSelfInPeerList — catalog returns OUR own URL
// among peers (e.g., a stale entry from a prior restore on this node
// that lost its local copy). Cascade must skip and try real peers
// instead of fetching from itself.
func TestEnsureLocal_SkipsSelfInPeerList(t *testing.T) {
	goodURL, id, _ := newPeerAgent(t)

	// Build a catalog where the FIRST peer is "us". The receiver
	// agent's selfAgentURL = http://10.0.0.99:8081 (from receiverAgent
	// helper).
	catalogURL, peerAdds := fakeCatalog(t, []map[string]string{
		{"node_name": "receiver-node", "agent_url": "http://10.0.0.99:8081"},
		{"node_name": "real-peer", "agent_url": goodURL},
	}, "")

	a, dir := receiverAgent(t, catalogURL)
	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id, "inventory.img")); err != nil {
		t.Errorf("inventory.img missing — should have fetched from real peer: %v", err)
	}
	if atomic.LoadInt32(peerAdds) != 1 {
		t.Errorf("peer-add count = %d, want 1", atomic.LoadInt32(peerAdds))
	}
}

// TestEnsureLocalHandler_PropagatesCascadeFailure — the HTTP wrapper
// that the restore caller hits. Cascade error → 502 with the error
// in the body so the caller can log it. Same-node hit → 204.
func TestEnsureLocalHandler_PropagatesCascadeFailure(t *testing.T) {
	// No peers, no blob URI: cascade must fail and the handler must
	// translate that into 502.
	id := "ckpt-handler-fail"
	catalogURL, _ := fakeCatalog(t, []map[string]string{}, "")
	a, _ := receiverAgent(t, catalogURL)

	router := mux.NewRouter()
	router.HandleFunc("/v1/checkpoints/{id}/ensure-local", a.ensureLocalHandler).Methods("POST")
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := httpPostEmpty(srv.URL + "/v1/checkpoints/" + id + "/ensure-local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for cascade failure", resp.StatusCode)
	}
}

// TestEnsureLocalHandler_SameNodeReturns204 — handler is the routing
// surface restore-side test-e2e calls; same-node short-circuit must
// be observable as a fast 204 (no body) so callers can `curl -f`.
func TestEnsureLocalHandler_SameNodeReturns204(t *testing.T) {
	catalogURL, _ := fakeCatalog(t, []map[string]string{}, "")
	a, dir := receiverAgent(t, catalogURL)
	id, _, _ := makeCheckpointDir(t, dir)

	router := mux.NewRouter()
	router.HandleFunc("/v1/checkpoints/{id}/ensure-local", a.ensureLocalHandler).Methods("POST")
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := httpPostEmpty(srv.URL + "/v1/checkpoints/" + id + "/ensure-local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

// TestSelfAgentURL_DerivesPort — port comes from ListenAddr's :NNNN
// suffix; missing NodeIP yields empty (defensive — peer-add bails).
func TestSelfAgentURL_DerivesPort(t *testing.T) {
	tests := []struct {
		nodeIP, listenAddr, want string
	}{
		{"10.0.0.1", ":8081", "http://10.0.0.1:8081"},
		{"10.0.0.1", ":9090", "http://10.0.0.1:9090"},
		{"", ":8081", ""},
		{"10.0.0.1", "", "http://10.0.0.1:8081"}, // empty addr → default 8081
	}
	for _, tc := range tests {
		a := &Agent{}
		a.config.NodeIP = tc.nodeIP
		a.config.ListenAddr = tc.listenAddr
		got := a.selfAgentURL()
		if got != tc.want {
			t.Errorf("NodeIP=%q ListenAddr=%q → %q, want %q", tc.nodeIP, tc.listenAddr, got, tc.want)
		}
	}
}
