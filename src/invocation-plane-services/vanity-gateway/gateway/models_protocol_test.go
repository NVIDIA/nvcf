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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	config "ai-api-gateway-service/gateway_config"

	"github.com/stretchr/testify/require"
)

// modelsDiscoveryMux serves a shared host with OpenAI-only, Messages-only,
// shared, private and expired models.
func modelsDiscoveryMux(t *testing.T) http.Handler {
	t.Helper()
	entry := func(name string) config.ModelFunctionDetails {
		return config.ModelFunctionDetails{ModelName: name, FunctionID: "func-" + name, FunctionType: config.FunctionTypeLLM}
	}
	expired := entry("anthropic/expired")
	expired.EOL = time.Now().Add(-time.Hour)
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"chat":   entry("openai-only/chat"),
		"shared": entry("shared/both"),
	}
	mappings.AnthropicMessages = map[string]config.ModelFunctionDetails{
		"a":       entry("anthropic/a"),
		"b":       entry("anthropic/b"),
		"c":       entry("anthropic/c"),
		"shared":  entry("shared/both"),
		"private": entry("private/hidden"),
		"expired": expired,
	}
	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              "http://nvcf.invalid",
		LLMGatewayEndpoint:           "http://llm.invalid",
		PrivateModelNameRegexPattern: "^private/",
	})
	require.NoError(t, err)
	return mux
}

func getModels(t *testing.T, mux http.Handler, target string, anthropic bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = openAIHost
	if anthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type anthropicModelPage struct {
	Data []struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
	} `json:"data"`
	HasMore bool    `json:"has_more"`
	FirstID *string `json:"first_id"`
	LastID  *string `json:"last_id"`
}

func ptr(s string) *string { return &s }

func pageIDs(page anthropicModelPage) []string {
	ids := make([]string, 0, len(page.Data))
	for _, m := range page.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestListModelsWithoutAnthropicVersionIsUnchanged(t *testing.T) {
	rec := getModels(t, modelsDiscoveryMux(t), "/v1/models", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, `{"object":"list","data":[`+
		`{"supported_endpoints":["/v1/messages"],"id":"anthropic/a","object":"model","created":735790403,"owned_by":"anthropic"},`+
		`{"supported_endpoints":["/v1/messages"],"id":"anthropic/b","object":"model","created":735790403,"owned_by":"anthropic"},`+
		`{"supported_endpoints":["/v1/messages"],"id":"anthropic/c","object":"model","created":735790403,"owned_by":"anthropic"},`+
		`{"id":"openai-only/chat","object":"model","created":735790403,"owned_by":"openai-only"},`+
		`{"supported_endpoints":["/v1/chat/completions","/v1/messages"],"id":"shared/both","object":"model","created":735790403,"owned_by":"shared"}`+
		"]}\n", rec.Body.String())
}

func TestListModelsForAnthropicClients(t *testing.T) {
	for _, tc := range []struct {
		name            string
		target          string
		ids             []string
		hasMore         bool
		firstID, lastID *string
	}{
		{"lists only Messages models", "/v1/models", []string{"anthropic/a", "anthropic/b", "anthropic/c", "shared/both"}, false, ptr("anthropic/a"), ptr("shared/both")},
		{"first page", "/v1/models?limit=2", []string{"anthropic/a", "anthropic/b"}, true, ptr("anthropic/a"), ptr("anthropic/b")},
		{"next page after last_id", "/v1/models?limit=2&after_id=anthropic/b", []string{"anthropic/c", "shared/both"}, false, ptr("anthropic/c"), ptr("shared/both")},
		{"previous page before first_id", "/v1/models?limit=2&before_id=shared/both", []string{"anthropic/b", "anthropic/c"}, true, ptr("anthropic/b"), ptr("anthropic/c")},
		{"first page reached going back", "/v1/models?limit=2&before_id=anthropic/c", []string{"anthropic/a", "anthropic/b"}, false, ptr("anthropic/a"), ptr("anthropic/b")},
		{"cursor not in the list keeps its position", "/v1/models?after_id=anthropic/bb", []string{"anthropic/c", "shared/both"}, false, ptr("anthropic/c"), ptr("shared/both")},
		{"empty page", "/v1/models?after_id=shared/both", []string{}, false, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getModels(t, modelsDiscoveryMux(t), tc.target, true)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			require.Equal(t, rec.Header().Get("X-Request-Id"), rec.Header().Get("Request-Id"))
			require.NotEmpty(t, rec.Header().Get("Request-Id"))
			var page anthropicModelPage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
			require.Equal(t, tc.ids, pageIDs(page))
			require.Equal(t, tc.hasMore, page.HasMore)
			require.Equal(t, tc.firstID, page.FirstID)
			require.Equal(t, tc.lastID, page.LastID)
			for _, m := range page.Data {
				require.Equal(t, "model", m.Type)
				require.Equal(t, m.ID, m.DisplayName)
				require.Equal(t, "1993-04-26T02:13:23Z", m.CreatedAt)
			}
		})
	}
}

func TestListModelsForAnthropicClientsRejectsInvalidParameters(t *testing.T) {
	for _, target := range []string{
		"/v1/models?limit=0",
		"/v1/models?limit=1001",
		"/v1/models?limit=two",
		"/v1/models?after_id=anthropic/a&before_id=anthropic/c",
	} {
		t.Run(target, func(t *testing.T) {
			rec := getModels(t, modelsDiscoveryMux(t), target, true)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			requireAnthropicError(t, rec, "invalid_request_error")
		})
	}
}

func TestGetModelForAnthropicClients(t *testing.T) {
	mux := modelsDiscoveryMux(t)

	rec := getModels(t, mux, "/v1/models/anthropic/a", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"type":"model","id":"anthropic/a","display_name":"anthropic/a","created_at":"1993-04-26T02:13:23Z"}`, rec.Body.String())
	require.Equal(t, rec.Header().Get("X-Request-Id"), rec.Header().Get("Request-Id"))

	for _, tc := range []struct {
		target string
		status int
	}{
		{"/v1/models/openai-only/chat", http.StatusNotFound},
		{"/v1/models/private/hidden", http.StatusNotFound},
		{"/v1/models/unknown", http.StatusNotFound},
		{"/v1/models/anthropic/expired", http.StatusGone},
	} {
		t.Run(tc.target, func(t *testing.T) {
			rec := getModels(t, mux, tc.target, true)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			requireAnthropicError(t, rec, "not_found_error")
		})
	}
}

func TestGetModelWithoutAnthropicVersionIsUnchanged(t *testing.T) {
	mux := modelsDiscoveryMux(t)
	rec := getModels(t, mux, "/v1/models/openai-only/chat", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, `{"id":"openai-only/chat","object":"model","created":735790403,"owned_by":"openai-only"}`+"\n", rec.Body.String())
	require.Empty(t, rec.Header().Get("Request-Id"))

	rec = getModels(t, mux, "/v1/models/unknown", false)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "Model does not exist by the name of unknown\n", rec.Body.String())
}

func requireAnthropicError(t *testing.T, rec *httptest.ResponseRecorder, kind string) {
	t.Helper()
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), rec.Body.String())
	require.Equal(t, "error", payload.Type)
	require.Equal(t, kind, payload.Error.Type)
	require.NotEmpty(t, payload.Error.Message)
}

func TestGetModelForAnthropicClientsReportsOfflineModel(t *testing.T) {
	offline := llmModelEntry()
	offline.OfflineMessage = "back soon"
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.AnthropicMessages = map[string]config.ModelFunctionDetails{"offline": offline}
	rec := getModels(t, openAIMux(t, mappings, "http://nvcf.invalid", "http://llm.invalid"), "/v1/models/"+publicModel, true)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, "10800", rec.Header().Get("Retry-After"))
	requireAnthropicError(t, rec, "api_error")
}

// Expiry for Anthropic clients follows the Messages entry, as model lookup and
// /v1/messages do, even when the same model has expired in an OpenAI section.
func TestListModelsForAnthropicClientsUsesMessagesExpiry(t *testing.T) {
	entry := func(name string, eol time.Time) config.ModelFunctionDetails {
		return config.ModelFunctionDetails{ModelName: name, FunctionID: "func-" + name, FunctionType: config.FunctionTypeLLM, EOL: eol}
	}
	past := time.Now().Add(-time.Hour)
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"openai-expired":   entry("shared/openai-expired", past),
		"messages-expired": entry("shared/messages-expired", time.Time{}),
	}
	mappings.AnthropicMessages = map[string]config.ModelFunctionDetails{
		"openai-expired":   entry("shared/openai-expired", time.Time{}),
		"messages-expired": entry("shared/messages-expired", past),
	}
	mux, err := buildChiMux(mappings, Config{NvcfApiEndpoint: "http://nvcf.invalid", LLMGatewayEndpoint: "http://llm.invalid", PrivateModelNameRegexPattern: "^private/"})
	require.NoError(t, err)

	rec := getModels(t, mux, "/v1/models", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page anthropicModelPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Equal(t, []string{"shared/openai-expired"}, pageIDs(page))

	require.Equal(t, http.StatusOK, getModels(t, mux, "/v1/models/shared/openai-expired", true).Code)
	require.Equal(t, http.StatusGone, getModels(t, mux, "/v1/models/shared/messages-expired", true).Code)
}
