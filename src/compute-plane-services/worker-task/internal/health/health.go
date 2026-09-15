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

package health

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"go.uber.org/zap"
)

// ------------------------------------------------------------------------

// The health server only exposes health of utils container
// for liveness and readiness probe, as there's no health
// endpoint on task container by design.
type HealthHandler struct {
	rwlock sync.RWMutex
	status pb.ExecutionStatus
}

// ------------------------------------------------------------------------

// Constructor.
func New() *HealthHandler {
	return &HealthHandler{
		status: pb.ExecutionStatus_TASK_CONTAINER_INITIALIZING,
	}
}

// ------------------------------------------------------------------------

func (h *HealthHandler) SetExecutionStatus(status pb.ExecutionStatus) {
	h.rwlock.Lock()
	h.status = status
	h.rwlock.Unlock()
}

// ------------------------------------------------------------------------

func (h *HealthHandler) GetExecutionStatus() pb.ExecutionStatus {
	h.rwlock.RLock()
	defer h.rwlock.RUnlock()
	return h.status
}

// ------------------------------------------------------------------------

// The utils container should be ready when the task is in the RUNNING state.
func (h *HealthHandler) GetReadinessStatus() bool {
	h.rwlock.RLock()
	defer h.rwlock.RUnlock()
	return h.status == pb.ExecutionStatus_RUNNING
}

// ------------------------------------------------------------------------

// Serve health check request
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.GetReadinessStatus() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ------------------------------------------------------------------------

func (h *HealthHandler) StartHeartbeat(ctx context.Context, nvctClient *nvct.Client) {
	for {
		if ctx.Err() != nil {
			zap.L().Info("heartbeat cancelled")
			return
		}

		status := h.GetExecutionStatus()
		if _, err := nvctClient.SendInProgressHeartbeat(ctx, status); err != nil {
			zap.L().Error("failed to send heartbeat", zap.Error(err))
		}

		utils.SleepWithContext(ctx, time.Minute)
	}
}
