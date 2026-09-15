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

package utils

import (
	"context"
	"net/http"

	"github.com/openzipkin/zipkin-go/propagation/b3"
	"google.golang.org/grpc/metadata"
)

const (
	// HandlerTypeCustom Set this value in any custom handler bypassing grpc-middleware.
	// This makes sure that any value passed in the context is processed by auth middlewares
	HandlerTypeCustom  = "custom-handler"
	HeaderKeyRequestID = "x-request-id"
	HeaderKeyAuditID   = "x-nv-audit-id"
	HeaderETag         = "etag"
	HeaderNewETag      = "new-etag"
	HeaderIfMatch      = "if-match"
)

// requestContextKey is a defined type for context keys (not the built-in string) to avoid collisions (SA1029).
type requestContextKey string

const (
	keyHandlerTypeCustom requestContextKey = "handlerTypeCustom"
	keyRequestID         requestContextKey = "requestID"
	keyAuditID           requestContextKey = "auditID"
	keyETag              requestContextKey = "eTag"
	keyB3TraceID         requestContextKey = "b3TraceID"
	keyB3SpanID          requestContextKey = "b3SpanID"
	keyB3ParentSpanID    requestContextKey = "b3ParentSpanID"
	keyB3Flags           requestContextKey = "b3Flags"
	keyB3Sampled         requestContextKey = "b3Sampled"
)

// AddRequestInfoToContext adds any custom values from request headers to request context that helps nvkit middleware
func AddRequestInfoToContext(req *http.Request) *http.Request {
	ctx := req.Context()
	ctx = context.WithValue(ctx, keyHandlerTypeCustom, HandlerTypeCustom)
	ctx = context.WithValue(ctx, keyRequestID, req.Header.Get(HeaderKeyRequestID))
	ctx = context.WithValue(ctx, keyAuditID, req.Header.Get(HeaderKeyAuditID))
	ctx = context.WithValue(ctx, keyETag, req.Header.Get(HeaderETag))
	ctx = context.WithValue(ctx, keyB3TraceID, req.Header.Get(b3.TraceID))
	ctx = context.WithValue(ctx, keyB3SpanID, req.Header.Get(b3.SpanID))
	ctx = context.WithValue(ctx, keyB3ParentSpanID, req.Header.Get(b3.ParentSpanID))
	ctx = context.WithValue(ctx, keyB3Flags, req.Header.Get(b3.Flags))
	ctx = context.WithValue(ctx, keyB3Sampled, req.Header.Get(b3.Sampled))
	return req.WithContext(ctx)
}

// CustomHandlerContext reports whether ctx carries the custom-handler marker set by AddRequestInfoToContext.
func CustomHandlerContext(ctx context.Context) bool {
	return ctx.Value(keyHandlerTypeCustom) != nil
}

// RequestIDFromRequestContext returns the request ID stored by AddRequestInfoToContext.
func RequestIDFromRequestContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(keyRequestID).(string)
	return v, ok
}

// ContextWithCustomHandlerForTest enriches ctx the way AddRequestInfoToContext does for custom-handler paths; for tests outside this package.
func ContextWithCustomHandlerForTest(ctx context.Context, requestID string) context.Context {
	ctx = context.WithValue(ctx, keyHandlerTypeCustom, HandlerTypeCustom)
	ctx = context.WithValue(ctx, keyRequestID, requestID)
	return ctx
}

// ContextWithCustomHandlerMarkerForTest sets only the custom-handler marker (no request ID key), for tests outside this package.
func ContextWithCustomHandlerMarkerForTest(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyHandlerTypeCustom, HandlerTypeCustom)
}

// GetMetadataFromRequest gets any custom values to be added to request metadata that helps nvkit middleware
func GetMetadataFromRequest(req *http.Request) metadata.MD {
	return metadata.Pairs(b3.TraceID, req.Header.Get(b3.TraceID),
		b3.SpanID, req.Header.Get(b3.SpanID),
		b3.ParentSpanID, req.Header.Get(b3.ParentSpanID),
		b3.Flags, req.Header.Get(b3.Flags),
		b3.Sampled, req.Header.Get(b3.Sampled),
		HeaderKeyRequestID, req.Header.Get(HeaderKeyRequestID),
		HeaderKeyAuditID, req.Header.Get(HeaderKeyAuditID),
		HeaderETag, req.Header.Get(HeaderETag),
		HeaderIfMatch, req.Header.Get(HeaderIfMatch))
}
