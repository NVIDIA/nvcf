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
)

func TestNewContextMiddlewareExtractsRoutingKeyFromModelPrefix(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(HeaderTargetRegion, "us-west")
	req.Header.Set(echo.HeaderAuthorization, "Bearer sk-test")

	rec := httptest.NewRecorder()
	ec := e.NewContext(req, rec)

	handler := NewContextMiddleware(cfg)(func(ec echo.Context) error {
		gc, ok := ec.(*GatewayContext)
		if !ok {
			t.Fatalf("context type = %T, want *GatewayContext", ec)
		}

		reqCtx := gc.RequestContext()
		if reqCtx == nil {
			t.Fatal("request context was not set")
		}

		if reqCtx.RoutingKey != "fn-chat" {
			t.Fatalf("routing key = %q, want fn-chat", reqCtx.RoutingKey)
		}
		if reqCtx.BearerToken != "sk-test" {
			t.Fatalf("bearer token = %q, want sk-test", reqCtx.BearerToken)
		}
		if reqCtx.TargetRegion != "us-west" {
			t.Fatalf("target region = %q, want us-west", reqCtx.TargetRegion)
		}
		if reqCtx.RequestID == "" {
			t.Fatal("request id was not set")
		}
		if got := gc.Response().Header().Get(HeaderRequestID); got != reqCtx.RequestID {
			t.Fatalf("response request id = %q, want %q", got, reqCtx.RequestID)
		}

		return gc.NoContent(http.StatusNoContent)
	})

	if err := handler(ec); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
}

func TestNewContextMiddlewareAcceptsLegacyTargetRegionHeader(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(HeaderLegacyTargetRegion, "us-legacy")

	rec := httptest.NewRecorder()
	ec := e.NewContext(req, rec)

	handler := NewContextMiddleware(cfg)(func(ec echo.Context) error {
		reqCtx := ec.(*GatewayContext).RequestContext()
		if reqCtx == nil {
			t.Fatal("request context was not set")
		}
		if reqCtx.TargetRegion != "us-legacy" {
			t.Fatalf("target region = %q, want us-legacy", reqCtx.TargetRegion)
		}
		return ec.NoContent(http.StatusNoContent)
	})

	if err := handler(ec); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
}

func TestNewContextMiddlewareNoReqCtxWhenHeaderMissing(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	rec := httptest.NewRecorder()
	ec := e.NewContext(req, rec)

	handler := NewContextMiddleware(cfg)(func(ec echo.Context) error {
		gc := ec.(*GatewayContext)
		if gc.RequestContext() != nil {
			t.Fatal("request context was set unexpectedly")
		}
		return gc.NoContent(http.StatusNoContent)
	})

	if err := handler(ec); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
}

func TestNewContextMiddlewareNoReqCtxWhenHeaderEmpty(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set(HeaderFunctionID, "   ")

	rec := httptest.NewRecorder()
	ec := e.NewContext(req, rec)

	handler := NewContextMiddleware(cfg)(func(ec echo.Context) error {
		gc := ec.(*GatewayContext)
		if gc.RequestContext() != nil {
			t.Fatal("request context was set unexpectedly for whitespace-only header")
		}
		return gc.NoContent(http.StatusNoContent)
	})

	if err := handler(ec); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
}
