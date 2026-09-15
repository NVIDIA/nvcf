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
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

// processFilters extracts and validates event and stage filters from URL query parameters
func processFilters(url *url.URL) (map[string]struct{}, error) {
	eventFilter := make(map[string]struct{})

	// Low level event filters
	filters, found := url.Query()["eventFilter"]
	if found {
		for _, v := range filters {
			// validate that the event filter name is a valid one
			if _, ok := constants.ValidEvents[v]; !ok {
				return nil, fmt.Errorf("invalid eventFilter: %s", v)
			}
			eventFilter[v] = struct{}{}
		}
	}

	// High level stage filters, each one is equivalent to multiple eventFilters
	stages, found := url.Query()["stageFilter"]
	if found {
		for _, v := range stages {
			events := constants.ValidStages[v]
			if events == nil {
				return nil, fmt.Errorf("invalid stageFilter: %s", v)
			}
			for _, event := range events {
				eventFilter[event] = struct{}{}
			}
		}
	}
	return eventFilter, nil
}

// parsePaginationParams parses and validates pagination parameters from URL query
func parsePaginationParams(query url.Values, config config.PaginationConfig) (data_access.PaginationParams, error) {
	limitStr := query.Get("limit")
	pageToken := query.Get("pageToken")

	// If no pagination params provided, return default
	if limitStr == "" && pageToken == "" {
		return data_access.PaginationParams{}, nil
	}

	limit := config.DefaultPageSize // Use configured default
	var err error
	if limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			return data_access.PaginationParams{}, fmt.Errorf("invalid limit: %s", limitStr)
		}

		if limit < config.MinPageSize {
			return data_access.PaginationParams{}, fmt.Errorf("limit cannot be less than %d: %d", config.MinPageSize, limit)
		}

		if limit > config.MaxPageSize {
			return data_access.PaginationParams{}, fmt.Errorf("limit cannot be greater than %d: %d", config.MaxPageSize, limit)
		}
	}

	return data_access.PaginationParams{
		Limit:     limit,
		PageToken: pageToken,
	}, nil
}

// filterInstancesByEvent filters instances based on event filter criteria
func filterInstancesByEvent(instances []types.Instance, eventFilter map[string]struct{}, logger *logging.TraceLogger, traceCtx context.Context) []types.Instance {
	// Do not filter if no event filter is provided
	if len(eventFilter) == 0 {
		return instances
	}

	filteredInstances := make([]types.Instance, 0)
	for _, instance := range instances {
		if _, ok := eventFilter[instance.LastEvent]; ok {
			filteredInstances = append(filteredInstances, instance)
		}
	}

	logger.DebugContext(
		traceCtx,
		"filtered instances by event",
		zap.Int("originalCount", len(instances)),
		zap.Int("remainingInstances", len(filteredInstances)),
		zap.Any("eventFilter", eventFilter),
	)

	return filteredInstances
}
