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
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap/zaptest"
)

func TestOldGrpcMetricsMiddleware(t *testing.T) {
	// Reset global state
	prometheus.DefaultRegisterer = prometheus.NewRegistry()
	prometheus.DefaultGatherer = prometheus.DefaultRegisterer.(prometheus.Gatherer)
	logger := zaptest.NewLogger(t)
	meterProvider := SetupGlobalOtelMetrics(logger)
	meter := meterProvider.Meter("foobar")
	middleWareFactory := CreateOldGrpcMetricsMiddleWare(logger, meter)

	// 1. Simulate a request
	simpleHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Spam")
	}

	testRouter := chi.NewRouter()
	testRouter.Use(middleWareFactory("spameggs"))
	testRouter.HandleFunc("/myendpoint", simpleHandler)

	req := httptest.NewRequest("GET", "/myendpoint", nil)
	w := httptest.NewRecorder()

	testRouter.ServeHTTP(w, req)

	// 2. get the metrics
	req = httptest.NewRequest("GET", "/metrics", nil)
	w = httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(w, req)

	contents := w.Body.String()

	metricsToTest := []string{
		"(?m)^nvcf_reval_request_duration_seconds_bucket\\{http_code=\"200\",.*method=\"spameggs\"",
		"(?m)^nvcf_reval_request_duration_seconds_sum\\{http_code=\"200\",.*method=\"spameggs\"",
		"(?m)^nvcf_reval_request_duration_seconds_count\\{http_code=\"200\",.*method=\"spameggs\"",
		"(?m)^nvcf_reval_response_payload_size_bucket\\{http_code=\"200\",.*method=\"spameggs\"",
		"(?m)^nvcf_reval_response_payload_size_sum\\{http_code=\"200\",.*method=\"spameggs\"",
		"(?m)^nvcf_reval_response_payload_size_count\\{http_code=\"200\",.*method=\"spameggs\"",
	}
	// fmt.Println(contents)
	for _, metric := range metricsToTest {
		t.Run(metric, func(tt *testing.T) {
			re, err := regexp.Compile(metric)
			if err != nil {
				tt.Fatalf("failed to compile regex: %v", err)
			}

			if !re.MatchString(contents) {
				tt.Errorf("missing metric %s", metric)
			}
		})
	}
}
