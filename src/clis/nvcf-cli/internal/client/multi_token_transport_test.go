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

package client

import (
	"bytes"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestMultiTokenTransportIsAdminOperation tests the token selection logic
func TestMultiTokenTransportIsAdminOperation(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		expected bool
		reason   string
	}{
		// ADMIN OPERATIONS - Should use FUNCTION TOKEN (JWT)
		{
			name:     "Function creation",
			method:   "POST",
			path:     "/v2/nvcf/functions",
			expected: true,
			reason:   "Function creation requires register_function scope",
		},
		{
			name:     "Function deployment",
			method:   "POST",
			path:     "/v2/nvcf/deployments/functions/id/versions/vid",
			expected: true,
			reason:   "Deployment requires admin:deploy_function scope",
		},
		{
			name:     "Update GPU specification (PATCH)",
			method:   "PATCH",
			path:     "/v2/nvcf/deployments/dep-id/gpu-specifications/spec-id",
			expected: true,
			reason:   "Per-spec deployment update requires deploy_function scope (function token)",
		},
		{
			name:     "Function deletion",
			method:   "DELETE",
			path:     "/v2/nvcf/functions/id/versions/vid",
			expected: true,
			reason:   "Deletion requires admin:delete_function scope",
		},
		{
			name:     "Deployment deletion",
			method:   "DELETE",
			path:     "/v2/nvcf/deployments/functions/id/versions/vid",
			expected: true,
			reason:   "Deployment deletion requires admin scope",
		},
		{
			name:     "Function update",
			method:   "PUT",
			path:     "/v2/nvcf/metadata/functions/id/versions/vid",
			expected: true,
			reason:   "Update requires admin:update_function scope",
		},
		{
			name:     "Registry credentials",
			method:   "POST",
			path:     "/v2/nvcf/registry-credentials",
			expected: true,
			reason:   "Registry operations require admin scope",
		},
		{
			name:     "Recognized registries GET",
			method:   "GET",
			path:     "/v2/nvcf/recognized-registries",
			expected: true,
			reason:   "Registry operations require admin scope",
		},

		// ICMS cluster-management — must use JWT (cluster-management scope) so ICMS
		// routes via the admin-issuer-uri JWT path, not the API-key introspector.
		{
			name:     "ICMS register cluster",
			method:   "POST",
			path:     "/v1/accounts/nvcf-default/clusters",
			expected: true,
			reason:   "Register requires cluster-management JWT, not nvapi- API key",
		},
		{
			name:     "ICMS list clusters",
			method:   "GET",
			path:     "/v1/accounts/nvcf-default/clusters",
			expected: true,
			reason:   "List on /v1/accounts/{nca}/clusters is cluster-management scope",
		},
		{
			name:     "ICMS rotate JWKS",
			method:   "PUT",
			path:     "/v1/nvca/clusters/abc-123/jwks",
			expected: true,
			reason:   "JWKS rotation is cluster-management scope",
		},
		{
			name:     "ICMS delete cluster",
			method:   "DELETE",
			path:     "/v1/nvca/clusters/abc-123",
			expected: true,
			reason:   "Delete is cluster-management scope",
		},

		// USER OPERATIONS - Should use API KEY
		{
			name:     "Get function details",
			method:   "GET",
			path:     "/v2/nvcf/functions/id/versions/vid",
			expected: false,
			reason:   "Read operation can use API key",
		},
		{
			name:     "List function versions",
			method:   "GET",
			path:     "/v2/nvcf/functions/id/versions",
			expected: false,
			reason:   "List operation can use API key",
		},
		{
			name:     "Direct invocation path",
			method:   "POST",
			path:     "/echo",
			expected: false,
			reason:   "Direct invocation can use API key",
		},
		{
			name:     "Queue position",
			method:   "GET",
			path:     "/v2/nvcf/queues/request-id/position",
			expected: false,
			reason:   "Queue position can use API key",
		},
		{
			name:     "List cluster groups",
			method:   "GET",
			path:     "/v2/nvcf/clusterGroups",
			expected: false,
			reason:   "Cluster groups can use API key",
		},
	}

	transport := &multiTokenTransport{
		apiKey:        "test-api-key",
		functionToken: "test-jwt-token",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqURL, err := url.Parse("https://api.nvcf.nvidia.com" + tt.path)
			if err != nil {
				t.Fatalf("Failed to parse URL: %v", err)
			}
			req := &http.Request{
				Method: tt.method,
				URL:    reqURL,
			}

			result := transport.isAdminOperation(req)
			if result != tt.expected {
				t.Errorf("isAdminOperation(%s, %s) = %v, expected %v\nReason: %s",
					tt.method, tt.path, result, tt.expected, tt.reason)
			}
		})
	}
}

// TestGetExpectedUserScope tests the scope detection for user operations
func TestGetExpectedUserScope(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		expected string
	}{
		{
			name:     "List cluster groups",
			method:   "GET",
			path:     "/v2/nvcf/clusterGroups",
			expected: "list_cluster_groups",
		},
		{
			name:     "Function creation",
			method:   "POST",
			path:     "/v2/nvcf/functions",
			expected: "register_function",
		},
		{
			name:     "List functions",
			method:   "GET",
			path:     "/v2/nvcf/functions",
			expected: "no-auth (list_functions for details)",
		},
		{
			name:     "Get function details",
			method:   "GET",
			path:     "/v2/nvcf/functions/id/versions/vid",
			expected: "no-auth (list_functions for details)",
		},
		{
			name:     "Deploy function",
			method:   "POST",
			path:     "/v2/nvcf/deployments/functions/id/versions/vid",
			expected: "deploy_function",
		},
		{
			name:     "Delete function",
			method:   "DELETE",
			path:     "/v2/nvcf/functions/id/versions/vid",
			expected: "delete_function",
		},
		{
			name:     "Update metadata",
			method:   "PUT",
			path:     "/v2/nvcf/metadata/functions/id/versions/vid",
			expected: "update_function",
		},
		{
			name:     "Direct invocation path",
			method:   "POST",
			path:     "/echo",
			expected: "no-auth or unknown",
		},
		{
			name:     "Registry credentials",
			method:   "POST",
			path:     "/v2/nvcf/registry-credentials",
			expected: "manage_registry_credentials",
		},
		{
			name:     "Telemetries",
			method:   "GET",
			path:     "/v2/nvcf/telemetries",
			expected: "manage_telemetries",
		},
		{
			name:     "Authorizations",
			method:   "POST",
			path:     "/v2/nvcf/authorizations/functions/id",
			expected: "authorize_clients",
		},
		{
			name:     "Queue details",
			method:   "GET",
			path:     "/v2/nvcf/queues/request-id/position",
			expected: "no-auth",
		},
	}

	transport := &multiTokenTransport{
		apiKey:        "test-api-key",
		functionToken: "test-jwt-token",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqURL, err := url.Parse("https://api.nvcf.nvidia.com" + tt.path)
			if err != nil {
				t.Fatalf("Failed to parse URL: %v", err)
			}
			req := &http.Request{
				Method: tt.method,
				URL:    reqURL,
			}

			result := transport.getExpectedUserScope(req)
			if result != tt.expected {
				t.Errorf("getExpectedUserScope(%s, %s) = %q, expected %q",
					tt.method, tt.path, result, tt.expected)
			}
		})
	}
}

// mockRoundTripper captures the Authorization header for verification
type mockRoundTripper struct {
	capturedAuth string
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.capturedAuth = req.Header.Get("Authorization")
	return &http.Response{
		StatusCode: 200,
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

// TestTokenSelectionPriority tests the priority of token selection
func TestTokenSelectionPriority(t *testing.T) {
	tests := []struct {
		name          string
		apiKey        string
		functionToken string
		requestMethod string
		requestPath   string
		expectedToken string
		description   string
	}{
		{
			name:          "Admin operation with JWT - use JWT",
			apiKey:        "api-key",
			functionToken: "jwt-token",
			requestMethod: "POST",
			requestPath:   "/v2/nvcf/functions",
			expectedToken: "Bearer jwt-token",
			description:   "Admin operations should always use JWT when available",
		},
		{
			name:          "Direct invocation with both tokens - use API key",
			apiKey:        "api-key",
			functionToken: "jwt-token",
			requestMethod: "POST",
			requestPath:   "/echo",
			expectedToken: "Bearer api-key",
			description:   "User operations should prefer API key when available",
		},
		{
			name:          "User operation with only JWT - use JWT",
			apiKey:        "",
			functionToken: "jwt-token",
			requestMethod: "GET",
			requestPath:   "/v2/nvcf/functions/id/versions/vid",
			expectedToken: "Bearer jwt-token",
			description:   "Fallback to JWT when API key not available",
		},
		{
			name:          "Admin operation without JWT - use API key (will fail at API)",
			apiKey:        "api-key",
			functionToken: "",
			requestMethod: "POST",
			requestPath:   "/v2/nvcf/deployments/functions/id/versions/vid",
			expectedToken: "Bearer api-key",
			description:   "When JWT missing, API key used but operation should fail",
		},
		{
			name:          "Admin DELETE operation with JWT",
			apiKey:        "api-key",
			functionToken: "jwt-token",
			requestMethod: "DELETE",
			requestPath:   "/v2/nvcf/functions/id/versions/vid",
			expectedToken: "Bearer jwt-token",
			description:   "DELETE operations are admin and use JWT",
		},
		{
			name:          "Admin PUT operation with JWT",
			apiKey:        "api-key",
			functionToken: "jwt-token",
			requestMethod: "PUT",
			requestPath:   "/v2/nvcf/metadata/functions/id/versions/vid",
			expectedToken: "Bearer jwt-token",
			description:   "PUT operations are admin and use JWT",
		},
		{
			name:          "PATCH GPU specification with both tokens - use JWT",
			apiKey:        "api-key",
			functionToken: "jwt-token",
			requestMethod: "PATCH",
			requestPath:   "/v2/nvcf/deployments/dep-id/gpu-specifications/spec-id",
			expectedToken: "Bearer jwt-token",
			description:   "Per-spec deployment update is admin; must use function token even when API key present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock base transport
			mockBase := &mockRoundTripper{}

			// Create transport with test tokens
			transport := &multiTokenTransport{
				apiKey:        tt.apiKey,
				functionToken: tt.functionToken,
				base:          mockBase,
			}

			// Create test request
			reqURL, err := url.Parse("https://api.nvcf.nvidia.com" + tt.requestPath)
			if err != nil {
				t.Fatalf("Failed to parse URL: %v", err)
			}
			req := &http.Request{
				Method: tt.requestMethod,
				URL:    reqURL,
				Header: make(http.Header),
			}

			// Execute request through transport
			_, err = transport.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip failed: %v", err)
			}

			// Verify correct token was used
			if mockBase.capturedAuth != tt.expectedToken {
				t.Errorf("%s\nExpected Authorization: %q\nGot Authorization: %q",
					tt.description, tt.expectedToken, mockBase.capturedAuth)
			}
		})
	}
}

func TestMultiTokenTransportLogsClusterManagementForSISDelete(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	mockBase := &mockRoundTripper{}
	transport := &multiTokenTransport{
		apiKey:        "api-key",
		functionToken: "jwt-token",
		base:          mockBase,
	}
	reqURL, err := url.Parse("https://icms.example.test/v1/nvca/clusters/abc-123")
	if err != nil {
		t.Fatalf("Failed to parse URL: %v", err)
	}
	req := &http.Request{
		Method: http.MethodDelete,
		URL:    reqURL,
		Header: make(http.Header),
	}

	_, err = transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}

	if mockBase.capturedAuth != "Bearer jwt-token" {
		t.Fatalf("expected ICMS delete to use admin JWT, got %q", mockBase.capturedAuth)
	}
	got := logs.String()
	if !strings.Contains(got, "cluster-management") {
		t.Fatalf("expected cluster-management auth log, got %q", got)
	}
	if strings.Contains(got, "function operation") {
		t.Fatalf("ICMS delete must not be logged as a function operation: %q", got)
	}
}
