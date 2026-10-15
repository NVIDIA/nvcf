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
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// PropagateCtxFromEnv pulls trace context from environment variables, else starts a new root if not present
func PropagateCtxFromEnv(ctx context.Context) context.Context {
	// Check tracestate/tracecontext
	tracestate := os.Getenv("TRACESTATE")
	traceparent := os.Getenv("TRACEPARENT")

	// Check if traceparent is empty
	if traceparent == "" {
		zap.L().Warn("traceparent environment variable is empty, starting a new root span.")
	}
	// Prepare propagation carrier
	carrier := propagation.MapCarrier{
		"traceparent": traceparent,
		"tracestate":  tracestate,
	}
	// Extract the context from the carrier
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	return ctx
}

func RecordSpanError(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	return err
}
