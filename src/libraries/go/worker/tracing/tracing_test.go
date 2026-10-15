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
	"go.opentelemetry.io/otel/propagation"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
)

// ------------------------------------------------------------------------

// Test ContextPropagation function
func TestContextPropagation(t *testing.T) {

	// Set TRACESTATE
	os.Setenv("TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	os.Setenv("TRACESTATE", "roco=00f067aa0ba902b7,ai=t61rcWkgMzE")

	// Initialize tracer
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	ctx := PropagateCtxFromEnv(context.Background())
	_, span := otel.GetTracerProvider().Tracer("nvcf-test").Start(ctx, "Run Propagation Test")
	defer span.End()

	spanCtx := span.SpanContext()
	if !spanCtx.IsValid() {
		t.Error("Expected valid span context, got invalid context")
	}
	if spanCtx.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("Expected Trace ID '4bf92f3577b34da6a3ce929d0e0e4736', got %s", spanCtx.TraceID())
	}

}

// Test ContextPropagation function
func TestContextPropagationEmptyParent(t *testing.T) {
	ctx := context.Background()

	// Unset traceparet and tracestate, if set
	_ = os.Unsetenv("TRACEPARENT")
	_ = os.Unsetenv("TRACESTATE")

	// Initialize tracer
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Initialize span
	_, span := otel.GetTracerProvider().Tracer("nvcf-test").Start(PropagateCtxFromEnv(ctx), "Run Propagation Test")
	defer span.End()

	spanCtx := span.SpanContext()
	if !spanCtx.IsValid() {
		t.Error("Expected valid span context, got invalid context")
	}
	if spanCtx.TraceID().IsValid() == false {
		t.Error("Expected a valid Trace ID, but got an invalid one")
	}
	if spanCtx.SpanID().IsValid() == false {
		t.Error("Expected a valid Span ID, but got an invalid one")
	}
	if spanCtx.IsRemote() {
		t.Error("Expected the span to be a root span, but it has a remote parent")
	}

}
