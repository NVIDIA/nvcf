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
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type Runtime struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
}

func InitFromEnv(ctx context.Context) (*Runtime, error) {
	runtime := &Runtime{}

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	if exporter := os.Getenv("OTEL_TRACES_EXPORTER"); exporter != "" && exporter != "none" {
		resource, err := newResource()
		if err != nil {
			return nil, err
		}

		traceProvider, err := newTracerProvider(ctx, exporter, resource)
		if err != nil {
			return nil, err
		}
		runtime.tracerProvider = traceProvider
		otel.SetTracerProvider(traceProvider)
	}

	if exporter := os.Getenv("OTEL_METRICS_EXPORTER"); exporter != "" && exporter != "none" {
		resource, err := newResource()
		if err != nil {
			return nil, err
		}

		meterProvider, err := newMeterProvider(ctx, exporter, resource)
		if err != nil {
			return nil, err
		}
		runtime.meterProvider = meterProvider
		otel.SetMeterProvider(meterProvider)
		InitializeMetrics()
	}

	return runtime, nil
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}

	var errs []error
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
		spanExporter, err = otlptracegrpc.New(ctx)
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

func newMeterProvider(
	ctx context.Context,
	exporter string,
	resource *resource.Resource,
) (*sdkmetric.MeterProvider, error) {
	switch exporter {
	case "otlp":
		metricExporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		return sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
			sdkmetric.WithResource(resource),
		), nil
	default:
		return nil, errors.New("unsupported OTEL_METRICS_EXPORTER: " + exporter)
	}
}
