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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/provider"
)

func TestRegisterRoutesRegistersOpenAIRoutes(t *testing.T) {
	t.Parallel()

	e := echo.New()
	RegisterRoutes(e, NewHandlers(config.Default(), nil, nil, nil))

	routes := make(map[string]struct{})
	for _, route := range e.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}

	expected := []string{
		http.MethodPost + " /v1/chat/completions",
		http.MethodPost + " /v1/responses",
		http.MethodPost + " /v1/embeddings",
	}

	for _, route := range expected {
		if _, ok := routes[route]; !ok {
			t.Fatalf("missing route %s", route)
		}
	}

	for route := range routes {
		if strings.Contains(route, " /api/openai/") {
			t.Fatalf("unexpected api/openai route %s", route)
		}
		if strings.Contains(route, " /openai/v1/") {
			t.Fatalf("unexpected openai-prefixed route %s", route)
		}
	}
}

func TestOpenAIChatCompletionsServesRequests(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	e := newTestAPI(cfg)

	body := `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response models.ChatCompletionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Object != models.ObjectChatCompletion {
		t.Fatalf("object = %q, want %q", response.Object, models.ObjectChatCompletion)
	}
	if response.Model != "fn-alpha/company-name/model-name" {
		t.Fatalf("model = %q, want fn-alpha/company-name/model-name", response.Model)
	}
}

func TestOpenAIChatCompletionsRejectsMissingFunctionPrefix(t *testing.T) {
	t.Parallel()

	e := newTestAPI(config.Default())

	body := `{"model":"alpha-model","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestOpenAIChatCompletionsRejectsMissingModel(t *testing.T) {
	t.Parallel()

	e := newTestAPI(config.Default())

	body := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestOpenAIResponsesServesRequests(t *testing.T) {
	t.Parallel()

	e := newTestAPI(config.Default())

	body := `{"model":"fn-alpha/company-name/model-name","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response["object"] != "response" {
		t.Fatalf("object = %v, want response", response["object"])
	}
	if response["model"] != "fn-alpha/company-name/model-name" {
		t.Fatalf("model = %v, want fn-alpha/company-name/model-name", response["model"])
	}
}

func TestOpenAIResponsesRejectsMissingModelPrefix(t *testing.T) {
	t.Parallel()

	e := newTestAPI(config.Default())

	body := `{"model":"alpha-model","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model prefix is required") {
		t.Fatalf("response body = %q", rec.Body.String())
	}
}

func newTestAPI(cfg *config.Config) *echo.Echo {
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	RegisterRoutes(
		e,
		NewHandlers(
			cfg,
			provider.NewEchoProvider(),
			nil,
			nil,
		),
	)
	return e
}
