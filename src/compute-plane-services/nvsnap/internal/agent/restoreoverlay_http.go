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

// HTTP handlers exposing OverlayManager. The webhook's primary path is
// in-process (it calls a.overlay.Prepare directly during admission) —
// these endpoints exist for tests, manual cleanup, and any future
// out-of-band orchestrator.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// PrepareOverlayRequest is the body of POST /v1/restore/overlay.
//
// captureHash + Volume together resolve the lower (read-only) directory
// that backs the overlay. The Volume's MountPath is also the per-pod
// overlay key inside OverlayManager — two volumes cannot mount at the
// same MountPath, so it's stable and unique.
//
// LowerDirHint bypasses the captureHash-based resolution (tests rely on
// this). When set, Volume.MountPath is still required as the overlay key.
type PrepareOverlayRequest struct {
	PodUID       string                     `json:"podUID"`
	CaptureHash  string                     `json:"captureHash,omitempty"`
	Volume       checkpointstore.VolumeMeta `json:"volume,omitempty"`
	Tier         string                     `json:"tier,omitempty"`     // "local" | "rox" — informational only today
	LowerDirHint string                     `json:"lowerDir,omitempty"` // explicit override; tests rely on this
}

// PrepareOverlayResponse is the JSON body returned by the prepare-overlay handler.
type PrepareOverlayResponse struct {
	Mountpoint string `json:"mountpoint"`
}

func (a *Agent) prepareRestoreOverlayHandler(w http.ResponseWriter, r *http.Request) {
	var req PrepareOverlayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	if req.PodUID == "" || req.Volume.MountPath == "" {
		http.Error(w, "podUID and volume.mountPath are required", http.StatusBadRequest)
		return
	}

	lower, err := a.resolveOverlayLowerDir(r.Context(), req)
	if err != nil {
		a.log.WithError(err).WithField("req", req).Warn("resolve overlay lower")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Peer-routed / init-container restores reach overlay.Prepare here
	// rather than via the in-process PrepareOverlay, so prewarm must hook
	// this path too. Idempotent per lowerdir, so a double-call is a no-op.
	a.prewarmLowerDirAsync(lower)
	mp, err := a.overlay.Prepare(req.PodUID, lower, req.Volume.MountPath)
	if err != nil {
		a.log.WithError(err).WithField("req", req).Warn("prepare overlay")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, PrepareOverlayResponse{Mountpoint: mp})
}

func (a *Agent) cleanupRestoreOverlayHandler(w http.ResponseWriter, r *http.Request) {
	podUID := mux.Vars(r)["pod-uid"]
	if podUID == "" {
		http.Error(w, "pod-uid is required", http.StatusBadRequest)
		return
	}
	if err := a.overlay.Cleanup(podUID); err != nil {
		a.log.WithError(err).WithField("podUID", podUID).Warn("cleanup overlay")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveOverlayLowerDir maps (captureHash, volume, tier) to a host
// path. Today only the Local backend (Tier-1) layout is implemented:
// `<root>/<hash>/tree/<subpath>` where <subpath> is whatever
// checkpointstore.VolumeSubpath(vol) returns — `rootfs/<extractPath>`
// for rootfs-extract subpaths, `volumes/<name>` for hostPath/emptyDir
// user-data volumes.
//
// Tier-2 (rox PVC) resolution is intentionally deferred until the L2
// promote → host-bind path stabilizes; the webhook's in-process path
// passes LowerDirHint directly, so the HTTP route doesn't gate that
// flow.
//
// Root resolution: prefer captureBackend.Root() when it's the Local
// backend; otherwise fall back to the agent's configured rootfs cache
// dir (the --rootfs-cache-dir flag, same path the rootfsonly watcher
// writes to). The fallback matters when L2 is wired — captureBackend
// is then the L2 PerCapturePVCBackend and lacks Root(), but rootfs
// captures still land on local disk under RootfsCapture.CacheDir.
func (a *Agent) resolveOverlayLowerDir(_ context.Context, req PrepareOverlayRequest) (string, error) {
	if req.LowerDirHint != "" {
		return req.LowerDirHint, nil
	}
	if req.CaptureHash == "" {
		return "", errors.New("either lowerDir or captureHash is required")
	}
	subdir := checkpointstore.VolumeSubpath(req.Volume)
	if subdir == "" {
		return "", fmt.Errorf("cannot derive lower subpath for volume %+v", req.Volume)
	}
	root := a.config.RootfsCapture.CacheDir
	if local, ok := a.captureBackend.(interface{ Root() string }); ok {
		root = local.Root()
	}
	if root == "" {
		return "", errors.New("no cache root configured (captureBackend has no Root() and RootfsCapture.CacheDir is empty)")
	}
	return filepath.Join(root, req.CaptureHash, "tree", subdir), nil
}

// PrepareOverlay is the in-process entry point the webhook calls during
// admission for ANY captured volume (rootfs-extract subpath OR a
// hostPath/emptyDir user-data volume captured into tree/volumes/<name>).
// When targetNode is set and matches a node other than self, the call
// is routed via peer HTTP to the agent on that node — the overlay MUST
// live on the node where kubelet will eventually bind it, which is the
// node the pod is nodeAffinity-pinned to (typically the one that has
// the captured tree locally).
//
// Webhook path matters: the bind-mount kubelet eventually makes into
// the restored pod is a plain bind, not rbind. The overlay must be
// mounted on the host BEFORE kubelet binds; calling this in-process
// from Mutate() (which blocks pod admission) is what gives us that
// ordering for free.
func (a *Agent) PrepareOverlay(podUID, captureHash string, vol checkpointstore.VolumeMeta, targetNode string) (string, error) {
	ctx, span := tracing.Tracer().Start(context.Background(), "restore.prepare_overlay")
	defer span.End()
	span.SetAttributes(
		attribute.String("nvsnap.hash", checkpointstore.ShortHash(captureHash)),
		attribute.String("nvsnap.volume", vol.Name),
		attribute.String("nvsnap.target_node", targetNode),
	)
	if a.overlay == nil {
		return "", errors.New("overlay manager not initialized")
	}

	// v0.0.88 instrumentation: which branch does the restore overlay-prep
	// actually take? (prewarm only fires on the local branch below.)
	a.log.WithFields(logrus.Fields{
		"subsys": "prewarm-trace", "hash": checkpointstore.ShortHash(captureHash),
		"targetNode": targetNode, "selfNode": a.config.NodeName,
		"branch": map[bool]string{true: "via_peer", false: "local-inline"}[targetNode != "" && targetNode != a.config.NodeName],
	}).Info("PrepareOverlay called")
	if targetNode != "" && targetNode != a.config.NodeName {
		span.SetAttributes(attribute.Bool("nvsnap.via_peer", true))
		return a.prepareOverlayViaPeer(targetNode, podUID, captureHash, vol)
	}

	lower, err := a.resolveOverlayLowerDir(ctx, PrepareOverlayRequest{
		CaptureHash: captureHash,
		Volume:      vol,
	})
	if err != nil {
		return "", err
	}
	// Kick off page-cache prewarm of the rox-backed tree (async,
	// idempotent per lowerdir). The rox is mounted once per node and
	// every fan-out pod's overlay uses a lowerdir into it, so one warm
	// accelerates the engine's mmap weight-load for all of them. Never
	// blocks admission. See prewarm.go.
	a.prewarmLowerDirAsync(lower)
	return a.overlay.Prepare(podUID, lower, vol.MountPath)
}

// prepareOverlayViaPeer POSTs to the target node's agent at
// /v1/restore/overlay, asking it to mount the overlay locally and
// return the resulting mountpoint. The mountpoint is a host path on
// the target node — kubelet on THAT node binds it into the pod.
func (a *Agent) prepareOverlayViaPeer(targetNode, podUID, captureHash string, vol checkpointstore.VolumeMeta) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	peerURL, err := a.peerAgentURL(ctx, targetNode)
	if err != nil {
		return "", fmt.Errorf("resolve peer URL for node %q: %w", targetNode, err)
	}

	body, err := json.Marshal(PrepareOverlayRequest{
		PodUID:      podUID,
		CaptureHash: captureHash,
		Volume:      vol,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		peerURL+"/v1/restore/overlay", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST %s: %w", peerURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("peer agent %s: %s: %s", peerURL, resp.Status, b)
	}
	var out PrepareOverlayResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode peer response: %w", err)
	}
	a.log.WithFields(logrus.Fields{
		"targetNode": targetNode,
		"volumeType": vol.Type,
		"mountPath":  vol.MountPath,
		"mountpoint": out.Mountpoint,
	}).Info("nvsnap#88: overlay prepared on peer node")
	return out.Mountpoint, nil
}

// MountpointFor is the pure-function counterpart to PrepareOverlay
// used by the webhook's init-container strategy (nvsnap#202): the
// webhook patches the pod spec with a hostPath volume pointing at
// the merged path, and the nvsnap-mount-prep init container drives
// the actual mount asynchronously. No mount work happens here.
func (a *Agent) MountpointFor(podUID string, vol checkpointstore.VolumeMeta) (string, error) {
	if a.overlay == nil {
		return "", errors.New("overlay manager not initialized")
	}
	return a.overlay.MountpointFor(podUID, vol.MountPath)
}

// CleanupOverlayForPod is the in-process cleanup entry point used by
// the pod-delete watcher.
func (a *Agent) CleanupOverlayForPod(podUID string) error {
	if a.overlay == nil {
		return nil
	}
	return a.overlay.Cleanup(podUID)
}

// Compile-time guard against silently dropping checkpointstore use in
// future refactors (the Local backend's tree layout is what the
// resolver mirrors).
var _ = checkpointstore.ErrNotFound

func respondJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
