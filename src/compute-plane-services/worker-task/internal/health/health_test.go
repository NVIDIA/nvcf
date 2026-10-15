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

package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvct"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

func TestNew(t *testing.T) {
	h := New()
	if h == nil {
		t.Fatal("New returned nil")
	}
	if got := h.GetExecutionStatus(); got != pb.ExecutionStatus_TASK_CONTAINER_INITIALIZING {
		t.Errorf("initial status = %v, want TASK_CONTAINER_INITIALIZING", got)
	}
	if h.GetReadinessStatus() {
		t.Error("fresh handler must not be ready")
	}
}

func TestSetGetExecutionStatus(t *testing.T) {
	h := New()
	h.SetExecutionStatus(pb.ExecutionStatus_RUNNING)
	if got := h.GetExecutionStatus(); got != pb.ExecutionStatus_RUNNING {
		t.Errorf("status = %v, want RUNNING", got)
	}

	// Overwrite to confirm the setter replaces rather than accumulates.
	h.SetExecutionStatus(pb.ExecutionStatus_COMPLETED)
	if got := h.GetExecutionStatus(); got != pb.ExecutionStatus_COMPLETED {
		t.Errorf("status = %v, want COMPLETED", got)
	}
}

func TestGetReadinessStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    pb.ExecutionStatus
		wantReady bool
	}{
		{"initializing", pb.ExecutionStatus_TASK_CONTAINER_INITIALIZING, false},
		{"queued", pb.ExecutionStatus_QUEUED, false},
		{"launched", pb.ExecutionStatus_LAUNCHED, false},
		{"running", pb.ExecutionStatus_RUNNING, true},
		{"completed", pb.ExecutionStatus_COMPLETED, false},
		{"errored", pb.ExecutionStatus_ERRORED, false},
		{"worker_terminated", pb.ExecutionStatus_WORKER_TERMINATED, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New()
			h.SetExecutionStatus(tt.status)
			if got := h.GetReadinessStatus(); got != tt.wantReady {
				t.Errorf("readiness for %v = %v, want %v", tt.status, got, tt.wantReady)
			}
		})
	}
}

func TestServeHTTP(t *testing.T) {
	tests := []struct {
		name     string
		status   pb.ExecutionStatus
		wantCode int
	}{
		{"not_ready_initializing", pb.ExecutionStatus_TASK_CONTAINER_INITIALIZING, http.StatusServiceUnavailable},
		{"not_ready_queued", pb.ExecutionStatus_QUEUED, http.StatusServiceUnavailable},
		{"not_ready_completed", pb.ExecutionStatus_COMPLETED, http.StatusServiceUnavailable},
		{"ready_running", pb.ExecutionStatus_RUNNING, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New()
			h.SetExecutionStatus(tt.status)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantCode)
			}
			if body := rec.Body.String(); body != "" {
				t.Errorf("body = %q, want empty", body)
			}
		})
	}
}

func TestServeHTTPStateTransition(t *testing.T) {
	h := New()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	// Not ready before transition.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("before transition: code = %d, want 503", rec.Code)
	}

	// Transition to RUNNING flips readiness.
	h.SetExecutionStatus(pb.ExecutionStatus_RUNNING)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("after RUNNING: code = %d, want 200", rec.Code)
	}

	// Transition away from RUNNING flips it back.
	h.SetExecutionStatus(pb.ExecutionStatus_COMPLETED)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("after COMPLETED: code = %d, want 503", rec.Code)
	}
}

// gatedContext lets the heartbeat loop run exactly one full body iteration and
// then terminate deterministically, with no timing-based sleep. On the first
// Err() call it reports nil so the loop proceeds into the body; its Done channel
// is already closed so SleepWithContext returns immediately; every subsequent
// Err() call reports cancellation so the next loop check exits the loop.
type gatedContext struct {
	context.Context
	done     chan struct{}
	errCalls int
}

func newGatedContext() *gatedContext {
	d := make(chan struct{})
	close(d)
	return &gatedContext{Context: context.Background(), done: d}
}

func (g *gatedContext) Done() <-chan struct{} { return g.done }

func (g *gatedContext) Err() error {
	g.errCalls++
	if g.errCalls == 1 {
		return nil // let the first iteration body run
	}
	return context.Canceled // terminate on the next loop check
}

// StartHeartbeat must run the send/log/sleep body and then exit when the context
// reports cancellation. Status is set to a value that is invalid for a success
// heartbeat so SendInProgressHeartbeat returns an error before any network or
// gRPC call (no real client connection is established), exercising the
// error-logging branch.
func TestStartHeartbeatBodyThenExit(t *testing.T) {
	h := New()
	// COMPLETED is not RUNNING/INITIALIZING, so SendInProgressHeartbeat returns
	// an error immediately without dialing the nil gRPC client.
	h.SetExecutionStatus(pb.ExecutionStatus_COMPLETED)

	ctx := newGatedContext()

	done := make(chan struct{})
	go func() {
		h.StartHeartbeat(ctx, &nvct.Client{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartHeartbeat did not return after gated cancellation")
	}
}

// StartHeartbeat must observe a cancelled context and return without ever
// touching the nvct client (the loop checks ctx.Err() before any send), so no
// network or gRPC connection is established.
func TestStartHeartbeatReturnsOnCancelledContext(t *testing.T) {
	h := New()
	h.SetExecutionStatus(pb.ExecutionStatus_RUNNING)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call so the first loop iteration returns.

	done := make(chan struct{})
	go func() {
		// nvctClient is never dereferenced because ctx is already cancelled.
		h.StartHeartbeat(ctx, &nvct.Client{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartHeartbeat did not return after context cancellation")
	}
}
