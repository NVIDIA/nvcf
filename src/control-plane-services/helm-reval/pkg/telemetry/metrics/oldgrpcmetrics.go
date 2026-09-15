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

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

// CreateHttpMetricsMiddleWare creates the HTTP metrics and returns a middleware.
func CreateOldGrpcMetricsMiddleWare(logger *zap.Logger, meter metric.Meter) func(grpcMethodName string) func(http.Handler) http.Handler {
	// GRPC compatibility metrics
	// These are the metrics with names compatible with the ones used when it was running
	// with GRPC. This will help with dashboards and alerting.
	requestLatencyGrpcCompat, err := meter.Int64Histogram(
		"nvcf_reval_request_duration_seconds", // Yes, it is in milliseconds
		metric.WithDescription("Request duration in milliseconds"),
		metric.WithExplicitBucketBoundaries(10, 25, 50, 100, 250, 500, 750, 1000, 2500, 5000, 10000),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create http_request_duration_ms metric: %v", err))
	}

	responseSizeGrpcCompat, err := meter.Int64Histogram(
		"nvcf_reval_response_payload_size",
		metric.WithDescription("Response size in bytes"),
		metric.WithExplicitBucketBoundaries(10, 100, 1000, 10000, 100000, 1000000, 10000000),
	)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to create http_response_size_bytes metric: %v", err))
	}

	return func(grpcMethodName string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// We'll use the request context for recording.
				ctx := r.Context()

				// Serve the request
				requestMetrics := httpsnoop.CaptureMetrics(next, w, r)

				compatAttributeSet := metric.WithAttributeSet(attribute.NewSet(
					attribute.String("method", grpcMethodName),
					attribute.Int("http_code", requestMetrics.Code),
				))

				requestLatencyGrpcCompat.Record(ctx, requestMetrics.Duration.Milliseconds(), compatAttributeSet)
				responseSizeGrpcCompat.Record(ctx, requestMetrics.Written, compatAttributeSet)
			})
		}
	}
}
