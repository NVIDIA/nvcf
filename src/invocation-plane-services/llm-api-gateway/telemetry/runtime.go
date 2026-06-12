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
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

type RuntimeConfig struct {
	MetricsPort int
}

type Runtime struct {
	tracerProvider  *sdktrace.TracerProvider
	meterProvider   *sdkmetric.MeterProvider
	metricsGatherer prometheus.Gatherer
	metricsServer   *http.Server
}

func InitFromEnv(ctx context.Context) (*Runtime, error) {
	metricsPort := 9464
	if raw := os.Getenv("METRICS_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid METRICS_PORT=%q: %w", raw, err)
		}
		metricsPort = port
	}
	return Init(ctx, RuntimeConfig{MetricsPort: metricsPort})
}

func Init(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	runtime := &Runtime{}

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	if err := runtime.initTracing(ctx); err != nil {
		return nil, err
	}
	if err := runtime.initMetrics(ctx, cfg); err != nil {
		return nil, err
	}

	return runtime, nil
}

func (r *Runtime) initTracing(ctx context.Context) error {
	if exporter := os.Getenv("OTEL_TRACES_EXPORTER"); exporter != "" && exporter != "none" {
		resource, err := newResource()
		if err != nil {
			return err
		}

		traceProvider, err := newTracerProvider(ctx, exporter, resource)
		if err != nil {
			return err
		}
		r.tracerProvider = traceProvider
		otel.SetTracerProvider(traceProvider)
	}

	return nil
}

func (r *Runtime) initMetrics(ctx context.Context, cfg RuntimeConfig) error {
	metricsExporter := os.Getenv("OTEL_METRICS_EXPORTER")
	if cfg.MetricsPort <= 0 && (metricsExporter == "" || metricsExporter == "none") {
		return nil
	}

	resource, err := newResource()
	if err != nil {
		return err
	}

	meterProvider, gatherer, err := newMeterProvider(ctx, metricsExporter, resource, cfg.MetricsPort > 0)
	if err != nil {
		return err
	}
	r.meterProvider = meterProvider
	r.metricsGatherer = gatherer
	otel.SetMeterProvider(meterProvider)
	InitializeMetrics()

	if cfg.MetricsPort <= 0 {
		return nil
	}

	metricsServer, err := startMetricsServer(cfg.MetricsPort, gatherer)
	if err != nil {
		_ = meterProvider.Shutdown(ctx)
		return err
	}
	r.metricsServer = metricsServer
	return nil
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}

	var errs []error
	if r.metricsServer != nil {
		errs = append(errs, r.metricsServer.Shutdown(ctx))
	}
	if r.meterProvider != nil {
		errs = append(errs, r.meterProvider.Shutdown(ctx))
	}
	if r.tracerProvider != nil {
		errs = append(errs, r.tracerProvider.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

func newResource() (*resource.Resource, error) {
	return resource.New(
		context.Background(),
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(ServiceName())),
		resource.WithTelemetrySDK(),
		resource.WithContainerID(),
	)
}

func newTracerProvider(
	ctx context.Context,
	exporter string,
	resource *resource.Resource,
) (*sdktrace.TracerProvider, error) {
	var (
		spanExporter sdktrace.SpanExporter
		err          error
	)

	switch exporter {
	case "otlp":
		switch otlpProtocol() {
		case "", "grpc":
			spanExporter, err = otlptracegrpc.New(ctx)
		case "http", "http/protobuf":
			spanExporter, err = otlptracehttp.New(ctx)
		default:
			return nil, errors.New("unsupported OTEL_EXPORTER_OTLP_PROTOCOL: " + otlpProtocol())
		}
	case "stdout":
		spanExporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	default:
		return nil, errors.New("unsupported OTEL_TRACES_EXPORTER: " + exporter)
	}
	if err != nil {
		return nil, err
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(spanExporter),
		sdktrace.WithResource(resource),
	), nil
}

func otlpProtocol() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")))
}

func newMeterProvider(
	ctx context.Context,
	exporter string,
	resource *resource.Resource,
	includePrometheus bool,
) (*sdkmetric.MeterProvider, prometheus.Gatherer, error) {
	var (
		readers  []sdkmetric.Reader
		gatherer prometheus.Gatherer
	)

	if includePrometheus {
		registry := prometheus.NewRegistry()
		prometheusReader, err := otelprom.New(
			otelprom.WithRegisterer(registry),
			otelprom.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes),
			otelprom.WithoutTargetInfo(),
			otelprom.WithoutScopeInfo(),
		)
		if err != nil {
			return nil, nil, err
		}
		readers = append(readers, prometheusReader)
		gatherer = prometheus.Gatherers{registry, prometheus.DefaultGatherer}
	}

	switch exporter {
	case "", "none":
	case "otlp":
		metricExporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, nil, err
		}
		readers = append(readers, sdkmetric.NewPeriodicReader(metricExporter))
	default:
		return nil, nil, errors.New("unsupported OTEL_METRICS_EXPORTER: " + exporter)
	}

	opts := []sdkmetric.Option{sdkmetric.WithResource(resource)}
	for _, reader := range readers {
		opts = append(opts, sdkmetric.WithReader(reader))
	}
	return sdkmetric.NewMeterProvider(opts...), gatherer, nil
}

func startMetricsServer(port int, gatherer prometheus.Gatherer) (*http.Server, error) {
	if gatherer == nil {
		return nil, errors.New("metrics gatherer is required")
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", NewPrometheusHandler(gatherer))

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen on metrics port %d: %w", port, err)
	}

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			otel.Handle(err)
		}
	}()

	return server, nil
}
