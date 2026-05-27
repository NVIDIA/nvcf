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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
)

func TestNVCFAuthMiddlewareEnrichesRequestContext(t *testing.T) {
	t.Parallel()

	authClient := &stubInvocationAuthClient{
		authResponse: &nvcf.InvocationAuthResponse{
			RoutingKey:   "fn-chat",
			ClientAuthID: "subject-123",
			AuthContext:  map[string]string{"ncaId": "nca-456"},
			RateLimitKey: "nca-456",
			ModelSpecs: map[string]nvcf.ModelSpec{
				"company-name/model-name": {
					TokenRateLimit: "9000-M,100000-D",
				},
			},
		},
	}

	cfg := config.Default()

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(authClient))
	e.POST("/v1/chat/completions", func(ec echo.Context) error {
		gc := ec.(*GatewayContext)
		reqCtx := gc.RequestContext()
		if reqCtx == nil {
			t.Fatal("request context was not set")
		}

		if reqCtx.APIKeyID != "subject-123" {
			t.Fatalf("api key id = %q, want subject-123", reqCtx.APIKeyID)
		}
		if reqCtx.OrgID != "nca-456" {
			t.Fatalf("org id = %q, want nca-456", reqCtx.OrgID)
		}
		if reqCtx.BearerToken != "sk-live" {
			t.Fatalf("bearer token = %q, want sk-live", reqCtx.BearerToken)
		}
		if reqCtx.RoutingKey != "fn-chat" {
			t.Fatalf("routing key = %q, want fn-chat", reqCtx.RoutingKey)
		}
		if reqCtx.ModelSpecs == nil {
			t.Fatal("model specs is nil")
		}
		spec, ok := reqCtx.ModelSpecs["company-name/model-name"]
		if !ok {
			t.Fatal("company-name/model-name not found in model specs")
		}
		if spec.TokenRateLimit != "9000-M,100000-D" {
			t.Fatalf("token rate limit = %q, want 9000-M,100000-D", spec.TokenRateLimit)
		}

		return gc.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer sk-live")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if authClient.authorizeToken != "sk-live" {
		t.Fatalf("authorize token = %q, want sk-live", authClient.authorizeToken)
	}
	if authClient.authorizeRoutingKey != "fn-chat" {
		t.Fatalf("authorize routing key = %q, want fn-chat", authClient.authorizeRoutingKey)
	}
	if authClient.authorizeCalls != 1 {
		t.Fatalf("authorize calls = %d, want 1", authClient.authorizeCalls)
	}
}

func TestNVCFAuthMiddlewareRejectsMissingBearerToken(t *testing.T) {
	t.Parallel()

	authClient := &stubInvocationAuthClient{}

	cfg := config.Default()

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(authClient))
	e.POST("/v1/chat/completions", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if authClient.authorizeCalls != 0 {
		t.Fatalf("authorize calls = %d, want 0", authClient.authorizeCalls)
	}
}

func TestNVCFAuthMiddlewareRejectsMissingRateLimitKey(t *testing.T) {
	t.Parallel()

	authClient := &stubInvocationAuthClient{
		authResponse: &nvcf.InvocationAuthResponse{
			RoutingKey:   "fn-alpha",
			ClientAuthID: "subject-123",
			AuthContext:  map[string]string{},
		},
	}

	cfg := config.Default()

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(authClient))
	e.POST("/v1/chat/completions", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer sk-live")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestNVCFAuthMiddlewareSkipsWhenNoRoutingKey(t *testing.T) {
	t.Parallel()

	authClient := &stubInvocationAuthClient{}

	cfg := config.Default()

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(authClient))
	e.GET("/healthz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if authClient.authorizeCalls != 0 {
		t.Fatalf("authorize calls = %d, want 0", authClient.authorizeCalls)
	}
}

func TestNVCFAuthMiddlewareUsesProjectScopedRateLimitKeyWhenPresent(t *testing.T) {
	t.Parallel()

	authClient := &stubInvocationAuthClient{
		authResponse: &nvcf.InvocationAuthResponse{
			RoutingKey:   "fn-chat",
			ClientAuthID: "subject-123",
			ProjectID:    "project-789",
			AuthContext: map[string]string{
				"ncaId":     "nca-456",
				"projectId": "project-789",
			},
			RateLimitKey: "nca-456",
		},
	}

	cfg := config.Default()

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(authClient))
	e.POST("/v1/chat/completions", func(ec echo.Context) error {
		gc := ec.(*GatewayContext)
		reqCtx := gc.RequestContext()
		if reqCtx == nil {
			t.Fatal("request context was not set")
		}
		if reqCtx.ProjectID != "project-789" {
			t.Fatalf("project id = %q, want project-789", reqCtx.ProjectID)
		}

		return gc.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer sk-live")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

type stubInvocationAuthClient struct {
	authResponse        *nvcf.InvocationAuthResponse
	authorizeCalls      int
	authorizeToken      string
	authorizeRoutingKey string
}

func (s *stubInvocationAuthClient) AuthorizeInvocation(
	_ context.Context,
	clientAuthorizationToken string,
	routingKey string,
) (*nvcf.InvocationAuthResponse, error) {
	s.authorizeCalls++
	s.authorizeToken = clientAuthorizationToken
	s.authorizeRoutingKey = routingKey
	return s.authResponse, nil
}
