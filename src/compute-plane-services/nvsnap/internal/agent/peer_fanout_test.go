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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// newTestAgent returns an Agent whose CheckpointDir is the given
// temp dir, with a no-op logger. Sufficient to exercise the
// peer-fanout HTTP handlers without spinning up a runtime.
func newTestAgent(t *testing.T, dir string) *Agent {
	t.Helper()
	log := logrus.New()
	log.SetOutput(io.Discard)
	a := &Agent{log: log}
	a.config.CheckpointDir = dir
	return a
}

// makeCheckpointDir populates a temp dir with files mimicking a CRIU
// dump layout (root files + nested rootfs-diff/, mounts/). Returns
// (id, total_size, file_count) for assertion.
func makeCheckpointDir(t *testing.T, parent string) (id string, totalSize int64, fileCount int) {
	t.Helper()
	id = "vllm-small__nvsnap-system__20260509-192743"
	root := filepath.Join(parent, id)
	files := map[string][]byte{
		"inventory.img":            []byte("inv-99-bytes" + strings.Repeat("x", 87)),
		"pstree.img":               []byte("pstree-stub"),
		"core-1.img":               []byte(strings.Repeat("c", 1952)),
		"pages-1.img":              []byte(strings.Repeat("p", 4096)),
		"metadata.json":            []byte(`{"id":"` + id + `"}`),
		"rootfs-diff/etc/hostname": []byte("vllm-small\n"),
		"mounts/dev_shm.tar":       []byte(strings.Repeat("d", 20480)),
	}
	var total int64
	for rel, data := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
		total += int64(len(data))
	}
	return id, total, len(files)
}

// TestCheckpointManifestHandler_FlatRecursiveListing is the load-bearing
// test: phase 5d.1's peer-fanout receiver makes ONE GET for the
// manifest then drives N parallel GETs for files. The manifest must
// expose every regular file with a forward-slash relative path the
// receiver can pass back to the /file endpoint verbatim.
func TestCheckpointManifestHandler_FlatRecursiveListing(t *testing.T) {
	dir := t.TempDir()
	id, wantTotal, wantCount := makeCheckpointDir(t, dir)
	a := newTestAgent(t, dir)

	router := mux.NewRouter()
	router.HandleFunc("/v1/checkpoints/{id}/manifest", a.checkpointManifestHandler).Methods("GET")

	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := httpGet(srv.URL + "/v1/checkpoints/" + id + "/manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var m struct {
		CheckpointID string `json:"checkpoint_id"`
		TotalSize    int64  `json:"total_size"`
		FileCount    int    `json:"file_count"`
		Files        []struct {
			Path  string `json:"path"`
			Size  int64  `json:"size"`
			Mtime string `json:"mtime"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if m.CheckpointID != id {
		t.Errorf("checkpoint_id = %q, want %q", m.CheckpointID, id)
	}
	if m.TotalSize != wantTotal {
		t.Errorf("total_size = %d, want %d", m.TotalSize, wantTotal)
	}
	if m.FileCount != wantCount {
		t.Errorf("file_count = %d, want %d", m.FileCount, wantCount)
	}

	// Spot-check: nested file is present with relative path that uses
	// forward-slashes (so the receiver can hand it back to the /file
	// endpoint as-is, regardless of OS).
	foundNested := false
	for _, f := range m.Files {
		if f.Path == filepath.ToSlash("rootfs-diff/etc/hostname") {
			foundNested = true
			if f.Size != int64(len("vllm-small\n")) {
				t.Errorf("nested file size = %d, want %d", f.Size, len("vllm-small\n"))
			}
		}
	}
	if !foundNested {
		var got []string
		for _, f := range m.Files {
			got = append(got, f.Path)
		}
		t.Errorf("nested rootfs-diff/etc/hostname not in manifest; got: %v", got)
	}

	// Phase 5d invariant: NO directory entries — the receiver doesn't
	// need to mkdir, only walk this list and download each file.
	for _, f := range m.Files {
		if strings.HasSuffix(f.Path, "/") {
			t.Errorf("directory entry leaked into manifest: %q", f.Path)
		}
	}
}

// TestCheckpointManifestHandler_NotFound covers the 404 path so future
// catalog code that probes peers can distinguish "peer alive but
// doesn't have it" from "peer unreachable".
func TestCheckpointManifestHandler_NotFound(t *testing.T) {
	dir := t.TempDir()
	a := newTestAgent(t, dir)

	router := mux.NewRouter()
	router.HandleFunc("/v1/checkpoints/{id}/manifest", a.checkpointManifestHandler).Methods("GET")
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := httpGet(srv.URL + "/v1/checkpoints/does-not-exist/manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestReadCheckpointFileHandler_StreamsLargeFile guards the lift of
// the previous 1 MB cap. Phase 5d.1 peer fanout MUST handle multi-GB
// pages-*.img files. We don't write 28 GB in a unit test, but we do
// write 4 MB (>> 1 MB old cap) and verify we get all the bytes back.
func TestReadCheckpointFileHandler_StreamsLargeFile(t *testing.T) {
	dir := t.TempDir()
	id := "test-large"
	root := filepath.Join(dir, id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	const size = 4 * 1024 * 1024 // 4 MB — well past old 1 MB cap
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 251)
	}
	if err := os.WriteFile(filepath.Join(root, "pages-1.img"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	a := newTestAgent(t, dir)
	router := mux.NewRouter()
	router.HandleFunc("/v1/checkpoints/{id}/file", a.readCheckpointFileHandler).Methods("GET")
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := httpGet(srv.URL + "/v1/checkpoints/" + id + "/file?path=pages-1.img")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (cap should be lifted for phase 5d.1)", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != size {
		t.Errorf("got %d bytes, want %d", len(got), size)
	}
	for i, b := range got {
		if b != byte(i%251) {
			t.Fatalf("byte mismatch at offset %d: got %d, want %d", i, b, i%251)
			break
		}
	}
}

// TestReadCheckpointFileHandler_RangeRequest verifies http.ServeContent
// honors Range — critical for parallel chunked downloads of huge files
// (the receiver fetches different byte ranges of pages-*.img on
// multiple connections to saturate network bandwidth).
func TestReadCheckpointFileHandler_RangeRequest(t *testing.T) {
	dir := t.TempDir()
	id := "test-range"
	root := filepath.Join(dir, id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("0123456789abcdefghijABCDEFGHIJ")
	if err := os.WriteFile(filepath.Join(root, "small.img"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	a := newTestAgent(t, dir)
	router := mux.NewRouter()
	router.HandleFunc("/v1/checkpoints/{id}/file", a.readCheckpointFileHandler).Methods("GET")
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/checkpoints/"+id+"/file?path=small.img", http.NoBody)
	req.Header.Set("Range", "bytes=10-19")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 Partial Content", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "abcdefghij" {
		t.Errorf("range body = %q, want %q", got, "abcdefghij")
	}
}
