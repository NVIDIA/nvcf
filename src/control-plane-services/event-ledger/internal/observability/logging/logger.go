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

package logging

import (
	"context"
	"net/http"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/zapotelspan"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/common"
)

const traceIDField = "otlp.trace_id"

type TraceLogger struct {
	traceParent string
	logger      *otelzap.Logger
}

func NewTraceLogger(ctx context.Context, logger *otelzap.Logger) *TraceLogger {
	if logger == nil {
		logger = otelzap.New(zap.NewNop())
	}

	spanCtx := trace.SpanContextFromContext(ctx)
	traceParent := spanCtx.SpanID().String()
	if spanCtx.TraceID().IsValid() {
		traceParent = spanCtx.TraceID().String()
	}

	return &TraceLogger{
		traceParent: traceParent,
		logger:      otelzap.New(zapotelspan.ContextLogger(ctx, logger.Logger)),
	}
}

func (t *TraceLogger) WarnContext(ctx context.Context, msg string, fields ...zapcore.Field) {
	caller, _ := common.CurrentFunction(2)
	fields = append(fields, zap.String(traceIDField, t.traceParent), zap.String("caller", caller))
	t.logger.WarnContext(ctx, msg, fields...)
}

func (t *TraceLogger) InfoContext(ctx context.Context, msg string, fields ...zapcore.Field) {
	caller, _ := common.CurrentFunction(2)
	fields = append(fields, zap.String(traceIDField, t.traceParent), zap.String("caller", caller))
	t.logger.InfoContext(ctx, msg, fields...)
}

func (t *TraceLogger) DebugContext(ctx context.Context, msg string, fields ...zapcore.Field) {
	caller, _ := common.CurrentFunction(2)
	fields = append(fields, zap.String(traceIDField, t.traceParent), zap.String("caller", caller))
	t.logger.DebugContext(ctx, msg, fields...)
}

func (t *TraceLogger) ErrorContext(ctx context.Context, msg string, fields ...zapcore.Field) {
	caller, _ := common.CurrentFunction(2)
	fields = append(fields, zap.String(traceIDField, t.traceParent), zap.String("caller", caller))
	t.logger.ErrorContext(ctx, msg, fields...)
}

type loggerKeyType struct{}

var LoggerKey = loggerKeyType{}

func GetLogger(ctx context.Context) *TraceLogger {
	logger, ok := ctx.Value(LoggerKey).(*TraceLogger)
	if ok && logger != nil {
		return logger
	}
	return NewTraceLogger(ctx, otelzap.New(zap.L()))
}

func LoggerMiddleware(logger *otelzap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := AttachLoggerToContext(r.Context(), logger)
			r = r.WithContext(ctx)
			LogHTTPRequest(ctx, GetLogger(ctx), r)
			next.ServeHTTP(w, r)
		})
	}
}

func AttachLoggerToContext(ctx context.Context, logger *otelzap.Logger) context.Context {
	return context.WithValue(ctx, LoggerKey, NewTraceLogger(ctx, logger))
}
