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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/mux"
)

// Peer endpoints for rootfs-only captures, mirroring the CRIU
// checkpoint peer endpoints (listCheckpointFilesHandler /
// readCheckpointFileHandler / checkpointManifestHandler) but
// rooted at the agent's rootfs cache directory:
//
//   /var/lib/nvsnap/cache/<hash>/        (overlay diff, hf-cache, vllm-cache, ...)
//
// The set is:
//
//   GET /v1/captures/{hash}/manifest   — flat recursive listing
//   GET /v1/captures/{hash}/file?path= — single-file fetch
//
// Each agent serves these for the captures it has materialised under
// its own CacheDir. EnsureCaptureLocal (capture_cascade.go) is the
// consumer; the in-agent webhook Mutator calls EnsureCaptureLocal
// before resolving Backend.Mount(hash, vm).

// captureManifestHandler returns a flat JSON listing of every file
// under <CacheDir>/<hash>/ with relative paths, sizes, and mtimes.
// 404 if the hash directory does not exist on this agent's filesystem.
func (a *Agent) captureManifestHandler(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	captureDir := a.captureDirFor(hash)
	if captureDir == "" {
		http.Error(w, "capture cache not configured on this agent", http.StatusServiceUnavailable)
		return
	}
	if _, err := os.Stat(captureDir); os.IsNotExist(err) {
		http.Error(w, "capture not found", http.StatusNotFound)
		return
	}

	type manifestFile struct {
		Path  string `json:"path"`
		Size  int64  `json:"size"`
		Mtime string `json:"mtime"`
	}
	var files []manifestFile
	var totalSize int64

	err := filepath.Walk(captureDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(captureDir, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, manifestFile{
			Path:  rel,
			Size:  info.Size(),
			Mtime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
		totalSize += info.Size()
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("walk: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"hash":       hash,
		"total_size": totalSize,
		"file_count": len(files),
		"files":      files,
	})
}

// captureFileHandler serves a single file from <CacheDir>/<hash>/<path>.
// 400 on missing/invalid path, 404 on missing hash or file.
//
// The path is taken from ?path=, must be relative, must not contain "..",
// and is checked against the resolved capture root to defend against
// symlink-induced traversal.
func (a *Agent) captureFileHandler(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}

	captureDir := a.captureDirFor(hash)
	if captureDir == "" {
		http.Error(w, "capture cache not configured on this agent", http.StatusServiceUnavailable)
		return
	}
	if _, err := os.Stat(captureDir); os.IsNotExist(err) {
		http.Error(w, "capture not found", http.StatusNotFound)
		return
	}

	// Resolve + boundary-check the path, following symlinks so a link
	// committed inside the capture tree can't escape it (nvsnap#92).
	targetFile, err := resolveWithinRoot(captureDir, relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
		} else {
			http.Error(w, "invalid path", http.StatusBadRequest)
		}
		return
	}

	info, err := os.Stat(targetFile)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf("stat: %v", err), http.StatusInternalServerError)
		}
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "not a regular file", http.StatusBadRequest)
		return
	}

	f, err := os.Open(targetFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("open: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// captureDirFor returns <CacheDir>/<hash> when the rootfs-capture cache
// is configured on this agent, or "" if --rootfs-capture is off.
func (a *Agent) captureDirFor(hash string) string {
	cache := a.config.RootfsCapture.CacheDir
	if cache == "" {
		return ""
	}
	return filepath.Join(cache, hash)
}
