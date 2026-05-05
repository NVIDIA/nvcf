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
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"

	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
)

const (
	// ScopeName is the instrumentation scope name.
	ScopeName = "github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry"
)

func GetTracer() oteltrace.Tracer {
	return otel.Tracer(ScopeName)
}

func GetTraceID(ctx context.Context) string {
	if span := oteltrace.SpanFromContext(ctx); span.SpanContext().TraceID().IsValid() {
		traceId := span.SpanContext().TraceID().String()
		return traceId
	}

	return ""
}

func newExporter(ctx context.Context, cfg *config.TracingConfig, logger *zap.Logger) (*otlptrace.Exporter, error) {
	var headers map[string]string

	endpoint := cfg.Endpoint

	// Initialize headers map if Lightstep token is provided
	if cfg.LightstepAccessToken != "" {
		headers = map[string]string{
			"lightstep-access-token": cfg.LightstepAccessToken,
		}
		logger.Info("using Lightstep authentication with access token")
	}

	secureOptions := otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
	if cfg.Insecure {
		secureOptions = otlptracegrpc.WithInsecure()
		logger.Warn("connecting to tracing endpoint insecurely")
	}

	client := otlptracegrpc.NewClient(
		otlptracegrpc.WithHeaders(headers),
		otlptracegrpc.WithEndpoint(endpoint),
		secureOptions,
	)
	return otlptrace.New(ctx, client)
}

func newTraceProvider(exp *otlptrace.Exporter, cfg *config.TelemetryConfig, logger *zap.Logger) *trace.TracerProvider {

	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.ServiceVersionKey.String(cfg.ServiceVersion),
			semconv.DeploymentEnvironmentNameKey.String(cfg.DeploymentEnvironmentName),
		),
		resource.WithContainer(),
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithOS(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
	)
	if errors.Is(err, resource.ErrPartialResource) || errors.Is(err, resource.ErrSchemaURLConflict) {
		logger.Error("issue with otel resource", zap.Error(err))
	} else if err != nil {
		logger.Fatal("could not create otel resource", zap.Error(err))
	}
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exp),
		trace.WithResource(res),
		trace.WithSampler(trace.AlwaysSample()),
	)
	if tp == nil {
		logger.Fatal("failed to create tracer provider")
	}
	return tp
}

func ApplyTracing(ctx context.Context, cfg *config.TracingConfig, telemetryCfg *config.TelemetryConfig, logger *zap.Logger) (func(), *trace.TracerProvider) {
	logger.Info("creating new otlp exporter",
		zap.String("endpoint", cfg.Endpoint),
		zap.Bool("insecure", cfg.Insecure),
		zap.Bool("using_lightstep_auth", cfg.LightstepAccessToken != ""),
	)
	exporter, err := newExporter(ctx, cfg, logger)
	if err != nil {
		logger.Fatal("could not create otlp exporter", zap.Error(err))
	}
	tp := newTraceProvider(exporter, telemetryCfg, logger)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if otel.GetTracerProvider() != tp {
		logger.Fatal("failed to set tracer provider")
	}

	logger.Info("tracing provider set successfully")

	deferFunc := func() {
		if err := tp.Shutdown(ctx); err != nil {
			logger.Fatal("could not shut down tracing provider", zap.Error(err))
		}
	}

	return deferFunc, tp
}
