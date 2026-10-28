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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	config "ai-api-gateway-service/gateway_config"
	"ai-api-gateway-service/middleware"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestUpstreamHealthCheckClientMetricLabels(t *testing.T) {
	for _, path := range []string{"/health", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			previous := otel.GetMeterProvider()
			otel.SetMeterProvider(provider)
			t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path {
					t.Errorf("health path: got %s, want %s", r.URL.Path, path)
				}
				w.Header().Set(middleware.ErrorSourceHeader, "invocation_service")
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(server.Close)
			client := http.Client{Transport: middleware.TracedRoundTripper(http.DefaultTransport)}
			check, err := upstreamHealthCheck(client, "upstream", server.URL, path)
			require.NoError(t, err)
			require.Error(t, check.Check(context.Background()))
			var rm metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(context.Background(), &rm))
			found := false
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name != "http.client.request.duration" {
						continue
					}
					found = true
					points := m.Data.(metricdata.Histogram[float64]).DataPoints
					require.Len(t, points, 1)
					kind, ok := points[0].Attributes.Value("gateway_call_kind")
					require.True(t, ok)
					require.Equal(t, "health", kind.AsString())
					source, ok := points[0].Attributes.Value("error_source")
					require.True(t, ok)
					require.Equal(t, "invocation_service", source.AsString())
				}
			}
			require.True(t, found)
		})
	}
}

func TestPollingClientMetricCallKind(t *testing.T) {
	reader := processingReader(t)
	director, err := NewVanityDirector("https://nvcf.example.test", middleware.TracedRoundTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	director.ServePolling(rec, httptest.NewRequest(http.MethodGet, "/v1/status/abc", nil))
	require.Equal(t, 200, rec.Code)
	clients := metricPoints(t, reader, "http.client.request.duration")
	require.Len(t, clients, 1)
	kind, ok := clients[0].Attributes.Value("gateway_call_kind")
	require.True(t, ok)
	require.Equal(t, "poll", kind.AsString())
}

func TestProxyClientMetricLabelsStaySeparateFromServer(t *testing.T) {
	for _, kind := range []config.FunctionType{"", config.FunctionTypeLLM} {
		t.Run(string(kind), func(t *testing.T) {
			reader := processingReader(t)
			handler, _ := processingHandler(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				header := http.Header{}
				header.Set(middleware.ErrorSourceHeader, "worker")
				return &http.Response{StatusCode: 500, Header: header, Body: io.NopCloser(strings.NewReader("upstream failure")), Request: r}, nil
			}), kind, "", false)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, openAIRequest(t, "/v1/chat/completions", `{"model":"example/primary"}`))
			require.Equal(t, 500, rec.Code)
			require.Equal(t, "upstream failure", rec.Body.String())
			require.Equal(t, "upstream_status", rec.Header().Get(middleware.ErrorSourceHeader))
			clients := metricPoints(t, reader, "http.client.request.duration")
			require.Len(t, clients, 1)
			for k, expected := range map[string]string{"gateway_call_kind": "inference", "error_source": "worker"} {
				value, ok := clients[0].Attributes.Value(attribute.Key(k))
				require.True(t, ok)
				require.Equal(t, expected, value.AsString())
			}
			shadow, ok := clients[0].Attributes.Value("nvcf.shadow")
			require.True(t, ok)
			require.False(t, shadow.AsBool())
			servers := metricPoints(t, reader, "http.server.request.duration")
			require.Len(t, servers, 1)
			outcome, ok := servers[0].Attributes.Value(middleware.GatewayProxyOutcomeMetricAttribute)
			require.True(t, ok)
			require.Equal(t, "upstream_status", outcome.AsString())
			for _, k := range []attribute.Key{"gateway_call_kind", "error_source", "error.type"} {
				_, ok := servers[0].Attributes.Value(k)
				require.False(t, ok, "client attribute %s leaked onto server metric", k)
			}
		})
	}
}
