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
	"sync"

	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

var (
	setupHTTPMetricsOnce sync.Once
	setupHTTPMetricsErr  error
)

var httpDurationHistogramBoundaries = []float64{
	0.1, 0.25, 0.5, 0.75, 1, 2, 5, 10,
	15, 30, 60, 120, 300, 600, 900,
}

func SetupHTTPMetrics() error {
	setupHTTPMetricsOnce.Do(func() {
		exporter, err := otelprom.New()
		if err != nil {
			setupHTTPMetricsErr = err
			return
		}

		provider := newHTTPMeterProvider(exporter)
		otel.SetMeterProvider(provider)
	})

	return setupHTTPMetricsErr
}

func newHTTPMeterProvider(reader sdkmetric.Reader) *sdkmetric.MeterProvider {
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(httpDurationHistogramView("http.server.request.duration")),
		sdkmetric.WithView(httpDurationHistogramView("http.client.request.duration")),
	)
}

func httpDurationHistogramView(name string) sdkmetric.View {
	return sdkmetric.NewView(
		sdkmetric.Instrument{Name: name},
		sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
			Boundaries: httpDurationHistogramBoundaries,
		}},
	)
}
