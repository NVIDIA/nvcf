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
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/provider"
)

func TestProxyEmbeddingsForwardsBodyAndHeaders(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		Path       string
		RoutingKey string
		Model      string
		BodyModel  string
	}

	captured := make(chan capturedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}

		captured <- capturedRequest{
			Path:       r.URL.Path,
			RoutingKey: r.Header.Get("X-Routing-Key"),
			Model:      r.Header.Get("X-Model"),
			BodyModel:  payload.Model,
		}
		w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		_, _ = io.WriteString(w, `{"object":"list","data":[],"model":"company-name/model-name"}`)
	}))
	defer upstream.Close()

	cfg := config.Default()
	proxyProvider, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
	if err != nil {
		t.Fatalf("new stargate provider: %v", err)
	}
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	RegisterRoutes(
		e,
		NewHandlers(
			cfg,
			proxyProvider,
			nil,
			nil,
		),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/embeddings",
		strings.NewReader(`{"model":"fn-alpha/company-name/model-name","input":"hello"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	got := <-captured
	if got.Path != "/v1/embeddings" {
		t.Fatalf("path = %q, want /v1/embeddings", got.Path)
	}
	if got.RoutingKey != "fn-alpha" {
		t.Fatalf("routing key = %q, want fn-alpha", got.RoutingKey)
	}
	if got.Model != "company-name/model-name" {
		t.Fatalf("header model = %q, want company-name/model-name", got.Model)
	}
	if got.BodyModel != "company-name/model-name" {
		t.Fatalf("body model = %q, want company-name/model-name", got.BodyModel)
	}
}

func TestEmbeddingsRejectsMissingModelPrefix(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
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

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/embeddings",
		strings.NewReader(`{"model":"alpha-model","input":"hello"}`),
	)
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
