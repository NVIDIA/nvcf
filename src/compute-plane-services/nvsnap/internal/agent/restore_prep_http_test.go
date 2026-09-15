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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// httpGet issues a GET with a background context (test helper for noctx).
func httpGet(u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// httpPostJSON issues a POST with a background context (test helper for noctx).
func httpPostJSON(u string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

// httpPostEmpty issues a POST with no body and a background context (test helper for noctx).
func httpPostEmpty(u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// httpHarness wires up a minimal Agent with just the prep handlers
// registered, behind an httptest.Server. The cmd/nvsnap-mount-prep
// binary drives the same JSON shape against the agent in production;
// this harness verifies the contract end-to-end.
func httpHarness(t *testing.T, fp preparer) (url string, agent *Agent, stop func()) { //nolint:unparam // agent result returned for callers that need it
	t.Helper()
	a := &Agent{}
	a.log = nil // exercise the log==nil path; PrepJobManager constructs a default
	a.prepJobs = NewPrepJobManager(fp, nil)
	router := mux.NewRouter()
	router.HandleFunc("/v1/restore/prep", a.startRestorePrepHandler).Methods("POST")
	router.HandleFunc("/v1/restore/prep/{pod-uid}", a.statusRestorePrepHandler).Methods("GET")
	router.HandleFunc("/v1/restore/prep/{pod-uid}", a.cleanupRestorePrepHandler).Methods("DELETE")
	srv := httptest.NewServer(router)
	return srv.URL, a, srv.Close
}

func TestRestorePrepHTTP_HappyPath(t *testing.T) {
	fp := &fakePreparer{delay: 5 * time.Millisecond}
	url, _, stop := httpHarness(t, fp)
	defer stop()

	// POST /v1/restore/prep
	req := PrepRequest{
		PodUID:      "pod-http-1",
		CaptureHash: "abc123def4567890abc123def4567890abc123def4567890abc123def4567890",
		Mounts: []checkpointstore.VolumeMeta{
			{Name: "a", MountPath: "/a", Type: "rootfs-extract"},
			{Name: "b", MountPath: "/b", Type: "rootfs-extract"},
		},
	}
	body, _ := json.Marshal(req)
	resp, err := httpPostJSON(url+"/v1/restore/prep", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST status=%s body=%s", resp.Status, b)
	}
	var startStatus PrepStatus
	if err := json.NewDecoder(resp.Body).Decode(&startStatus); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if startStatus.State != PrepStatePreparing || startStatus.Total != 2 {
		t.Fatalf("POST response = %+v, want preparing total=2", startStatus)
	}

	// GET /v1/restore/prep/{pod-uid} — poll until ready
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, err := httpGet(url + "/v1/restore/prep/pod-http-1")
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			t.Fatalf("GET status=%s body=%s", r.Status, b)
		}
		var s PrepStatus
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if s.State == PrepStateReady {
			if s.Prepared != 2 {
				t.Errorf("ready but prepared=%d, want 2", s.Prepared)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out polling for ready")
}

func TestRestorePrepHTTP_StatusNotFound(t *testing.T) {
	url, _, stop := httpHarness(t, &fakePreparer{})
	defer stop()
	resp, err := httpGet(url + "/v1/restore/prep/never-posted")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%s, want 404", resp.Status)
	}
}

func TestRestorePrepHTTP_PostMissingPodUID(t *testing.T) {
	url, _, stop := httpHarness(t, &fakePreparer{})
	defer stop()
	body, _ := json.Marshal(PrepRequest{CaptureHash: "h", Mounts: makeMounts(1)})
	resp, err := httpPostJSON(url+"/v1/restore/prep", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%s, want 400", resp.Status)
	}
}

func TestRestorePrepHTTP_Cleanup(t *testing.T) {
	fp := &fakePreparer{}
	url, _, stop := httpHarness(t, fp)
	defer stop()

	// Start a job.
	body, _ := json.Marshal(PrepRequest{
		PodUID: "pod-clean-http", CaptureHash: "h",
		Mounts: makeMounts(1),
	})
	r, _ := httpPostJSON(url+"/v1/restore/prep", body)
	_ = r.Body.Close()

	// DELETE
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, url+"/v1/restore/prep/pod-clean-http", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%s, want 204", resp.Status)
	}

	// Subsequent GET should 404.
	g, err := httpGet(url + "/v1/restore/prep/pod-clean-http")
	if err != nil {
		t.Fatalf("GET after DELETE: %v", err)
	}
	defer func() { _ = g.Body.Close() }()
	if g.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE status=%s, want 404", g.Status)
	}
}

// TestRestorePrepHTTP_ManagerDisabled covers the deployment branch
// where an Agent has prepJobs=nil (e.g. testing pre-202 agent against
// a post-202 init container). Endpoints must respond 503 so the init
// container can fail explicitly rather than loop on 404s.
func TestRestorePrepHTTP_ManagerDisabled(t *testing.T) {
	a := &Agent{prepJobs: nil}
	router := mux.NewRouter()
	router.HandleFunc("/v1/restore/prep", a.startRestorePrepHandler).Methods("POST")
	router.HandleFunc("/v1/restore/prep/{pod-uid}", a.statusRestorePrepHandler).Methods("GET")
	srv := httptest.NewServer(router)
	defer srv.Close()

	body, _ := json.Marshal(PrepRequest{PodUID: "p", CaptureHash: "h"})
	resp, err := httpPostJSON(srv.URL+"/v1/restore/prep", body)
	if err != nil {
		t.Fatalf("disabled POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("disabled POST = %s, want 503", resp.Status)
	}
}
