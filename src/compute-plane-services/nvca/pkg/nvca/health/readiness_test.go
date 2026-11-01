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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type readinessStatusGetterMock struct {
	status        *atomic.Value
	lastUsedLevel nvcatypes.StatusLevel
}

func (m *readinessStatusGetterMock) GetStatus() nvcatypes.AgentHealth {
	return m.status.Load().(nvcatypes.AgentHealth)
}

func (m *readinessStatusGetterMock) GetStatusForLevel(level nvcatypes.StatusLevel) nvcatypes.AgentHealth {
	// Store the level for verification in tests
	m.lastUsedLevel = level
	return m.status.Load().(nvcatypes.AgentHealth)
}

func TestHTTPAddReadinessRoute(t *testing.T) {
	router := mux.NewRouter()
	mockGetter := &readinessStatusGetterMock{status: &atomic.Value{}}

	g := NewLazyReadinessCheckGetter()

	// Ensure GetCheck() returns false with no checkers.
	_, ok := g.GetCheck()
	assert.False(t, ok)
	g.SetCheck(mockGetter)
	_, ok = g.GetCheck()
	assert.True(t, ok)

	// Ensure error if SetCheck() is called twice.
	assert.PanicsWithValue(t, "readiness check is already initialized", func() {
		g.SetCheck(mockGetter)
	})

	HTTPAddReadinessRoute(router, g)
	s := httptest.NewServer(router)
	t.Cleanup(s.Close)

	type test struct {
		status                     nvcatypes.AgentHealth
		expectedResponseStatusCode int
	}

	httpClient := &http.Client{}
	tests := map[string]test{
		"Healthy status": {status: nvcatypes.AgentHealth{
			Status: nvcatypes.HealthStatusHealthy,
		}, expectedResponseStatusCode: http.StatusOK},
		"Unhealthy status": {status: nvcatypes.AgentHealth{
			Status: nvcatypes.HealthStatusUnhealthy,
		}, expectedResponseStatusCode: http.StatusServiceUnavailable},
	}
	for k, test := range tests {
		t.Run(k, func(t *testing.T) {
			mockGetter.status.Store(test.status)
			req, err := http.NewRequest(http.MethodGet, s.URL+HTTPReadinessRoutePath, nil)
			require.NoError(t, err)
			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { resp.Body.Close() })
			assert.Equal(t, test.expectedResponseStatusCode, resp.StatusCode)
			respStatus := nvcatypes.AgentHealth{}
			err = json.NewDecoder(resp.Body).Decode(&respStatus)
			require.NoError(t, err)
			assert.Equal(t, test.status, respStatus)
		})
	}
}

func TestReadinessHandler_UnhealthyReturns503WithBody(t *testing.T) {
	mockGetter := &readinessStatusGetterMock{status: &atomic.Value{}}
	mockGetter.status.Store(nvcatypes.AgentHealth{
		Status: nvcatypes.HealthStatusUnhealthy,
		Components: map[string]nvcatypes.ComponentHealth{
			"gpumonitor": {
				Status: nvcatypes.HealthStatusUnhealthy,
				Errors: []string{"no GPUs available in cluster"},
			},
		},
	})

	g := NewLazyReadinessCheckGetter()
	g.SetCheck(mockGetter)

	req, err := http.NewRequest(http.MethodGet, HTTPReadinessRoutePath, nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := httpReadinessHandler(g)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var respStatus nvcatypes.AgentHealth
	err = json.NewDecoder(rr.Body).Decode(&respStatus)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, respStatus.Status)
	require.Len(t, respStatus.Components, 1)
	gpuMonitorHealth, ok := respStatus.Components["gpumonitor"]
	require.True(t, ok, "gpumonitor component should exist")
	assert.Contains(t, gpuMonitorHealth.Errors, "no GPUs available in cluster")
}

func TestReadinessHandler_UsesWarnLevel(t *testing.T) {
	// Create a mock getter that tracks which level was requested
	mockGetter := &readinessStatusGetterMock{
		status: &atomic.Value{},
	}

	// Store a status for the test
	mockGetter.status.Store(nvcatypes.AgentHealth{
		Status: nvcatypes.HealthStatusHealthy,
	})

	g := NewLazyReadinessCheckGetter()
	g.SetCheck(mockGetter)

	// Create a test request
	req, err := http.NewRequest(http.MethodGet, HTTPReadinessRoutePath, nil)
	require.NoError(t, err)

	// Create a response recorder
	rr := httptest.NewRecorder()

	// Create and call the handler
	handler := httpReadinessHandler(g)
	handler.ServeHTTP(rr, req)

	// Check response status
	assert.Equal(t, http.StatusOK, rr.Code)

	// Verify the correct level was used
	assert.Equal(t, nvcatypes.StatusLevelWarn, mockGetter.lastUsedLevel,
		"Readiness handler should use StatusLevelWarn")
}
