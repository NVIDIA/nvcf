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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

const bareModelID = "meta/llama-3.1-8b-instruct"

func staticModeConfig(allowedPaths ...string) *config.Config {
	cfg := config.Default()
	cfg.Auth.APIKeysPath = "/etc/llm-api-gateway/api-keys.json"
	if len(allowedPaths) > 0 {
		cfg.Auth.StaticAllowedPaths = allowedPaths
	}
	return cfg
}

func staticKeyAuthResponse() *nvcf.InvocationAuthResponse {
	return &nvcf.InvocationAuthResponse{
		ClientAuthID: "api-key:team-a",
		RateLimitKey: "team-a",
	}
}

func TestNewContextMiddlewareStaticModeStoresBareModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		wantModel string
	}{
		{name: "namespaced model", body: `{"model":"meta/llama-3.1-8b-instruct"}`, wantModel: bareModelID},
		{name: "model without slash", body: `{"model":"llama-3.1-8b-instruct"}`, wantModel: "llama-3.1-8b-instruct"},
		{name: "prefix-looking model is not split", body: `{"model":"fn-chat/company-name/model-name"}`, wantModel: "fn-chat/company-name/model-name"},
		{name: "missing model still stores a context", body: `{"messages":[]}`, wantModel: ""},
		{name: "empty body still stores a context", body: ``, wantModel: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.Header.Set(echo.HeaderAuthorization, "Bearer sk-static")
			ec := echo.New().NewContext(req, httptest.NewRecorder())

			var got *requestctx.RequestContext
			handler := NewContextMiddleware(staticModeConfig())(func(ec echo.Context) error {
				got = ec.(*GatewayContext).RequestContext()
				return nil
			})
			if err := handler(ec); err != nil {
				t.Fatalf("handler returned error: %v", err)
			}

			if got == nil {
				t.Fatal("request context was not stored")
			}
			if got.RoutingKey != "" {
				t.Fatalf("routing key = %q, want empty", got.RoutingKey)
			}
			if got.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", got.Model, tc.wantModel)
			}
			if got.BearerToken != "sk-static" {
				t.Fatalf("bearer token = %q, want sk-static", got.BearerToken)
			}
		})
	}
}

func TestStaticKeyAuthMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		path        string
		bearer      string
		authErr     error
		wantStatus  int
		wantCalls   int
		wantMessage string
	}{
		{
			name: "valid key on allowed path", method: http.MethodPost, path: "/v1/chat/completions",
			bearer: "sk-static", wantStatus: http.StatusNoContent, wantCalls: 1,
		},
		{
			name: "missing bearer", method: http.MethodPost, path: "/v1/chat/completions",
			wantStatus: http.StatusUnauthorized, wantMessage: "bearer authorization is required",
		},
		{
			name: "rejected key", method: http.MethodPost, path: "/v1/chat/completions", bearer: "sk-wrong",
			authErr:    status.Error(codes.Unauthenticated, "authentication failed"),
			wantStatus: http.StatusUnauthorized, wantCalls: 1, wantMessage: "authentication failed",
		},
		{
			name: "valid key on path outside allowlist", method: http.MethodPost, path: "/v1/embeddings",
			bearer: "sk-static", wantStatus: http.StatusForbidden, wantCalls: 1,
			wantMessage: `endpoint \"/v1/embeddings\" is not enabled on this gateway`,
		},
		{
			name: "valid key on unknown path", method: http.MethodPost, path: "/v1/models/import",
			bearer: "sk-static", wantStatus: http.StatusForbidden, wantCalls: 1,
		},
		{
			name: "authentication precedes the path check", method: http.MethodPost, path: "/v1/embeddings",
			wantStatus: http.StatusUnauthorized,
		},
		{name: "health probe needs no key", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusNoContent},
		{name: "readiness probe needs no key", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusNoContent},
		{name: "info needs no key", method: http.MethodGet, path: "/info", wantStatus: http.StatusNoContent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authClient := &stubInvocationAuthClient{authResponse: staticKeyAuthResponse(), authErr: tc.authErr}
			e := echo.New()
			e.Use(NewContextMiddleware(staticModeConfig()))
			e.Use(NewStaticKeyAuthMiddleware(authClient, []string{"/v1/chat/completions"}))
			var nextCtx *requestctx.RequestContext
			e.Any("/*", func(ec echo.Context) error {
				nextCtx = ec.(*GatewayContext).RequestContext()
				return ec.NoContent(http.StatusNoContent)
			})

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"model":"`+bareModelID+`"}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			if tc.bearer != "" {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if authClient.authorizeCalls != tc.wantCalls {
				t.Fatalf("authorize calls = %d, want %d", authClient.authorizeCalls, tc.wantCalls)
			}
			if tc.wantMessage != "" && !strings.Contains(rec.Body.String(), tc.wantMessage) {
				t.Fatalf("response body = %q, want it to contain %q", rec.Body.String(), tc.wantMessage)
			}
			if tc.wantCalls > 0 && authClient.authorizeRoutingKey != "" {
				t.Fatalf("authorize routing key = %q, want empty", authClient.authorizeRoutingKey)
			}
			if tc.wantStatus != http.StatusNoContent || tc.wantCalls == 0 {
				return
			}
			if nextCtx == nil {
				t.Fatal("handler saw no request context")
			}
			if nextCtx.APIKeyID != "api-key:team-a" || nextCtx.RateLimitKey != "team-a" {
				t.Fatalf("request context = %+v, want api key attribution", nextCtx)
			}
			if nextCtx.RoutingKey != "" || nextCtx.Model != bareModelID {
				t.Fatalf("routing key = %q model = %q, want empty and %q", nextCtx.RoutingKey, nextCtx.Model, bareModelID)
			}
		})
	}
}

func TestStaticKeyAuthMiddlewareFailsClosedWithoutClient(t *testing.T) {
	t.Parallel()

	e := echo.New()
	e.Use(NewContextMiddleware(staticModeConfig()))
	e.Use(NewStaticKeyAuthMiddleware(nil, []string{"/v1/chat/completions"}))
	e.POST("/v1/chat/completions", func(ec echo.Context) error {
		return ec.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+bareModelID+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer sk-static")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestStaticKeyAuthMiddlewareRequiresRequestContext(t *testing.T) {
	t.Parallel()

	authClient := &stubInvocationAuthClient{authResponse: staticKeyAuthResponse()}
	e := echo.New()
	// Without the context middleware there is nothing to authenticate into.
	e.Use(NewStaticKeyAuthMiddleware(authClient, []string{"/v1/chat/completions"}))
	e.POST("/v1/chat/completions", func(ec echo.Context) error {
		return ec.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderAuthorization, "Bearer sk-static")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if authClient.authorizeCalls != 0 {
		t.Fatalf("authorize calls = %d, want 0", authClient.authorizeCalls)
	}
}

// TestStaticModeRoutesForwardBareModels exercises every LLM route in static
// key mode against a stubbed router: the model string travels unchanged in
// the body and X-Model, and neither X-Routing-Key nor the caller's
// Authorization reaches the router.
func TestStaticModeRoutesForwardBareModels(t *testing.T) {
	t.Parallel()

	routes := []struct {
		path string
		body string
	}{
		{"/v1/chat/completions", `{"model":"` + bareModelID + `","messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/responses", `{"model":"` + bareModelID + `","input":"hello"}`},
		{"/v1/embeddings", `{"model":"` + bareModelID + `","input":"hello"}`},
	}

	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			t.Parallel()

			type captured struct {
				header    http.Header
				bodyModel string
			}
			seen := make(chan captured, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				seen <- captured{header: r.Header.Clone(), bodyModel: payload.Model}
				switch r.URL.Path {
				case "/v1/embeddings":
					w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
					_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
				case "/v1/responses":
					w.Header().Set(echo.HeaderContentType, "text/event-stream")
					_, _ = io.WriteString(w, "event: response.completed\n"+
						`data: {"type":"response.completed","response":{"id":"resp_1","object":"response",`+
						`"model":"`+bareModelID+`","status":"completed","output":[]}}`+"\n\n")
				default:
					w.Header().Set(echo.HeaderContentType, "text/event-stream")
					_, _ = io.WriteString(w, `data: {"id":"chatcmpl-static","object":"chat.completion.chunk",`+
						`"created":123,"model":"`+bareModelID+`","choices":[{"index":0,`+
						`"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`+"\n\n"+
						"data: [DONE]\n\n")
				}
			}))
			defer upstream.Close()

			stargateProvider, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
			if err != nil {
				t.Fatalf("new stargate provider: %v", err)
			}
			cfg := staticModeConfig("/v1/chat/completions", "/v1/responses", "/v1/embeddings")
			authClient := &stubInvocationAuthClient{authResponse: staticKeyAuthResponse()}
			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			e.Use(NewStaticKeyAuthMiddleware(authClient, cfg.Auth.StaticAllowedPaths))
			RegisterRoutes(e, NewHandlers(cfg, stargateProvider, nil))

			req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.Header.Set(echo.HeaderAuthorization, "Bearer sk-static")
			req.Header.Set("X-Routing-Key", "smuggled-routing-key")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var got captured
			select {
			case got = <-seen:
			default:
				t.Fatal("router did not receive a request")
			}
			if model := got.header.Get("X-Model"); model != bareModelID {
				t.Fatalf("X-Model = %q, want %q", model, bareModelID)
			}
			if got.bodyModel != bareModelID {
				t.Fatalf("body model = %q, want %q", got.bodyModel, bareModelID)
			}
			if values := got.header.Values("X-Routing-Key"); len(values) != 0 {
				t.Fatalf("X-Routing-Key = %q, want absent", values)
			}
			if values := got.header.Values(echo.HeaderAuthorization); len(values) != 0 {
				t.Fatalf("Authorization = %q, want absent", values)
			}
		})
	}
}

func TestStaticModeHandlersRequireModelNotPrefix(t *testing.T) {
	t.Parallel()

	routes := []struct {
		path string
		body string
	}{
		{"/v1/chat/completions", `{"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/responses", `{"input":"hello"}`},
		{"/v1/embeddings", `{"input":"hello"}`},
	}

	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			t.Parallel()

			cfg := staticModeConfig("/v1/chat/completions", "/v1/responses", "/v1/embeddings")
			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			e.Use(NewStaticKeyAuthMiddleware(
				&stubInvocationAuthClient{authResponse: staticKeyAuthResponse()},
				cfg.Auth.StaticAllowedPaths,
			))
			RegisterRoutes(e, NewHandlers(cfg, provider.NewEchoProvider(), nil))

			req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.Header.Set(echo.HeaderAuthorization, "Bearer sk-static")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "model prefix is required") {
				t.Fatalf("static mode returned the NVCF prefix error: %s", rec.Body.String())
			}
		})
	}
}

func TestStaticModeSkipsPerModelURIAllowlist(t *testing.T) {
	t.Parallel()

	cfg := staticModeConfig()
	cfg.ModelURIAllowlistEnabled = true
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewStaticKeyAuthMiddleware(
		&stubInvocationAuthClient{authResponse: staticKeyAuthResponse()},
		cfg.Auth.StaticAllowedPaths,
	))
	// Specs that would refuse chat in NVCF mode are ignored in static mode.
	e.Use(modelSpecsMiddleware(map[string]nvcf.ModelSpec{bareModelID: {URIs: []string{"/v1/embeddings"}}}))
	RegisterRoutes(e, NewHandlers(cfg, provider.NewEchoProvider(), nil))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"`+bareModelID+`","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer sk-static")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestNVCFModeKeepsPrefixAuthBehavior pins the NVCF-mode contract that other
// suites rely on: "unauthenticated/check" is a 401 and a bare model is a 400.
func TestNVCFModeKeepsPrefixAuthBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		body        string
		bearer      string
		authErr     error
		wantStatus  int
		wantCalls   int
		wantMessage string
	}{
		{
			name: "unauthenticated check without bearer", path: "/v1/chat/completions",
			body:       `{"model":"unauthenticated/check","messages":[]}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "unauthenticated check with rejected bearer", path: "/v1/chat/completions",
			body: `{"model":"unauthenticated/check","messages":[]}`, bearer: "sk-bad",
			authErr:    status.Error(codes.Unauthenticated, "invalid key"),
			wantStatus: http.StatusUnauthorized, wantCalls: 1,
		},
		{
			name: "chat without prefix", path: "/v1/chat/completions",
			body:       `{"model":"alpha-model","messages":[{"role":"user","content":"hello"}]}`,
			wantStatus: http.StatusBadRequest, wantMessage: "model prefix is required",
		},
		{
			name: "responses without prefix", path: "/v1/responses",
			body:       `{"model":"alpha-model","input":"hello"}`,
			wantStatus: http.StatusBadRequest, wantMessage: "model prefix is required",
		},
		{
			name: "embeddings without prefix", path: "/v1/embeddings",
			body:       `{"model":"alpha-model","input":"hello"}`,
			wantStatus: http.StatusBadRequest, wantMessage: "model prefix is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			authClient := &stubInvocationAuthClient{authErr: tc.authErr}
			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			e.Use(NewNVCFAuthMiddleware(authClient))
			RegisterRoutes(e, NewHandlers(cfg, provider.NewEchoProvider(), nil))

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			if tc.bearer != "" {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if authClient.authorizeCalls != tc.wantCalls {
				t.Fatalf("authorize calls = %d, want %d", authClient.authorizeCalls, tc.wantCalls)
			}
			if tc.wantMessage != "" && !strings.Contains(rec.Body.String(), tc.wantMessage) {
				t.Fatalf("response body = %q, want it to contain %q", rec.Body.String(), tc.wantMessage)
			}
		})
	}
}

func TestRateLimitSubjectKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rateLimitKey string
		projectID    string
		routingKey   string
		want         string
	}{
		{name: "nvcf org", rateLimitKey: "nca-456", routingKey: "fn-chat", want: "nvcf:nca-456:routing_key:fn-chat"},
		{
			name: "nvcf project", rateLimitKey: "nca-456", projectID: "project-789", routingKey: "fn-chat",
			want: "nvcf:nca-456:project:project-789:routing_key:fn-chat",
		},
		{name: "static key", rateLimitKey: "team-a", want: "nvcf:team-a"},
		{name: "static key with project", rateLimitKey: "team-a", projectID: "project-789", want: "nvcf:team-a:project:project-789"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := rateLimitSubjectKey(tc.rateLimitKey, tc.projectID, tc.routingKey); got != tc.want {
				t.Fatalf("rateLimitSubjectKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCallerLimitResolverWithoutRoutingKey(t *testing.T) {
	t.Parallel()

	limits, err := CallerLimitResolver{}.ResolveLimits(
		context.Background(),
		&requestctx.RequestContext{
			RateLimitKey: "team-a",
			Model:        bareModelID,
			ModelSpecs: map[string]nvcf.ModelSpec{
				bareModelID: {TokenRateLimit: "9000-M"},
			},
		},
		"/v1/chat/completions",
	)
	if err != nil {
		t.Fatalf("resolve limits: %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(limits))
	}
	if limits[0].SubjectKey != "nvcf:team-a" {
		t.Fatalf("subject key = %q, want nvcf:team-a", limits[0].SubjectKey)
	}
	if limits[0].SubjectRepr != "rate limit key `team-a`" {
		t.Fatalf("subject repr = %q", limits[0].SubjectRepr)
	}
	if limits[0].TokensPerMinute != 9000 {
		t.Fatalf("tokens per minute = %d, want 9000", limits[0].TokensPerMinute)
	}
}

func TestCallerLimitResolverRequiresRateLimitKey(t *testing.T) {
	t.Parallel()

	limits, err := CallerLimitResolver{}.ResolveLimits(
		context.Background(),
		&requestctx.RequestContext{
			RoutingKey: "fn-chat",
			Model:      "company-name/model-name",
			ModelSpecs: map[string]nvcf.ModelSpec{
				"company-name/model-name": {TokenRateLimit: "9000-M"},
			},
		},
		"/v1/chat/completions",
	)
	if err != nil {
		t.Fatalf("resolve limits: %v", err)
	}
	if len(limits) != 0 {
		t.Fatalf("len(limits) = %d, want 0 without a rate limit key", len(limits))
	}
}
