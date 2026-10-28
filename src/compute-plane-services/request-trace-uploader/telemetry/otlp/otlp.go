// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package otlp pushes the telemetry instruments to an OpenTelemetry
// collector over OTLP. It is the only package that links the OpenTelemetry
// SDK and gRPC, so a consumer that records through telemetry.Metrics does
// not.
package otlp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

const (
	// EnvMetricsEndpoint and EnvMetricsProtocol are the only settings
	// the exporter reads. The generic OTEL_EXPORTER_OTLP_ENDPOINT is never a
	// fallback: it can describe the collector's own outbound path rather than
	// the path from this workload to the collector.
	EnvMetricsEndpoint = "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"
	EnvMetricsProtocol = "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"
	// MetricPrefix is prepended to every catalog name on export.
	MetricPrefix = "dynamo.request_trace."
	// ServiceName is the exported service.name resource attribute.
	ServiceName = "request-trace-uploader"
	// DefaultExportInterval is how often metrics are pushed.
	DefaultExportInterval = 60 * time.Second

	exportTimeout = 10 * time.Second
	readTimeout   = 30 * time.Second
	meterName     = "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// Protocol is the OTLP transport.
type Protocol string

const (
	ProtocolGRPC Protocol = "grpc"
	ProtocolHTTP Protocol = "http/protobuf"
)

// Config says where and how often metrics are pushed.
type Config struct {
	// Endpoint is used as given: an http or https URL with a host. For
	// http/protobuf its path is the request path, "/" when it has none.
	Endpoint string
	Protocol Protocol
	Interval time.Duration
}

// ConfigFromEnv reads the metrics endpoint and protocol. It reports
// enabled false, with no error, when the endpoint is unset or blank. With an
// endpoint, the protocol is required: grpc, http or http/protobuf.
func ConfigFromEnv(lookup func(string) (string, bool)) (Config, bool, error) {
	endpoint, _ := lookup(EnvMetricsEndpoint)
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return Config{}, false, nil
	}
	raw, _ := lookup(EnvMetricsProtocol)
	var protocol Protocol
	switch strings.TrimSpace(raw) {
	case "grpc":
		protocol = ProtocolGRPC
	case "http", "http/protobuf":
		protocol = ProtocolHTTP
	default:
		return Config{}, false, fmt.Errorf("%s must be grpc, http or http/protobuf when %s is set", EnvMetricsProtocol, EnvMetricsEndpoint)
	}
	cfg := Config{Endpoint: endpoint, Protocol: protocol, Interval: DefaultExportInterval}
	if err := cfg.Validate(); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

// Validate reports the first invalid setting.
func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an http or https URL with a host", EnvMetricsEndpoint)
	}
	if c.Protocol != ProtocolGRPC && c.Protocol != ProtocolHTTP {
		return fmt.Errorf("%s must be grpc, http or http/protobuf", EnvMetricsProtocol)
	}
	if c.Interval <= 0 {
		return errors.New("metrics export interval must be positive")
	}
	return nil
}

// Exporter records observations and pushes them over OTLP. Recording never
// blocks on the network: a periodic reader exports in the background, and a
// failed export costs that export only.
type Exporter struct {
	backend    attribute.KeyValue
	provider   *sdkmetric.MeterProvider
	counters   map[string]metric.Float64Counter
	gauges     map[string]metric.Float64Gauge
	histograms map[string]metric.Float64Histogram

	mu      sync.Mutex
	series  map[string]map[attribute.Distinct]bool
	dropped int
}

// New builds the exporter and presets every known counter to zero. Every
// exporter setting is passed explicitly, so no other OTEL_EXPORTER_OTLP_*
// variable changes where or how metrics are sent.
//
// It also replaces the process-wide OpenTelemetry error handler and internal
// logger, before the exporter reads its environment. The SDK's defaults print
// the collector URL, its response body, and the values of variables it
// failed to parse; these log a bounded reason, rate limited.
func New(ctx context.Context, cfg Config, backend string) (*Exporter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := telemetry.ValidLabelValue(backend); err != nil {
		return nil, err
	}
	otel.SetErrorHandler(newErrorHandler(errorLogInterval, time.Now))
	otel.SetLogger(logr.New(newSDKSink(errorLogInterval, time.Now)))
	exporter, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create OTLP metrics exporter: %w", err)
	}
	res, err := resource.New(ctx, resource.WithTelemetrySDK(), resource.WithAttributes(attribute.String("service.name", ServiceName)))
	if err != nil {
		return nil, fmt.Errorf("build metrics resource: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(cfg.Interval), sdkmetric.WithTimeout(readTimeout))
	e := &Exporter{
		backend:    attribute.String(telemetry.BackendLabel, backend),
		provider:   sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res)),
		counters:   map[string]metric.Float64Counter{},
		gauges:     map[string]metric.Float64Gauge{},
		histograms: map[string]metric.Float64Histogram{},
		series:     map[string]map[attribute.Distinct]bool{},
	}
	meter := e.provider.Meter(meterName)
	for _, instrument := range telemetry.Instruments() {
		name, unit := MetricPrefix+instrument.Name, metric.WithUnit(instrument.Unit)
		var err error
		switch instrument.Kind {
		case telemetry.Counter:
			e.counters[instrument.Name], err = meter.Float64Counter(name, unit)
		case telemetry.Gauge:
			e.gauges[instrument.Name], err = meter.Float64Gauge(name, unit)
		case telemetry.Histogram:
			e.histograms[instrument.Name], err = meter.Float64Histogram(name, unit, metric.WithExplicitBucketBoundaries(instrument.Buckets...))
		}
		if err != nil {
			_ = e.provider.Shutdown(ctx)
			return nil, fmt.Errorf("create instrument %s: %w", name, err)
		}
	}
	telemetry.Preset(e)
	return e, nil
}

// newExporter passes every setting the exporters would otherwise read from
// OTEL_EXPORTER_OTLP_* variables, so only the endpoint and protocol decide
// the export. TLS is set for both schemes: an http endpoint gets none, which
// also clears a certificate the environment named, and an https endpoint
// gets the system roots with no client certificate.
func newExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	var tlsConfig *tls.Config
	if strings.HasPrefix(cfg.Endpoint, "https://") {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if cfg.Protocol == ProtocolGRPC {
		transport := insecure.NewCredentials()
		if tlsConfig != nil {
			transport = credentials.NewTLS(tlsConfig)
		}
		return otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpointURL(cfg.Endpoint),
			otlpmetricgrpc.WithTLSCredentials(transport),
			otlpmetricgrpc.WithHeaders(map[string]string{}),
			otlpmetricgrpc.WithTimeout(exportTimeout),
			otlpmetricgrpc.WithCompressor(""),
			otlpmetricgrpc.WithTemporalitySelector(sdkmetric.DefaultTemporalitySelector),
			otlpmetricgrpc.WithAggregationSelector(sdkmetric.DefaultAggregationSelector),
		)
	}
	return otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(cfg.Endpoint),
		otlpmetrichttp.WithTLSClientConfig(tlsConfig),
		otlpmetrichttp.WithHeaders(map[string]string{}),
		otlpmetrichttp.WithTimeout(exportTimeout),
		otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
		otlpmetrichttp.WithTemporalitySelector(sdkmetric.DefaultTemporalitySelector),
		otlpmetrichttp.WithAggregationSelector(sdkmetric.DefaultAggregationSelector),
	)
}

func (e *Exporter) Add(name string, delta float64, labels telemetry.Labels) {
	if attrs, ok := e.admit(telemetry.Counter, name, delta, labels); ok {
		e.counters[name].Add(context.Background(), delta, metric.WithAttributeSet(attrs))
	}
}

func (e *Exporter) Set(name string, value float64, labels telemetry.Labels) {
	if attrs, ok := e.admit(telemetry.Gauge, name, value, labels); ok {
		e.gauges[name].Record(context.Background(), value, metric.WithAttributeSet(attrs))
	}
}

func (e *Exporter) Observe(name string, value float64, labels telemetry.Labels) {
	if attrs, ok := e.admit(telemetry.Histogram, name, value, labels); ok {
		e.histograms[name].Record(context.Background(), value, metric.WithAttributeSet(attrs))
	}
}

// admit applies the catalog rules and the series cap, as telemetry.Memory
// does, and returns the attributes to record with.
func (e *Exporter) admit(kind telemetry.Kind, name string, value float64, labels telemetry.Labels) (attribute.Set, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := telemetry.Validate(kind, name, value, labels); err != nil {
		e.dropped++
		return attribute.Set{}, false
	}
	attrs := make([]attribute.KeyValue, 0, len(labels)+1)
	attrs = append(attrs, e.backend)
	for k, v := range labels {
		attrs = append(attrs, attribute.String(k, v))
	}
	set := attribute.NewSet(attrs...)
	key := set.Equivalent()
	known := e.series[name]
	if !known[key] {
		if len(known) >= telemetry.MaxSeriesPerInstrument {
			e.dropped++
			return attribute.Set{}, false
		}
		if known == nil {
			known = map[attribute.Distinct]bool{}
			e.series[name] = known
		}
		known[key] = true
	}
	return set, true
}

// Flush exports everything recorded so far. An error names only a bounded
// reason, so it is safe to log.
func (e *Exporter) Flush(ctx context.Context) error { return exportError(e.provider.ForceFlush(ctx)) }

// Shutdown exports what is left and stops the exporter. ctx bounds it. An
// error names only a bounded reason, so it is safe to log.
func (e *Exporter) Shutdown(ctx context.Context) error { return exportError(e.provider.Shutdown(ctx)) }

// Dropped counts observations rejected by telemetry.Validate or the series
// cap.
func (e *Exporter) Dropped() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}
