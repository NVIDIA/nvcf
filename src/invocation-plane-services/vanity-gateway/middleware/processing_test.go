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
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestPreUpstreamLocalCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, want        string
		outcome           GatewayProxyOutcome
		canceled, expired bool
	}{
		{name: "local success", want: "local"},
		{name: "local rejection", want: "gateway_rejected", outcome: GatewayProxyOutcomeRejected},
		{name: "proxy error before transport", want: "gateway_proxy_error", outcome: GatewayProxyOutcomeProxyError},
		{name: "canceled without transport", want: "client_canceled", canceled: true},
		{name: "expired without transport", want: "client_canceled", expired: true},
		{name: "canceled rejection", want: "client_canceled", outcome: GatewayProxyOutcomeRejected, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			provider := newHTTPMeterProvider(reader)
			previous := otel.GetMeterProvider()
			otel.SetMeterProvider(provider)
			t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
			req := httptest.NewRequest(http.MethodGet, "/unbounded-user-path", nil)
			if tc.canceled {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			if tc.expired {
				ctx, cancel := context.WithDeadline(req.Context(), time.Now().Add(-time.Hour))
				defer cancel()
				req = req.WithContext(ctx)
			}
			handler := ServerTelemetryMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				AddGatewayProxyOutcomeMetricAttribute(r.Context(), tc.outcome)
				w.WriteHeader(http.StatusNoContent)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), req)
			metrics := collectMetrics(t, reader)
			found := false
			for _, metric := range metrics {
				if metric.Name != preUpstreamMetricName {
					continue
				}
				found = true
				histogram := metric.Data.(metricdata.Histogram[float64])
				require.Len(t, histogram.DataPoints, 1)
				point := histogram.DataPoints[0]
				require.EqualValues(t, 1, point.Count)
				outcome, _ := point.Attributes.Value("outcome")
				require.Equal(t, tc.want, outcome.AsString())
				route, _ := point.Attributes.Value("http.route")
				require.Equal(t, "unknown", route.AsString())
				require.Equal(t, 2, point.Attributes.Len())
			}
			require.True(t, found)
		})
	}
}

func TestPreUpstreamPrometheusName(t *testing.T) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	require.NoError(t, err)
	provider := newHTTPMeterProvider(exporter)
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
	ServerTelemetryMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/info", nil))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == preUpstreamMetricName {
			require.Len(t, family.Metric, 1)
			require.EqualValues(t, 1, family.Metric[0].Histogram.GetSampleCount())
			require.Equal(t, 0.001, family.Metric[0].Histogram.Bucket[0].GetUpperBound())
			return
		}
	}
	t.Fatal("documented Prometheus histogram not found")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientMetricCallLabels(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		status                         int
		source                         string
		err                            error
		health                         bool
		wantKind, wantSource, wantType string
	}{
		{name: "ok", status: 200, wantKind: "inference"},
		{name: "worker error", status: 503, source: "worker", wantKind: "inference", wantSource: "worker"},
		{name: "service error", status: 504, source: "invocation_service", wantKind: "inference", wantSource: "invocation_service"},
		{name: "unbounded source", status: 500, source: "arbitrary", wantKind: "inference", wantSource: "unknown"},
		{name: "no header", status: 502, wantKind: "inference", wantSource: "unknown"},
		{name: "health probe", status: 200, health: true, wantKind: "health"},
		{name: "canceled", err: context.Canceled, wantKind: "inference", wantType: "canceled"},
		{name: "timeout", err: context.DeadlineExceeded, wantKind: "inference", wantType: "timeout"},
		{name: "transport", err: errors.New("connection refused"), wantKind: "inference", wantType: "transport_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			provider := newHTTPMeterProvider(reader)
			previous := otel.GetMeterProvider()
			otel.SetMeterProvider(provider)
			t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
			rt := TracedRoundTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				header := http.Header{}
				if tc.source != "" {
					header.Set(ErrorSourceHeader, tc.source)
				}
				return &http.Response{StatusCode: tc.status, Header: header, Body: http.NoBody, Request: r}, nil
			}))
			req := httptest.NewRequest(http.MethodGet, "http://upstream.example/x", nil)
			if tc.health {
				req = req.WithContext(WithHealthCall(req.Context()))
			}
			resp, err := rt.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			require.Equal(t, tc.err != nil, err != nil)
			found := false
			for _, metric := range collectMetrics(t, reader) {
				if metric.Name != "http.client.request.duration" {
					continue
				}
				points := metric.Data.(metricdata.Histogram[float64]).DataPoints
				require.Len(t, points, 1)
				found = true
				kind, _ := points[0].Attributes.Value("gateway_call_kind")
				require.Equal(t, tc.wantKind, kind.AsString())
				source, hasSource := points[0].Attributes.Value("error_source")
				require.Equal(t, tc.wantSource != "", hasSource)
				require.Equal(t, tc.wantSource, source.AsString())
				typ, _ := points[0].Attributes.Value("error.type")
				require.Equal(t, tc.wantType, typ.AsString())
			}
			require.True(t, found)
		})
	}
}
