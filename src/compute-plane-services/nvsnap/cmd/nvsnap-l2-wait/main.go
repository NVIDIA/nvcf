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

// Package main implements nvsnap-l2-wait, the init container the mutating
// webhook injects onto
// restore pods (nvsnap#147). Its job is small and focused: poll
// nvsnap-server's GET /api/v1/checkpoints/by-hash/{hash}/pvc-state until
// the L2 PVC promote reaches a terminal state, then exit. The
// inference container is sequenced after this container by kubelet's
// initContainers chain, so by the time the engine starts the
// rox-<hash> PVC is guaranteed to be ready (or restoration has been
// abandoned cleanly).
//
// Design rationale:
//
//   - Customer pods have no K8s API credentials. nvsnap-server does.
//     This container talks to nvsnap-server over HTTP and learns the
//     L2 state without needing to read PVCs / snapshots directly.
//
//   - Exit codes drive kubelet's restart policy:
//     0  → terminal "ready". Init complete; main container starts.
//     1  → terminal "failed" (snap+clone aborted, agent crashed,
//     etc). Init failure; kubelet honors Pod.spec.restartPolicy;
//     for restartPolicy=Never the pod goes Failed and NVCA's
//     pod-failure handler kicks in (Cold fallback).
//     2  → timeout. Same kubelet treatment as code 1, but the log
//     line distinguishes "promote stalled" from "promote
//     actively failed" — operators care about that.
//     3  → configuration error (missing env, bad URL, ...) — bug.
//
//   - All logs are line-oriented JSON so kubectl logs + log
//     aggregators ingest cleanly. The status line on each poll
//     attempt includes elapsed, next-backoff, last-state — enough
//     for an operator to debug a stuck nvsnap-l2-wait without
//     attaching to the pod.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	// Exit codes — see file header for the contract.
	exitReady   = 0
	exitFailed  = 1
	exitTimeout = 2
	exitConfig  = 3
)

// Default values for the env-driven config knobs. Picked deliberately:
//
//	pollInterval=2s   roughly matches the agent's L2 state-transition
//	                  cadence (the agent posts updates after each
//	                  stage finishes; 2s polling adds at most one
//	                  poll-interval of latency on top).
//	maxInterval=30s   caps backoff so a slow snap+clone (60 GB+
//	                  dumps) doesn't end up polling every minute.
//	timeout=15m       GCP-H100-a 2026-06-03 baseline: 87 GiB
//	                  capture had snap+clone in 1m53s. 15 min is
//	                  ~8x headroom for a worst-case 200 GiB capture
//	                  on the slow default snapshot class.
const (
	defaultPollInterval = 2 * time.Second
	defaultMaxInterval  = 30 * time.Second
	defaultTimeout      = 15 * time.Minute
)

// promoteState mirrors the server's pvcPromoteStateResponse. Decoded
// here without importing internal/server to keep this binary's
// dependency footprint small (it ships in a 5 MB scratch image).
type promoteState struct {
	Hash    string `json:"hash"`
	State   string `json:"state"`
	PVCName string `json:"pvc_name,omitempty"`
}

// config is the immutable settings the binary derives from flags +
// env. Built once at startup; all functions operate on a value of
// this type so tests can construct one directly without touching
// process state.
type config struct {
	ServerURL    string
	Hash         string
	PollInterval time.Duration
	MaxInterval  time.Duration
	Timeout      time.Duration

	// AllowEmptyState exits 0 on the empty-string state instead of
	// continuing to poll. Set this on clusters where the L2 backend
	// may be disabled — nvsnap-init becomes a no-op and main container
	// proceeds with whatever fallback the inference image has built
	// in. Default false; the webhook decides per-injection.
	AllowEmptyState bool

	// HTTPClient is plumbed through so tests can inject an httptest
	// transport. nil → use a sane production-default client.
	HTTPClient *http.Client
}

// logEvent is the line-JSON shape emitted on every log call.
type logEvent struct {
	Time     string  `json:"time"`
	Level    string  `json:"level"`
	Msg      string  `json:"msg"`
	Hash     string  `json:"hash,omitempty"`
	State    string  `json:"state,omitempty"`
	PVCName  string  `json:"pvc_name,omitempty"`
	Elapsed  float64 `json:"elapsed_s,omitempty"`
	NextIn   float64 `json:"next_poll_s,omitempty"`
	HTTPCode int     `json:"http_code,omitempty"`
	Err      string  `json:"error,omitempty"`
}

// logTo writes a single JSON line to out (used in tests for capture).
// Production wires this to os.Stderr so kubectl logs picks it up
// independent of stdout buffering.
func logTo(out io.Writer, level, msg string, ev logEvent) {
	ev.Time = time.Now().UTC().Format(time.RFC3339Nano)
	ev.Level = level
	ev.Msg = msg
	b, _ := json.Marshal(ev)
	_, _ = fmt.Fprintln(out, string(b))
}

// fetchState makes ONE poll. Returns the decoded state on 200, an
// error on transport failure / non-2xx. 404 is reported as a typed
// error so the caller can distinguish "not registered yet" from
// "communication broken".
//
// errCatalogMiss = HTTP 404 from nvsnap-server. Means no catalog row
// carries the hash. nvsnap-init treats this as a transient (the
// agent's /register may not have landed) and continues polling
// within the overall timeout.
var errCatalogMiss = errors.New("catalog: no row for hash")

func fetchState(ctx context.Context, client *http.Client, baseURL, hash string) (promoteState, error) {
	url := fmt.Sprintf("%s/api/v1/checkpoints/by-hash/%s/pvc-state", strings.TrimRight(baseURL, "/"), hash)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return promoteState{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return promoteState{}, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return promoteState{}, errCatalogMiss
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return promoteState{}, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var ps promoteState
	if err := json.Unmarshal(body, &ps); err != nil {
		return promoteState{}, fmt.Errorf("decode: %w (body=%q)", err, string(body))
	}
	return ps, nil
}

// classify maps the wire state to (terminal, exitCode). Non-terminal
// states return (false, _) so the poll loop continues.
//
//	ready  → terminal success
//	failed → terminal failure
//	pending|writing|snapshotting → keep polling
//	""     → "no L2 write yet"; keep polling unless allowEmpty
//	other  → keep polling (forward-compat for future states)
func classify(state string, allowEmpty bool) (terminal bool, exit int) {
	switch state {
	case "ready":
		return true, exitReady
	case "failed":
		return true, exitFailed
	case "":
		if allowEmpty {
			return true, exitReady
		}
		return false, 0
	default:
		return false, 0
	}
}

// nextInterval computes the backoff for the next poll. Exponential
// with a cap, plus 10% jitter to avoid thundering-herd against
// nvsnap-server when many restore pods admit simultaneously.
func nextInterval(current, maxIv time.Duration) time.Duration {
	next := current * 2
	if next > maxIv {
		next = maxIv
	}
	jitter := time.Duration(rand.Int63n(int64(next) / 10)) //nolint:gosec // backoff jitter is not security-sensitive
	return next + jitter
}

// run is the main loop, extracted from main() so tests can drive it
// against an httptest.Server. Returns the process exit code.
func run(ctx context.Context, cfg config, out io.Writer) int {
	if cfg.ServerURL == "" || cfg.Hash == "" {
		logTo(out, "error", "missing required config", logEvent{Err: "ServerURL and Hash are required"})
		return exitConfig
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = defaultMaxInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	logTo(out, "info", "nvsnap-l2-wait starting", logEvent{
		Hash:    cfg.Hash,
		Elapsed: 0,
		NextIn:  cfg.PollInterval.Seconds(),
	})

	deadline := time.Now().Add(cfg.Timeout)
	interval := cfg.PollInterval
	start := time.Now()

	for {
		// Respect parent context cancellation (SIGTERM from kubelet
		// during graceful pod shutdown). Treat as failure so the
		// init container isn't reported as success on a half-state.
		if ctx.Err() != nil {
			logTo(out, "warn", "context cancelled (kubelet stopping pod?)", logEvent{
				Err:     ctx.Err().Error(),
				Elapsed: time.Since(start).Seconds(),
			})
			return exitFailed
		}
		if time.Now().After(deadline) {
			logTo(out, "error", "timeout waiting for L2 promote", logEvent{
				Hash:    cfg.Hash,
				Elapsed: time.Since(start).Seconds(),
			})
			return exitTimeout
		}

		// Bounded per-poll context so a hung server doesn't eat the
		// entire deadline on one request. 10s is well above the
		// server's healthy p99 (catalog read is a single SQLite
		// SELECT) but short enough to react to network issues.
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ps, err := fetchState(pollCtx, client, cfg.ServerURL, cfg.Hash)
		cancel()

		if err != nil {
			ev := logEvent{
				Hash:    cfg.Hash,
				Err:     err.Error(),
				Elapsed: time.Since(start).Seconds(),
				NextIn:  interval.Seconds(),
			}
			switch {
			case errors.Is(err, errCatalogMiss):
				logTo(out, "info", "catalog miss; agent /register may not have landed yet", ev)
			default:
				logTo(out, "warn", "poll failed; will retry", ev)
			}
		} else {
			terminal, code := classify(ps.State, cfg.AllowEmptyState)
			if terminal {
				logTo(out, "info", "terminal state reached", logEvent{
					Hash:    cfg.Hash,
					State:   ps.State,
					PVCName: ps.PVCName,
					Elapsed: time.Since(start).Seconds(),
				})
				return code
			}
			logTo(out, "info", "poll", logEvent{
				Hash:    cfg.Hash,
				State:   ps.State,
				Elapsed: time.Since(start).Seconds(),
				NextIn:  interval.Seconds(),
			})
		}

		// Sleep with cancellation. select{} avoids the leak that
		// time.Sleep(interval) creates when ctx is cancelled
		// mid-sleep — the init container should respond to SIGTERM
		// promptly during pod cleanup.
		select {
		case <-ctx.Done():
			return exitFailed
		case <-time.After(interval):
		}
		interval = nextInterval(interval, cfg.MaxInterval)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		// Bad value silently falls back to default; the startup log
		// records the effective value so operators see what we used.
	}
	return fallback
}

func envBoolOr(key string, fallback bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		return fallback
	}
}

func main() {
	// Flags are the wire contract; env is for Helm convenience. Flags
	// win on conflict so an operator running `kubectl debug` can
	// override env defaults without rewriting the deployment.
	serverURL := flag.String("server-url", envOr("NVSNAP_SERVER_URL", ""), "Base URL of nvsnap-server (e.g. http://nvsnap-server.nvsnap-system.svc.cluster.local:8080)")
	hash := flag.String("hash", envOr("NVSNAP_CHECKPOINT_HASH", ""), "Content hash of the checkpoint to wait for")
	pollInterval := flag.Duration("poll-interval", envDurationOr("NVSNAP_POLL_INTERVAL", defaultPollInterval), "Initial poll interval")
	maxInterval := flag.Duration("max-interval", envDurationOr("NVSNAP_MAX_POLL_INTERVAL", defaultMaxInterval), "Maximum poll interval (exponential backoff cap)")
	timeout := flag.Duration("timeout", envDurationOr("NVSNAP_WAIT_TIMEOUT", defaultTimeout), "Overall wait deadline")
	allowEmpty := flag.Bool("allow-empty-state", envBoolOr("NVSNAP_ALLOW_EMPTY_STATE", false), "Exit 0 on empty state (L2 disabled)")
	flag.Parse()

	cfg := config{
		ServerURL:       *serverURL,
		Hash:            *hash,
		PollInterval:    *pollInterval,
		MaxInterval:     *maxInterval,
		Timeout:         *timeout,
		AllowEmptyState: *allowEmpty,
	}

	// Plumb SIGTERM/SIGINT into ctx so kubelet can cleanly cancel
	// during pod shutdown without us hanging in time.Sleep.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)

	code := run(ctx, cfg, os.Stderr)
	cancel()
	os.Exit(code)
}
