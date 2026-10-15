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

// Command nvsnap-mount-prep is the init container the webhook injects
// into restored pods. It calls the nvsnap-agent's async overlay-prep
// API and waits for the prep job to finish before exiting, so
// kubelet sees the bind-mounted OverlayFS paths ready by the time
// the main container starts.
//
// See docs/proposals/init-container-mount-prep.md (nvsnap#202) for
// the why. Short version: doing 355 OverlayFS mounts inside a K8s
// mutating-admission webhook exceeded the 10–30 s timeout budget;
// moving the work into pod lifecycle (here) lets it take however
// long it needs without admission pressure.
//
// Failure modes (all surface in `kubectl describe pod` because this
// is an ordinary init container):
//
//	exit 1  → prep job reported state="failed"; stderr lists which
//	          paths failed and why
//	exit 2  → polling timed out (agent unreachable or stuck)
//	exit 3  → required env var missing / malformed
//
// Reading config:
//
//	NVSNAP_POD_UID         (required) downward API: metadata.uid
//	NVSNAP_RESTORE_HASH    (required) full sha256 of the capture
//	NVSNAP_AGENT_URL       (required) e.g. http://$(HOST_IP):8081
//	NVSNAP_CAPTURE_NODE    (optional) where capture data lives; empty=this node
//	NVSNAP_PREP_MOUNTS     (required) JSON-encoded []VolumeMeta from the manifest
//	NVSNAP_PREP_DEADLINE   (optional) duration; default 15m
//	NVSNAP_PREP_POLL       (optional) duration between status polls; default 500ms
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// prepRequest mirrors agent.PrepRequest. We don't import the agent
// package to keep this binary tiny and reduce coupling — the JSON
// shape is the API contract.
type prepRequest struct {
	PodUID      string            `json:"podUID"`
	CaptureHash string            `json:"captureHash"`
	CaptureNode string            `json:"captureNode,omitempty"`
	Mounts      []json.RawMessage `json:"mounts"` // opaque pass-through
}

type prepStatus struct {
	State    string `json:"state"`
	Total    int    `json:"total"`
	Prepared int    `json:"prepared"`
	Failures []struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	} `json:"failures,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nvsnap-mount-prep: %v\n", err)
		// run() returns nil on success; any error here is a config /
		// network / failure-state problem. Exit code is set inside
		// run() via os.Exit so this fallthrough is the last-resort
		// path (config errors).
		os.Exit(3)
	}
}

func run() error {
	podUID := mustEnv("NVSNAP_POD_UID")
	hash := mustEnv("NVSNAP_RESTORE_HASH")
	agentURL := strings.TrimRight(mustEnv("NVSNAP_AGENT_URL"), "/")
	captureNode := os.Getenv("NVSNAP_CAPTURE_NODE")
	mountsJSON := mustEnv("NVSNAP_PREP_MOUNTS")

	deadline := parseDur(os.Getenv("NVSNAP_PREP_DEADLINE"), 15*time.Minute)
	poll := parseDur(os.Getenv("NVSNAP_PREP_POLL"), 500*time.Millisecond)

	// Mounts is encoded as a JSON array; we pass it through as
	// opaque sub-documents so we don't have to mirror the
	// VolumeMeta struct here.
	var mounts []json.RawMessage
	if err := json.Unmarshal([]byte(mountsJSON), &mounts); err != nil {
		return fmt.Errorf("NVSNAP_PREP_MOUNTS not a JSON array: %w", err)
	}

	req := prepRequest{
		PodUID:      podUID,
		CaptureHash: hash,
		CaptureNode: captureNode,
		Mounts:      mounts,
	}

	fmt.Printf("nvsnap-mount-prep: starting prep for pod=%s hash=%s mounts=%d agent=%s captureNode=%s\n",
		podUID, hash[:12], len(mounts), agentURL, captureNode)

	// POST to start. Retry briefly on transient errors — the agent
	// may be mid-restart on its DaemonSet rolling update.
	if err := startWithRetry(agentURL, req); err != nil {
		return fmt.Errorf("start prep: %w", err)
	}

	// Poll until ready, failed, or deadline.
	stop := time.Now().Add(deadline)
	lastPrepared := -1
	for time.Now().Before(stop) {
		s, err := getStatus(agentURL, podUID)
		if err != nil {
			fmt.Printf("nvsnap-mount-prep: status poll error (will retry): %v\n", err)
			time.Sleep(poll)
			continue
		}
		// Progress log only on change so we don't flood for ~3 s of
		// repeats-at-100%.
		if s.Prepared != lastPrepared {
			fmt.Printf("nvsnap-mount-prep: state=%s prepared=%d/%d\n", s.State, s.Prepared, s.Total)
			lastPrepared = s.Prepared
		}
		switch s.State {
		case "ready":
			fmt.Printf("nvsnap-mount-prep: ready (%d mounts in %s)\n", s.Total, time.Since(time.Now().Add(-time.Since(stop.Add(-deadline)))).Round(time.Millisecond))
			os.Exit(0)
		case "failed":
			fmt.Fprintf(os.Stderr, "nvsnap-mount-prep: prep failed (%d/%d succeeded)\n", s.Prepared, s.Total)
			for _, f := range s.Failures {
				fmt.Fprintf(os.Stderr, "  - %s: %s\n", f.Path, f.Error)
			}
			os.Exit(1)
		}
		time.Sleep(poll)
	}
	fmt.Fprintf(os.Stderr, "nvsnap-mount-prep: deadline %s reached without ready/failed signal\n", deadline)
	os.Exit(2)
	return nil // unreachable
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "nvsnap-mount-prep: required env %s is empty\n", name)
		os.Exit(3)
	}
	return v
}

func parseDur(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvsnap-mount-prep: bad duration %q (%v), using default %s\n", s, err, def)
		return def
	}
	return d
}

// startWithRetry POSTs the prep request. Retries on transient
// errors (5xx, network) for up to 30 s; permanent (4xx other than 404)
// errors fail immediately. 404 is treated as transient because
// during DaemonSet rolling restart the route may not yet be
// registered on the agent we hit.
func startWithRetry(agentURL string, req prepRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, agentURL+"/v1/restore/prep", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		// drain + close so the connection can be reused
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
			return nil
		}
		// 4xx except 404 — permanent (bad request, etc).
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("agent rejected request: %s: %s", resp.Status, string(buf))
		}
		lastErr = fmt.Errorf("agent transient response %s: %s", resp.Status, string(buf))
		time.Sleep(500 * time.Millisecond)
	}
	return lastErr
}

func getStatus(agentURL, podUID string) (*prepStatus, error) {
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, agentURL+"/v1/restore/prep/"+podUID, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// Agent hasn't seen our POST yet (rolling restart raced
		// between POST handler and status handler). Surface as
		// "preparing" so the caller keeps polling.
		return &prepStatus{State: "preparing"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %s: %s", resp.Status, string(buf))
	}
	var s prepStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	return &s, nil
}
