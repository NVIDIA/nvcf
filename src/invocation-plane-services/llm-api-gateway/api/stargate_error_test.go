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
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

type upstreamErrorEndpoint struct {
	name      string
	path      string
	payload   string
	anthropic bool
}

var upstreamErrorEndpoints = []upstreamErrorEndpoint{
	{name: "chat", path: "/v1/chat/completions", payload: `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":false}`},
	{name: "streaming chat", path: "/v1/chat/completions", payload: `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":true}`},
	{name: "responses", path: "/v1/responses", payload: `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":false}`},
	{name: "streaming responses", path: "/v1/responses", payload: `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":true}`},
	{name: "embeddings", path: "/v1/embeddings", payload: `{"model":"fn-alpha/company-name/model-name","input":"hello"}`},
	{name: "messages", path: messagesEndpointPath, payload: `{"model":"fn-alpha/company-name/model-name","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, anthropic: true},
}

// TestUpstreamErrorEnvelopes pins the client-visible status and body for each
// error the request router, pylon, or a model server can return. Router
// rejections carry X-Stargate-Error-Code; pylon and backend errors pass
// through the router without it.
func TestUpstreamErrorEnvelopes(t *testing.T) {
	t.Parallel()

	const (
		openAIBackendError    = `{"error":{"message":"max_tokens is too large","type":"invalid_request_error","param":"max_tokens","code":"invalid_value"}}`
		anthropicBackendError = `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is too large"}}`
	)
	for _, tc := range []struct {
		name       string
		status     int
		routerCode string
		body       string
		wantStatus int
		// wantMessage is the message in the gateway envelope.
		wantMessage string
		// nativeOpenAI and nativeAnthropic mark backend bodies the endpoint
		// relays unchanged.
		nativeOpenAI    bool
		nativeAnthropic bool
	}{
		{
			name:   "no eligible candidates",
			status: http.StatusNotFound, routerCode: "no_eligible_candidates",
			body:       `{"error":"no eligible candidates","code":"no_eligible_candidates"}`,
			wantStatus: http.StatusNotFound, wantMessage: "no eligible candidates",
		},
		{
			name:   "unknown routing method",
			status: http.StatusBadRequest, routerCode: "unknown_method",
			body:       `{"error":"unknown routing method 'bogus'","code":"unknown_method"}`,
			wantStatus: http.StatusBadRequest, wantMessage: "unknown routing method 'bogus'",
		},
		{
			name:   "routing method not configured",
			status: http.StatusBadRequest, routerCode: "unavailable",
			body:       `{"error":"routing method pulsar is not configured for this model","code":"unavailable"}`,
			wantStatus: http.StatusBadRequest, wantMessage: "routing method pulsar is not configured for this model",
		},
		{
			name:   "malformed routing expression",
			status: http.StatusBadRequest, routerCode: "malformed_expression",
			body:       `{"error":"P3: commas are not allowed","code":"malformed_expression"}`,
			wantStatus: http.StatusBadRequest, wantMessage: "P3: commas are not allowed",
		},
		{
			name:   "overloaded",
			status: http.StatusServiceUnavailable, routerCode: "overloaded_error",
			body:       `{"error":{"code":"overloaded_error","message":"Inference capacity is temporarily unavailable.","param":"","type":"overloaded_error"}}`,
			wantStatus: 529, wantMessage: "Inference capacity is temporarily unavailable.",
		},
		{
			name:       "invalid contract header with empty body",
			status:     http.StatusBadRequest,
			wantStatus: http.StatusBadRequest, wantMessage: "Bad Request",
		},
		{
			name:       "request body over replay limit with empty body",
			status:     http.StatusRequestEntityTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge, wantMessage: "Request Entity Too Large",
		},
		{
			name:       "no eligible backend with empty body",
			status:     http.StatusServiceUnavailable,
			wantStatus: http.StatusServiceUnavailable, wantMessage: "Service Unavailable",
		},
		{
			name:       "tunnel failure with empty body",
			status:     http.StatusBadGateway,
			wantStatus: http.StatusBadGateway, wantMessage: "Bad Gateway",
		},
		{
			name:       "backend OpenAI error",
			status:     http.StatusBadRequest,
			body:       openAIBackendError,
			wantStatus: http.StatusBadRequest, wantMessage: "max_tokens is too large",
			nativeOpenAI: true,
		},
		{
			name:       "backend OpenAI error with numeric code",
			status:     http.StatusBadRequest,
			body:       `{"error":{"message":"max_tokens is too large","type":"BadRequestError","param":null,"code":400}}`,
			wantStatus: http.StatusBadRequest, wantMessage: "max_tokens is too large",
			nativeOpenAI: true,
		},
		{
			name:       "backend overload",
			status:     529,
			body:       `{"error":{"code":"overloaded_error","message":"busy","param":null,"type":"overloaded_error"}}`,
			wantStatus: 529, wantMessage: "busy",
			nativeOpenAI: true,
		},
		{
			name:       "backend Anthropic error",
			status:     http.StatusBadRequest,
			body:       anthropicBackendError,
			wantStatus: http.StatusBadRequest, wantMessage: "max_tokens is too large",
			nativeOpenAI: true, nativeAnthropic: true,
		},
		{
			name:       "backend Anthropic error with a backend request ID",
			status:     http.StatusBadRequest,
			body:       `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is too large"},"request_id":"req_backend"}`,
			wantStatus: http.StatusBadRequest, wantMessage: "max_tokens is too large",
			nativeOpenAI: true, nativeAnthropic: true,
		},
		{
			name:       "backend Anthropic-shaped error with a non-Anthropic type",
			status:     http.StatusBadRequest,
			body:       `{"type":"error","error":{"type":"BadRequestError","message":"stop cannot contain an empty string."}}`,
			wantStatus: http.StatusBadRequest, wantMessage: "stop cannot contain an empty string.",
			nativeOpenAI: true,
		},
		{
			name:       "vLLM request validation error",
			status:     http.StatusBadRequest,
			body:       `{"error":{"message":"1 validation error:\n  {'type': 'list_type', 'loc': ('body', 'stop_sequences'), 'msg': 'Input should be a valid list', 'input': 'BETA'}","type":"Bad Request","param":null,"code":400}}`,
			wantStatus: http.StatusBadRequest, wantMessage: "1 validation error:\n  {'type': 'list_type', 'loc': ('body', 'stop_sequences'), 'msg': 'Input should be a valid list', 'input': 'BETA'}",
			nativeOpenAI: true,
		},
		{
			name:       "older vLLM error",
			status:     http.StatusBadRequest,
			body:       `{"object":"error","message":"The model does not exist.","type":"NotFoundError","param":null,"code":404}`,
			wantStatus: http.StatusBadRequest, wantMessage: "The model does not exist.",
		},
		{
			name:       "FastAPI validation error",
			status:     http.StatusUnprocessableEntity,
			body:       `{"detail":[{"type":"missing","loc":["body","messages"],"msg":"Field required","input":{}},{"loc":["body","max_tokens"],"msg":"Input should be a valid integer"}]}`,
			wantStatus: http.StatusUnprocessableEntity, wantMessage: "body.messages: Field required; body.max_tokens: Input should be a valid integer",
		},
		{
			name:       "pylon problem details",
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"about:blank","title":"Service Unavailable","status":503,"detail":"local upstream connection failed"}`,
			wantStatus: http.StatusServiceUnavailable, wantMessage: "local upstream connection failed",
		},
		{
			name:       "problem details without detail",
			status:     http.StatusServiceUnavailable,
			body:       `{"type":"about:blank","title":"Backend Unavailable","status":503}`,
			wantStatus: http.StatusServiceUnavailable, wantMessage: "Backend Unavailable",
		},
		{
			name:       "unknown JSON shape",
			status:     http.StatusInternalServerError,
			body:       `{"unexpected":{"nested":true}}`,
			wantStatus: http.StatusInternalServerError, wantMessage: "Internal Server Error",
		},
		{
			name:       "non-JSON body",
			status:     http.StatusBadGateway,
			body:       "upstream connect error",
			wantStatus: http.StatusBadGateway, wantMessage: "upstream connect error",
		},
	} {
		for _, endpoint := range upstreamErrorEndpoints {
			for _, expose := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/expose=%t", tc.name, endpoint.name, expose), func(t *testing.T) {
					t.Parallel()

					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if tc.routerCode != "" {
							w.Header().Set("X-Stargate-Error-Code", tc.routerCode)
						}
						w.Header().Set("Retry-After", "2")
						w.WriteHeader(tc.status)
						_, _ = io.WriteString(w, tc.body)
					}))
					defer upstream.Close()

					cfg := config.Default()
					cfg.ExposeRouterErrorCodes = expose
					e, _ := messagesTestAPI(t, upstream.URL, cfg, nil, nil)
					req := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.payload))
					req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
					req.Header.Set("Authorization", "Bearer caller-token")
					req.Header.Set(HeaderRequestID, "request-a")
					rec := httptest.NewRecorder()
					e.ServeHTTP(rec, req)

					require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
					require.Equal(t, echo.MIMEApplicationJSON, rec.Header().Get(echo.HeaderContentType))
					require.Equal(t, "request-a", rec.Header().Get(HeaderRequestID))
					if endpoint.path != "/v1/chat/completions" {
						// Proxy endpoints keep upstream headers; chat completions never relayed them.
						require.Equal(t, "2", rec.Header().Get("Retry-After"))
					}
					require.Empty(t, rec.Header().Values("X-Stargate-Error-Code"))

					switch {
					case endpoint.anthropic && tc.nativeAnthropic:
						// Relayed with the NVCF request ID in place of the backend's.
						require.JSONEq(t, withRequestID(t, tc.body, "request-a"), rec.Body.String())
					case !endpoint.anthropic && tc.nativeOpenAI:
						require.Equal(t, tc.body, rec.Body.String())
					case endpoint.anthropic:
						require.JSONEq(t, anthropicErrorJSON(t, anthropicTypes[tc.wantStatus], tc.wantMessage, "request-a"), rec.Body.String())
					default:
						var code any
						if tc.wantStatus == 529 {
							code = "overloaded_error"
						} else if expose && tc.routerCode != "" {
							code = tc.routerCode
						}
						require.JSONEq(t, openAIErrorJSON(t, openAITypes[tc.wantStatus], tc.wantMessage, code), rec.Body.String())
					}
				})
			}
		}
	}
}

var openAITypes = map[int]string{
	http.StatusBadRequest:            "invalid_request_error",
	http.StatusUnauthorized:          "authentication_error",
	http.StatusNotFound:              "invalid_request_error",
	http.StatusRequestEntityTooLarge: "invalid_request_error",
	http.StatusUnprocessableEntity:   "invalid_request_error",
	http.StatusInternalServerError:   "server_error",
	http.StatusBadGateway:            "server_error",
	http.StatusServiceUnavailable:    "server_error",
	529:                              "overloaded_error",
}

var anthropicTypes = map[int]string{
	http.StatusBadRequest:            "invalid_request_error",
	http.StatusUnauthorized:          "authentication_error",
	http.StatusNotFound:              "not_found_error",
	http.StatusRequestEntityTooLarge: "request_too_large",
	http.StatusUnprocessableEntity:   "invalid_request_error",
	http.StatusInternalServerError:   "api_error",
	http.StatusBadGateway:            "api_error",
	http.StatusServiceUnavailable:    "api_error",
	529:                              "overloaded_error",
}

func openAIErrorJSON(t *testing.T, errorType, message string, code any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"error": map[string]any{
		"message": message, "type": errorType, "param": nil, "code": code,
	}})
	require.NoError(t, err)
	return string(body)
}

func anthropicErrorJSON(t *testing.T, errorType, message, requestID string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"type":       "error",
		"error":      map[string]string{"type": errorType, "message": message},
		"request_id": requestID,
	})
	require.NoError(t, err)
	return string(body)
}

func withRequestID(t *testing.T, body, requestID string) string {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &fields))
	fields["request_id"] = requestID
	out, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(out)
}

func TestGatewayErrorEnvelopes(t *testing.T) {
	t.Parallel()

	for _, endpoint := range upstreamErrorEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			t.Parallel()

			e, _ := messagesTestAPI(t, "http://127.0.0.1:1", config.Default(), nil, nil)
			req := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.payload))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.Header.Set(HeaderRequestID, "request-a")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			message := "bearer authorization is required"
			if endpoint.anthropic {
				message = "authorization required: send the API key in Authorization: Bearer or x-api-key"
				require.JSONEq(t, anthropicErrorJSON(t, "authentication_error", message, "request-a"), rec.Body.String())
				return
			}
			require.JSONEq(t, openAIErrorJSON(t, "authentication_error", message, nil), rec.Body.String())
		})
	}
}

func TestGatewayErrorHandlerHead(t *testing.T) {
	t.Parallel()

	e := echo.New()
	e.HTTPErrorHandler = newHTTPErrorHandler(false)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/missing", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, rec.Body.String())
}

func TestUpstreamErrorBodyEncodingAndSize(t *testing.T) {
	t.Parallel()

	const (
		routerBody = `{"error":"no eligible candidates","code":"no_eligible_candidates"}`
		nativeBody = `{"error":{"message":"max_tokens is too large","type":"invalid_request_error","param":"max_tokens","code":"invalid_value"}}`
	)
	padding := strings.Repeat("x", provider.MaxUpstreamErrorBodyBytes)
	for _, tc := range []struct {
		name        string
		status      int
		routerCode  string
		encoding    string
		body        []byte
		wantMessage string
		// wantRelay is the body OpenAI endpoints relay unchanged, if any.
		wantRelay string
	}{
		{
			name:   "gzip router rejection",
			status: http.StatusNotFound, routerCode: "no_eligible_candidates",
			encoding: "gzip", body: gzipBytes(t, routerBody),
			wantMessage: "no eligible candidates",
		},
		{
			name:     "gzip native backend error",
			status:   http.StatusBadRequest,
			encoding: "gzip", body: gzipBytes(t, nativeBody),
			wantMessage: "max_tokens is too large", wantRelay: nativeBody,
		},
		{
			name:     "unsupported encoding",
			status:   http.StatusBadRequest,
			encoding: "br", body: []byte("\x1b\x00 compressed"),
			wantMessage: "Bad Request",
		},
		{
			name:        "oversized validation error",
			status:      http.StatusUnprocessableEntity,
			body:        []byte(`{"detail":[{"loc":["body","input"],"msg":"too long","input":"` + padding + `"}]}`),
			wantMessage: "Unprocessable Entity",
		},
		{
			name:        "oversized native backend error",
			status:      http.StatusBadRequest,
			body:        []byte(`{"error":{"message":"` + padding + `","type":"invalid_request_error"}}`),
			wantMessage: "Bad Request",
		},
	} {
		for _, endpoint := range upstreamErrorEndpoints {
			t.Run(tc.name+"/"+endpoint.name, func(t *testing.T) {
				t.Parallel()

				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if tc.routerCode != "" {
						w.Header().Set("X-Stargate-Error-Code", tc.routerCode)
					}
					if tc.encoding != "" {
						w.Header().Set(echo.HeaderContentEncoding, tc.encoding)
					}
					w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
					w.WriteHeader(tc.status)
					_, _ = w.Write(tc.body)
				}))
				defer upstream.Close()

				e, _ := messagesTestAPI(t, upstream.URL, config.Default(), nil, nil)
				req := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.payload))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				req.Header.Set(echo.HeaderAcceptEncoding, "gzip, br")
				req.Header.Set("Authorization", "Bearer caller-token")
				req.Header.Set(HeaderRequestID, "request-a")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)

				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				require.Empty(t, rec.Header().Get(echo.HeaderContentEncoding))
				switch {
				case endpoint.anthropic:
					require.JSONEq(t, anthropicErrorJSON(t, anthropicTypes[tc.status], tc.wantMessage, "request-a"), rec.Body.String())
				case tc.wantRelay != "":
					require.Equal(t, tc.wantRelay, rec.Body.String())
				default:
					require.JSONEq(t, openAIErrorJSON(t, openAITypes[tc.status], tc.wantMessage, nil), rec.Body.String())
				}
			})
		}
	}
}

func gzipBytes(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, err := writer.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

func TestRouterRejectionLogsErrorCode(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := zlog.Logger
	zlog.Logger = zerolog.New(&logs)
	t.Cleanup(func() { zlog.Logger = oldLogger })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Stargate-Error-Code", "no_eligible_candidates")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"no eligible candidates","code":"no_eligible_candidates"}`)
	}))
	defer upstream.Close()

	e, _ := messagesTestAPI(t, upstream.URL, config.Default(), nil, nil)
	for _, endpoint := range upstreamErrorEndpoints {
		logs.Reset()
		req := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.payload))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set("Authorization", "Bearer caller-token")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code, endpoint.name)
		require.NotContains(t, rec.Body.String(), "no_eligible_candidates", endpoint.name)
		require.Contains(t, logs.String(), `"level":"info"`, endpoint.name)
		require.Contains(t, logs.String(), `"router_error_code":"no_eligible_candidates"`, endpoint.name)
		require.Contains(t, logs.String(), `"message":"completed http request"`, endpoint.name)
	}
}
