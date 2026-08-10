// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command checkpoint-restore is a reference client for the nvsnap-server
// REST API. It walks the full lifecycle against a running cluster:
//
//  1. discover GPU pods            GET    /api/v1/pods
//  2. checkpoint one               POST   /api/v1/checkpoints   (202 + id)
//  3. poll until terminal          GET    /api/v1/checkpoints/{id}
//  4. list the catalog             GET    /api/v1/checkpoints?source=db
//  5. (optional) restore           POST   /api/v1/restores      (202 + id)
//  6. (optional) delete            DELETE /api/v1/checkpoints/{id}
//
// It deliberately depends only on the Go standard library and talks to
// nvsnap-server's documented API (internal/server/openapi.yaml) — nothing
// in this file imports nvsnap internals, so it doubles as a copy-paste
// starting point for your own integration.
//
// Usage:
//
//	# port-forward the server, then:
//	go run ./examples/checkpoint-restore \
//	    -server http://localhost:8080 -namespace default
//
// By default it checkpoints the first Running GPU pod it finds and stops
// there. Add -restore to launch a restore from the new checkpoint, and
// -delete to clean it up afterward.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func main() {
	var (
		server       = flag.String("server", "http://localhost:8080", "nvsnap-server base URL")
		namespace    = flag.String("namespace", "default", "namespace to operate in")
		podName      = flag.String("pod", "", "pod to checkpoint (default: first Running GPU pod in -namespace)")
		leaveRunning = flag.Bool("leave-running", false, "keep the source process running after checkpoint")
		doRestore    = flag.Bool("restore", false, "restore from the new checkpoint after it completes")
		doDelete     = flag.Bool("delete", false, "delete the checkpoint at the end")
		timeout      = flag.Duration("timeout", 15*time.Minute, "max time to wait for an async op to finish")
	)
	flag.Parse()

	c := &client{base: strings.TrimRight(*server, "/"), http: &http.Client{Timeout: 30 * time.Second}}
	ctx := context.Background()

	// 1. Pick a target pod.
	if *podName == "" {
		p, err := c.firstRunningGPUPod(ctx, *namespace)
		if err != nil {
			log.Fatalf("find GPU pod: %v", err)
		}
		*podName = p
		log.Printf("auto-selected pod %s/%s", *namespace, *podName)
	}

	// 2. Start the checkpoint. The API is async: 202 + an opaque id.
	// Single-GPU pods go through CRIU + cuda-checkpoint; multi-GPU pods
	// auto-route to the rootfs path — same contract, no caller change.
	op, err := c.createCheckpoint(ctx, createCheckpointRequest{
		PodName:      *podName,
		Namespace:    *namespace,
		LeaveRunning: *leaveRunning,
	})
	if err != nil {
		log.Fatalf("create checkpoint: %v", err)
	}
	log.Printf("checkpoint started: id=%s phase=%s", op.ID, op.Phase)

	// 3. Poll until the checkpoint reaches a terminal phase.
	if err := c.waitCheckpoint(ctx, op.ID, *namespace, *timeout); err != nil {
		log.Fatalf("checkpoint did not complete: %v", err)
	}
	log.Printf("checkpoint %s completed", op.ID)

	// 4. Show the durable catalog (source=db) — what a UI or operator sees.
	cks, err := c.listCheckpoints(ctx, *namespace)
	if err != nil {
		log.Fatalf("list checkpoints: %v", err)
	}
	log.Printf("catalog now holds %d checkpoint(s) in %s", len(cks), *namespace)
	for _, ck := range cks {
		log.Printf("  - %s phase=%s", ck.ID, ck.Phase)
	}

	// 5. Optionally restore. createRestore launches a placeholder pod,
	// fetches the dump via the L1/L2/L3 cascade, then replays it.
	if *doRestore {
		rop, err := c.createRestore(ctx, createRestoreRequest{
			CheckpointName: op.ID,
			Namespace:      *namespace,
		})
		if err != nil {
			log.Fatalf("create restore: %v", err)
		}
		log.Printf("restore started: id=%s phase=%s", rop.ID, rop.Phase)
		if err := c.waitRestore(ctx, rop.ID, *namespace, *timeout); err != nil {
			log.Fatalf("restore did not complete: %v", err)
		}
		log.Printf("restore %s completed", rop.ID)
	}

	// 6. Optionally delete the checkpoint (on-disk dump + catalog row).
	if *doDelete {
		if err := c.deleteCheckpoint(ctx, op.ID); err != nil {
			log.Fatalf("delete checkpoint: %v", err)
		}
		log.Printf("checkpoint %s deleted", op.ID)
	}
}

// ── tiny API client ───────────────────────────────────────────────────

type client struct {
	base string
	http *http.Client
}

// request types mirror internal/server/openapi.yaml.
type createCheckpointRequest struct {
	PodName       string `json:"podName"`
	Namespace     string `json:"namespace"`
	ContainerName string `json:"containerName,omitempty"`
	LeaveRunning  bool   `json:"leaveRunning,omitempty"`
}

type createRestoreRequest struct {
	CheckpointName string `json:"checkpointName"`
	NewPodName     string `json:"newPodName,omitempty"`
	NodeName       string `json:"nodeName,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
}

// operationAccepted is the 202 body for both checkpoint and restore.
type operationAccepted struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase"`
	Message   string `json:"message"`
}

type pod struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	GPUCount  int    `json:"gpuCount"`
}

type podList struct {
	Pods []pod `json:"pods"`
}

// checkpoint is the subset of fields shared by every checkpoint shape the
// API returns (CR / agent / db); we only need id + phase here.
type checkpoint struct {
	ID    string `json:"id"`
	Phase string `json:"phase"`
}

type checkpointList struct {
	Checkpoints []checkpoint `json:"checkpoints"`
}

type restore struct {
	ID    string `json:"id"`
	Phase string `json:"phase"`
}

func (c *client) firstRunningGPUPod(ctx context.Context, ns string) (string, error) {
	var pl podList
	q := url.Values{"namespace": {ns}}
	if err := c.do(ctx, http.MethodGet, "/api/v1/pods?"+q.Encode(), nil, &pl); err != nil {
		return "", err
	}
	for _, p := range pl.Pods {
		if p.Status == "Running" && p.GPUCount > 0 {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no Running GPU pod found in namespace %q", ns)
}

func (c *client) createCheckpoint(ctx context.Context, req createCheckpointRequest) (operationAccepted, error) {
	var op operationAccepted
	err := c.do(ctx, http.MethodPost, "/api/v1/checkpoints", req, &op)
	return op, err
}

func (c *client) createRestore(ctx context.Context, req createRestoreRequest) (operationAccepted, error) {
	var op operationAccepted
	err := c.do(ctx, http.MethodPost, "/api/v1/restores", req, &op)
	return op, err
}

func (c *client) listCheckpoints(ctx context.Context, ns string) ([]checkpoint, error) {
	var cl checkpointList
	q := url.Values{"source": {"db"}, "namespace": {ns}, "limit": {"100"}}
	err := c.do(ctx, http.MethodGet, "/api/v1/checkpoints?"+q.Encode(), nil, &cl)
	return cl.Checkpoints, err
}

func (c *client) deleteCheckpoint(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/checkpoints/"+url.PathEscape(id), nil, nil)
}

// waitCheckpoint polls GET /api/v1/checkpoints/{id} until Completed/Failed.
func (c *client) waitCheckpoint(ctx context.Context, id, ns string, timeout time.Duration) error {
	return poll(ctx, timeout, func() (done bool, err error) {
		var ck checkpoint
		q := url.Values{"namespace": {ns}}
		if err := c.do(ctx, http.MethodGet, "/api/v1/checkpoints/"+url.PathEscape(id)+"?"+q.Encode(), nil, &ck); err != nil {
			return false, err
		}
		return terminal(ck.Phase, id)
	})
}

// waitRestore polls GET /api/v1/restores/{id} until Completed/Failed.
func (c *client) waitRestore(ctx context.Context, id, ns string, timeout time.Duration) error {
	return poll(ctx, timeout, func() (bool, error) {
		var r restore
		q := url.Values{"namespace": {ns}}
		if err := c.do(ctx, http.MethodGet, "/api/v1/restores/"+url.PathEscape(id)+"?"+q.Encode(), nil, &r); err != nil {
			return false, err
		}
		return terminal(r.Phase, id)
	})
}

// terminal reports whether phase is a final state, erroring on Failed.
func terminal(phase, id string) (bool, error) {
	switch phase {
	case "Completed", "Restored":
		return true, nil
	case "Failed":
		return false, fmt.Errorf("%s entered phase Failed", id)
	default:
		log.Printf("  %s: %s…", id, phase)
		return false, nil
	}
}

func poll(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// do issues one JSON request. body and out are optional. It treats any
// 2xx as success and surfaces the server's error body otherwise.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
