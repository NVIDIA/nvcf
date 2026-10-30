// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// firstScrapeSeries returns the label sets of every series metric name holds
// after InitializeMetrics and before any traffic, each with its value.
func firstScrapeSeries(t *testing.T, name string) map[attribute.Distinct]int64 {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldProvider)
		_ = provider.Shutdown(context.Background())
	})

	InitializeMetrics()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	series := map[attribute.Distinct]int64{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					series[dp.Attributes.Equivalent()] = dp.Value
				}
			default:
				t.Fatalf("unexpected data type %T for %s", data, name)
			}
		}
	}
	return series
}

func labels(kv ...string) attribute.Distinct {
	attrs := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		attrs = append(attrs, attribute.String(kv[i], kv[i+1]))
	}
	set := attribute.NewSet(attrs...)
	return set.Equivalent()
}

func TestDecisionUsageObservationsStartAtZero(t *testing.T) {
	series := firstScrapeSeries(t, "llm_api_gateway_decision_usage_observations_total")

	for _, endpoint := range []string{"/v1/decisions", "/v1/systemone"} {
		for _, status := range []string{"observed", "absent"} {
			value, ok := series[labels("endpoint", endpoint, "status", status, "function_id", "none")]
			require.True(t, ok, "missing zero series %s %s in %v", endpoint, status, series)
			require.Zero(t, value)
		}
	}
	require.Len(t, series, 4, "only the bounded seed series may exist before traffic")
}

func TestDecisionRoutesHTTPRequestSeriesStartAtZero(t *testing.T) {
	requests := firstScrapeSeries(t, "llm_api_gateway_http_requests_total")
	active := firstScrapeSeries(t, "llm_api_gateway_http_active_requests")

	for _, route := range []string{"/v1/decisions", "/v1/systemone"} {
		for _, status := range []string{"200", "400", "401", "404", "429", "500", "502", "504"} {
			value, ok := requests[labels("method", "POST", "route", route, "function_id", "none", "status", status)]
			require.True(t, ok, "missing zero request series %s %s", route, status)
			require.Zero(t, value)
		}
		value, ok := active[labels("method", "POST", "route", route, "function_id", "none")]
		require.True(t, ok, "missing zero active series %s", route)
		require.Zero(t, value)
	}
}
