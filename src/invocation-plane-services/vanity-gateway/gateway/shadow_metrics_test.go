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
	"net/http"
	"net/http/httptest"
	"testing"

	config "ai-api-gateway-service/gateway_config"

	"github.com/stretchr/testify/assert"
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

func TestShadowSamplingDecisionsMetricInitializesAndCountsDecidingMethod(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	counter, err := newShadowSamplingDecisionsCounter(provider.Meter(shadowMetricsScope))
	require.NoError(t, err)
	previousCounter := shadowSamplingDecisionsCounter
	shadowSamplingDecisionsCounter = counter
	t.Cleanup(func() {
		shadowSamplingDecisionsCounter = previousCounter
	})

	initializeShadowSamplingMetrics()
	initial := collectShadowSamplingDecisionCounts(t, reader)
	// random and perBearerKey on three endpoint labels, plus the two
	// request-body methods on chatCompletions and responses, each admitted and
	// rejected.
	require.Len(t, initial, (2*3+2*2)*2)
	for labels, count := range initial {
		assert.Zero(t, count, "unexpected initial count for %+v", labels)
	}

	methods := testShadowSamplingMethods(
		config.ShadowSamplingMethodPromptCacheKey,
		config.ShadowSamplingMethodFirstMessageHash,
	)
	shadows := []shadowConfig{
		{modelName: "shadow-a", percentage: 96, samplingMethods: methods},
		{modelName: "shadow-b", percentage: 10, samplingMethods: methods},
		{modelName: "shadow-always", percentage: 100, samplingMethods: methods},
	}

	// The prompt cache key hashes to bucket 95, so it decides both targets.
	keyed := newShadowRequestSampler(
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		[]byte(`{"prompt_cache_key":"session-123"}`),
		shadowSamplingEndpointChatCompletions,
		nil,
		fixedRandomBucket(0),
	)
	assert.Equal(t, []string{"shadow-a", "shadow-always"}, shadowModelNames(admittedShadowsWithSampler(shadows, keyed)))
	assert.Equal(t, []config.ShadowSamplingMethod{config.ShadowSamplingMethodPromptCacheKey}, keyed.decidedMethods)

	// With no key and no first user message, sampling falls back to random.
	unkeyed := newShadowRequestSampler(
		httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		[]byte(`{}`),
		shadowSamplingEndpointResponses,
		nil,
		fixedRandomBucket(5),
	)
	assert.Equal(t, []string{"shadow-a", "shadow-b", "shadow-always"}, shadowModelNames(admittedShadowsWithSampler(shadows, unkeyed)))
	assert.Equal(t, []config.ShadowSamplingMethod{config.ShadowSamplingMethodRandom}, unkeyed.decidedMethods)

	counts := collectShadowSamplingDecisionCounts(t, reader)
	require.Len(t, counts, len(initial), "decisions must not add label combinations")
	nonzero := map[shadowSamplingDecisionLabels]int64{}
	for labels, count := range counts {
		if count != 0 {
			nonzero[labels] = count
		}
	}
	assert.Equal(t, map[shadowSamplingDecisionLabels]int64{
		{endpoint: "chatCompletions", method: "promptCacheKey", outcome: "admitted"}: 1,
		{endpoint: "chatCompletions", method: "promptCacheKey", outcome: "rejected"}: 1,
		{endpoint: "responses", method: "random", outcome: "admitted"}:               2,
	}, nonzero)
}

type shadowSamplingDecisionLabels struct {
	endpoint string
	method   string
	outcome  string
}

func collectShadowSamplingDecisionCounts(t *testing.T, reader *sdkmetric.ManualReader) map[shadowSamplingDecisionLabels]int64 {
	t.Helper()

	var resourceMetrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &resourceMetrics))

	counts := make(map[shadowSamplingDecisionLabels]int64)
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		for _, metric := range scopeMetrics.Metrics {
			if metric.Name != shadowSamplingDecisionsMetricName {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, point := range sum.DataPoints {
				require.Equal(t, 3, point.Attributes.Len(), "only endpoint, method, and outcome may be labels")
				endpoint, ok := point.Attributes.Value(shadowSamplingEndpointLabel)
				require.True(t, ok)
				method, ok := point.Attributes.Value(shadowSamplingMethodLabel)
				require.True(t, ok)
				outcome, ok := point.Attributes.Value(shadowSamplingOutcomeLabel)
				require.True(t, ok)
				counts[shadowSamplingDecisionLabels{
					endpoint: endpoint.AsString(),
					method:   method.AsString(),
					outcome:  outcome.AsString(),
				}] = point.Value
			}
		}
	}
	return counts
}
