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

package gateway

import (
	"ai-api-gateway-service/middleware"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// serveThroughTelemetry drives handler through the real otelhttp server
// middleware and returns the recorded response, metrics, and ended spans.
func serveThroughTelemetry(t *testing.T, handler http.HandlerFunc, req *http.Request) (*httptest.ResponseRecorder, *sdkmetric.ManualReader, *tracetest.SpanRecorder) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, meterProvider.Shutdown(context.Background())) })
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { require.NoError(t, tracerProvider.Shutdown(context.Background())) })

	wrapped := middleware.ServerTelemetryMiddleware(
		otelhttp.WithMeterProvider(meterProvider),
		otelhttp.WithTracerProvider(tracerProvider),
	)(handler)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	return rec, reader, spanRecorder
}

func requireOrigin(t *testing.T, rec *httptest.ResponseRecorder, reader *sdkmetric.ManualReader, spans *tracetest.SpanRecorder, status int, outcome middleware.GatewayProxyOutcome) {
	t.Helper()
	require.Equal(t, status, rec.Code)
	require.Equal(t, string(outcome), rec.Header().Get(middleware.ErrorSourceHeader))
	require.True(t, gatewayMetricHasAttributes(collectGatewayMetrics(t, reader), map[attribute.Key]attribute.Value{
		"http.response.status_code":                   attribute.Int64Value(int64(status)),
		middleware.GatewayProxyOutcomeMetricAttribute: attribute.StringValue(string(outcome)),
	}), "server metric must carry outcome %s for status %d", outcome, status)
	ended := spans.Ended()
	require.Len(t, ended, 1)
	require.True(t, spanHasAttribute(ended[0].Attributes(), traceAttrGatewayProxyOutcome, string(outcome)))
}

func upstreamReturning(t *testing.T, status int, body string, header http.Header) *VanityDirector {
	t.Helper()
	director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := header.Clone()
		if h == nil {
			h = http.Header{}
		}
		return &http.Response{
			StatusCode:    status,
			Header:        h,
			Body:          io.NopCloser(bytes.NewBufferString(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}, nil
	}))
	require.NoError(t, err)
	return director
}

// Group 1: gateway-local rejections.
func TestGatewayRejectedOutcome(t *testing.T) {
	director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("upstream must not be called for a gateway-local rejection")
		return nil, nil
	}))
	require.NoError(t, err)

	tests := []struct {
		name   string
		target VanityExecRequest
		status int
	}{
		{"offline", VanityExecRequest{FunctionID: "f", OfflineMessage: "down"}, http.StatusServiceUnavailable},
		{"end of life", VanityExecRequest{FunctionID: "f", EOL: time.Now().Add(-time.Hour)}, http.StatusGone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec, reader, spans := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, director.ServeExec(test.target, w, r))
			}, httptest.NewRequest(http.MethodPost, "/test", nil))
			requireOrigin(t, rec, reader, spans, test.status, middleware.GatewayProxyOutcomeRejected)
		})
	}
}

func TestGatewayRejectedOutcomeOpenAIDirector(t *testing.T) {
	director := &OpenAIDirector{}
	known := map[string]FunctionInfo{
		"offline-model": {functionId: "f", offlineMessage: "down"},
		"expired-model": {functionId: "f", eol: time.Now().Add(-time.Hour)},
	}
	tests := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{"model not found", "/v1/chat/completions", `{"model":"nope"}`, http.StatusNotFound},
		{"model field missing", "/v1/chat/completions", `{}`, http.StatusBadRequest},
		{"offline", "/v1/chat/completions", `{"model":"offline-model"}`, http.StatusServiceUnavailable},
		{"expired", "/v1/chat/completions", `{"model":"expired-model"}`, http.StatusGone},
		{"messages model not found", "/v1/messages", `{"model":"nope"}`, http.StatusNotFound},
		{"messages invalid json", "/v1/messages", `{`, http.StatusBadRequest},
		{"messages offline", "/v1/messages", `{"model":"offline-model"}`, http.StatusServiceUnavailable},
		{"messages expired", "/v1/messages", `{"model":"expired-model"}`, http.StatusGone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			req.Header.Set("Content-Type", "application/json")
			rec, reader, spans := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
				_, handled := director.resolveModelMappedRequest(w, r, known)
				require.True(t, handled)
			}, req)
			requireOrigin(t, rec, reader, spans, test.status, middleware.GatewayProxyOutcomeRejected)
		})
	}
}

func TestGatewayRejectedOutcomeGetModelUndefinedName(t *testing.T) {
	director := &OpenAIDirector{}
	req := httptest.NewRequest(http.MethodGet, "/v1/models/unknown", nil)
	rec, reader, spans := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
		director.GetModel(w, r)
	}, req)
	requireOrigin(t, rec, reader, spans, http.StatusInternalServerError, middleware.GatewayProxyOutcomeRejected)
}

func TestShadowGuardRejectionLandsOnServerMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	guard := middleware.RejectSpoofedShadowRequests("NVCF-Shadow", middleware.ServerTelemetryMiddleware(otelhttp.WithMeterProvider(provider)))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("must not reach handler") }),
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("NVCF-Shadow", "1")
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, string(middleware.GatewayProxyOutcomeRejected), rec.Header().Get(middleware.ErrorSourceHeader))
	require.True(t, gatewayMetricHasAttributes(collectGatewayMetrics(t, reader), map[attribute.Key]attribute.Value{
		"http.response.status_code":                   attribute.Int64Value(http.StatusBadRequest),
		middleware.GatewayProxyOutcomeMetricAttribute: attribute.StringValue(string(middleware.GatewayProxyOutcomeRejected)),
	}))
}

// Group 2: gateway-written, dependency-caused.
func TestDependencyErrorsCarryErrorSourceHeader(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		cancel  bool
		status  int
		outcome middleware.GatewayProxyOutcome
	}{
		{"transport failure", errors.New("connection refused"), false, http.StatusBadGateway, middleware.GatewayProxyOutcomeProxyError},
		{"client canceled", context.Canceled, true, statusClientClosedRequest, middleware.GatewayProxyOutcomeClientCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, test.err
			}))
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancel {
				cancel()
			}
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
			rec, reader, spans := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
				_ = director.ServeExec(VanityExecRequest{FunctionID: "f"}, w, r)
			}, req)
			requireOrigin(t, rec, reader, spans, test.status, test.outcome)
		})
	}
}

// Group 3: upstream passthrough.
func TestUpstreamPassthroughOutcome(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			director := upstreamReturning(t, status, `{"error":"upstream"}`, nil)
			rec, reader, spans := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, director.ServeExec(VanityExecRequest{FunctionID: "f"}, w, r))
			}, httptest.NewRequest(http.MethodPost, "/test", nil))
			requireOrigin(t, rec, reader, spans, status, middleware.GatewayProxyOutcomeUpstreamStatus)
			require.JSONEq(t, `{"error":"upstream"}`, rec.Body.String())
		})
	}
}

func TestUpstreamRewrittenTooManyRequestsOutcome(t *testing.T) {
	director := upstreamReturning(t, http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, nil)
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req = req.WithContext(context.WithValue(req.Context(), tooManyRequestsKey, "Upgrade your plan."))
	rec, reader, spans := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, director.ServeExec(VanityExecRequest{FunctionID: "f"}, w, r))
	}, req)
	requireOrigin(t, rec, reader, spans, http.StatusTooManyRequests, middleware.GatewayProxyOutcomeUpstreamStatus)
	require.Contains(t, rec.Body.String(), "Upgrade your plan.")
}

func TestUpstreamPollingPassthroughOutcome(t *testing.T) {
	director := upstreamReturning(t, http.StatusNotFound, `{}`, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/status/abc", nil)
	rec, reader, spans := serveThroughTelemetry(t, director.ServePolling, req)
	requireOrigin(t, rec, reader, spans, http.StatusNotFound, middleware.GatewayProxyOutcomeUpstreamStatus)
}

func TestUpstreamErrorSourceHeaderCannotBeSpoofed(t *testing.T) {
	spoofed := http.Header{}
	spoofed.Set(middleware.ErrorSourceHeader, string(middleware.GatewayProxyOutcomeRejected))

	t.Run("2xx copy is stripped", func(t *testing.T) {
		director := upstreamReturning(t, http.StatusOK, `{}`, spoofed)
		rec, reader, _ := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, director.ServeExec(VanityExecRequest{FunctionID: "f"}, w, r))
		}, httptest.NewRequest(http.MethodPost, "/test", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, rec.Header().Values(middleware.ErrorSourceHeader))
		require.False(t, gatewayMetricHasAttributes(collectGatewayMetrics(t, reader), map[attribute.Key]attribute.Value{
			middleware.GatewayProxyOutcomeMetricAttribute: attribute.StringValue(string(middleware.GatewayProxyOutcomeUpstreamStatus)),
		}))
	})

	t.Run("non-2xx copy is replaced", func(t *testing.T) {
		director := upstreamReturning(t, http.StatusServiceUnavailable, `{}`, spoofed)
		rec, _, _ := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, director.ServeExec(VanityExecRequest{FunctionID: "f"}, w, r))
		}, httptest.NewRequest(http.MethodPost, "/test", nil))
		require.Equal(t, []string{string(middleware.GatewayProxyOutcomeUpstreamStatus)}, rec.Header().Values(middleware.ErrorSourceHeader))
	})
}

func TestSuccessfulResponsesHaveNoErrorSourceOrOutcome(t *testing.T) {
	director := upstreamReturning(t, http.StatusOK, `{}`, nil)
	rec, reader, _ := serveThroughTelemetry(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, director.ServeExec(VanityExecRequest{FunctionID: "f"}, w, r))
	}, httptest.NewRequest(http.MethodPost, "/test", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Values(middleware.ErrorSourceHeader))
	require.True(t, gatewayMetricHasAttributeAbsent(collectGatewayMetrics(t, reader), map[attribute.Key]attribute.Value{
		"http.response.status_code": attribute.Int64Value(http.StatusOK),
	}, middleware.GatewayProxyOutcomeMetricAttribute))
}
