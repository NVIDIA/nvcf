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

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestNew(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := &Config{
		ServiceName: "test-service",
	}

	router := New(logger, config)

	if router == nil {
		t.Fatal("Expected router to be created, but got nil")
	}

	if router.engine == nil {
		t.Fatal("Expected gin engine to be created, but got nil")
	}

	if router.logger != logger {
		t.Fatal("Expected logger to be set correctly")
	}

	if router.config != config {
		t.Fatal("Expected config to be set correctly")
	}
}

func TestHandlePing(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := &Config{
		ServiceName: "test-service",
	}

	router := New(logger, config)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/ping", nil)

	router.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, but got %d", http.StatusOK, w.Code)
	}

	// Parse response body
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response body: %v", err)
	}

	// Check message
	if response["message"] != "pong" {
		t.Errorf("Expected message 'pong', but got '%v'", response["message"])
	}

	// Check service name
	if response["service"] != "test-service" {
		t.Errorf("Expected service 'test-service', but got '%v'", response["service"])
	}

	// Check timestamp exists
	if _, exists := response["timestamp"]; !exists {
		t.Error("Expected timestamp field to exist in response")
	}
}

func TestHandleRoot(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := &Config{
		ServiceName: "test-service",
	}

	router := New(logger, config)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)

	router.engine.ServeHTTP(w, req)

	if w.Code != http.StatusMovedPermanently {
		t.Errorf("Expected status code %d, but got %d", http.StatusMovedPermanently, w.Code)
	}

	// Check redirect location
	location := w.Header().Get("Location")
	if location != "/v1/ping" {
		t.Errorf("Expected redirect location '/v1/ping', but got '%s'", location)
	}
}

func TestEngine(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := &Config{
		ServiceName: "test-service",
	}

	router := New(logger, config)
	engine := router.Engine()

	if engine == nil {
		t.Fatal("Expected engine to be returned, but got nil")
	}

	if engine != router.engine {
		t.Fatal("Expected returned engine to match internal engine")
	}
}

// Benchmark tests
func BenchmarkHandlePing(b *testing.B) {
	logger, _ := zap.NewDevelopment()
	config := &Config{
		ServiceName: "test-service",
	}

	router := New(logger, config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/ping", nil)
		router.engine.ServeHTTP(w, req)
	}
}

func TestMetricsDisabled(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := &Config{
		ServiceName: "test-service",
		Metrics: &MetricsConfig{
			Enabled: false,
			Port:    "9090",
		},
	}

	router := New(logger, config)

	// Test that metrics endpoint returns 404 when disabled
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/metrics", nil)
	router.engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status code %d for disabled metrics endpoint, but got %d", http.StatusNotFound, w.Code)
	}

	// Test that healthz still works
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/healthz", nil)
	router.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d for healthz endpoint, but got %d", http.StatusOK, w.Code)
	}
}
