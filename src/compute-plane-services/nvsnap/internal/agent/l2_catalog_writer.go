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

// Agent-side adapter for the PerCapturePVCBackend's CatalogStateWriter
// interface (nvsnap#63 / nvsnap#76). Translates each state-machine
// transition into a POST to nvsnap-server's
// /api/v1/checkpoints/by-hash/{hash}/pvc-state endpoint.
//
// The agent doesn't have direct DB access — only nvsnap-server does.
// This adapter is the only writer to the catalog's L2 columns on the
// agent side. Defined here (not in the checkpointstore package)
// because it talks HTTP, which is an agent-side concern; the
// checkpointstore package stays SQL- and HTTP-free.
//
// Keyed by content hash, not catalog id: the producer
// (PerCapturePVCBackend) only knows the hash. nvsnap-server fans the
// write out across every catalog row sharing that hash.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// pvcStateHTTPWriter implements checkpointstore.CatalogStateWriter by
// POSTing each state transition to nvsnap-server. Initialized once at
// agent startup; reused across all L2 promotes for the lifetime of
// the agent process.
type pvcStateHTTPWriter struct {
	catalogURL string        // e.g. "http://nvsnap-server.nvsnap-system.svc.cluster.local:8080"
	client     *http.Client  // shared, with a sane timeout
	timeout    time.Duration // per-request
}

// NewPVCStateHTTPWriter constructs the adapter. catalogURL must be
// non-empty (caller validates). Per-request timeout defaults to 30s.
// Exported so cross-component tests (internal/server) can wire the
// real producer against the real router.
func NewPVCStateHTTPWriter(catalogURL string) *pvcStateHTTPWriter { //nolint:revive // unexported return is intentional; callers use it via the CatalogStateWriter interface
	return &pvcStateHTTPWriter{
		catalogURL: catalogURL,
		client:     &http.Client{Timeout: 30 * time.Second},
		timeout:    30 * time.Second,
	}
}

// UpdatePVCPromoteState satisfies checkpointstore.CatalogStateWriter.
// hash is the full content hash (the producer side never has access
// to the per-capture catalog id). state must be one of the documented
// values (pending/writing/snapshotting/ready/failed); pvcName is the
// rox-<short-hash> name to publish on the ready transition (empty
// otherwise, which the server's COALESCE preserves).
//
// Returns an error if the request fails or the server returns
// non-2xx. The Backend's caller (the agent CRIU path) treats L2
// promote as best-effort — a single transition failure won't fail
// capture; the row stays in whatever state was last written and the
// restore-side resolver falls back to peer cascade.
func (w *pvcStateHTTPWriter) UpdatePVCPromoteState(hash, state, pvcName string) error {
	if w.catalogURL == "" {
		return fmt.Errorf("pvcStateHTTPWriter: catalogURL not configured")
	}
	body, _ := json.Marshal(struct {
		State   string `json:"state"`
		PVCName string `json:"pvc_name,omitempty"`
	}{
		State:   state,
		PVCName: pvcName,
	})
	u := fmt.Sprintf("%s/api/v1/checkpoints/by-hash/%s/pvc-state",
		w.catalogURL, url.PathEscape(hash))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	// 404 → no catalog row with this hash yet. Surface as the typed
	// sentinel so PerCapturePVCBackend.setState can distinguish "try
	// again later" from real failures. The rootfs path hits this
	// because nvsnap-server only writes the row's hash field AFTER
	// agent's Backend.Put completes. See ErrCatalogHashNotFound docs.
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("pvc-state %s → 404: %s: %w", state, string(b), checkpointstore.ErrCatalogHashNotFound)
	}
	return fmt.Errorf("pvc-state %s → %d: %s", state, resp.StatusCode, string(b))
}
