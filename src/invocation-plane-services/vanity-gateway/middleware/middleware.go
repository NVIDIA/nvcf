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
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
)

const (
	serverOperationName = "vanity-gateway"
	unknownHTTPRoute    = "unknown"

	openAIModelNameAttribute attribute.Key = "openai_model_name"
	functionIDAttribute      attribute.Key = "function_id"
)

var spanNameFormatter = otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
	return r.URL.Path
})

func ServerTelemetryMiddleware(opts ...otelhttp.Option) func(http.Handler) http.Handler {
	options := append(defaultHTTPServerOptions(), opts...)
	return otelhttp.NewMiddleware(serverOperationName, options...)
}

func TracedRoundTripper(rt http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(rt, spanNameFormatter)
}

func defaultHTTPServerOptions() []otelhttp.Option {
	return []otelhttp.Option{
		spanNameFormatter,
		otelhttp.WithMetricAttributesFn(metricAttributesFromRequest),
	}
}

func metricAttributesFromRequest(r *http.Request) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("http.route", routePattern(r)),
	}
}

func AddOpenAIRequestMetricAttributes(ctx context.Context, modelName, functionID string) {
	attrs := make([]attribute.KeyValue, 0, 2)
	if modelName != "" {
		// Detach model names decoded from request bodies before the metric SDK retains them.
		attrs = append(attrs, openAIModelNameAttribute.String(strings.Clone(modelName)))
	}
	if functionID != "" {
		attrs = append(attrs, functionIDAttribute.String(functionID))
	}
	addRequestMetricAttributes(ctx, attrs...)
}

func AddFunctionIDMetricAttribute(ctx context.Context, functionID string) {
	if functionID == "" {
		return
	}
	addRequestMetricAttributes(ctx, functionIDAttribute.String(functionID))
}

func addRequestMetricAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	if len(attrs) == 0 {
		return
	}
	labeler, ok := otelhttp.LabelerFromContext(ctx)
	if !ok {
		return
	}
	labeler.Add(attrs...)
}

func routePattern(r *http.Request) string {
	if r == nil {
		return unknownHTTPRoute
	}
	if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
		if routePattern := routeContext.RoutePattern(); routePattern != "" {
			return routePattern
		}
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return unknownHTTPRoute
}
