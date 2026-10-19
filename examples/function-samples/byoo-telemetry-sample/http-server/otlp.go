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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultServiceName = "byoo-telemetry-sample"
	scopeName          = "nvcf-byoo-telemetry-sample"
	exportQueueSize    = 1024
	exportTimeout      = 5 * time.Second

	metricRequests        = "byoo_sample_requests_total"
	metricRequestDuration = "byoo_sample_request_duration_seconds"

	// OTLP enum values from opentelemetry-proto.
	spanKindServer            = 2
	spanStatusOK              = 1
	spanStatusError           = 2
	severityInfo              = 9
	severityWarn              = 13
	aggregationCumulative     = 2
	signalLogs                = "logs"
	signalMetrics             = "metrics"
	signalTraces              = "traces"
	otlpHTTPContentTypeHeader = "application/json"
)

var durationBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// config holds the resolved OTLP/HTTP endpoint for each signal. An empty
// endpoint disables that signal.
type config struct {
	serviceName       string
	resourceAttrs     map[string]string
	logsEndpoint      string
	metricsEndpoint   string
	tracesEndpoint    string
	exportHTTPTimeout time.Duration
}

// loadConfig reads the standard OTEL_EXPORTER_OTLP_* variables. NVCF BYOO
// injects the per-signal OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT and _PROTOCOL
// variables; the generic OTEL_EXPORTER_OTLP_ENDPOINT is supported for local
// runs and gets the /v1/<signal> path appended as the OTel spec requires.
func loadConfig(getenv func(string) string) (config, []string) {
	cfg := config{
		serviceName:       strings.TrimSpace(getenv("OTEL_SERVICE_NAME")),
		resourceAttrs:     map[string]string{},
		exportHTTPTimeout: exportTimeout,
	}
	if cfg.serviceName == "" {
		cfg.serviceName = defaultServiceName
	}
	for _, name := range []string{"NVCF_FUNCTION_ID", "NVCF_FUNCTION_VERSION_ID", "NVCF_INSTANCE_ID"} {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			cfg.resourceAttrs["nvcf."+strings.ToLower(strings.TrimPrefix(name, "NVCF_"))] = value
		}
	}

	var warnings []string
	resolve := func(signal string) string {
		upper := strings.ToUpper(signal)
		protocol := strings.ToLower(strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_" + upper + "_PROTOCOL")))
		if protocol == "" {
			protocol = strings.ToLower(strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_PROTOCOL")))
		}
		endpoint := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_" + upper + "_ENDPOINT"))
		if endpoint == "" {
			if base := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); base != "" {
				endpoint = strings.TrimRight(base, "/") + "/v1/" + signal
			}
		}
		if endpoint == "" {
			warnings = append(warnings, fmt.Sprintf("no OTLP endpoint for %s; signal disabled", signal))
			return ""
		}
		if protocol != "" && !strings.HasPrefix(protocol, "http") {
			warnings = append(warnings, fmt.Sprintf(
				"OTLP protocol %q for %s is not supported by this sample; register the telemetry with protocol HTTP; signal disabled",
				protocol, signal))
			return ""
		}
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			warnings = append(warnings, fmt.Sprintf("OTLP endpoint for %s must start with http:// or https://; signal disabled", signal))
			return ""
		}
		return endpoint
	}
	cfg.logsEndpoint = resolve(signalLogs)
	cfg.metricsEndpoint = resolve(signalMetrics)
	cfg.tracesEndpoint = resolve(signalTraces)
	return cfg, warnings
}

// requestRecord describes one handled request.
type requestRecord struct {
	route        string
	method       string
	status       int
	requestID    string
	traceID      string
	spanID       string
	parentSpanID string
	message      string
	start        time.Time
	end          time.Time
}

type seriesKey struct {
	route  string
	status int
}

type durationSeries struct {
	count   uint64
	sum     float64
	buckets []uint64
}

type exportJob struct {
	signal string
	url    string
	body   []byte
}

// telemetry turns request records into OTLP/HTTP JSON payloads and sends them
// from a single background worker so request latency does not depend on the
// sink.
type telemetry struct {
	cfg       config
	logger    *slog.Logger
	client    *http.Client
	startTime time.Time
	resource  resource

	mu        sync.Mutex
	requests  map[seriesKey]uint64
	durations map[seriesKey]*durationSeries

	queue chan exportJob
	done  chan struct{}
}

func newTelemetry(cfg config, logger *slog.Logger) *telemetry {
	attrs := []keyValue{kv("service.name", cfg.serviceName)}
	keys := make([]string, 0, len(cfg.resourceAttrs))
	for key := range cfg.resourceAttrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		attrs = append(attrs, kv(key, cfg.resourceAttrs[key]))
	}
	if host, err := os.Hostname(); err == nil {
		attrs = append(attrs, kv("host.name", host))
	}
	t := &telemetry{
		cfg:       cfg,
		logger:    logger,
		client:    &http.Client{Timeout: cfg.exportHTTPTimeout},
		startTime: time.Now(),
		resource:  resource{Attributes: attrs},
		requests:  map[seriesKey]uint64{},
		durations: map[seriesKey]*durationSeries{},
		queue:     make(chan exportJob, exportQueueSize),
		done:      make(chan struct{}),
	}
	go t.run()
	return t
}

// record emits a span, a log record, and a cumulative metrics snapshot for rec.
func (t *telemetry) record(rec requestRecord) {
	snapshot := t.observe(rec)
	t.enqueue(signalTraces, t.cfg.tracesEndpoint, t.tracesPayload(rec))
	t.enqueue(signalLogs, t.cfg.logsEndpoint, t.logsPayload(rec))
	t.enqueue(signalMetrics, t.cfg.metricsEndpoint, snapshot)
}

// close stops accepting records and waits for queued exports to finish.
func (t *telemetry) close(ctx context.Context) error {
	close(t.queue)
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *telemetry) enqueue(signal, url string, payload any) {
	if url == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.logger.Error("encode OTLP payload", "signal", signal, "error", err)
		return
	}
	select {
	case t.queue <- exportJob{signal: signal, url: url, body: body}:
	default:
		t.logger.Warn("OTLP export queue full; dropping payload", "signal", signal)
	}
}

func (t *telemetry) run() {
	defer close(t.done)
	for job := range t.queue {
		if err := t.send(job); err != nil {
			t.logger.Warn("OTLP export failed", "signal", job.signal, "error", err)
		}
	}
}

func (t *telemetry) send(job exportJob) error {
	ctx, cancel := context.WithTimeout(context.Background(), t.cfg.exportHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", otlpHTTPContentTypeHeader)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("sink returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// observe updates the cumulative series and returns a metrics payload with
// every series, so each request exports a complete snapshot.
func (t *telemetry) observe(rec requestRecord) metricsData {
	key := seriesKey{route: rec.route, status: rec.status}
	seconds := rec.end.Sub(rec.start).Seconds()
	now := unixNano(rec.end)
	start := unixNano(t.startTime)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests[key]++
	series := t.durations[key]
	if series == nil {
		series = &durationSeries{buckets: make([]uint64, len(durationBounds)+1)}
		t.durations[key] = series
	}
	series.count++
	series.sum += seconds
	series.buckets[bucketIndex(seconds)]++

	keys := make([]seriesKey, 0, len(t.requests))
	for k := range t.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].status < keys[j].status
	})

	counter := sum{AggregationTemporality: aggregationCumulative, IsMonotonic: true}
	hist := histogram{AggregationTemporality: aggregationCumulative}
	for _, k := range keys {
		attrs := []keyValue{kv("http.route", k.route), kv("http.response.status_code", strconv.Itoa(k.status))}
		counter.DataPoints = append(counter.DataPoints, numberDataPoint{
			Attributes: attrs, StartTimeUnixNano: start, TimeUnixNano: now,
			AsInt: strconv.FormatUint(t.requests[k], 10),
		})
		d := t.durations[k]
		buckets := make([]string, len(d.buckets))
		for i, count := range d.buckets {
			buckets[i] = strconv.FormatUint(count, 10)
		}
		total := d.sum
		hist.DataPoints = append(hist.DataPoints, histogramDataPoint{
			Attributes: attrs, StartTimeUnixNano: start, TimeUnixNano: now,
			Count: strconv.FormatUint(d.count, 10), Sum: &total,
			BucketCounts: buckets, ExplicitBounds: durationBounds,
		})
	}
	return metricsData{ResourceMetrics: []resourceMetrics{{
		Resource: t.resource,
		ScopeMetrics: []scopeMetrics{{
			Scope: scope{Name: scopeName},
			Metrics: []metric{
				{Name: metricRequests, Description: "Inference requests handled.", Unit: "1", Sum: &counter},
				{Name: metricRequestDuration, Description: "Inference request duration.", Unit: "s", Histogram: &hist},
			},
		}},
	}}}
}

func (t *telemetry) tracesPayload(rec requestRecord) tracesData {
	status := spanStatus{Code: spanStatusOK}
	attrs := rec.attributes()
	if rec.status >= http.StatusBadRequest {
		status = spanStatus{Code: spanStatusError, Message: http.StatusText(rec.status)}
		attrs = append(attrs, kv("error", "true"))
	}
	return tracesData{ResourceSpans: []resourceSpans{{
		Resource: t.resource,
		ScopeSpans: []scopeSpans{{
			Scope: scope{Name: scopeName},
			Spans: []span{{
				TraceID:           rec.traceID,
				SpanID:            rec.spanID,
				ParentSpanID:      rec.parentSpanID,
				Name:              "byoo_sample.echo",
				Kind:              spanKindServer,
				StartTimeUnixNano: unixNano(rec.start),
				EndTimeUnixNano:   unixNano(rec.end),
				Attributes:        attrs,
				Status:            status,
			}},
		}},
	}}}
}

func (t *telemetry) logsPayload(rec requestRecord) logsData {
	severity, text := severityInfo, "INFO"
	if rec.status >= http.StatusBadRequest {
		severity, text = severityWarn, "WARN"
	}
	body := fmt.Sprintf("%s %s returned %d", rec.method, rec.route, rec.status)
	attrs := rec.attributes()
	if rec.message != "" {
		attrs = append(attrs, kv("echo.message_length", strconv.Itoa(len(rec.message))))
	}
	return logsData{ResourceLogs: []resourceLogs{{
		Resource: t.resource,
		ScopeLogs: []scopeLogs{{
			Scope: scope{Name: scopeName},
			LogRecords: []logRecord{{
				TimeUnixNano:         unixNano(rec.end),
				ObservedTimeUnixNano: unixNano(time.Now()),
				SeverityNumber:       severity,
				SeverityText:         text,
				Body:                 anyValue{StringValue: body},
				Attributes:           attrs,
				TraceID:              rec.traceID,
				SpanID:               rec.spanID,
			}},
		}},
	}}}
}

func (rec requestRecord) attributes() []keyValue {
	attrs := []keyValue{
		kv("http.request.method", rec.method),
		kv("http.route", rec.route),
		kv("http.response.status_code", strconv.Itoa(rec.status)),
	}
	if rec.requestID != "" {
		attrs = append(attrs, kv("nvcf.request_id", rec.requestID))
	}
	return attrs
}

func bucketIndex(seconds float64) int {
	for i, bound := range durationBounds {
		if seconds <= bound {
			return i
		}
	}
	return len(durationBounds)
}

// parseTraceParent returns the trace ID and parent span ID from a W3C
// traceparent header, or empty strings when the header is absent or invalid.
func parseTraceParent(header string) (string, string) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return "", ""
	}
	traceID, spanID := strings.ToLower(parts[1]), strings.ToLower(parts[2])
	if !isHex(traceID) || !isHex(spanID) || strings.Trim(traceID, "0") == "" || strings.Trim(spanID, "0") == "" {
		return "", ""
	}
	return traceID, spanID
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func unixNano(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

func kv(key, value string) keyValue {
	return keyValue{Key: key, Value: anyValue{StringValue: value}}
}

// OTLP/HTTP JSON encoding, following opentelemetry-proto. 64-bit integers are
// encoded as strings and IDs as lowercase hex, as the OTLP JSON mapping requires.

type anyValue struct {
	StringValue string `json:"stringValue"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type resource struct {
	Attributes []keyValue `json:"attributes"`
}

type scope struct {
	Name string `json:"name"`
}

type tracesData struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

type resourceSpans struct {
	Resource   resource     `json:"resource"`
	ScopeSpans []scopeSpans `json:"scopeSpans"`
}

type scopeSpans struct {
	Scope scope  `json:"scope"`
	Spans []span `json:"spans"`
}

type span struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []keyValue `json:"attributes"`
	Status            spanStatus `json:"status"`
}

type spanStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type logsData struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

type resourceLogs struct {
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type scopeLogs struct {
	Scope      scope       `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type logRecord struct {
	TimeUnixNano         string     `json:"timeUnixNano"`
	ObservedTimeUnixNano string     `json:"observedTimeUnixNano"`
	SeverityNumber       int        `json:"severityNumber"`
	SeverityText         string     `json:"severityText"`
	Body                 anyValue   `json:"body"`
	Attributes           []keyValue `json:"attributes"`
	TraceID              string     `json:"traceId"`
	SpanID               string     `json:"spanId"`
}

type metricsData struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

type resourceMetrics struct {
	Resource     resource       `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type scopeMetrics struct {
	Scope   scope    `json:"scope"`
	Metrics []metric `json:"metrics"`
}

type metric struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Unit        string     `json:"unit"`
	Sum         *sum       `json:"sum,omitempty"`
	Histogram   *histogram `json:"histogram,omitempty"`
}

type sum struct {
	DataPoints             []numberDataPoint `json:"dataPoints"`
	AggregationTemporality int               `json:"aggregationTemporality"`
	IsMonotonic            bool              `json:"isMonotonic"`
}

type numberDataPoint struct {
	Attributes        []keyValue `json:"attributes"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	TimeUnixNano      string     `json:"timeUnixNano"`
	AsInt             string     `json:"asInt"`
}

type histogram struct {
	DataPoints             []histogramDataPoint `json:"dataPoints"`
	AggregationTemporality int                  `json:"aggregationTemporality"`
}

type histogramDataPoint struct {
	Attributes        []keyValue `json:"attributes"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	TimeUnixNano      string     `json:"timeUnixNano"`
	Count             string     `json:"count"`
	Sum               *float64   `json:"sum,omitempty"`
	BucketCounts      []string   `json:"bucketCounts"`
	ExplicitBounds    []float64  `json:"explicitBounds"`
}
