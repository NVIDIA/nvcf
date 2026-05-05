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
	"runtime"

	otelattr "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// InvokeWithSpan creates a span and passes it to the downstream
// function (next) that it invokes
// for tracing with OpenTelemetry which includes properly recording an error
// and setting status upon success
func InvokeWithSpan(ctx context.Context,
	tracer oteltrace.Tracer,
	spanName string,
	next func(ctx context.Context) error,
	opts ...oteltrace.SpanStartOption) error {
	return InvokeWithSpanWithSkip(ctx, tracer, spanName, next, 1, opts...)
}

// InvokeWithSpanWithSkip creates a span and passes it to the downstream
// function (next) that it invokes
// for tracing with OpenTelemetry which includes properly recording an error
// and setting status upon success
// 'skip' parameter tells "how many levels of stack to unwind from this call
// to determine 'code.function' and 'code.lineno'".
func InvokeWithSpanWithSkip(ctx context.Context,
	tracer oteltrace.Tracer,
	spanName string,
	next func(ctx context.Context) error,
	skip int,
	opts ...oteltrace.SpanStartOption) error {
	// Starting span to wrap the downstream function
	// if an error is returned we must record it and bubble it up
	childCtx, span := tracer.Start(ctx, spanName, opts...)
	span.SetAttributes(GetSpanCodeAttributes(skip + 2)...)
	defer span.End()
	err := next(childCtx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		// lightstep wants the error bit set to true to flag properly
		span.SetAttributes(otelattr.Bool("error", true))
		return err
	}
	// No error was returned, set status to OK
	span.SetStatus(otelcodes.Ok, "")
	return nil
}

func GetSpanCodeAttributes(skip int) []otelattr.KeyValue {
	// https://opentelemetry.io/docs/specs/semconv/general/attributes/#source-code-attributes

	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		file = "unknown_file"
		line = 0
	}

	functionName := "unknown_function"
	fn := runtime.FuncForPC(pc)
	if fn != nil {
		functionName = fn.Name()
	}

	attributes := []otelattr.KeyValue{
		// skipping code.column
		otelattr.String("code.filepath", file),
		otelattr.Int("code.lineno", line),
		otelattr.String("code.function", functionName),
		// skipping code.namespace, golang doesn't have namespaces, and getting package name is quite tricky
		// skipping code.stacktrace, it's opt-in
	}

	return attributes
}
