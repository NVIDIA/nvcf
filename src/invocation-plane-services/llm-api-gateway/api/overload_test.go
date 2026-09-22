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
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

func TestGatewayPreservesOverloadContractAcrossEndpoints(t *testing.T) {
	t.Parallel()

	const body = `{"error":{"code":"overloaded_error","message":"Inference capacity is temporarily unavailable.","param":"","type":"overloaded_error"}}`
	for _, tc := range []struct {
		name    string
		path    string
		payload string
	}{
		{"chat", "/v1/chat/completions", `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":false}`},
		{"streaming chat", "/v1/chat/completions", `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":true}`},
		{"responses", "/v1/responses", `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":false}`},
		{"streaming responses", "/v1/responses", `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":true}`},
		{"embeddings", "/v1/embeddings", `{"model":"fn-alpha/company-name/model-name","input":"hello"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				for _, name := range internalResponseHeadersForTest() {
					w.Header().Add(name, "internal-value")
					w.Header().Add(name, "second-value")
				}
				w.WriteHeader(529)
				_, _ = io.WriteString(w, body)
			}))
			defer upstream.Close()

			cfg := config.Default()
			proxyProvider, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
			if err != nil {
				t.Fatal(err)
			}
			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			RegisterRoutes(e, NewHandlers(cfg, proxyProvider, nil, nil))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.payload))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.Header.Set(HeaderRequestID, "request-a")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != 529 {
				t.Fatalf("status = %d, want 529: %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get(HeaderRequestID) != "request-a" {
				t.Fatal("overload response lost the public request ID")
			}
			var got, want models.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(body), &want); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("error = %+v, want %+v", got, want)
			}
			for _, name := range internalResponseHeadersForTest() {
				if values := rec.Header().Values(name); len(values) != 0 {
					t.Errorf("internal header %s leaked: %v", name, values)
				}
			}
		})
	}
}

func internalResponseHeadersForTest() []string {
	return []string{
		"x-stargate-retryable", "X-Stargate-Retry-Reason", "x-stargate-retry-after-ms",
		"x-stargate-upstream-retryable", "x-stargate-expected-queue-ms", "x-stargate-error-code",
		"x-stargate-auth-token", "x-stargate-cluster-id", "x-stargate-additional-control",
		"x-inference-server-id", "x-inference-server-url",
	}
}

func TestProxyResponseStripsInternalHeaders(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests, http.StatusServiceUnavailable, 529} {
		src := make(http.Header)
		for _, name := range internalResponseHeadersForTest() {
			src[name] = []string{"one", "two"}
		}
		src.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		src.Set("X-Request-Id", "request-a")
		src.Set("Retry-After", "2")
		src.Add("Vary", "Accept")
		src.Add("Vary", "Origin")
		src.Set("Content-Length", "42")
		e := echo.New()
		rec := httptest.NewRecorder()
		ctx := &GatewayContext{Context: e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)}
		if err := writeProxyResponse(ctx, &provider.ProxyResponse{
			StatusCode: status, Header: src, Body: io.NopCloser(strings.NewReader("application body")),
		}); err != nil {
			t.Fatal(err)
		}
		if rec.Code != status || rec.Body.String() != "application body" {
			t.Fatalf("response changed: %d %s", rec.Code, rec.Body.String())
		}
		want := http.Header{
			"Content-Type": []string{echo.MIMEApplicationJSON},
			"X-Request-Id": []string{"request-a"},
			"Retry-After":  []string{"2"},
			"Vary":         []string{"Accept", "Origin"},
		}
		if !reflect.DeepEqual(rec.Header(), want) {
			t.Fatalf("headers = %v, want %v", rec.Header(), want)
		}
	}
}
