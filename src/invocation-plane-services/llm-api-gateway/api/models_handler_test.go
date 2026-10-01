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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
)

const (
	modelsTestStart = int64(1759741923)

	modelListingUnavailableBody = `{"error":{"code":"model_listing_unavailable",` +
		`"message":"The model listing is temporarily unavailable","param":"","type":"server_error"}}`
)

// stubRouter serves the LLM Request Router listing endpoint.
type stubRouter struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	modelIDs []string
	status   int
	body     string
	block    bool
	calls    int
	// gate, when set, holds each request until it is closed. received
	// reports each request as it arrives.
	gate     chan struct{}
	received chan struct{}
}

func newStubRouter(t *testing.T, modelIDs ...string) *stubRouter {
	t.Helper()
	router := &stubRouter{t: t, modelIDs: modelIDs, status: http.StatusOK}
	router.server = httptest.NewServer(http.HandlerFunc(router.serveHTTP))
	t.Cleanup(router.server.Close)
	return router
}

func (r *stubRouter) serveHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet || req.URL.Path != "/v1/models" || req.URL.RawQuery != "" {
		r.t.Errorf("router got %s %s, want GET /v1/models with no query", req.Method, req.URL.RequestURI())
	}

	r.mu.Lock()
	r.calls++
	modelIDs, status, body, block, gate, received := r.modelIDs, r.status, r.body, r.block, r.gate, r.received
	r.mu.Unlock()

	if received != nil {
		select {
		case received <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}

	if block {
		<-req.Context().Done()
		return
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	if body != "" {
		_, _ = w.Write([]byte(body))
		return
	}
	w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	_ = json.NewEncoder(w).Encode(routableListing(modelIDs))
}

// routableListing is a router listing where every model has one routable
// server, the way the router reports a model that chat requests can reach.
func routableListing(modelIDs []string) provider.ModelListing {
	listing := provider.ModelListing{ModelIDs: modelIDs, Models: make([]provider.RegisteredModel, 0, len(modelIDs))}
	for _, id := range modelIDs {
		listing.Models = append(listing.Models, provider.RegisteredModel{
			ModelID:  id,
			Clusters: []provider.ClusterRegistration{{ClusterID: "cluster-a", RegisteredServers: 1, HealthyServers: 1}},
		})
	}
	return listing
}

func (r *stubRouter) setModels(modelIDs ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelIDs = modelIDs
}

func (r *stubRouter) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// modelsTestGateway also serves as the model catalog's clock.
type modelsTestGateway struct {
	engine *echo.Echo
	now    time.Time
}

func (g *modelsTestGateway) Now() time.Time { return g.now }

func (g *modelsTestGateway) NowNano() int64 { return g.now.UnixNano() }

func (g *modelsTestGateway) Tick(time.Duration) <-chan time.Time { return nil }

func newModelsTestGateway(t *testing.T, routerURL string, cacheTTL time.Duration) *modelsTestGateway {
	t.Helper()
	cfg := config.Default()
	cfg.Stargate.URL = routerURL
	cfg.Stargate.ListingCacheTTL = cacheTTL

	stargate, err := provider.NewStargateProvider(cfg.Stargate)
	require.NoError(t, err)

	gateway := &modelsTestGateway{engine: echo.New(), now: time.Unix(modelsTestStart, 0)}
	handlers := NewHandlers(cfg, stargate, ratelimit.AllowAll)
	handlers.modelCatalog = newModelCatalog(stargate, cacheTTL, gateway)
	gateway.engine.Use(NewContextMiddleware(cfg))
	RegisterRoutes(gateway.engine, handlers)
	return gateway
}

func (g *modelsTestGateway) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	g.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func (g *modelsTestGateway) getWithContext(
	t *testing.T,
	ctx context.Context,
	target string,
) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	g.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))
	return rec
}

func (g *modelsTestGateway) advance(d time.Duration) {
	g.now = g.now.Add(d)
}

func TestListModels_RouterListing_ReturnsSortedOpenAIList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		modelIDs []string
		wantBody string
	}{
		{
			name:     "sorted by id",
			modelIDs: []string{"meta/llama-3.1-8b-instruct", "deepseek-r1"},
			wantBody: `{"object":"list","data":[
				{"id":"deepseek-r1","object":"model","created":1759741923,"owned_by":"inference-endpoints"},
				{"id":"meta/llama-3.1-8b-instruct","object":"model","created":1759741923,"owned_by":"inference-endpoints"}
			]}`,
		},
		{
			name:     "empty listing",
			modelIDs: nil,
			wantBody: `{"object":"list","data":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router := newStubRouter(t, tc.modelIDs...)
			gateway := newModelsTestGateway(t, router.server.URL, time.Minute)

			rec := gateway.get(t, "/v1/models")

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Header().Get(echo.HeaderContentType), echo.MIMEApplicationJSON)
			require.JSONEq(t, tc.wantBody, rec.Body.String())
		})
	}
}

func TestRetrieveModel_ModelID_ReturnsModelOrNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "id with slash",
			target:     "/v1/models/meta/llama-3.1-8b-instruct",
			wantStatus: http.StatusOK,
			wantBody: `{"id":"meta/llama-3.1-8b-instruct","object":"model",` +
				`"created":1759741923,"owned_by":"inference-endpoints"}`,
		},
		{
			name:       "id with escaped slash",
			target:     "/v1/models/meta%2Fllama-3.1-8b-instruct",
			wantStatus: http.StatusOK,
			wantBody: `{"id":"meta/llama-3.1-8b-instruct","object":"model",` +
				`"created":1759741923,"owned_by":"inference-endpoints"}`,
		},
		{
			name:       "id without slash",
			target:     "/v1/models/deepseek-r1",
			wantStatus: http.StatusOK,
			wantBody:   `{"id":"deepseek-r1","object":"model","created":1759741923,"owned_by":"inference-endpoints"}`,
		},
		{
			name:       "id with literal percent",
			target:     "/v1/models/org/model%25v2",
			wantStatus: http.StatusOK,
			wantBody:   `{"id":"org/model%v2","object":"model","created":1759741923,"owned_by":"inference-endpoints"}`,
		},
		{
			name:       "unlisted model",
			target:     "/v1/models/meta/unknown",
			wantStatus: http.StatusNotFound,
			wantBody: `{"error":{"code":"model_not_found","message":"The model 'meta/unknown' does not exist",` +
				`"param":"model","type":"invalid_request_error"}}`,
		},
		{
			name:       "prefix of a listed model",
			target:     "/v1/models/meta",
			wantStatus: http.StatusNotFound,
			wantBody: `{"error":{"code":"model_not_found","message":"The model 'meta' does not exist",` +
				`"param":"model","type":"invalid_request_error"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router := newStubRouter(t, "meta/llama-3.1-8b-instruct", "deepseek-r1", "org/model%v2")
			gateway := newModelsTestGateway(t, router.server.URL, time.Minute)

			rec := gateway.get(t, tc.target)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			require.JSONEq(t, tc.wantBody, rec.Body.String())
		})
	}
}

// TestListModels_ListingFieldsDisagree_FollowsHealthyServers covers the
// router filling model_ids and models in two separate reads: a registration
// that changed between them appears in one field but not the other. The
// gateway follows models[], the field GET /v1/registry derives health from.
func TestListModels_ListingFieldsDisagree_FollowsHealthyServers(t *testing.T) {
	t.Parallel()
	router := newStubRouter(t)
	router.body = `{
		"model_ids": ["stale/became-unroutable"],
		"models": [
			{"model_id": "stale/became-unroutable", "clusters": [
				{"cluster_id": "cluster-a", "registered_servers": 1, "healthy_servers": 0}
			]},
			{"model_id": "fresh/became-routable", "clusters": [
				{"cluster_id": "cluster-a", "registered_servers": 1, "healthy_servers": 0},
				{"cluster_id": "cluster-b", "registered_servers": 1, "healthy_servers": 1}
			]}
		]
	}`
	gateway := newModelsTestGateway(t, router.server.URL, time.Minute)

	rec := gateway.get(t, "/v1/models")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"object":"list","data":[
		{"id":"fresh/became-routable","object":"model","created":1759741923,"owned_by":"inference-endpoints"}
	]}`, rec.Body.String())
	rec = gateway.get(t, "/v1/models/stale/became-unroutable")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestListModels_RefreshAddsModel_KeepsFirstSeenCreated(t *testing.T) {
	t.Parallel()
	router := newStubRouter(t, "model-a")
	gateway := newModelsTestGateway(t, router.server.URL, 0)

	rec := gateway.get(t, "/v1/models")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	gateway.advance(time.Minute)
	router.setModels("model-a", "model-b")
	rec = gateway.get(t, "/v1/models")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"object":"list","data":[
		{"id":"model-a","object":"model","created":1759741923,"owned_by":"inference-endpoints"},
		{"id":"model-b","object":"model","created":1759741983,"owned_by":"inference-endpoints"}
	]}`, rec.Body.String())
	require.Equal(t, 2, router.callCount())
}

func TestModelsEndpoints_CallsWithinCacheTTL_ShareOneRouterCall(t *testing.T) {
	t.Parallel()
	router := newStubRouter(t, "meta/llama-3.1-8b-instruct")
	gateway := newModelsTestGateway(t, router.server.URL, 3*time.Second)

	for _, target := range []string{"/v1/models", "/v1/models/meta/llama-3.1-8b-instruct", "/v1/models"} {
		rec := gateway.get(t, target)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", target, rec.Body.String())
		gateway.advance(time.Second)
	}
	require.Equal(t, 1, router.callCount(), "calls within the TTL")

	rec := gateway.get(t, "/v1/models")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 2, router.callCount(), "call after the TTL expired")
}

func TestModelsEndpoints_RouterFailsAfterCacheExpires_ReturnsBadGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		breakRouter func(router *stubRouter)
	}{
		{name: "5xx", breakRouter: func(router *stubRouter) { router.status = http.StatusServiceUnavailable }},
		{name: "dial error", breakRouter: func(router *stubRouter) { router.server.Close() }},
		{name: "timeout", breakRouter: func(router *stubRouter) { router.block = true }},
		{name: "malformed body", breakRouter: func(router *stubRouter) { router.body = "not json" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router := newStubRouter(t, "meta/llama-3.1-8b-instruct")
			gateway := newModelsTestGateway(t, router.server.URL, 3*time.Second)
			rec := gateway.get(t, "/v1/models")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			router.mu.Lock()
			tc.breakRouter(router)
			router.mu.Unlock()

			rec = gateway.get(t, "/v1/models/meta/llama-3.1-8b-instruct")
			require.Equal(t, http.StatusOK, rec.Code, "served from the fresh cache: %s", rec.Body.String())

			gateway.advance(3 * time.Second)
			for _, target := range []string{"/v1/models", "/v1/models/meta/llama-3.1-8b-instruct", "/v1/registry"} {
				rec := gateway.get(t, target)
				require.Equal(t, http.StatusBadGateway, rec.Code, "%s: %s", target, rec.Body.String())
				require.JSONEq(t, modelListingUnavailableBody, rec.Body.String(), target)
			}
		})
	}
}

func TestModelsEndpoints_FirstCallerCancelsDuringRefresh_RefreshStillCompletes(t *testing.T) {
	t.Parallel()
	router := newStubRouter(t, "meta/llama-3.1-8b-instruct")
	router.gate = make(chan struct{})
	router.received = make(chan struct{}, 1)
	releaseRouter := sync.OnceFunc(func() { close(router.gate) })
	t.Cleanup(releaseRouter)
	gateway := newModelsTestGateway(t, router.server.URL, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- gateway.getWithContext(t, ctx, "/v1/models") }()
	<-router.received
	cancel()

	select {
	case rec := <-first:
		t.Fatalf("refresh ended when its first caller cancelled: %d %s", rec.Code, rec.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	releaseRouter()
	<-first

	rec := gateway.get(t, "/v1/models/meta/llama-3.1-8b-instruct")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, router.callCount(), "the refreshed listing was not cached")
}
