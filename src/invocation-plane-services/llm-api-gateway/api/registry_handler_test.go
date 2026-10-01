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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// registryTestListing is a router listing response. qwen is registered but not
// routable; llama is routable from cluster-b only. The router does not sort.
const registryTestListing = `{
	"model_ids": ["meta/llama-3.1-8b-instruct"],
	"models": [
		{"model_id": "qwen/qwen3-8b", "clusters": [
			{"cluster_id": "cluster-a", "registered_servers": 1, "healthy_servers": 0}
		]},
		{"model_id": "meta/llama-3.1-8b-instruct", "clusters": [
			{"cluster_id": "cluster-b", "registered_servers": 2, "healthy_servers": 1},
			{"cluster_id": "cluster-a", "registered_servers": 1, "healthy_servers": 0}
		]}
	]
}`

const (
	registryTestLlama = `{"model": "meta/llama-3.1-8b-instruct", "health": "Healthy", "clusters": [
		{"clusterId": "cluster-a", "registeredServers": 1, "healthyServers": 0},
		{"clusterId": "cluster-b", "registeredServers": 2, "healthyServers": 1}
	]}`
	registryTestQwen = `{"model": "qwen/qwen3-8b", "health": "Unhealthy", "clusters": [
		{"clusterId": "cluster-a", "registeredServers": 1, "healthyServers": 0}
	]}`
)

func newRegistryTestRouter(t *testing.T, listing string) *stubRouter {
	t.Helper()
	router := newStubRouter(t)
	router.body = listing
	return router
}

func TestRegistry_RouterListing_ReturnsModelsWithHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		listing    string
		target     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "all models sorted, clusters sorted",
			listing:    registryTestListing,
			target:     "/v1/registry",
			wantStatus: http.StatusOK,
			wantBody: `{"generatedAt": "2025-10-06T09:12:03Z", "models": [` +
				registryTestLlama + `,` + registryTestQwen + `]}`,
		},
		{
			name:       "model filter",
			listing:    registryTestListing,
			target:     "/v1/registry?model=qwen/qwen3-8b",
			wantStatus: http.StatusOK,
			wantBody:   `{"generatedAt": "2025-10-06T09:12:03Z", "models": [` + registryTestQwen + `]}`,
		},
		{
			name:       "escaped model filter",
			listing:    registryTestListing,
			target:     "/v1/registry?model=meta%2Fllama-3.1-8b-instruct",
			wantStatus: http.StatusOK,
			wantBody:   `{"generatedAt": "2025-10-06T09:12:03Z", "models": [` + registryTestLlama + `]}`,
		},
		{
			name:       "unlisted model filter",
			listing:    registryTestListing,
			target:     "/v1/registry?model=meta/unknown",
			wantStatus: http.StatusOK,
			wantBody:   `{"generatedAt": "2025-10-06T09:12:03Z", "models": []}`,
		},
		{
			name:       "empty listing",
			listing:    `{"model_ids": [], "models": []}`,
			target:     "/v1/registry",
			wantStatus: http.StatusOK,
			wantBody:   `{"generatedAt": "2025-10-06T09:12:03Z", "models": []}`,
		},
		{
			name:       "router without models field",
			listing:    `{"model_ids": ["meta/llama-3.1-8b-instruct"]}`,
			target:     "/v1/registry",
			wantStatus: http.StatusOK,
			wantBody:   `{"generatedAt": "2025-10-06T09:12:03Z", "models": []}`,
		},
		{
			name:       "empty model filter",
			listing:    registryTestListing,
			target:     "/v1/registry?model=",
			wantStatus: http.StatusBadRequest,
			wantBody: `{"error": {"code": "invalid_model_filter", "message": "The model filter must not be empty",` +
				`"param": "model", "type": "invalid_request_error"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router := newRegistryTestRouter(t, tc.listing)
			gateway := newModelsTestGateway(t, router.server.URL, time.Minute)

			rec := gateway.get(t, tc.target)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			require.JSONEq(t, tc.wantBody, rec.Body.String())
		})
	}
}

func TestRegistry_CallsWithinCacheTTL_ShareOneRefresh(t *testing.T) {
	t.Parallel()
	router := newRegistryTestRouter(t, registryTestListing)
	gateway := newModelsTestGateway(t, router.server.URL, 3*time.Second)

	first := registryGeneratedAt(t, gateway)
	gateway.advance(time.Second)
	rec := gateway.get(t, "/v1/models")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	gateway.advance(time.Second)
	second := registryGeneratedAt(t, gateway)

	require.Equal(t, "2025-10-06T09:12:03Z", first)
	require.Equal(t, first, second, "responses from the same cached refresh")
	require.Equal(t, 1, router.callCount(), "registry and models share one refresh")

	gateway.advance(time.Second)
	require.Equal(t, "2025-10-06T09:12:06Z", registryGeneratedAt(t, gateway), "after the TTL expired")
	require.Equal(t, 2, router.callCount())
}

func TestRegistry_SameListing_HealthMatchesModelsMembership(t *testing.T) {
	t.Parallel()
	router := newRegistryTestRouter(t, registryTestListing)
	gateway := newModelsTestGateway(t, router.server.URL, time.Minute)

	var models openAIModelList
	rec := gateway.get(t, "/v1/models")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &models))
	var registry registryResponse
	rec = gateway.get(t, "/v1/registry")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &registry))

	var listed, healthy []string
	for _, model := range models.Data {
		listed = append(listed, model.ID)
	}
	for _, model := range registry.Models {
		if model.Health == registryHealthy {
			healthy = append(healthy, model.Model)
		}
	}
	require.Equal(t, []string{"meta/llama-3.1-8b-instruct"}, healthy)
	require.Equal(t, listed, healthy)
}

func registryGeneratedAt(t *testing.T, gateway *modelsTestGateway) string {
	t.Helper()
	rec := gateway.get(t, "/v1/registry")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var registry registryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &registry))
	return registry.GeneratedAt
}
