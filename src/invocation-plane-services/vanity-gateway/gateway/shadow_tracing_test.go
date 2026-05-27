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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestTracerProvider returns a tracer provider with an in-memory exporter
// for inspecting spans in tests. Call tp.ForceFlush to ensure all spans are
// exported before reading from the exporter.
func newTestTracerProvider() (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	return tp, exp
}

func spansByName(spans tracetest.SpanStubs, name string) []tracetest.SpanStub {
	var out []tracetest.SpanStub
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func spanAttr(s tracetest.SpanStub, key attribute.Key) (attribute.Value, bool) {
	for _, a := range s.Attributes {
		if a.Key == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestShadowSpanIsolation(t *testing.T) {
	// Verify that shadow replay gets its own span and its attributes
	// do not appear on the primary request span.
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	// Set up a vanity director that just records the call.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	vanity, err := NewVanityDirector(upstream.URL, http.DefaultTransport)
	require.NoError(t, err)

	shadower := NewTrafficShadower(10, 30*time.Second)

	modelMapping := map[string]FunctionInfo{
		"primary-model": {
			functionId:       "func-primary",
			shadowModelNames: []string{"shadow-model"},
			shadowPercentage: 100,
		},
		"shadow-model": {
			functionId: "func-shadow",
		},
	}

	director := &OpenAIDirector{
		shadower:       shadower,
		vanityDirector: vanity,
	}

	// Start a primary span so we can inspect it after dispatch.
	ctx, primarySpan := tracer.Start(context.Background(), "primary_request")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"primary-model"}`))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	resolved := resolvedOpenAIRequest{
		request: req,
		functionInfo: FunctionInfo{
			shadowModelNames:               []string{"shadow-model"},
			shadowPercentage:               100,
			shadowCancelOnClientDisconnect: true, // attached mode
		},
	}
	director.dispatchShadowIfNeeded(resolved, modelMapping)

	// Wait for shadow to complete.
	assert.Eventually(t, func() bool {
		tp.ForceFlush(context.Background())
		return len(spansByName(exp.GetSpans(), shadowReplaySpanName)) > 0
	}, 5*time.Second, 50*time.Millisecond)

	primarySpan.End()
	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()

	// Primary span should have aggregate shadow attributes but no per-target shadow model.
	primarySpans := spansByName(spans, "primary_request")
	require.Len(t, primarySpans, 1)
	dispatched, ok := spanAttr(primarySpans[0], traceAttrShadowDispatched)
	assert.True(t, ok)
	assert.True(t, dispatched.AsBool())
	_, hasShadowModel := spanAttr(primarySpans[0], traceAttrShadowTargetModel)
	assert.False(t, hasShadowModel)
	// Primary span should NOT have is_shadow=true (that's on the shadow span).
	isShadow, hasIsShadow := spanAttr(primarySpans[0], traceAttrIsShadow)
	if hasIsShadow {
		assert.False(t, isShadow.AsBool(), "primary span should not be marked as shadow")
	}

	// Shadow replay span should exist with its own attributes.
	shadowSpans := spansByName(spans, shadowReplaySpanName)
	require.Len(t, shadowSpans, 1)
	targetModel, ok := spanAttr(shadowSpans[0], traceAttrShadowTargetModel)
	assert.True(t, ok)
	assert.Equal(t, "shadow-model", targetModel.AsString())

	// Shadow replay should be a child of the primary (attached mode).
	assert.Equal(t, primarySpans[0].SpanContext.TraceID(), shadowSpans[0].SpanContext.TraceID(),
		"shadow span should share trace ID with primary")
	assert.Equal(t, primarySpans[0].SpanContext.SpanID(), shadowSpans[0].Parent.SpanID(),
		"shadow span should be a child of the primary span")
}

func TestDetachedShadowKeepsParentSpanContext(t *testing.T) {
	// When shadowCancelOnClientDisconnect=false, only cancellation is detached.
	// The shadow span still keeps the primary span as its parent.
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	vanity, err := NewVanityDirector(upstream.URL, http.DefaultTransport)
	require.NoError(t, err)

	shadower := NewTrafficShadower(10, 30*time.Second)

	modelMapping := map[string]FunctionInfo{
		"primary-model": {
			functionId:                     "func-primary",
			shadowModelNames:               []string{"shadow-model"},
			shadowPercentage:               100,
			shadowCancelOnClientDisconnect: false, // detached
		},
		"shadow-model": {
			functionId: "func-shadow",
		},
	}

	director := &OpenAIDirector{
		shadower:       shadower,
		vanityDirector: vanity,
	}

	ctx, primarySpan := tracer.Start(context.Background(), "primary_request")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"primary-model"}`))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	resolved := resolvedOpenAIRequest{
		request: req,
		functionInfo: FunctionInfo{
			shadowModelNames:               []string{"shadow-model"},
			shadowPercentage:               100,
			shadowCancelOnClientDisconnect: false,
		},
	}
	director.dispatchShadowIfNeeded(resolved, modelMapping)

	assert.Eventually(t, func() bool {
		tp.ForceFlush(context.Background())
		return len(spansByName(exp.GetSpans(), shadowReplaySpanName)) > 0
	}, 5*time.Second, 50*time.Millisecond)

	primarySpan.End()
	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	shadowSpans := spansByName(spans, shadowReplaySpanName)
	require.Len(t, shadowSpans, 1)

	primarySpans := spansByName(spans, "primary_request")
	require.Len(t, primarySpans, 1)
	assert.Equal(t, primarySpans[0].SpanContext.TraceID(), shadowSpans[0].SpanContext.TraceID(),
		"detached shadow should stay in the primary trace")
	assert.Equal(t, primarySpans[0].SpanContext.SpanID(), shadowSpans[0].Parent.SpanID(),
		"detached shadow should keep the primary span as parent")
	assert.Empty(t, shadowSpans[0].Links, "detached shadow should not add a span link")
}

func TestShadowSpanRecordsTimeoutError(t *testing.T) {
	// Verify that a shadow replay that times out gets codes.Error on its span.
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	// Upstream that blocks until context is cancelled (simulates slow stream).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	vanity, err := NewVanityDirector(upstream.URL, http.DefaultTransport)
	require.NoError(t, err)

	// Very short timeout so the shadow times out quickly.
	shadower := NewTrafficShadower(10, 200*time.Millisecond)

	modelMapping := map[string]FunctionInfo{
		"primary-model": {
			functionId:       "func-primary",
			shadowModelNames: []string{"shadow-model"},
			shadowPercentage: 100,
		},
		"shadow-model": {
			functionId: "func-shadow",
		},
	}

	director := &OpenAIDirector{
		shadower:       shadower,
		vanityDirector: vanity,
	}

	ctx, primarySpan := tracer.Start(context.Background(), "primary_request")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"primary-model"}`))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	resolved := resolvedOpenAIRequest{
		request: req,
		functionInfo: FunctionInfo{
			shadowModelNames: []string{"shadow-model"},
			shadowPercentage: 100,
		},
	}
	director.dispatchShadowIfNeeded(resolved, modelMapping)

	// Wait for shadow span to appear (after timeout fires).
	assert.Eventually(t, func() bool {
		tp.ForceFlush(context.Background())
		return len(spansByName(exp.GetSpans(), shadowReplaySpanName)) > 0
	}, 5*time.Second, 50*time.Millisecond)

	primarySpan.End()
	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	shadowSpans := spansByName(spans, shadowReplaySpanName)
	require.Len(t, shadowSpans, 1)

	// Shadow timeout is reported as 504 so otelhttp sets the span status.
	statusCode, ok := spanAttr(shadowSpans[0], traceAttrHTTPResponseStatusCode)
	assert.True(t, ok)
	assert.Equal(t, int64(http.StatusGatewayTimeout), statusCode.AsInt64())
	assert.Equal(t, codes.Error, shadowSpans[0].Status.Code,
		"timed-out shadow span should have Error status")

	// Should have recorded the error event.
	require.NotEmpty(t, shadowSpans[0].Events)
	foundErr := false
	for _, ev := range shadowSpans[0].Events {
		if ev.Name == "exception" {
			foundErr = true
		}
	}
	assert.True(t, foundErr, "shadow span should have a recorded error event")
}

func TestShadowSpanRecordsHTTPError(t *testing.T) {
	// Verify that a shadow replay returning 4xx/5xx gets codes.Error on its span.
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	// Upstream returns 503.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	vanity, err := NewVanityDirector(upstream.URL, http.DefaultTransport)
	require.NoError(t, err)

	shadower := NewTrafficShadower(10, 30*time.Second)

	modelMapping := map[string]FunctionInfo{
		"primary-model": {
			functionId:       "func-primary",
			shadowModelNames: []string{"shadow-model"},
			shadowPercentage: 100,
		},
		"shadow-model": {
			functionId: "func-shadow",
		},
	}

	director := &OpenAIDirector{
		shadower:       shadower,
		vanityDirector: vanity,
	}

	ctx, primarySpan := tracer.Start(context.Background(), "primary_request")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"primary-model"}`))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	resolved := resolvedOpenAIRequest{
		request: req,
		functionInfo: FunctionInfo{
			shadowModelNames: []string{"shadow-model"},
			shadowPercentage: 100,
		},
	}
	director.dispatchShadowIfNeeded(resolved, modelMapping)

	assert.Eventually(t, func() bool {
		tp.ForceFlush(context.Background())
		return len(spansByName(exp.GetSpans(), shadowReplaySpanName)) > 0
	}, 5*time.Second, 50*time.Millisecond)

	primarySpan.End()
	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	shadowSpans := spansByName(spans, shadowReplaySpanName)
	require.Len(t, shadowSpans, 1)

	// Should record the status code.
	statusCode, ok := spanAttr(shadowSpans[0], traceAttrHTTPResponseStatusCode)
	assert.True(t, ok)
	assert.Equal(t, int64(503), statusCode.AsInt64())

	// Should have error status.
	assert.Equal(t, codes.Error, shadowSpans[0].Status.Code)
}

func TestShadowAdmissionAttributes(t *testing.T) {
	// Verify primary span attributes differ based on admission vs drop.
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	vanity, err := NewVanityDirector(upstream.URL, http.DefaultTransport)
	require.NoError(t, err)

	modelMapping := map[string]FunctionInfo{
		"primary-model": {
			functionId:       "func-primary",
			shadowModelNames: []string{"shadow-model"},
			shadowPercentage: 100,
		},
		"shadow-model": {
			functionId: "func-shadow",
		},
	}

	t.Run("admitted", func(t *testing.T) {
		exp.Reset()
		shadower := NewTrafficShadower(10, 30*time.Second)
		director := &OpenAIDirector{shadower: shadower, vanityDirector: vanity}

		ctx, primarySpan := tracer.Start(context.Background(), "primary_request")
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"model":"primary-model"}`))
		req = req.WithContext(ctx)

		director.dispatchShadowIfNeeded(resolvedOpenAIRequest{
			request:      req,
			functionInfo: FunctionInfo{shadowModelNames: []string{"shadow-model"}, shadowPercentage: 100},
		}, modelMapping)

		assert.Eventually(t, func() bool {
			tp.ForceFlush(context.Background())
			return len(spansByName(exp.GetSpans(), shadowReplaySpanName)) > 0
		}, 5*time.Second, 50*time.Millisecond)

		primarySpan.End()
		tp.ForceFlush(context.Background())

		primarySpans := spansByName(exp.GetSpans(), "primary_request")
		require.Len(t, primarySpans, 1)

		dispatched, ok := spanAttr(primarySpans[0], traceAttrShadowDispatched)
		assert.True(t, ok)
		assert.True(t, dispatched.AsBool())
		_, hasDropReason := spanAttr(primarySpans[0], attribute.Key("shadow.dropped_reason"))
		assert.False(t, hasDropReason, "admitted shadow should not have dropped_reason")
		_, hasDroppedReasons := spanAttr(primarySpans[0], traceAttrShadowDroppedReasons)
		assert.False(t, hasDroppedReasons, "admitted shadow should not have dropped_reasons")
		_, hasDroppedTargetModels := spanAttr(primarySpans[0], traceAttrShadowDroppedTargetModels)
		assert.False(t, hasDroppedTargetModels, "admitted shadow should not have dropped_target_models")
	})

	t.Run("dropped", func(t *testing.T) {
		exp.Reset()
		// Capacity=1, fill it with a blocking handler.
		var started atomic.Bool
		done := make(chan struct{})
		blockingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started.Store(true)
			<-done
		})

		shadower := NewTrafficShadower(1, 30*time.Second)
		director := &OpenAIDirector{shadower: shadower, vanityDirector: vanity}

		// Fill the slot.
		fillReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
		fillReq = fillReq.WithContext(context.Background())
		require.NoError(t, shadower.Shadow(fillReq, blockingHandler))
		assert.Eventually(t, func() bool { return started.Load() }, 2*time.Second, 10*time.Millisecond)

		// Now dispatch a shadow that will be dropped.
		ctx, primarySpan := tracer.Start(context.Background(), "primary_dropped")
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"model":"primary-model"}`))
		req = req.WithContext(ctx)

		director.dispatchShadowIfNeeded(resolvedOpenAIRequest{
			request:      req,
			functionInfo: FunctionInfo{shadowModelNames: []string{"shadow-model"}, shadowPercentage: 100},
		}, modelMapping)

		primarySpan.End()
		close(done)
		tp.ForceFlush(context.Background())

		primarySpans := spansByName(exp.GetSpans(), "primary_dropped")
		require.Len(t, primarySpans, 1)

		dispatched, ok := spanAttr(primarySpans[0], traceAttrShadowDispatched)
		assert.True(t, ok)
		assert.False(t, dispatched.AsBool())

		_, hasSingleReason := spanAttr(primarySpans[0], attribute.Key("shadow.dropped_reason"))
		assert.False(t, hasSingleReason)

		reasons, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedReasons)
		assert.True(t, ok)
		assert.Equal(t, []string{shadowDroppedReasonConcurrencyLimit}, reasons.AsStringSlice())

		droppedTargetModels, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedTargetModels)
		assert.True(t, ok)
		assert.Equal(t, []string{"shadow-model"}, droppedTargetModels.AsStringSlice())
	})
}

func TestMultiShadowDispatchRecordsAggregatePrimarySpanAttributes(t *testing.T) {
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	vanity, err := NewVanityDirector(upstream.URL, http.DefaultTransport)
	require.NoError(t, err)

	modelMapping := map[string]FunctionInfo{
		"primary-model": {
			functionId:       "func-primary",
			shadowModelNames: []string{"shadow-a", "shadow-b"},
			shadowPercentage: 100,
		},
		"shadow-a": {functionId: "func-shadow-a"},
		"shadow-b": {functionId: "func-shadow-b"},
	}

	shadower := NewTrafficShadower(1, 30*time.Second)
	director := &OpenAIDirector{shadower: shadower, vanityDirector: vanity}

	ctx, primarySpan := tracer.Start(context.Background(), "primary_multi_shadow")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"primary-model"}`))
	req = req.WithContext(ctx)

	director.dispatchShadowIfNeeded(resolvedOpenAIRequest{
		request:      req,
		functionInfo: FunctionInfo{shadowModelNames: []string{"shadow-a", "shadow-b"}, shadowPercentage: 100},
	}, modelMapping)

	primarySpan.End()
	close(done)
	tp.ForceFlush(context.Background())

	primarySpans := spansByName(exp.GetSpans(), "primary_multi_shadow")
	require.Len(t, primarySpans, 1)

	dispatched, ok := spanAttr(primarySpans[0], traceAttrShadowDispatched)
	assert.True(t, ok)
	assert.True(t, dispatched.AsBool())

	dispatchedCount, ok := spanAttr(primarySpans[0], traceAttrShadowDispatchedCount)
	assert.True(t, ok)
	assert.Equal(t, int64(1), dispatchedCount.AsInt64())

	droppedCount, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedCount)
	assert.True(t, ok)
	assert.Equal(t, int64(1), droppedCount.AsInt64())

	targetModels, ok := spanAttr(primarySpans[0], traceAttrShadowTargetModels)
	assert.True(t, ok)
	assert.Equal(t, []string{"shadow-a", "shadow-b"}, targetModels.AsStringSlice())

	droppedReasons, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedReasons)
	assert.True(t, ok)
	assert.Equal(t, []string{shadowDroppedReasonConcurrencyLimit}, droppedReasons.AsStringSlice())

	droppedTargetModels, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedTargetModels)
	assert.True(t, ok)
	assert.Equal(t, []string{"shadow-b"}, droppedTargetModels.AsStringSlice())

	_, hasSingleTargetModel := spanAttr(primarySpans[0], traceAttrShadowTargetModel)
	assert.False(t, hasSingleTargetModel)

	_, hasSingleReason := spanAttr(primarySpans[0], attribute.Key("shadow.dropped_reason"))
	assert.False(t, hasSingleReason)
}

func TestMultiShadowDispatchRecordsAllDroppedReasonsAndTargets(t *testing.T) {
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	vanity, err := NewVanityDirector(upstream.URL, http.DefaultTransport)
	require.NoError(t, err)

	modelMapping := map[string]FunctionInfo{
		"primary-model": {
			functionId:       "func-primary",
			shadowModelNames: []string{"shadow-a", "shadow-b"},
			shadowPercentage: 100,
		},
		"shadow-a": {functionId: "func-shadow-a"},
		"shadow-b": {functionId: "func-shadow-b"},
	}

	var started atomic.Bool
	done := make(chan struct{})
	blockingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Store(true)
		<-done
	})

	shadower := NewTrafficShadower(1, 30*time.Second)
	director := &OpenAIDirector{shadower: shadower, vanityDirector: vanity}

	fillReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
	fillReq = fillReq.WithContext(context.Background())
	require.NoError(t, shadower.Shadow(fillReq, blockingHandler))
	assert.Eventually(t, func() bool { return started.Load() }, 2*time.Second, 10*time.Millisecond)

	ctx, primarySpan := tracer.Start(context.Background(), "primary_multi_shadow_all_dropped")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"primary-model"}`))
	req = req.WithContext(ctx)

	director.dispatchShadowIfNeeded(resolvedOpenAIRequest{
		request:      req,
		functionInfo: FunctionInfo{shadowModelNames: []string{"shadow-a", "shadow-b"}, shadowPercentage: 100},
	}, modelMapping)

	primarySpan.End()
	close(done)
	tp.ForceFlush(context.Background())

	primarySpans := spansByName(exp.GetSpans(), "primary_multi_shadow_all_dropped")
	require.Len(t, primarySpans, 1)

	dispatched, ok := spanAttr(primarySpans[0], traceAttrShadowDispatched)
	assert.True(t, ok)
	assert.False(t, dispatched.AsBool())

	dispatchedCount, ok := spanAttr(primarySpans[0], traceAttrShadowDispatchedCount)
	assert.True(t, ok)
	assert.Equal(t, int64(0), dispatchedCount.AsInt64())

	droppedCount, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedCount)
	assert.True(t, ok)
	assert.Equal(t, int64(2), droppedCount.AsInt64())

	droppedReasons, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedReasons)
	assert.True(t, ok)
	assert.Equal(t,
		[]string{shadowDroppedReasonConcurrencyLimit, shadowDroppedReasonConcurrencyLimit},
		droppedReasons.AsStringSlice(),
	)

	droppedTargetModels, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedTargetModels)
	assert.True(t, ok)
	assert.Equal(t, []string{"shadow-a", "shadow-b"}, droppedTargetModels.AsStringSlice())

	_, hasSingleReason := spanAttr(primarySpans[0], attribute.Key("shadow.dropped_reason"))
	assert.False(t, hasSingleReason)
}

func TestRecordShadowDispatchSummaryPreservesDroppedReasonTargetAlignment(t *testing.T) {
	tp, exp := newTestTracerProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")
	ctx, primarySpan := tracer.Start(context.Background(), "primary_mixed_drop_reasons")

	recordShadowDispatchSummary(
		ctx,
		[]string{"shadow-a", "shadow-b"},
		0,
		2,
		[]string{shadowDroppedReasonBodyReadError, shadowDroppedReasonConcurrencyLimit},
		[]string{"shadow-a", "shadow-b"},
	)

	primarySpan.End()
	tp.ForceFlush(context.Background())

	primarySpans := spansByName(exp.GetSpans(), "primary_mixed_drop_reasons")
	require.Len(t, primarySpans, 1)

	droppedReasons, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedReasons)
	assert.True(t, ok)
	assert.Equal(t,
		[]string{shadowDroppedReasonBodyReadError, shadowDroppedReasonConcurrencyLimit},
		droppedReasons.AsStringSlice(),
	)

	droppedTargetModels, ok := spanAttr(primarySpans[0], traceAttrShadowDroppedTargetModels)
	assert.True(t, ok)
	assert.Equal(t, []string{"shadow-a", "shadow-b"}, droppedTargetModels.AsStringSlice())
}
