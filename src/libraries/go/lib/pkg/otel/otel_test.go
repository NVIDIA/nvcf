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

package otel

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otelattr "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func byKey(a, b otelattr.KeyValue) int {
	if a.Key < b.Key {
		return -1
	}
	if a.Key > b.Key {
		return 1
	}
	return 0
}

func currentLine(t *testing.T) int {
	t.Helper()
	_, _, line, ok := runtime.Caller(1)
	require.True(t, ok)
	return line
}

func TestInvokeWithSpan(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	defer exporter.Shutdown(ctx)
	spanProcessor := sdktrace.NewSimpleSpanProcessor(exporter)
	defer spanProcessor.Shutdown(ctx)
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanProcessor))
	defer tracerProvider.Shutdown(ctx)
	otel.SetTracerProvider(tracerProvider)

	tracer := otel.Tracer("github.com/NVIDIA/nvcf/src/libraries/go/lib/otel")
	pc, expectedFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	expectedFunction := runtime.FuncForPC(pc)
	require.NotNil(t, expectedFunction)
	successCallStartLine := currentLine(t) + 1
	InvokeWithSpan(ctx, tracer, "some-span", func(ctx context.Context) error {
		return nil
	}, oteltrace.WithAttributes(otelattr.String("some-string", "value")))
	successCallEndLine := successCallStartLine + 2
	stubs := exporter.GetSpans()
	require.Len(t, stubs, 1)
	assert.Equal(t, otelcodes.Ok, stubs[0].Status.Code)
	assert.Len(t, stubs[0].Attributes, 4)

	attrs := stubs[0].Attributes
	slices.SortFunc(attrs, byKey)
	assert.Equal(t, expectedFile, attrs[0].Value.AsString())
	assert.Equal(t, attrs[1], otelattr.String("code.function", expectedFunction.Name()))
	assert.GreaterOrEqual(t, attrs[2].Value.AsInt64(), int64(successCallStartLine))
	assert.LessOrEqual(t, attrs[2].Value.AsInt64(), int64(successCallEndLine))
	assert.Equal(t, attrs[3], otelattr.String("some-string", "value"))

	exporter.Reset()

	spanErr := errors.New("span-error")
	errorCallStartLine := currentLine(t) + 1
	InvokeWithSpan(ctx, tracer, "some-span", func(ctx context.Context) error {
		return spanErr
	}, oteltrace.WithAttributes(otelattr.String("some-other-string", "other-value")))
	errorCallEndLine := errorCallStartLine + 2
	stubs = exporter.GetSpans()
	require.Len(t, stubs, 1)
	assert.Equal(t, otelcodes.Error, stubs[0].Status.Code)

	attrs = stubs[0].Attributes
	slices.SortFunc(attrs, byKey)
	assert.Equal(t, expectedFile, attrs[0].Value.AsString())
	assert.Equal(t, attrs[1], otelattr.String("code.function", expectedFunction.Name()))
	assert.GreaterOrEqual(t, attrs[2].Value.AsInt64(), int64(errorCallStartLine))
	assert.LessOrEqual(t, attrs[2].Value.AsInt64(), int64(errorCallEndLine))
	assert.Equal(t, attrs[3], otelattr.Bool("error", true))
	assert.Equal(t, attrs[4], otelattr.String("some-other-string", "other-value"))
}
