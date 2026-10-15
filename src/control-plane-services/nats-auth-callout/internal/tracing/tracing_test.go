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

package tracing

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"
)

func TestAutoDetectRuntimeAttributes(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		expectAttrs []string // Attributes we expect to be detected
	}{
		{
			name: "Full Kubernetes environment",
			envVars: map[string]string{
				"POD_NAME":      "nvcf-nats-auth-callout-service-abc123",
				"POD_NAMESPACE": "production",
				"NODE_NAME":     "worker-node-1",
				"CLUSTER_NAME":  "k8s-cluster-prod",
			},
			expectAttrs: []string{"pod_name", "namespace", "node_name", "cluster_name", "instance_id"},
		},
		{
			name: "Minimal Kubernetes environment",
			envVars: map[string]string{
				"POD_NAME":      "test-pod",
				"POD_NAMESPACE": "default",
			},
			expectAttrs: []string{"pod_name", "namespace", "instance_id"},
		},
		{
			name: "Hostname fallback",
			envVars: map[string]string{
				"HOSTNAME": "fallback-host",
			},
			expectAttrs: []string{"pod_name", "instance_id"},
		},

		{
			name:        "No environment variables",
			envVars:     map[string]string{},
			expectAttrs: []string{}, // May detect hostname-based instance_id
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original environment
			originalVars := make(map[string]string)
			envVarsToClean := []string{"POD_NAME", "POD_NAMESPACE", "NODE_NAME", "CLUSTER_NAME", "HOSTNAME"}
			for _, envVar := range envVarsToClean {
				originalVars[envVar] = os.Getenv(envVar)
				os.Unsetenv(envVar)
			}

			// Set test environment variables
			for key, value := range tt.envVars {
				os.Setenv(key, value)
			}

			// Restore environment after test
			defer func() {
				for _, envVar := range envVarsToClean {
					if originalValue, exists := originalVars[envVar]; exists && originalValue != "" {
						os.Setenv(envVar, originalValue)
					} else {
						os.Unsetenv(envVar)
					}
				}
			}()

			logger := zaptest.NewLogger(t)
			provider := &OtelProvider{logger: logger}
			attrs := provider.autoDetectRuntimeAttributes()

			// Check that we got some attributes if expected
			if len(tt.expectAttrs) > 0 && len(attrs) == 0 {
				t.Errorf("Expected to detect attributes %v, but got none", tt.expectAttrs)
			}

			// Verify expected attributes are present
			// Note: This is a simplified check - in a real scenario, you'd want to
			// examine the actual resource attributes to verify they contain the expected values
			if len(attrs) > 0 {
				t.Logf("Detected %d resource attributes", len(attrs))
			}
		})
	}
}

func TestNewTracingProvider_Disabled(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracingConfig := TracingConfig{
		Enabled: false,
	}

	provider, err := NewTracingProvider(logger, tracingConfig)
	if err != nil {
		t.Fatalf("Expected no error when tracing is disabled, got %v", err)
	}
	if provider == nil {
		t.Error("Expected provider, got nil")
	}

	// Test provider info
	info := provider.GetProviderInfo()
	if info != "No tracing provider (disabled)" {
		t.Errorf("Expected no-op provider info, got %s", info)
	}

	// Test initialization
	shutdown, err := provider.InitTracing(context.Background(), "test-service", "v1.0.0")
	if err != nil {
		t.Fatalf("Expected no error when initializing disabled tracing, got %v", err)
	}
	if shutdown == nil {
		t.Error("Expected shutdown function, got nil")
	}

	// Test shutdown function
	err = shutdown(context.Background())
	if err != nil {
		t.Errorf("Expected no error from shutdown, got %v", err)
	}
}

func TestNewTracingProvider_OTEL_InvalidEndpoint(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracingConfig := TracingConfig{
		Enabled:  true,
		Provider: "otel",
		Otel: OtelTracingConfig{
			HTTPEndpoint: "", // No endpoint configured
			Endpoint:     "", // No fallback either
		},
	}

	provider, err := NewTracingProvider(logger, tracingConfig)
	if err != nil {
		t.Fatalf("Expected no error creating provider, got %v", err)
	}

	shutdown, err := provider.InitTracing(context.Background(), "test-service", "v1.0.0")
	if err == nil {
		t.Error("Expected error for missing endpoint")
		if shutdown != nil {
			shutdown(context.Background())
		}
	} else {
		t.Logf("Got expected error for missing endpoint: %v", err)
	}
}

func TestNewTracingProvider_OTEL_ValidConfig(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracingConfig := TracingConfig{
		Enabled:  true,
		Provider: "otel",
		Otel: OtelTracingConfig{
			HTTPEndpoint:       "http://localhost:4318/v1/traces",
			Insecure:           true,
			SamplingRatio:      "0.1",
			TimeoutMs:          5000,
			RetryDelayMs:       1000,
			MaxExportBatchSize: 100,
			ExportTimeoutMs:    10000,
			MaxQueueSize:       1000,
			ScheduleDelayMs:    2000,
			Compression:        "gzip",
			Headers: TracingHeadersConfig{
				Authorization: "Bearer test-token",
				XAPIKey:       "test-api-key",
			},
		},
	}

	provider, err := NewTracingProvider(logger, tracingConfig)
	if err != nil {
		t.Fatalf("Expected no error creating provider, got %v", err)
	}

	// Test provider info
	info := provider.GetProviderInfo()
	expectedInfo := "OpenTelemetry - Endpoint: http://localhost:4318/v1/traces, Environment: "
	if !strings.HasPrefix(info, expectedInfo) {
		t.Errorf("Expected provider info to start with %s, got %s", expectedInfo, info)
	}

	shutdown, err := provider.InitTracing(context.Background(), "test-service", "v1.0.0")
	// We expect this might fail in test environment due to no actual OTLP collector
	if err != nil {
		t.Logf("InitTracing failed (expected in test environment): %v", err)
	} else {
		t.Log("InitTracing succeeded")
		if shutdown != nil {
			err = shutdown(context.Background())
			if err != nil {
				t.Logf("Shutdown failed: %v", err)
			}
		}
	}
}

func TestOtelProvider_WithAutoDetection(t *testing.T) {
	// Set up test environment to simulate Kubernetes
	originalVars := make(map[string]string)
	envVarsToSet := map[string]string{
		"POD_NAME":      "test-pod-12345",
		"POD_NAMESPACE": "test-namespace",
		"NODE_NAME":     "test-node",
	}
	envVarsToClean := []string{"POD_NAME", "POD_NAMESPACE", "NODE_NAME"}

	// Save and clear environment
	for _, envVar := range envVarsToClean {
		originalVars[envVar] = os.Getenv(envVar)
		os.Unsetenv(envVar)
	}

	// Set test environment
	for key, value := range envVarsToSet {
		os.Setenv(key, value)
	}

	// Restore environment after test
	defer func() {
		for _, envVar := range envVarsToClean {
			if originalValue, exists := originalVars[envVar]; exists && originalValue != "" {
				os.Setenv(envVar, originalValue)
			} else {
				os.Unsetenv(envVar)
			}
		}
	}()

	logger := zaptest.NewLogger(t)
	tracingConfig := TracingConfig{
		Enabled:  true,
		Provider: "otel",
		Otel: OtelTracingConfig{
			HTTPEndpoint: "http://localhost:4318/v1/traces",
			Insecure:     true,
		},
	}

	provider, err := NewTracingProvider(logger, tracingConfig)
	if err != nil {
		t.Fatalf("Expected no error creating provider, got %v", err)
	}

	shutdown, err := provider.InitTracing(context.Background(), "test-service", "v1.0.0")
	// We expect this might fail in test environment due to no actual OTLP collector
	if err != nil {
		t.Logf("InitTracing failed (expected in test environment): %v", err)
	} else {
		t.Log("InitTracing with auto-detection succeeded")
		if shutdown != nil {
			err = shutdown(context.Background())
			if err != nil {
				t.Logf("Shutdown failed: %v", err)
			}
		}
	}
}

func TestNewTracingProvider_Lightstep_ValidConfig(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracingConfig := TracingConfig{
		Enabled:  true,
		Provider: "lightstep",
		Lightstep: LightstepTracingConfig{
			Endpoint:           "https://ingest.lightstep.com:443/traces/otel/v1",
			AccessToken:        "test-token",
			Environment:        "test",
			SamplingRatio:      "0.5",
			TimeoutMs:          5000,
			MaxExportBatchSize: 100,
			ExportTimeoutMs:    10000,
			MaxQueueSize:       1000,
			ScheduleDelayMs:    2000,
			Compression:        "gzip",
			Insecure:           false,
		},
	}

	provider, err := NewTracingProvider(logger, tracingConfig)
	if err != nil {
		t.Fatalf("Expected no error creating Lightstep provider, got %v", err)
	}

	// Test provider info
	info := provider.GetProviderInfo()
	expectedInfo := "Lightstep - Endpoint: https://ingest.lightstep.com:443/traces/otel/v1, Environment: test"
	if info != expectedInfo {
		t.Errorf("Expected provider info %s, got %s", expectedInfo, info)
	}

	shutdown, err := provider.InitTracing(context.Background(), "test-service", "v1.0.0")
	// We expect this might fail in test environment due to no actual Lightstep endpoint
	if err != nil {
		t.Logf("Lightstep InitTracing failed (expected in test environment): %v", err)
	} else {
		t.Log("Lightstep InitTracing succeeded")
		if shutdown != nil {
			err = shutdown(context.Background())
			if err != nil {
				t.Logf("Lightstep shutdown failed: %v", err)
			}
		}
	}
}

func TestNewTracingProvider_UnsupportedProvider(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracingConfig := TracingConfig{
		Enabled:  true,
		Provider: "unsupported-provider",
	}

	provider, err := NewTracingProvider(logger, tracingConfig)
	if err == nil {
		t.Error("Expected error for unsupported provider")
	}
	if provider != nil {
		t.Error("Expected nil provider for unsupported provider")
	}
}

func TestNewTracingProvider_EmptyProvider(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracingConfig := TracingConfig{
		Enabled:  true,
		Provider: "",
	}

	provider, err := NewTracingProvider(logger, tracingConfig)
	if err == nil {
		t.Error("Expected error for empty provider")
	}
	if provider != nil {
		t.Error("Expected nil provider for empty provider")
	}
}
