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

package apikeys

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureRequest spins up a test server, calls GenerateAPIKey with the given
// config and scopes, and returns the parsed request body and the headers the
// client sent.
func captureRequest(t *testing.T, cfg *Config, customScopes []string) (payload map[string]any, headers http.Header) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"value":"nvapi-test-key"}`))
	}))
	t.Cleanup(srv.Close)
	cfg.ServiceURL = srv.URL
	_, err := NewClient(cfg).GenerateAPIKey(context.Background(), "test", time.Now().Add(time.Hour), customScopes)
	require.NoError(t, err)
	return payload, headers
}

func policyFrom(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	auths, _ := payload["authorizations"].(map[string]any)
	policies, _ := auths["policies"].([]any)
	require.Len(t, policies, 1, "expected exactly one policy")
	return policies[0].(map[string]any)
}

func scopesFrom(policy map[string]any) []string {
	raw, _ := policy["scopes"].([]any)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		out = append(out, s.(string))
	}
	return out
}

func resourceTypesFrom(policy map[string]any) []string {
	raw, _ := policy["resources"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		rm := r.(map[string]any)
		out = append(out, rm["type"].(string))
	}
	return out
}

// --- Scope fallback chain ---

func TestGenerateAPIKey_UsesCustomScopesWhenProvided(t *testing.T) {
	payload, _ := captureRequest(t, &Config{
		ServiceID:     "svc-id",
		DefaultScopes: []string{"default_scope"},
	}, []string{"custom_scope_a", "custom_scope_b"})

	scopes := scopesFrom(policyFrom(t, payload))
	assert.Equal(t, []string{"custom_scope_a", "custom_scope_b"}, scopes)
}

func TestGenerateAPIKey_FallsBackToDefaultScopes(t *testing.T) {
	payload, _ := captureRequest(t, &Config{
		ServiceID:     "svc-id",
		DefaultScopes: []string{"launch_task", "list_tasks"},
	}, nil)

	scopes := scopesFrom(policyFrom(t, payload))
	assert.Equal(t, []string{"launch_task", "list_tasks"}, scopes)
}

func TestGenerateAPIKey_FallsBackToNVCFBuiltins(t *testing.T) {
	payload, _ := captureRequest(t, &Config{ServiceID: "svc-id"}, nil)

	scopes := scopesFrom(policyFrom(t, payload))
	assert.Contains(t, scopes, "invoke_function")
	assert.Contains(t, scopes, "list_functions")
}

// --- Product and resources ---

func TestGenerateAPIKey_NVCFProductAndResources(t *testing.T) {
	payload, _ := captureRequest(t, &Config{ServiceID: "nvidia-cloud-functions-ncp-service-id-aketm"}, nil)

	policy := policyFrom(t, payload)
	assert.Equal(t, "nv-cloud-functions", policy["product"])

	types := resourceTypesFrom(policy)
	assert.Contains(t, types, "account-functions")
	assert.Contains(t, types, "authorized-functions")
}

func TestGenerateAPIKey_NVCTProductAndResources(t *testing.T) {
	payload, _ := captureRequest(t, &Config{
		ServiceID:       "nvidia-cloud-tasks-ncp-service-id-nvcttasks",
		Product:         "nv-cloud-tasks",
		PolicyResources: []map[string]string{{"id": "*", "type": "account-tasks"}},
		DefaultScopes:   []string{"launch_task", "list_tasks"},
	}, nil)

	policy := policyFrom(t, payload)
	assert.Equal(t, "nv-cloud-tasks", policy["product"])
	assert.Equal(t, "nvidia-cloud-tasks-ncp-service-id-nvcttasks", policy["aud"])

	types := resourceTypesFrom(policy)
	assert.Equal(t, []string{"account-tasks"}, types)
}

// --- Request headers ---

func TestGenerateAPIKey_SetsIssuerHeaders(t *testing.T) {
	_, headers := captureRequest(t, &Config{
		ServiceID:     "my-service-id",
		IssuerService: "my-issuer",
		OwnerID:       "svc@my-service.local",
		JWTToken:      "my-jwt",
	}, nil)

	assert.Equal(t, "Bearer my-jwt", headers.Get("Authorization"))
	assert.Equal(t, "my-issuer", headers.Get("Key-Issuer-Service"))
	assert.Equal(t, "my-service-id", headers.Get("Key-Issuer-Id"))
	assert.Equal(t, "svc@my-service.local", headers.Get("Key-Owner-Id"))
}
