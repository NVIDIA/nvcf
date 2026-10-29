// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
)

// Metrics leave over OTLP only, so the health server has no scrape endpoint.
func TestHealthServesLivenessButNoMetricsEndpoint(t *testing.T) {
	spool, _ := spoolWith(t)
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{}, nil)
	for path, want := range map[string]int{
		"/livez":   http.StatusOK,
		"/metrics": http.StatusNotFound,
	} {
		response := httptest.NewRecorder()
		uploader.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		if response.Code != want {
			t.Errorf("%s status = %d, want %d", path, response.Code, want)
		}
	}
}
