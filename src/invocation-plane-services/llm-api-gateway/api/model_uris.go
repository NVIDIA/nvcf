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
	"fmt"
	"net/http"
	"strings"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	chatCompletionsEndpointPath = "/v1/chat/completions"
	responsesEndpointPath       = "/v1/responses"
	embeddingsEndpointPath      = "/v1/embeddings"
)

// requireModelURIAllowlist checks the model's declared uris allowlist for
// endpointPath. Models with no spec or an empty uris list are allowed
// unchanged. An undeclared endpoint is always counted and logged; enforce
// controls whether the request is also refused with a 400.
func (h *Handlers) requireModelURIAllowlist(
	c *GatewayContext,
	model string,
	endpointPath string,
	enforce bool,
) error {
	reqCtx := c.RequestContext()
	if reqCtx == nil || reqCtx.ModelSpecs == nil {
		return nil
	}

	spec, ok := reqCtx.ModelSpecs[model]
	if !ok || len(spec.URIs) == 0 {
		return nil
	}

	for _, uri := range spec.URIs {
		if modelURIMatches(uri, endpointPath) {
			return nil
		}
	}

	mode := "log"
	if enforce {
		mode = "enforce"
	}
	ctx := c.UserContext()
	telemetry.AddWithContext(
		ctx,
		h.observability.modelURIAllowlistRejections,
		1,
		attribute.String("endpoint", endpointPath),
	)
	telemetry.Logger(ctx).Warn().
		Str("model", model).
		Str("endpoint", endpointPath).
		Str("mode", mode).
		Msg("endpoint not in model uris allowlist")

	if !enforce {
		return nil
	}
	return echo.NewHTTPError(
		http.StatusBadRequest,
		fmt.Sprintf("model %q does not support %s", model, endpointPath),
	)
}

// modelURIAllowlistEnabled reports whether undeclared endpoints are refused rather
// than only counted and logged. Controlled by the MODEL_URI_ALLOWLIST_ENABLED
// environment variable; defaults to false for safe rollout.
func (h *Handlers) modelURIAllowlistEnabled() bool {
	return h != nil && h.config != nil && h.config.ModelURIAllowlistEnabled
}

// modelURIMatches reports whether a declared model uri refers to
// endpointPath. Declared uris are matched leniently: surrounding whitespace,
// a missing leading slash, trailing slashes, and letter case are ignored.
func modelURIMatches(uri, endpointPath string) bool {
	return strings.EqualFold(normalizeModelURI(uri), endpointPath)
}

func normalizeModelURI(uri string) string {
	uri = strings.TrimSpace(uri)
	uri = strings.TrimRight(uri, "/")
	if uri == "" {
		return ""
	}
	return "/" + strings.TrimPrefix(uri, "/")
}
