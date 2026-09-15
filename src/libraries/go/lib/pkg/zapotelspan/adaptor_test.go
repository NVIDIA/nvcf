// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package zapotelspan

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestContextLogger_nil(t *testing.T) {
	require.Nil(t, ContextLogger(context.Background(), nil))
}

func TestZapOtelAdaptor_Write_addsSpanEvent(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	t.Cleanup(func() { _ = exporter.Shutdown(ctx) })
	sp := sdktrace.NewSimpleSpanProcessor(exporter)
	t.Cleanup(func() { _ = sp.Shutdown(ctx) })
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sp))
	t.Cleanup(func() { _ = tp.Shutdown(ctx) })

	tr := tp.Tracer("test")
	ctx, span := tr.Start(ctx, "op")

	core := NewZapOtelAdaptor(zapcore.InfoLevel)
	log := zap.New(core)
	log = ContextLogger(ctx, log)
	log.Info("hello", zap.String("extra", "v"))
	span.End()

	stubs := exporter.GetSpans()
	require.Len(t, stubs, 1)
	evs := stubs[0].Events
	require.GreaterOrEqual(t, len(evs), 1)
	require.Equal(t, "log", evs[len(evs)-1].Name)
}

func TestZapOtelAdaptor_Write_setsErrorStatus(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	t.Cleanup(func() { _ = exporter.Shutdown(ctx) })
	sp := sdktrace.NewSimpleSpanProcessor(exporter)
	t.Cleanup(func() { _ = sp.Shutdown(ctx) })
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sp))
	t.Cleanup(func() { _ = tp.Shutdown(ctx) })

	tr := tp.Tracer("test")
	ctx, span := tr.Start(ctx, "op")

	core := NewZapOtelAdaptor(zapcore.InfoLevel)
	log := ContextLogger(ctx, zap.New(core))
	log.Error("boom")
	span.End()

	stubs := exporter.GetSpans()
	require.Len(t, stubs, 1)
	require.Equal(t, otelcodes.Error, stubs[0].Status.Code)
}

func TestZapOtelAdaptor_Write_noSpan(t *testing.T) {
	core := NewZapOtelAdaptor(zapcore.InfoLevel)
	ent := zapcore.Entry{Level: zapcore.InfoLevel, Message: "x"}
	require.NoError(t, core.Write(ent, nil))
}

func TestZapOtelAdaptor_With_Sync(t *testing.T) {
	z := NewZapOtelAdaptor(zapcore.InfoLevel)
	child := z.With([]zapcore.Field{zap.String("k", "v")})
	require.NotNil(t, child)
	require.NoError(t, z.Sync())
}

func TestWrapCoreWithZapOtelAdaptor(t *testing.T) {
	base := zapcore.NewNopCore()
	log := zap.New(base, WrapCoreWithZapOtelAdaptor(zapcore.InfoLevel))
	require.NotNil(t, log)
	log.Info("wrapped")
}
