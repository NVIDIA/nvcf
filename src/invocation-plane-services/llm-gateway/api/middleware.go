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
	"strings"
	"time"

	"github.com/google/uuid"
	echo "github.com/labstack/echo/v4"
	zlog "github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
	"github.com/NVIDIA/nvcf/llm-api-gateway/telemetry"
)

const (
	HeaderFunctionID   = "nvcf-function-id"
	HeaderRequestID    = "X-Request-ID"
	HeaderTargetRegion = "X-Groq-Region"
)

func NewContextMiddleware(cfg *config.Config) echo.MiddlewareFunc {
	meter := otel.GetMeterProvider().Meter(telemetry.ServiceName())

	recordDuration := func(context.Context, float64, ...attribute.KeyValue) {}
	if histogram, err := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("duration of inbound HTTP requests"),
	); err == nil {
		recordDuration = func(ctx context.Context, value float64, attrs ...attribute.KeyValue) {
			histogram.Record(ctx, value, metric.WithAttributes(attrs...))
		}
	} else {
		otel.Handle(err)
	}

	recordActiveRequests := func(context.Context, int64, ...attribute.KeyValue) {}
	if counter, err := meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("number of concurrent in-flight HTTP requests"),
	); err == nil {
		recordActiveRequests = func(ctx context.Context, delta int64, attrs ...attribute.KeyValue) {
			counter.Add(ctx, delta, metric.WithAttributes(attrs...))
		}
	} else {
		otel.Handle(err)
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			gc := NewGatewayContext(ec)
			requestStart := time.Now()

			requestID := gc.Request().Header.Get(HeaderRequestID)
			if requestID == "" {
				requestID = uuid.NewString()
			}

			routingKey := requestRoutingKey(gc.Request())
			bearerToken := bearerTokenFromHeader(gc.Request().Header.Get(echo.HeaderAuthorization))
			if routingKey != "" {
				gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
					RequestID:    requestID,
					BearerToken:  bearerToken,
					RoutingKey:   routingKey,
					TargetRegion: gc.Request().Header.Get(HeaderTargetRegion),
				})
			}

			gc.store.Set(contextKeyRequestID, requestID)

			parentCtx := otel.GetTextMapPropagator().Extract(
				gc.UserContext(),
				propagation.HeaderCarrier(gc.Request().Header),
			)
			routePath := gc.Path()
			if routePath == "" {
				routePath = gc.Request().URL.Path
			}
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

			logger := zlog.With().
				Str("request_id", requestID).
				Str("routing_key", routingKey).
				Logger()

			ctx = telemetry.LoggingSpanContext(ctx, logger)
			gc.SetUserContext(ctx)
			gc.SetRequest(gc.Request().WithContext(ctx))
			gc.Response().Header().Set(HeaderRequestID, requestID)

			metricAttrs := []attribute.KeyValue{
				attribute.String("http.request.method", gc.Request().Method),
				attribute.String("url.path", routePath),
			}
			recordActiveRequests(ctx, 1, metricAttrs...)
			defer recordActiveRequests(context.WithoutCancel(ctx), -1, metricAttrs...)

			err := next(gc)
			statusCode := httpStatusCode(gc, err)
			finalAttrs := append(metricAttrs, attribute.Int("http.response.status_code", statusCode))
			recordDuration(context.WithoutCancel(ctx), time.Since(requestStart).Seconds(), finalAttrs...)

			span.SetAttributes(attribute.Int("http.response.status_code", statusCode))
			if err != nil {
				span.RecordError(err)
			}
			if statusCode >= http.StatusBadRequest {
				span.SetStatus(codes.Error, http.StatusText(statusCode))
			}
			span.End()

			return err
		}
	}
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
