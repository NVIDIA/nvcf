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

// Tests for cascading deleteCheckpoint behavior — origin agent +
// peer agent hostpaths + nvsnap-blobstore manifest. The bug this
// closes: UI delete left orphaned blobs and peer caches behind,
// silently growing storage costs.

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// fakeAgent is a per-test stand-in for the nvsnap-agent endpoint —
// answers DELETE /v1/checkpoints/{id}, records the call.
type fakeAgent struct {
	mu     sync.Mutex
	hits   []string
	status int
}

func newFakeAgent(status int) *fakeAgent {
	return &fakeAgent{status: status}
}

func (fa *fakeAgent) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		fa.mu.Lock()
		fa.hits = append(fa.hits, r.URL.Path)
		fa.mu.Unlock()
		w.WriteHeader(fa.status)
	}
}

// failingPeerTier registers a peer agent for checkpointID that answers
// every DELETE with status, so a cascade has one tier that fails.
func failingPeerTier(t *testing.T, s *Server, checkpointID string, status int) {
	t.Helper()
	fa := newFakeAgent(status)
	srv := httptest.NewServer(fa.handler())
	t.Cleanup(srv.Close)
	host, port, _ := splitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	s.config.AgentPort = mustAtoi(port)
	if _, err := s.kubeClient.CoreV1().Nodes().Create(context.Background(),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "peer-fail-" + checkpointID},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}}}},
		metav1.CreateOptions{}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := s.catalog.AddPeer(checkpointID, "peer-fail-"+checkpointID, "http://peer-fail:8081"); err != nil {
		t.Fatalf("peer-add: %v", err)
	}
}

func TestCascadeDelete_PeerAgentsFromCheckpointPeersTable(t *testing.T) {
	s := newTestServerWithCatalog(t)
	// Two peer agents
	fa := newFakeAgent(http.StatusNoContent)
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()
	// Wire client to send all agent calls to our httptest server by
	// overriding nodeInternalIP via fake k8s nodes — but
	// nodeInternalIP returns 10.0.0.10 which can't route to our
	// httptest server's port. So instead we drop AgentPort to the
	// httptest port and point all nodes at httptest's host.
	// Cleanest: short-circuit deleteOnAgentNode by overriding the
	// node IP to httptest's listener. We do that by pointing
	// kubeClient at fake Nodes whose InternalIP is 127.0.0.1.
	srvURL := strings.TrimPrefix(srv.URL, "http://")
	host, port, _ := splitHostPort(srvURL)
	s.config.AgentPort = mustAtoi(port)
	if _, err := s.kubeClient.CoreV1().Nodes().Create(context.Background(),
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "peer-1"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
			},
		}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create peer-1: %v", err)
	}
	if _, err := s.kubeClient.CoreV1().Nodes().Create(context.Background(),
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "peer-2"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
			},
		}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create peer-2: %v", err)
	}

	// Register peers in the catalog.
	if err := s.catalog.AddPeer("ck-1", "peer-1", "http://peer-1:8081"); err != nil {
		t.Fatalf("peer-add 1: %v", err)
	}
	if err := s.catalog.AddPeer("ck-1", "peer-2", "http://peer-2:8081"); err != nil {
		t.Fatalf("peer-add 2: %v", err)
	}

	res := s.cascadeDeleteCheckpoint(context.Background(), "ck-1", "agent-ck-1", nil)
	if res.PeerAgents != 2 {
		t.Errorf("PeerAgents = %d, want 2", res.PeerAgents)
	}
	if !res.AnySuccess {
		t.Error("AnySuccess should be true after successful peer deletes")
	}
}

func TestCascadeDelete_AuditSummaryListsAttemptedTiers(t *testing.T) {
	s := newTestServerWithCatalog(t)
	res := cascadeDeleteResult{
		OriginAgents: 3,
	}
	got := res.Summary()
	for _, want := range []string{"3 agent path(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary missing %q; got %q", want, got)
		}
	}
	if res.Status() != "success" {
		t.Errorf("Status = %q, want success", res.Status())
	}

	// With errors
	res.Errors = []string{"agent(node-X) DELETE failed"}
	if !strings.Contains(res.Summary(), "errors:") {
		t.Errorf("Summary should include errors; got %q", res.Summary())
	}
	if res.Status() != "partial" {
		t.Errorf("Status with errors = %q, want partial", res.Status())
	}

	_ = s // keep linter quiet
}

// TestDeleteCheckpoint_HTTP_404WhenNothingFound covers the response
// contract: 404 when no tier had the checkpoint, audit row not
// written.
func TestDeleteCheckpoint_HTTP_404WhenNothingFound(t *testing.T) {
	s := newTestServerWithCatalog(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/checkpoints/never-existed", http.NoBody)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rr.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if !strings.Contains(body.Error, "not found") {
		t.Errorf("error should mention 'not found'; got %q", body.Error)
	}
}

// TestDeleteCheckpoint_HTTP_204WhenAtLeastOneTierFound covers the
// happy path: catalog row alone is enough to return 204 + write the
// audit row. (Operator action: "forget this entry", even if every
// physical tier already evicted it.)
func TestDeleteCheckpoint_HTTP_204WhenCatalogRowExists(t *testing.T) {
	s := newTestServerWithCatalog(t)

	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID:           "ck-orphan",
		CheckpointID: "ck-orphan",
		Namespace:    "ns1",
		PodName:      "p1",
		NodeName:     "node-1",
		Status:       "Completed",
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/checkpoints/ck-orphan", http.NoBody)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("got %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
	// Catalog row must be gone now.
	if _, err := s.catalog.GetCheckpoint("ck-orphan"); err == nil {
		t.Error("catalog row should be deleted; got nil error")
	}
}

// A cascade where every tier fails leaves AnySuccess false, same as a
// checkpoint that genuinely does not exist. Answering 404 for both told the
// caller the dump was gone while it was still on disk -- the nvsnap#736
// failure mode wearing a different status code. The retained catalog row is
// what distinguishes them.
func TestDeleteCheckpoint_HTTP_500WhenEveryTierFails(t *testing.T) {
	s := newTestServerWithCatalog(t)
	failingPeerTier(t, s, "ck-allfail", http.StatusInternalServerError)

	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID:             "ck-allfail",
		Hash:           "aaa111aaa111aaa111",
		CheckpointPath: "/var/lib/nvsnap/checkpoints/agent-dead",
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/checkpoints/ck-allfail", http.NoBody)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("got %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := s.catalog.GetCheckpoint("ck-allfail"); err != nil {
		t.Errorf("catalog row must survive so the delete can be retried; get: %v", err)
	}
}

// The by-hash endpoint runs the same cascade and so owes the same answer.
// It only consulted AnySuccess, so a partial cascade reported 204 while the
// row it deliberately kept was still there.
func TestDeleteCheckpointByHash_HTTP_500OnPartialCascade(t *testing.T) {
	s := newTestServerWithCatalog(t)
	failingPeerTier(t, s, "ck-hash", http.StatusInternalServerError)

	const hash = "bbb222bbb222bbb222"
	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID:             "ck-hash",
		Hash:           hash,
		Namespace:      "ns1",
		PodName:        "p1",
		CheckpointPath: "/var/lib/nvsnap/checkpoints/agent-dead",
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/checkpoints/by-hash/"+hash, http.NoBody)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("got %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if rows, err := s.catalog.ListByHash(hash); err != nil || len(rows) == 0 {
		t.Errorf("catalog row must survive a partial by-hash cascade (rows=%d, err=%v)", len(rows), err)
	}
}

// A catalog delete that itself errors is the one tier whose failure the
// cascade cannot route around: it appended to Errors but left
// CatalogRetained false, so a run where some other tier had already
// succeeded still answered 204 with the row in place.
func TestCascadeDelete_CatalogDeleteErrorSetsRetained(t *testing.T) {
	s := newTestServerWithCatalog(t)

	// An empty Hash is what isolates this: every other catalog touch in the
	// cascade (sibling lookup, L2 PVCs, snapshots, leases, capture CM) is
	// gated on row.Hash != "", so DeleteCheckpoint is the only catalog call
	// that runs. Closing the DB therefore fails that call and nothing else,
	// leaving result.Errors empty until it -- which means the pre-delete
	// "errors already happened" early return cannot be what sets the flag.
	row := &db.Checkpoint{
		ID:        "ck-catalog-err",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.catalog.UpsertCheckpoint(row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.catalog.Close(); err != nil {
		t.Fatalf("close catalog: %v", err)
	}

	res := s.cascadeDeleteCheckpoint(context.Background(), row.ID, "agent-x", row)

	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "DeleteCheckpoint") {
		t.Fatalf("want exactly one error, from the catalog delete; got %v", res.Errors)
	}
	if !res.CatalogRetained {
		t.Error("CatalogRetained = false; a failed catalog delete leaves the row, " +
			"so callers must not be told the checkpoint is gone")
	}
}

// ---------------- helpers ----------------

func splitHostPort(s string) (host, port string, ok bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func mustAtoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// TestDeleteCheckpoint_HTTP_AlsoDeletesGPUCheckpointCRD is the regression
// test for nvsnap#74. Before the fix, DELETE /api/v1/checkpoints/{id}
// would clean up the DB row, L1 hostpath, peer caches, and the
// blobstore manifest — but leave the GPUCheckpoint CRD intact. The
// default GET / LIST handlers fall back to the CRD when no
// `source=db` is given, so the API surface kept reporting the
// "deleted" checkpoint as alive. Reproduced live on GCP-H100-a
// 2026-06-02: 6 GPUCheckpoint CRDs survived 6 successful DELETE 204s.
func TestDeleteCheckpoint_HTTP_AlsoDeletesGPUCheckpointCRD(t *testing.T) {
	s := newTestServerWithCatalog(t)

	const ns = "nvcf-backend"
	const id = "0-sr-test-1780000000"

	// Seed the catalog row with namespace populated (required to
	// scope the CRD delete).
	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID: id, CheckpointID: id,
		Namespace: ns, PodName: "pod-x", NodeName: "node-1",
		Status: "Completed", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	// Seed the matching GPUCheckpoint CRD in the dynamic fake client.
	crd := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvsnap.io/v1alpha1",
			"kind":       "GPUCheckpoint",
			"metadata": map[string]interface{}{
				"name":      id,
				"namespace": ns,
			},
			"spec": map[string]interface{}{"podName": "pod-x"},
		},
	}
	if _, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).
		Create(context.Background(), crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed CRD: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/checkpoints/"+id, http.NoBody)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}

	// Catalog row gone.
	if _, err := s.catalog.GetCheckpoint(id); err == nil {
		t.Error("catalog row should be deleted after DELETE")
	}

	// CRD gone — this is the regression.
	_, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).
		Get(context.Background(), id, metav1.GetOptions{})
	if err == nil {
		t.Error("GPUCheckpoint CRD should be deleted by DELETE; survived (nvsnap#74)")
	}
}

// CRD already gone (NotFound) is not an error — the cascade still
// counts the catalog row as success and returns 204. Mirrors the
// "blobstore 404 isn't an error" pattern.
func TestCascadeDelete_CRDNotFoundIsNotError(t *testing.T) {
	s := newTestServerWithCatalog(t)

	const id = "ck-orphan-crd"
	row := &db.Checkpoint{
		ID: id, CheckpointID: id,
		Namespace: "nvcf-backend", PodName: "p", NodeName: "n",
		Status: "Completed", CreatedAt: time.Now().UTC(),
	}
	res := s.cascadeDeleteCheckpoint(context.Background(), id, id, row)

	if res.CRDsDeleted > 0 {
		t.Errorf("CRDDeleted=true; want false when CRD didn't exist")
	}
	if len(res.Errors) != 0 {
		t.Errorf("NotFound shouldn't appear in Errors; got %v", res.Errors)
	}
	if !res.AnySuccess {
		t.Errorf("AnySuccess=false; want true (catalog row presence alone counts)")
	}
}

// Agent-register rows (id = "<short-hash>__<ts>", no Namespace) have
// no corresponding CRD — the CRD delete branch must be skipped, not
// errored.
func TestCascadeDelete_NoCRDDeleteForRowsWithoutNamespace(t *testing.T) {
	s := newTestServerWithCatalog(t)

	const id = "a4f7818605da321e__20260602-005042"
	row := &db.Checkpoint{
		ID: id, CheckpointID: id,
		// Namespace deliberately empty (agent-register shape)
		PodName: "p", NodeName: "n",
		Status: "Completed", CreatedAt: time.Now().UTC(),
	}
	res := s.cascadeDeleteCheckpoint(context.Background(), id, id, row)

	if res.CRDsDeleted > 0 {
		t.Errorf("CRDDeleted=true for namespace-less row; cascade should have skipped CRD delete")
	}
	if len(res.Errors) != 0 {
		t.Errorf("namespace-less row should not generate errors; got %v", res.Errors)
	}
}

// nvsnap#736: the catalog row is the only pointer to the on-disk dump. When a
// tier delete fails (agent 401 under --auth-mode=required, agent down,
// blobstore 5xx), dropping the row orphans those bytes -- nothing references
// them, so nothing retries or GCs them. Worse, the catalog delete itself set
// AnySuccess, so the handler returned 204 No Content while the dump survived.
//
// The row must be retained on any tier failure so the delete stays retryable.
func TestCascadeDelete_RetainsCatalogRowWhenATierFails(t *testing.T) {
	s := newTestServerWithCatalog(t)
	failingPeerTier(t, s, "ck-1", http.StatusInternalServerError)

	row := &db.Checkpoint{ID: "ck-1", Hash: "abc123abc123abc123", CheckpointPath: "/var/lib/nvsnap/checkpoints/server-broken"}
	if err := s.catalog.UpsertCheckpoint(row); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	res := s.cascadeDeleteCheckpoint(context.Background(), "ck-1", "server-broken", row)

	if len(res.Errors) == 0 {
		t.Fatal("expected a tier error from the failing peer agent")
	}
	if res.CatalogRows != 0 {
		t.Errorf("CatalogRows = %d, want 0; the row must survive a failed tier delete", res.CatalogRows)
	}
	// The row itself must still be readable, or the dump is unreachable.
	if got, err := s.catalog.GetCheckpoint("ck-1"); err != nil || got == nil {
		t.Errorf("catalog row was deleted despite a tier failure (get: %v); dump is now orphaned", err)
	}
	if res.Status() != "partial" {
		t.Errorf("Status() = %q, want partial", res.Status())
	}
}

// The clean path must still delete the row, or nothing is ever reclaimed.
func TestCascadeDelete_DeletesCatalogRowWhenAllTiersSucceed(t *testing.T) {
	s := newTestServerWithCatalog(t)

	row := &db.Checkpoint{ID: "ck-ok", Hash: "def456def456def456", CheckpointPath: "/var/lib/nvsnap/checkpoints/agent-ok"}
	if err := s.catalog.UpsertCheckpoint(row); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	res := s.cascadeDeleteCheckpoint(context.Background(), "ck-ok", "agent-ok", row)

	if len(res.Errors) != 0 {
		t.Fatalf("clean cascade should have no errors; got %v", res.Errors)
	}
	if res.CatalogRows == 0 {
		t.Error("CatalogRows = 0; a clean cascade must delete the row")
	}
}
