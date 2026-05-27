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
	"context"
	"fmt"
	"net/http"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

type InvocationAuthClient interface {
	AuthorizeInvocation(
		ctx context.Context,
		clientAuthorizationToken string,
		routingKey string,
	) (*nvcf.InvocationAuthResponse, error)
}

func NewNVCFAuthMiddleware(client InvocationAuthClient) echo.MiddlewareFunc {
	if client == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			gc, ok := ec.(*GatewayContext)
			if !ok {
				return next(ec)
			}

			reqCtx := gc.RequestContext()
			if reqCtx == nil || reqCtx.RoutingKey == "" {
				return next(gc)
			}

			bearerToken := reqCtx.BearerToken
			if bearerToken == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "bearer authorization is required")
			}

			authCtx, span := telemetry.Tracer().Start(gc.UserContext(), "llm-api-gateway.auth")
			span.SetAttributes(
				attribute.String("routing_key", reqCtx.RoutingKey),
				attribute.String("target_region", reqCtx.TargetRegion),
			)
			authResponse, err := client.AuthorizeInvocation(
				authCtx,
				bearerToken,
				reqCtx.RoutingKey,
			)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(otelcodes.Error, "nvcf auth failed")
				span.End()
				return nvcfAuthHTTPError(err)
			}
			span.End()
			telemetry.Logger(authCtx).
				Info().
				Str("auth_routing_key", authResponse.RoutingKey).
				Str("client_auth_id", authResponse.ClientAuthID).
				Str("project_id", authResponse.ProjectID).
				Str("rate_limit_key", authResponse.RateLimitKey).
				Interface("auth_context", authResponse.AuthContext).
				Msg("received nvcf auth response")

			if err := applyInvocationAuth(reqCtx, authResponse, bearerToken); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}

			return next(gc)
		}
	}
}

func applyInvocationAuth(
	reqCtx *requestctx.RequestContext,
	authResponse *nvcf.InvocationAuthResponse,
	bearerToken string,
) error {
	if reqCtx == nil {
		return fmt.Errorf("request context is required")
	}
	if authResponse == nil {
		return fmt.Errorf("nvcf auth response is required")
	}

	if authRoutingKey := authResponse.RoutingKey; authRoutingKey != "" && authRoutingKey != reqCtx.RoutingKey {
		return fmt.Errorf(
			"nvcf auth returned unexpected routing key %q for routing key %q",
			authRoutingKey,
			reqCtx.RoutingKey,
		)
	}

	rateLimitKey := authResponse.RateLimitKey
	if rateLimitKey == "" {
		return fmt.Errorf("nvcf auth response did not include a rate limit key")
	}

	reqCtx.APIKeyID = authResponse.ClientAuthID
	reqCtx.OrgID = rateLimitKey
	reqCtx.ProjectID = authResponse.ProjectID
	reqCtx.BearerToken = bearerToken
	if authRoutingKey := authResponse.RoutingKey; authRoutingKey != "" {
		reqCtx.RoutingKey = authRoutingKey
	}
	reqCtx.ModelSpecs = authResponse.ModelSpecs

	return nil
}

func rateLimitSubjectKey(rateLimitKey string, projectID string, routingKey string) string {
	if projectID != "" {
		return fmt.Sprintf("nvcf:%s:project:%s:routing_key:%s", rateLimitKey, projectID, routingKey)
	}

	return fmt.Sprintf("nvcf:%s:routing_key:%s", rateLimitKey, routingKey)
}

func nvcfAuthHTTPError(err error) error {
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.Unauthenticated:
		return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	case codes.PermissionDenied:
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	case codes.NotFound:
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case codes.DeadlineExceeded:
		return echo.NewHTTPError(http.StatusGatewayTimeout, err.Error())
	case codes.Unavailable:
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	default:
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}
}
