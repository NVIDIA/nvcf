/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package nvsnap is the NVCA-side client for the NvSnap checkpoint/restore
// HTTP API exposed by nvsnap-server. This package is a thin transport
// layer only — no business logic, no policy decisions, no
// CreatePodArtifactInstances integration. Those land in subsequent PRs
// once the no-op baseline (this client + feature flag) is merged.
//
// Design reference: docs/users/nvsnap/NVSNAP-INTEGRATION-DESIGN.md
//
// Endpoints consumed:
//
//	POST   /api/v1/checkpoints           CreateCheckpoint
//	GET    /api/v1/checkpoints/<id>      GetCheckpoint
//	DELETE /api/v1/checkpoints/<id>      DeleteCheckpoint
//
// Checkpoint and Restore lifecycle on the nvsnap side is asynchronous
// — POST returns 202 with an id, and the caller polls GET until
// Phase reaches a terminal value (Completed | Failed). NVCA's
// post-Ready reconciler is what drives that polling; the client just
// exposes the raw operations.
package nvsnap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default base URL for the in-cluster nvsnap-server Service. Callers
// override with WithBaseURL — operators may run nvsnap out-of-cluster
// or behind a custom ingress.
const DefaultBaseURL = "http://nvsnap-server.nvsnap-system.svc.cluster.local:8080"

// Default per-request timeout. Checkpoint create returns 202 quickly;
// long-running operations are polled via GetCheckpoint. 30s is
// generous for the synchronous POST and avoids head-of-line blocking
// if nvsnap-server is briefly slow.
const DefaultTimeout = 30 * time.Second

// Terminal phase values for Checkpoint.Phase. Non-terminal values
// (InProgress) mean the caller should keep polling.
const (
	PhaseInProgress = "InProgress"
	PhaseCompleted  = "Completed"
	PhaseFailed     = "Failed"
)

// Client is the nvsnap-server HTTP client. Construct via NewClient.
//
// Safe for concurrent use by multiple goroutines.
type Client struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
}

// Option configures the Client. Pattern matches what's idiomatic
// across other NVCA HTTP clients.
type Option func(*Client)

// WithBaseURL overrides DefaultBaseURL — used for out-of-cluster
// deployments or tests that point at httptest.Server.
func WithBaseURL(url string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient overrides the underlying *http.Client. Useful for
// injecting timeouts, transport-level tracing, or test doubles.
// Default = &http.Client{Timeout: DefaultTimeout}.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithUserAgent overrides the User-Agent header. Default
// "nvca-nvsnap-client/1".
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		c.userAgent = ua
	}
}

// NewClient returns a Client. With no options, it talks to the
// in-cluster nvsnap-server Service with a 30s timeout.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: DefaultTimeout},
		userAgent:  "nvca-nvsnap-client/1",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// CheckpointRequest is the body of POST /api/v1/checkpoints. Namespace
// and PodName are required; ContainerName defaults to single-container
// pods on the nvsnap side. LeaveRunning is true by default for NVCA's
// use case (don't kill the source pod after checkpoint).
type CheckpointRequest struct {
	Namespace     string `json:"namespace"`
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName,omitempty"`
	LeaveRunning  bool   `json:"leaveRunning"`
}

// Checkpoint is the response shape from POST /api/v1/checkpoints and
// GET /api/v1/checkpoints/<id>. Fields are populated incrementally:
// POST returns ID + Phase=InProgress; GET adds Hash + Size + Duration
// when Phase reaches Completed (or Error when Phase=Failed).
type Checkpoint struct {
	ID        string  `json:"id"`
	Namespace string  `json:"namespace,omitempty"`
	Phase     string  `json:"phase"`
	Message   string  `json:"message,omitempty"`
	Hash      string  `json:"hash,omitempty"`
	Path      string  `json:"path,omitempty"`
	Size      int64   `json:"size,omitempty"`
	Duration  float64 `json:"duration,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// IsTerminal reports whether the checkpoint has reached a final phase.
// Callers poll GetCheckpoint while !IsTerminal.
func (c Checkpoint) IsTerminal() bool {
	return c.Phase == PhaseCompleted || c.Phase == PhaseFailed
}

// CreateCheckpoint kicks off a checkpoint for the named container in
// the named pod. Returns immediately with Phase=InProgress and the
// checkpoint ID; caller polls GetCheckpoint for the terminal status.
//
// Returns an error only for transport failures or non-2xx responses.
// A "succeeded but with errors" terminal state is communicated via
// the returned Checkpoint's Phase=Failed + Error.
func (c *Client) CreateCheckpoint(ctx context.Context, req CheckpointRequest) (*Checkpoint, error) {
	if req.Namespace == "" || req.PodName == "" {
		return nil, errors.New("nvsnap: CheckpointRequest.Namespace and PodName are required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	var out Checkpoint
	if err := c.do(ctx, http.MethodPost, "/api/v1/checkpoints", bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCheckpoint fetches the current state of a checkpoint by id.
// Returns *APIError with StatusCode=404 if the checkpoint doesn't
// exist (or is being deleted).
func (c *Client) GetCheckpoint(ctx context.Context, id string) (*Checkpoint, error) {
	if id == "" {
		return nil, errors.New("nvsnap: id required")
	}
	path := "/api/v1/checkpoints/" + url.PathEscape(id)
	var out Checkpoint
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LookupRequest asks nvsnap-server "do you have a checkpoint for this
// canonical workload identity?" The response carries any matching
// catalog rows, freshest-first. Used by Hook A to dedup checkpoints
// across function-version-IDs (nvnvsnap#59 / nvca cross-fvID restore):
// two functions with the same image + model + flags + driver should
// share one checkpoint instead of each cold-starting and re-capturing.
//
// ImageRef is required (the indexed lookup key — kubelet has it on
// every pod spec); the rest narrows the match. EngineFlags is
// canonicalized server-side (sort + strip --model*) before
// comparison. DriverMajor=0 means "match any driver".
type LookupRequest struct {
	ImageRef    string   `json:"imageRef"`
	ModelID     string   `json:"modelId,omitempty"`
	EngineFlags []string `json:"engineFlags,omitempty"`
	DriverMajor int      `json:"driverMajor,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

// LookupMatch is one row of LookupResponse. Just enough for NVCA to
// decide "restore from this hash" — the rest of CatalogInfo (function
// name, GPU type, etc.) is accessible via GetCheckpoint(id).
type LookupMatch struct {
	Hash           string    `json:"hash"`
	CheckpointID   string    `json:"checkpointId"`
	CapturedAt     time.Time `json:"capturedAt"`
	CapturedOnNode string    `json:"capturedOnNode"`
	ImageRef       string    `json:"imageRef"`
	ImageDigest    string    `json:"imageDigest,omitempty"`
	ModelID        string    `json:"modelId,omitempty"`
	GPUType        string    `json:"gpuType,omitempty"`
	DriverVersion  string    `json:"driverVersion,omitempty"`
}

// LookupResponse is the body of POST /api/v1/checkpoints/lookup.
// Matches is always non-nil (empty array on no match), so callers
// can range without nil-checking.
type LookupResponse struct {
	Matches []LookupMatch `json:"matches"`
}

// LookupCheckpoints asks nvsnap-server for catalog rows matching the
// canonical workload identity. Returns the response struct (possibly
// with empty Matches) on 200; any non-2xx is an error.
//
// Used by Hook A. Empty result is NOT an error — it just means
// "this is a Cold start, fall through to the capture flow".
func (c *Client) LookupCheckpoints(ctx context.Context, req LookupRequest) (*LookupResponse, error) {
	if req.ImageRef == "" {
		return nil, errors.New("nvsnap: LookupRequest.ImageRef required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("nvsnap: marshal lookup: %w", err)
	}
	var out LookupResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/checkpoints/lookup", bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PVCPromoteState mirrors the catalog row for the L2 per-capture
// PVC pipeline. The reconciler (nvca#179) uses this to gate the
// CFS=Warm transition on "rox-<hash> is actually ready to serve" —
// without it, NVCA flips CFS=Warm the moment the CRIU checkpoint
// reaches terminal phase, even though the agent's async snap+clone
// promote is still in flight. Restored function pods then race the
// promote and either stall in ContainerCreating or get a partial
// dump.
//
// State values (mirroring nvsnap-server's pvcPromoteStateResponse):
//
//	""              L2 disabled at agent startup, or agent died
//	                before the first state write. Treat as "no L2,
//	                proceed with whatever fallback the restore path
//	                uses" (peer cascade in production today).
//	"pending"       lease acquired, writer Job not yet started
//	"writing"       writer Job running (rwx-<hash> filling up)
//	"snapshotting"  VolumeSnapshot + rox-<hash> clone in progress
//	"ready"         rox-<hash> Bound and serving — safe to admit
//	                restore pods
//	"failed"        promote terminally failed; rox-<hash> won't
//	                appear. Restored pods must cold-start.
type PVCPromoteState struct {
	Hash    string `json:"hash"`
	State   string `json:"state"`
	PVCName string `json:"pvc_name,omitempty"`
}

// IsTerminal reports whether the state can no longer change. Used by
// the reconciler's poll loop to break out (and decide pass/fail).
func (s PVCPromoteState) IsTerminal() bool {
	switch s.State {
	case "ready", "failed", "":
		return true
	default:
		return false
	}
}

// GetPVCPromoteState fetches the L2 promote-state for a checkpoint
// hash. Returns *APIError with StatusCode=404 if the catalog has no
// row for that hash (which the reconciler treats as "no L2" — same
// as the empty-state case).
//
// Symmetric to the nvsnap-server's
//
//	GET /api/v1/checkpoints/by-hash/{hash}/pvc-state
//
// endpoint (internal/server/sources.go in the nvsnap repo).
func (c *Client) GetPVCPromoteState(ctx context.Context, hash string) (*PVCPromoteState, error) {
	if hash == "" {
		return nil, errors.New("nvsnap: hash required")
	}
	path := "/api/v1/checkpoints/by-hash/" + url.PathEscape(hash) + "/pvc-state"
	var out PVCPromoteState
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteCheckpoint removes a checkpoint from the nvsnap-server catalog
// (and best-effort the blobs). Idempotent: 404 is treated as success
// so callers can call this from a cleanup pass without checking
// existence first.
func (c *Client) DeleteCheckpoint(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("nvsnap: id required")
	}
	path := "/api/v1/checkpoints/" + url.PathEscape(id)
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

// APIError is returned for non-2xx HTTP responses. Lets callers
// distinguish "transport failed" (a generic error) from "server said
// no" (*APIError with a status code).
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("nvsnap: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("nvsnap: HTTP %d: %s", e.StatusCode, e.Body)
}

// do issues a request, decoding a 2xx body into `out` (if non-nil).
// Returns *APIError on non-2xx so callers can inspect StatusCode.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Cap body read so a misbehaving server can't OOM us.
		const maxErrBody = 16 << 10
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return &APIError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(b)),
		}
	}
	if out == nil {
		return nil
	}
	// 204 No Content has no body — return without decoding.
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
