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

package metrics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const (
	metricsPath = "/metrics"
)

// SetupGlobalOtelMetrics creates the prometheus exporter and configures it with OTEL
func SetupGlobalOtelMetrics(logger *zap.Logger) *sdkmetric.MeterProvider {
	// OTEL Prometheus exporter
	exporter, err := prometheus.New()
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create prometheus exporter: %v", err))
	}

	// Create a MeterProvider with the exporter
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
	)

	// Set this MeterProvider as the global so instrumentation via `otel.GetMeterProvider()`
	// or `otel.Meter(...)` uses it by default.
	otel.SetMeterProvider(meterProvider)
	return meterProvider
}

// CreateHttpMetricsMiddleWare creates the HTTP metrics and returns a middleware.
func CreateHttpMetricsMiddleWare(logger *zap.Logger, meter metric.Meter) func(http.Handler) http.Handler {
	// Register our instruments. If they already exist they won't be created twice.
	requestsCount, err := meter.Int64Counter(
		"http_requests_total",
		metric.WithDescription("Number of requests received"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create http_requests_total metric: %v", err))
	}

	requestsInFlight, err := meter.Int64UpDownCounter(
		"http_requests_in_flight",
		metric.WithDescription("Number of requests currently in flight"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create http_requests_in_flight metric: %v", err))
	}

	requestLatency, err := meter.Int64Histogram(
		"http_request_duration_ms",
		metric.WithDescription("Request duration in milliseconds"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create http_request_duration_ms metric: %v", err))
	}

	threadOnlyLatency, err := meter.Int64Histogram(
		"reval_thread_only_duration_ms",
		metric.WithDescription("Thread-only duration in milliseconds"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create reval_thread_only_duration_ms metric: %v", err))
	}

	helmDownloadLatency, err := meter.Int64Histogram(
		"reval_helm_download_duration_ms",
		metric.WithDescription("Helm download duration in milliseconds"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create reval_helm_download_duration_ms metric: %v", err))
	}

	imageCheckLatency, err := meter.Int64Histogram(
		"reval_image_check_duration_ms",
		metric.WithDescription("Image duration in milliseconds"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create reval_image_check_duration_ms metric: %v", err))
	}

	requestSize, err := meter.Int64Histogram(
		"http_request_size_bytes",
		metric.WithDescription("Request size in bytes"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create http_request_size_bytes metric: %v", err))
	}

	responseSize, err := meter.Int64Histogram(
		"http_response_size_bytes",
		metric.WithDescription("Response size in bytes"),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create http_response_size_bytes metric: %v", err))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// We'll use the request context for recording.
			ctx := r.Context()
			// Create a timer for internal process tracking.
			timer := NewRunTimer()
			r = r.WithContext(RunTimerIntoContext(ctx, timer))

			// Route
			routeStr := getRoutePattern(r)

			methodPathSet := metric.WithAttributeSet(attribute.NewSet(
				attribute.String("method", r.Method),
				attribute.String("path", routeStr)))
			if r.ContentLength >= 0 {
				requestSize.Record(ctx, r.ContentLength, methodPathSet)
			}
			// Serve the request
			requestsInFlight.Add(ctx, 1, methodPathSet)
			defer requestsInFlight.Add(ctx, -1, methodPathSet)
			requestMetrics := httpsnoop.CaptureMetrics(next, w, r)

			methodCodePathSet := metric.WithAttributeSet(attribute.NewSet(
				attribute.String("method", r.Method),
				attribute.String("path", routeStr),
				attribute.Int("code", requestMetrics.Code),
			))
			requestLatency.Record(ctx, requestMetrics.Duration.Milliseconds(), methodCodePathSet)
			threadOnlyLatency.Record(ctx, timer.GetLocalThreadDuration().Milliseconds(), methodCodePathSet)
			helmDownloadLatency.Record(ctx, timer.GetHelmDownloadDuration().Milliseconds(), methodCodePathSet)
			imageCheckLatency.Record(ctx, timer.GetImageCheckDuration().Milliseconds(), methodCodePathSet)
			if requestMetrics.Written >= 0 {
				responseSize.Record(ctx, requestMetrics.Written, methodCodePathSet)
			}
			requestsCount.Add(ctx, 1, methodCodePathSet)
		})
	}
}

func getRoutePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return r.URL.Path
	}
	if pattern := rctx.RoutePattern(); pattern != "" {
		// Pattern is already available
		return pattern
	}

	routePath := r.URL.Path
	if r.URL.RawPath != "" {
		routePath = r.URL.RawPath
	}

	tctx := chi.NewRouteContext()
	if !rctx.Routes.Match(tctx, r.Method, routePath) {
		// No matching pattern, so just return the request path.
		// Depending on your use case, it might make sense to
		// return an empty string or error here instead
		return routePath
	}

	// tctx has the updated pattern, since Match mutates it
	return tctx.RoutePattern()
}

func NewMetricsServer(logger *zap.Logger, port uint16) *http.Server {
	router := chi.NewRouter()
	router.Handle(metricsPath, promhttp.Handler())

	addr := fmt.Sprintf(":%d", port)
	server := &http.Server{
		Addr:    addr,
		Handler: router,
		// Sanity timeouts
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return server
}
