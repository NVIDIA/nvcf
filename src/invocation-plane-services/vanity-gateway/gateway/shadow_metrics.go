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
)

const (
	shadowDroppedMetricName  = "nvcf_ai_api_gateway_shadow_requests_dropped"
	shadowMetricsScope       = "ai-api-gateway-service/gateway"
	shadowDroppedModelLabel  = "openai_model_name"
	shadowDroppedReasonLabel = "reason"
)

var (
	shadowDroppedCounter otelmetric.Int64Counter = metricnoop.Int64Counter{}
	shadowDroppedReasons                         = []string{
		shadowDroppedReasonBodyReadError,
		shadowDroppedReasonBodyRewriteError,
		shadowDroppedReasonConcurrencyLimit,
	}
)

func setupShadowMetrics() error {
	counter, err := newShadowDroppedCounter(otel.GetMeterProvider().Meter(shadowMetricsScope))
	if err != nil {
		return err
	}
	shadowDroppedCounter = counter
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
