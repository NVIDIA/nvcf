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
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
)

// accountPathParams reads and validates the account, function, and function
// version from the route. It writes a 400 problem detail and returns false when
// any of them is malformed.
func accountPathParams(w http.ResponseWriter, r *http.Request) (ncaID, functionID, functionVersionID string, ok bool) {
	vars := mux.Vars(r)
	ncaID = vars["ncaId"]
	functionID = vars["functionId"]
	functionVersionID = vars["functionVersionId"]

	switch {
	case len(ncaID) > maxNcaIDLength || !ncaIDFieldPattern.MatchString(ncaID):
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request",
			fmt.Sprintf("invalid ncaId: must match %s and be at most %d characters", ncaIDFieldPattern, maxNcaIDLength))
	case !uuidFieldPattern.MatchString(functionID):
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", "invalid functionId: must be a UUID")
	case !uuidFieldPattern.MatchString(functionVersionID):
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", "invalid functionVersionId: must be a UUID")
	default:
		return ncaID, functionID, functionVersionID, true
	}
	return "", "", "", false
}

// GetAccountEventsV3 handles GET requests to
// /v3/ledger/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/events
// It returns the events of one context in the function version namespace that
// were recorded for the given account and function. The path account is
// trusted: the caller is a service that has already authenticated the user.
// A request that matches nothing returns 200 with an empty list, so a caller
// cannot tell whether a function exists or who owns it.
// It accepts the same query parameters as GetEventsV3.
func (s *Server) GetAccountEventsV3(w http.ResponseWriter, r *http.Request) {
	traceCtx := r.Context()
	logger := logging.GetLogger(traceCtx)

	ncaID, functionID, namespace, ok := accountPathParams(w, r)
	if !ok {
		return
	}

	queryParams := r.URL.Query()
	contextV3 := ContextV3{
		InstanceID:         queryParams.Get(contextFieldInstanceID),
		DeploymentID:       queryParams.Get(contextFieldDeploymentID),
		GPUSpecificationID: queryParams.Get(contextFieldGPUSpecificationID),
		ClusterID:          queryParams.Get(contextFieldClusterID),
		ResourceID:         queryParams.Get(contextFieldResourceID),
	}

	attributeKey := queryParams.Get(queryParamAttributeKey)
	attributeValue := queryParams.Get(queryParamAttributeValue)
	if (attributeKey == "") != (attributeValue == "") {
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request",
			fmt.Sprintf("%s and %s must be provided together", queryParamAttributeKey, queryParamAttributeValue))
		return
	}

	eventContext, err := eventContextToCanonical(contextV3)
	if err != nil {
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request",
			fmt.Sprintf("Invalid context parameters: %s", err.Error()))
		return
	}

	records, err := s.conns.DbHandlerV2.GetAccountEventsV3(traceCtx, namespace, eventContext, ncaID, functionID)
	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to retrieve account events",
			zap.Error(err),
			zap.String("namespace", namespace),
			zap.String("context", eventContext),
			zap.String("nca_id", ncaID),
			zap.String("function_id", functionID))
		sendProblemDetail(w, http.StatusInternalServerError, "Internal Server Error",
			"Failed to retrieve events from database")
		return
	}

	// Cassandra cannot ORDER BY timestamp since it is not a clustering key.
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})

	response := EventsV3Response{
		Namespace: namespace,
		Context:   eventContext,
		Events:    make([]EventV3Item, 0, len(records)),
	}

	for _, record := range records {
		var details EventDetails
		if len(record.Details) > 0 {
			if err := json.Unmarshal(record.Details, &details); err != nil {
				logger.WarnContext(traceCtx, "Failed to unmarshal event details, using empty details",
					zap.Error(err),
					zap.String("event_name", record.EventName),
					zap.Time("timestamp", record.Timestamp))
				details = EventDetails{}
			}
		}

		if !attributeMatches(details, attributeKey, attributeValue) {
			continue
		}

		response.Events = append(response.Events, EventV3Item{
			EventName: record.EventName,
			Source:    record.Source,
			Timestamp: record.Timestamp,
			Details:   details,
			CreatedAt: record.CreatedAt,
			UpdatedAt: record.UpdatedAt,
		})
	}

	logger.InfoContext(traceCtx, "Successfully retrieved account events",
		zap.String("namespace", namespace),
		zap.String("nca_id", ncaID),
		zap.String("function_id", functionID),
		zap.Int("event_count", len(response.Events)))

	sendJSONResponse(w, http.StatusOK, response)
}

// GetAccountStatsV3 handles GET requests to
// /v3/ledger/accounts/{ncaId}/functions/{functionId}/versions/{functionVersionId}/stats
// It returns stats for the function version namespace built only from rows
// recorded for the given account and function, so the summary counts only
// those rows. The optional eventFilter query parameter works as in
// GetStatsV3. A request that matches nothing returns 200 with empty data.
func (s *Server) GetAccountStatsV3(w http.ResponseWriter, r *http.Request) {
	traceCtx := r.Context()
	logger := logging.GetLogger(traceCtx)

	ncaID, functionID, namespace, ok := accountPathParams(w, r)
	if !ok {
		return
	}

	eventFilter := r.URL.Query().Get("eventFilter")
	var filterEvents map[string]struct{}
	if eventFilter != "" {
		filterEvents = make(map[string]struct{})
		for _, event := range strings.Split(eventFilter, ",") {
			filterEvents[strings.TrimSpace(event)] = struct{}{}
		}
	}

	records, err := s.conns.DbHandlerV2.GetAccountStatsV3(traceCtx, namespace, ncaID, functionID)
	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to retrieve account stats",
			zap.Error(err),
			zap.String("namespace", namespace),
			zap.String("nca_id", ncaID),
			zap.String("function_id", functionID))
		sendProblemDetail(w, http.StatusInternalServerError, "Internal Server Error",
			"Failed to retrieve stats from database")
		return
	}

	response := StatsV3Response{
		Namespace: namespace,
		Summary: StatsSummary{
			ByEvent: make(map[string]int),
		},
		Contexts: []ContextStats{},
	}

	for _, record := range records {
		if filterEvents != nil {
			if _, ok := filterEvents[record.EventName]; !ok {
				continue
			}
		}

		response.Contexts = append(response.Contexts, ContextStats{
			Context:   record.Context,
			EventName: record.EventName,
			Timestamp: record.Timestamp,
			FirstSeen: record.CreatedAt,
		})
		response.Summary.ByEvent[record.EventName]++
	}
	response.Summary.TotalContexts = len(response.Contexts)

	logger.InfoContext(traceCtx, "Successfully retrieved account stats",
		zap.String("namespace", namespace),
		zap.String("nca_id", ncaID),
		zap.String("function_id", functionID),
		zap.Int("total_contexts", response.Summary.TotalContexts))

	sendJSONResponse(w, http.StatusOK, response)
}
