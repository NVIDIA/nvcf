// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package health exposes liveness and readiness endpoints.
package health

import (
	"context"
	"errors"
	"fmt"
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

// Step is one local startup action that must finish before the uploader
// reports ready, such as taking the spool lock or loading the journal.
type Step struct {
	Name string
	Run  func(context.Context) error
}

// Start runs steps in order and marks the handler ready only after all of them
// succeed. Serve the handler before calling Start, so liveness answers while a
// slow step runs on a large backlog. A failing step, or a context that ends
// between steps, stops the sequence and leaves the handler unready; the caller
// exits non-zero. Remote destinations and telemetry never belong in a step.
func (h *Handler) Start(ctx context.Context, steps ...Step) error {
	for _, step := range steps {
		if step.Name == "" || step.Run == nil {
			return errors.New("startup step needs a name and a function")
		}
	}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("startup stopped before %s: %w", step.Name, err)
		}
		if err := step.Run(ctx); err != nil {
			return fmt.Errorf("startup step %s: %w", step.Name, err)
		}
	}
	h.SetReady(true)
	return nil
}
