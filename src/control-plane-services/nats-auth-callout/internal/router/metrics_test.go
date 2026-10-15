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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestDurationHistogram(t *testing.T) {
	if DurationHistogram == nil {
		t.Fatal("Expected DurationHistogram to be initialized, got nil")
	}

	// Test that we can observe values
	DurationHistogram.WithLabelValues("/test", "GET", "200").Observe(0.1)

	// Test metric collection
	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	found := false
	for _, mf := range metricFamilies {
		if mf.GetName() == "request_duration_seconds" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected to find request_duration_seconds metric")
	}
}

func TestRequestCounter(t *testing.T) {
	if RequestCounter == nil {
		t.Fatal("Expected RequestCounter to be initialized, got nil")
	}

	// Test that we can increment counter
	RequestCounter.WithLabelValues("/test", "GET", "200").Inc()

	// Test metric collection
	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	found := false
	for _, mf := range metricFamilies {
		if mf.GetName() == "requests_total" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected to find requests_total metric")
	}
}

func TestMetricsLabels(t *testing.T) {
	// Test different label combinations
	testCases := []struct {
		path   string
		method string
		status string
	}{
		{"/v1/ping", "GET", "200"},
		{"/healthz", "GET", "200"},
		{"/metrics", "GET", "200"},
		{"/v1/ping", "POST", "405"},
	}

	for _, tc := range testCases {
		DurationHistogram.WithLabelValues(tc.path, tc.method, tc.status).Observe(0.1)
		RequestCounter.WithLabelValues(tc.path, tc.method, tc.status).Inc()
	}

	// Verify metrics were recorded
	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	if len(metricFamilies) == 0 {
		t.Error("Expected metrics to be recorded")
	}
}
