/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	config "ai-api-gateway-service/gateway_config"
	"ai-api-gateway-service/middleware"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	// Config IDs may differ in case from the lowercase IDs the NVCF API returns.
	allowedPrivateFunctionID = "6A1F0C2E-0000-4000-8000-000000000001"
	deniedPrivateFunctionID  = "6a1f0c2e-0000-4000-8000-000000000002"
	expiredPrivateFunctionID = "6a1f0c2e-0000-4000-8000-000000000003"
	stagingFunctionID        = "6a1f0c2e-0000-4000-8000-000000000004"
	publicFunctionID         = "6a1f0c2e-0000-4000-8000-000000000005"
	unmappedFunctionID       = "6a1f0c2e-0000-4000-8000-000000000006"
)

// fakeFunctionIDs serves the NVCF function ID list for fixed test keys. When
// gate is set, each response waits until it is closed. While failing is set,
// every response is a server error.
type fakeFunctionIDs struct {
	t       *testing.T
	calls   atomic.Int32
	gate    chan struct{}
	failing atomic.Bool
}

func (f *fakeFunctionIDs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	if r.URL.Path != privateModelLookupPath || r.URL.Query().Get("visibility") != "authorized,private" {
		f.t.Errorf("unexpected NVCF API request %s", r.URL)
	}
	if f.failing.Load() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	switch r.Header.Get("Authorization") {
	case "Bearer good":
		_ = json.NewEncoder(w).Encode(map[string][]string{"functionIds": {
			strings.ToLower(allowedPrivateFunctionID),
			expiredPrivateFunctionID,
			stagingFunctionID,
			publicFunctionID,
			unmappedFunctionID,
		}})
	case "Bearer other":
		_ = json.NewEncoder(w).Encode(map[string][]string{"functionIds": {}})
	case "Bearer bad":
		w.WriteHeader(http.StatusUnauthorized)
	case "Bearer noscope":
		w.WriteHeader(http.StatusForbidden)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func newFakeFunctionIDs(t *testing.T) (*fakeFunctionIDs, string) {
	t.Helper()
	nvcf := &fakeFunctionIDs{t: t}
	server := httptest.NewServer(nvcf)
	t.Cleanup(server.Close)
	return nvcf, server.URL
}

// privateModelsMux serves one public model, private models the test keys may
// or may not access, an expired private model, and a staging alias.
func privateModelsMux(t *testing.T, controlPlaneEndpoint string) http.Handler {
	t.Helper()
	entry := func(name, functionID string) config.ModelFunctionDetails {
		return config.ModelFunctionDetails{ModelName: name, FunctionID: functionID}
	}
	expired := entry("private/team/expired", expiredPrivateFunctionID)
	expired.EOL = time.Now().Add(-time.Hour)
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"public":  entry("acme/public", publicFunctionID),
		"allowed": entry("private/team/allowed", allowedPrivateFunctionID),
		"denied":  entry("private/team/denied", deniedPrivateFunctionID),
		"expired": expired,
		"staging": entry("stg/acme/model", stagingFunctionID),
	}
	mappings.AnthropicMessages = map[string]config.ModelFunctionDetails{
		"allowed": entry("private/team/allowed", allowedPrivateFunctionID),
	}
	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              "http://nvcf.invalid",
		NvcfControlPlaneEndpoint:     controlPlaneEndpoint,
		PrivateModelNameRegexPattern: "^(stg/|private/)",
	})
	require.NoError(t, err)
	return mux
}

// sharedAccess returns the process-wide access cache for a fake endpoint.
func sharedAccess(t *testing.T, endpoint string) *privateModelAccess {
	t.Helper()
	access, ok := privateModelAccessByEndpoint.Load(endpoint)
	require.True(t, ok)
	return access.(*privateModelAccess)
}

func listModels(t *testing.T, mux http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = openAIHost
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func keyHeaders(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func listedModelIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var list ModelListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list), rec.Body.String())
	ids := make([]string, 0, len(list.Data))
	for _, model := range list.Data {
		ids = append(ids, model.Id)
	}
	return ids
}

func TestListModelsIncludesAuthorizedPrivateModels(t *testing.T) {
	nvcf, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)

	rec := listModels(t, mux, "/v1/models", keyHeaders("good"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `{"object":"list","data":[`+
		`{"id":"acme/public","object":"model","created":735790403,"owned_by":"acme"},`+
		`{"supported_endpoints":["/v1/chat/completions","/v1/messages"],"id":"private/team/allowed","object":"model","created":735790403,"owned_by":"private"},`+
		`{"id":"stg/acme/model","object":"model","created":735790403,"owned_by":"stg"}`+
		"]}\n", rec.Body.String())
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, privateModelsAuthorized, rec.Header().Get(PrivateModelsHeader))
	require.Contains(t, rec.Header().Values("Vary"), "Authorization")
	require.EqualValues(t, 1, nvcf.calls.Load())

	rec = listModels(t, mux, "/v1/models", keyHeaders("other"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"acme/public"}, listedModelIDs(t, rec))
	require.Equal(t, privateModelsAuthorized, rec.Header().Get(PrivateModelsHeader))
}

func TestListModelsWithoutBearerKeySkipsLookup(t *testing.T) {
	nvcf, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)

	for _, headers := range []map[string]string{
		nil,
		{"Authorization": "Basic Zm9vOmJhcg=="},
		{"Authorization": "Bearer "},
	} {
		rec := listModels(t, mux, "/v1/models", headers)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, []string{"acme/public"}, listedModelIDs(t, rec))
		require.Empty(t, rec.Header().Get("Cache-Control"))
		require.Empty(t, rec.Header().Get(PrivateModelsHeader))
		require.Contains(t, rec.Header().Values("Vary"), "Authorization")
	}
	require.Zero(t, nvcf.calls.Load())
}

// A lookup never fails the request: the caller gets the public list and a
// header that says why private models are missing.
func TestListModelsFallsBackToPublicList(t *testing.T) {
	for _, tc := range []struct {
		token  string
		result string
	}{
		{"bad", privateModelsUnauthenticated},
		{"noscope", privateModelsForbidden},
		{"broken", privateModelsUnavailable},
	} {
		t.Run(tc.token, func(t *testing.T) {
			nvcf, endpoint := newFakeFunctionIDs(t)
			rec := listModels(t, privateModelsMux(t, endpoint), "/v1/models", keyHeaders(tc.token))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []string{"acme/public"}, listedModelIDs(t, rec))
			require.Equal(t, tc.result, rec.Header().Get(PrivateModelsHeader))
			require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
			require.EqualValues(t, 1, nvcf.calls.Load())
		})
	}
}

func TestListModelsServesLastGoodResultWhileLookupFails(t *testing.T) {
	nvcf, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)
	access := sharedAccess(t, endpoint)
	now := time.Now()
	access.now = func() time.Time { return now }
	list := func() *httptest.ResponseRecorder {
		rec := listModels(t, mux, "/v1/models", keyHeaders("good"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}
	withPrivate := []string{"acme/public", "private/team/allowed", "stg/acme/model"}

	require.Equal(t, withPrivate, listedModelIDs(t, list()))
	nvcf.failing.Store(true)

	// Past the cache TTL, a failed lookup serves the last good result.
	now = now.Add(privateModelLookupCacheTTL)
	rec := list()
	require.Equal(t, withPrivate, listedModelIDs(t, rec))
	require.Equal(t, privateModelsAuthorized, rec.Header().Get(PrivateModelsHeader))
	require.EqualValues(t, 2, nvcf.calls.Load())

	// Past the stale window, the caller gets the public list.
	now = now.Add(privateModelLookupStaleTTL)
	rec = list()
	require.Equal(t, []string{"acme/public"}, listedModelIDs(t, rec))
	require.Equal(t, privateModelsUnavailable, rec.Header().Get(PrivateModelsHeader))

	nvcf.failing.Store(false)
	require.Equal(t, withPrivate, listedModelIDs(t, list()))
}

func TestListModelsWithoutControlPlaneEndpoint(t *testing.T) {
	rec := listModels(t, privateModelsMux(t, ""), "/v1/models", keyHeaders("good"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"acme/public"}, listedModelIDs(t, rec))
	require.Empty(t, rec.Header().Get("Cache-Control"))
	require.Empty(t, rec.Header().Get(PrivateModelsHeader))
}

func TestListModelsIncludesAuthorizedPrivateModelsForAnthropicClients(t *testing.T) {
	_, endpoint := newFakeFunctionIDs(t)
	headers := keyHeaders("good")
	headers["anthropic-version"] = "2023-06-01"

	rec := listModels(t, privateModelsMux(t, endpoint), "/v1/models", headers)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page anthropicModelPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Equal(t, []string{"private/team/allowed"}, pageIDs(page))
	require.Equal(t, privateModelsAuthorized, rec.Header().Get(PrivateModelsHeader))
}

func TestGetModelReturnsPrivateModelOnlyWithAccess(t *testing.T) {
	nvcf, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)

	rec := listModels(t, mux, "/v1/models/private/team/allowed", keyHeaders("good"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `{"supported_endpoints":["/v1/chat/completions","/v1/messages"],"id":"private/team/allowed","object":"model","created":735790403,"owned_by":"private"}`+"\n", rec.Body.String())
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))

	// Expiry follows the public-model rules.
	rec = listModels(t, mux, "/v1/models/private/team/expired", keyHeaders("good"))
	require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())

	// Public names need no lookup.
	rec = listModels(t, mux, "/v1/models/acme/public", keyHeaders("good"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get(PrivateModelsHeader))
	require.EqualValues(t, 1, nvcf.calls.Load())
}

// Without access, a private name gets exactly what an unknown name gets.
func TestGetModelHidesPrivateModelsWithoutAccess(t *testing.T) {
	_, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)
	for _, headers := range []map[string]string{
		keyHeaders("good"), keyHeaders("bad"), keyHeaders("noscope"), keyHeaders("broken"), nil,
	} {
		get := func(name string) http.Header {
			rec := listModels(t, mux, "/v1/models/"+name, headers)
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Equal(t, "Model does not exist by the name of "+name+"\n", rec.Body.String())
			header := rec.Header().Clone()
			header.Del(middleware.RequestIDHeader)
			return header
		}
		unknown := get("private/team/unknown")
		require.Equal(t, unknown, get("private/team/denied"), headers)
		if headers == nil {
			require.Equal(t, unknown, get("private/team/allowed"))
		}
	}
}

func TestGetModelChecksPrivateAccessForAnthropicClients(t *testing.T) {
	_, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)
	headers := func(token string) map[string]string {
		h := keyHeaders(token)
		h["anthropic-version"] = "2023-06-01"
		return h
	}

	rec := listModels(t, mux, "/v1/models/private/team/allowed", headers("good"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var model AnthropicModelInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &model))
	require.Equal(t, "private/team/allowed", model.Id)

	rec = listModels(t, mux, "/v1/models/private/team/allowed", headers("other"))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	requireAnthropicError(t, rec, "not_found_error")
}

func TestPrivateModelLookupCachesResultsPerKey(t *testing.T) {
	nvcf, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)
	list := func(token string) string {
		return listModels(t, mux, "/v1/models", keyHeaders(token)).Header().Get(PrivateModelsHeader)
	}

	require.Equal(t, privateModelsAuthorized, list("good"))
	require.Equal(t, privateModelsAuthorized, list("good"))
	require.EqualValues(t, 1, nvcf.calls.Load())

	require.Equal(t, privateModelsAuthorized, list("other"))
	require.EqualValues(t, 2, nvcf.calls.Load())

	require.Equal(t, privateModelsUnauthenticated, list("bad"))
	require.Equal(t, privateModelsUnauthenticated, list("bad"))
	require.EqualValues(t, 3, nvcf.calls.Load())

	// Failures are retried instead of cached.
	require.Equal(t, privateModelsUnavailable, list("broken"))
	require.Equal(t, privateModelsUnavailable, list("broken"))
	require.EqualValues(t, 5, nvcf.calls.Load())
}

func TestPrivateModelLookupSharesConcurrentRequests(t *testing.T) {
	nvcf := &fakeFunctionIDs{t: t, gate: make(chan struct{})}
	server := httptest.NewServer(nvcf)
	t.Cleanup(server.Close)
	mux := privateModelsMux(t, server.URL)

	results := make([]string, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = listModels(t, mux, "/v1/models", keyHeaders("good")).Header().Get(PrivateModelsHeader)
		}()
	}
	require.Eventually(t, func() bool { return nvcf.calls.Load() == 1 }, time.Second, time.Millisecond)
	require.Never(t, func() bool { return nvcf.calls.Load() > 1 }, 100*time.Millisecond, 5*time.Millisecond)
	close(nvcf.gate)
	wg.Wait()

	for _, result := range results {
		require.Equal(t, privateModelsAuthorized, result)
	}
	require.EqualValues(t, 1, nvcf.calls.Load())
}

// A mapping reload builds a new router but keeps the cached lookups.
func TestPrivateModelLookupCacheSurvivesMappingReload(t *testing.T) {
	nvcf, endpoint := newFakeFunctionIDs(t)
	before := privateModelsMux(t, endpoint)
	require.Equal(t, privateModelsAuthorized, listModels(t, before, "/v1/models", keyHeaders("good")).Header().Get(PrivateModelsHeader))

	after := privateModelsMux(t, endpoint)
	rec := listModels(t, after, "/v1/models", keyHeaders("good"))
	require.Equal(t, []string{"acme/public", "private/team/allowed", "stg/acme/model"}, listedModelIDs(t, rec))
	require.EqualValues(t, 1, nvcf.calls.Load())
}

func TestPrivateModelAccessCacheExpiresAndStaysBounded(t *testing.T) {
	nvcf, endpoint := newFakeFunctionIDs(t)
	allowed := strings.ToLower(allowedPrivateFunctionID)
	access, err := newPrivateModelAccess(endpoint, http.DefaultTransport)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	access.now = func() time.Time { return now }
	lookup := func() (map[string]struct{}, string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer good")
		return access.authorizedFunctionIDs(req, map[string][]ModelInfo{allowed: nil})
	}

	// Only function IDs behind private aliases are kept.
	ids, result := lookup()
	require.Equal(t, privateModelsAuthorized, result)
	require.Equal(t, map[string]struct{}{allowed: {}}, ids)
	lookup()
	require.EqualValues(t, 1, nvcf.calls.Load())

	now = now.Add(privateModelLookupCacheTTL)
	lookup()
	require.EqualValues(t, 2, nvcf.calls.Load())

	// Keys without access are evicted before the last good results of others.
	access.store("keep", privateModelAccessEntry{result: privateModelsAuthorized, fetched: now})
	for i := range privateModelLookupCacheSize + 10 {
		access.store(fmt.Sprint(i), privateModelAccessEntry{result: privateModelsUnauthenticated, fetched: now})
	}
	require.LessOrEqual(t, len(access.cache), privateModelLookupCacheSize)
	_, ok := access.cached("keep", privateModelLookupStaleTTL)
	require.True(t, ok)
}

func TestNewPrivateModelAccessRejectsInvalidEndpoint(t *testing.T) {
	for _, endpoint := range []string{"nvcf.example.com", "http://", "://bad"} {
		_, err := newPrivateModelAccess(endpoint, http.DefaultTransport)
		require.Error(t, err, endpoint)
	}
}

type privateModelLookupLabels struct {
	result string
	cached bool
}

func TestPrivateModelLookupMetricsInitializeAndCount(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	counter, err := newPrivateModelLookupsCounter(provider.Meter(shadowMetricsScope))
	require.NoError(t, err)
	previousCounter := privateModelLookupsCounter
	privateModelLookupsCounter = counter
	t.Cleanup(func() {
		privateModelLookupsCounter = previousCounter
	})
	initializePrivateModelLookupMetrics()

	_, endpoint := newFakeFunctionIDs(t)
	mux := privateModelsMux(t, endpoint)
	for _, token := range []string{"good", "good", "noscope"} {
		listModels(t, mux, "/v1/models", keyHeaders(token))
	}

	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &data))
	counts := map[privateModelLookupLabels]int64{}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != privateModelLookupsMetricName {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, point := range sum.DataPoints {
				result, _ := point.Attributes.Value(privateModelLookupResultLabel)
				cached, _ := point.Attributes.Value(privateModelLookupCachedLabel)
				counts[privateModelLookupLabels{result.AsString(), cached.AsBool()}] = point.Value
			}
		}
	}
	require.Equal(t, map[privateModelLookupLabels]int64{
		{privateModelsAuthorized, false}:      1,
		{privateModelsAuthorized, true}:       1,
		{privateModelsForbidden, false}:       1,
		{privateModelsForbidden, true}:        0,
		{privateModelsUnauthenticated, false}: 0,
		{privateModelsUnauthenticated, true}:  0,
		{privateModelsUnavailable, false}:     0,
		{privateModelsUnavailable, true}:      0,
	}, counts)
}
