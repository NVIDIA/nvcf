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
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	config "ai-api-gateway-service/gateway_config"
)

const (
	shadowDroppedMetricName  = "nvcf_ai_api_gateway_shadow_requests_dropped"
	shadowMetricsScope       = "ai-api-gateway-service/gateway"
	shadowDroppedModelLabel  = "openai_model_name"
	shadowDroppedReasonLabel = "reason"

	shadowSamplingDecisionsMetricName = "nvcf_ai_api_gateway_shadow_sampling_decisions"
	shadowSamplingEndpointLabel       = "sampling_endpoint"
	shadowSamplingMethodLabel         = "method"
	shadowSamplingOutcomeLabel        = "outcome"
	shadowSamplingOutcomeAdmitted     = "admitted"
	shadowSamplingOutcomeRejected     = "rejected"
	shadowSamplingEndpointOther       = "other"
)

var (
	shadowDroppedCounter           otelmetric.Int64Counter = metricnoop.Int64Counter{}
	shadowSamplingDecisionsCounter otelmetric.Int64Counter = metricnoop.Int64Counter{}
	shadowDroppedReasons                                   = []string{
		shadowDroppedReasonBodyReadError,
		shadowDroppedReasonBodyRewriteError,
		shadowDroppedReasonConcurrencyLimit,
	}
)

func setupShadowMetrics() error {
	meter := otel.GetMeterProvider().Meter(shadowMetricsScope)
	droppedCounter, err := newShadowDroppedCounter(meter)
	if err != nil {
		return err
	}
	decisionsCounter, err := newShadowSamplingDecisionsCounter(meter)
	if err != nil {
		return err
	}
	shadowDroppedCounter = droppedCounter
	shadowSamplingDecisionsCounter = decisionsCounter
	initializeShadowSamplingMetrics()
	return nil
}

func newShadowDroppedCounter(meter otelmetric.Meter) (otelmetric.Int64Counter, error) {
	counter, err := meter.Int64Counter(
		shadowDroppedMetricName,
		otelmetric.WithDescription("Number of shadow request dispatches dropped before replay."),
	)
	if err != nil {
		return nil, fmt.Errorf("create shadow dropped counter: %w", err)
	}
	return counter, nil
}

func initializeShadowDropMetrics(targetModels []string) {
	for _, targetModel := range targetModels {
		for _, reason := range shadowDroppedReasons {
			recordShadowDropMetric(context.Background(), targetModel, reason, 0)
		}
	}
}

func recordShadowDropMetrics(ctx context.Context, droppedReasons, droppedTargetModels []string) {
	for index, reason := range droppedReasons {
		if index >= len(droppedTargetModels) {
			break
		}
		recordShadowDropMetric(ctx, droppedTargetModels[index], reason, 1)
	}
}

func recordShadowDropMetric(ctx context.Context, targetModel, reason string, count int64) {
	shadowDroppedCounter.Add(
		ctx,
		count,
		otelmetric.WithAttributes(
			attribute.String(shadowDroppedModelLabel, targetModel),
			attribute.String(shadowDroppedReasonLabel, reason),
		),
	)
}

func newShadowSamplingDecisionsCounter(meter otelmetric.Meter) (otelmetric.Int64Counter, error) {
	counter, err := meter.Int64Counter(
		shadowSamplingDecisionsMetricName,
		otelmetric.WithDescription("Number of shadow admission decisions by endpoint, deciding sampling method, and outcome."),
	)
	if err != nil {
		return nil, fmt.Errorf("create shadow sampling decisions counter: %w", err)
	}
	return counter, nil
}

// initializeShadowSamplingMetrics emits every valid label combination at zero
// so the series exist on the first scrape. Request-body methods are limited to
// the Chat Completions and Responses endpoints, as in config validation.
func initializeShadowSamplingMetrics() {
	bodyEndpoints := []shadowSamplingEndpoint{shadowSamplingEndpointChatCompletions, shadowSamplingEndpointResponses}
	allEndpoints := append([]shadowSamplingEndpoint{shadowSamplingEndpointNone}, bodyEndpoints...)
	for _, admitted := range []bool{true, false} {
		for _, endpoint := range allEndpoints {
			for _, method := range []config.ShadowSamplingMethod{
				config.ShadowSamplingMethodRandom,
				config.ShadowSamplingMethodPerBearerKey,
			} {
				recordShadowSamplingDecisionCount(context.Background(), endpoint, method, admitted, 0)
			}
		}
		for _, endpoint := range bodyEndpoints {
			for _, method := range []config.ShadowSamplingMethod{
				config.ShadowSamplingMethodPromptCacheKey,
				config.ShadowSamplingMethodFirstMessageHash,
			} {
				recordShadowSamplingDecisionCount(context.Background(), endpoint, method, admitted, 0)
			}
		}
	}
}

func recordShadowSamplingDecision(ctx context.Context, endpoint shadowSamplingEndpoint, method config.ShadowSamplingMethod, admitted bool) {
	recordShadowSamplingDecisionCount(ctx, endpoint, method, admitted, 1)
}

func recordShadowSamplingDecisionCount(ctx context.Context, endpoint shadowSamplingEndpoint, method config.ShadowSamplingMethod, admitted bool, count int64) {
	endpointLabel := string(endpoint)
	if endpoint == shadowSamplingEndpointNone {
		endpointLabel = shadowSamplingEndpointOther
	}
	outcome := shadowSamplingOutcomeRejected
	if admitted {
		outcome = shadowSamplingOutcomeAdmitted
	}
	shadowSamplingDecisionsCounter.Add(
		ctx,
		count,
		otelmetric.WithAttributes(
			attribute.String(shadowSamplingEndpointLabel, endpointLabel),
			attribute.String(shadowSamplingMethodLabel, string(method)),
			attribute.String(shadowSamplingOutcomeLabel, outcome),
		),
	)
}
