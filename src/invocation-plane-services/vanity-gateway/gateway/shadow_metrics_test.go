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
	"testing"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestShadowDroppedMetricInitializesAndCountsPerModel(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	counter, err := newShadowDroppedCounter(provider.Meter(shadowMetricsScope))
	require.NoError(t, err)

	previousCounter := shadowDroppedCounter
	shadowDroppedCounter = counter
	t.Cleanup(func() {
		shadowDroppedCounter = previousCounter
	})

	initializeShadowDropMetrics([]string{"shadow-a", "shadow-b"})
	recordShadowDispatchSummary(
		context.Background(),
		[]string{"shadow-a", "shadow-b"},
		0,
		2,
		[]string{
			shadowDroppedReasonConcurrencyLimit,
			shadowDroppedReasonBodyReadError,
		},
		[]string{"shadow-a", "shadow-b"},
	)

	require.Equal(t, map[shadowDropMetricLabels]int64{
		{model: "shadow-a", reason: shadowDroppedReasonBodyReadError}:    0,
		{model: "shadow-a", reason: shadowDroppedReasonBodyRewriteError}: 0,
		{model: "shadow-a", reason: shadowDroppedReasonConcurrencyLimit}: 1,
		{model: "shadow-b", reason: shadowDroppedReasonBodyReadError}:    1,
		{model: "shadow-b", reason: shadowDroppedReasonBodyRewriteError}: 0,
		{model: "shadow-b", reason: shadowDroppedReasonConcurrencyLimit}: 0,
	}, collectShadowDroppedCounts(t, reader))
}

type shadowDropMetricLabels struct {
	model  string
	reason string
}

func collectShadowDroppedCounts(t *testing.T, reader *sdkmetric.ManualReader) map[shadowDropMetricLabels]int64 {
	t.Helper()

	var resourceMetrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &resourceMetrics))

	counts := make(map[shadowDropMetricLabels]int64)
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		for _, metric := range scopeMetrics.Metrics {
			if metric.Name != shadowDroppedMetricName {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, point := range sum.DataPoints {
				model, ok := point.Attributes.Value(shadowDroppedModelLabel)
				require.True(t, ok)
				reason, ok := point.Attributes.Value(shadowDroppedReasonLabel)
				require.True(t, ok)
				counts[shadowDropMetricLabels{
					model:  model.AsString(),
					reason: reason.AsString(),
				}] = point.Value
			}
		}
	}
	return counts
}
