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
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const preUpstreamMetricName = "nvcf_vanity_gateway_pre_upstream_processing_seconds"

type processingContextKey struct{}
type shadowContextKey struct{}

type requestProcessing struct {
	once      sync.Once
	start     time.Time
	request   *http.Request
	histogram metric.Float64Histogram
	model     string
	outcome   string
}

func processingFromContext(ctx context.Context) *requestProcessing {
	state, _ := ctx.Value(processingContextKey{}).(*requestProcessing)
	return state
}

func (s *requestProcessing) record(ctx context.Context, outcome string) {
	elapsed := time.Since(s.start).Seconds()
	s.once.Do(func() {
		attrs := []attribute.KeyValue{
			attribute.String("http.route", routePattern(s.request)),
			attribute.String("outcome", outcome),
		}
		if s.model != "" {
			attrs = append(attrs, openAIModelNameAttribute.String(s.model))
		}
		s.histogram.Record(ctx, elapsed, metric.WithAttributes(attrs...))
	})
}

// SetPreUpstreamModel accepts only model names resolved from public configuration.
func SetPreUpstreamModel(ctx context.Context, model string) {
	if state := processingFromContext(ctx); state != nil {
		state.model = model
	}
}

// ShadowTelemetryContext isolates mutable inbound telemetry while retaining trace context.
func ShadowTelemetryContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, processingContextKey{}, (*requestProcessing)(nil))
	ctx = context.WithValue(ctx, shadowContextKey{}, true)
	return otelhttp.ContextWithLabeler(ctx, &otelhttp.Labeler{})
}

func shadowMetricAttributes(r *http.Request) []attribute.KeyValue {
	shadow, _ := r.Context().Value(shadowContextKey{}).(bool)
	return []attribute.KeyValue{attribute.Bool("nvcf.shadow", shadow)}
}

func processingMiddleware(next http.Handler) http.Handler {
	histogram, err := otel.Meter(serverOperationName).Float64Histogram(preUpstreamMetricName,
		metric.WithUnit("s"),
		metric.WithDescription("Primary request processing from telemetry entry to upstream dispatch or local completion"),
		metric.WithExplicitBucketBoundaries(0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60))
	otel.Handle(err)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shadow, _ := r.Context().Value(shadowContextKey{}).(bool); shadow {
			next.ServeHTTP(w, r)
			return
		}
		state := &requestProcessing{start: time.Now(), request: r, histogram: histogram, outcome: "local"}
		ctx := context.WithValue(r.Context(), processingContextKey{}, state)
		defer func() {
			outcome := state.outcome
			if ctx.Err() != nil {
				outcome = string(GatewayProxyOutcomeClientCanceled)
			}
			state.record(ctx, outcome)
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
