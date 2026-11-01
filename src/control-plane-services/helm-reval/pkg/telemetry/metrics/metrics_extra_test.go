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

package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/go-chi/chi/v5"
)

// ── NewMetricsServer ──────────────────────────────────────────────────────────

func TestNewMetricsServer_ReturnsServer(t *testing.T) {
	logger := zap.NewNop()
	srv := NewMetricsServer(logger, 9999)
	require.NotNil(t, srv)
	assert.Equal(t, ":9999", srv.Addr)
	assert.NotNil(t, srv.Handler)
}

func TestNewMetricsServer_MetricsEndpointResponds(t *testing.T) {
	logger := zap.NewNop()
	srv := NewMetricsServer(logger, 0)
	require.NotNil(t, srv)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	srv.Handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── getRoutePattern ───────────────────────────────────────────────────────────

func TestGetRoutePattern_NoRouteContext_ReturnsFallback(t *testing.T) {
	// Request without a chi route context → should return r.URL.Path.
	r := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	// No chi route context in this request, so rctx == nil.
	got := getRoutePattern(r)
	assert.Equal(t, "/some/path", got)
}

func TestGetRoutePattern_ChiRouterWithPattern(t *testing.T) {
	router := chi.NewRouter()
	var capturedPattern string
	router.Get("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		capturedPattern = getRoutePattern(r)
		w.WriteHeader(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/items/{id}", capturedPattern)
}

// ── RunTimerFromContext ───────────────────────────────────────────────────────

func TestRunTimerFromContext_NoTimer_ReturnsDefault(t *testing.T) {
	rt := RunTimerFromContext(context.Background())
	require.NotNil(t, rt)
	// Default timer should have zero durations.
	assert.Equal(t, time.Duration(0), rt.GetHelmDownloadDuration())
	assert.Equal(t, time.Duration(0), rt.GetImageCheckDuration())
}

func TestRunTimerFromContext_WithTimer_ReturnsSame(t *testing.T) {
	rt := NewRunTimer()
	ctx := RunTimerIntoContext(context.Background(), rt)
	got := RunTimerFromContext(ctx)
	assert.Equal(t, rt, got)
}

// ── RunTimer record/get cycle ─────────────────────────────────────────────────

func TestRunTimer_RecordCycle(t *testing.T) {
	rt := NewRunTimer()

	rt.RecordThreadStart()
	rt.RecordHelmDownloadStart()
	time.Sleep(1 * time.Millisecond)
	rt.RecordHelmDownloadEnd()
	rt.RecordImageCheckStart()
	time.Sleep(1 * time.Millisecond)
	rt.RecordImageCheckEnd()
	rt.RecordThreadEnd()

	assert.Positive(t, rt.GetHelmDownloadDuration())
	assert.Positive(t, rt.GetImageCheckDuration())
	assert.GreaterOrEqual(t, rt.GetLocalThreadDuration(), time.Duration(0))
}
