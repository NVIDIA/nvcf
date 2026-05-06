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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap/zaptest"
)

func TestHttpMetricsMiddleware(t *testing.T) {
	// Reset global state
	prometheus.DefaultRegisterer = prometheus.NewRegistry()
	prometheus.DefaultGatherer = prometheus.DefaultRegisterer.(prometheus.Gatherer)
	logger := zaptest.NewLogger(t)
	meterProvider := SetupGlobalOtelMetrics(logger)
	meter := meterProvider.Meter("foobar")
	middleWare := CreateHttpMetricsMiddleWare(logger, meter)

	// 1. Simulate a request
	simpleHandler := func(w http.ResponseWriter, r *http.Request) {
		timer := RunTimerFromContext(r.Context())
		timer.RecordThreadStart()
		defer timer.RecordThreadEnd()
		fmt.Println("Thread start")
		time.Sleep(30 * time.Millisecond)

		timer.RecordHelmDownloadStart()
		defer timer.RecordHelmDownloadEnd()
		fmt.Println("HelmDownload start")
		time.Sleep(20 * time.Millisecond)

		timer.RecordImageCheckStart()
		defer timer.RecordImageCheckEnd()
		fmt.Println("ImageCheck start")
		time.Sleep(20 * time.Millisecond)

		fmt.Fprint(w, "Done")
	}

	testRouter := chi.NewRouter()
	testRouter.Use(middleWare)
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
		"(?m)^http_requests_in_flight\\{method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_requests_total\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_duration_ms_bucket\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_duration_ms_sum\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_duration_ms_count\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_thread_only_duration_ms_bucket\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_thread_only_duration_ms_sum\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_thread_only_duration_ms_count\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_helm_download_duration_ms_bucket\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_helm_download_duration_ms_sum\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_helm_download_duration_ms_count\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_image_check_duration_ms_bucket\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_image_check_duration_ms_sum\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^reval_image_check_duration_ms_count\\{code=\"200\",method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_size_bytes_bucket\\{method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_size_bytes_sum\\{method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_size_bytes_count\\{method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_response_size_bytes_bucket\\{code=\"200\".*,method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_response_size_bytes_sum\\{code=\"200\".*,method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_response_size_bytes_count\\{code=\"200\".*,method=\"GET\".*path=\"/myendpoint\"",
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
