/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package telemetry

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

func TestMetricsDefinitionsUseServiceScopedNames(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldProvider)
		_ = provider.Shutdown(context.Background())
	})

	InitializeMetrics()

	Add(HTTPRequestsTotal(), 1)
	AddUpDownWithContext(context.Background(), HTTPActiveRequests(), 1)
	Add(UpstreamRequestsTotal(), 1)
	Add(PubSubPublishFailures(), 1)
	Add(PubSubConsumeFailures(), 1)
	Record(PubSubConsumeDuration(), 0.01)
	Add(RateLimitEventsReceived(), 1)
	Add(RateLimitEventsDropped(), 1, DropReasonSameCluster())
	Add(RateLimitEventsApplied(), 1)
	Add(RateLimitEventsFailedApply(), 1)
	Add(RateLimitEventsDryRunWouldApply(), 1)
	Record(RateLimitSynchronizerPublishDuration(), 0.01)
	Record(RateLimitSynchronizerQueueWait(), 0.01)
	Add(RateLimitSynchronizerEventsDropped(), 1, SynchronizerDropReasonOldMessage())
	Record(RateLimitEventReplicationLag(), 1.5)
	Record(HTTPServerRequestDuration(), 0.01)
	Record(UpstreamRequestDuration(), 0.02)
	Add(LLMTokens(), 7)
	Record(ProviderTime(), 0.03)
	Record(StreamFirstToken(), 0.04)
	Record(StreamDuration(), 0.05)

	names := collectMetricNames(t, reader)
	for name := range names {
		if strings.HasPrefix(name, "orion"+".") {
			t.Fatalf("metric name %q must not use legacy Orion prefix", name)
		}
		if !strings.HasPrefix(name, "llm_api_gateway_") {
			t.Fatalf("metric name %q must start with llm_api_gateway_", name)
		}
	}

	for _, want := range []string{
		"llm_api_gateway_http_requests_total",
		"llm_api_gateway_http_request_duration_seconds",
		"llm_api_gateway_http_active_requests",
		"llm_api_gateway_upstream_requests_total",
		"llm_api_gateway_upstream_request_duration_seconds",
		"llm_api_gateway_llm_tokens_total",
		"llm_api_gateway_provider_time_seconds",
		"llm_api_gateway_stream_first_token_seconds",
		"llm_api_gateway_stream_duration_seconds",
		"llm_api_gateway_pubsub_publish_failures_total",
		"llm_api_gateway_pubsub_consume_failures_total",
		"llm_api_gateway_pubsub_consume_duration_seconds",
		"llm_api_gateway_rate_limit_event_replication_lag_seconds",
		"llm_api_gateway_rate_limit_events_received_total",
		"llm_api_gateway_rate_limit_events_dropped_total",
		"llm_api_gateway_rate_limit_events_applied_total",
		"llm_api_gateway_rate_limit_events_failed_apply_total",
		"llm_api_gateway_rate_limit_events_dry_run_would_apply_total",
		"llm_api_gateway_rate_limit_synchronizer_publish_duration_seconds",
		"llm_api_gateway_rate_limit_synchronizer_queue_wait_seconds",
		"llm_api_gateway_rate_limit_synchronizer_queue_length",
		"llm_api_gateway_rate_limit_synchronizer_events_dropped_total",
	} {
		if !names[want] {
			t.Fatalf("missing metric %q in %#v", want, names)
		}
	}
}

func TestInitStartsPrometheusMetricsServer(t *testing.T) {
	port := freePort(t)

	runtime, err := Init(context.Background(), RuntimeConfig{MetricsPort: port})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = runtime.Shutdown(context.Background())
	})

	Add(PubSubPublishFailures(), 1)

	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	body := waitForMetricsBody(t, url)
	if !strings.Contains(body, "llm_api_gateway_pubsub_publish_failures_total") {
		t.Fatalf("metrics body missing pubsub counter:\n%s", body)
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Fatalf("metrics body missing Go runtime collector:\n%s", body)
	}
	if strings.Contains(body, "llm_api_gateway_pubsub_publish_failures_total_total") {
		t.Fatalf("metrics body contains duplicated total suffix:\n%s", body)
	}
}

func TestInitAllowsMultiplePrometheusRuntimes(t *testing.T) {
	firstPort := freePort(t)
	first, err := Init(context.Background(), RuntimeConfig{MetricsPort: firstPort})
	if err != nil {
		t.Fatalf("first Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = first.Shutdown(context.Background())
	})

	secondPort := freePort(t)
	second, err := Init(context.Background(), RuntimeConfig{MetricsPort: secondPort})
	if err != nil {
		t.Fatalf("second Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = second.Shutdown(context.Background())
	})

	Add(PubSubPublishFailures(), 1)

	body := waitForMetricsBody(t, fmt.Sprintf("http://127.0.0.1:%d/metrics", secondPort))
	if !strings.Contains(body, "llm_api_gateway_pubsub_publish_failures_total") {
		t.Fatalf("second metrics body missing pubsub counter:\n%s", body)
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Fatalf("second metrics body missing Go runtime collector:\n%s", body)
	}
}

func TestInitDoesNotStartMetricsServerWhenPortIsZero(t *testing.T) {
	runtime, err := Init(context.Background(), RuntimeConfig{MetricsPort: 0})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = runtime.Shutdown(context.Background())
	})

	if runtime.metricsServer != nil {
		t.Fatal("metrics server is set, want nil when port is zero")
	}
}

func TestPrometheusHandlerProducesParseableMetricNames(t *testing.T) {
	provider, gatherer, err := newMeterProvider(context.Background(), "", resource.Empty(), true)
	if err != nil {
		t.Fatalf("new meter provider: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	meter := provider.Meter("test")
	histogram, err := meter.Float64Histogram(
		"http.client.request.body.size",
		otelmetric.WithUnit("By"),
		otelmetric.WithDescription("HTTP client request body size."),
	)
	if err != nil {
		t.Fatalf("create histogram: %v", err)
	}
	histogram.Record(context.Background(), 42)

	recorder := httptest.NewRecorder()
	NewPrometheusHandler(gatherer).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)

	body := recorder.Body.String()
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse Prometheus metrics: %v\n%s", err, body)
	}
	if strings.Contains(body, "http.client.request.body.size") {
		t.Fatalf("metrics body contains raw dotted OTel name:\n%s", body)
	}

	foundTranslated := false
	for name := range families {
		if strings.Contains(name, ".") {
			t.Fatalf("metric family %q contains dots", name)
		}
		if strings.HasPrefix(name, "http_client_request_body_size") {
			foundTranslated = true
		}
	}
	if !foundTranslated {
		t.Fatalf("missing translated HTTP client metric in %#v", families)
	}
}

func TestObservedRateLimitSynchronizerQueueLengthSumsObservers(t *testing.T) {
	queueLengthMu.Lock()
	previous := queueLengthObservers
	queueLengthObservers = []func() int64{
		func() int64 { return 2 },
		func() int64 { return 3 },
	}
	queueLengthMu.Unlock()
	t.Cleanup(func() {
		queueLengthMu.Lock()
		queueLengthObservers = previous
		queueLengthMu.Unlock()
	})

	if got := observedRateLimitSynchronizerQueueLength(); got != 5 {
		t.Fatalf("queue length = %d, want 5", got)
	}
}

func collectMetricNames(t *testing.T, reader *sdkmetric.ManualReader) map[string]bool {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	names := map[string]bool{}
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			names[metric.Name] = true
		}
	}
	return names
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on free port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

func waitForMetricsBody(t *testing.T, url string) string {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return string(body)
		}
		lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("metrics endpoint did not become ready: %v", lastErr)
	return ""
}
