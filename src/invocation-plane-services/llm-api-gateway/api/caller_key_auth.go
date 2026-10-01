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

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/auth/statickeys"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const callerKeySubjectPrefix = "api-key:"

// Probes and build info stay open so kubelet and operators need no key. Every
// other route, including ones added later, requires one.
var routesWithoutCallerKey = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
	"/info":    {},
}

// Discovery reads skip the key only when public discovery reads are enabled.
// Matched by route pattern, so /v1/models/{id} is covered.
var discoveryReadRoutes = map[string]struct{}{
	"/v1/models":           {},
	modelsPathPrefix + "*": {},
	"/v1/registry":         {},
}

// NewCallerKeyAuthMiddleware authenticates callers against static API keys in
// place of NVCF auth. A nil key set disables it. With publicDiscoveryReads,
// GET on the model and registry routes needs no key.
func NewCallerKeyAuthMiddleware(keys *statickeys.KeySet, publicDiscoveryReads bool) echo.MiddlewareFunc {
	if keys == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			if _, ok := routesWithoutCallerKey[ec.Path()]; ok {
				return next(ec)
			}
			if publicDiscoveryReads && ec.Request().Method == http.MethodGet {
				if _, ok := discoveryReadRoutes[ec.Path()]; ok {
					return next(ec)
				}
			}

			apiKey := bearerTokenFromHeader(ec.Request().Header.Get(echo.HeaderAuthorization))
			keyID, ok := keys.Lookup(apiKey)
			if !ok {
				return invalidCallerKey()
			}

			// The key authenticates the caller to this gateway only. The
			// pass-through handlers forward a copy of the inbound headers, so
			// remove it before they run.
			ec.Request().Header.Del(echo.HeaderAuthorization)

			subject := callerKeySubjectPrefix + keyID
			telemetry.Logger(ec.Request().Context()).
				Info().
				Str("subject", subject).
				Msg("authenticated caller")

			if gc, ok := ec.(*GatewayContext); ok {
				applyCallerKeyAuth(gc.RequestContext(), subject, keyID)
			}
			return next(ec)
		}
	}
}

// applyCallerKeyAuth records the authorizer result. It adds no routing key, so
// the request keeps whatever routing key its model name carries.
func applyCallerKeyAuth(reqCtx *requestctx.RequestContext, subject string, keyID string) {
	if reqCtx == nil {
		return
	}
	reqCtx.APIKeyID = subject
	reqCtx.OrgID = keyID
	reqCtx.ProjectID = ""
	reqCtx.BearerToken = ""
	reqCtx.ModelSpecs = nil
	reqCtx.Priority = nil
}

func invalidCallerKey() error {
	return echo.NewHTTPError(http.StatusUnauthorized, models.ErrorResponse{Error: models.Error{
		Code:    "invalid_api_key",
		Message: "Missing or invalid API key",
		Type:    "invalid_request_error",
	}})
}
