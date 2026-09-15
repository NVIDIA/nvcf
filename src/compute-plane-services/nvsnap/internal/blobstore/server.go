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

// Package blobstore is a disk-backed content-addressed blob store with
// an HTTP front-end for peer fan-out of capture blobs and manifests.
package blobstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
)

// Server wraps a Store with the HTTP protocol from
// docs/archive/PHASE5D-PEER-FANOUT-BLOB-STORE.md (lines 105-119):
//
//	PUT  /v1/blob/{sha256}             body: file stream → 201 Created (200 if exists)
//	GET  /v1/blob/{sha256}             → 200 + body (Range supported)
//	HEAD /v1/blob/{sha256}             → 200 if exists, 404 if not
//	DELETE /v1/blob/{sha256}           → 204
//
//	PUT  /v1/capture/{hash}/manifest.json   body: JSON
//	GET  /v1/capture/{hash}/manifest.json   → manifest JSON
//	DELETE /v1/capture/{hash}              → 204
//
//	GET  /v1/healthz                   → 200 with disk stats
//
// All handlers are safe to invoke concurrently.
type Server struct {
	store *Store
}

// NewServer returns a Server backed by the given Store.
func NewServer(store *Store) *Server {
	return &Server{store: store}
}

// Handler returns a configured *mux.Router. Caller wraps in
// http.Server with whatever timeouts/TLS apply.
func (s *Server) Handler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/v1/blob/{sha}", s.putBlob).Methods(http.MethodPut)
	r.HandleFunc("/v1/blob/{sha}", s.getBlob).Methods(http.MethodGet)
	r.HandleFunc("/v1/blob/{sha}", s.headBlob).Methods(http.MethodHead)
	r.HandleFunc("/v1/blob/{sha}", s.deleteBlob).Methods(http.MethodDelete)
	r.HandleFunc("/v1/capture/{hash}/manifest.json", s.putManifest).Methods(http.MethodPut)
	r.HandleFunc("/v1/capture/{hash}/manifest.json", s.getManifest).Methods(http.MethodGet)
	r.HandleFunc("/v1/capture/{hash}", s.deleteCapture).Methods(http.MethodDelete)
	r.HandleFunc("/v1/healthz", s.healthz).Methods(http.MethodGet)
	r.HandleFunc("/v1/stats", s.stats).Methods(http.MethodGet)
	r.HandleFunc("/v1/captures", s.listCaptures).Methods(http.MethodGet)
	return r
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

func (s *Server) listCaptures(w http.ResponseWriter, r *http.Request) {
	captures, err := s.store.ListCaptures()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"captures": captures,
		"total":    len(captures),
	})
}

func (s *Server) putBlob(w http.ResponseWriter, r *http.Request) {
	sha := strings.ToLower(mux.Vars(r)["sha"])
	defer func() { _ = r.Body.Close() }()
	existed, err := s.store.PutBlob(sha, r.Body)
	if err != nil {
		if errors.Is(err, ErrHashMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if existed {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	sha := strings.ToLower(mux.Vars(r)["sha"])
	if !validSha256Hex(sha) {
		http.Error(w, "invalid sha256", http.StatusBadRequest)
		return
	}
	f, err := s.store.OpenBlob(sha)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, immutable, max-age=31536000")
	// http.ServeContent honors Range so receivers can do parallel
	// chunked downloads of large blobs (pages-*.img can be GBs).
	http.ServeContent(w, r, sha, fi.ModTime(), f)
}

func (s *Server) headBlob(w http.ResponseWriter, r *http.Request) {
	sha := strings.ToLower(mux.Vars(r)["sha"])
	if !validSha256Hex(sha) {
		http.Error(w, "invalid sha256", http.StatusBadRequest)
		return
	}
	if !s.store.HasBlob(sha) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) deleteBlob(w http.ResponseWriter, r *http.Request) {
	sha := strings.ToLower(mux.Vars(r)["sha"])
	if !validSha256Hex(sha) {
		http.Error(w, "invalid sha256", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteBlob(sha); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putManifest(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	defer func() { _ = r.Body.Close() }()
	// Cap manifest size at 64 MiB — at 100 bytes/file that's ~640K
	// files per capture, well past anything CRIU produces today.
	const maxManifest = 64 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxManifest)
	var m Manifest
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, fmt.Sprintf("decode manifest: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.store.PutManifest(hash, &m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getManifest(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	m, err := s.store.GetManifest(hash)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

func (s *Server) deleteCapture(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	if err := s.store.DeleteCapture(hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type healthResponse struct {
	Status string     `json:"status"`
	Disk   *DiskStats `json:"disk"`
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.DiskStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(healthResponse{
		Status: "ok",
		Disk:   stats,
	})
}
