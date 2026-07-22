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
)

// newPriorityGuardTestServer wires the production route registration so these
// tests fail if the guard is ever dropped from the LLM route group.
func newPriorityGuardTestServer(authClient InvocationAuthClient) *echo.Echo {
	cfg := config.Default()
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(authClient))
	RegisterRoutes(e, NewHandlers(cfg, nil, nil))
	return e
}

func TestLLMRoutesRejectClientSuppliedPriority(t *testing.T) {
	t.Parallel()

	paths := []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings"}
	values := []struct {
		name     string
		priority string
	}{
		{name: "explicit value", priority: "0"},
		{name: "empty value", priority: ""},
	}

	for _, path := range paths {
		for _, value := range values {
			t.Run(path+" "+value.name, func(t *testing.T) {
				t.Parallel()

				authClient := &stubInvocationAuthClient{
					authResponse: &nvcf.InvocationAuthResponse{
						RoutingKey:   "fn-chat",
						ClientAuthID: "subject-123",
						AuthContext:  map[string]string{"ncaId": "nca-456"},
						RateLimitKey: "nca-456",
					},
				}

				e := newPriorityGuardTestServer(authClient)

				req := httptest.NewRequest(
					http.MethodPost,
					path,
					strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
				)
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				req.Header.Set(echo.HeaderAuthorization, "Bearer sk-live")
				req.Header.Set(headerPriority, value.priority)
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "reserved header") {
					t.Fatalf("body = %q, want the reserved-header rejection message", rec.Body.String())
				}
			})
		}
	}

	// The guard is route middleware on the LLM group, so it runs after the
	// global auth middleware: the reject does not skip authentication.
	t.Run("auth runs before the reject", func(t *testing.T) {
		t.Parallel()

		authClient := &stubInvocationAuthClient{
			authResponse: &nvcf.InvocationAuthResponse{
				RoutingKey:   "fn-chat",
				ClientAuthID: "subject-123",
				AuthContext:  map[string]string{"ncaId": "nca-456"},
				RateLimitKey: "nca-456",
			},
		}

		e := newPriorityGuardTestServer(authClient)

		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/chat/completions",
			strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
		)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(echo.HeaderAuthorization, "Bearer sk-live")
		req.Header.Set(headerPriority, "0")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "reserved header") {
			t.Fatalf("body = %q, want the reserved-header rejection message", rec.Body.String())
		}
		if authClient.authorizeCalls != 1 {
			t.Fatalf("authorize calls = %d, want 1", authClient.authorizeCalls)
		}
	})

	// The guard is scoped to the LLM route group; non-proxied routes such as
	// the health endpoints accept requests regardless of X-Priority.
	t.Run("healthz not rejected", func(t *testing.T) {
		t.Parallel()

		e := newPriorityGuardTestServer(&stubInvocationAuthClient{})

		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(headerPriority, "1")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
	})
}

// A request without X-Priority must pass the guard untouched and reach the next
// handler. Positive control: without this, a guard that rejected unconditionally
// would still satisfy every rejection case above while breaking all legitimate
// LLM traffic. The guard runs after authentication in the wired server, so its
// only remaining job on a clean request is to forward it, which this asserts
// directly rather than driving the full provider stack behind the LLM routes.
func TestRejectClientSuppliedPriorityForwardsRequestWithoutHeader(t *testing.T) {
	t.Parallel()

	forwarded := false
	next := func(c echo.Context) error {
		forwarded = true
		return c.NoContent(http.StatusNoContent)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := rejectClientSuppliedPriority(next)(c); err != nil {
		t.Fatalf("guard returned error for a request without X-Priority: %v", err)
	}
	if !forwarded {
		t.Fatal("guard did not forward a request without X-Priority to the next handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
