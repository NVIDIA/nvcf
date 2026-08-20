// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNewRegistersAndPreinitializesMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	m, err := New(registry)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if len(families) != 10 {
		t.Fatalf("metric families = %d, want 10", len(families))
	}
	response := httptest.NewRecorder()
	m.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.Len() == 0 {
		t.Fatal("metrics response is empty")
	}
}

func TestNewRejectsNilRegistry(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
}
