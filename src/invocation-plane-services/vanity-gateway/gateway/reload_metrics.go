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
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

const (
	reloadMetricsScope = "ai-api-gateway-service/gateway"

	lastReloadMetricName     = "nvcf_vanity_gateway_last_successful_config_reload_timestamp_seconds"
	reloadFailuresMetricName = "nvcf_vanity_gateway_config_reload_failures"
	reloadStageLabel         = "stage"

	// reloadStageLoad covers a config file that is unreadable, unparsable or
	// fails validation. reloadStageRouterBuild covers a config that loaded but
	// whose router could not be built, so the previous router keeps serving.
	reloadStageLoad        = "load"
	reloadStageRouterBuild = "router_build"
)

// reloadClock is a variable so tests can control the recorded timestamp.
var reloadClock = time.Now

var (
	lastReloadGauge       otelmetric.Int64Gauge   = metricnoop.Int64Gauge{}
	reloadFailuresCounter otelmetric.Int64Counter = metricnoop.Int64Counter{}
	reloadStages                                  = []string{reloadStageLoad, reloadStageRouterBuild}
)

func setupReloadMetrics() error {
	meter := otel.GetMeterProvider().Meter(reloadMetricsScope)
	gauge, err := newLastReloadGauge(meter)
	if err != nil {
		return err
	}
	counter, err := newReloadFailuresCounter(meter)
	if err != nil {
		return err
	}
	lastReloadGauge = gauge
	reloadFailuresCounter = counter
	for _, stage := range reloadStages {
		recordConfigReloadFailureCount(context.Background(), stage, 0)
	}
	return nil
}

func newLastReloadGauge(meter otelmetric.Meter) (otelmetric.Int64Gauge, error) {
	gauge, err := meter.Int64Gauge(
		lastReloadMetricName,
		otelmetric.WithDescription("Unix time of the last config load that was followed by a successful router swap."),
	)
	if err != nil {
		return nil, fmt.Errorf("create last config reload gauge: %w", err)
	}
	return gauge, nil
}

func newReloadFailuresCounter(meter otelmetric.Meter) (otelmetric.Int64Counter, error) {
	counter, err := meter.Int64Counter(
		reloadFailuresMetricName,
		otelmetric.WithDescription("Number of failed config reloads by stage."),
	)
	if err != nil {
		return nil, fmt.Errorf("create config reload failures counter: %w", err)
	}
	return counter, nil
}

func recordConfigReloadSuccess(ctx context.Context) {
	lastReloadGauge.Record(ctx, reloadClock().Unix())
}

// recordConfigLoadFailure is the file-watcher callback for a reload rejected
// before a router build was attempted.
func recordConfigLoadFailure(error) {
	recordConfigReloadFailure(context.Background(), reloadStageLoad)
}

func recordConfigReloadFailure(ctx context.Context, stage string) {
	recordConfigReloadFailureCount(ctx, stage, 1)
}

func recordConfigReloadFailureCount(ctx context.Context, stage string, count int64) {
	reloadFailuresCounter.Add(ctx, count, otelmetric.WithAttributes(attribute.String(reloadStageLabel, stage)))
}
