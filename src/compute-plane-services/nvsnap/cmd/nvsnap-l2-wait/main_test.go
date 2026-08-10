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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stateServer is a controllable stub of nvsnap-server's GET pvc-state
// endpoint. tests set seq once and the server walks it from there;
// readers atomic-load the cursor so the poll loop is safe under -race.
type stateServer struct {
	seq    []promoteState // walked in order; last entry repeats forever
	status []int          // optional per-step HTTP status override (0 = 200)
	cursor atomic.Int32
	hits   atomic.Int32
}

func (s *stateServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		i := int(s.cursor.Load())
		if i >= len(s.seq) {
			i = len(s.seq) - 1
		}
		// Override status if explicitly set for this step. Advance
		// the cursor unconditionally — both the JSON-success and
		// status-override paths consume one entry from seq[].
		s.cursor.CompareAndSwap(int32(i), int32(i+1)) //nolint:gosec // i is bounded by len(seq), no overflow
		if i < len(s.status) && s.status[i] != 0 {
			w.WriteHeader(s.status[i])
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.seq[i])
	})
}

// TestRun_ReadyImmediately covers the fastest happy path: the very
// first poll returns state=ready, run() returns exitReady.
func TestRun_ReadyImmediately(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		seq: []promoteState{{Hash: "deadbeef", State: "ready", PVCName: "rox-deadbeef"}},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	var logs bytes.Buffer
	code := run(context.Background(), config{
		ServerURL:    srv.URL,
		Hash:         "deadbeef",
		PollInterval: 1 * time.Millisecond,
		MaxInterval:  10 * time.Millisecond,
		Timeout:      2 * time.Second,
		HTTPClient:   srv.Client(),
	}, &logs)

	if code != exitReady {
		t.Errorf("exit = %d, want %d (ready); logs:\n%s", code, exitReady, logs.String())
	}
	if stub.hits.Load() != 1 {
		t.Errorf("polled %d times, want 1 (state=ready on first poll)", stub.hits.Load())
	}
}

// TestRun_WalksPendingToReady simulates the realistic timeline: the
// agent posts pending → writing → snapshotting → ready over time.
// nvsnap-l2-wait must poll through all of them and only exit on ready.
func TestRun_WalksPendingToReady(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		seq: []promoteState{
			{Hash: "h", State: "pending"},
			{Hash: "h", State: "writing"},
			{Hash: "h", State: "snapshotting"},
			{Hash: "h", State: "ready", PVCName: "rox-h"},
		},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	var logs bytes.Buffer
	code := run(context.Background(), config{
		ServerURL:    srv.URL,
		Hash:         "h",
		PollInterval: 1 * time.Millisecond,
		MaxInterval:  5 * time.Millisecond,
		Timeout:      2 * time.Second,
		HTTPClient:   srv.Client(),
	}, &logs)

	if code != exitReady {
		t.Errorf("exit = %d, want %d; logs:\n%s", code, exitReady, logs.String())
	}
	if got := stub.hits.Load(); got < 4 {
		t.Errorf("polled %d times; expected at least 4 (one per state)", got)
	}
}

// TestRun_FailedTerminalReturnsExit1 — the snap+clone or writer Job
// reported failed; nvsnap-l2-wait must exit 1 (kubelet will fail the
// init step and the pod, NVCA's pod-failure handler restarts cold).
func TestRun_FailedTerminalReturnsExit1(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		seq: []promoteState{
			{Hash: "h", State: "pending"},
			{Hash: "h", State: "failed"},
		},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	var logs bytes.Buffer
	code := run(context.Background(), config{
		ServerURL:    srv.URL,
		Hash:         "h",
		PollInterval: 1 * time.Millisecond,
		MaxInterval:  5 * time.Millisecond,
		Timeout:      1 * time.Second,
		HTTPClient:   srv.Client(),
	}, &logs)

	if code != exitFailed {
		t.Errorf("exit = %d, want %d (failed); logs:\n%s", code, exitFailed, logs.String())
	}
}

// TestRun_TimeoutReturnsExit2 — the server stays at "pending"
// forever; the overall deadline fires; nvsnap-l2-wait returns exit 2
// so operators can grep "timeout" vs "failed" in the logs and
// distinguish stalls from active failures.
func TestRun_TimeoutReturnsExit2(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		seq: []promoteState{{Hash: "h", State: "pending"}},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	var logs bytes.Buffer
	code := run(context.Background(), config{
		ServerURL:    srv.URL,
		Hash:         "h",
		PollInterval: 5 * time.Millisecond,
		MaxInterval:  10 * time.Millisecond,
		Timeout:      50 * time.Millisecond,
		HTTPClient:   srv.Client(),
	}, &logs)

	if code != exitTimeout {
		t.Errorf("exit = %d, want %d (timeout); logs:\n%s", code, exitTimeout, logs.String())
	}
	// At least one poll should have happened during the window.
	if stub.hits.Load() < 1 {
		t.Errorf("no polls fired before timeout — backoff is too aggressive")
	}
}

// TestRun_CatalogMissKeepsPolling — 404 means "agent /register may
// not have landed". Must continue polling within the deadline rather
// than fail. Otherwise the wire race between agent /register and the
// webhook injecting nvsnap-l2-wait would frequently fail restores
// before the catalog row even exists.
func TestRun_CatalogMissKeepsPolling(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		// Both entries are sentinel "ready"; the status overrides
		// turn the first one into a 404 so we exercise the miss path.
		seq:    []promoteState{{State: "ready"}, {State: "ready", PVCName: "rox-h"}},
		status: []int{http.StatusNotFound, 0},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	var logs bytes.Buffer
	code := run(context.Background(), config{
		ServerURL:    srv.URL,
		Hash:         "h",
		PollInterval: 1 * time.Millisecond,
		MaxInterval:  5 * time.Millisecond,
		Timeout:      2 * time.Second,
		HTTPClient:   srv.Client(),
	}, &logs)

	if code != exitReady {
		t.Errorf("exit = %d, want %d (recovered from 404); logs:\n%s", code, exitReady, logs.String())
	}
	if !strings.Contains(logs.String(), "catalog miss") {
		t.Errorf("expected an info log about the catalog miss; got:\n%s", logs.String())
	}
}

// TestRun_EmptyStateAllowEmptyExits0 — the operator opt-in for
// "L2 disabled on this cluster" should let nvsnap-l2-wait become a
// no-op when AllowEmptyState=true. Without the opt-in nvsnap-l2-wait
// must keep polling (empty state could mean "agent will write a
// state soon").
func TestRun_EmptyStateAllowEmptyExits0(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		seq: []promoteState{{Hash: "h", State: ""}},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	var logs bytes.Buffer
	code := run(context.Background(), config{
		ServerURL:       srv.URL,
		Hash:            "h",
		PollInterval:    1 * time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Timeout:         200 * time.Millisecond,
		AllowEmptyState: true,
		HTTPClient:      srv.Client(),
	}, &logs)

	if code != exitReady {
		t.Errorf("exit = %d, want %d (AllowEmptyState=true treats empty as ready)", code, exitReady)
	}
}

// TestRun_EmptyStateNoAllowKeepsPolling — confirms the inverse of
// the above: without AllowEmptyState, empty state is treated as
// "keep polling" and the timeout fires.
func TestRun_EmptyStateNoAllowKeepsPolling(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		seq: []promoteState{{Hash: "h", State: ""}},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	var logs bytes.Buffer
	code := run(context.Background(), config{
		ServerURL:    srv.URL,
		Hash:         "h",
		PollInterval: 1 * time.Millisecond,
		MaxInterval:  5 * time.Millisecond,
		Timeout:      50 * time.Millisecond,
		HTTPClient:   srv.Client(),
	}, &logs)
	if code != exitTimeout {
		t.Errorf("exit = %d, want %d (no allow-empty → keep polling → timeout)", code, exitTimeout)
	}
}

// TestRun_MissingConfigReturnsExit3 — defensive guard against
// helm-template breakage; missing server URL or hash must not enter
// the poll loop (would spin against an empty URL forever).
func TestRun_MissingConfigReturnsExit3(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  config
	}{
		{"missing-url", config{Hash: "h"}},
		{"missing-hash", config{ServerURL: "http://x"}},
		{"both-missing", config{}},
	} {

		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			if code := run(context.Background(), tc.cfg, &logs); code != exitConfig {
				t.Errorf("exit = %d, want %d; logs: %s", code, exitConfig, logs.String())
			}
		})
	}
}

// TestRun_CancelContextReturnsExitFailed — SIGTERM from kubelet
// during pod teardown must terminate the binary promptly; we must
// NOT report success on a half-state. exitFailed lets the kubelet
// honor restartPolicy.
func TestRun_CancelContextReturnsExitFailed(t *testing.T) {
	t.Parallel()
	stub := &stateServer{
		seq: []promoteState{{Hash: "h", State: "pending"}},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay; the poll loop will be in time.After
	// when this fires, exercising the select{ctx.Done; time.After}
	// branch.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	var logs bytes.Buffer
	code := run(ctx, config{
		ServerURL:    srv.URL,
		Hash:         "h",
		PollInterval: 5 * time.Millisecond,
		MaxInterval:  10 * time.Millisecond,
		Timeout:      10 * time.Second,
		HTTPClient:   srv.Client(),
	}, &logs)

	if code != exitFailed {
		t.Errorf("exit = %d, want %d (ctx cancelled mid-poll)", code, exitFailed)
	}
}

// TestNextInterval — exponential backoff with the cap behavior.
// Locked here because the poll cadence is observable to operators
// (alert thresholds, runbook expectations) and changing it should
// be a deliberate decision.
func TestNextInterval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		current  time.Duration
		max      time.Duration
		wantMin  time.Duration
		wantMaxV time.Duration
	}{
		{1 * time.Second, 30 * time.Second, 2 * time.Second, 2*time.Second + 200*time.Millisecond}, // doubled + ~10% jitter
		{20 * time.Second, 30 * time.Second, 30 * time.Second, 30*time.Second + 3*time.Second},     // capped + jitter on cap
		{30 * time.Second, 30 * time.Second, 30 * time.Second, 30*time.Second + 3*time.Second},     // already at cap
	}
	for i, c := range cases {
		got := nextInterval(c.current, c.max)
		if got < c.wantMin || got > c.wantMaxV {
			t.Errorf("case %d: nextInterval(%v, %v) = %v; want [%v, %v]",
				i, c.current, c.max, got, c.wantMin, c.wantMaxV)
		}
	}
}

// TestClassify — terminal-vs-continue decision table. Pin the wire
// contract so a future state addition (e.g. "validating") doesn't
// silently become terminal.
func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state      string
		allowEmpty bool
		wantTerm   bool
		wantExit   int
	}{
		{"ready", false, true, exitReady},
		{"failed", false, true, exitFailed},
		{"", false, false, 0},
		{"", true, true, exitReady},
		{"pending", false, false, 0},
		{"writing", false, false, 0},
		{"snapshotting", false, false, 0},
		{"unknown-future-state", false, false, 0},
	}
	for _, c := range cases {
		gotTerm, gotExit := classify(c.state, c.allowEmpty)
		if gotTerm != c.wantTerm || gotExit != c.wantExit {
			t.Errorf("classify(%q, allowEmpty=%v) = (%v, %d); want (%v, %d)",
				c.state, c.allowEmpty, gotTerm, gotExit, c.wantTerm, c.wantExit)
		}
	}
}
