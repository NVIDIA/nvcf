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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

const (
	metricHTTPRequests        = "llm_api_gateway_http_requests_total"
	metricHTTPRequestDuration = "llm_api_gateway_http_request_duration_seconds"
	metricLLMTokens           = "llm_api_gateway_llm_tokens_total"
	metricStreamFirstToken    = "llm_api_gateway_stream_first_token_seconds"
)

// The tests in this file swap the global meter provider, so they must not run
// in parallel.

func TestModelLabel_RouterAcceptsRequest_RecordsRoutedModel(t *testing.T) {
	settings := []struct {
		name        string
		isEnabled   bool
		model       string
		wantFunctID string
	}{
		{"bare model names", true, bareModelName, "none"},
		{"routing key", false, "fn-alpha/" + bareModelName, "fn-alpha"},
	}
	endpoints := []struct {
		name                string
		path                string
		body                string
		upstreamContentType string
		upstreamBody        string
		wantMetrics         []string
	}{
		{
			name:                "chat completions",
			path:                "/v1/chat/completions",
			body:                `{"model":%q,"messages":[{"role":"user","content":"hi"}]}`,
			upstreamContentType: "text/event-stream",
			upstreamBody:        passthroughChatUpstreamSSE,
			wantMetrics:         []string{metricHTTPRequests, metricHTTPRequestDuration, metricLLMTokens},
		},
		{
			name:                "chat completions streaming",
			path:                "/v1/chat/completions",
			body:                `{"model":%q,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstreamContentType: "text/event-stream",
			upstreamBody:        passthroughChatUpstreamSSE,
			wantMetrics: []string{
				metricHTTPRequests, metricHTTPRequestDuration, metricLLMTokens, metricStreamFirstToken,
			},
		},
		{
			name:                "responses",
			path:                "/v1/responses",
			body:                `{"model":%q,"input":"hi"}`,
			upstreamContentType: "text/event-stream",
			upstreamBody:        bareModelResponsesUpstreamSSE,
			wantMetrics:         []string{metricHTTPRequests, metricHTTPRequestDuration, metricLLMTokens},
		},
		{
			name:                "responses streaming",
			path:                "/v1/responses",
			body:                `{"model":%q,"stream":true,"input":"hi"}`,
			upstreamContentType: "text/event-stream",
			upstreamBody:        bareModelResponsesUpstreamSSE,
			wantMetrics:         []string{metricHTTPRequests, metricHTTPRequestDuration, metricLLMTokens},
		},
		{
			name:                "embeddings",
			path:                "/v1/embeddings",
			body:                `{"model":%q,"input":"hi"}`,
			upstreamContentType: echo.MIMEApplicationJSON,
			upstreamBody:        `{"object":"list","data":[],"model":"meta/llama-3.1-8b-instruct"}`,
			wantMetrics:         []string{metricHTTPRequests, metricHTTPRequestDuration},
		},
	}

	for _, setting := range settings {
		for _, endpoint := range endpoints {
			t.Run(setting.name+"/"+endpoint.name, func(t *testing.T) {
				reader := useManualMeterReader(t)
				e := newModelLabelAPI(t, setting.isEnabled, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set(echo.HeaderContentType, endpoint.upstreamContentType)
					_, _ = io.WriteString(w, endpoint.upstreamBody)
				})

				rec := serveModelLabelRequest(e, endpoint.path, fmt.Sprintf(endpoint.body, setting.model))

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				metrics := collectMetrics(t, reader)
				for _, name := range endpoint.wantMetrics {
					assertMetricHasAttributes(t, metrics, name, map[string]string{
						"model":       bareModelName,
						"function_id": setting.wantFunctID,
					})
				}
			})
		}
	}
}

func TestModelLabel_RouterDoesNotAcceptRequest_RecordsEmptyModel(t *testing.T) {
	cases := []struct {
		name         string
		isBareModels bool
		path         string
		body         string
		routerStatus int
		routerHeader http.Header
		routerBody   string
		wantStatus   int
	}{
		{
			// Without bare model names, a model with no routing key prefix is invalid.
			name:         "gateway rejects invalid model before routing",
			path:         "/v1/chat/completions",
			body:         `{"model":"caller-invented-model","messages":[{"role":"user","content":"hi"}]}`,
			routerStatus: http.StatusOK,
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "router answers unknown model on chat completions",
			isBareModels: true,
			path:         "/v1/chat/completions",
			body:         `{"model":"caller-invented-model","messages":[{"role":"user","content":"hi"}]}`,
			routerStatus: http.StatusNotFound,
			routerHeader: http.Header{"X-Stargate-Error-Code": {"no_eligible_candidates"}},
			routerBody:   `{"error":"no eligible candidates","code":"no_eligible_candidates"}`,
			wantStatus:   http.StatusNotFound,
		},
		{
			name:         "router answers unknown model on embeddings",
			isBareModels: true,
			path:         "/v1/embeddings",
			body:         `{"model":"caller-invented-model","input":"hi"}`,
			routerStatus: http.StatusNotFound,
			routerHeader: http.Header{"X-Stargate-Error-Code": {"no_eligible_candidates"}},
			routerBody:   `{"error":"no eligible candidates","code":"no_eligible_candidates"}`,
			wantStatus:   http.StatusNotFound,
		},
		{
			name:         "router rejects the model header",
			isBareModels: true,
			path:         "/v1/chat/completions",
			body:         `{"model":"caller-invented-model","messages":[{"role":"user","content":"hi"}]}`,
			routerStatus: http.StatusBadRequest,
			wantStatus:   http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := useManualMeterReader(t)
			e := newModelLabelAPI(t, tc.isBareModels, func(w http.ResponseWriter, _ *http.Request) {
				for name, values := range tc.routerHeader {
					w.Header()[name] = values
				}
				w.WriteHeader(tc.routerStatus)
				_, _ = io.WriteString(w, tc.routerBody)
			})

			rec := serveModelLabelRequest(e, tc.path, tc.body)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			metrics := collectMetrics(t, reader)
			for _, name := range []string{metricHTTPRequests, metricHTTPRequestDuration} {
				assertMetricHasAttributes(t, metrics, name, map[string]string{
					"model":  "",
					"route":  tc.path,
					"status": fmt.Sprint(tc.wantStatus),
				})
			}
		})
	}
}

func useManualMeterReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldMeterProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(meterProvider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldMeterProvider)
		_ = meterProvider.Shutdown(context.Background())
	})
	return reader
}

// newModelLabelAPI wires the gateway the way server.go does, in front of a
// stub router. Create it after useManualMeterReader so its instruments bind to
// the test reader.
func newModelLabelAPI(t *testing.T, bareModelNamesEnabled bool, router http.HandlerFunc) *echo.Echo {
	t.Helper()

	upstream := httptest.NewServer(router)
	t.Cleanup(upstream.Close)

	stargate, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
	require.NoError(t, err)

	cfg := config.Default()
	cfg.BareModelNamesEnabled = bareModelNamesEnabled
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(echoingInvocationAuthClient{}))
	RegisterRoutes(e, NewHandlers(cfg, stargate, nil, nil))
	return e
}

func serveModelLabelRequest(e *echo.Echo, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer caller-secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}
