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

package tracing

import (
	"context"
	"fmt"
	"time"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
)

const (
	AccessTokenHeaderLightstep = "lightstep-access-token"
)

// OTELConfig holds OTEL tracing related info
type OTELConfig struct {
	Enabled              bool
	Endpoint             string
	AccessToken          string //nolint:gosec // G117: false positive - this is a configuration struct
	Insecure             bool
	Attributes           Attributes `mapstructure:"-"`
	SpanProcessorWrapper func(sdktrace.SpanProcessor) sdktrace.SpanProcessor
}

// Attributes defines trace attributes to add to the tracer
type Attributes struct {
	ServiceName    string
	ServiceVersion string
	Extra          map[string]string
}

var tracer *sdktrace.TracerProvider

// SetupOTELTracer sets up opentracing compliant tracer per config
// Currently, we only support lightstep tracer setup
func SetupOTELTracer(cfg *OTELConfig) (trace.TracerProvider, error) {
	if !cfg.Enabled {
		return noop.NewTracerProvider(), nil
	}
	if cfg.Endpoint == "" {
		return nil, &nverrors.ConfigError{FieldName: "tracing.endpoint", Message: "not found"}
	}

	// If already initialized, return the existing tracer information
	if tracer != nil {
		zap.L().Info("Tracer already initialized. Returning existing instance.")
		return tracer, nil
	}

	// Build attributes for tracing resource
	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(cfg.Attributes.ServiceName),
		semconv.ServiceVersionKey.String(cfg.Attributes.ServiceVersion),
	}
	// Add custom tags from Tracing config
	for k, v := range cfg.Attributes.Extra {
		attrs = append(attrs, attribute.String(k, v))
	}

	// Build resource for trace provider
	r, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(resource.Default().SchemaURL(), attrs...),
	)
	if err != nil {
		return nil, err
	}

	// AuthN headers
	var headers = map[string]string{
		AccessTokenHeaderLightstep: cfg.AccessToken,
	}
	// Create an OTLP exporter with gRPC transport and a developer satellite endpoint
	secureOption := otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
	if cfg.Insecure {
		secureOption = otlptracegrpc.WithInsecure()
	}
	ctx, cancelFn := context.WithTimeout(context.Background(), time.Second)
	defer cancelFn()
	exporter, err := otlptracegrpc.New(
		ctx,
		secureOption,
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		otlptracegrpc.WithHeaders(headers),
	)
	if err != nil {
		return nil, fmt.Errorf("tracing exporter setup failed: %+v", err)
	}

	spanProcessorWrapper := cfg.SpanProcessorWrapper
	if spanProcessorWrapper == nil {
		spanProcessorWrapper = func(processor sdktrace.SpanProcessor) sdktrace.SpanProcessor {
			return processor
		}
	}
	spanProcessor := spanProcessorWrapper(sdktrace.NewBatchSpanProcessor(exporter))

	// Create a trace provider with the exporter and resource
	tracer = sdktrace.NewTracerProvider(
		sdktrace.WithResource(r),
		sdktrace.WithSpanProcessor(spanProcessor),
	)
	// Register the trace provider
	otel.SetTracerProvider(tracer)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	return otel.GetTracerProvider(), nil
}

func Shutdown() {
	if tracer == nil {
		return
	}
	zap.L().Info("Shutting down OTEL tracer")
	if err := tracer.Shutdown(context.Background()); err != nil {
		zap.L().Error("Error shutting down tracer", zap.Error(err))
	}
	tracer = nil
}
