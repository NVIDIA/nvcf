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
	"net"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
)

type healthCallContextKey struct{}

// ContextWithHealthCall marks an outbound readiness probe for client metrics.
func ContextWithHealthCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, healthCallContextKey{}, true)
}

func clientMetricContext(ctx context.Context) context.Context {
	// A server labeler may be inherited through the proxy. Copy its attributes
	// into a fresh labeler so results from this outbound call cannot leak back
	// into server metrics, shadow replays, or another call using the same context.
	labeler := &otelhttp.Labeler{}
	if parent, ok := otelhttp.LabelerFromContext(ctx); ok {
		labeler.Add(parent.Get()...)
	}
	kind := "inference"
	if health, _ := ctx.Value(healthCallContextKey{}).(bool); health {
		kind = "health"
	}
	labeler.Add(attribute.String("gateway_call_kind", kind))
	return otelhttp.ContextWithLabeler(ctx, labeler)
}

type clientMetricsRoundTripper struct{ http.RoundTripper }

func (rt clientMetricsRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := rt.RoundTripper.RoundTrip(r)
	labeler, _ := otelhttp.LabelerFromContext(r.Context())
	if err != nil {
		kind := "transport_error"
		var networkError net.Error
		switch {
		case errors.Is(err, context.Canceled):
			kind = "canceled"
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
			kind = "timeout"
		}
		labeler.Add(attribute.String("error.type", kind))
	} else if response != nil && (response.StatusCode < 200 || response.StatusCode >= 300) {
		source := response.Header.Get(ErrorSourceHeader)
		switch source {
		case "invocation_service", "worker":
		default:
			source = "unknown"
		}
		labeler.Add(attribute.String("error_source", source))
	}
	return response, err
}
