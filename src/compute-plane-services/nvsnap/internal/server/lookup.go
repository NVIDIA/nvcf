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
// nvsnap#59: content-addressed checkpoint lookup endpoint.

package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// lookupCheckpointRequest is the JSON body for POST /lookup. Callers
// describe the workload they're about to run (image + model + flags +
// driver) and ask if any catalog row matches. Used by NVCA's Hook A
// to dedup checkpoints across function-version-IDs.
//
// imageRef is the only required field — without it the query would
// scan the entire catalog. Other fields narrow the match; an empty
// modelID or driverMajor means "match anything for that field". An
// empty engineFlags slice means "only match rows that also have no
// flags" (because we don't want to accidentally restore from a flag
// superset that the requesting workload isn't compatible with).
type lookupCheckpointRequest struct {
	ImageRef    string   `json:"imageRef"`
	ModelID     string   `json:"modelId,omitempty"`
	EngineFlags []string `json:"engineFlags,omitempty"`
	DriverMajor int      `json:"driverMajor,omitempty"`
	Limit       int      `json:"limit,omitempty"` // 0 = default 10, max 100
}

// lookupCheckpointMatch is one row of the lookup response. Just the
// fields NVCA actually needs to decide "restore from this" — the rest
// of CatalogInfo is accessible via GET /api/v1/checkpoints/{id}.
type lookupCheckpointMatch struct {
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

type lookupCheckpointResponse struct {
	Matches []lookupCheckpointMatch `json:"matches"`
}

// lookupCheckpoint serves POST /api/v1/checkpoints/lookup. Returns
// matching catalog rows sorted by capturedAt descending (freshest
// first). Empty response is 200 with matches=[] — a 404 would be
// wrong (the query succeeded, there just isn't a hit).
func (s *Server) lookupCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req lookupCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.ImageRef == "" {
		s.writeError(w, http.StatusBadRequest, "imageRef is required")
		return
	}

	rows, err := s.catalog.LookupCheckpoints(db.LookupCriteria{
		ImageRef:    req.ImageRef,
		ModelID:     req.ModelID,
		EngineFlags: req.EngineFlags,
		DriverMajor: req.DriverMajor,
		Limit:       req.Limit,
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "lookup failed: "+err.Error())
		return
	}

	resp := lookupCheckpointResponse{
		Matches: make([]lookupCheckpointMatch, 0, len(rows)),
	}
	for i := range rows {
		row := &rows[i]
		// CapturedAt is the time the checkpoint completed — use
		// CompletedAt when present, fall back to CreatedAt.
		capturedAt := row.CreatedAt
		if row.CompletedAt != nil {
			capturedAt = *row.CompletedAt
		}
		resp.Matches = append(resp.Matches, lookupCheckpointMatch{
			Hash:           row.Hash,
			CheckpointID:   row.ID,
			CapturedAt:     capturedAt,
			CapturedOnNode: row.NodeName,
			ImageRef:       row.ImageRef,
			ImageDigest:    row.ImageDigest,
			ModelID:        row.ModelID,
			GPUType:        row.GPUType,
			DriverVersion:  row.DriverVersion,
		})
	}
	s.writeJSON(w, http.StatusOK, resp)
}
