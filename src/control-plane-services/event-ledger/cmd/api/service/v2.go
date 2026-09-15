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
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	api_error "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/cmd/api/error"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/utils"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

// --- V2 Methods ---

// PostDeploymentStageTransitionEvent handles POST requests for v2 deployment-based events.
func (s *Server) PostDeploymentStageTransitionEvent(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "POST Deployment Stage Transition Event Error"

	vars := mux.Vars(r)
	deploymentId, err := uuid.Parse(vars["deploymentId"])
	if err != nil {
		title := "'deploymentId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	var dste types.DeploymentStageTransitionEvent // Use renamed type
	err = json.NewDecoder(r.Body).Decode(&dste)
	if err != nil {
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, "server failed to unmarshal message", r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Overwrite deploymentId from path parameter if present in body but different
	if dste.DeploymentId != uuid.Nil && dste.DeploymentId != deploymentId {
		logger.WarnContext(traceCtx, "deployment id in body differs from path parameter, using path parameter",
			zap.String("bodyDeploymentId", dste.DeploymentId.String()),
			zap.String("pathDeploymentId", deploymentId.String()))
	}
	dste.DeploymentId = deploymentId // Ensure deploymentId from path is used

	// Basic validation using utils.HasZeroValues
	if utils.HasZeroValues(dste) {
		title := "event has missing required values"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, errors.New(title), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	if !middleware.IsTenantAuthorized(traceCtx, dste.NcaId) {
		status := http.StatusForbidden
		err := errors.New("tenant is not authorized")
		api_error.GenerateErrorResponse(traceCtx, errType, "Forbidden", r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	if _, _, _, err = codex.ParseInstanceId(dste.InstanceId); err != nil {
		title := "failed to parse instance id"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, errors.New(title), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionId", dste.FunctionId.String()))
	parentSpan.SetAttributes(attribute.String("functionVersionId", dste.FunctionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("deploymentId", dste.DeploymentId.String()))
	parentSpan.SetAttributes(attribute.String("instanceId", dste.InstanceId))
	parentSpan.SetAttributes(attribute.String("event", dste.Event))

	if _, ok := constants.ValidEvents[dste.Event]; !ok {
		title := "event is not valid"
		err = fmt.Errorf("event '%s' is not a valid event", dste.Event)
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	var detailsMap map[string]interface{}
	if err = json.Unmarshal([]byte(dste.Details), &detailsMap); err != nil {
		title := "'details' field is not valid json"
		err = errors.New(title)
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Use V2 handler
	if s.conns.DbHandlerV2 == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V2 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	err = s.WriteDeploymentStageTransitionEvent(traceCtx, dste)
	if err != nil {
		var title string
		var status int
		if err.Error() == "missing instance type details" {
			title = "instance type missing from event details"
			status = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "DUPLICATE EVENT") {
			title = "duplicate event posted"
			status = http.StatusConflict
		} else {
			title = err.Error()
			status = http.StatusInternalServerError
		}
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	s.eventPub.PublishV2(traceCtx, dste)

	status := http.StatusAccepted
	w.WriteHeader(status)
	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

// ListDeploymentStageTransitionEvents handles GET requests for v2 deployment-based event listings.
// Returns []types.StageTransitionEvent or potentially []types.DeployedStageTransitionEvent depending on DB layer.
// Assuming DB layer returns the base type or a compatible structure for now.
func (s *Server) ListDeploymentStageTransitionEvents(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "LIST Deployment Stage Transition Events Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	deploymentId, err := uuid.Parse(vars["deploymentId"])
	if err != nil {
		title := "'deploymentId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	instanceId := vars["instanceId"]
	if _, _, _, err = codex.ParseInstanceId(instanceId); err != nil {
		title := "failed to parse instance id"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("deploymentId", deploymentId.String()))
	parentSpan.SetAttributes(attribute.String("instanceId", instanceId))

	// Use V2 handler
	if s.conns.DbHandlerV2 == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V2 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	events, err := s.conns.DbHandlerV2.ListDeploymentStageTransitionEvents(traceCtx, functionVersionId, deploymentId, instanceId)
	if err != nil {
		title := "failed to list stage transition event"
		var status int
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else {
			status = http.StatusInternalServerError
		}
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	w.Header().Set("content-type", "application/json")
	status := http.StatusOK
	w.WriteHeader(status)
	err = json.NewEncoder(w).Encode(events)
	if err != nil {
		status = http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("failed to encode json"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

// ListDeploymentInstances handles GET requests for v2 deployment-based instance listings.
func (s *Server) ListDeploymentInstances(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "LIST Deployment Instances Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	deploymentId, err := uuid.Parse(vars["deploymentId"])
	if err != nil {
		title := "'deploymentId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("deploymentId", deploymentId.String()))

	eventFilter, err := processFilters(r.URL) // Assumed from v1 or shared
	if err != nil {
		title := "'eventFilter' field is not valid filter"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Parse pagination parameters
	paginationParams, err := parsePaginationParams(r.URL.Query(), s.paginationConfig)
	if err != nil {
		title := "Invalid pagination parameters"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Use V2 handler
	if s.conns.DbHandlerV2 == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V2 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Check if pagination parameters are provided or if we need to use paginated response
	if paginationParams.Limit > 0 || paginationParams.PageToken != "" {
		// Apply default limit if only pageToken is provided
		paginationParams = paginationParams.ApplyDefaultLimit(s.paginationConfig.DefaultPageSize)

		// Use paginated method
		paginatedResponse, err := s.conns.DbHandlerV2.ListDeploymentInstancesPaginated(traceCtx, functionVersionId, deploymentId, paginationParams)
		if err != nil {
			var status int
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			} else {
				status = http.StatusInternalServerError
			}
			api_error.GenerateErrorResponse(traceCtx, errType, err.Error(), r.URL.Path, status, err, w)
			logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
			return
		}

		// Apply event filter to paginated results
		paginatedResponse.Instances = filterInstancesByEvent(paginatedResponse.Instances, eventFilter, logger, traceCtx)
		paginatedResponse.Pagination.PageSize = len(paginatedResponse.Instances)

		w.Header().Set("content-type", "application/json")
		status := http.StatusOK
		w.WriteHeader(status)
		err = json.NewEncoder(w).Encode(paginatedResponse)
		if err != nil {
			status = http.StatusInternalServerError
			api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("failed to encode json"), w)
			logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
			return
		}
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Use original method for backward compatibility
	instances, err := s.conns.DbHandlerV2.ListDeploymentInstances(traceCtx, functionVersionId, deploymentId)
	if err != nil {
		var status int
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else {
			status = http.StatusInternalServerError
		}
		api_error.GenerateErrorResponse(traceCtx, errType, err.Error(), r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	instances = filterInstancesByEvent(instances, eventFilter, logger, traceCtx)

	w.Header().Set("content-type", "application/json")
	status := http.StatusOK
	w.WriteHeader(status)
	err = json.NewEncoder(w).Encode(instances)

	if err != nil {
		status = http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("failed to encode json"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

// ArchiveDeploymentInstanceStageTransitionEvents handles DELETE requests for v2 instance events.
func (s *Server) ArchiveDeploymentInstanceStageTransitionEvents(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Archive Deployment Instance Stage Transition Events Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	deploymentId, err := uuid.Parse(vars["deploymentId"])
	if err != nil {
		title := "'deploymentId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	instanceId := vars["instanceId"]
	if _, _, _, err = codex.ParseInstanceId(instanceId); err != nil {
		title := "'instanceId' is not valid"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("deploymentId", deploymentId.String()))
	parentSpan.SetAttributes(attribute.String("instanceId", instanceId))

	// Use V2 handler
	if s.conns.DbHandlerV2 == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V2 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	err = s.conns.DbHandlerV2.ArchiveDeploymentInstanceStageTransitionEvents(traceCtx, functionVersionId, deploymentId, instanceId)
	if err != nil {
		var title string
		var status int
		if strings.Contains(err.Error(), "not found") {
			title = "events not found"
			status = http.StatusNotFound
		} else {
			title = "error archiving instance"
			status = http.StatusInternalServerError
		}
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	status := http.StatusOK
	w.WriteHeader(status)
	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

// ArchiveDeploymentFunctionVersionStageTransitionEvents handles DELETE requests for all v2 events under a deployment.
func (s *Server) ArchiveDeploymentFunctionVersionStageTransitionEvents(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Archive Deployment Function Version Stage Transition Events Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	deploymentId, err := uuid.Parse(vars["deploymentId"])
	if err != nil {
		title := "'deploymentId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("deploymentId", deploymentId.String()))

	// Use V2 handler
	if s.conns.DbHandlerV2 == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V2 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	instances, err := s.conns.DbHandlerV2.ListDeploymentInstances(traceCtx, functionVersionId, deploymentId)
	if err != nil {
		var status int
		var title string
		// If no instances found, that's okay for deletion, return success.
		if strings.Contains(err.Error(), "not found") {
			logger.InfoContext(traceCtx, "no instances found to archive for deployment",
				zap.String("functionVersionId", functionVersionId.String()),
				zap.String("deploymentId", deploymentId.String()))
			w.WriteHeader(http.StatusOK)
			return
		} else {
			status = http.StatusInternalServerError
			title = "failed to retrieve instances"
		}
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Archive events for each instance
	archiveErrors := make([]string, 0)
	for _, instance := range instances {
		err = s.conns.DbHandlerV2.ArchiveDeploymentInstanceStageTransitionEvents(traceCtx, functionVersionId, deploymentId, instance.InstanceId)
		if err != nil {
			errMsg := fmt.Sprintf("failed to archive events for instance %s: %v", instance.InstanceId, err)
			logger.ErrorContext(traceCtx, errMsg)
			archiveErrors = append(archiveErrors, errMsg)
		}
	}

	if len(archiveErrors) > 0 {
		title := "failed to archive one or more instance events"
		err = errors.New(strings.Join(archiveErrors, "; "))
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	status := http.StatusOK
	w.WriteHeader(status)
	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

// GetDeploymentDeploymentStats handles GET requests for v2 deployment statistics.
func (s *Server) GetDeploymentDeploymentStats(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Get Deployment Deployment Stats Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	deploymentId, err := uuid.Parse(vars["deploymentId"])
	if err != nil {
		title := "'deploymentId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("deploymentId", deploymentId.String()))

	// Use V2 handler
	if s.conns.DbHandlerV2 == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V2 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	stats, err := s.conns.DbHandlerV2.ReadDeploymentDeploymentStats(traceCtx, functionVersionId, deploymentId)
	if err != nil {
		var status int
		var title string
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
			title = "deployment stats not found"
		} else {
			status = http.StatusInternalServerError
			title = "failed to retrieve deployment stats"
		}
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	w.Header().Set("content-type", "application/json")
	status := http.StatusOK
	w.WriteHeader(status)
	err = json.NewEncoder(w).Encode(stats)
	if err != nil {
		status = http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("failed to encode json"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

// GetDeploymentInstanceEvent handles GET requests for a single v2 deployment event.
// Returns types.StageTransitionEvent or potentially types.DeployedStageTransitionEvent depending on DB layer.
// Assuming DB layer returns the base type or a compatible structure for now.
func (s *Server) GetDeploymentInstanceEvent(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Get Deployment Instance Event Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	deploymentId, err := uuid.Parse(vars["deploymentId"])
	if err != nil {
		title := "'deploymentId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	instanceId := vars["instanceId"]
	if _, _, _, err = codex.ParseInstanceId(instanceId); err != nil {
		title := "'instanceId' field is not valid"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	eventName := vars["event"]
	event, err := codex.NewEvent(eventName)
	if err != nil {
		title := "'event' field is invalid"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("deploymentId", deploymentId.String()))
	parentSpan.SetAttributes(attribute.String("instanceId", instanceId))
	parentSpan.SetAttributes(attribute.String("event", event.Name))

	// Use V2 handler
	if s.conns.DbHandlerV2 == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V2 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	eventRecord, err := s.conns.DbHandlerV2.GetDeploymentInstanceEvent(traceCtx, functionVersionId, deploymentId, instanceId, event)
	if err != nil {
		var status int
		var title string
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
			title = "event not found"
		} else {
			status = http.StatusInternalServerError
			title = "failed to retrieve event"
		}
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	w.Header().Set("content-type", "application/json")
	status := http.StatusOK
	w.WriteHeader(status)
	err = json.NewEncoder(w).Encode(eventRecord)
	if err != nil {
		status = http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("failed to encode json"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

// NOTE: Shared utility functions (processFilters, parsePaginationParams, filterInstancesByEvent)
// are now available in common.go within this package.
