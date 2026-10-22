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
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	serverOperationName = "vanity-gateway"
	unknownHTTPRoute    = "unknown"

	openAIModelNameAttribute           attribute.Key = "openai_model_name"
	functionIDAttribute                attribute.Key = "function_id"
	GatewayProxyOutcomeMetricAttribute attribute.Key = "gateway_proxy_outcome"
	GatewayProxyOutcomeSpanAttribute   attribute.Key = "gateway.proxy.outcome"
)

type GatewayProxyOutcome string

const (
	GatewayProxyOutcomeClientCanceled GatewayProxyOutcome = "client_canceled"
	GatewayProxyOutcomeProxyError     GatewayProxyOutcome = "gateway_proxy_error"
	// GatewayProxyOutcomeRejected marks a response the gateway wrote itself with
	// no dependency involved (offline, end of life, validation, not found).
	GatewayProxyOutcomeRejected GatewayProxyOutcome = "gateway_rejected"
	// GatewayProxyOutcomeUpstreamStatus marks a non-2xx response passed through
	// from the upstream service.
	GatewayProxyOutcomeUpstreamStatus GatewayProxyOutcome = "upstream_status"
)

// ErrorSourceHeader is added to non-2xx responses so clients and support can
// tell where the status came from. Its value is a GatewayProxyOutcome.
const ErrorSourceHeader = "NVCF-Error-Source"

var spanNameFormatter = otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
	return r.URL.Path
})

func ServerTelemetryMiddleware(opts ...otelhttp.Option) func(http.Handler) http.Handler {
	options := append(defaultHTTPServerOptions(), opts...)
	telemetry := otelhttp.NewMiddleware(serverOperationName, options...)
	return func(next http.Handler) http.Handler {
		return processingMiddleware(telemetry(next))
	}
}

func TracedRoundTripper(rt http.RoundTripper) http.RoundTripper {
	traced := otelhttp.NewTransport(upstreamOutcomeRoundTripper{rt}, spanNameFormatter, otelhttp.WithMetricAttributesFn(shadowMetricAttributes))
	return dispatchRoundTripper{traced}
}

type upstreamOutcomeKey struct{}

type healthCallKey struct{}

// upstreamOutcome is filled by upstreamOutcomeRoundTripper and read when
// otelhttp records the client metric, after the response status is known.
type upstreamOutcome struct{ errorSource, errorType string }

// WithHealthCall marks outbound requests made by health probes so client
// metrics can tell them apart from proxied inference traffic.
func WithHealthCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, healthCallKey{}, true)
}

// upstreamOutcomeRoundTripper sits below otelhttp. It records a bounded
// error_source from the upstream header on non-2xx responses and an
// error.type when no response was received.
type upstreamOutcomeRoundTripper struct{ http.RoundTripper }

func (rt upstreamOutcomeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := rt.RoundTripper.RoundTrip(r)
	outcome, _ := r.Context().Value(upstreamOutcomeKey{}).(*upstreamOutcome)
	if outcome == nil {
		return resp, err
	}
	switch {
	case err != nil:
		outcome.errorType = "transport_error"
		if errors.Is(err, context.Canceled) {
			outcome.errorType = "canceled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			outcome.errorType = "timeout"
		}
	case resp.StatusCode >= http.StatusMultipleChoices:
		outcome.errorSource = "unknown"
		if source := resp.Header.Get(ErrorSourceHeader); source == "invocation_service" || source == "worker" {
			outcome.errorSource = source
		}
	}
	return resp, err
}

type dispatchRoundTripper struct{ http.RoundTripper }

func (rt dispatchRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.WithContext(context.WithValue(r.Context(), upstreamOutcomeKey{}, &upstreamOutcome{}))
	if state := processingFromContext(r.Context()); state != nil {
		outcome := "dispatched"
		if r.Context().Err() != nil {
			outcome = string(GatewayProxyOutcomeClientCanceled)
		}
		state.record(r.Context(), outcome)
	}
	return rt.RoundTripper.RoundTrip(r)
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

func AddGatewayProxyOutcomeMetricAttribute(ctx context.Context, outcome GatewayProxyOutcome) {
	switch outcome {
	case GatewayProxyOutcomeClientCanceled, GatewayProxyOutcomeProxyError,
		GatewayProxyOutcomeRejected, GatewayProxyOutcomeUpstreamStatus:
		if state := processingFromContext(ctx); state != nil {
			state.outcome = string(outcome)
		}
		addRequestMetricAttributes(ctx, GatewayProxyOutcomeMetricAttribute.String(string(outcome)))
	}
}

// RecordGatewayProxyOutcome sets the outcome on the inbound server request
// metric and on the active server span so traces match metrics.
func RecordGatewayProxyOutcome(ctx context.Context, outcome GatewayProxyOutcome) {
	AddGatewayProxyOutcomeMetricAttribute(ctx, outcome)
	trace.SpanFromContext(ctx).SetAttributes(GatewayProxyOutcomeSpanAttribute.String(string(outcome)))
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
