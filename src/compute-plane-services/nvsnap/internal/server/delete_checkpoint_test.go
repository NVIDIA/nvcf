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

// fakeBlobstore records DELETE /v1/capture/{hash} calls + answers
// 204 unless one of the hashes is in the failHash set.
type fakeBlobstore struct {
	mu        sync.Mutex
	deletedOK []string
	failHash  map[string]int
}

func newFakeBlobstore() (*fakeBlobstore, *httptest.Server) {
	fb := &fakeBlobstore{failHash: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/v1/capture/"
		if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		hash := strings.TrimPrefix(r.URL.Path, prefix)
		fb.mu.Lock()
		defer fb.mu.Unlock()
		if code, fail := fb.failHash[hash]; fail {
			w.WriteHeader(code)
			return
		}
		fb.deletedOK = append(fb.deletedOK, hash)
		w.WriteHeader(http.StatusNoContent)
	}))
	return fb, srv
}

func (fb *fakeBlobstore) deletes() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	out := make([]string, len(fb.deletedOK))
	copy(out, fb.deletedOK)
	return out
}

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

func TestCascadeDelete_BlobstoreCalledWithAgentID(t *testing.T) {
	s := newTestServerWithCatalog(t)
	fb, fbSrv := newFakeBlobstore()
	defer fbSrv.Close()
	s.config.BlobstoreURL = fbSrv.URL

	// Seed a row with the agent-id form in CheckpointPath. The
	// catalog row's id (server-format) differs from the path basename
	// (agent-format), and the blobstore is keyed by the latter.
	if err := s.catalog.UpsertCheckpoint(&db.Checkpoint{
		ID:             "0-sr-abc-1780000000",
		CheckpointID:   "0-sr-abc-1780000000",
		Namespace:      "nvcf-backend",
		PodName:        "0-sr-abc",
		NodeName:       "node-1",
		Status:         "Completed",
		CheckpointPath: "/var/lib/nvsnap/checkpoints/0-sr-abc__nvcf-backend__20260531-220923",
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := s.cascadeDeleteCheckpoint(context.Background(), "0-sr-abc-1780000000",
		"0-sr-abc__nvcf-backend__20260531-220923", &db.Checkpoint{})

	if res.Blobstores == 0 {
		t.Errorf("Blobstore=false; result=%+v", res)
	}
	gotHashes := fb.deletes()
	if len(gotHashes) != 1 || gotHashes[0] != "0-sr-abc__nvcf-backend__20260531-220923" {
		t.Errorf("blobstore got DELETE for: %v; want agent-id form", gotHashes)
	}
}

func TestCascadeDelete_BlobstoreSkippedWhenURLEmpty(t *testing.T) {
	s := newTestServerWithCatalog(t)
	s.config.BlobstoreURL = "" // disabled

	res := s.cascadeDeleteCheckpoint(context.Background(), "ck-1", "agent-id", nil)
	if res.Blobstores > 0 {
		t.Errorf("Blobstore should be skipped when URL empty")
	}
}

func TestCascadeDelete_Blobstore404IsNotError(t *testing.T) {
	// 404 from the blobstore means "manifest already gone" — should
	// count as a successful cleanup attempt (nothing to delete), not
	// an audit-row error.
	s := newTestServerWithCatalog(t)
	fb, fbSrv := newFakeBlobstore()
	fb.failHash["already-gone"] = http.StatusNotFound
	defer fbSrv.Close()
	s.config.BlobstoreURL = fbSrv.URL

	res := s.cascadeDeleteCheckpoint(context.Background(), "ck-1", "already-gone", nil)
	if res.Blobstores > 0 {
		t.Errorf("Blobstore should be false when 404 (nothing actually deleted)")
	}
	if len(res.Errors) != 0 {
		t.Errorf("404 should not surface as error; got %v", res.Errors)
	}
}

func TestCascadeDelete_Blobstore5xxIsError(t *testing.T) {
	s := newTestServerWithCatalog(t)
	fb, fbSrv := newFakeBlobstore()
	fb.failHash["server-broken"] = http.StatusInternalServerError
	defer fbSrv.Close()
	s.config.BlobstoreURL = fbSrv.URL

	res := s.cascadeDeleteCheckpoint(context.Background(), "ck-1", "server-broken", nil)
	if len(res.Errors) != 1 {
		t.Errorf("5xx should yield exactly 1 error; got %v", res.Errors)
	}
	if !strings.Contains(res.Errors[0], "500") {
		t.Errorf("error should mention status code 500; got %q", res.Errors[0])
	}
	if res.Status() != "partial" {
		t.Errorf("Status() = %q, want partial when errors present", res.Status())
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
		Blobstores:   1,
	}
	got := res.Summary()
	for _, want := range []string{"3 agent path(s)", "1 blobstore capture(s)"} {
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
	// No blobstore configured → no blobstore attempt
	s.config.BlobstoreURL = ""

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
	s.config.BlobstoreURL = ""

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
	s.config.BlobstoreURL = ""

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
	s.config.BlobstoreURL = ""

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
	s.config.BlobstoreURL = ""

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
