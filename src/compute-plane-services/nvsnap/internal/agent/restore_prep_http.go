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

// restore_prep_http.go — HTTP handlers for the nvsnap-mount-prep init
// container. See docs/proposals/init-container-mount-prep.md.

package agent

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
)

// startRestorePrepHandler handles POST /v1/restore/prep.
//
// Body: PrepRequest. Behavior: idempotent — re-POST for the same
// podUID returns the existing job's status, never starts a second
// goroutine. Init container POSTs every restart (and may retry on
// transient network errors), so idempotency is load-bearing.
//
// Returns 202 Accepted with the (current) PrepStatus on success.
// 400 if the request body is malformed. The actual prep work runs
// in a background goroutine; status polling lives on the GET handler.
func (a *Agent) startRestorePrepHandler(w http.ResponseWriter, r *http.Request) {
	var req PrepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	if req.PodUID == "" || req.CaptureHash == "" {
		http.Error(w, "podUID and captureHash are required", http.StatusBadRequest)
		return
	}
	if a.prepJobs == nil {
		// Agent constructed without a prep manager (test path or
		// a deployment that left it disabled). Return 503 so the
		// init container backs off rather than tight-looping.
		http.Error(w, "restore-prep manager not initialized", http.StatusServiceUnavailable)
		return
	}
	status := a.prepJobs.Start(r.Context(), req)
	respondJSON(w, http.StatusAccepted, status)
}

// statusRestorePrepHandler handles GET /v1/restore/prep/{pod-uid}.
//
// Returns:
//   - 200 + PrepStatus when the job is tracked
//   - 404 when the manager has no record — init container should
//     treat this as "POST hasn't landed on this agent yet, retry"
//     rather than as a hard failure
//
// Init container polls this every ~250 ms. Handler is cheap (one
// sync.Map load + struct snapshot under a brief per-job mutex).
func (a *Agent) statusRestorePrepHandler(w http.ResponseWriter, r *http.Request) {
	podUID := mux.Vars(r)["pod-uid"]
	if podUID == "" {
		http.Error(w, "pod-uid required", http.StatusBadRequest)
		return
	}
	if a.prepJobs == nil {
		http.Error(w, "restore-prep manager not initialized", http.StatusServiceUnavailable)
		return
	}
	status, ok := a.prepJobs.Status(podUID)
	if !ok {
		http.Error(w, "no prep job for pod-uid", http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, status)
}

// cleanupRestorePrepHandler handles DELETE /v1/restore/prep/{pod-uid}.
//
// Called by the existing overlay-cleanup pod-DELETE watcher. Idempotent:
// missing pod-uid is a no-op so multiple cleanup paths can fire safely.
func (a *Agent) cleanupRestorePrepHandler(w http.ResponseWriter, r *http.Request) {
	podUID := mux.Vars(r)["pod-uid"]
	if podUID == "" {
		http.Error(w, "pod-uid required", http.StatusBadRequest)
		return
	}
	if a.prepJobs == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.prepJobs.Cleanup(podUID); err != nil {
		a.log.WithError(err).WithField("podUID", podUID).Warn("cleanup prep")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
