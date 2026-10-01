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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
)

func TestOpenAPIRoutesServed(t *testing.T) {
	t.Parallel()

	e := echo.New()
	RegisterRoutes(e, NewHandlers(config.Default(), nil, nil))

	t.Run("GET /openapi.yaml returns spec", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/yaml", rec.Header().Get(echo.HeaderContentType))
		assert.Equal(t, openAPISpecYAML, rec.Body.Bytes())
	})

	t.Run("GET /docs returns interactive documentation", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/docs", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Header().Get(echo.HeaderContentType), "text/html")
		assert.Contains(t, rec.Body.String(), "/openapi.yaml")
		assert.Contains(t, rec.Body.String(), "@scalar/api-reference")
	})
}

func TestOpenAPISpecMatchesRegisteredRoutes(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, openAPISpecYAML, "openapi.yaml must not be empty")

	var spec struct {
		OpenAPI string                    `yaml:"openapi"`
		Info    map[string]any            `yaml:"info"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	err := yaml.Unmarshal(openAPISpecYAML, &spec)
	require.NoError(t, err, "openapi.yaml must be valid YAML")
	assert.True(t, strings.HasPrefix(spec.OpenAPI, "3.1"), "expected OpenAPI 3.1.x, got %s", spec.OpenAPI)

	e := echo.New()
	RegisterRoutes(e, NewHandlers(config.Default(), nil, nil))

	registeredRoutes := make(map[string]struct{})
	for _, route := range e.Routes() {
		registeredRoutes[route.Method+" "+route.Path] = struct{}{}
	}

	validHTTPMethods := map[string]string{
		"get":     http.MethodGet,
		"post":    http.MethodPost,
		"put":     http.MethodPut,
		"delete":  http.MethodDelete,
		"patch":   http.MethodPatch,
		"head":    http.MethodHead,
		"options": http.MethodOptions,
	}

	// 1. Verify every path and method documented in the OpenAPI spec is registered on Echo router
	for path, methods := range spec.Paths {
		for methodKey := range methods {
			httpMethod, ok := validHTTPMethods[strings.ToLower(methodKey)]
			if !ok {
				continue
			}
			routeKey := httpMethod + " " + path
			assert.Contains(t, registeredRoutes, routeKey, "spec documents %s, but route is not registered in router", routeKey)
		}
	}

	// 2. Verify all primary public and inference endpoints are present in the OpenAPI spec
	expectedInSpec := []string{
		"/healthz",
		"/readyz",
		"/info",
		"/v1/chat/completions",
		"/v1/responses",
		"/v1/embeddings",
	}

	for _, path := range expectedInSpec {
		assert.Contains(t, spec.Paths, path, "route %s is registered but missing from OpenAPI spec", path)
	}
}
