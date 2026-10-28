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
	"embed"
	"net/http"

	sigsyaml "sigs.k8s.io/yaml"
)

// The OpenAPI 3.1 spec for the public NvSnap API. Source of truth lives
// in api/openapi.yaml so it's reviewable as plain YAML in MRs;
// embed pulls it into the binary at build time.
//
//go:embed openapi.yaml
var openapiFS embed.FS

const openapiPath = "openapi.yaml"

// scalarDocsHTML renders the spec via Scalar — a modern, framework-
// free OpenAPI viewer. The page loads our spec from the same server
// at /api/v1/openapi.json, so there are no cross-origin concerns and
// no separate hosting required.
const scalarDocsHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>NvSnap API — Reference</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
</head>
<body>
  <script id="api-reference" data-url="/api/v1/openapi.json"></script>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`

// openapiYAMLHandler serves the spec as YAML.
func (s *Server) openapiYAML(w http.ResponseWriter, r *http.Request) {
	data, err := openapiFS.ReadFile(openapiPath)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "spec missing from binary")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}

// openapiJSON converts the embedded YAML spec to JSON on the fly.
// Conversion is fast (<1 ms) and the response is cacheable, so we
// don't precompute it at startup.
func (s *Server) openapiJSON(w http.ResponseWriter, r *http.Request) {
	yamlData, err := openapiFS.ReadFile(openapiPath)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "spec missing from binary")
		return
	}
	jsonData, err := sigsyaml.YAMLToJSON(yamlData)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "spec YAML→JSON: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(jsonData)
}

// docsHandler serves the Scalar-rendered API reference.
func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(scalarDocsHTML))
}
