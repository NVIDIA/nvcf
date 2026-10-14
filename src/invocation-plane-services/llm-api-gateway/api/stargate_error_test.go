/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// TestChatCompletionsStargateRejections pins the client-visible status and
// body for each error Stargate can return on /v1/chat/completions. Stargate
// rejections carry X-Stargate-Error-Code; pylon and backend errors pass
// through Stargate without it.
func TestChatCompletionsStargateRejections(t *testing.T) {
	t.Parallel()

	const overloadedBody = `{"error":{"code":"overloaded_error","message":"Inference capacity is temporarily unavailable.","param":"","type":"overloaded_error"}}`
	for _, tc := range []struct {
		name       string
		status     int
		code       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name:   "no eligible candidates",
			status: http.StatusNotFound, code: "no_eligible_candidates",
			body:       `{"error":"no eligible candidates","code":"no_eligible_candidates"}`,
			wantStatus: http.StatusNotFound, wantBody: messageBody("no eligible candidates"),
		},
		{
			name:   "unknown routing method",
			status: http.StatusBadRequest, code: "unknown_method",
			body:       `{"error":"unknown routing method 'bogus'","code":"unknown_method"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("unknown routing method 'bogus'"),
		},
		{
			name:   "routing method not configured",
			status: http.StatusBadRequest, code: "unavailable",
			body:       `{"error":"routing method pulsar is not configured for this model","code":"unavailable"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("routing method pulsar is not configured for this model"),
		},
		{
			name:   "malformed routing expression",
			status: http.StatusBadRequest, code: "malformed_expression",
			body:       `{"error":"P3: commas are not allowed","code":"malformed_expression"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("P3: commas are not allowed"),
		},
		{
			name:   "invalid routing parameter value",
			status: http.StatusBadRequest, code: "invalid_value",
			body:       `{"error":"next_bucket_unlock_factor: expected a finite decimal","code":"invalid_value"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("next_bucket_unlock_factor: expected a finite decimal"),
		},
		{
			name:   "inert routing parameter value",
			status: http.StatusBadRequest, code: "inert_value",
			body:       `{"error":"sample_count must be positive","code":"inert_value"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("sample_count must be positive"),
		},
		{
			name:   "inert routing parameter combination",
			status: http.StatusBadRequest, code: "inert_combination",
			body:       `{"error":"max_queue_time_floor_ms and max_queue_time_ceil_ms must both be configured","code":"inert_combination"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("max_queue_time_floor_ms and max_queue_time_ceil_ms must both be configured"),
		},
		{
			name:   "unknown routing parameter",
			status: http.StatusBadRequest, code: "unknown_parameter",
			body:       `{"error":"unknown parameter 'bogus' for algorithm power-of-n","code":"unknown_parameter"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("unknown parameter 'bogus' for algorithm power-of-n"),
		},
		{
			name:   "routing parameter not applicable",
			status: http.StatusBadRequest, code: "not_applicable",
			body:       `{"error":"sample_count is not applicable to pulsar","code":"not_applicable"}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("sample_count is not applicable to pulsar"),
		},
		{
			name:   "overloaded",
			status: http.StatusServiceUnavailable, code: "overloaded_error",
			body:       overloadedBody,
			wantStatus: 529, wantBody: overloadedBody,
		},
		{
			name:       "invalid contract header with empty body",
			status:     http.StatusBadRequest,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("Bad Request"),
		},
		{
			name:       "request body over replay limit with empty body",
			status:     http.StatusRequestEntityTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge, wantBody: messageBody("Request Entity Too Large"),
		},
		{
			name:       "no eligible backend with empty body",
			status:     http.StatusServiceUnavailable,
			wantStatus: http.StatusServiceUnavailable, wantBody: messageBody("Service Unavailable"),
		},
		{
			name:       "tunnel failure with empty body",
			status:     http.StatusBadGateway,
			wantStatus: http.StatusBadGateway, wantBody: messageBody("Bad Gateway"),
		},
		{
			name:       "invalid backend identity with empty body",
			status:     http.StatusInternalServerError,
			wantStatus: http.StatusInternalServerError, wantBody: messageBody("Internal Server Error"),
		},
		{
			name:       "backend OpenAI error",
			status:     http.StatusBadRequest,
			body:       `{"error":{"message":"max_tokens is too large","type":"invalid_request_error","param":"max_tokens","code":"invalid_value"}}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("max_tokens is too large"),
		},
		{
			name:       "backend OpenAI error with numeric code",
			status:     http.StatusBadRequest,
			body:       `{"error":{"message":"max_tokens is too large","type":"BadRequestError","param":null,"code":400}}`,
			wantStatus: http.StatusBadRequest, wantBody: messageBody("max_tokens is too large"),
		},
		{
			name:       "pylon problem details",
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"about:blank","title":"Service Unavailable","status":503,"detail":"local upstream connection failed"}`,
			wantStatus: http.StatusServiceUnavailable, wantBody: messageBody("local upstream connection failed"),
		},
		{
			name:       "problem details without detail",
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"about:blank","title":"Backend Unavailable","status":503}`,
			wantStatus: http.StatusServiceUnavailable, wantBody: messageBody("Backend Unavailable"),
		},
		{
			name:       "non-JSON body",
			status:     http.StatusBadGateway,
			body:       "upstream connect error",
			wantStatus: http.StatusBadGateway, wantBody: messageBody("upstream connect error"),
		},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				t.Parallel()

				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if tc.code != "" {
						w.Header().Set("X-Stargate-Error-Code", tc.code)
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				defer upstream.Close()

				cfg := config.Default()
				stargate, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
				require.NoError(t, err)
				e := echo.New()
				e.Use(NewContextMiddleware(cfg))
				RegisterRoutes(e, NewHandlers(cfg, stargate, nil, nil))

				payload := fmt.Sprintf(`{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)

				require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
				require.JSONEq(t, tc.wantBody, rec.Body.String())
				require.Empty(t, rec.Header().Values("X-Stargate-Error-Code"))
			})
		}
	}
}

func messageBody(message string) string {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		panic(err)
	}
	return string(body)
}
