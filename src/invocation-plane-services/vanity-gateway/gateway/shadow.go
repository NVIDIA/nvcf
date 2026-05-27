/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"

	mw "ai-api-gateway-service/middleware"
)

const (
	shadowHeader      = "NVCF-Shadow"
	shadowHeaderValue = "true"
)

var errShadowConcurrencyLimit = errors.New("shadow concurrency limit reached")

type TrafficShadower struct {
	timeout       time.Duration
	maxConcurrent int
	sem           *semaphore.Weighted
}

func NewTrafficShadower(maxConcurrent int, timeout time.Duration) *TrafficShadower {
	return &TrafficShadower{
		timeout:       timeout,
		maxConcurrent: maxConcurrent,
		sem:           semaphore.NewWeighted(int64(maxConcurrent)),
	}
}

// Shadow dispatches the request to handler in a background goroutine with
// bounded concurrency and its own timeout. It returns nil when the request is
// admitted, or an error explaining why it was not dispatched. The caller
// controls the shadow's cancellation policy via the request's context: pass
// the original request context to tie the shadow to the primary request
// lifetime, or pass a detached context (e.g. context.WithoutCancel) to let
// it run independently.
func (s *TrafficShadower) Shadow(req *http.Request, handler http.Handler) error {
	if !s.sem.TryAcquire(1) {
		err := fmt.Errorf("%w: max_concurrent=%d", errShadowConcurrencyLimit, s.maxConcurrent)
		zap.L().Debug("shadow request dropped", append(mw.TraceFields(req.Context()), zap.Error(err))...)
		return err
	}

	ctx := req.Context()

	go func() {
		defer s.sem.Release(1)

		ctx, timeoutCancel := context.WithTimeout(ctx, s.timeout)
		defer timeoutCancel()

		handler.ServeHTTP(newDiscardResponseWriter(), req.WithContext(ctx))
	}()
	return nil
}

func isShadowRequest(req *http.Request) bool {
	return req.Header.Get(shadowHeader) == shadowHeaderValue
}

func setShadowSpanAttribute(span trace.Span, req *http.Request) {
	span.SetAttributes(traceAttrIsShadow.Bool(isShadowRequest(req)))
}

func newShadowRequest(req *http.Request, body []byte, ctx context.Context) *http.Request {
	shadowReq := req.Clone(ctx)
	shadowReq.Header.Set(shadowHeader, shadowHeaderValue)
	shadowReq.Body = io.NopCloser(bytes.NewReader(body))
	shadowReq.ContentLength = int64(len(body))
	return shadowReq
}

func shadowContext(req *http.Request, cancelOnClientDisconnect bool) (context.Context, func(error)) {
	// Detach request cancellation. Context values, including the active span,
	// stay available so the shadow span remains in the primary trace.
	ctx := context.WithoutCancel(req.Context())
	if !cancelOnClientDisconnect {
		return ctx, func(error) {}
	}

	ctx, cancel := context.WithCancel(ctx)
	stopCancelOnRequestDone := context.AfterFunc(req.Context(), cancel)
	return ctx, func(proxyErr error) {
		if proxyErr != nil {
			stopCancelOnRequestDone()
			cancel()
			return
		}
		if !stopCancelOnRequestDone() {
			cancel()
		}
	}
}

func recordShadowDispatchSummary(ctx context.Context, targetModels []string, dispatchedCount int, droppedCount int, droppedReasons []string, droppedTargetModels []string) {
	span := trace.SpanFromContext(ctx)
	attrs := []attribute.KeyValue{
		traceAttrShadowDispatched.Bool(dispatchedCount > 0),
		traceAttrShadowDispatchedCount.Int(dispatchedCount),
		traceAttrShadowDroppedCount.Int(droppedCount),
	}

	if len(targetModels) > 0 {
		attrs = append(attrs, traceAttrShadowTargetModels.StringSlice(targetModels))
	}
	if len(droppedReasons) > 0 {
		attrs = append(attrs, traceAttrShadowDroppedReasons.StringSlice(droppedReasons))
	}
	if len(droppedTargetModels) > 0 {
		attrs = append(attrs, traceAttrShadowDroppedTargetModels.StringSlice(droppedTargetModels))
	}
	span.SetAttributes(attrs...)
}

func shadowDroppedReason(err error) string {
	if errors.Is(err, errShadowConcurrencyLimit) {
		return shadowDroppedReasonConcurrencyLimit
	}
	return ""
}

func repeatedStrings(value string, count int) []string {
	if value == "" || count <= 0 {
		return nil
	}
	values := make([]string, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func newShadowReplayHandler(targetModel string, handler http.Handler) http.Handler {
	replay := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dw := newDiscardResponseWriter()
		handler.ServeHTTP(dw, r)
		w.WriteHeader(shadowReplayStatusCode(r.Context(), dw.statusCode))
	})

	return otelhttp.NewHandler(
		replay,
		shadowReplaySpanName,
		otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator()),
		otelhttp.WithSpanOptions(trace.WithAttributes(traceAttrShadowTargetModel.String(targetModel))),
	)
}

func shadowReplayStatusCode(ctx context.Context, statusCode int) int {
	if err := ctx.Err(); err != nil {
		span := trace.SpanFromContext(ctx)
		span.RecordError(err)
		if errors.Is(err, context.DeadlineExceeded) {
			return http.StatusGatewayTimeout
		}
	}
	return statusCode
}

type discardResponseWriter struct {
	header     http.Header
	statusCode int
}

func newDiscardResponseWriter() *discardResponseWriter {
	return &discardResponseWriter{
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (w *discardResponseWriter) Header() http.Header {
	return w.header
}

func (w *discardResponseWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func (w *discardResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *discardResponseWriter) Flush() {
	// Shadow replay responses are intentionally discarded, so Flush is a no-op.
}
