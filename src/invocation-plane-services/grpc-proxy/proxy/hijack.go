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
package proxy

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/quic-go/quic-go/http3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"net"
	"net/http"
	"nvcf-grpc-proxy/proxy/quicconn"
	"strings"
)

func (s *StreamDirector) HijackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := otel.GetTextMapPropagator().Extract(context.WithoutCancel(r.Context()), propagation.HeaderCarrier(r.Header))
	tracer := otel.GetTracerProvider().Tracer("proxy-tracer")
	ctx, span := tracer.Start(ctx, "CONNECT /v1/proxy",
		trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.NetworkProtocolVersion(r.Proto)))
	defer span.End()

	http1Hijacker, _ := w.(http.Hijacker)
	http3Hijacker, _ := w.(http3.HTTPStreamer)
	if http1Hijacker == nil && http3Hijacker == nil {
		err := fmt.Errorf("HTTP CONNECT request is not hijack-able")
		err = spanError(span, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	auth := r.Header.Get("Authorization")
	auth = strings.TrimPrefix(auth, "Bearer ")
	if auth == "" {
		err := fmt.Errorf("HTTP CONNECT request is missing auth")
		err = spanError(span, err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	requestId := r.Header.Get("X-Request-ID")
	if requestId == "" {
		err := fmt.Errorf("HTTP CONNECT request is missing request id")
		err = spanError(span, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parsedRequestId, err := uuid.Parse(requestId)
	if err != nil {
		err := fmt.Errorf("HTTP CONNECT request has an invalid request id")
		err = spanError(span, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	span.SetAttributes(attribute.Stringer("request_id", parsedRequestId))

	authLookup := s.workerAuth.Get(auth)
	if authLookup == nil || authLookup.Value().requestId != parsedRequestId {
		err := fmt.Errorf("HTTP CONNECT request is missing valid auth")
		err = spanError(span, err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	authInfo := authLookup.Value()

	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	var conn net.Conn
	if http3Hijacker != nil {
		conn = quicconn.NewQuicStreamConn(http3Hijacker)
	} else {
		conn, _, err = http1Hijacker.Hijack()
		if err != nil {
			err = spanError(span, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	err = s.RegisterWorker(parsedRequestId, auth, authInfo.functionId, authInfo.functionVersionId, conn)
	if err != nil {
		zap.L().Error("failed to register worker", zap.Error(err))
		_ = spanError(span, err)
		_ = conn.Close()
	}

	// workers can't reconnect to the client if their connection drops because the client has
	// already been sent away, so no point in keeping the auth around.
	// also need to make sure we don't allow reconnects to guard against replay attacks with 0-rtt.
	s.workerAuth.Delete(auth)
}
