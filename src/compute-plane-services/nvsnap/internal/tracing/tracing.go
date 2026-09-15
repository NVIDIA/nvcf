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

// Package tracing wires OpenTelemetry into NvSnap binaries.
//
// Default is no-op: if OTEL_EXPORTER_OTLP_ENDPOINT is unset, Init
// returns a working tracer provider that drops all spans. Production
// gets zero impact unless the operator opts in by setting the env var
// (or the Helm chart wires it for them).
//
// When enabled, spans are exported via OTLP/gRPC to the configured
// collector (typically Jaeger's collector deployed in the same
// cluster).
package tracing

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TracerName is the instrumentation scope name used by NvSnap spans.
// Pass to otel.Tracer() so spans show up under a consistent name in
// the Jaeger UI.
const TracerName = "nvsnap"

// Init configures the global tracer provider. Returns a shutdown
// function the caller must invoke before exit to flush buffered
// spans. shutdown is always safe to call (no-op when tracing was
// never enabled).
//
// serviceName flows through to resource attributes so Jaeger groups
// spans per binary (nvsnap-agent, nvsnap-server, …).
func Init(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// No-op shutdown. The default global tracer provider already
		// drops all spans, so we don't need to install our own.
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(os.Getenv("NVSNAP_VERSION")),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("build OTel resource: %w", err)
	}

	// OTLP/gRPC. WithInsecure because the collector is in-cluster — no
	// public exposure. TLS would need cert plumbing that buys nothing
	// here.
	exporter, err := otlptrace.New(ctx,
		otlptracegrpc.NewClient(
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithResource(res),
		// AlwaysSample for now — checkpoint/restore are infrequent
		// enough that we want every trace. Move to ParentBased +
		// TraceIDRatio if span volume becomes a concern.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// Tracer returns the named NvSnap tracer. Use this everywhere a span is
// started so the instrumentation-scope name stays consistent.
func Tracer() trace.Tracer {
	return otel.Tracer(TracerName)
}

// ContextFromEnv reads a W3C traceparent from the OTEL_TRACE_PARENT env
// var and returns a context carrying that parent. When the env var is
// unset (or malformed) the original context is returned unchanged — the
// caller's spans become independent roots.
//
// Used to bridge the agent → restore-entrypoint process boundary: the
// agent stamps `OTEL_TRACE_PARENT=<traceparent>` into the placeholder
// pod's env, restore-entrypoint extracts it on startup so its spans
// nest under the agent's `restore.full` span.
func ContextFromEnv(ctx context.Context) context.Context {
	tp := os.Getenv("OTEL_TRACE_PARENT")
	if tp == "" {
		return ctx
	}
	prop := propagation.TraceContext{}
	carrier := propagation.MapCarrier{"traceparent": tp}
	if ts := os.Getenv("OTEL_TRACE_STATE"); ts != "" {
		carrier["tracestate"] = ts
	}
	return prop.Extract(ctx, carrier)
}

// InjectTraceparent serializes the active span's context to W3C
// traceparent + tracestate strings suitable for injection into env
// vars. Returns empty strings when there's no active span. Pair with
// ContextFromEnv on the receiving side.
func InjectTraceparent(ctx context.Context) (traceparent, tracestate string) {
	prop := propagation.TraceContext{}
	carrier := propagation.MapCarrier{}
	prop.Inject(ctx, carrier)
	return carrier["traceparent"], carrier["tracestate"]
}
