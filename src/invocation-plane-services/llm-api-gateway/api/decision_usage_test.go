// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// These tests replace the global meter and tracer providers, so they do not
// run in parallel.

func installDecisionMetricReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(old)
		_ = provider.Shutdown(context.Background())
	})
	return reader
}

// counterSeries returns the counter's value for every label set, keyed by the
// value of one label.
func counterSeries(t *testing.T, reader *sdkmetric.ManualReader, name string, labelFilter map[string]string, keyLabel string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]int64{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				match := true
				for label, want := range labelFilter {
					if got, _ := dp.Attributes.Value(attribute.Key(label)); got.AsString() != want {
						match = false
					}
				}
				if match {
					key, _ := dp.Attributes.Value(attribute.Key(keyLabel))
					out[key.AsString()] += dp.Value
				}
			}
		}
	}
	return out
}

func TestDecisionRoutesObserveResponseUsage(t *testing.T) {
	gzipped := gzipBytes(t, `{"usage":{"input_tokens":42}}`)
	for _, route := range decisionRoutes {
		for _, tc := range []struct {
			name         string
			headers      http.Header
			body         []byte
			wantTokens   int64
			wantObserved int64
			wantAbsent   int64
		}{
			{"usage present", http.Header{"Content-Type": {"application/json"}}, []byte(`{"results":[{"p":0.5}],"usage":{"input_tokens":42,"output_tokens":3}}`), 42, 1, 0},
			{"usage first member", http.Header{"Content-Type": {"application/json"}}, []byte(`{"usage":{"input_tokens":7},"results":[]}`), 7, 1, 0},
			{"usage zero", http.Header{"Content-Type": {"application/json"}}, []byte(`{"usage":{"input_tokens":0}}`), 0, 1, 0},
			{"usage absent", http.Header{"Content-Type": {"application/json"}}, []byte(`{"results":[{"p":0.5}]}`), 0, 0, 1},
			{"usage without input_tokens", http.Header{"Content-Type": {"application/json"}}, []byte(`{"usage":{"output_tokens":3}}`), 0, 0, 1},
			{"usage not an object", http.Header{"Content-Type": {"application/json"}}, []byte(`{"usage":"n/a"}`), 0, 0, 1},
			{"negative input_tokens", http.Header{"Content-Type": {"application/json"}}, []byte(`{"usage":{"input_tokens":-5}}`), 0, 0, 1},
			{"body not JSON", http.Header{"Content-Type": {"text/plain"}}, []byte(`ok`), 0, 0, 1},
			{"encoded body is skipped", http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}, gzipped, 0, 0, 0},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				reader := installDecisionMetricReader(t)
				rec := serveDecisionUpstream(t, route, config.Default(), http.Header{"Accept-Encoding": {"gzip"}}, http.StatusOK, tc.headers, tc.body)

				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, string(tc.body), rec.Body.String(), "the response must reach the client unchanged")

				observations := counterSeries(t, reader, "llm_api_gateway_decision_usage_observations_total", map[string]string{"endpoint": route.path}, "status")
				require.EqualValues(t, tc.wantObserved, observations["observed"], "observed")
				require.EqualValues(t, tc.wantAbsent, observations["absent"], "absent")

				tokens := counterSeries(t, reader, "llm_api_gateway_llm_tokens_total", map[string]string{"endpoint": route.path, "function_id": "fn-alpha"}, "token_type")
				require.EqualValues(t, tc.wantTokens, tokens["prompt"], "prompt tokens")
				require.Zero(t, tokens["completion"], "only the input side is observed")
				require.Zero(t, tokens["total"])
			})
		}
	}
}

func TestDecisionRoutesDoNotObserveErrorResponses(t *testing.T) {
	for _, route := range decisionRoutes {
		reader := installDecisionMetricReader(t)
		rec := serveDecisionUpstream(t, route, config.Default(), nil, http.StatusBadRequest,
			http.Header{"Content-Type": {"application/json"}}, []byte(`{"usage":{"input_tokens":42}}`))

		require.Equal(t, http.StatusBadRequest, rec.Code)
		observations := counterSeries(t, reader, "llm_api_gateway_decision_usage_observations_total", map[string]string{"endpoint": route.path}, "status")
		require.Zero(t, observations["observed"]+observations["absent"], route.name)
		tokens := counterSeries(t, reader, "llm_api_gateway_llm_tokens_total", map[string]string{"endpoint": route.path}, "token_type")
		require.Empty(t, tokens, route.name)
	}
}

func TestDecisionRoutesSetSpanAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
	oldTracer := telemetry.Tracer
	telemetry.Tracer = sync.OnceValue(func() trace.Tracer { return tracerProvider.Tracer("test") })
	t.Cleanup(func() { telemetry.Tracer = oldTracer })

	for _, route := range decisionRoutes {
		recorder.Reset()
		rec := serveDecisionUpstream(t, route, config.Default(), nil, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{}`))
		require.Equal(t, http.StatusOK, rec.Code)

		var found bool
		for _, span := range recorder.Ended() {
			attrs := attribute.NewSet(span.Attributes()...)
			endpoint, ok := attrs.Value("gateway.endpoint")
			if !ok {
				continue
			}
			found = true
			require.Equal(t, route.path, endpoint.AsString())
			functionID, _ := attrs.Value("nvcf.function.id")
			require.Equal(t, "fn-alpha", functionID.AsString())
			orgID, _ := attrs.Value("nvcf.org.id")
			require.Equal(t, "org-test", orgID.AsString())
		}
		require.True(t, found, "%s: no span carries gateway.endpoint", route.name)
	}
}

func TestDecisionRoutesSeededByTelemetry(t *testing.T) {
	require.Equal(t, []string{decisionsEndpointPath, systemOneEndpointPath}, telemetry.DecisionRoutes())
}
