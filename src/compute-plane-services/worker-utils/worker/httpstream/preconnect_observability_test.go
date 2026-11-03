/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package httpstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestCallbackPreconnectObservability(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	originalProvider, originalPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		otel.SetTracerProvider(originalProvider)
		otel.SetTextMapPropagator(originalPropagator)
		_ = provider.Shutdown(context.Background())
	}()
	ctx, parent := provider.Tracer("synthetic").Start(t.Context(), "work")
	defer parent.End()
	traceparent := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent <- r.Header.Get("Traceparent")
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, tr := localCallbackClient(t, server, true, 0)
	h, err := NewRequestStreamHandler(ctx, ProxyClient(client), baseConfig(server.URL),
		&pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost, RequestId: "synthetic-trace-request", NcaId: "synthetic-org", RequestHeaders: []*pb.StringKV{{Key: "NVCF-FUNCTION-ID", Value: "synthetic-function"}, {Key: "Authorization", Value: "synthetic-secret-must-not-be-recorded"}}})
	require.NoError(t, err)
	defer h.Close()
	req, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	route, _ := tr.route(req)
	require.NoError(t, entryFor(t, tr.pool, route.key).err)
	require.NoError(t, h.SendResponse(ctx, bufferedResponse(), nil))
	require.Contains(t, <-traceparent, parent.SpanContext().TraceID().String())
	var prepared, handshake, claimed bool
	for _, span := range recorder.Ended() {
		require.Equal(t, parent.SpanContext().TraceID(), span.SpanContext().TraceID())
		require.NotContains(t, fmt.Sprint(span.Attributes()), "response-token")
		require.NotContains(t, fmt.Sprint(span.Attributes()), "synthetic-secret-must-not-be-recorded")
		if span.Name() == "worker.callback_preconnect" {
			prepared = true
			require.Equal(t, trace.SpanKindClient, span.SpanKind())
			values := map[string]string{}
			for _, value := range span.Attributes() {
				values[string(value.Key)] = value.Value.AsString()
			}
			require.Equal(t, "synthetic-org", values["nvcf.org.id"])
			require.NotContains(t, values, "nvcf.function.id", "request headers are not authoritative function identity")
			require.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID())
		}
		if span.Name() == "worker.callback_tls_handshake" {
			handshake = true
		}
		for _, event := range span.Events() {
			claimed = claimed || event.Name == "callback.prepared_connection.claimed"
		}
	}
	require.True(t, prepared)
	require.True(t, handshake)
	require.True(t, claimed)
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	var counters int
	for _, family := range families {
		if strings.HasPrefix(family.GetName(), "nvcf_worker_service_callback_preconnect") {
			for _, metric := range family.Metric {
				for _, label := range metric.Label {
					require.Contains(t, []string{"result", "le"}, label.GetName())
				}
			}
			if family.GetName() == "nvcf_worker_service_callback_preconnect_total" {
				counters = len(family.Metric)
			}
		}
	}
	require.Equal(t, 10, counters, "lifecycle result counters must be pre-initialized")
}

func TestCallbackPreconnectCancellationIsNotSetupFailure(t *testing.T) {
	failed := testutil.ToFloat64(preconnectResults.WithLabelValues("failed"))
	canceled := testutil.ToFloat64(preconnectResults.WithLabelValues("canceled"))
	_, finish := preconnectSpan(t.Context(), "synthetic-canceled-request")
	finish(context.Canceled)
	require.Equal(t, failed, testutil.ToFloat64(preconnectResults.WithLabelValues("failed")))
	require.Equal(t, canceled+1, testutil.ToFloat64(preconnectResults.WithLabelValues("canceled")))
}
