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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	echo "github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	HeaderFunctionID         = "nvcf-function-id"
	HeaderRequestID          = "X-Request-ID"
	HeaderTargetRegion       = "X-NVCF-Target-Region"
	HeaderLegacyTargetRegion = "X-Groq-Region"

	// Anthropic SDKs read the request ID only from this header.
	headerMessagesRequestID = "Request-Id"
)

func NewContextMiddleware(cfg *config.Config) echo.MiddlewareFunc {
	requestsTotal := telemetry.HTTPRequestsTotal()
	requestDuration := telemetry.HTTPServerRequestDuration()
	activeRequests := telemetry.HTTPActiveRequests()
	var maxRequestBodyBytes int64
	if cfg != nil {
		maxRequestBodyBytes = cfg.Server.MaxRequestBodyBytes
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			gc := NewGatewayContext(ec)
			requestStart := time.Now()

			requestID := requestIDHeader(gc.Request().Header)
			gc.store.Set(contextKeyRequestID, requestID)
			// The error handler receives the original echo context.
			ec.Set(contextKeyRequestID, requestID)

			parentCtx := otel.GetTextMapPropagator().Extract(
				gc.UserContext(),
				propagation.HeaderCarrier(gc.Request().Header),
			)
			routePath := requestRoute(gc)
			ctx, span := telemetry.Tracer().Start(
				parentCtx,
				gc.Request().Method+" "+routePath,
			)

			// Enforce the body limit inside the span and before anything else
			// buffers the body, including the routing-key lookup. A rejected
			// request still gets the span, metrics, and completion log.
			bodyErr := bufferRequestBody(gc.Request(), maxRequestBodyBytes)
			routingKey := ""
			if bodyErr == nil {
				routingKey = requestRoutingKey(gc.Request())
			}
			targetRegion := targetRegionHeader(gc.Request().Header)
			bearerToken := callerCredential(gc.Request())
			storeRequestContext(gc, requestID, bearerToken, routingKey, targetRegion)

			span.SetAttributes(
				attribute.String("http.request.method", gc.Request().Method),
				attribute.String("http.request_id", requestID),
				attribute.String("url.path", routePath),
				attribute.String("gateway.routing_key", routingKey),
			)
			if routingKey != "" {
				span.SetAttributes(attribute.String("nvcf.function.id", routingKey))
			}

			logger := requestLogger(requestID, routingKey, targetRegion, gc.Request().Method, routePath)

			ctx = telemetry.LoggingSpanContext(ctx, logger)
			gc.SetUserContext(ctx)
			gc.SetRequest(gc.Request().WithContext(ctx))
			logRequestBodyReadFailure(ctx, bodyErr)
			gc.Response().Header().Set(HeaderRequestID, requestID)
			switch errorShapeFor(gc.Request()) {
			case shapeAnthropic:
				// Runs as headers are written, after backend headers are
				// copied, so the NVCF ID replaces any backend request ID.
				gc.Response().Before(func() {
					gc.Response().Header().Set(HeaderRequestID, requestID)
					gc.Response().Header().Set(headerMessagesRequestID, requestID)
				})
			case shapeSystemOne:
				// System One errors carry the NVCF ID in the body, so the
				// header must match after backend headers are copied.
				gc.Response().Before(func() {
					if relayed, _ := gc.Get(contextKeyBackendErrorRelayed).(bool); relayed {
						return
					}
					gc.Response().Header().Set(HeaderRequestID, requestID)
				})
			}

			metricAttrs := []attribute.KeyValue{
				attribute.String("method", gc.Request().Method),
				attribute.String("route", routePath),
				telemetry.FunctionIDAttribute(routingKey),
			}
			telemetry.AddUpDownWithContext(ctx, activeRequests, 1, metricAttrs...)
			defer telemetry.AddUpDownWithContext(context.WithoutCancel(ctx), activeRequests, -1, metricAttrs...)

			err := bodyErr
			if err == nil {
				err = next(gc)
			}
			statusCode := httpStatusCode(gc, err)
			status := strconv.Itoa(statusCode)
			finalAttrs := append(metricAttrs, attribute.String("status", status))
			telemetry.AddWithContext(context.WithoutCancel(ctx), requestsTotal, 1, finalAttrs...)
			telemetry.RecordWithContext(context.WithoutCancel(ctx), requestDuration, time.Since(requestStart).Seconds(), finalAttrs...)

			finishHTTPSpan(span, statusCode, err)
			span.End()

			logCompletedHTTPRequest(ctx, gc, requestStart, statusCode, err)

			return err
		}
	}
}

// logRequestBodyReadFailure logs the internal cause of a body read failure,
// which the client response omits. Oversized bodies carry no internal cause
// and are covered by the completion log.
func logRequestBodyReadFailure(ctx context.Context, err error) {
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Internal == nil {
		return
	}
	telemetry.Logger(ctx).Warn().
		Err(httpErr.Internal).
		Int("status", httpErr.Code).
		Msg("failed to read request body")
}

func requestIDHeader(headers http.Header) string {
	if requestID := headers.Get(HeaderRequestID); requestID != "" {
		return requestID
	}
	return uuid.NewString()
}

func storeRequestContext(
	gc *GatewayContext,
	requestID string,
	bearerToken string,
	routingKey string,
	targetRegion string,
) {
	if routingKey == "" {
		return
	}
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:    requestID,
		BearerToken:  bearerToken,
		RoutingKey:   routingKey,
		TargetRegion: targetRegion,
	})
}

func requestLogger(requestID, routingKey, targetRegion, method, routePath string) zerolog.Logger {
	return zlog.With().
		Str("request_id", requestID).
		Str("routing_key", routingKey).
		Str("target_region", targetRegion).
		Str("method", method).
		Str("route", routePath).
		Logger()
}

func finishHTTPSpan(span trace.Span, statusCode int, err error) {
	span.SetAttributes(attribute.Int("http.response.status_code", statusCode))
	if err != nil {
		span.RecordError(err)
	}
	if statusCode >= http.StatusBadRequest {
		span.SetStatus(codes.Error, http.StatusText(statusCode))
	}
}

func logCompletedHTTPRequest(ctx context.Context, gc *GatewayContext, requestStart time.Time, statusCode int, err error) {
	log := completionLogEvent(ctx, statusCode, err).
		Int("status", statusCode).
		Dur("duration", time.Since(requestStart))
	if reqCtx := gc.RequestContext(); reqCtx != nil {
		log = log.
			Str("project_id", reqCtx.ProjectID).
			Str("rate_limit_key", reqCtx.OrgID).
			Str("routing_key", reqCtx.RoutingKey).
			Str("target_region", reqCtx.TargetRegion)
	}
	log.Msg("completed http request")
}

// completionLogEvent logs request-router rejections at info or above with
// router_error_code, because clients see the code only when
// ExposeRouterErrorCodes is enabled.
func completionLogEvent(ctx context.Context, statusCode int, err error) *zerolog.Event {
	var upstream *provider.UpstreamError
	routerCode := ""
	if errors.As(err, &upstream) {
		routerCode = upstream.RouterCode
	}
	var log *zerolog.Event
	switch {
	case statusCode >= http.StatusInternalServerError:
		log = telemetry.Logger(ctx).Warn()
		if err != nil {
			log = log.Err(err)
		}
	case routerCode != "":
		log = telemetry.Logger(ctx).Info()
	default:
		return telemetry.Logger(ctx).Debug()
	}
	if routerCode != "" {
		log = log.Str("router_error_code", routerCode)
	}
	return log
}

func targetRegionHeader(headers http.Header) string {
	if value := headers.Get(HeaderTargetRegion); value != "" {
		return value
	}
	return headers.Get(HeaderLegacyTargetRegion)
}

func requestRoute(gc *GatewayContext) string {
	if gc != nil {
		if route := gc.Path(); route != "" {
			return route
		}
	}
	return "unknown"
}

func httpStatusCode(gc *GatewayContext, err error) int {
	if err != nil {
		if httpErr, ok := err.(*echo.HTTPError); ok {
			return httpErr.Code
		}
	}
	if gc != nil && gc.Response() != nil && gc.Response().Status > 0 {
		return gc.Response().Status
	}
	if err != nil {
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

// headerAPIKey is the Anthropic SDK default credential header.
const headerAPIKey = "X-Api-Key"

// callerCredential returns the NVCF API key that authorizes the request.
// Authorization, when present, is authoritative on every route. Messages also
// accepts X-Api-Key so Anthropic SDK clients work with their default settings.
// The Stargate provider strips both headers from outbound Messages requests.
func callerCredential(r *http.Request) string {
	if len(r.Header.Values(echo.HeaderAuthorization)) > 0 {
		return bearerTokenFromHeader(r.Header.Get(echo.HeaderAuthorization))
	}
	if isMessagesRequest(r) {
		return r.Header.Get(headerAPIKey)
	}
	return ""
}

// isMessagesRequest reports whether r targets the Anthropic Messages route.
// Credential selection, request-id, the 401 message, and the error envelope
// all use it so they stay in step.
func isMessagesRequest(r *http.Request) bool {
	return r.URL.Path == messagesEndpointPath
}

func bearerTokenFromHeader(value string) string {
	authHeader := value
	if authHeader == "" {
		return ""
	}

	if token, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		return token
	}

	return ""
}
