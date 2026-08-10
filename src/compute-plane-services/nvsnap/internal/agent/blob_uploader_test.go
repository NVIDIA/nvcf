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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
)

// fakeBlobStoreServer captures the HTTP requests an uploader
// makes against nvsnap-blobstore. Tests assert on the recorded
// state at the end of the run rather than mocking responses
// turn-by-turn — the protocol is simple enough that a faithful
// in-memory fake is more readable than per-request expectations.
type fakeBlobStoreServer struct {
	mu       sync.Mutex
	blobs    map[string][]byte
	manifest *manifestPayload
	url      string
	// PUT call counts for dedup verification.
	putCount  int
	headCount int
}

func newFakeBlobStoreServer(t *testing.T) *fakeBlobStoreServer {
	t.Helper()
	f := &fakeBlobStoreServer{blobs: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/blob/", func(w http.ResponseWriter, r *http.Request) {
		sha := strings.TrimPrefix(r.URL.Path, "/v1/blob/")
		switch r.Method {
		case http.MethodHead:
			f.mu.Lock()
			f.headCount++
			_, ok := f.blobs[sha]
			f.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			h := sha256.Sum256(body)
			got := hex.EncodeToString(h[:])
			if got != sha {
				http.Error(w, "hash mismatch", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.putCount++
			_, existed := f.blobs[sha]
			f.blobs[sha] = body
			f.mu.Unlock()
			if existed {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/capture/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "PUT only", http.StatusMethodNotAllowed)
			return
		}
		var m manifestPayload
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.manifest = &m
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// fakeBlobUploadedCatalog records /blob-uploaded callbacks so
// tests can assert the agent fired them with the right blob URI.
func fakeBlobUploadedCatalog(t *testing.T) (srvURL string, countPtr *int32, lastBlobURIPtr *string) {
	t.Helper()
	var count int32
	var lastBlobURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/blob-uploaded") && r.Method == http.MethodPost:
			var body struct {
				BlobURI string `json:"blob_uri"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastBlobURI = body.BlobURI
			atomic.AddInt32(&count, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &count, &lastBlobURI
}

// uploaderAgent constructs an Agent whose dump dir is populated
// by makeCheckpointDir, with BlobStoreURL + CatalogURL pointing
// at the test fakes.
func uploaderAgent(t *testing.T, blobStoreURL, catalogURL string) (agent *Agent, dumpDir, checkpointID string) {
	t.Helper()
	dir := t.TempDir()
	id, _, _ := makeCheckpointDir(t, dir)
	log := logrus.New()
	log.SetOutput(io.Discard)
	a := &Agent{log: log}
	a.config.CheckpointDir = dir
	a.config.BlobStoreURL = blobStoreURL
	a.config.CatalogURL = catalogURL
	return a, dir, id
}

// TestUploadCheckpoint_HappyPath — the load-bearing test. After
// UploadCheckpoint, every file in the dump dir is in the blob
// store under its sha, the manifest matches the dir, and the
// catalog callback fired with the right URI.
func TestUploadCheckpoint_HappyPath(t *testing.T) {
	bs := newFakeBlobStoreServer(t)
	catURL, callbacks, lastURI := fakeBlobUploadedCatalog(t)
	a, dir, id := uploaderAgent(t, bs.url, catURL)

	if err := a.UploadCheckpoint(t.Context(), id); err != nil {
		t.Fatalf("UploadCheckpoint: %v", err)
	}

	// Every file in the dump must have its bytes stored under
	// sha. Walk the dir and verify the blob store has each one.
	bs.mu.Lock()
	defer bs.mu.Unlock()
	dumpFiles := 0
	_ = filepath.Walk(filepath.Join(dir, id), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		dumpFiles++
		body, _ := os.ReadFile(path)
		h := sha256.Sum256(body)
		sha := hex.EncodeToString(h[:])
		got, ok := bs.blobs[sha]
		if !ok {
			t.Errorf("blob missing for %s (sha=%s)", path, sha)
			return nil
		}
		if !bytes.Equal(got, body) {
			t.Errorf("blob bytes mismatch for %s", path)
		}
		return nil
	})

	if bs.manifest == nil {
		t.Fatal("manifest never PUT")
	}
	if len(bs.manifest.Files) != dumpFiles {
		t.Errorf("manifest has %d files, dump has %d", len(bs.manifest.Files), dumpFiles)
	}

	// Catalog callback fired exactly once with the blobstore URL.
	if atomic.LoadInt32(callbacks) != 1 {
		t.Errorf("blob-uploaded callbacks = %d, want 1", atomic.LoadInt32(callbacks))
	}
	if *lastURI != bs.url {
		t.Errorf("blob_uri = %q, want %q", *lastURI, bs.url)
	}
}

// TestUploadCheckpoint_DedupHEAD — second upload of the same
// dump must HEAD every file and PUT zero (all already present).
// This is what makes recaptures of long-lived workloads cheap.
func TestUploadCheckpoint_DedupHEAD(t *testing.T) {
	bs := newFakeBlobStoreServer(t)
	catURL, _, _ := fakeBlobUploadedCatalog(t)
	a, _, id := uploaderAgent(t, bs.url, catURL)

	if err := a.UploadCheckpoint(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	bs.mu.Lock()
	firstPuts := bs.putCount
	bs.mu.Unlock()

	if err := a.UploadCheckpoint(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	bs.mu.Lock()
	secondPuts := bs.putCount - firstPuts
	bs.mu.Unlock()

	if secondPuts != 0 {
		t.Errorf("second upload did %d PUTs (want 0 — every file should HEAD-hit)", secondPuts)
	}
}

// TestUploadCheckpoint_NoBlobStoreURLSkipped — when the agent
// runs without --blob-store-url (peer-only fanout, no durable
// backstop), UploadCheckpoint must be a clean no-op rather than
// an error. The capture path always calls UploadCheckpoint, but
// only some clusters configure the blob store.
func TestUploadCheckpoint_NoBlobStoreURLSkipped(t *testing.T) {
	dir := t.TempDir()
	id, _, _ := makeCheckpointDir(t, dir)
	log := logrus.New()
	log.SetOutput(io.Discard)
	a := &Agent{log: log}
	a.config.CheckpointDir = dir
	// BlobStoreURL deliberately empty.

	if err := a.UploadCheckpoint(t.Context(), id); err != nil {
		t.Errorf("UploadCheckpoint with empty BlobStoreURL: want nil, got %v", err)
	}
}

// TestUploadCheckpoint_DumpDirMissing — capture path called us
// with a stale ID (race against retention sweep). Surface as
// error so the caller logs it; don't silently no-op.
func TestUploadCheckpoint_DumpDirMissing(t *testing.T) {
	bs := newFakeBlobStoreServer(t)
	catURL, _, _ := fakeBlobUploadedCatalog(t)
	dir := t.TempDir()
	log := logrus.New()
	log.SetOutput(io.Discard)
	a := &Agent{log: log}
	a.config.CheckpointDir = dir
	a.config.BlobStoreURL = bs.url
	a.config.CatalogURL = catURL

	err := a.UploadCheckpoint(t.Context(), "ghost")
	if err == nil {
		t.Error("want error for missing dump dir, got nil")
	}
}

// TestUploadCheckpoint_CatalogCallbackBestEffort — blobs land
// fine; catalog callback fails. UploadCheckpoint must still
// return nil (durability is preserved; the catalog reconciler
// can pick up the orphan later).
func TestUploadCheckpoint_CatalogCallbackBestEffort(t *testing.T) {
	bs := newFakeBlobStoreServer(t)
	// Catalog stub returns 500 on every call.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()
	a, _, id := uploaderAgent(t, bs.url, failSrv.URL)

	if err := a.UploadCheckpoint(t.Context(), id); err != nil {
		t.Errorf("UploadCheckpoint: catalog failure should be best-effort, got %v", err)
	}

	// But blobs ARE present.
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if len(bs.blobs) == 0 {
		t.Error("no blobs stored — durability path failed too")
	}
	if bs.manifest == nil {
		t.Error("manifest not stored")
	}
}

// TestUploadCheckpoint_BlobPutFailureSurfaces — blob store is
// hard-down (every PUT returns 500). UploadCheckpoint must
// return error so the agent's caller knows the capture isn't
// durable yet (and can retry).
func TestUploadCheckpoint_BlobPutFailureSurfaces(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			http.NotFound(w, r)
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer failSrv.Close()

	catURL, _, _ := fakeBlobUploadedCatalog(t)
	a, _, id := uploaderAgent(t, failSrv.URL, catURL)

	err := a.UploadCheckpoint(t.Context(), id)
	if err == nil {
		t.Error("want upload error when blob store is down, got nil")
	}
}
