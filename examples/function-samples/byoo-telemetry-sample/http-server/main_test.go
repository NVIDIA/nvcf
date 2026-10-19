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

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedExport struct {
	path        string
	contentType string
	body        []byte
}

type fakeSink struct {
	server *httptest.Server
	mu     sync.Mutex
	got    []capturedExport
}

func newFakeSink(t *testing.T) *fakeSink {
	t.Helper()
	sink := &fakeSink{}
	sink.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sink.mu.Lock()
		sink.got = append(sink.got, capturedExport{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: body})
		sink.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sink.server.Close)
	return sink
}

func (s *fakeSink) byPath() map[string][]capturedExport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]capturedExport{}
	for _, export := range s.got {
		out[export.path] = append(out[export.path], export)
	}
	return out
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func startSample(t *testing.T, sink *fakeSink) (*httptest.Server, *telemetry) {
	t.Helper()
	cfg, warnings := loadConfig(envMap(map[string]string{
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":    sink.server.URL + "/v1/logs",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL":    "http",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": sink.server.URL + "/v1/metrics",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": "http",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":  sink.server.URL + "/v1/traces",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL":  "http",
		"NVCF_FUNCTION_ID":                    "fn-123",
	}))
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	tel := newTelemetry(cfg, quietLogger())
	app := httptest.NewServer(newRouter(tel, quietLogger()))
	t.Cleanup(app.Close)
	return app, tel
}

func flush(t *testing.T, tel *telemetry) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tel.close(ctx); err != nil {
		t.Fatalf("flush telemetry: %v", err)
	}
}

func TestHealth(t *testing.T) {
	sink := newFakeSink(t)
	app, tel := startSample(t, sink)

	resp, err := http.Get(app.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Post(app.URL+"/health", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health = %d, want 405", resp.StatusCode)
	}

	flush(t, tel)
	if got := sink.byPath(); len(got) != 0 {
		t.Fatalf("health checks must not emit telemetry, got %v", got)
	}
}

func TestEchoEmitsAllSignals(t *testing.T) {
	sink := newFakeSink(t)
	app, tel := startSample(t, sink)

	const requests = 3
	var lastTraceID string
	for i := 0; i < requests; i++ {
		req, _ := http.NewRequest(http.MethodPost, app.URL+"/echo", strings.NewReader(`{"message":"hello"}`))
		req.Header.Set("NVCF-REQID", "req-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body echoResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || body.Message != "hello" || len(body.TraceID) != 32 {
			t.Fatalf("POST /echo = %d %+v", resp.StatusCode, body)
		}
		lastTraceID = body.TraceID
	}
	flush(t, tel)

	got := sink.byPath()
	for _, path := range []string{"/v1/logs", "/v1/metrics", "/v1/traces"} {
		if len(got[path]) != requests {
			t.Fatalf("%s exports = %d, want %d", path, len(got[path]), requests)
		}
		if ct := got[path][0].contentType; ct != "application/json" {
			t.Fatalf("%s content type = %q", path, ct)
		}
	}

	var traces tracesData
	mustUnmarshal(t, got["/v1/traces"][requests-1].body, &traces)
	sp := traces.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.TraceID != lastTraceID || len(sp.SpanID) != 16 || sp.Kind != spanKindServer || sp.Status.Code != spanStatusOK {
		t.Fatalf("unexpected span %+v", sp)
	}
	if !hasAttr(traces.ResourceSpans[0].Resource.Attributes, "service.name", defaultServiceName) ||
		!hasAttr(traces.ResourceSpans[0].Resource.Attributes, "nvcf.function_id", "fn-123") {
		t.Fatalf("unexpected resource %+v", traces.ResourceSpans[0].Resource)
	}
	if !hasAttr(sp.Attributes, "nvcf.request_id", "req-1") {
		t.Fatalf("span missing request id: %+v", sp.Attributes)
	}

	var logs logsData
	mustUnmarshal(t, got["/v1/logs"][requests-1].body, &logs)
	rec := logs.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.TraceID != lastTraceID || rec.SpanID != sp.SpanID || rec.SeverityNumber != severityInfo {
		t.Fatalf("log record not correlated with span: %+v", rec)
	}

	var metrics metricsData
	mustUnmarshal(t, got["/v1/metrics"][requests-1].body, &metrics)
	ms := metrics.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if len(ms) != 2 || ms[0].Name != metricRequests || ms[1].Name != metricRequestDuration {
		t.Fatalf("unexpected metrics %+v", ms)
	}
	if dp := ms[0].Sum.DataPoints[0]; dp.AsInt != "3" || !ms[0].Sum.IsMonotonic {
		t.Fatalf("request counter = %+v, want 3", dp)
	}
	if dp := ms[1].Histogram.DataPoints[0]; dp.Count != "3" || len(dp.BucketCounts) != len(durationBounds)+1 {
		t.Fatalf("duration histogram = %+v", dp)
	}
}

func TestEchoErrorsAreRecorded(t *testing.T) {
	sink := newFakeSink(t)
	app, tel := startSample(t, sink)

	resp, err := http.Post(app.URL+"/echo", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body = %d, want 400", resp.StatusCode)
	}
	resp, err = http.Get(app.URL + "/echo")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /echo = %d, want 405", resp.StatusCode)
	}
	flush(t, tel)

	got := sink.byPath()
	var traces tracesData
	mustUnmarshal(t, got["/v1/traces"][0].body, &traces)
	sp := traces.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.Status.Code != spanStatusError || !hasAttr(sp.Attributes, "error", "true") {
		t.Fatalf("error span = %+v", sp)
	}
	var metrics metricsData
	mustUnmarshal(t, got["/v1/metrics"][1].body, &metrics)
	if n := len(metrics.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].Sum.DataPoints); n != 2 {
		t.Fatalf("want one series per status code, got %d", n)
	}
}

func TestTraceParentIsPropagated(t *testing.T) {
	sink := newFakeSink(t)
	app, tel := startSample(t, sink)

	req, _ := http.NewRequest(http.MethodPost, app.URL+"/echo", strings.NewReader(`{"message":"x"}`))
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	flush(t, tel)

	var traces tracesData
	mustUnmarshal(t, sink.byPath()["/v1/traces"][0].body, &traces)
	sp := traces.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || sp.ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("traceparent not propagated: %+v", sp)
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantLogs     string
		wantMetrics  string
		wantTraces   string
		wantWarnings int
	}{
		{
			name:         "no endpoints disables all signals",
			env:          map[string]string{},
			wantWarnings: 3,
		},
		{
			name:        "generic endpoint gets signal paths",
			env:         map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318/"},
			wantLogs:    "http://collector:4318/v1/logs",
			wantMetrics: "http://collector:4318/v1/metrics",
			wantTraces:  "http://collector:4318/v1/traces",
		},
		{
			name: "per-signal endpoints win and grpc is rejected",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT":         "http://collector:4318",
				"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":    "http://logs:4318/v1/logs",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":  "http://traces:4317",
				"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL":  "grpc",
				"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": "http/protobuf",
			},
			wantLogs:     "http://logs:4318/v1/logs",
			wantMetrics:  "http://collector:4318/v1/metrics",
			wantWarnings: 1,
		},
		{
			name:         "endpoint without scheme is rejected",
			env:          map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4318"},
			wantWarnings: 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, warnings := loadConfig(envMap(tc.env))
			if cfg.logsEndpoint != tc.wantLogs || cfg.metricsEndpoint != tc.wantMetrics || cfg.tracesEndpoint != tc.wantTraces {
				t.Fatalf("endpoints = %q %q %q", cfg.logsEndpoint, cfg.metricsEndpoint, cfg.tracesEndpoint)
			}
			if len(warnings) != tc.wantWarnings {
				t.Fatalf("warnings = %v, want %d", warnings, tc.wantWarnings)
			}
			if cfg.serviceName != defaultServiceName {
				t.Fatalf("service name = %q", cfg.serviceName)
			}
		})
	}
}

func TestParseTraceParent(t *testing.T) {
	for header, want := range map[string]string{
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01": "4bf92f3577b34da6a3ce929d0e0e4736",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01": "",
		"00-xyz-00f067aa0ba902b7-01":                              "",
		"":                                                        "",
	} {
		if got, _ := parseTraceParent(header); got != want {
			t.Errorf("parseTraceParent(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestBucketIndex(t *testing.T) {
	if got := bucketIndex(0.001); got != 0 {
		t.Fatalf("bucketIndex(0.001) = %d", got)
	}
	if got := bucketIndex(0.3); got != 6 {
		t.Fatalf("bucketIndex(0.3) = %d", got)
	}
	if got := bucketIndex(60); got != len(durationBounds) {
		t.Fatalf("bucketIndex(60) = %d", got)
	}
}

func mustUnmarshal(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func hasAttr(attrs []keyValue, key, value string) bool {
	for _, attr := range attrs {
		if attr.Key == key && attr.Value.StringValue == value {
			return true
		}
	}
	return false
}
