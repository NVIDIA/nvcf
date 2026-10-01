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
	_ "embed"
	"net/http"

	echo "github.com/labstack/echo/v4"
)

//go:embed openapi.yaml
var openAPISpecYAML []byte

const openAPIDocsHTML = `<!doctype html>
<html>
  <head>
    <title>LLM API Gateway Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="/openapi.yaml"></script>
    <script
      src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.25.130"
      integrity="sha384-oyL8y2b0EvVxYsvg2qNlT8xHcsvmySaljIklWvbATuTrccZSyvWnsPnmOunYQL2R"
      crossorigin="anonymous"></script>
  </body>
</html>`

// ServeOpenAPISpec serves the raw OpenAPI 3.1 YAML document.
func ServeOpenAPISpec(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/yaml", openAPISpecYAML)
}

// ServeOpenAPIDocs serves an interactive API reference rendered with Scalar.
func ServeOpenAPIDocs(c echo.Context) error {
	return c.HTML(http.StatusOK, openAPIDocsHTML)
}
