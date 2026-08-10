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

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// TestOpenAPIEmbedded asserts the spec is reachable via go:embed and
// is structurally a valid OpenAPI 3.1 document.
func TestOpenAPIEmbedded(t *testing.T) {
	data, err := openapiFS.ReadFile(openapiPath)
	if err != nil {
		t.Fatalf("read embedded spec: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("embedded spec is empty")
	}

	jsonData, err := sigsyaml.YAMLToJSON(data)
	if err != nil {
		t.Fatalf("YAML→JSON: %v", err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}

	if got, _ := doc["openapi"].(string); !strings.HasPrefix(got, "3.") {
		t.Fatalf("openapi version: got %q, want 3.x", got)
	}

	paths, ok := doc["paths"].(map[string]interface{})
	if !ok || len(paths) == 0 {
		t.Fatal("spec has no paths")
	}

	// Spot-check that the documented public endpoints are present.
	// If any of these are removed from the spec the test fails so we
	// notice the contract change.
	required := []string{
		// Public
		"/api/v1/health",
		"/api/v1/nodes",
		"/api/v1/pods",
		"/api/v1/checkpoints",
		"/api/v1/checkpoints/{id}",
		"/api/v1/restores",
		"/api/v1/retention-policies",
		"/api/v1/audit",
		// Storage
		"/api/v1/blobstore/stats",
		"/api/v1/blobstore/captures",
		// Realtime
		"/api/v1/ws",
		// Internal — documented so operators can debug, may change.
		"/api/v1/checkpoints/{id}/sources",
		"/api/v1/checkpoints/{id}/peer-add",
		"/api/v1/checkpoints/register",
		// Observability
		"/metrics",
	}
	for _, p := range required {
		if _, ok := paths[p]; !ok {
			t.Errorf("spec missing path: %s", p)
		}
	}
}

// TestOpenAPIHandlers asserts /openapi.yaml, /openapi.json, /docs all
// respond with the right content type and a non-empty body.
func TestOpenAPIHandlers(t *testing.T) {
	s := &Server{}

	cases := []struct {
		name       string
		handler    http.HandlerFunc
		wantType   string
		wantSubstr string
	}{
		{"yaml", s.openapiYAML, "application/yaml", "openapi: 3.1.0"},
		{"json", s.openapiJSON, "application/json", `"openapi"`},
		{"docs", s.docs, "text/html", "@scalar/api-reference"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/openapi", http.NoBody)
			rr := httptest.NewRecorder()
			tc.handler(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if got := rr.Header().Get("Content-Type"); !strings.Contains(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want substring %q", got, tc.wantType)
			}
			if !strings.Contains(rr.Body.String(), tc.wantSubstr) {
				t.Errorf("body missing %q", tc.wantSubstr)
			}
		})
	}
}
