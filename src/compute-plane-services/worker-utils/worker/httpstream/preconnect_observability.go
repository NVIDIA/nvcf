/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package httpstream

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http/httptrace"
	"time"

	workerMetrics "github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var preconnectResults = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: workerMetrics.NvcfRootNamespace,
	Name:      "callback_preconnect_total",
	Help:      "Callback connection preparation lifecycle results; no route or request labels.",
}, []string{"result"})

var preconnectDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: workerMetrics.NvcfRootNamespace,
	Name:      "callback_preconnect_setup_duration_seconds",
	Help:      "Speculative first-hop connection setup duration.",
	Buckets:   prometheus.DefBuckets,
}, []string{"result"})

func init() {
	for _, result := range []string{"started", "coalesced", "cooldown", "capacity", "ready", "failed", "claimed", "expired", "discarded", "canceled"} {
		preconnectResults.WithLabelValues(result).Add(0)
	}
	for _, result := range []string{"ready", "failed", "canceled"} {
		preconnectDuration.WithLabelValues(result)
	}
}

func preconnectResult(result string) { preconnectResults.WithLabelValues(result).Inc() }

type callbackTraceAttributesKey struct{}

func callbackTraceAttributes(request *pb.WorkerInvokeFunctionRequest) []attribute.KeyValue {
	var attributes []attribute.KeyValue
	if id := request.GetNcaId(); id != "" {
		attributes = append(attributes, attribute.String("nvcf.org.id", id))
	}
	return attributes
}

func preconnectSpan(ctx context.Context, requestID string) (context.Context, func(error)) {
	start := time.Now()
	attributes, _ := ctx.Value(callbackTraceAttributesKey{}).([]attribute.KeyValue)
	ctx, span := otel.Tracer("nvcf.worker.httpstream").Start(ctx, "worker.callback_preconnect",
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attributes...))
	span.SetAttributes(attribute.String("nvcf.request.id", requestID))
	ctx = httptrace.WithClientTrace(ctx, otelhttptrace.NewClientTrace(ctx))
	return ctx, func(err error) {
		result := "ready"
		if errors.Is(err, context.Canceled) {
			result = "canceled"
		} else if err != nil {
			result = "failed"
			span.SetStatus(codes.Error, "callback connection preparation failed")
			span.SetAttributes(attribute.Bool("error", true))
		}
		preconnectResult(result)
		preconnectDuration.WithLabelValues(result).Observe(time.Since(start).Seconds())
		span.SetAttributes(attribute.String("preconnect.result", result))
		span.End()
		fields := []zap.Field{zap.String("request_id", requestID), zap.String("result", result)}
		for _, attribute := range attributes {
			fields = append(fields, zap.String(string(attribute.Key), attribute.Value.AsString()))
		}
		zap.L().Debug("callback connection preparation finished", fields...)
	}
}

// net/http checks HandshakeContext again after a custom TLS dialer returns.
// This explicit span measures the actual handshake, including ordinary fallback
// dials, rather than that subsequent already-completed handshake check.
func callbackTLSHandshake(ctx context.Context, conn *tls.Conn) error {
	attributes, _ := ctx.Value(callbackTraceAttributesKey{}).([]attribute.KeyValue)
	ctx, span := otel.Tracer("nvcf.worker.httpstream").Start(ctx, "worker.callback_tls_handshake",
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attributes...))
	defer span.End()
	err := conn.HandshakeContext(ctx)
	if err != nil {
		span.SetStatus(codes.Error, "callback TLS handshake failed")
		span.SetAttributes(attribute.Bool("error", true))
	}
	return err
}

func callbackConnectionClaimed(ctx context.Context) {
	trace.SpanFromContext(ctx).AddEvent("callback.prepared_connection.claimed")
}
