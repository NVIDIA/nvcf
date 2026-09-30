// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package health exposes liveness and readiness endpoints.
package health

import (
	"net/http"
	"sync/atomic"
)

// Handler exposes liveness and readiness for the uploader process.
type Handler struct {
	ready atomic.Bool
}

// New returns an unready Handler. The running process is always live.
func New() *Handler {
	return &Handler{}
}

// SetReady updates readiness after local startup checks complete.
func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

// Live handles the liveness endpoint.
func (h *Handler) Live(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Ready handles the readiness endpoint. It intentionally does not depend on
// a remote destination or the current backlog.
func (h *Handler) Ready(w http.ResponseWriter, _ *http.Request) {
	if !h.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
