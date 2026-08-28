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
	"net/url"
	"testing"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"
)

func TestProcessFilters(t *testing.T) {
	tests := []struct {
		name     string
		url      *url.URL
		expected map[string]struct{}
	}{
		{
			name:     "empty",
			url:      &url.URL{Path: "/"},
			expected: map[string]struct{}{},
		},
		{
			name: "single eventFilter",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?eventFilter=pending")
				return u
			}(),
			expected: map[string]struct{}{"pending": {}},
		},
		{
			name: "multiple eventFilters",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?eventFilter=pending&eventFilter=ready")
				return u
			}(),
			expected: map[string]struct{}{"pending": {}, "ready": {}},
		},
		{
			name: "destroyed stageFilter",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?stageFilter=destroyed")
				return u
			}(),
			expected: map[string]struct{}{
				"destroyed":             {},
				"requestingTermination": {},
			},
		},
		{
			name: "ready stageFilter",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?stageFilter=ready")
				return u
			}(),
			expected: map[string]struct{}{
				"ready": {},
				// "active": {},
			},
		},
		{
			name: "error stageFilter",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?stageFilter=error")
				return u
			}(),
			expected: map[string]struct{}{
				"pendingError":               {},
				"buildingError":              {},
				"downloadingModelError":      {},
				"downloadingContainerError":  {},
				"initializingContainerError": {},
			},
		},
		{
			name: "pending stageFilter",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?stageFilter=pending")
				return u
			}(),
			expected: map[string]struct{}{
				"pending":               {},
				"building":              {},
				"downloadingModel":      {},
				"downloadingContainer":  {},
				"initializingContainer": {},
			},
		},
		{
			name: "multiple stageFilters",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?stageFilter=pending&stageFilter=ready")
				return u
			}(),
			expected: map[string]struct{}{
				"pending":               {},
				"building":              {},
				"downloadingModel":      {},
				"downloadingContainer":  {},
				"initializingContainer": {},
				"ready":                 {},
				// "active":                {},
			},
		},
		{
			name: "mixed stage and event filters",
			url: func() *url.URL {
				u, _ := url.ParseRequestURI("/?stageFilter=error&eventFilter=initializingContainer")
				return u
			}(),
			expected: map[string]struct{}{
				"pendingError":               {},
				"buildingError":              {},
				"downloadingModelError":      {},
				"downloadingContainerError":  {},
				"initializingContainerError": {},
				"initializingContainer":      {},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := processFilters(test.url)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestFilterInstancesByEvent(t *testing.T) {
	tests := []struct {
		name        string
		instances   []types.Instance
		eventFilter map[string]struct{}
		expected    []types.Instance
	}{
		{
			name: "no filter",
			instances: []types.Instance{
				{LastEvent: "pending"},
				{LastEvent: "ready"},
			},
			eventFilter: map[string]struct{}{},
			expected: []types.Instance{
				{LastEvent: "pending"},
				{LastEvent: "ready"},
			},
		},
		{
			name: "single filter",
			instances: []types.Instance{
				{LastEvent: "pending"},
				{LastEvent: "ready"},
			},
			eventFilter: map[string]struct{}{
				"pending": {},
			},
			expected: []types.Instance{
				{LastEvent: "pending"},
			},
		},
		{
			name: "multiple filter",
			instances: []types.Instance{
				{LastEvent: "pending"},
				{LastEvent: "ready"},
				{LastEvent: "banana"},
			},
			eventFilter: map[string]struct{}{
				"pending": {},
				"ready":   {},
			},
			expected: []types.Instance{
				{LastEvent: "pending"},
				{LastEvent: "ready"},
			},
		},
		{
			name: "no results",
			instances: []types.Instance{
				{LastEvent: "pending"},
				{LastEvent: "ready"},
				{LastEvent: "banana"},
			},
			eventFilter: map[string]struct{}{
				"buildingError": {},
			},
			expected: make([]types.Instance, 0),
		},
	}
	logger := logging.NewTraceLogger(context.Background(), testutils.InitTestLogger(t))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := filterInstancesByEvent(test.instances, test.eventFilter, logger, context.Background())
			assert.NotNil(t, result)
			assert.Equal(t, test.expected, result)
		})
	}
}
