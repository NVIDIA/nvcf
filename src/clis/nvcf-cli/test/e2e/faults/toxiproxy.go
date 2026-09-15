//go:build e2e

/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// Package faults provides helpers for fault-injection E2E tests (T11-T17).
// All helpers require external tooling (toxiproxy-server, cqlsh, helm) and are
// guarded by the NVCF_E2E_FAULTS=1 environment variable at the test level.
package faults

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Toxiproxy wraps a toxiproxy-server subprocess and its HTTP admin API. Create
// one per test so injected failures don't leak between tests.
//
// Usage:
//
//	tp, err := faults.Start(ctx, 8474)
//	require.NoError(t, err, "toxiproxy-server not on PATH; install it first")
//	t.Cleanup(func() { tp.Stop() })
//	require.NoError(t, tp.AddProxy("sis", "127.0.0.1:8888", "sis-host:443"))
type Toxiproxy struct {
	cmd    *exec.Cmd
	apiURL string   // e.g. "http://127.0.0.1:8474"
	names  []string // proxy names registered so far (for logging/cleanup)
}

// Start launches toxiproxy-server on listenPort and waits up to 5 s for the
// admin API to become reachable. Returns an error if toxiproxy-server is not
// on PATH.
//
// Caller MUST call t.Cleanup(func() { tp.Stop() }) to avoid lingering processes.
func Start(ctx context.Context, listenPort int) (*Toxiproxy, error) {
	if _, err := exec.LookPath("toxiproxy-server"); err != nil {
		return nil, fmt.Errorf("toxiproxy-server not on PATH (install from https://github.com/Shopify/toxiproxy/releases): %w", err)
	}
	apiURL := fmt.Sprintf("http://127.0.0.1:%d", listenPort)
	cmd := exec.CommandContext(ctx, "toxiproxy-server",
		"-host", "127.0.0.1",
		"-port", strconv.Itoa(listenPort),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start toxiproxy-server: %w", err)
	}
	// Poll until the admin API is reachable.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("toxiproxy-server admin API not reachable within 5s on port %d", listenPort)
		}
		resp, err := http.Get(apiURL + "/version") //nolint:noctx
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &Toxiproxy{cmd: cmd, apiURL: apiURL}, nil
}

// AddProxy creates a TCP proxy named name, listening on listen and forwarding
// to upstream. Both listen and upstream are "host:port" strings.
func (t *Toxiproxy) AddProxy(name, listen, upstream string) error {
	body, err := json.Marshal(map[string]any{
		"name":     name,
		"listen":   listen,
		"upstream": upstream,
		"enabled":  true,
	})
	if err != nil {
		return fmt.Errorf("marshal proxy body: %w", err)
	}
	resp, err := http.Post(t.apiURL+"/proxies", "application/json", strings.NewReader(string(body))) //nolint:noctx
	if err != nil {
		return fmt.Errorf("POST /proxies: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add proxy %q: HTTP %d: %s", name, resp.StatusCode, b)
	}
	t.names = append(t.names, name)
	return nil
}

// AddToxic adds a toxic to a proxy.
//
// Common toxic types:
//   - "latency"    — attrs: {"latency": <ms>, "jitter": <ms>}
//   - "limit_data" — attrs: {"bytes": <n>}   (closes connection after n bytes)
//   - "timeout"    — attrs: {"timeout": <ms>} (closes after timeout)
//   - "slow_close" — attrs: {"delay": <ms>}
//
// For HTTP-level fault injection (5xx status codes), use a separate
// httptest.Server middleware in front of the real service instead — toxiproxy
// operates at the TCP layer and cannot synthesise HTTP responses.
func (t *Toxiproxy) AddToxic(proxyName, toxicName, toxicType string, attrs map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"name":       toxicName,
		"type":       toxicType,
		"stream":     "downstream",
		"toxicity":   1.0,
		"attributes": attrs,
	})
	if err != nil {
		return fmt.Errorf("marshal toxic body: %w", err)
	}
	u := t.apiURL + "/proxies/" + url.PathEscape(proxyName) + "/toxics"
	resp, err := http.Post(u, "application/json", strings.NewReader(string(body))) //nolint:noctx
	if err != nil {
		return fmt.Errorf("POST %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add toxic %q to %q: HTTP %d: %s", toxicName, proxyName, resp.StatusCode, b)
	}
	return nil
}

// RemoveToxic deletes a toxic from a proxy (used to restore normal connectivity
// and verify that the orchestrator converges after a transient fault is cleared).
func (t *Toxiproxy) RemoveToxic(proxyName, toxicName string) error {
	u := t.apiURL + "/proxies/" + url.PathEscape(proxyName) + "/toxics/" + url.PathEscape(toxicName)
	req, err := http.NewRequest(http.MethodDelete, u, nil) //nolint:noctx
	if err != nil {
		return fmt.Errorf("build DELETE request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", u, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("remove toxic %q from %q: HTTP %d", toxicName, proxyName, resp.StatusCode)
	}
	return nil
}

// Stop kills the toxiproxy-server subprocess and waits for it to exit.
// Idempotent — safe to call multiple times or when already stopped.
func (t *Toxiproxy) Stop() error {
	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	// Best-effort kill; ignore "already finished" errors.
	_ = t.cmd.Process.Kill()
	_ = t.cmd.Wait()
	t.cmd = nil
	return nil
}
