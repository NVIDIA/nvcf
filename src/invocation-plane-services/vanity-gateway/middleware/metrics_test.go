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
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestServerTelemetryMiddlewareRecordsStatusAndRouteMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	r := chi.NewRouter()
	r.Use(ServerTelemetryMiddleware(otelhttp.WithMeterProvider(provider)))
	r.Get("/limited", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	r.Get("/requests/{requestId}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	r.Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		AddOpenAIRequestMetricAttributes(r.Context(), "meta/llama-3", "func-123")
		w.WriteHeader(http.StatusAccepted)
	})

	serve(r, http.MethodGet, "/limited")
	serve(r, http.MethodGet, "/requests/abc123")
	serve(r, http.MethodPost, "/v1/chat/completions")

	metrics := collectMetrics(t, reader)

	require.True(t, hasMetricPointWithAttributes(metrics, "http.server.request.duration", map[attribute.Key]attribute.Value{
		"http.request.method":       attribute.StringValue(http.MethodGet),
		"http.response.status_code": attribute.Int64Value(http.StatusTooManyRequests),
		"http.route":                attribute.StringValue("/limited"),
	}))
	require.True(t, hasMetricPointWithAttributes(metrics, "http.server.request.duration", map[attribute.Key]attribute.Value{
		"http.request.method":       attribute.StringValue(http.MethodGet),
		"http.response.status_code": attribute.Int64Value(http.StatusGatewayTimeout),
		"http.route":                attribute.StringValue("/requests/{requestId}"),
	}))
	require.True(t, hasMetricPointWithAttributes(metrics, "http.server.request.duration", map[attribute.Key]attribute.Value{
		"http.request.method":       attribute.StringValue(http.MethodPost),
		"http.response.status_code": attribute.Int64Value(http.StatusAccepted),
		"http.route":                attribute.StringValue("/v1/chat/completions"),
		"openai_model_name":         attribute.StringValue("meta/llama-3"),
		"function_id":               attribute.StringValue("func-123"),
	}))
}

func TestHTTPDurationHistogramBoundaries(t *testing.T) {
	expectedBoundaries := []float64{
		0.1, 0.25, 0.5, 0.75, 1, 2, 5, 10,
		15, 30, 60, 120, 300, 600, 900,
	}
	reader := sdkmetric.NewManualReader()
	provider := newHTTPMeterProvider(reader)
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	handler := ServerTelemetryMiddleware(otelhttp.WithMeterProvider(provider))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithMeterProvider(provider))}
	response, err := client.Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	metrics := collectMetrics(t, reader)
	require.Equal(t, expectedBoundaries, histogramBounds(t, metrics, "http.server.request.duration"))
	require.Equal(t, expectedBoundaries, histogramBounds(t, metrics, "http.client.request.duration"))
}

func TestAddOpenAIRequestMetricAttributesCopiesModelName(t *testing.T) {
	const value = "meta/llama-3"
	// Model names parsed by goccy/go-json can share the request body's backing storage.
	backing := strings.Repeat("x", 1<<20) + value
	modelName := backing[len(backing)-len(value):]
	labeler := &otelhttp.Labeler{}
	ctx := otelhttp.ContextWithLabeler(context.Background(), labeler)

	AddOpenAIRequestMetricAttributes(ctx, modelName, "")

	attrs := labeler.Get()
	require.Len(t, attrs, 1)
	require.Equal(t, openAIModelNameAttribute, attrs[0].Key)
	got := attrs[0].Value.AsString()
	require.Equal(t, modelName, got)
	require.False(t, unsafe.StringData(modelName) == unsafe.StringData(got))
	runtime.KeepAlive(backing)
}

func serve(handler http.Handler, method, path string) {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.Metrics {
	t.Helper()

	var resourceMetrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &resourceMetrics))

	var metrics []metricdata.Metrics
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		metrics = append(metrics, scopeMetrics.Metrics...)
	}
	return metrics
}

func hasMetricPointWithAttributes(metrics []metricdata.Metrics, name string, want map[attribute.Key]attribute.Value) bool {
	for _, metric := range metrics {
		if metric.Name != name {
			continue
		}
		if histogramHasAttributes(metric.Data, want) {
			return true
		}
	}
	return false
}

func histogramHasAttributes(data metricdata.Aggregation, want map[attribute.Key]attribute.Value) bool {
	switch histogram := data.(type) {
	case metricdata.Histogram[float64]:
		for _, point := range histogram.DataPoints {
			if dataPointHasAttributes(point.Attributes, want) {
				return true
			}
		}
	}
	return false
}

func histogramBounds(t *testing.T, metrics []metricdata.Metrics, name string) []float64 {
	t.Helper()

	for _, metric := range metrics {
		if metric.Name != name {
			continue
		}
		histogram, ok := metric.Data.(metricdata.Histogram[float64])
		if ok && len(histogram.DataPoints) > 0 {
			return histogram.DataPoints[0].Bounds
		}
	}

	require.FailNow(t, "histogram not found", name)
	return nil
}

func dataPointHasAttributes(attrs attribute.Set, want map[attribute.Key]attribute.Value) bool {
	for key, wantValue := range want {
		actualValue, ok := attrs.Value(key)
		if !ok || actualValue != wantValue {
			return false
		}
	}
	return true
}
