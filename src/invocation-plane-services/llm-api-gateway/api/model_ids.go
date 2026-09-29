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
	"strings"

	"github.com/google/uuid"
	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

const canonicalUUIDLength = 36

func splitOpenAIModelID(modelID string) (routingKey string, routedModel string, hasPrefix bool) {
	if modelID == "" {
		return "", "", false
	}

	routingKey, routedModel, hasPrefix = strings.Cut(modelID, "/")
	if !hasPrefix {
		return "", modelID, false
	}

	return canonicalizeRoutingKey(routingKey), routedModel, true
}

// canonicalizeRoutingKey lowercases routing keys written as standard
// hyphenated UUIDs. UUID hex digits are case-insensitive on input
// (RFC 9562 section 4), but routing targets register under the lowercase
// form, so mixed-case keys must be canonicalized before auth and routing.
// Non-UUID routing keys are opaque and pass through unchanged.
func canonicalizeRoutingKey(routingKey string) string {
	if len(routingKey) != canonicalUUIDLength {
		return routingKey
	}
	parsed, err := uuid.Parse(routingKey)
	if err != nil {
		return routingKey
	}
	return parsed.String()
}

func routingKeyFromOpenAIModelID(modelID string) string {
	routingKey, _, hasPrefix := splitOpenAIModelID(modelID)
	if !hasPrefix {
		return ""
	}
	return routingKey
}

func normalizeOpenAIRequestModel(
	reqCtx *requestctx.RequestContext,
	model string,
) (routedModel string, err error) {
	if model == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "model is required")
	}

	routingKey, routedModel, hasPrefix := splitOpenAIModelID(model)
	if !hasPrefix {
		return "", echo.NewHTTPError(http.StatusBadRequest, "model prefix is required")
	}
	if routingKey == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "routing key is required before model")
	}
	if routedModel == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "model suffix is required after routing key")
	}

	if reqCtx != nil {
		existingRoutingKey := reqCtx.RoutingKey
		switch {
		case existingRoutingKey == "":
			reqCtx.RoutingKey = routingKey
		case existingRoutingKey != routingKey:
			return "", echo.NewHTTPError(
				http.StatusBadRequest,
				"model targets a different routing key",
			)
		}
	}

	return routedModel, nil
}

// normalizeBareRequestModel validates a static key mode model id. The model
// string is used as is: it carries no routing key, so a "/" is part of the
// model name.
func normalizeBareRequestModel(reqCtx *requestctx.RequestContext, model string) (string, error) {
	if model == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "model is required")
	}
	if reqCtx != nil && reqCtx.RoutingKey != "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "routing keys are not supported by this gateway")
	}
	return model, nil
}

// normalizeRequestModel resolves the routed model for the gateway's model id
// scheme: bare model ids in static key mode, "<routing key>/<model>" in NVCF
// mode.
func (h *Handlers) normalizeRequestModel(reqCtx *requestctx.RequestContext, model string) (string, error) {
	if h.bareModelIDs() {
		return normalizeBareRequestModel(reqCtx, model)
	}
	return normalizeOpenAIRequestModel(reqCtx, model)
}

// missingRequestContextError is returned when a handler runs without a
// request context. In NVCF mode that means the model carried no routing-key
// prefix; static key mode always stores a context, so there it can only mean
// the model is missing.
func (h *Handlers) missingRequestContextError() error {
	if h.bareModelIDs() {
		return echo.NewHTTPError(http.StatusBadRequest, "model is required")
	}
	return echo.NewHTTPError(http.StatusBadRequest, "model prefix is required")
}

func (h *Handlers) bareModelIDs() bool {
	return h != nil && h.config.StaticAuthMode()
}

func setRoutingMethodForModel(reqCtx *requestctx.RequestContext, model string) {
	if reqCtx == nil {
		return
	}

	reqCtx.RoutingMethod = ""
	if reqCtx.ModelSpecs == nil {
		return
	}

	spec, ok := reqCtx.ModelSpecs[model]
	if !ok || spec.RoutingMethod == "" {
		return
	}

	routingMethod := strings.TrimSpace(spec.RoutingMethod)
	if routingMethod == "" {
		return
	}

	reqCtx.RoutingMethod = routingMethod
}
