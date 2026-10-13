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
	"runtime"
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

const (
	lookupSpan    = "llm-api-gateway.last_cluster_lookup"
	writeSpan     = "llm-api-gateway.last_cluster_write"
	lookupsMetric = "llm_api_gateway_last_cluster_lookups_total"
	writesMetric  = "llm_api_gateway_last_cluster_writes_total"
)

// fakeStore can inject errors or block calls while ignoring the context,
// like a store that does not honor cancellation.
type fakeStore struct {
	mu       sync.Mutex
	values   map[string]string
	gets     int
	sets     int
	getErr   error
	setErr   error
	blockGet chan struct{}
	blockSet chan struct{}
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
	block := s.blockSet
	s.mu.Unlock()
	if block != nil {
		<-block
	}
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

func inflightWrites(t *Tracker) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writes
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

// counterValues returns the counter's values keyed by its result label.
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

// assertSpan checks the named span's result, status, and function ID.
func assertSpan(t *testing.T, recorder *tracetest.SpanRecorder, name, wantResult string, wantStatus codes.Code) {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() != name {
			continue
		}
		attrs := attribute.NewSet(span.Attributes()...)
		result, _ := attrs.Value("result")
		functionID, _ := attrs.Value("nvcf.function.id")
		if result.AsString() != wantResult || span.Status().Code != wantStatus || functionID.AsString() != "fn-alpha" {
			t.Fatalf("span %s attributes=%v status=%v, want result=%s status=%v nvcf.function.id=fn-alpha",
				name, span.Attributes(), span.Status().Code, wantResult, wantStatus)
		}
		return
	}
	t.Fatalf("span %q not recorded", name)
}

func TestLookupOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stored     string
		getErr     error
		block      bool
		cancel     bool
		wantID     string
		wantResult string
		wantStatus codes.Code
	}{
		{name: "hit", stored: "cluster-b", wantID: "cluster-b", wantResult: "hit"},
		{name: "miss", wantResult: "miss"},
		{name: "invalid stored value", stored: "bad\nvalue", wantResult: "miss"},
		{name: "error", getErr: errors.New("olric unavailable"), wantResult: "error", wantStatus: codes.Error},
		{name: "timeout", block: true, wantResult: "timeout", wantStatus: codes.Error},
		// A cancelled request (client gone) is not a store failure.
		{name: "cancelled request", block: true, cancel: true, wantResult: "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := installSpanRecorder(t)
			reader := installMetricReader(t)
			var logs syncBuffer
			ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(context.Background()))
			defer cancel()
			if tc.cancel {
				cancel()
			}
			store := newFakeStore()
			store.getErr = tc.getErr
			if tc.block {
				store.blockGet = make(chan struct{})
				t.Cleanup(func() { close(store.blockGet) })
			}
			reqCtx := sessionCtx(requestctx.SessionSourceHeader)
			if tc.stored != "" {
				store.values[Key(reqCtx.RoutingKey, reqCtx.Model, reqCtx.CacheAffinityKey)] = tc.stored
			}

			start := time.Now()
			key, clusterID := newTestTracker(store).Lookup(ctx, reqCtx, reqCtx.Model)
			// A blocked store never returns before cleanup, so any bound well
			// under that proves Lookup did not wait for it. The slack keeps
			// loaded -race runs stable.
			if elapsed := time.Since(start); elapsed >= 200*time.Millisecond {
				t.Fatalf("Lookup() took %s, want < 200ms", elapsed)
			}
			if key == "" || clusterID != tc.wantID {
				t.Fatalf("Lookup() = %q, %q; want a key and %q", key, clusterID, tc.wantID)
			}
			assertSpan(t, recorder, lookupSpan, tc.wantResult, tc.wantStatus)
			if got := counterValues(t, reader, lookupsMetric); got[tc.wantResult] != 1 || len(got) != 1 {
				t.Fatalf("lookups = %v, want only %s=1", got, tc.wantResult)
			}
			// Failures warn (timeouts sampled); see TestStoreErrorLogsOmitSessionIdentifiers.
			if tc.wantStatus != codes.Error && strings.Contains(logs.String(), `"level":"warn"`) {
				t.Fatalf("logs = %s, want no warn", logs.String())
			}
		})
	}
}

func TestLookupSkipsIneligibleSessions(t *testing.T) {
	reader := installMetricReader(t)
	store := newFakeStore()
	tracker := newTestTracker(store)
	header := sessionCtx(requestctx.SessionSourceHeader)

	for _, tc := range []struct {
		reqCtx *requestctx.RequestContext
		model  string
	}{
		{sessionCtx(requestctx.SessionSourcePayload), "m"},
		{sessionCtx("unknown"), "m"},
		{header, ""},
		// No session at all is not counted as skipped.
		{&requestctx.RequestContext{RoutingKey: "fn"}, "m"},
	} {
		if key, clusterID := tracker.Lookup(context.Background(), tc.reqCtx, tc.model); key != "" || clusterID != "" {
			t.Fatalf("Lookup(%+v, %q) = %q, %q; want skipped", tc.reqCtx, tc.model, key, clusterID)
		}
	}
	if gets, _ := store.counts(); gets != 0 {
		t.Fatalf("store gets = %d, want 0", gets)
	}
	if got := counterValues(t, reader, lookupsMetric); got["skipped"] != 3 || len(got) != 1 {
		t.Fatalf("lookups = %v, want only skipped=3", got)
	}
}

func TestWriteOutcomes(t *testing.T) {
	for _, tc := range []struct {
		setErr     error
		wantResult string
		wantStatus codes.Code
	}{
		{wantResult: "ok"},
		{setErr: errors.New("olric unavailable"), wantResult: "error", wantStatus: codes.Error},
	} {
		t.Run(tc.wantResult, func(t *testing.T) {
			recorder := installSpanRecorder(t)
			reader := installMetricReader(t)
			ctx := context.Background()
			store := newFakeStore()
			store.setErr = tc.setErr
			tracker := newTestTracker(store)

			tracker.Remember(ctx, sessionCtx(requestctx.SessionSourceHeader), "m", "k", http.StatusOK, "cluster-b")
			tracker.Drain(ctx)

			assertSpan(t, recorder, writeSpan, tc.wantResult, tc.wantStatus)
			if got := counterValues(t, reader, writesMetric); got[tc.wantResult] != 1 || len(got) != 1 {
				t.Fatalf("writes = %v, want only %s=1", got, tc.wantResult)
			}
		})
	}
}

func TestRememberStoresOnlyValidSuccessfulResponses(t *testing.T) {
	store := newFakeStore()
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)
	// The write outlives the request, so a cancelled request still records.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		status    int
		clusterID string
	}{
		{http.StatusMultipleChoices, "cluster-c"},
		{http.StatusOK, "   "},
		{http.StatusOK, "bad\x01value"},
		{http.StatusCreated, "  cluster-b  "},
	} {
		tracker.Remember(ctx, reqCtx, reqCtx.Model, "k", tc.status, tc.clusterID)
	}
	tracker.Drain(context.Background())

	if _, sets := store.counts(); sets != 1 || store.value("k") != "cluster-b" {
		t.Fatalf("sets = %d, stored = %q; want one write of trimmed cluster-b", sets, store.value("k"))
	}
}

func TestStoreErrorLogsOmitSessionIdentifiers(t *testing.T) {
	var logs syncBuffer
	ctx := zerolog.New(&logs).WithContext(context.Background())
	store := newFakeStore()
	store.getErr = errors.New("olric unavailable")
	store.setErr = store.getErr
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)

	// The logged model is the one used for the key, not reqCtx.Model.
	key, _ := tracker.Lookup(ctx, reqCtx, "company/effective-model")
	tracker.Remember(ctx, reqCtx, "company/effective-model", key, http.StatusOK, "cluster-b")
	tracker.Drain(ctx)

	out := logs.String()
	for _, want := range []string{
		`"level":"warn"`, `"request_id":"req-1"`, `"model":"company/effective-model"`, `"routing_key":"fn-alpha"`,
	} {
		if strings.Count(out, want) != 2 {
			t.Fatalf("logs = %s, want %s on both the lookup and the write line", out, want)
		}
	}
	for _, secret := range []string{"affinity-secret", reqCtx.SessionID, key} {
		if strings.Contains(out, secret) {
			t.Fatalf("logs leak %q: %s", secret, out)
		}
	}
}

func TestTrackerCapsStuckStoreCalls(t *testing.T) {
	reader := installMetricReader(t)
	ctx := context.Background()
	unblock := make(chan struct{})
	store := newFakeStore()
	store.blockGet, store.blockSet = unblock, unblock
	const maxInflight, calls = 4, 50
	tracker := NewTracker(store, Options{TTL: time.Minute, LookupTimeout: 5 * time.Millisecond})
	tracker.lookupSlots = make(chan struct{}, maxInflight)
	tracker.maxWrites = maxInflight
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)
	baseline := runtime.NumGoroutine()

	start := time.Now()
	for i := 0; i < calls; i++ {
		key, _ := tracker.Lookup(ctx, reqCtx, reqCtx.Model)
		tracker.Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-b")
	}
	// Only the first maxInflight lookups wait for their deadline.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("%d calls against a stuck store took %s", calls, elapsed)
	}
	if lookups, writes := len(tracker.lookupSlots), inflightWrites(tracker); lookups != maxInflight || writes != maxInflight {
		t.Fatalf("in flight lookups=%d writes=%d, want %d each", lookups, writes, maxInflight)
	}
	if delta := runtime.NumGoroutine() - baseline; delta > 2*maxInflight+4 {
		t.Fatalf("goroutines grew by %d, want at most %d", delta, 2*maxInflight+4)
	}
	if got := counterValues(t, reader, lookupsMetric); got["timeout"] != calls {
		t.Fatalf("lookups = %v, want timeout=%d", got, calls)
	}
	if got := counterValues(t, reader, writesMetric); got["error"] != calls-maxInflight {
		t.Fatalf("writes = %v, want error=%d for dropped writes", got, calls-maxInflight)
	}

	close(unblock)
	tracker.Drain(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for len(tracker.lookupSlots) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("lookup slots not released: %d", len(tracker.lookupSlots))
		}
		time.Sleep(time.Millisecond)
	}
	// Freed slots serve new calls, and the admitted writes landed.
	if _, clusterID := tracker.Lookup(ctx, reqCtx, reqCtx.Model); clusterID != "cluster-b" {
		t.Fatalf("Lookup() after unblock cluster = %q, want cluster-b", clusterID)
	}
}

func TestDrainWaitsForWritesStartedWhileWaiting(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	gate1, gate2 := make(chan struct{}), make(chan struct{})
	store.blockSet = gate1
	tracker := newTestTracker(store)
	reqCtx := sessionCtx(requestctx.SessionSourceHeader)
	tracker.Remember(ctx, reqCtx, reqCtx.Model, "k1", http.StatusOK, "cluster-a")

	// The hook pauses Drain after it captured the first write's idle channel
	// and before it waits on it.
	captured, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tracker.drainWaitHook = func() {
		once.Do(func() {
			close(captured)
			<-resume
		})
	}
	drained := make(chan struct{})
	go func() {
		tracker.Drain(ctx)
		close(drained)
	}()
	<-captured

	// The first write finishes and a second starts before Drain resumes.
	close(gate1)
	for inflightWrites(tracker) != 0 {
		time.Sleep(time.Millisecond)
	}
	store.mu.Lock()
	store.blockSet = gate2
	store.mu.Unlock()
	tracker.Remember(ctx, reqCtx, reqCtx.Model, "k2", http.StatusOK, "cluster-b")
	close(resume)

	select {
	case <-drained:
		t.Fatal("Drain returned while the second write was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate2)
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return after the second write finished")
	}
}
