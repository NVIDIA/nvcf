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

// Package zapotelspan provides a zapcore.Core that records log lines as OpenTelemetry
// span events on the active span (when passed via ContextLogger), preserving trace-local
// diagnostics without a separate GitLab-only module.
package zapotelspan

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ZapOtelAdaptor is a Zap core that integrates with OpenTelemetry traces by adding
// log lines as span events on the span supplied via the trace_span field.
type ZapOtelAdaptor struct {
	zapcore.LevelEnabler
	context []zapcore.Field
}

var _ zapcore.Core = (*ZapOtelAdaptor)(nil)

// NewZapOtelAdaptor creates a new ZapOtelAdaptor.
func NewZapOtelAdaptor(level zapcore.Level) *ZapOtelAdaptor {
	return &ZapOtelAdaptor{
		LevelEnabler: level,
	}
}

// Check implements zapcore.Core.
func (z *ZapOtelAdaptor) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if z.Enabled(ent.Level) {
		ce = ce.AddCore(ent, z)
	}
	return ce
}

// With implements zapcore.Core.
func (z *ZapOtelAdaptor) With(fields []zapcore.Field) zapcore.Core {
	return &ZapOtelAdaptor{
		LevelEnabler: z.LevelEnabler,
		context:      append(append([]zapcore.Field(nil), z.context...), fields...),
	}
}

// Write implements zapcore.Core.
func (z *ZapOtelAdaptor) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	var span trace.Span

	fields = append(append([]zapcore.Field(nil), z.context...), fields...)

	for _, field := range fields {
		if field.Key == traceSpanFieldKey {
			if s, ok := field.Interface.(trace.Span); ok {
				span = s
				break
			}
		}
	}

	if span == nil {
		return nil
	}

	attrs := []attribute.KeyValue{
		attribute.String("log.severity", ent.Level.String()),
		attribute.String("log.message", ent.Message),
		attribute.String("log.time", ent.Time.Format(time.RFC3339Nano)),
	}

	encoder := NewOtelAttrsEncoder()

	for _, field := range fields {
		if field.Key == traceSpanFieldKey {
			continue
		}
		field.AddTo(encoder)
	}

	span.AddEvent("log", trace.WithAttributes(append(attrs, encoder.GetAttrs()...)...))

	if ent.Level >= zapcore.ErrorLevel {
		span.SetStatus(codes.Error, ent.Message)
	}

	return nil
}

// Sync implements zapcore.Core.
func (z *ZapOtelAdaptor) Sync() error {
	_ = z
	return nil
}

const traceSpanFieldKey = "trace_span"

// ContextLogger returns a zap.Logger that carries the span from ctx so ZapOtelAdaptor
// can attach log lines as span events. If logger is nil, returns nil.
func ContextLogger(ctx context.Context, logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return nil
	}
	span := trace.SpanFromContext(ctx)
	spanCtx := span.SpanContext()

	if spanCtx.IsValid() {
		traceSpan := zap.Field{
			Key:       traceSpanFieldKey,
			Type:      zapcore.SkipType,
			Interface: span,
		}

		return logger.With(
			traceSpan,
			zap.String("trace_id", spanCtx.TraceID().String()),
			zap.String("span_id", spanCtx.SpanID().String()))
	}

	return logger
}

// WrapCoreWithZapOtelAdaptor returns a zap.Option that tees a ZapOtelAdaptor alongside
// the primary core.
func WrapCoreWithZapOtelAdaptor(level zapcore.Level) zap.Option {
	otelAdaptor := NewZapOtelAdaptor(level)

	return zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, otelAdaptor)
	})
}
