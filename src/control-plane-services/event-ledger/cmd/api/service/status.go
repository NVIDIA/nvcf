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

package service

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
)

// statusResponse holds data for the overall status response
type statusResponse struct {
	Status    string                 `json:"status"`
	Database  map[string]statusCheck `json:"database"`
	Auth      map[string]statusCheck `json:"auth"`
	Version   string                 `json:"version"`
	Timestamp string                 `json:"timestamp"`
}

// statusCheck holds data for each dependency's status
type statusCheck struct {
	Status  string `json:"status"`
	Details string `json:"details"`
}

// Status checks the availability of the service's dependencies
func (s *Server) Status(w http.ResponseWriter, r *http.Request) {
	s.status(w, r, config.AuthConfig{})
}

func (s *Server) StatusHandler(authConfig config.AuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.status(w, r, authConfig)
	})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request, authConfig config.AuthConfig) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)

	response := statusResponse{
		Database:  make(map[string]statusCheck),
		Auth:      make(map[string]statusCheck),
		Version:   s.version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	allHealthy := true

	databaseResults := s.conns.DbHandler.CheckStatus(traceCtx)

	for name, err := range databaseResults {
		if err != nil {
			response.Database[name] = statusCheck{
				Status:  "unavailable",
				Details: "dependency check failed",
			}
			allHealthy = false
		} else {
			response.Database[name] = statusCheck{
				Status: "available",
			}
		}
	}

	authResults := middleware.CheckStatus(traceCtx, logger, s.httpClientConfig, authConfig)

	for name, err := range authResults {
		if err != nil {
			response.Auth[name] = statusCheck{
				Status:  "unavailable",
				Details: "dependency check failed",
			}
			allHealthy = false
		} else {
			response.Auth[name] = statusCheck{
				Status: "available",
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if allHealthy {
		response.Status = "healthy"
		w.WriteHeader(http.StatusOK)
	} else {
		response.Status = "degraded"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}
