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
	"sync"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

const (
	GcpTraceIDKey = "logging.googleapis.com/trace"
	GcpSpanIDKey  = "logging.googleapis.com/spanId"
)

var (
	serviceName   = "llm-api-gateway"
	serviceNameMu sync.RWMutex

	Tracer = sync.OnceValue(func() trace.Tracer {
		return otel.GetTracerProvider().Tracer(ServiceName())
	})
)

func ServiceName() string {
	serviceNameMu.RLock()
	defer serviceNameMu.RUnlock()
	return serviceName
}

func SetServiceName(name string) {
	serviceNameMu.Lock()
	defer serviceNameMu.Unlock()
	serviceName = name
}

func Logger(ctx context.Context) *zerolog.Logger {
	logger := zerolog.Ctx(ctx)
	if logger.GetLevel() == zerolog.Disabled {
		logger = &zlog.Logger
	}

	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return logger
	}

	spanLogger := logger.With().
		Str(GcpSpanIDKey, spanContext.SpanID().String()).
		Logger()
	return &spanLogger
}

func LoggingSpanContext(ctx context.Context, logger zerolog.Logger) context.Context {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.HasTraceID() {
		logger = logger.With().Str(
			GcpTraceIDKey,
			spanContext.TraceID().String(),
		).Logger()
	}

	return logger.WithContext(ctx)
}
