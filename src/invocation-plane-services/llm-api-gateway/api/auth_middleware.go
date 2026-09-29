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

// AuthMiddlewareOption configures NewNVCFAuthMiddleware and
// NewStaticKeyAuthMiddleware.
type AuthMiddlewareOption func(*authMiddlewareOptions)

type authMiddlewareOptions struct {
	publicReads bool
}

// WithPublicReadEndpoints lets GET /v1/models and GET /v1/registry through
// without authentication when enabled. Without this option both require
// authentication.
func WithPublicReadEndpoints(enabled bool) AuthMiddlewareOption {
	return func(o *authMiddlewareOptions) {
		o.publicReads = enabled
	}
}

func newAuthMiddlewareOptions(opts []AuthMiddlewareOption) authMiddlewareOptions {
	var options authMiddlewareOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

// skip reports whether the request bypasses authentication: exactly GET on
// one of the discovery endpoints, and only when public reads are enabled.
func (o authMiddlewareOptions) skip(ec echo.Context) bool {
	return o.publicReads && isDiscoveryRead(ec.Request())
}

// isDiscoveryRead reports whether r is GET /v1/models or GET /v1/registry.
// Any other method on those paths is treated like every other route.
func isDiscoveryRead(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case modelsEndpointPath, registryEndpointPath:
		return true
	default:
		return false
	}
}

// NewNVCFAuthMiddleware authenticates requests that carry a routing key
// against the NVCF auth service. Requests without one pass through, except
// the discovery reads, which require a bearer that NVCF accepts with an empty
// routing key unless WithPublicReadEndpoints(true) is set. A nil client
// authenticates nothing (anonymous mode).
func NewNVCFAuthMiddleware(client InvocationAuthClient, opts ...AuthMiddlewareOption) echo.MiddlewareFunc {
	if client == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	options := newAuthMiddlewareOptions(opts)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			if options.skip(ec) {
				return next(ec)
			}

			gc, ok := ec.(*GatewayContext)
			if !ok {
				return next(ec)
			}

			reqCtx := gc.RequestContext()
			if reqCtx == nil || reqCtx.RoutingKey == "" {
				if isDiscoveryRead(gc.Request()) {
					return authenticateNVCFDiscoveryRead(gc, client, next)
				}
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

// authenticateNVCFDiscoveryRead authenticates a discovery read in NVCF mode.
// The request names no function, so the bearer is checked with an empty
// routing key and nothing from the response is applied to a request context.
func authenticateNVCFDiscoveryRead(
	gc *GatewayContext,
	client InvocationAuthClient,
	next echo.HandlerFunc,
) error {
	bearerToken := bearerTokenFromHeader(gc.Request().Header.Get(echo.HeaderAuthorization))
	if bearerToken == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "bearer authorization is required")
	}

	authCtx, span := telemetry.Tracer().Start(gc.UserContext(), "llm-api-gateway.auth")
	span.SetAttributes(attribute.String("auth_mode", "nvcf"))
	authResponse, err := client.AuthorizeInvocation(authCtx, bearerToken, "")
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "nvcf auth failed")
		span.End()
		return nvcfAuthHTTPError(err)
	}
	span.End()
	if authResponse == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "nvcf auth response is required")
	}
	telemetry.Logger(authCtx).
		Debug().
		Str("client_auth_id", authResponse.ClientAuthID).
		Msg("authenticated nvcf discovery read")

	return next(gc)
}

// staticModePublicPaths stay reachable without credentials in static key
// mode so liveness, readiness and version probes keep working.
var staticModePublicPaths = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
	"/info":    {},
}

// NewStaticKeyAuthMiddleware authenticates every request except the probe
// endpoints against client, the static API key authorizer. Unlike the NVCF
// middleware it never skips a request for lacking a routing key: static key
// mode has none. After authentication, requests to paths outside
// allowedPaths are refused with 403, because static keys carry no per-model
// endpoint specs. The discovery reads (GET /v1/models, GET /v1/registry) are
// always within the allowlist; WithPublicReadEndpoints(true) also exempts
// them from authentication.
func NewStaticKeyAuthMiddleware(
	client InvocationAuthClient,
	allowedPaths []string,
	opts ...AuthMiddlewareOption,
) echo.MiddlewareFunc {
	allowed := make(map[string]struct{}, len(allowedPaths))
	for _, path := range allowedPaths {
		allowed[path] = struct{}{}
	}
	options := newAuthMiddlewareOptions(opts)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			path := ec.Request().URL.Path
			if _, public := staticModePublicPaths[path]; public {
				return next(ec)
			}
			if options.skip(ec) {
				return next(ec)
			}

			gc, ok := ec.(*GatewayContext)
			if !ok || gc.RequestContext() == nil {
				// The context middleware always stores a request context in
				// static key mode; refuse rather than serve unauthenticated.
				return echo.NewHTTPError(http.StatusInternalServerError, "request context is required")
			}
			reqCtx := gc.RequestContext()

			bearerToken := reqCtx.BearerToken
			if bearerToken == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "bearer authorization is required")
			}
			if client == nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "authentication failed")
			}

			authCtx, span := telemetry.Tracer().Start(gc.UserContext(), "llm-api-gateway.auth")
			span.SetAttributes(
				attribute.String("auth_mode", "static-keys"),
				attribute.String("target_region", reqCtx.TargetRegion),
			)
			authResponse, err := client.AuthorizeInvocation(authCtx, bearerToken, "")
			if err != nil {
				span.RecordError(err)
				span.SetStatus(otelcodes.Error, "static key auth failed")
				span.End()
				return nvcfAuthHTTPError(err)
			}
			span.End()
			telemetry.Logger(authCtx).
				Debug().
				Str("client_auth_id", authResponse.ClientAuthID).
				Str("rate_limit_key", authResponse.RateLimitKey).
				Msg("authenticated static api key")

			if err := applyInvocationAuth(reqCtx, authResponse, bearerToken); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}

			if _, ok := allowed[path]; !ok && !isDiscoveryRead(gc.Request()) {
				return echo.NewHTTPError(
					http.StatusForbidden,
					fmt.Sprintf("endpoint %q is not enabled on this gateway", path),
				)
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
	reqCtx.RateLimitKey = rateLimitKey
	reqCtx.ProjectID = authResponse.ProjectID
	reqCtx.BearerToken = bearerToken
	if authRoutingKey := authResponse.RoutingKey; authRoutingKey != "" {
		reqCtx.RoutingKey = authRoutingKey
	}
	reqCtx.ModelSpecs = authResponse.ModelSpecs
	reqCtx.Priority = authResponse.Priority

	return nil
}

// rateLimitSubjectKey builds the rate-limit store key. The routing key
// segment is omitted when empty (static key mode), which keeps the key stable
// per caller and identical to the NVCF format whenever a routing key exists.
func rateLimitSubjectKey(rateLimitKey string, projectID string, routingKey string) string {
	key := "nvcf:" + rateLimitKey
	if projectID != "" {
		key += ":project:" + projectID
	}
	if routingKey != "" {
		key += ":routing_key:" + routingKey
	}
	return key
}

// nvcfAuthHTTPError maps an auth gRPC error onto an HTTP response.
//
// The messages are deliberately generic. A gRPC error stringifies to something
// like:
//
//	rpc error: code = Unavailable desc = connection error: desc = "transport:
//	Error while dialing dial tcp 10.0.0.5:9090: connect: connection refused"
//
// so returning err.Error() handed every caller the auth service's address, port
// and dial state. This path is reachable before authentication succeeds, so
// that was available to anyone who could reach the gateway.
//
// The status code still carries what a caller can act on. The detail is not
// lost: the call site records the original error on the span before calling
// this, so it stays in traces for debugging.
func nvcfAuthHTTPError(err error) error {
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.InvalidArgument:
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	case codes.Unauthenticated:
		return echo.NewHTTPError(http.StatusUnauthorized, "authentication failed")
	case codes.PermissionDenied:
		return echo.NewHTTPError(http.StatusForbidden, "permission denied")
	case codes.NotFound:
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	case codes.DeadlineExceeded:
		return echo.NewHTTPError(http.StatusGatewayTimeout, "authentication timed out")
	case codes.Unavailable:
		return echo.NewHTTPError(http.StatusServiceUnavailable, "authentication service unavailable")
	default:
		return echo.NewHTTPError(http.StatusBadGateway, "authentication failed")
	}
}
