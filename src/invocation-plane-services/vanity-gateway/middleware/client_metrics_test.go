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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type clientMetricTestTransport func(*http.Request) (*http.Response, error)

func (f clientMetricTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientMetricClassification(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		status                int
		source                string
		err                   error
		wantSource, wantError string
		shadow                bool
	}{
		{name: "success", status: 200},
		{name: "success ignores source", status: 204, source: "worker"},
		{name: "redirect", status: 302, source: "worker", wantSource: "worker"},
		{name: "invocation service", status: 400, source: "invocation_service", wantSource: "invocation_service"},
		{name: "worker", status: 500, source: "worker", wantSource: "worker"},
		{name: "missing source", status: 503, wantSource: "unknown"},
		{name: "unbounded source", status: 500, source: "arbitrary-worker-123", wantSource: "unknown"},
		{name: "canceled", err: context.Canceled, wantError: "canceled"},
		{name: "wrapped cancellation", err: fmt.Errorf("request: %w", context.Canceled), wantError: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, wantError: "timeout"},
		{name: "network timeout", err: &net.DNSError{IsTimeout: true}, wantError: "timeout"},
		{name: "transport", err: errors.New("connection failed"), wantError: "transport_error"},
		{name: "shadow inference", status: 500, source: "worker", wantSource: "worker", shadow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			provider := newHTTPMeterProvider(reader)
			previous := otel.GetMeterProvider()
			otel.SetMeterProvider(provider)
			t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
			transport := TracedRoundTripper(clientMetricTestTransport(func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				h := http.Header{}
				h.Set(ErrorSourceHeader, tc.source)
				return &http.Response{StatusCode: tc.status, Header: h, Body: io.NopCloser(strings.NewReader("response"))}, nil
			}))
			req, err := http.NewRequest(http.MethodPost, "http://upstream.test/infer", nil)
			require.NoError(t, err)
			if tc.shadow {
				req = req.WithContext(ShadowTelemetryContext(req.Context()))
			}
			resp, err := transport.RoundTrip(req)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.status, resp.StatusCode)
				require.Equal(t, tc.source, resp.Header.Get(ErrorSourceHeader))
				body, readErr := io.ReadAll(resp.Body)
				require.NoError(t, readErr)
				require.Equal(t, "response", string(body))
				require.NoError(t, resp.Body.Close())
			}
			var points []metricdata.HistogramDataPoint[float64]
			for _, m := range collectMetrics(t, reader) {
				if m.Name == "http.client.request.duration" {
					points = append(points, m.Data.(metricdata.Histogram[float64]).DataPoints...)
				}
			}
			require.Len(t, points, 1)
			require.EqualValues(t, 1, points[0].Count)
			shadow, present := points[0].Attributes.Value("nvcf.shadow")
			require.True(t, present)
			require.Equal(t, tc.shadow, shadow.AsBool())
			for key, want := range map[attribute.Key]string{"gateway_call_kind": "inference", "error_source": tc.wantSource, "error.type": tc.wantError} {
				got, present := points[0].Attributes.Value(key)
				if want == "" {
					require.False(t, present, "unexpected %s: %s", key, got.AsString())
				} else {
					require.True(t, present, "missing %s", key)
					require.Equal(t, want, got.AsString(), "%s", key)
				}
			}
		})
	}
}

func TestClientMetricLabelsDoNotContaminateParentOrNextCall(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := newHTTPMeterProvider(reader)
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
	parent := &otelhttp.Labeler{}
	parent.Add(attribute.String("existing", "retained"))
	ctx := otelhttp.ContextWithLabeler(context.Background(), parent)
	transport := TracedRoundTripper(clientMetricTestTransport(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set(ErrorSourceHeader, "worker")
		status := 500
		if r.URL.Path == "/success" {
			status = 200
		}
		return &http.Response{StatusCode: status, Header: h, Body: http.NoBody}, nil
	}))
	for _, path := range []string{"/failure", "/success"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://upstream.test"+path, nil)
		require.NoError(t, err)
		res, err := transport.RoundTrip(req)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
	}
	require.Equal(t, []attribute.KeyValue{attribute.String("existing", "retained")}, parent.Get())
	metrics := collectMetrics(t, reader)
	require.True(t, hasMetricPointWithAttributes(metrics, "http.client.request.duration", map[attribute.Key]attribute.Value{
		"http.response.status_code": attribute.Int64Value(500), "gateway_call_kind": attribute.StringValue("inference"),
		"error_source": attribute.StringValue("worker"), "existing": attribute.StringValue("retained"),
	}))
	require.True(t, hasMetricPointWithoutAttribute(metrics, "http.client.request.duration", map[attribute.Key]attribute.Value{
		"http.response.status_code": attribute.Int64Value(200), "gateway_call_kind": attribute.StringValue("inference"),
		"existing": attribute.StringValue("retained"),
	}, "error_source"))
}

func TestClientMetricPrometheusLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	require.NoError(t, err)
	provider := newHTTPMeterProvider(exporter)
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
	req, err := http.NewRequest(http.MethodGet, "http://upstream.test", nil)
	require.NoError(t, err)
	_, err = TracedRoundTripper(clientMetricTestTransport(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })).RoundTrip(req)
	require.ErrorIs(t, err, context.Canceled)
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "http_client_request_duration_seconds" {
			continue
		}
		require.Len(t, family.Metric, 1)
		labels := map[string]string{}
		for _, label := range family.Metric[0].Label {
			labels[label.GetName()] = label.GetValue()
		}
		require.Equal(t, "canceled", labels["error_type"])
		require.Equal(t, "inference", labels["gateway_call_kind"])
		require.Equal(t, "false", labels["nvcf_shadow"])
		require.NotContains(t, labels, "error_source")
		return
	}
	t.Fatal("client duration histogram missing from Prometheus export")
}

func TestClientMetricDefaultTransport(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := newHTTPMeterProvider(reader)
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); require.NoError(t, provider.Shutdown(context.Background())) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ErrorSourceHeader, "worker")
		w.WriteHeader(502)
	}))
	t.Cleanup(server.Close)
	client := http.Client{Transport: TracedRoundTripper(nil)}
	res, err := client.Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())
	require.Equal(t, 502, res.StatusCode)
	require.True(t, hasMetricPointWithAttributes(collectMetrics(t, reader), "http.client.request.duration", map[attribute.Key]attribute.Value{
		"gateway_call_kind": attribute.StringValue("inference"), "error_source": attribute.StringValue("worker"),
	}))
}
