// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

const decisionTestModel = "company-name/model-name"

// decisionRoute is one of the two decision routes with the body a client sends.
type decisionRoute struct {
	name, path, body string
	systemOne        bool
}

var decisionRoutes = []decisionRoute{
	{name: "decisions", path: "/v1/decisions", body: `{"model":"fn-alpha/company-name/model-name","input":"hello","predicates":[{"id":"p1","text":"Is it red?"}]}`},
	{name: "systemone", path: "/v1/systemone", body: `{"model":"fn-alpha/company-name/model-name","state":"hello","questions":{"q1":{"type":"choice","options":["a","b"]}}}`, systemOne: true},
}

// staticLimitResolver applies the same limits to every request.
type staticLimitResolver []ratelimit.ResourceLimit

func (r staticLimitResolver) ResolveLimits(_ context.Context, _ *requestctx.RequestContext, _ string) ([]ratelimit.ResourceLimit, error) {
	return r, nil
}

// decisionTestAPI serves both decision routes against upstream. specs is the
// model spec map the auth response declares for function fn-alpha.
func decisionTestAPI(t *testing.T, upstream string, cfg *config.Config, limiter *recordingRateLimiter, specs map[string]nvcf.ModelSpec, opts ...HandlerOption) *echo.Echo {
	t.Helper()
	p, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream})
	require.NoError(t, err)
	auth := &stubInvocationAuthClient{authResponse: &nvcf.InvocationAuthResponse{
		RoutingKey: "fn-alpha", RateLimitKey: "org-test", AuthContext: map[string]string{"ncaId": "org-test"},
		ModelSpecs: specs,
	}}
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(auth))
	h := NewHandlers(cfg, p, nil, opts...)
	if limiter != nil {
		h.rateLimiter = limiter
	}
	RegisterRoutes(e, h)
	return e
}

func decisionSpecs(uris ...string) map[string]nvcf.ModelSpec {
	return map[string]nvcf.ModelSpec{decisionTestModel: {URIs: uris, RoutingMethod: "random"}}
}

func decisionRequest(route decisionRoute, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set(HeaderRequestID, "request-a")
	return req
}

// requireNativeError checks the error body is in the route's native shape.
func requireNativeError(t *testing.T, route decisionRoute, rec *httptest.ResponseRecorder, wantStatus int, wantOpenAIType, wantSystemOneCode string) {
	t.Helper()
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	errObject, ok := body["error"].(map[string]any)
	require.True(t, ok, rec.Body.String())
	if route.systemOne {
		require.Equal(t, wantSystemOneCode, errObject["code"], rec.Body.String())
		require.Equal(t, "request-a", errObject["request_id"], rec.Body.String())
		require.Equal(t, "request-a", body["request_id"], rec.Body.String())
		require.Equal(t, "request-a", rec.Header().Get(HeaderRequestID))
		return
	}
	require.Equal(t, wantOpenAIType, errObject["type"], rec.Body.String())
	require.Contains(t, body["error"], "message")
	require.NotContains(t, body, "request_id")
}

func TestDecisionRoutesForwardBodyAndHeaders(t *testing.T) {
	t.Parallel()

	type captured struct {
		Path, Model, RoutingKey, InputTokens, TokenEstimate string
		Header                                              http.Header
		Body                                                []byte
	}
	const responseBody = `{"results":[{"id":"p1","probability":0.25,"vendor":{"n":9007199254740993}}]}`
	for _, route := range decisionRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()

			got := make(chan captured, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				got <- captured{r.URL.Path, r.Header.Get("X-Model"), r.Header.Get("X-Routing-Key"), r.Header.Get("X-Input-Tokens"), r.Header.Get("X-Token-Estimate"), r.Header.Clone(), body}
				w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				w.Header().Set("X-Vendor-Response", "kept")
				_, _ = io.WriteString(w, responseBody)
			}))
			defer upstream.Close()

			cfg := config.Default()
			cfg.DecisionImageTokens = 1000
			e := decisionTestAPI(t, upstream.URL, cfg, nil, decisionSpecs(route.path))
			body := strings.Replace(route.body, `"hello"`, `"hello","images":["QUJD","QUJD"]`, 1)
			req := decisionRequest(route, body)
			req.Header.Set("TypeSafe-Danger-Zone-Images", "true")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, responseBody, rec.Body.String())
			require.Equal(t, echo.MIMEApplicationJSON, rec.Header().Get(echo.HeaderContentType))
			require.Equal(t, "kept", rec.Header().Get("X-Vendor-Response"))

			upstreamRequest := <-got
			require.Equal(t, route.path, upstreamRequest.Path)
			require.Equal(t, decisionTestModel, upstreamRequest.Model)
			require.Equal(t, "fn-alpha", upstreamRequest.RoutingKey)
			require.Equal(t, "true", upstreamRequest.Header.Get("TypeSafe-Danger-Zone-Images"))
			require.Equal(t, "Bearer caller-token", upstreamRequest.Header.Get("Authorization"))
			outbound := jsonObject(t, upstreamRequest.Body)
			require.JSONEq(t, `"`+decisionTestModel+`"`, string(outbound["model"]))
			require.JSONEq(t, `["QUJD","QUJD"]`, string(outbound["images"]))

			wantTokens := estimatedTokenCountForDecisionBody([]byte(body), 1000)
			require.GreaterOrEqual(t, wantTokens, 2000, "two images must count")
			require.Equal(t, strconv.Itoa(wantTokens), upstreamRequest.InputTokens)
			require.Equal(t, upstreamRequest.InputTokens, upstreamRequest.TokenEstimate)
		})
	}
}

func TestDecisionRoutesModelNotEnabled(t *testing.T) {
	t.Parallel()

	for _, route := range decisionRoutes {
		for _, tc := range []struct {
			name  string
			specs map[string]nvcf.ModelSpec
		}{
			{"route not declared", decisionSpecs("/v1/chat/completions")},
			{"only the other decision route declared", decisionSpecs(otherDecisionPath(route.path))},
			{"no uris declared", decisionSpecs()},
			{"no spec for the model", map[string]nvcf.ModelSpec{"company-name/other": {URIs: []string{route.path}}}},
			{"no specs at all", nil},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
				defer upstream.Close()

				e := decisionTestAPI(t, upstream.URL, config.Default(), nil, tc.specs)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, decisionRequest(route, route.body))

				requireNativeError(t, route, rec, http.StatusNotFound, "invalid_request_error", "not_found")
				require.Contains(t, rec.Body.String(), route.path)
				require.Zero(t, hits.Load(), "a model that is not enabled must not reach the backend")
			})
		}
	}
}

func otherDecisionPath(path string) string {
	if path == decisionsEndpointPath {
		return systemOneEndpointPath
	}
	return decisionsEndpointPath
}

func TestDecisionRoutesAcceptLenientlyDeclaredURIs(t *testing.T) {
	t.Parallel()

	for _, route := range decisionRoutes {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) }))
		defer upstream.Close()

		declared := strings.ToUpper(strings.TrimPrefix(route.path, "/")) + "/"
		e := decisionTestAPI(t, upstream.URL, config.Default(), nil, decisionSpecs("/v1/chat/completions", declared))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, decisionRequest(route, route.body))
		require.Equal(t, http.StatusOK, rec.Code, route.name+": "+rec.Body.String())
	}
}

func TestDecisionRoutesRejectBadRequests(t *testing.T) {
	t.Parallel()

	for _, route := range decisionRoutes {
		for _, tc := range []struct {
			name, body, wantSystemOneCode string
		}{
			{"malformed JSON", `{"model":"fn-alpha/company-name/model-name",`, "invalid_json"},
			{"empty body", ``, "invalid_json"},
			{"body not an object", `[1,2]`, "invalid_request"},
			{"model missing", `{"input":"hello"}`, "invalid_request"},
			{"model not a string", `{"model":7}`, "invalid_request"},
			{"model without function prefix", `{"model":"model"}`, "invalid_request"},
			{"model duplicate member case", `{"model":"fn-alpha/company-name/model-name","MODEL":"fn-alpha/company-name/other"}`, "invalid_request"},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
				defer upstream.Close()

				e := decisionTestAPI(t, upstream.URL, config.Default(), nil, decisionSpecs(route.path))
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, decisionRequest(route, tc.body))

				requireNativeError(t, route, rec, http.StatusBadRequest, "invalid_request_error", tc.wantSystemOneCode)
				require.Zero(t, hits.Load())
			})
		}
	}
}

func TestDecisionRoutesRequireBearerAuthorization(t *testing.T) {
	t.Parallel()

	for _, route := range decisionRoutes {
		e := decisionTestAPI(t, "http://127.0.0.1:1", config.Default(), nil, decisionSpecs(route.path))
		req := decisionRequest(route, route.body)
		req.Header.Del("Authorization")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		requireNativeError(t, route, rec, http.StatusUnauthorized, "authentication_error", "invalid_api_key")
	}
}

// TestDecisionRoutesRateLimitRefusal checks that an exhausted request count or
// input token limit refuses the request before the backend is called, with the
// route's native 429 and a Retry-After header.
func TestDecisionRoutesRateLimitRefusal(t *testing.T) {
	t.Parallel()

	limits := staticLimitResolver{{SubjectKey: "org-test:fn-alpha", RequestsPerMinute: 10, InputTokensPerMinute: 10_000}}
	for _, route := range decisionRoutes {
		for _, tc := range []struct {
			name      string
			exhausted ratelimit.LimitDimension
		}{
			{"request count", ratelimit.RequestsPerMinute},
			{"input tokens", ratelimit.InputTokensPerMinute},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
				defer upstream.Close()

				limiter := &recordingRateLimiter{currentValueFn: func(call rateLimitCall) int64 {
					if strings.HasSuffix(call.key, ":"+string(tc.exhausted)) {
						return 0
					}
					return call.limit.Limit
				}}
				e := decisionTestAPI(t, upstream.URL, config.Default(), limiter, decisionSpecs(route.path), WithLimitResolver(limits))
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, decisionRequest(route, route.body))

				requireNativeError(t, route, rec, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded")
				require.NotEmpty(t, rec.Header().Get("Retry-After"))
				require.Zero(t, hits.Load(), "a refused request must not reach the backend")
				var sawLimit bool
				for _, call := range limiter.Calls() {
					require.False(t, call.mustConsume, "a refused request must not consume")
					sawLimit = sawLimit || strings.HasSuffix(call.key, ":"+string(tc.exhausted))
				}
				require.True(t, sawLimit, "the %s dimension must be checked", tc.exhausted)
			})
		}
	}
}
