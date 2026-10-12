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
	config "ai-api-gateway-service/gateway_config"
	"ai-api-gateway-service/middleware"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const processingMetric = "nvcf_vanity_gateway_pre_upstream_processing_seconds"

func processingReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return reader
}

func processingHandler(t *testing.T, rt http.RoundTripper, kind config.FunctionType, offline string, shadow bool) (http.Handler, *TrafficShadower) {
	t.Helper()
	transport := middleware.TracedRoundTripper(rt)
	vanity, err := NewVanityDirector("http://invocation.example", transport)
	require.NoError(t, err)
	llm, err := NewLLMGatewayDirector("http://llm.example", transport)
	require.NoError(t, err)
	primary := config.ModelFunctionDetails{ModelName: "example/primary", FunctionID: "primary-function", FunctionType: kind, OfflineMessage: offline}
	if shadow {
		primary.Shadows = []config.ShadowConfig{{ModelName: "example/shadow"}}
	}
	mappings := llmMappings(primary)
	mappings.OpenAI.ChatCompletions["shadow"] = config.ModelFunctionDetails{ModelName: "example/shadow", FunctionID: "shadow-function", FunctionType: kind}
	mappings.OpenAI.ChatCompletions["private"] = config.ModelFunctionDetails{ModelName: "private/model", FunctionID: "private-function", FunctionType: kind}
	shadower := NewTrafficShadower(2, 10*time.Second)
	director, err := NewModelDirector(mappings, regexp.MustCompile("^private/"), vanity, llm, shadower)
	require.NoError(t, err)
	router := chi.NewRouter()
	router.Use(middleware.ServerTelemetryMiddleware())
	router.Post("/v1/chat/completions", director.ServeChatCompletions)
	return router, shadower
}

func metricPoints(t *testing.T, reader *sdkmetric.ManualReader, name string) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var result []metricdata.HistogramDataPoint[float64]
	for _, metric := range collectGatewayMetrics(t, reader) {
		if metric.Name == name {
			histogram, ok := metric.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			result = append(result, histogram.DataPoints...)
		}
	}
	return result
}

func awaitProcessingSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for processing test stage")
	}
}

// Each read gates a separate stream chunk, independently of the header gate.
type processingBody struct {
	started chan struct{}
	release chan struct{}
	chunks  int
}

func (b *processingBody) Read(p []byte) (int, error) {
	if b.chunks == 2 {
		return 0, io.EOF
	}
	b.started <- struct{}{}
	<-b.release
	b.chunks++
	return copy(p, "data: chunk\n\n"), nil
}
func (*processingBody) Close() error { return nil }

func TestPreUpstreamProcessingExcludesHeadersAndStreamingBody(t *testing.T) {
	for _, kind := range []config.FunctionType{"", config.FunctionTypeLLM} {
		t.Run(string(kind), func(t *testing.T) {
			reader := processingReader(t)
			headersStarted := make(chan struct{}, 1)
			releaseHeaders := make(chan struct{}, 1)
			var headersEntry time.Time
			done := make(chan struct{})
			body := &processingBody{started: make(chan struct{}, 2), release: make(chan struct{}, 2)}
			// Unblock every gate even if an assertion fails.
			t.Cleanup(func() {
				releaseHeaders <- struct{}{}
				body.release <- struct{}{}
				body.release <- struct{}{}
				awaitProcessingSignal(t, done)
			})
			handler, _ := processingHandler(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				headersEntry = time.Now()
				headersStarted <- struct{}{}
				<-releaseHeaders
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: r}, nil
			}), kind, "", false)
			go func() {
				defer close(done)
				handler.ServeHTTP(httptest.NewRecorder(), openAIRequest(t, "/v1/chat/completions", `{"model":"example/primary","stream":true}`))
			}()
			awaitProcessingSignal(t, headersStarted)
			points := metricPoints(t, reader, processingMetric)
			require.Len(t, points, 1)
			require.EqualValues(t, 1, points[0].Count)
			require.Equal(t, attribute.NewSet(attribute.String("http.route", "/v1/chat/completions"), attribute.String("outcome", "dispatched"), attribute.String("openai_model_name", "example/primary")), points[0].Attributes)
			require.Equal(t, []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}, points[0].Bounds)
			require.Empty(t, metricPoints(t, reader, "http.client.request.duration"))
			require.Empty(t, metricPoints(t, reader, "http.server.request.duration"))
			headersWait := time.Since(headersEntry).Seconds()
			releaseHeaders <- struct{}{}
			awaitProcessingSignal(t, body.started)
			require.Equal(t, points[0].Sum, metricPoints(t, reader, processingMetric)[0].Sum)
			clientPoints := metricPoints(t, reader, "http.client.request.duration")
			require.Len(t, clientPoints, 1)
			require.GreaterOrEqual(t, clientPoints[0].Sum, headersWait)
			require.Empty(t, metricPoints(t, reader, "http.server.request.duration"))
			body.release <- struct{}{}
			awaitProcessingSignal(t, body.started)
			require.Equal(t, points[0].Sum, metricPoints(t, reader, processingMetric)[0].Sum)
			bodyWait := time.Since(headersEntry).Seconds()
			body.release <- struct{}{}
			awaitProcessingSignal(t, done)
			final := metricPoints(t, reader, processingMetric)
			require.EqualValues(t, 1, final[0].Count)
			require.Equal(t, points[0].Sum, final[0].Sum)
			require.GreaterOrEqual(t, metricPoints(t, reader, "http.server.request.duration")[0].Sum, bodyWait)
		})
	}
}

func TestPreUpstreamProcessingLocalAndProxyOutcomes(t *testing.T) {
	for _, kind := range []config.FunctionType{"", config.FunctionTypeLLM} {
		for _, tc := range []struct {
			name, body, offline, outcome, model string
			canceled, proxyError                bool
			status                              int
		}{
			{name: "unknown model", body: `{"model":"user-supplied-unknown"}`, outcome: "gateway_rejected", status: 404},
			{name: "invalid body", body: `{`, outcome: "gateway_rejected", status: 500},
			{name: "offline", body: `{"model":"example/primary"}`, offline: "unavailable", outcome: "gateway_rejected", model: "example/primary", status: 503},
			{name: "cancel before dispatch", body: `{"model":"example/primary"}`, canceled: true, proxyError: true, outcome: "client_canceled", model: "example/primary", status: 499},
			{name: "proxy error", body: `{"model":"example/primary"}`, proxyError: true, outcome: "dispatched", model: "example/primary", status: 502},
			{name: "private model", body: `{"model":"private/model"}`, outcome: "dispatched", status: 200},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				reader := processingReader(t)
				calls := 0
				handler, _ := processingHandler(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if tc.proxyError {
						return nil, errors.New("upstream failed")
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
				}), kind, tc.offline, false)
				req := openAIRequest(t, "/v1/chat/completions", tc.body)
				if tc.canceled {
					ctx, cancel := context.WithCancel(req.Context())
					cancel()
					req = req.WithContext(ctx)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				require.Equal(t, tc.status, rec.Code)
				points := metricPoints(t, reader, processingMetric)
				require.Len(t, points, 1)
				require.EqualValues(t, 1, points[0].Count)
				outcome, _ := points[0].Attributes.Value("outcome")
				require.Equal(t, tc.outcome, outcome.AsString())
				model, hasModel := points[0].Attributes.Value("openai_model_name")
				require.Equal(t, tc.model != "", hasModel)
				require.Equal(t, tc.model, model.AsString())
				if tc.outcome == "gateway_rejected" {
					require.Zero(t, calls)
				}
				if tc.proxyError {
					expected := "gateway_proxy_error"
					if tc.canceled {
						expected = "client_canceled"
					}
					require.Equal(t, expected, rec.Header().Get(middleware.ErrorSourceHeader))
					require.True(t, gatewayMetricHasAttributes(collectGatewayMetrics(t, reader), map[attribute.Key]attribute.Value{middleware.GatewayProxyOutcomeMetricAttribute: attribute.StringValue(expected)}))
				}
			})
		}
	}
}

func TestPreUpstreamProcessingShadowIsolation(t *testing.T) {
	for _, kind := range []config.FunctionType{"", config.FunctionTypeLLM} {
		t.Run(string(kind), func(t *testing.T) {
			reader := processingReader(t)
			shadowStarted := make(chan struct{})
			handler, shadower := processingHandler(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if isShadowRequest(r) {
					close(shadowStarted)
					return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("shadow error")), Request: r}, nil
				}
				select {
				case <-shadowStarted:
				case <-time.After(10 * time.Second):
					return nil, errors.New("shadow replay did not start")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
			}), kind, "", true)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, openAIRequest(t, "/v1/chat/completions", `{"model":"example/primary"}`))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, shadower.sem.Acquire(ctx, 2))
			shadower.sem.Release(2)
			require.Equal(t, 200, rec.Code)
			points := metricPoints(t, reader, processingMetric)
			require.Len(t, points, 1)
			require.EqualValues(t, 1, points[0].Count)
			model, _ := points[0].Attributes.Value("openai_model_name")
			require.Equal(t, "example/primary", model.AsString())
			clients := metricPoints(t, reader, "http.client.request.duration")
			require.Len(t, clients, 2)
			populations := map[bool]uint64{}
			for _, point := range clients {
				shadow, ok := point.Attributes.Value("nvcf.shadow")
				require.True(t, ok)
				populations[shadow.AsBool()] += point.Count
			}
			require.Equal(t, map[bool]uint64{false: 1, true: 1}, populations)
			for _, point := range metricPoints(t, reader, "http.server.request.duration") {
				shadow, _ := point.Attributes.Value("nvcf.shadow")
				if shadow.AsBool() {
					continue
				}
				require.EqualValues(t, 1, point.Count)
				model, _ := point.Attributes.Value("openai_model_name")
				require.Equal(t, "example/primary", model.AsString())
				_, hasOutcome := point.Attributes.Value(middleware.GatewayProxyOutcomeMetricAttribute)
				require.False(t, hasOutcome, "shadow failure must not change primary error origin")
				function, _ := point.Attributes.Value("function_id")
				require.Equal(t, "primary-function", function.AsString())
			}
		})
	}
}
