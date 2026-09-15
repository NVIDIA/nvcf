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

package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// Phase 5d.1 catalog-routing handlers.
//
// Restore-side cascading fetch reads /api/v1/checkpoints/{id}/sources
// to learn which agents have a local copy + (in 5d.2) the
// nvsnap-blobstore URI as a final fallback. Agents POST peer-add /
// peer-remove as their local cache state changes.

// peerRegisterRequest is the JSON body for /peer-add and /peer-remove.
//
// Wire format kept tiny on purpose — these are hot-path catalog
// updates that fire on every successful download / LRU eviction; we
// don't want agents pushing a lot of metadata they could re-derive.
type peerRegisterRequest struct {
	NodeName string `json:"node_name"`
	AgentURL string `json:"agent_url,omitempty"` // omit on peer-remove
}

// sourcesResponse is what the restore-side cascade reads. Peers are
// pre-sorted most-recently-seen first so the receiver can iterate
// without re-sorting.
type sourcesResponse struct {
	CheckpointID string             `json:"checkpoint_id"`
	Peers        []sourcesPeerEntry `json:"peers"`
	BlobURI      string             `json:"blob_uri,omitempty"` // empty until stage 5d.2 wires the uploader
}

type sourcesPeerEntry struct {
	NodeName     string `json:"node_name"`
	AgentURL     string `json:"agent_url"`
	RegisteredAt string `json:"registered_at,omitempty"`
	LastSeen     string `json:"last_seen,omitempty"`
}

// getCheckpointSources is the load-bearing query the restore-side
// cascade reads on every fanout. Returns the prioritized peer list +
// blob fallback URI in one round-trip.
//
// Status codes:
//   - 200: response valid (peers may be empty if no agent has
//     registered yet; receiver should fall back to blob_uri)
//   - 404: checkpoint id unknown to the catalog
//   - 500: db error
func (s *Server) getCheckpointSources(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	// Confirm the checkpoint exists at all — distinguishes "no peers
	// yet" (200 with empty list) from "wrong id" (404).
	if _, err := s.catalog.GetCheckpoint(id); err != nil {
		http.Error(w, fmt.Sprintf("checkpoint not found: %v", err), http.StatusNotFound)
		return
	}

	peers, blobURI, err := s.catalog.GetCheckpointSources(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("query sources: %v", err), http.StatusInternalServerError)
		return
	}

	out := sourcesResponse{
		CheckpointID: id,
		Peers:        make([]sourcesPeerEntry, 0, len(peers)),
		BlobURI:      blobURI,
	}
	for _, p := range peers {
		out.Peers = append(out.Peers, sourcesPeerEntry{
			NodeName:     p.NodeName,
			AgentURL:     p.AgentURL,
			RegisteredAt: p.RegisteredAt.Format("2006-01-02T15:04:05Z"),
			LastSeen:     p.LastSeen.Format("2006-01-02T15:04:05Z"),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// peerAddCheckpoint is called by an agent when it has a local copy
// of a checkpoint that wasn't tracked yet — either:
//
//   - Source-of-capture node, immediately after the checkpoint
//     completes locally (idempotent re-registration is fine).
//   - Restore-target node, after a successful cascading-fetch download
//     lands the dump on its local disk (the receiver becomes a new
//     peer for future fanouts).
func (s *Server) peerAddCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var req peerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.NodeName == "" || req.AgentURL == "" {
		http.Error(w, "node_name and agent_url required", http.StatusBadRequest)
		return
	}

	// Verify the checkpoint exists — catalog integrity. If a peer
	// claims to have a checkpoint we don't know about, the agent's
	// local cache is out of sync (probably a stale entry).
	if _, err := s.catalog.GetCheckpoint(id); err != nil {
		http.Error(w, fmt.Sprintf("checkpoint not found: %v", err), http.StatusNotFound)
		return
	}

	if err := s.catalog.AddPeer(id, req.NodeName, req.AgentURL); err != nil {
		http.Error(w, fmt.Sprintf("add peer: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// peerRemoveCheckpoint is called when an agent evicts a checkpoint
// from its local cache (LRU pressure, manual delete, etc). The
// catalog removes the row so future restores don't try to fetch from
// a node that no longer has the bytes.
//
// Idempotent — removing an already-absent peer returns 204.
func (s *Server) peerRemoveCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var req peerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.NodeName == "" {
		http.Error(w, "node_name required", http.StatusBadRequest)
		return
	}

	if err := s.catalog.RemovePeer(id, req.NodeName); err != nil {
		http.Error(w, fmt.Sprintf("remove peer: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// blobUploadedRequest is the JSON body for /blob-uploaded. The agent
// reports the nvsnap-blobstore base URL it uploaded to so the cascade
// can construct manifest + per-blob URLs from the same prefix.
type blobUploadedRequest struct {
	BlobURI string `json:"blob_uri"`
}

// registerCheckpointRequest is the JSON body for /register. Agents
// directly initiating a checkpoint (without going through nvsnap-server's
// API) call this after CRIU completes so the catalog has a row to
// hang peer-add / blob-uploaded / sources routing off of.
//
// Catalog fields (Hash, ImageRef, ModelID, EngineFlags, GPUType,
// DriverVersion, CUDAVersion, CPUArchitecture, FunctionName,
// FunctionVersionID) are populated from the agent's on-disk
// metadata.json. They back the content-addressed lookup endpoint
// (nvsnap#59) so NVCA can dedup checkpoints across fvIDs without
// fan-out scraping every agent's HTTP API.
//
// All catalog fields are optional — older agents that don't send
// them get a partial row (lookup won't match them, GET still works).
type registerCheckpointRequest struct {
	CheckpointID   string  `json:"checkpoint_id"`
	Namespace      string  `json:"namespace"`
	PodName        string  `json:"pod_name"`
	ContainerName  string  `json:"container_name,omitempty"`
	ContainerImage string  `json:"container_image,omitempty"`
	NodeName       string  `json:"node_name"`
	CheckpointPath string  `json:"checkpoint_path,omitempty"`
	CheckpointSize int64   `json:"checkpoint_size,omitempty"`
	Status         string  `json:"status,omitempty"` // default "Completed"
	HasGPU         bool    `json:"has_gpu,omitempty"`
	DurationSecs   float64 `json:"duration_secs,omitempty"`

	// CatalogInfo fields (nvsnap#59) — all optional.
	Hash              string   `json:"hash,omitempty"`
	ImageRef          string   `json:"image_ref,omitempty"`
	ImageDigest       string   `json:"image_digest,omitempty"`
	ModelID           string   `json:"model_id,omitempty"`
	EngineFlags       []string `json:"engine_flags,omitempty"`
	GPUType           string   `json:"gpu_type,omitempty"`
	GPUCount          int      `json:"gpu_count,omitempty"`
	DriverVersion     string   `json:"driver_version,omitempty"`
	CUDAVersion       string   `json:"cuda_version,omitempty"`
	CPUArchitecture   string   `json:"cpu_architecture,omitempty"`
	FunctionName      string   `json:"function_name,omitempty"`
	FunctionVersionID string   `json:"function_version_id,omitempty"`
}

// registerCheckpoint upserts a catalog row keyed by checkpoint_id. The
// agent calls this immediately after a successful CRIU dump (before
// kicking off peer-add or the blob-store upload) so every later catalog
// operation has the row to anchor on.
//
// Idempotent: re-registration is fine and is the expected behavior when
// the agent restarts mid-upload and retries.
//
// 204 on success, 400 on missing required fields.
func (s *Server) registerCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req registerCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.CheckpointID == "" || req.Namespace == "" || req.PodName == "" || req.NodeName == "" {
		http.Error(w, "checkpoint_id, namespace, pod_name, node_name required", http.StatusBadRequest)
		return
	}
	status := req.Status
	if status == "" {
		status = "Completed"
	}
	now := time.Now().UTC()
	cp := &db.Checkpoint{
		ID:            req.CheckpointID,
		CheckpointID:  req.CheckpointID,
		Namespace:     req.Namespace,
		PodName:       req.PodName,
		ContainerName: req.ContainerName,
		// CatalogInfo (nvsnap#59) — copied through verbatim; UpsertCheckpoint
		// derives DriverMajor from DriverVersion and canonicalizes EngineFlags
		// for the indexed lookup column.
		Hash:              req.Hash,
		ImageRef:          req.ImageRef,
		ImageDigest:       req.ImageDigest,
		ModelID:           req.ModelID,
		EngineFlags:       req.EngineFlags,
		GPUType:           req.GPUType,
		GPUCount:          req.GPUCount,
		DriverVersion:     req.DriverVersion,
		CUDAVersion:       req.CUDAVersion,
		CPUArchitecture:   req.CPUArchitecture,
		FunctionName:      req.FunctionName,
		FunctionVersionID: req.FunctionVersionID,
		ContainerImage:    req.ContainerImage,
		NodeName:          req.NodeName,
		CheckpointPath:    req.CheckpointPath,
		CheckpointSize:    req.CheckpointSize,
		Status:            status,
		HasGPU:            req.HasGPU,
		CreatedAt:         now,
		StartedAt:         &now,
		DurationSecs:      req.DurationSecs,
	}
	if status == "Completed" || status == "Failed" {
		cp.CompletedAt = &now
	}
	if err := s.catalog.UpsertCheckpoint(cp); err != nil {
		http.Error(w, fmt.Sprintf("upsert checkpoint: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// blobUploadedCheckpoint records a successful upload to the cluster's
// nvsnap-blobstore. Triggered by the agent's background uploader after
// every file in the dump dir is durably stored. Once this fires, the
// checkpoint survives loss of the source node — the catalog will
// route restores to the blob store as the tier-3 fallback.
//
// 204 on success, 400 on missing fields, 404 if the checkpoint id
// was deleted between capture and upload-callback (rare but possible
// under aggressive retention sweeps).
func (s *Server) blobUploadedCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var req blobUploadedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.BlobURI == "" {
		http.Error(w, "blob_uri required", http.StatusBadRequest)
		return
	}

	if _, err := s.catalog.GetCheckpoint(id); err != nil {
		http.Error(w, fmt.Sprintf("checkpoint not found: %v", err), http.StatusNotFound)
		return
	}

	if err := s.catalog.SetCheckpointBlobURI(id, req.BlobURI); err != nil {
		http.Error(w, fmt.Sprintf("set blob uri: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pvcPromoteStateResponse is the JSON body for GET /pvc-state. It's
// the contract the nvsnap-init init container (nvsnap#147) polls during
// restore-pod admission — emit a stable shape, never break it.
//
// state values: empty string when the catalog row exists but the
// agent's L2 backend never wrote any state (L2 disabled at agent
// startup, or agent died before the first state write). Otherwise
// one of pending|writing|snapshotting|ready|failed. nvsnap-init treats
// empty as "L2 not in use, fall back to peer cascade".
type pvcPromoteStateResponse struct {
	Hash    string `json:"hash"`
	State   string `json:"state"`              // pending|writing|snapshotting|ready|failed|"" (no-l2)
	PVCName string `json:"pvc_name,omitempty"` // rox-<short-hash> when state=ready
}

// getPVCPromoteStateByHash handles GET
// /api/v1/checkpoints/by-hash/{hash}/pvc-state. Read symmetric to the
// existing POST writer endpoint. nvsnap-init (nvsnap#147) polls this
// during restore-pod admission to gate the inference container on
// "rox PVC ready" before exec.
//
// 200 with the typed shape on success.
// 400 on empty / malformed hash.
// 404 when no catalog row carries the hash (caller should retry
// briefly — the agent's /register call may not have landed yet —
// then give up to allow cold-start fallback).
//
// Hash-keyed (not id-keyed) for the same reason the writer is:
// rox-<short-hash> is hash-keyed and multiple capture rows can share
// a hash (re-capture of the same workload). The freshest row wins
// (db layer enforces this).
func (s *Server) getPVCPromoteStateByHash(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	if hash == "" {
		http.Error(w, "hash required", http.StatusBadRequest)
		return
	}
	state, pvcName, err := s.catalog.GetPVCPromoteStateByHash(hash)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no checkpoint found for hash", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("get pvc_promote_state: %v", err), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, pvcPromoteStateResponse{
		Hash:    hash,
		State:   state,
		PVCName: pvcName,
	})
}

// updatePVCPromoteStateRequest is the JSON body for /pvc-state.
//
// The agent's L2 backend (internal/checkpointstore.PerCapturePVCBackend)
// POSTs one of these on every state transition. Empty pvc_name is
// honored by the catalog as "leave the prior value" (COALESCE) — only
// the final "ready" transition carries the rox-<hash> name.
type updatePVCPromoteStateRequest struct {
	State   string `json:"state"`              // pending|writing|snapshotting|ready|failed
	PVCName string `json:"pvc_name,omitempty"` // rox-<short-hash> on ready; empty otherwise
}

// updatePVCPromoteStateByHash handles POST
// /api/v1/checkpoints/by-hash/{hash}/pvc-state. Writes the L2
// state-machine columns on every catalog row that shares the given
// content hash. See docs/L2-PVC-CRIU-DESIGN.md §"pvc_promote_state
// state machine".
//
// Hash-keyed (not id-keyed) because the L2 artifact rox-<short-hash>
// is hash-keyed and multiple capture rows can share a hash
// (re-capture of the same workload). The producer side
// (PerCapturePVCBackend in internal/checkpointstore) only ever knows
// the content hash — it has no access to the per-capture
// "<short>__<ts>" id the agent assigns at dump time. See nvsnap#76.
//
// 204 on success, 400 on invalid state, 404 if no row has that hash
// (caller should ensure /register has run first).
func (s *Server) updatePVCPromoteStateByHash(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	if hash == "" {
		http.Error(w, "hash required", http.StatusBadRequest)
		return
	}
	var req updatePVCPromoteStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	switch req.State {
	case "pending", "writing", "snapshotting", "ready", "failed":
		// ok
	default:
		http.Error(w, fmt.Sprintf("invalid state %q (allowed: pending|writing|snapshotting|ready|failed)", req.State),
			http.StatusBadRequest)
		return
	}
	n, err := s.catalog.UpdatePVCPromoteStateByHash(hash, req.State, req.PVCName)
	if err != nil {
		http.Error(w, fmt.Sprintf("update pvc_promote_state: %v", err), http.StatusInternalServerError)
		return
	}
	if n == 0 {
		http.Error(w, "no checkpoint found for hash", http.StatusNotFound)
		return
	}
	_ = s.catalog.LogAudit(&db.AuditEntry{
		Action: "checkpoint.pvc_promote_state", Resource: "checkpoint_hash", ResourceID: hash,
		Actor:   "agent",
		Details: fmt.Sprintf(`{"state":%q,"pvc_name":%q,"rows":%d}`, req.State, req.PVCName, n),
	})
	w.WriteHeader(http.StatusNoContent)
}

// listPeersCheckpoint is a debugging endpoint — returns the raw peer
// rows for a checkpoint without the sources-routing decoration. Used
// by the agent's restore-side health-check sweep and the operator
// UI's checkpoint detail page.
func (s *Server) listPeersCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	peers, err := s.catalog.ListPeers(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("list peers: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"checkpoint_id": id,
		"peers":         peers,
	})
}

// Compile-time assertion: catalog must expose the peer methods. If
// the catalog interface ever drops these, this stops compiling.
var _ = func() {
	var c *db.DB
	_ = c.AddPeer
	_ = c.RemovePeer
	_ = c.ListPeers
	_ = c.GetCheckpointSources
}
