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
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// routerSingleModel is the router's GET /v1/models for one Pylon-registered
// inference server.
const routerSingleModel = `{"model_ids":["m"],"entries":[{"model_id":"m","cluster_id":"spark-berlin",` +
	`"inference_server_id":"spark-berlin.models.llama.pylon-abc"}]}`

func routerEntry(model, cluster, server string) string {
	return `{"model_id":"` + model + `","cluster_id":"` + cluster + `","inference_server_id":"` + server + `"}`
}

func routerList(modelIDs string, entries ...string) string {
	return `{"model_ids":[` + modelIDs + `],"entries":[` + strings.Join(entries, ",") + `]}`
}

// newRegistryTestAPI serves the gateway routes against a real Stargate
// provider pointed at router.
func newRegistryTestAPI(t *testing.T, router http.Handler, stargate config.StargateConfig) *echo.Echo {
	t.Helper()

	upstream := httptest.NewServer(router)
	t.Cleanup(upstream.Close)
	stargate.URL = upstream.URL
	stargateProvider, err := provider.NewStargateProvider(stargate)
	if err != nil {
		t.Fatalf("new stargate provider: %v", err)
	}

	cfg := config.Default()
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	RegisterRoutes(e, NewHandlers(cfg, stargateProvider, nil))
	return e
}

func routerReturning(statusCode int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		w.WriteHeader(statusCode)
		_, _ = io.WriteString(w, body)
	}
}

func serveGet(e *echo.Echo, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func assertJSONResponse(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	mediaType, _, err := mime.ParseMediaType(rec.Header().Get(echo.HeaderContentType))
	if err != nil || mediaType != echo.MIMEApplicationJSON {
		t.Fatalf("content-type = %q, want %s", rec.Header().Get(echo.HeaderContentType), echo.MIMEApplicationJSON)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestModelsListsDistinctRouterModelIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		router string
		want   string
	}{
		{
			name:   "single model",
			router: routerSingleModel,
			want:   `{"object":"list","data":[{"id":"m","object":"model","created":0,"owned_by":"nvidia"}]}`,
		},
		{
			name: "one entry per model in router order",
			router: routerList(`"zeta","alpha"`,
				routerEntry("zeta", "c", "s1"),
				routerEntry("zeta", "c", "s2"),
				routerEntry("alpha", "c", "s3")),
			want: `{"object":"list","data":[` +
				`{"id":"zeta","object":"model","created":0,"owned_by":"nvidia"},` +
				`{"id":"alpha","object":"model","created":0,"owned_by":"nvidia"}]}`,
		},
		{
			name:   "duplicate and empty ids collapse",
			router: routerList(`"a","","a","b"`),
			want: `{"object":"list","data":[` +
				`{"id":"a","object":"model","created":0,"owned_by":"nvidia"},` +
				`{"id":"b","object":"model","created":0,"owned_by":"nvidia"}]}`,
		},
		{
			name:   "entry ids missing from model_ids are listed",
			router: routerList(`"a"`, routerEntry("a", "c", "s1"), routerEntry("b", "c", "s2")),
			want: `{"object":"list","data":[` +
				`{"id":"a","object":"model","created":0,"owned_by":"nvidia"},` +
				`{"id":"b","object":"model","created":0,"owned_by":"nvidia"}]}`,
		},
		{
			name:   "empty router list",
			router: `{"model_ids":[],"entries":[]}`,
			want:   `{"object":"list","data":[]}`,
		},
		{
			name:   "unknown router fields are ignored",
			router: `{"model_ids":["m"],"entries":[],"next":"x"}`,
			want:   `{"object":"list","data":[{"id":"m","object":"model","created":0,"owned_by":"nvidia"}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := newRegistryTestAPI(t, routerReturning(http.StatusOK, tc.router), config.StargateConfig{})
			assertJSONResponse(t, serveGet(e, "/v1/models"), tc.want)
		})
	}
}

func TestRegistryAggregatesRouterEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		router string
		want   string
	}{
		{
			name:   "single inference server",
			router: routerSingleModel,
			want: `{"object":"list","data":[` +
				`{"model":"m","clusterId":"spark-berlin","inferenceServers":1}]}`,
		},
		{
			name: "multiple servers per model sorted by model",
			router: routerList(`"zeta","alpha"`,
				routerEntry("zeta", "spark", "s1"),
				routerEntry("zeta", "spark", "s2"),
				routerEntry("zeta", "spark", "s3"),
				routerEntry("alpha", "spark", "s4")),
			want: `{"object":"list","data":[` +
				`{"model":"alpha","clusterId":"spark","inferenceServers":1},` +
				`{"model":"zeta","clusterId":"spark","inferenceServers":3}]}`,
		},
		{
			name: "unknown entry fields are ignored",
			router: `{"model_ids":["m"],"entries":[` +
				`{"model_id":"m","cluster_id":"spark","inference_server_id":"s1"},` +
				`{"model_id":"m","cluster_id":"spark","inference_server_id":"s2","extra":{"k":"v"}}]}`,
			want: `{"object":"list","data":[` +
				`{"model":"m","clusterId":"spark","inferenceServers":2}]}`,
		},
		{
			name: "disagreeing cluster ids are joined sorted",
			router: routerList(`"m"`,
				routerEntry("m", "spark-b", "s1"),
				routerEntry("m", "spark-a", "s2"),
				routerEntry("m", "spark-b", "s3")),
			want: `{"object":"list","data":[` +
				`{"model":"m","clusterId":"spark-a,spark-b","inferenceServers":3}]}`,
		},
		{
			name: "empty cluster ids are ignored",
			router: routerList(`"m"`,
				routerEntry("m", "", "s1"),
				routerEntry("m", "spark", "s2")),
			want: `{"object":"list","data":[` +
				`{"model":"m","clusterId":"spark","inferenceServers":2}]}`,
		},
		{
			name:   "empty router list",
			router: `{"model_ids":[],"entries":[]}`,
			want:   `{"object":"list","data":[]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := newRegistryTestAPI(t, routerReturning(http.StatusOK, tc.router), config.StargateConfig{})
			assertJSONResponse(t, serveGet(e, "/v1/registry"), tc.want)
		})
	}
}

func TestModelRegistryMapsRouterFailures(t *testing.T) {
	t.Parallel()

	hang := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	tests := []struct {
		name        string
		router      http.Handler
		timeout     time.Duration
		wantStatus  int
		wantMessage string
	}{
		{
			name: "router 500", router: routerReturning(http.StatusInternalServerError, `{"error":"boom"}`),
			wantStatus: http.StatusBadGateway, wantMessage: "model list is unavailable from the router",
		},
		{
			name: "router 503", router: routerReturning(http.StatusServiceUnavailable, ``),
			wantStatus: http.StatusBadGateway, wantMessage: "model list is unavailable from the router",
		},
		{
			name: "router without the endpoint", router: http.NotFoundHandler(),
			wantStatus: http.StatusBadGateway,
		},
		{
			name: "router body is not JSON", router: routerReturning(http.StatusOK, `<html>proxy error</html>`),
			wantStatus: http.StatusBadGateway, wantMessage: "model list is unavailable from the router",
		},
		{
			name: "router body is truncated", router: routerReturning(http.StatusOK, `{"model_ids":["m"`),
			wantStatus: http.StatusBadGateway,
		},
		{
			name: "router timeout", router: hang, timeout: 50 * time.Millisecond,
			wantStatus: http.StatusGatewayTimeout, wantMessage: "timed out",
		},
	}

	for _, tc := range tests {
		for _, path := range []string{"/v1/models", "/v1/registry"} {
			t.Run(tc.name+path, func(t *testing.T) {
				t.Parallel()

				e := newRegistryTestAPI(t, tc.router, config.StargateConfig{RequestTimeout: tc.timeout})
				rec := serveGet(e, path)

				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if tc.wantMessage != "" && !strings.Contains(rec.Body.String(), tc.wantMessage) {
					t.Fatalf("body = %q, want it to contain %q", rec.Body.String(), tc.wantMessage)
				}
			})
		}
	}
}

func TestModelRegistryDoesNotLeakRouterAddress(t *testing.T) {
	t.Parallel()

	closed := httptest.NewServer(http.NotFoundHandler())
	routerURL := closed.URL
	closed.Close()

	stargateProvider, err := provider.NewStargateProvider(config.StargateConfig{URL: routerURL})
	if err != nil {
		t.Fatalf("new stargate provider: %v", err)
	}
	cfg := config.Default()
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	RegisterRoutes(e, NewHandlers(cfg, stargateProvider, nil))

	rec := serveGet(e, "/v1/models")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	host := strings.TrimPrefix(routerURL, "http://")
	if strings.Contains(rec.Body.String(), host) {
		t.Fatalf("body %q leaks the router address %s", rec.Body.String(), host)
	}
}

func TestModelRegistryForwardsNoCallerHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		serviceToken string
		wantAuth     []string
	}{
		{name: "service token only", serviceToken: "router-token", wantAuth: []string{"Bearer router-token"}},
		{name: "no service token"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			captured := make(chan *http.Request, 1)
			router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured <- r.Clone(r.Context())
				routerReturning(http.StatusOK, routerSingleModel)(w, r)
			})
			e := newRegistryTestAPI(t, router, config.StargateConfig{ServiceToken: tc.serviceToken})

			req := httptest.NewRequest(http.MethodGet, "/v1/registry?model_ids=m", nil)
			req.Header.Set(echo.HeaderAuthorization, "Bearer sk-caller-secret")
			req.Header.Set("X-Routing-Key", "caller-routing-key")
			req.Header.Set("X-Model", "caller-model")
			req.Header.Set("X-Priority", "9")
			req.Header.Set(HeaderRequestID, "caller-request-id")
			req.Header.Set("Cookie", "session=caller-cookie")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			got := <-captured
			if got.Method != http.MethodGet || got.URL.Path != "/v1/models" || got.URL.RawQuery != "" {
				t.Fatalf("router request = %s %s?%s, want GET /v1/models", got.Method, got.URL.Path, got.URL.RawQuery)
			}
			if values := got.Header.Values(echo.HeaderAuthorization); strings.Join(values, ",") != strings.Join(tc.wantAuth, ",") {
				t.Fatalf("Authorization = %q, want %q", values, tc.wantAuth)
			}
			for name, values := range got.Header {
				for _, value := range values {
					if strings.Contains(value, "caller") {
						t.Fatalf("caller header value reached the router in %s: %q", name, value)
					}
				}
			}
		})
	}
}

func TestModelRegistryWithoutModelLister(t *testing.T) {
	t.Parallel()

	e := newTestAPI(config.Default())
	for _, path := range []string{"/v1/models", "/v1/registry"} {
		if rec := serveGet(e, path); rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s status = %d, want %d: %s", path, rec.Code, http.StatusNotImplemented, rec.Body.String())
		}
	}
}

func TestDiscoveryReadAuthentication(t *testing.T) {
	t.Parallel()

	const (
		static    = "static-keys"
		nvcfMode  = "nvcf"
		anonymous = "anonymous"
	)
	tests := []struct {
		name        string
		mode        string
		publicReads bool
		method      string
		path        string
		bearer      string
		authErr     error
		wantStatus  int
		wantCalls   int
	}{
		{name: "static public models without key", mode: static, publicReads: true,
			method: http.MethodGet, path: "/v1/models", wantStatus: http.StatusNoContent},
		{name: "static public registry without key", mode: static, publicReads: true,
			method: http.MethodGet, path: "/v1/registry", wantStatus: http.StatusNoContent},
		{name: "static public skips even a bad key", mode: static, publicReads: true,
			method: http.MethodGet, path: "/v1/models", bearer: "sk-wrong",
			authErr: status.Error(codes.Unauthenticated, "bad"), wantStatus: http.StatusNoContent},
		{name: "static public POST models is not exempt", mode: static, publicReads: true,
			method: http.MethodPost, path: "/v1/models", wantStatus: http.StatusUnauthorized},
		{name: "static public HEAD models is not exempt", mode: static, publicReads: true,
			method: http.MethodHead, path: "/v1/models", wantStatus: http.StatusUnauthorized},
		{name: "static public exact path only", mode: static, publicReads: true,
			method: http.MethodGet, path: "/v1/models/m", wantStatus: http.StatusUnauthorized},
		{name: "static private models without key", mode: static,
			method: http.MethodGet, path: "/v1/models", wantStatus: http.StatusUnauthorized},
		{name: "static private registry without key", mode: static,
			method: http.MethodGet, path: "/v1/registry", wantStatus: http.StatusUnauthorized},
		{name: "static private registry with key bypasses path allowlist", mode: static,
			method: http.MethodGet, path: "/v1/registry", bearer: "sk-static",
			wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "static private models with rejected key", mode: static,
			method: http.MethodGet, path: "/v1/models", bearer: "sk-wrong",
			authErr: status.Error(codes.Unauthenticated, "bad"), wantStatus: http.StatusUnauthorized, wantCalls: 1},
		{name: "static POST models with key stays behind path allowlist", mode: static,
			method: http.MethodPost, path: "/v1/models", bearer: "sk-static",
			wantStatus: http.StatusForbidden, wantCalls: 1},
		{name: "nvcf private models without bearer", mode: nvcfMode,
			method: http.MethodGet, path: "/v1/models", wantStatus: http.StatusUnauthorized},
		{name: "nvcf private registry with accepted bearer", mode: nvcfMode,
			method: http.MethodGet, path: "/v1/registry", bearer: "nvapi-ok",
			wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "nvcf private models with rejected bearer", mode: nvcfMode,
			method: http.MethodGet, path: "/v1/models", bearer: "nvapi-bad",
			authErr: status.Error(codes.Unauthenticated, "bad"), wantStatus: http.StatusUnauthorized, wantCalls: 1},
		{name: "nvcf public models without bearer", mode: nvcfMode, publicReads: true,
			method: http.MethodGet, path: "/v1/models", wantStatus: http.StatusNoContent},
		{name: "anonymous private models without key", mode: anonymous,
			method: http.MethodGet, path: "/v1/models", wantStatus: http.StatusNoContent},
		{name: "anonymous public registry without key", mode: anonymous, publicReads: true,
			method: http.MethodGet, path: "/v1/registry", wantStatus: http.StatusNoContent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authClient := &stubInvocationAuthClient{authResponse: staticKeyAuthResponse(), authErr: tc.authErr}
			option := WithPublicReadEndpoints(tc.publicReads)
			cfg := config.Default()
			var middleware echo.MiddlewareFunc
			switch tc.mode {
			case static:
				cfg = staticModeConfig()
				middleware = NewStaticKeyAuthMiddleware(authClient, cfg.Auth.StaticAllowedPaths, option)
			case nvcfMode:
				middleware = NewNVCFAuthMiddleware(authClient, option)
			default:
				middleware = NewNVCFAuthMiddleware(nil, option)
			}

			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			e.Use(middleware)
			e.Any("/*", func(ec echo.Context) error {
				return ec.NoContent(http.StatusNoContent)
			})

			req := httptest.NewRequest(tc.method, tc.path, nil)
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
			if tc.wantCalls > 0 && authClient.authorizeRoutingKey != "" {
				t.Fatalf("authorize routing key = %q, want empty", authClient.authorizeRoutingKey)
			}
		})
	}
}
