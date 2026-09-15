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

	api_error "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/cmd/api/error"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/utils"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

func (s *Server) PostStageTransitionEvent(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "POST Stage Transition Event Error"

	var ste types.StageTransitionEvent
	err := json.NewDecoder(r.Body).Decode(&ste)
	if err != nil {
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, "server failed to unmarshal message", r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	if utils.HasZeroValues(ste) {
		title := "event has empty values"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, errors.New(title), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	if !middleware.IsTenantAuthorized(traceCtx, ste.NcaId) {
		status := http.StatusForbidden
		err := errors.New("tenant is not authorized")
		api_error.GenerateErrorResponse(traceCtx, errType, "Forbidden", r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	if _, _, _, err = codex.ParseInstanceId(ste.InstanceId); err != nil {
		title := "failed to parse instance id"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, errors.New(title), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionId", ste.FunctionId.String()))
	parentSpan.SetAttributes(attribute.String("functionVersionId", ste.FunctionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("instanceId", ste.InstanceId))

	if _, ok := constants.ValidEvents[ste.Event]; !ok {
		title := "event is not valid"
		err = fmt.Errorf("event '%s' is not a valid event", ste.Event)
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	parentSpan.SetAttributes(attribute.String("event", ste.Event))

	var detailsMap map[string]interface{}
	if err = json.Unmarshal([]byte(ste.Details), &detailsMap); err != nil {
		title := "'details' field is not valid json"
		err = errors.New(title)
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Use V1 handler
	if s.conns.DbHandler == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V1 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	err = s.WriteStageTransitionEvent(traceCtx, ste)
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

	s.eventPub.Publish(traceCtx, ste)

	status := http.StatusAccepted
	w.WriteHeader(status)
	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

func (s *Server) ListStageTransitionEvents(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "LIST Stage Transition Events Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
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
	parentSpan.SetAttributes(attribute.String("instanceId", instanceId))

	// Use V1 handler
	if s.conns.DbHandler == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V1 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	event, err := s.conns.DbHandler.ListStageTransitionEvents(traceCtx, functionVersionId, instanceId)
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
	err = json.NewEncoder(w).Encode(event)
	if err != nil {
		status = http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("failed to encode json"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

func (s *Server) ListInstances(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "LIST Instances Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))

	eventFilter, err := processFilters(r.URL)
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

	// Use V1 handler
	if s.conns.DbHandler == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V1 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	// Check if pagination parameters are provided or if we need to use paginated response
	if paginationParams.Limit > 0 || paginationParams.PageToken != "" {
		// Apply default limit if only pageToken is provided
		paginationParams = paginationParams.ApplyDefaultLimit(s.paginationConfig.DefaultPageSize)

		// Use paginated method
		paginatedResponse, err := s.conns.DbHandler.ListInstancesPaginated(traceCtx, functionVersionId, paginationParams)
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
	instances, err := s.conns.DbHandler.ListInstances(traceCtx, functionVersionId)
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

func (s *Server) ArchiveInstanceStageTransitionEvents(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Archive Instance Stage Transition Events Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
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
	parentSpan.SetAttributes(attribute.String("instanceId", instanceId))

	// Use V1 handler
	if s.conns.DbHandler == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V1 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	err = s.conns.DbHandler.ArchiveInstanceStageTransitionEvents(traceCtx, functionVersionId, instanceId)
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

func (s *Server) ArchiveFunctionVersionStageTransitionEvents(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Archive Function Version Stage Transition Events Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))

	// Use V1 handler
	if s.conns.DbHandler == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V1 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	instances, err := s.conns.DbHandler.ListInstances(traceCtx, functionVersionId)
	if err != nil {
		var status int
		var title string
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
			title = "instances not found"
		} else {
			status = http.StatusInternalServerError
			title = "failed to retrieve instances"
		}
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	for _, instance := range instances {
		// Ensure V1 handler is available before loop iteration (already checked above)
		err = s.conns.DbHandler.ArchiveInstanceStageTransitionEvents(traceCtx, functionVersionId, instance.InstanceId)
		if err != nil {
			var status int
			var title string
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
				title = "events not found"
			} else {
				status = http.StatusInternalServerError
				title = "failed to archive instance events"
			}
			api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
			logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
			return
		}
	}
	status := http.StatusOK
	w.WriteHeader(status)
	logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
}

func (s *Server) GetDeploymentStats(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Get Deployment Stats Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))

	// Use V1 handler
	if s.conns.DbHandler == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V1 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	stats, err := s.conns.DbHandler.ReadDeploymentStats(traceCtx, functionVersionId)
	if err != nil {
		var status int
		var title string
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
			title = "functionVersionId not found"
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

func (s *Server) GetInstanceEvent(w http.ResponseWriter, r *http.Request) {
	parentCtx := r.Context()
	// Tracing handled by external library
	traceCtx := parentCtx

	logger := logging.GetLogger(traceCtx)
	logging.LogHTTPRequest(traceCtx, logger, r)

	errType := "Get Instance Event Error"

	vars := mux.Vars(r)
	functionVersionId, err := uuid.Parse(vars["functionVersionId"])
	if err != nil {
		title := "'functionVersionId' field is not valid UUID"
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

	event, err := codex.NewEvent(vars["event"])
	if err != nil {
		title := "'event' field is invalid"
		status := http.StatusBadRequest
		api_error.GenerateErrorResponse(traceCtx, errType, title, r.URL.Path, status, err, w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}

	parentSpan := trace.SpanFromContext(parentCtx)
	parentSpan.SetAttributes(attribute.String("functionVersionId", functionVersionId.String()))
	parentSpan.SetAttributes(attribute.String("instanceId", instanceId))
	parentSpan.SetAttributes(attribute.String("event", event.Name))

	// Use V1 handler
	if s.conns.DbHandler == nil {
		status := http.StatusInternalServerError
		api_error.GenerateErrorResponse(traceCtx, errType, "Internal Server Error", r.URL.Path, status, errors.New("V1 DB handler not initialized"), w)
		logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
		return
	}
	eventRecord, err := s.conns.DbHandler.GetInstanceEvent(traceCtx, functionVersionId, instanceId, event)
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
