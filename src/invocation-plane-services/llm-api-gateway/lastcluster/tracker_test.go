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

package lastcluster

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// fakeStore records calls and can inject errors or block Get while ignoring
// the context, like a store that does not honor cancellation.
type fakeStore struct {
	mu       sync.Mutex
	values   map[string]string
	gets     int
	sets     int
	getErr   error
	setErr   error
	blockGet chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: map[string]string{}}
}

func (s *fakeStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	s.gets++
	block, err := s.blockGet, s.getErr
	value, ok := s.values[key]
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return "", false, err
	}
	return value, ok, nil
}

func (s *fakeStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func (s *fakeStore) counts() (gets, sets int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.sets
}

func (s *fakeStore) value(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func sessionCtx(source string) *requestctx.RequestContext {
	return &requestctx.RequestContext{
		RequestID:        "req-1",
		RoutingKey:       "fn-alpha",
		Model:            "company/model",
		SessionID:        "raw-session-secret",
		SessionSource:    source,
		CacheAffinityKey: "mt:v1:session:affinity-secret",
	}
}

func newTestTracker(store Store) *Tracker {
	return NewTracker(store, Options{TTL: time.Minute, LookupTimeout: 20 * time.Millisecond})
}

func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldTracer := telemetry.Tracer
	telemetry.Tracer = sync.OnceValue(func() trace.Tracer { return provider.Tracer("test") })
	t.Cleanup(func() {
		telemetry.Tracer = oldTracer
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}

func installMetricReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldProvider)
		_ = provider.Shutdown(context.Background())
	})
	return reader
}

func counterValues(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				value, _ := dp.Attributes.Value("result")
				out[value.AsString()] = dp.Value
			}
		}
	}
	return out
}

func spanByName(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("span %q not recorded", name)
	return nil
}

func spanResult(span sdktrace.ReadOnlySpan) string {
	for _, attr := range span.Attributes() {
		if attr.Key == attribute.Key("result") {
			return attr.Value.AsString()
		}
	}
	return ""
}

func TestTrackerRoundTripForEligibleSources(t *testing.T) {
	for _, source := range []string{
		requestctx.SessionSourcePromptCacheKey,
		requestctx.SessionSourceConversationID,
		requestctx.SessionSourceHeader,
	} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			tracker := newTestTracker(NewLocalStore(10))
			reqCtx := sessionCtx(source)

			key, clusterID := tracker.Lookup(ctx, reqCtx, reqCtx.Model)
			if key == "" || clusterID != "" {
				t.Fatalf("first Lookup() = %q, %q; want key and no cluster", key, clusterID)
			}
			tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-b")
			tracker.Drain(ctx)

			_, clusterID = tracker.Lookup(ctx, reqCtx, reqCtx.Model)
			if clusterID != "cluster-b" {
				t.Fatalf("second Lookup() cluster = %q, want cluster-b", clusterID)
			}
		})
	}
}

func TestTrackerSkipsIneligibleSessions(t *testing.T) {
	reader := installMetricReader(t)
	ctx := context.Background()
	store := newFakeStore()
	tracker := newTestTracker(store)

	for _, source := range []string{
		requestctx.SessionSourcePayload,
		requestctx.SessionSourceClaudeCodeHeader,
		"unknown",
	} {
		key, clusterID := tracker.Lookup(ctx, sessionCtx(source), "company/model")
		if key != "" || clusterID != "" {
			t.Fatalf("Lookup(%s) = %q, %q; want skipped", source, key, clusterID)
		}
		tracker.Remember(ctx, sessionCtx(source), "company/model", key, http.StatusOK, "cluster-b")
	}
	if key, _ := tracker.Lookup(ctx, sessionCtx(requestctx.SessionSourceHeader), ""); key != "" {
		t.Fatal("Lookup() with empty model must be skipped")
	}
	// No session at all is not counted.
	if key, _ := tracker.Lookup(ctx, &requestctx.RequestContext{RoutingKey: "fn"}, "m"); key != "" {
		t.Fatal("Lookup() without a session must return no key")
	}
	tracker.Drain(ctx)

	if gets, sets := store.counts(); gets != 0 || sets != 0 {
		t.Fatalf("store gets=%d sets=%d, want 0 and 0", gets, sets)
	}
	lookups := counterValues(t, reader, "llm_api_gateway_last_cluster_lookups_total")
	if lookups["skipped"] != 4 || len(lookups) != 1 {
		t.Fatalf("lookups = %v, want only skipped=4", lookups)
	}
}

func TestTrackerNilIsNoop(t *testing.T) {
	var tracker *Tracker
	ctx := context.Background()
	if key, clusterID := tracker.Lookup(ctx, sessionCtx(requestctx.SessionSourceHeader), "m"); key != "" || clusterID != "" {
		t.Fatal("nil Tracker Lookup() must return empty values")
	}
	tracker.Remember(ctx, nil, "m", "k", http.StatusOK, "B")
	tracker.Drain(ctx)
}

func TestTrackerRememberOnlyStoresValidSuccessfulResponses(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)
	key, _ := tracker.Lookup(ctx, reqCtx, reqCtx.Model)

	tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-a")
	tracker.Drain(ctx)
	for _, tc := range []struct {
		status    int
		clusterID string
	}{
		{status: http.StatusServiceUnavailable, clusterID: "cluster-c"},
		{status: http.StatusTooManyRequests, clusterID: "cluster-c"},
		{status: http.StatusMultipleChoices, clusterID: "cluster-c"},
		{status: http.StatusOK, clusterID: ""},
		{status: http.StatusOK, clusterID: "   "},
		{status: http.StatusOK, clusterID: strings.Repeat("c", MaxValueBytes+1)},
		{status: http.StatusOK, clusterID: "bad\x01value"},
	} {
		tracker.Remember(ctx, reqCtx, reqCtx.Model, key, tc.status, tc.clusterID)
	}
	tracker.Drain(ctx)

	if got := store.value(key); got != "cluster-a" {
		t.Fatalf("stored value = %q, want cluster-a", got)
	}
	if _, sets := store.counts(); sets != 1 {
		t.Fatalf("store sets = %d, want 1", sets)
	}

	tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusCreated, "  cluster-b  ")
	tracker.Drain(ctx)
	if got := store.value(key); got != "cluster-b" {
		t.Fatalf("stored value = %q, want trimmed cluster-b", got)
	}
}

func TestTrackerTreatsInvalidStoredValueAsMiss(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)
	key := Key(reqCtx.RoutingKey, reqCtx.Model, reqCtx.CacheAffinityKey)
	store.values[key] = "bad\nvalue"

	if _, clusterID := tracker.Lookup(ctx, reqCtx, reqCtx.Model); clusterID != "" {
		t.Fatalf("Lookup() cluster = %q, want miss for an invalid stored value", clusterID)
	}
}

func TestLookupTimeout(t *testing.T) {
	recorder := installSpanRecorder(t)
	reader := installMetricReader(t)
	store := newFakeStore()
	store.blockGet = make(chan struct{})
	t.Cleanup(func() { close(store.blockGet) })
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourcePromptCacheKey)

	start := time.Now()
	key, clusterID := tracker.Lookup(context.Background(), reqCtx, reqCtx.Model)
	elapsed := time.Since(start)

	if elapsed >= 25*time.Millisecond {
		t.Fatalf("Lookup() took %s, want < timeout + 5ms", elapsed)
	}
	if key == "" || clusterID != "" {
		t.Fatalf("Lookup() = %q, %q; want key and no cluster", key, clusterID)
	}
	span := spanByName(t, recorder, "llm-api-gateway.last_cluster_lookup")
	if spanResult(span) != "timeout" || span.Status().Code != codes.Error {
		t.Fatalf("span result=%q status=%v, want timeout and Error", spanResult(span), span.Status().Code)
	}
	if got := counterValues(t, reader, "llm_api_gateway_last_cluster_lookups_total"); got["timeout"] != 1 {
		t.Fatalf("lookups = %v, want timeout=1", got)
	}
}

func TestLookupCancelledRequestIsNotAStoreFailure(t *testing.T) {
	recorder := installSpanRecorder(t)
	reader := installMetricReader(t)
	var buf syncBuffer
	store := newFakeStore()
	store.blockGet = make(chan struct{})
	t.Cleanup(func() { close(store.blockGet) })
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)
	ctx, cancel := context.WithCancel(zerolog.New(&buf).WithContext(context.Background()))
	time.AfterFunc(time.Millisecond, cancel)

	key, clusterID := tracker.Lookup(ctx, reqCtx, reqCtx.Model)
	if key == "" || clusterID != "" {
		t.Fatalf("Lookup() = %q, %q; want key and no cluster", key, clusterID)
	}
	span := spanByName(t, recorder, "llm-api-gateway.last_cluster_lookup")
	if spanResult(span) != "timeout" || span.Status().Code == codes.Error {
		t.Fatalf("span result=%q status=%v, want timeout without error status", spanResult(span), span.Status().Code)
	}
	if got := counterValues(t, reader, "llm_api_gateway_last_cluster_lookups_total"); got["timeout"] != 1 || got["error"] != 0 {
		t.Fatalf("lookups = %v, want timeout=1 and no error", got)
	}
	if logs := buf.String(); strings.Contains(logs, `"level":"warn"`) {
		t.Fatalf("logs = %s, want no warn for a cancelled request", logs)
	}
}

func TestTrackerSpansCarryFunctionID(t *testing.T) {
	recorder := installSpanRecorder(t)
	ctx := context.Background()
	tracker := newTestTracker(newFakeStore())
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)

	key, _ := tracker.Lookup(ctx, reqCtx, reqCtx.Model)
	tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-b")
	tracker.Drain(ctx)

	for _, name := range []string{"llm-api-gateway.last_cluster_lookup", "llm-api-gateway.last_cluster_write"} {
		span := spanByName(t, recorder, name)
		found := false
		for _, attr := range span.Attributes() {
			if attr.Key == "nvcf.function.id" && attr.Value.AsString() == reqCtx.RoutingKey {
				found = true
			}
		}
		if !found {
			t.Fatalf("span %s attributes = %v, want nvcf.function.id=%s", name, span.Attributes(), reqCtx.RoutingKey)
		}
	}
}

func TestTrackerWriteLogUsesLookupModel(t *testing.T) {
	var buf syncBuffer
	ctx := zerolog.New(&buf).WithContext(context.Background())
	store := newFakeStore()
	store.setErr = errors.New("olric unavailable")
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)

	key, _ := tracker.Lookup(ctx, reqCtx, "company/effective-model")
	tracker.Remember(ctx, reqCtx, "company/effective-model", key, http.StatusOK, "cluster-b")
	tracker.Drain(ctx)

	if logs := buf.String(); !strings.Contains(logs, `"model":"company/effective-model"`) {
		t.Fatalf("logs = %s, want the model used for the key", logs)
	}
}

func TestTrackerSpansAndMetrics(t *testing.T) {
	recorder := installSpanRecorder(t)
	reader := installMetricReader(t)
	ctx := context.Background()
	store := newFakeStore()
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)

	key, _ := tracker.Lookup(ctx, reqCtx, reqCtx.Model) // miss
	tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-b")
	tracker.Drain(ctx)
	tracker.Lookup(ctx, reqCtx, reqCtx.Model) // hit

	store.mu.Lock()
	store.getErr = errors.New("olric unavailable")
	store.setErr = errors.New("olric unavailable")
	store.mu.Unlock()
	tracker.Lookup(ctx, reqCtx, reqCtx.Model) // error
	tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-c")
	tracker.Drain(ctx)

	var lookups, writes []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case "llm-api-gateway.last_cluster_lookup":
			lookups = append(lookups, span)
		case "llm-api-gateway.last_cluster_write":
			writes = append(writes, span)
		}
	}
	if len(lookups) != 3 || len(writes) != 2 {
		t.Fatalf("spans lookups=%d writes=%d, want 3 and 2", len(lookups), len(writes))
	}
	for i, want := range []struct {
		result string
		code   codes.Code
	}{{"miss", codes.Unset}, {"hit", codes.Unset}, {"error", codes.Error}} {
		if got := spanResult(lookups[i]); got != want.result || lookups[i].Status().Code != want.code {
			t.Fatalf("lookup span %d result=%q status=%v, want %q %v", i, got, lookups[i].Status().Code, want.result, want.code)
		}
	}
	for i, want := range []struct {
		result string
		code   codes.Code
	}{{"ok", codes.Unset}, {"error", codes.Error}} {
		if got := spanResult(writes[i]); got != want.result || writes[i].Status().Code != want.code {
			t.Fatalf("write span %d result=%q status=%v, want %q %v", i, got, writes[i].Status().Code, want.result, want.code)
		}
	}

	gotLookups := counterValues(t, reader, "llm_api_gateway_last_cluster_lookups_total")
	if gotLookups["miss"] != 1 || gotLookups["hit"] != 1 || gotLookups["error"] != 1 {
		t.Fatalf("lookups = %v, want miss=1 hit=1 error=1", gotLookups)
	}
	gotWrites := counterValues(t, reader, "llm_api_gateway_last_cluster_writes_total")
	if gotWrites["ok"] != 1 || gotWrites["error"] != 1 {
		t.Fatalf("writes = %v, want ok=1 error=1", gotWrites)
	}
}

func TestTrackerStoreErrorsLogWarnWithoutSessionIdentifiers(t *testing.T) {
	var buf syncBuffer
	ctx := zerolog.New(&buf).WithContext(context.Background())
	store := newFakeStore()
	store.getErr = errors.New("olric unavailable")
	store.setErr = errors.New("olric unavailable")
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)

	key, clusterID := tracker.Lookup(ctx, reqCtx, reqCtx.Model)
	if key == "" || clusterID != "" {
		t.Fatalf("Lookup() = %q, %q; want key and no cluster on error", key, clusterID)
	}
	tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-b")
	tracker.Drain(ctx)

	logs := buf.String()
	if strings.Count(logs, `"level":"warn"`) != 2 {
		t.Fatalf("logs = %s, want two warn lines", logs)
	}
	for _, want := range []string{`"request_id":"req-1"`, `"model":"company/model"`, `"routing_key":"fn-alpha"`} {
		if strings.Count(logs, want) != 2 {
			t.Fatalf("logs = %s, want %s on both lines", logs, want)
		}
	}
	for _, secret := range []string{reqCtx.CacheAffinityKey, reqCtx.SessionID, "affinity-secret", key} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs leak %q: %s", secret, logs)
		}
	}
}

func TestTrackerWriteSurvivesRequestCancellation(t *testing.T) {
	store := newFakeStore()
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)
	ctx, cancel := context.WithCancel(context.Background())
	key, _ := tracker.Lookup(ctx, reqCtx, reqCtx.Model)

	tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-b")
	cancel()
	tracker.Drain(context.Background())

	if got := store.value(key); got != "cluster-b" {
		t.Fatalf("stored value = %q, want cluster-b", got)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
