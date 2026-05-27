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

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

func splitOpenAIModelID(modelID string) (routingKey string, routedModel string, hasPrefix bool) {
	if modelID == "" {
		return "", "", false
	}

	routingKey, routedModel, hasPrefix = strings.Cut(modelID, "/")
	if !hasPrefix {
		return "", modelID, false
	}

	return routingKey, routedModel, true
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
