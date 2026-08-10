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

package middleware

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceFieldsReturnsEmptyWhenNoTrace(t *testing.T) {
	assert.Empty(t, TraceFields(context.Background()))
}

func TestTraceFieldsReturnsTraceAndSpanIDs(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	fields := TraceFields(ctx)

	require.Len(t, fields, 2)

	sc := trace.SpanFromContext(ctx).SpanContext()
	assert.True(t, sc.HasTraceID())
	assert.Equal(t, traceIDLogField, fields[0].Key)
	assert.Equal(t, sc.TraceID().String(), fields[0].String)
	assert.Equal(t, spanIDLogField, fields[1].Key)
	assert.Equal(t, sc.SpanID().String(), fields[1].String)
}
