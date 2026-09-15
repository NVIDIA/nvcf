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
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	HeaderFunctionID         = "nvcf-function-id"
	HeaderRequestID          = "X-Request-ID"
	HeaderTargetRegion       = "X-NVCF-Target-Region"
	HeaderLegacyTargetRegion = "X-Groq-Region"
)

func NewContextMiddleware(cfg *config.Config) echo.MiddlewareFunc {
	requestsTotal := telemetry.HTTPRequestsTotal()
	requestDuration := telemetry.HTTPServerRequestDuration()
	activeRequests := telemetry.HTTPActiveRequests()

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			gc := NewGatewayContext(ec)
			requestStart := time.Now()

			requestID := requestIDHeader(gc.Request().Header)

			routingKey := requestRoutingKey(gc.Request())
			targetRegion := targetRegionHeader(gc.Request().Header)
			bearerToken := bearerTokenFromHeader(gc.Request().Header.Get(echo.HeaderAuthorization))
			storeRequestContext(gc, requestID, bearerToken, routingKey, targetRegion)

			gc.store.Set(contextKeyRequestID, requestID)

			parentCtx := otel.GetTextMapPropagator().Extract(
				gc.UserContext(),
				propagation.HeaderCarrier(gc.Request().Header),
			)
			routePath := requestRoute(gc)
			ctx, span := telemetry.Tracer().Start(
				parentCtx,
				gc.Request().Method+" "+routePath,
			)

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
			gc.Response().Header().Set(HeaderRequestID, requestID)

			metricAttrs := []attribute.KeyValue{
				attribute.String("method", gc.Request().Method),
				attribute.String("route", routePath),
				telemetry.FunctionIDAttribute(routingKey),
			}
			telemetry.AddUpDownWithContext(ctx, activeRequests, 1, metricAttrs...)
			defer telemetry.AddUpDownWithContext(context.WithoutCancel(ctx), activeRequests, -1, metricAttrs...)

			err := next(gc)
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

func completionLogEvent(ctx context.Context, statusCode int, err error) *zerolog.Event {
	if statusCode < http.StatusInternalServerError {
		return telemetry.Logger(ctx).Debug()
	}
	log := telemetry.Logger(ctx).Warn()
	if err != nil {
		log = log.Err(err)
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
