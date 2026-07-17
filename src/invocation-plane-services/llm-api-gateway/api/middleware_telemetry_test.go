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

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

func TestNewContextMiddlewareRecordsTraceParentAndStatus(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() {
		_ = tracerProvider.Shutdown(context.Background())
	}()

	oldTracer := telemetry.Tracer
	telemetry.Tracer = sync.OnceValue(func() trace.Tracer {
		return tracerProvider.Tracer("test")
	})
	defer func() {
		telemetry.Tracer = oldTracer
	}()

	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(oldPropagator)

	cfg := config.Default()

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.POST("/v1/chat/completions", func(ec echo.Context) error {
		return ec.NoContent(http.StatusAccepted)
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")

	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}

	span := spans[0]
	if got := span.Parent().TraceID().String(); got != "11111111111111111111111111111111" {
		t.Fatalf("parent trace id = %q, want %q", got, "11111111111111111111111111111111")
	}
	if got := span.Parent().SpanID().String(); got != "2222222222222222" {
		t.Fatalf("parent span id = %q, want %q", got, "2222222222222222")
	}

	attrs := span.Attributes()
	assertHasAttribute(t, attrs, "http.response.status_code", int64(http.StatusAccepted))
	assertHasAttribute(t, attrs, "url.path", "/v1/chat/completions")
	assertHasAttribute(t, attrs, "http.request.method", http.MethodPost)
}

func TestNewContextMiddlewareRecordsServiceScopedHTTPMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldMeterProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(meterProvider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldMeterProvider)
		_ = meterProvider.Shutdown(context.Background())
	})

	cfg := config.Default()

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.GET("/healthz", func(ec echo.Context) error {
		return ec.NoContent(http.StatusAccepted)
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	metrics := collectMetrics(t, reader)
	assertMetricHasAttributes(t, metrics, "llm_api_gateway_http_requests_total", map[string]string{
		"method":      http.MethodGet,
		"route":       "/healthz",
		"status":      "202",
		"function_id": "none",
	})
	assertMetricHasAttributes(t, metrics, "llm_api_gateway_http_request_duration_seconds", map[string]string{
		"method":      http.MethodGet,
		"route":       "/healthz",
		"status":      "202",
		"function_id": "none",
	})
	assertMetricHasAttributes(t, metrics, "llm_api_gateway_http_active_requests", map[string]string{
		"method":      http.MethodGet,
		"route":       "/healthz",
		"function_id": "none",
	})
}

func TestRequestMetricsIncludeFunctionID(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldMeterProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(meterProvider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldMeterProvider)
		_ = meterProvider.Shutdown(context.Background())
	})

	e := echo.New()
	e.Use(NewContextMiddleware(config.Default()))
	e.POST("/v1/chat/completions", func(ec echo.Context) error {
		return ec.NoContent(http.StatusOK)
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-metrics/provider/model"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	observability := newObservabilityMetrics()
	observability.recordLLMUsage(
		context.Background(),
		"/v1/chat/completions",
		"fn-metrics",
		&models.ChatCompletionUsage{
			PromptTokens:     2,
			PromptTime:       0.02,
			CompletionTokens: 3,
			CompletionTime:   0.03,
			TotalTokens:      5,
			TotalTime:        0.05,
		},
		false,
	)

	content := "first token"
	firstTokenRecorded := false
	recordFirstTokenIfNeeded(
		context.Background(),
		"/v1/chat/completions",
		"fn-metrics",
		observability,
		&models.ChatCompletionChunk{
			Choices: []models.ChatCompletionChunkChoice{{
				Delta: models.ChatCompletionChunkDelta{Content: &content},
			}},
		},
		&firstTokenRecorded,
		time.Now().Add(-time.Millisecond),
	)

	events := make(chan provider.StreamEvent)
	close(events)
	handlers := &OpenAIChatHandlers{handlers: &Handlers{observability: observability}}
	wrapped := handlers.wrapStreamForFinalization(
		context.Background(),
		context.Background(),
		"/v1/chat/completions",
		"fn-metrics",
		nil,
		"",
		events,
		&streamFinalizationState{},
	)
	for range wrapped {
	}

	metrics := collectMetrics(t, reader)
	for _, name := range []string{
		"llm_api_gateway_http_requests_total",
		"llm_api_gateway_http_request_duration_seconds",
		"llm_api_gateway_http_active_requests",
		"llm_api_gateway_llm_tokens_total",
		"llm_api_gateway_provider_time_seconds",
		"llm_api_gateway_stream_first_token_seconds",
		"llm_api_gateway_stream_duration_seconds",
	} {
		assertMetricHasAttributes(t, metrics, name, map[string]string{
			"function_id": "fn-metrics",
		})
	}
}

func assertHasAttribute(t *testing.T, attrs []attribute.KeyValue, key string, want any) {
	t.Helper()

	for _, attr := range attrs {
		if string(attr.Key) != key {
			continue
		}
		switch v := want.(type) {
		case int64:
			if attr.Value.AsInt64() != v {
				t.Fatalf("attribute %q = %d, want %d", key, attr.Value.AsInt64(), v)
			}
		case string:
			if attr.Value.AsString() != v {
				t.Fatalf("attribute %q = %q, want %q", key, attr.Value.AsString(), v)
			}
		default:
			t.Fatalf("unsupported wanted type %T", want)
		}
		return
	}

	t.Fatalf("missing attribute %q", key)
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.Metrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	var out []metricdata.Metrics
	for _, scope := range rm.ScopeMetrics {
		out = append(out, scope.Metrics...)
	}
	return out
}

func assertMetricHasAttributes(
	t *testing.T,
	metrics []metricdata.Metrics,
	name string,
	want map[string]string,
) {
	t.Helper()

	for _, metric := range metrics {
		if metric.Name != name {
			continue
		}
		if metricDataHasAttributes(metric.Data, want) {
			return
		}
		t.Fatalf("metric %q missing labels %#v in %#v", name, want, metric.Data)
	}

	t.Fatalf("missing metric %q in %#v", name, metrics)
}

func metricDataHasAttributes(data metricdata.Aggregation, want map[string]string) bool {
	switch typed := data.(type) {
	case metricdata.Sum[int64]:
		for _, point := range typed.DataPoints {
			if pointHasAttributes(point.Attributes.ToSlice(), want) {
				return true
			}
		}
	case metricdata.Histogram[float64]:
		for _, point := range typed.DataPoints {
			if pointHasAttributes(point.Attributes.ToSlice(), want) {
				return true
			}
		}
	}
	return false
}

func pointHasAttributes(attrs []attribute.KeyValue, want map[string]string) bool {
	got := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		got[string(attr.Key)] = attr.Value.AsString()
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
