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
	"log"
	"net/http"
	"strings"
)

const deploymentPathSegment = "/deployments/"

// multiTokenTransport is an HTTP transport that uses different tokens for different operations
// Based on NVCF service analysis:
// - NVCF_TOKEN: Operations requiring standard scopes (create, deploy, delete, update functions, manage registry credentials)
// - NVCF_API_KEY: User operations requiring regular scopes (list, invoke, cluster groups)
type multiTokenTransport struct {
	apiKey        string // User operations: list, invoke (NVCF_API_KEY)
	functionToken string // Function operations: create, deploy, delete, update (NVCF_TOKEN)
	base          http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface with operation-specific tokens
func (m *multiTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Don't clone - modify the original request so the Authorization header is visible to debug transport
	// Ensure headers are initialized
	if req.Header == nil {
		req.Header = make(http.Header)
	}

	// Determine which token to use based on NVCF service scope requirements:
	// FUNCTION OPERATIONS (standard scopes) → NVCF_TOKEN or NVCF_API_KEY:
	//   - register_function (create functions)
	//   - deploy_function (deploy functions)
	//   - delete_function (delete functions)
	//   - update_function (update functions)
	//   - manage_registry_credentials (manage registry credentials)
	// USER OPERATIONS (regular scopes) → NVCF_API_KEY:
	//   - list_functions, list_cluster_groups, invoke_function, etc.
	var token string
	var expectedScope string
	clusterManagementOperation := m.isClusterManagementOperation(req)
	isAdminOperation := m.isAdminOperation(req)

	if isAdminOperation && m.functionToken != "" {
		token = m.functionToken
		if clusterManagementOperation {
			expectedScope = "cluster-management"
			log.Printf("DEBUG: Using ADMIN JWT (cluster-management operation) for %s %s - expects scope: %s", req.Method, req.URL.Path, expectedScope)
		} else {
			expectedScope = "register_function, deploy_function, delete_function, update_function, etc."
			log.Printf("DEBUG: Using FUNCTION TOKEN (function operation) for %s %s - expects scope: %s", req.Method, req.URL.Path, expectedScope)
		}
	} else if m.apiKey != "" {
		token = m.apiKey
		expectedScope = m.getExpectedUserScope(req)
		log.Printf("DEBUG: Using API KEY (user operation) for %s %s - expects scope: %s", req.Method, req.URL.Path, expectedScope)
	} else {
		// Fallback: use function token for all operations if API key is not available
		token = m.functionToken
		log.Printf("DEBUG: Using FUNCTION TOKEN (fallback) for %s %s - API key not available", req.Method, req.URL.Path)
	}

	// Set the Authorization header on the original request
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return m.base.RoundTrip(req)
}

// isAdminOperation checks if this request requires function management privileges
// Based on NVCF service @PreAuthorize annotations requiring function management scopes
func (m *multiTokenTransport) isAdminOperation(req *http.Request) bool {
	path := req.URL.Path
	method := req.Method

	// FUNCTION OPERATIONS requiring standard scopes (work with NVCF_TOKEN or NVCF_API_KEY):
	return isFunctionCreate(method, path) ||
		isDeploymentManagement(method, path) ||
		isFunctionDeletion(method, path) ||
		isFunctionMetadataUpdate(method, path) ||
		isRegistryCredentialManagement(path) ||
		isSecretUpdate(method, path) ||
		isTelemetryManagement(path) ||
		m.isClusterManagementOperation(req)
}

func isFunctionCreate(method, path string) bool {
	// register_function - Function creation.
	return method == "POST" && strings.Contains(path, "/v2/nvcf/functions")
}

func isDeploymentManagement(method, path string) bool {
	// deploy_function - Function deployment (create, read, update).
	return strings.Contains(path, deploymentPathSegment) &&
		(method == "GET" || method == "POST" || method == "PUT" || method == "PATCH")
}

func isFunctionDeletion(method, path string) bool {
	// delete_function - Function/deployment deletion.
	return method == "DELETE" && (strings.Contains(path, "/functions/") || strings.Contains(path, deploymentPathSegment))
}

func isFunctionMetadataUpdate(method, path string) bool {
	// update_function - Function metadata updates.
	return method == "PUT" && strings.Contains(path, "/metadata/")
}

func isRegistryCredentialManagement(path string) bool {
	// manage_registry_credentials - Registry credential management (all operations).
	return strings.Contains(path, "/registry-credentials") || strings.Contains(path, "/recognized-registries")
}

func isSecretUpdate(method, path string) bool {
	// update_secrets - Secrets management for functions and telemetries.
	return method == "PUT" && strings.Contains(path, "/secrets/")
}

func isTelemetryManagement(path string) bool {
	// manage_telemetries - Telemetry management.
	return strings.Contains(path, "/telemetries")
}

func (m *multiTokenTransport) isClusterManagementOperation(req *http.Request) bool {
	path := req.URL.Path

	// cluster-management — ICMS cluster register / list / rotate / delete.
	// These are scoped `cluster-management` and only the JWT
	// (admin-issuer-proxy minted) carries that scope. The `nvapi-*` API key
	// would route ICMS to its API-key introspector, which is wrong for self-hosted.
	if strings.Contains(path, "/v1/accounts/") && strings.Contains(path, "/clusters") {
		return true
	}
	return strings.Contains(path, "/v1/nvca/clusters")
}

// getExpectedUserScope returns the expected scope for user operations (for debugging)
func (m *multiTokenTransport) getExpectedUserScope(req *http.Request) string {
	path := req.URL.Path
	method := req.Method

	// Based on OpenAPI spec security requirements
	if scope, ok := functionUserScope(method, path); ok {
		return scope
	}
	if scope, ok := platformUserScope(path); ok {
		return scope
	}
	return "no-auth or unknown"
}

func functionUserScope(method, path string) (string, bool) {
	if strings.Contains(path, "/clusterGroups") {
		return "list_cluster_groups", true
	}
	if method == "POST" && strings.Contains(path, "/v2/nvcf/functions") {
		return "register_function", true
	}
	if strings.Contains(path, "/functions") && method == "GET" {
		return "no-auth (list_functions for details)", true
	}
	if strings.Contains(path, deploymentPathSegment) {
		return "deploy_function", true
	}
	if method == "DELETE" && strings.Contains(path, "/functions/") {
		return "delete_function", true
	}
	if method == "PUT" && strings.Contains(path, "/metadata/") {
		return "update_function", true
	}
	return "", false
}

func platformUserScope(path string) (string, bool) {
	if strings.Contains(path, "/registry-credentials") {
		return "manage_registry_credentials", true
	}
	if strings.Contains(path, "/telemetries") {
		return "manage_telemetries", true
	}
	if strings.Contains(path, "/authorizations/") {
		return "authorize_clients", true
	}
	if strings.Contains(path, "/queues/") {
		return "no-auth", true
	}
	return "", false
}

// newMultiTokenTransport creates a new multi-token transport
func newMultiTokenTransport(apiKey, functionToken string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &multiTokenTransport{
		apiKey:        apiKey,
		functionToken: functionToken,
		base:          base,
	}
}
