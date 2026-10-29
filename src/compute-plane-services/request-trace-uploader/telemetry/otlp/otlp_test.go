// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

func envLookup(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
}

func TestConfigFromEnvIsOffWithoutTheMetricsEndpoint(t *testing.T) {
	_, enabled, err := ConfigFromEnv(envLookup(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
	}))
	if err != nil || enabled {
		t.Fatalf("ConfigFromEnv() enabled = %v error = %v, want off with no error", enabled, err)
	}
	_, enabled, err = ConfigFromEnv(envLookup(map[string]string{EnvMetricsEndpoint: "  "}))
	if err != nil || enabled {
		t.Fatalf("ConfigFromEnv() blank endpoint enabled = %v error = %v, want off", enabled, err)
	}
}

func TestConfigFromEnvReadsEndpointAndProtocol(t *testing.T) {
	tests := map[string]Protocol{
		"grpc":          ProtocolGRPC,
		"http":          ProtocolHTTP,
		"http/protobuf": ProtocolHTTP,
	}
	for value, want := range tests {
		t.Run(value, func(t *testing.T) {
			cfg, enabled, err := ConfigFromEnv(envLookup(map[string]string{
				EnvMetricsEndpoint: "https://collector.example:4318/v1/metrics",
				EnvMetricsProtocol: value,
			}))
			if err != nil || !enabled {
				t.Fatalf("ConfigFromEnv() enabled = %v error = %v", enabled, err)
			}
			if cfg.Protocol != want || cfg.Endpoint != "https://collector.example:4318/v1/metrics" {
				t.Fatalf("cfg = %+v, want protocol %q and the endpoint as given", cfg, want)
			}
			if cfg.Interval != DefaultExportInterval {
				t.Fatalf("Interval = %v, want %v", cfg.Interval, DefaultExportInterval)
			}
		})
	}
}

func TestConfigFromEnvRejectsBadValues(t *testing.T) {
	tests := map[string]struct {
		env   map[string]string
		named string
	}{
		"protocol missing":     {map[string]string{EnvMetricsEndpoint: "http://collector:4318/v1/metrics"}, EnvMetricsProtocol},
		"protocol unsupported": {map[string]string{EnvMetricsEndpoint: "http://collector:4318/v1/metrics", EnvMetricsProtocol: "http/json"}, EnvMetricsProtocol},
		"endpoint no scheme":   {map[string]string{EnvMetricsEndpoint: "collector:4317", EnvMetricsProtocol: "grpc"}, EnvMetricsEndpoint},
		"endpoint bad scheme":  {map[string]string{EnvMetricsEndpoint: "ftp://collector:4317", EnvMetricsProtocol: "grpc"}, EnvMetricsEndpoint},
		"endpoint no host":     {map[string]string{EnvMetricsEndpoint: "http:///v1/metrics", EnvMetricsProtocol: "http"}, EnvMetricsEndpoint},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := ConfigFromEnv(envLookup(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.named) {
				t.Fatalf("ConfigFromEnv() error = %v, want it to name %s", err, tt.named)
			}
		})
	}
}

// receiver collects OTLP metric exports over either protocol.
type receiver struct {
	colmetricpb.UnimplementedMetricsServiceServer

	mu       sync.Mutex
	requests []*colmetricpb.ExportMetricsServiceRequest
	headers  []map[string][]string
	paths    []string
}

func (r *receiver) record(req *colmetricpb.ExportMetricsServiceRequest, headers map[string][]string, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	r.headers = append(r.headers, headers)
	r.paths = append(r.paths, path)
}

func (r *receiver) Export(ctx context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	r.record(req, md, "")
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var export colmetricpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &export); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	headers := map[string][]string{}
	for k, v := range req.Header {
		headers[strings.ToLower(k)] = v
	}
	r.record(&export, headers, req.URL.Path)
	out, _ := proto.Marshal(&colmetricpb.ExportMetricsServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// metric returns the last exported metric named name.
func (r *receiver) metric(name string) *metricpb.Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found *metricpb.Metric
	for _, req := range r.requests {
		for _, rm := range req.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					if m.GetName() == name {
						found = m
					}
				}
			}
		}
	}
	return found
}

func (r *receiver) resourceAttribute(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.requests {
		for _, rm := range req.GetResourceMetrics() {
			for _, kv := range rm.GetResource().GetAttributes() {
				if kv.GetKey() == key {
					return kv.GetValue().GetStringValue()
				}
			}
		}
	}
	return ""
}

func httpReceiver(t *testing.T) (*receiver, string) {
	t.Helper()
	r := &receiver{}
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	return r, server.URL + "/v1/metrics"
}

func grpcReceiver(t *testing.T) (*receiver, string) {
	t.Helper()
	r := &receiver{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	colmetricpb.RegisterMetricsServiceServer(server, r)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return r, "http://" + listener.Addr().String()
}

func newOTLP(t *testing.T, endpoint string, protocol Protocol) *Exporter {
	t.Helper()
	exporter, err := New(context.Background(), Config{Endpoint: endpoint, Protocol: protocol, Interval: time.Hour}, "fake")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = exporter.Shutdown(ctx)
	})
	return exporter
}

func flush(t *testing.T, exporter *Exporter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exporter.Flush(ctx); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
}

func attributes(kvs interface {
	GetAttributes() []*commonpb.KeyValue
}) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs.GetAttributes() {
		out[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	return out
}

func TestOTLPDeliversEveryKindOverEachProtocol(t *testing.T) {
	receivers := map[Protocol]func(*testing.T) (*receiver, string){
		ProtocolHTTP: httpReceiver,
		ProtocolGRPC: grpcReceiver,
	}
	for protocol, start := range receivers {
		t.Run(string(protocol), func(t *testing.T) {
			r, endpoint := start(t)
			exporter := newOTLP(t, endpoint, protocol)

			exporter.Add(telemetry.Uploads, 2, telemetry.Labels{"status": "success"})
			exporter.Set(telemetry.PendingSegments, 7, nil)
			exporter.Observe(telemetry.UploadDuration, 1.5, nil)
			flush(t, exporter)

			uploads := r.metric(MetricPrefix + telemetry.Uploads)
			if uploads == nil || uploads.GetSum() == nil || !uploads.GetSum().GetIsMonotonic() {
				t.Fatalf("uploads = %v, want a monotonic sum", uploads)
			}
			var success *metricpb.NumberDataPoint
			for _, point := range uploads.GetSum().GetDataPoints() {
				if attributes(point)["status"] == "success" {
					success = point
				}
			}
			if success == nil || success.GetAsDouble() != 2 || attributes(success)[telemetry.BackendLabel] != "fake" {
				t.Fatalf("uploads{status=success} = %v, want 2 with backend=fake", success)
			}
			pending := r.metric(MetricPrefix + telemetry.PendingSegments)
			if pending == nil || pending.GetGauge() == nil || pending.GetGauge().GetDataPoints()[0].GetAsDouble() != 7 {
				t.Fatalf("pending_segments = %v, want a gauge of 7", pending)
			}
			duration := r.metric(MetricPrefix + telemetry.UploadDuration)
			if duration == nil || duration.GetHistogram() == nil || duration.GetUnit() != "s" {
				t.Fatalf("upload.duration = %v, want a histogram in s", duration)
			}
			if point := duration.GetHistogram().GetDataPoints()[0]; point.GetCount() != 1 || point.GetSum() != 1.5 {
				t.Fatalf("upload.duration point = %v, want count 1 sum 1.5", point)
			}
			if got := r.resourceAttribute("service.name"); got != ServiceName {
				t.Fatalf("service.name = %q, want %q", got, ServiceName)
			}
		})
	}
}

func TestOTLPExportsPresetCountersAtZero(t *testing.T) {
	r, endpoint := httpReceiver(t)
	exporter := newOTLP(t, endpoint, ProtocolHTTP)
	flush(t, exporter)

	ambiguous := r.metric(MetricPrefix + telemetry.SubmitsAmbiguous)
	if ambiguous == nil || len(ambiguous.GetSum().GetDataPoints()) != 1 || ambiguous.GetSum().GetDataPoints()[0].GetAsDouble() != 0 {
		t.Fatalf("submits_ambiguous = %v, want one point at 0 before any traffic", ambiguous)
	}
}

func TestOTLPIgnoresTheGenericExporterEnvironment(t *testing.T) {
	decoy, decoyEndpoint := httpReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", strings.TrimSuffix(decoyEndpoint, "/v1/metrics"))
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-from-env=1")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "x-from-metrics-env=1")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", "delta")

	r, endpoint := httpReceiver(t)
	exporter := newOTLP(t, endpoint, ProtocolHTTP)
	exporter.Add(telemetry.SubmitsAmbiguous, 1, nil)
	flush(t, exporter)
	exporter.Add(telemetry.SubmitsAmbiguous, 1, nil)
	flush(t, exporter)

	if decoy.count() != 0 {
		t.Fatalf("the generic endpoint got %d exports, want none", decoy.count())
	}
	r.mu.Lock()
	for i, headers := range r.headers {
		if _, ok := headers["x-from-env"]; ok {
			t.Errorf("export %d carries a header from OTEL_EXPORTER_OTLP_HEADERS", i)
		}
		if _, ok := headers["x-from-metrics-env"]; ok {
			t.Errorf("export %d carries a header from OTEL_EXPORTER_OTLP_METRICS_HEADERS", i)
		}
		if r.paths[i] != "/v1/metrics" {
			t.Errorf("export %d went to %q, want the configured path", i, r.paths[i])
		}
	}
	r.mu.Unlock()
	sum := r.metric(MetricPrefix + telemetry.SubmitsAmbiguous).GetSum()
	if sum.GetAggregationTemporality() != metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE || sum.GetDataPoints()[0].GetAsDouble() != 2 {
		t.Fatalf("submits_ambiguous = %v, want a cumulative 2", sum)
	}
}

func TestOTLPDropsInvalidObservations(t *testing.T) {
	r, endpoint := httpReceiver(t)
	exporter := newOTLP(t, endpoint, ProtocolHTTP)

	exporter.Add(telemetry.RecordsUnreadable, 1, telemetry.Labels{"reason": "/var/spool/request-trace.000001.jsonl.gz"})
	exporter.Add("made_up", 1, nil)
	exporter.Set(telemetry.Uploads, 1, telemetry.Labels{"status": "success"})
	for i := 0; i < telemetry.MaxSeriesPerInstrument+3; i++ {
		exporter.Add(telemetry.RecordsUnreadable, 1, telemetry.Labels{"reason": "reason_" + strings.Repeat("a", i%10) + string(rune('a'+i/10))})
	}
	flush(t, exporter)

	if exporter.Dropped() != 6 {
		t.Fatalf("Dropped() = %d, want 6", exporter.Dropped())
	}
	unreadable := r.metric(MetricPrefix + telemetry.RecordsUnreadable)
	if got := len(unreadable.GetSum().GetDataPoints()); got != telemetry.MaxSeriesPerInstrument {
		t.Fatalf("records_unreadable series = %d, want the cap %d", got, telemetry.MaxSeriesPerInstrument)
	}
	for _, point := range unreadable.GetSum().GetDataPoints() {
		if strings.Contains(attributes(point)["reason"], "/") {
			t.Fatalf("an unbounded label value was exported: %v", point)
		}
	}
	if r.metric(MetricPrefix+"made_up") != nil {
		t.Fatal("an unknown instrument was exported")
	}
}

func TestOTLPNeverBlocksOnADeadCollector(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := "http://" + listener.Addr().String() + "/v1/metrics"
	_ = listener.Close()
	exporter, err := New(context.Background(), Config{Endpoint: dead, Protocol: ProtocolHTTP, Interval: 10 * time.Millisecond}, "fake")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	started := time.Now()
	for i := 0; i < 1000; i++ {
		exporter.Add(telemetry.SubmitsAmbiguous, 1, nil)
		exporter.Set(telemetry.PendingSegments, float64(i), nil)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("recording took %v with a dead collector, want it immediate", elapsed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started = time.Now()
	_ = exporter.Shutdown(ctx)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %v, want it bounded by its context", elapsed)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := map[string]struct {
		cfg     Config
		backend string
	}{
		"no endpoint":      {Config{Protocol: ProtocolHTTP, Interval: time.Minute}, "fake"},
		"unknown protocol": {Config{Endpoint: "http://collector:4318/v1/metrics", Protocol: "http/json", Interval: time.Minute}, "fake"},
		"no interval":      {Config{Endpoint: "http://collector:4318/v1/metrics", Protocol: ProtocolHTTP}, "fake"},
		"bad backend":      {Config{Endpoint: "http://collector:4318/v1/metrics", Protocol: ProtocolHTTP, Interval: time.Minute}, "Not A Token"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(context.Background(), tt.cfg, tt.backend); err == nil {
				t.Fatal("New() error = nil, want error")
			}
		})
	}
}

func TestOTLPExportFailureLogsNoCollectorDetails(t *testing.T) {
	logs := captureDefaultLog(t)
	const pathMarker, bodyMarker = "/v1/metrics-path-marker", "body-marker-7f3a"
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, bodyMarker, http.StatusBadRequest)
	}))
	defer collector.Close()
	exporter, err := New(context.Background(), Config{Endpoint: collector.URL + pathMarker, Protocol: ProtocolHTTP, Interval: 20 * time.Millisecond}, "fake")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = exporter.Shutdown(context.Background()) }()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), `"reason":"error"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no export failure logged within 5 s\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	flushErr := exporter.Flush(context.Background())
	if flushErr == nil {
		t.Fatal("Flush() error = nil against a collector that answers 400")
	}

	host := strings.TrimPrefix(collector.URL, "http://")
	for name, text := range map[string]string{"log": logs.String(), "Flush error": flushErr.Error()} {
		for _, leak := range []string{host, pathMarker, bodyMarker, "Bad Request"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s = %s, contains %q", name, text, leak)
			}
		}
	}
}
