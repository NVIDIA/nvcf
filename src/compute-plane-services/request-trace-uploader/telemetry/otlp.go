// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

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
)

const (
	// EnvOTLPMetricsEndpoint and EnvOTLPMetricsProtocol are the only settings
	// the exporter reads. The generic OTEL_EXPORTER_OTLP_ENDPOINT is never a
	// fallback: it can describe the collector's own outbound path rather than
	// the path from this workload to the collector.
	EnvOTLPMetricsEndpoint = "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"
	EnvOTLPMetricsProtocol = "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"
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

// OTLPConfig says where and how often metrics are pushed.
type OTLPConfig struct {
	// Endpoint is used as given: an http or https URL with a host. For
	// http/protobuf its path is the request path, "/" when it has none.
	Endpoint string
	Protocol Protocol
	Interval time.Duration
}

// OTLPConfigFromEnv reads the metrics endpoint and protocol. It reports
// enabled false, with no error, when the endpoint is unset or blank. With an
// endpoint, the protocol is required: grpc, http or http/protobuf.
func OTLPConfigFromEnv(lookup func(string) (string, bool)) (OTLPConfig, bool, error) {
	endpoint, _ := lookup(EnvOTLPMetricsEndpoint)
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return OTLPConfig{}, false, nil
	}
	raw, _ := lookup(EnvOTLPMetricsProtocol)
	var protocol Protocol
	switch strings.TrimSpace(raw) {
	case "grpc":
		protocol = ProtocolGRPC
	case "http", "http/protobuf":
		protocol = ProtocolHTTP
	default:
		return OTLPConfig{}, false, fmt.Errorf("%s must be grpc, http or http/protobuf when %s is set", EnvOTLPMetricsProtocol, EnvOTLPMetricsEndpoint)
	}
	cfg := OTLPConfig{Endpoint: endpoint, Protocol: protocol, Interval: DefaultExportInterval}
	if err := cfg.Validate(); err != nil {
		return OTLPConfig{}, false, err
	}
	return cfg, true, nil
}

// Validate reports the first invalid setting.
func (c OTLPConfig) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an http or https URL with a host", EnvOTLPMetricsEndpoint)
	}
	if c.Protocol != ProtocolGRPC && c.Protocol != ProtocolHTTP {
		return fmt.Errorf("%s must be grpc, http or http/protobuf", EnvOTLPMetricsProtocol)
	}
	if c.Interval <= 0 {
		return errors.New("metrics export interval must be positive")
	}
	return nil
}

// OTLP records observations and pushes them over OTLP. Recording never
// blocks on the network: a periodic reader exports in the background, and a
// failed export costs that export only.
type OTLP struct {
	backend    attribute.KeyValue
	provider   *sdkmetric.MeterProvider
	counters   map[string]metric.Float64Counter
	gauges     map[string]metric.Float64Gauge
	histograms map[string]metric.Float64Histogram

	mu      sync.Mutex
	series  map[string]map[string]bool
	dropped int
}

// NewOTLP builds the exporter and presets every known counter to zero. Every
// exporter setting is passed explicitly, so no other OTEL_EXPORTER_OTLP_*
// variable changes where or how metrics are sent.
//
// It also replaces the process-wide OpenTelemetry error handler and internal
// logger, before the exporter reads its environment. The SDK's defaults print
// the collector URL, its response body, and the values of variables it
// failed to parse; these log a bounded reason, rate limited.
func NewOTLP(ctx context.Context, cfg OTLPConfig, backend string) (*OTLP, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := ValidLabelValue(backend); err != nil {
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
	o := &OTLP{
		backend:    attribute.String(BackendLabel, backend),
		provider:   sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res)),
		counters:   map[string]metric.Float64Counter{},
		gauges:     map[string]metric.Float64Gauge{},
		histograms: map[string]metric.Float64Histogram{},
		series:     map[string]map[string]bool{},
	}
	meter := o.provider.Meter(meterName)
	for _, instrument := range catalog {
		name, unit := MetricPrefix+instrument.Name, metric.WithUnit(instrument.Unit)
		var err error
		switch instrument.Kind {
		case Counter:
			o.counters[instrument.Name], err = meter.Float64Counter(name, unit)
		case Gauge:
			o.gauges[instrument.Name], err = meter.Float64Gauge(name, unit)
		case Histogram:
			o.histograms[instrument.Name], err = meter.Float64Histogram(name, unit, metric.WithExplicitBucketBoundaries(instrument.Buckets...))
		}
		if err != nil {
			_ = o.provider.Shutdown(ctx)
			return nil, fmt.Errorf("create instrument %s: %w", name, err)
		}
	}
	Preset(o)
	return o, nil
}

// newExporter passes every setting the exporters would otherwise read from
// OTEL_EXPORTER_OTLP_* variables, so only the endpoint and protocol decide
// the export. TLS is set for both schemes: an http endpoint gets none, which
// also clears a certificate the environment named, and an https endpoint
// gets the system roots with no client certificate.
func newExporter(ctx context.Context, cfg OTLPConfig) (sdkmetric.Exporter, error) {
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

func (o *OTLP) Add(name string, delta float64, labels Labels) {
	if attrs, ok := o.admit(Counter, name, delta, labels); ok {
		o.counters[name].Add(context.Background(), delta, metric.WithAttributes(attrs...))
	}
}

func (o *OTLP) Set(name string, value float64, labels Labels) {
	if attrs, ok := o.admit(Gauge, name, value, labels); ok {
		o.gauges[name].Record(context.Background(), value, metric.WithAttributes(attrs...))
	}
}

func (o *OTLP) Observe(name string, value float64, labels Labels) {
	if attrs, ok := o.admit(Histogram, name, value, labels); ok {
		o.histograms[name].Record(context.Background(), value, metric.WithAttributes(attrs...))
	}
}

// admit applies the catalog rules and the series cap, as Memory does, and
// returns the attributes to record with.
func (o *OTLP) admit(kind Kind, name string, value float64, labels Labels) ([]attribute.KeyValue, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := Validate(kind, name, value, labels); err != nil {
		o.dropped++
		return nil, false
	}
	key := seriesKey(labels)
	known := o.series[name]
	if !known[key] {
		if len(known) >= MaxSeriesPerInstrument {
			o.dropped++
			return nil, false
		}
		if known == nil {
			known = map[string]bool{}
			o.series[name] = known
		}
		known[key] = true
	}
	attrs := make([]attribute.KeyValue, 0, len(labels)+1)
	attrs = append(attrs, o.backend)
	for k, v := range labels {
		attrs = append(attrs, attribute.String(k, v))
	}
	return attrs, true
}

// Flush exports everything recorded so far. An error names only a bounded
// reason, so it is safe to log.
func (o *OTLP) Flush(ctx context.Context) error { return exportError(o.provider.ForceFlush(ctx)) }

// Shutdown exports what is left and stops the exporter. ctx bounds it. An
// error names only a bounded reason, so it is safe to log.
func (o *OTLP) Shutdown(ctx context.Context) error { return exportError(o.provider.Shutdown(ctx)) }

// Dropped counts observations rejected by Validate or the series cap.
func (o *OTLP) Dropped() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dropped
}
