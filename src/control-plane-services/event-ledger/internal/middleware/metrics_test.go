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
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestHttpMetricsMiddleware(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	SetupGlobalOtelMetrics(logger)
	middleWare := CreateHttpMetricsMiddleWare(logger)

	// 1. Simulate a request
	simpleHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Spam")
	}

	testRouter := mux.NewRouter()
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
		"(?m)^http_request_size_bytes_bucket\\{method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_size_bytes_sum\\{method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_request_size_bytes_count\\{method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_response_size_bytes_bucket\\{code=\"200\".*,method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_response_size_bytes_sum\\{code=\"200\".*,method=\"GET\".*path=\"/myendpoint\"",
		"(?m)^http_response_size_bytes_count\\{code=\"200\".*,method=\"GET\".*path=\"/myendpoint\"",
	}
	fmt.Println(contents)
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
