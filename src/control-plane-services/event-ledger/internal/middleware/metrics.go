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
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

// CloudEventsMetrics holds metrics for CloudEvents resilience
type CloudEventsMetrics struct {
	EventsSkippedCounter  metric.Int64Counter
	EventsRetriedCounter  metric.Int64Counter
	EventsFallbackCounter metric.Int64Counter
}

// CreateCloudEventsMetrics creates and returns CloudEvents resilience metrics
func CreateCloudEventsMetrics(logger *otelzap.Logger) (*CloudEventsMetrics, error) {
	meter := otel.GetMeterProvider().Meter(constants.ApiSvcName)

	eventsSkippedCounter, err := meter.Int64Counter(
		"fnds_cloudevents_skipped_total",
		metric.WithDescription("Total number of CloudEvents skipped due to exceeding max retry attempts"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create fnds_cloudevents_skipped_total metric: %w", err)
	}

	eventsRetriedCounter, err := meter.Int64Counter(
		"fnds_cloudevents_retried_total",
		metric.WithDescription("Total number of CloudEvents successfully retried"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create fnds_cloudevents_retried_total metric: %w", err)
	}

	eventsFallbackCounter, err := meter.Int64Counter(
		"fnds_cloudevents_fallback_total",
		metric.WithDescription("Total number of CloudEvents sent to Cassandra fallback"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create fnds_cloudevents_fallback_total metric: %w", err)
	}

	return &CloudEventsMetrics{
		EventsSkippedCounter:  eventsSkippedCounter,
		EventsRetriedCounter:  eventsRetriedCounter,
		EventsFallbackCounter: eventsFallbackCounter,
	}, nil
}

// SetupGlobalOtelMetrics creates the prometheus exporter and configures it with OTEL
func SetupGlobalOtelMetrics(logger *otelzap.Logger) {
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
}

// CreateHttpMetricsMiddleWare creates the HTTP metrics and returns a middleware.
func CreateHttpMetricsMiddleWare(logger *otelzap.Logger) mux.MiddlewareFunc {
	// Create a Meter for our application
	meter := otel.GetMeterProvider().Meter(constants.ApiSvcName)

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

			// Route
			routeStr := "RouteNotFound"
			route := mux.CurrentRoute(r)
			if route != nil {
				var err error
				routeStr, err = route.GetPathTemplate()
				if err != nil {
					routeStr, err = route.GetPathRegexp()
					if err != nil {
						routeStr = "RouteNotFound"
					}
				}
			}

			methodPathSet := metric.WithAttributeSet(attribute.NewSet(
				attribute.String("method", r.Method),
				attribute.String("path", routeStr)))

			requestSize.Record(ctx, r.ContentLength, methodPathSet)

			// Serve the request
			requestsInFlight.Add(ctx, 1, methodPathSet)
			requestMetrics := httpsnoop.CaptureMetrics(next, w, r)
			requestsInFlight.Add(ctx, -1, methodPathSet)

			methodCodePathSet := metric.WithAttributeSet(attribute.NewSet(
				attribute.String("method", r.Method),
				attribute.String("path", routeStr),
				attribute.Int("code", requestMetrics.Code),
			))
			requestLatency.Record(ctx, requestMetrics.Duration.Milliseconds(), methodCodePathSet)
			responseSize.Record(ctx, requestMetrics.Written, methodCodePathSet)
			requestsCount.Add(ctx, 1, methodCodePathSet)
		})
	}
}

func ServeMetrics(logger *otelzap.Logger, port int, profilingEnabled bool) {
	mux := mux.NewRouter()
	mux.Handle("/metrics", promhttp.Handler())
	if profilingEnabled {
		logger.Warn("enabling profiling endpoints on the internal server")
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	go func() {
		addr := fmt.Sprintf(":%d", port)
		server := &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      2 * time.Minute,
			IdleTimeout:       60 * time.Second,
		}
		logger.Warn(
			fmt.Sprintf("serving metrics at %s/metrics", addr))
		if err := server.ListenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				errMsg := fmt.Sprintf("unable to listen and serve: %s\n", err.Error())
				logger.Fatal(errMsg)
			}
		}
	}()
}
