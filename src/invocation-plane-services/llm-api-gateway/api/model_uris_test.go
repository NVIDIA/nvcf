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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

func TestModelURIMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		uri  string
		want bool
	}{
		{uri: "/v1/embeddings", want: true},
		{uri: "v1/embeddings", want: true},
		{uri: "/v1/embeddings/", want: true},
		{uri: "  /v1/embeddings  ", want: true},
		{uri: "/V1/Embeddings", want: true},
		{uri: "V1/EMBEDDINGS//", want: true},
		{uri: "/v1/chat/completions", want: false},
		{uri: "/v1/embedding", want: false},
		{uri: "", want: false},
		{uri: "/", want: false},
	}

	for _, tc := range tests {
		if got := modelURIMatches(tc.uri, embeddingsEndpointPath); got != tc.want {
			t.Errorf("modelURIMatches(%q, %q) = %v, want %v", tc.uri, embeddingsEndpointPath, got, tc.want)
		}
	}
}

func TestChatCompletionsModelURIAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		uris       []string
		enforce    bool
		wantStatus int
	}{
		{
			name:       "rejects undeclared endpoint when enforced",
			uris:       []string{"/v1/embeddings"},
			enforce:    true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "allows undeclared endpoint in log mode",
			uris:       []string{"/v1/embeddings"},
			enforce:    false,
			wantStatus: http.StatusOK,
		},
		{
			name:       "allows declared endpoint",
			uris:       []string{"/v1/chat/completions"},
			enforce:    true,
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.ModelURIEnforce = tc.enforce
			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			e.Use(modelSpecsMiddleware(map[string]nvcf.ModelSpec{
				"company-name/model-name": {URIs: tc.uris},
			}))
			RegisterRoutes(
				e,
				NewHandlers(
					cfg,
					provider.NewEchoProvider(),
					nil,
					nil,
				),
			)

			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/chat/completions",
				strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
			)
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusBadRequest &&
				!strings.Contains(rec.Body.String(), chatCompletionsEndpointPath) {
				t.Fatalf("response body missing endpoint: %s", rec.Body.String())
			}
		})
	}
}
