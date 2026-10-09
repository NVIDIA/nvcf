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

package cache

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// fakeDB is a non-nil inner handler. Calling any of its methods panics, which
// is what these tests want since the cache must not reach the database yet.
type fakeDB struct {
	data_access.DBHandlerV2
}

func (h *CachingDBHandler) insert(cachedKey key, e entry) {
	h.entriesMu.Lock()
	defer h.entriesMu.Unlock()
	if old, ok := h.entries[cachedKey]; ok {
		h.recency.Remove(old.recencyNode)
	}
	cachedEntry := &cached{entry: e}
	cachedEntry.recencyNode = h.recency.PushFront(cachedKey)
	h.entries[cachedKey] = cachedEntry
}

// discardFlush is a FlushFunc that drops what it is given, for tests that do not
// look at what is written.
func discardFlush(context.Context, data_access.EventV3UpsertRecord) error { return nil }

// flushRecorder is a FlushFunc that records what it is asked to write.
type flushRecorder struct {
	mu   sync.Mutex
	recs []data_access.EventV3UpsertRecord
	err  error
	// failFirst makes that many of the first calls, counted across all keys, fail
	// whatever err is.
	failFirst int
}

func (r *flushRecorder) flush(_ context.Context, rec data_access.EventV3UpsertRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	if len(r.recs) <= r.failFirst {
		return errors.New("database unavailable")
	}
	return r.err
}

func (r *flushRecorder) records() []data_access.EventV3UpsertRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]data_access.EventV3UpsertRecord(nil), r.recs...)
}

// assertConsistent checks that the map and the recency list describe the same
// entries and that the cache is within max size.
func assertConsistent(t *testing.T, h *CachingDBHandler) {
	t.Helper()
	h.entriesMu.RLock()
	defer h.entriesMu.RUnlock()
	require.Equal(t, len(h.entries), h.recency.Len())
	assert.LessOrEqual(t, len(h.entries), h.cfg.MaxSize)
	for node := h.recency.Front(); node != nil; node = node.Next() {
		cachedEntry, ok := h.entries[node.Value.(key)]
		require.True(t, ok)
		assert.Same(t, node, cachedEntry.recencyNode)
	}
}

func testConfig() Config {
	return Config{MaxSize: 100, FlushInterval: 60 * time.Second}
}

func newTestHandler(t *testing.T) *CachingDBHandler {
	t.Helper()
	h, _ := newTestHandlerWith(t, testConfig())
	return h
}

func newTestHandlerWith(t *testing.T, cfg Config) (*CachingDBHandler, *flushRecorder) {
	t.Helper()
	rec := &flushRecorder{}
	h, err := NewCachingDBHandler(&fakeDB{}, cfg, rec.flush, discardMeter())
	require.NoError(t, err)
	return h, rec
}

func TestLookup_EmptyCacheMisses(t *testing.T) {
	h := newTestHandler(t)

	e, ok := h.lookup(key{namespace: "ns", context: "ctx", eventName: "evt"})

	assert.False(t, ok)
	assert.Equal(t, entry{}, e)
}

func TestLookup_AfterInsertHits(t *testing.T) {
	h := newTestHandler(t)
	k := key{namespace: "ns", context: "ctx", eventName: "evt"}
	ts := time.Date(2026, 9, 19, 14, 32, 10, 0, time.UTC)
	want := entry{
		timestamp:   ts,
		source:      "nvidia-cluster-agent",
		details:     json.RawMessage(`{"downloadProgress":0.75}`),
		pending:     true,
		lastWritten: ts.Add(-time.Minute),
		lastUpdated: ts,
	}
	h.insert(k, want)

	got, ok := h.lookup(k)

	require.True(t, ok)
	assert.Equal(t, want, got)
}

func TestLookup_StatsEntryWithoutPayloadHits(t *testing.T) {
	h := newTestHandler(t)
	k := key{namespace: "ns", context: "ctx", eventName: "evt"}
	ts := time.Date(2026, 9, 19, 14, 32, 10, 0, time.UTC)
	h.insert(k, entry{timestamp: ts})

	got, ok := h.lookup(k)

	require.True(t, ok)
	assert.Equal(t, ts, got.timestamp)
	assert.Empty(t, got.source)
	assert.Nil(t, got.details)
}

func TestLookup_ReturnsCopy(t *testing.T) {
	h := newTestHandler(t)
	k := key{namespace: "ns", context: "ctx", eventName: "evt"}
	h.insert(k, entry{pending: true})

	got, ok := h.lookup(k)
	require.True(t, ok)
	got.pending = false

	again, ok := h.lookup(k)
	require.True(t, ok)
	assert.True(t, again.pending)
}

func TestLookup_KeysWithDifferentFieldsDoNotCollide(t *testing.T) {
	h := newTestHandler(t)
	base := key{namespace: "ns", context: "ctx", eventName: "evt"}
	h.insert(base, entry{})

	tests := map[string]key{
		"namespace": {namespace: "other", context: "ctx", eventName: "evt"},
		"context":   {namespace: "ns", context: "other", eventName: "evt"},
		"eventName": {namespace: "ns", context: "ctx", eventName: "other"},
		// Shifting a character across a field boundary must not alias.
		"boundary": {namespace: "nsc", context: "tx", eventName: "evt"},
	}
	for name, other := range tests {
		t.Run(name, func(t *testing.T) {
			_, ok := h.lookup(other)
			assert.False(t, ok)
		})
	}

	_, ok := h.lookup(base)
	assert.True(t, ok)
}

// Run with -race: a lookup without the read lock would be reported here.
func TestLookup_ConcurrentWithReplace(t *testing.T) {
	const (
		readers    = 8
		iterations = 2000
	)
	h := newTestHandler(t)
	k := key{namespace: "ns", context: "ctx", eventName: "evt"}
	base := time.Date(2026, 9, 19, 14, 32, 10, 0, time.UTC)
	h.insert(k, entry{timestamp: base, lastUpdated: base, details: json.RawMessage("0")})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= iterations; i++ {
			ts := base.Add(time.Duration(i) * time.Second)
			h.insert(k, entry{timestamp: ts, lastUpdated: ts, details: json.RawMessage(strconv.Itoa(i)), pending: i%2 == 0})
		}
	}()

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last time.Time
			for range iterations {
				got, ok := h.lookup(k)
				if !assert.True(t, ok) {
					return
				}
				// timestamp and lastUpdated are always written together, so a
				// mismatch means the reader saw a partially written entry.
				if !assert.True(t, got.timestamp.Equal(got.lastUpdated)) {
					return
				}
				// details is written with the timestamp, so it must name the
				// same iteration.
				step := int(got.timestamp.Sub(base) / time.Second)
				if !assert.Equal(t, strconv.Itoa(step), string(got.details)) {
					return
				}
				// The writer only moves forward, so a reader must never see
				// time go backwards.
				if !assert.False(t, got.timestamp.Before(last)) {
					return
				}
				last = got.timestamp
			}
		}()
	}
	wg.Wait()

	got, ok := h.lookup(k)
	require.True(t, ok)
	assert.Equal(t, base.Add(iterations*time.Second), got.timestamp)
}

func TestLookup_ConcurrentInsertsOfDistinctKeys(t *testing.T) {
	const (
		writers       = 8
		keysPerWriter = 250
	)
	h := newTestHandler(t)
	ts := time.Date(2026, 9, 19, 14, 32, 10, 0, time.UTC)
	keyFor := func(writer, n int) key {
		return key{namespace: "ns", context: "writer-" + strconv.Itoa(writer), eventName: "evt-" + strconv.Itoa(n)}
	}

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range keysPerWriter {
				h.insert(keyFor(w, n), entry{timestamp: ts})
				// A reader on the same goroutine's own key must see it.
				_, ok := h.lookup(keyFor(w, n))
				assert.True(t, ok)
			}
		}()
	}
	wg.Wait()

	for w := range writers {
		for n := range keysPerWriter {
			_, ok := h.lookup(keyFor(w, n))
			require.True(t, ok, "missing key writer=%d n=%d", w, n)
		}
	}
	assert.Len(t, h.entries, writers*keysPerWriter)
}

var (
	testKey  = key{namespace: "ns", context: "ctx", eventName: "evt"}
	testBase = time.Date(2026, 9, 19, 14, 32, 10, 0, time.UTC)
)

func TestProcessEvent_MissStoresCleanEntry(t *testing.T) {
	h := newTestHandler(t)
	now := testBase.Add(time.Hour)
	ev := event{timestamp: testBase, source: "nvidia-cluster-agent", details: json.RawMessage(`{"downloadProgress":0.75}`)}

	got := h.processEvent(testKey, ev, now)

	assert.Equal(t, outcomeMiss, got)
	e, ok := h.lookup(testKey)
	require.True(t, ok)
	assert.Equal(t, entry{
		timestamp:   testBase,
		source:      "nvidia-cluster-agent",
		details:     json.RawMessage(`{"downloadProgress":0.75}`),
		pending:     false,
		lastWritten: now,
		lastUpdated: now,
	}, e)
}

func TestProcessEvent_NewerEventReplacesCleanEntryAndMarksPending(t *testing.T) {
	h := newTestHandler(t)
	firstSeen := testBase.Add(time.Hour)
	h.processEvent(testKey, event{timestamp: testBase, source: "old", details: json.RawMessage(`{"p":1}`)}, firstSeen)
	now := firstSeen.Add(time.Second)
	newer := testBase.Add(time.Second)

	got := h.processEvent(testKey, event{timestamp: newer, source: "new", details: json.RawMessage(`{"p":2}`)}, now)

	assert.Equal(t, outcomeBecamePending, got)
	e, ok := h.lookup(testKey)
	require.True(t, ok)
	assert.Equal(t, entry{
		timestamp:   newer,
		source:      "new",
		details:     json.RawMessage(`{"p":2}`),
		pending:     true,
		lastWritten: firstSeen,
		lastUpdated: now,
	}, e)
}

func TestProcessEvent_NewerEventOnPendingEntryKeepsItPending(t *testing.T) {
	h := newTestHandler(t)
	h.processEvent(testKey, event{timestamp: testBase}, testBase)
	h.processEvent(testKey, event{timestamp: testBase.Add(time.Second)}, testBase)
	now := testBase.Add(time.Minute)

	got := h.processEvent(testKey, event{
		timestamp: testBase.Add(2 * time.Second),
		source:    "latest",
		details:   json.RawMessage(`{"p":3}`),
	}, now)

	assert.Equal(t, outcomeAlreadyPending, got)
	e, ok := h.lookup(testKey)
	require.True(t, ok)
	assert.Equal(t, entry{
		timestamp:   testBase.Add(2 * time.Second),
		source:      "latest",
		details:     json.RawMessage(`{"p":3}`),
		pending:     true,
		lastWritten: testBase,
		lastUpdated: now,
	}, e)
}

func TestProcessEvent_OlderOrEqualEventIsDiscarded(t *testing.T) {
	tests := []struct {
		name    string
		pending bool
		ts      time.Time
	}{
		{name: "older on clean entry", pending: false, ts: testBase.Add(-time.Second)},
		{name: "equal on clean entry", pending: false, ts: testBase},
		{name: "older on pending entry", pending: true, ts: testBase.Add(-time.Second)},
		{name: "equal on pending entry", pending: true, ts: testBase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(t)
			h.insert(testKey, entry{
				timestamp:   testBase,
				source:      "kept",
				details:     json.RawMessage(`{"kept":true}`),
				pending:     tt.pending,
				lastWritten: testBase,
				lastUpdated: testBase,
			})
			before, _ := h.lookup(testKey)

			got := h.processEvent(testKey, event{timestamp: tt.ts, source: "dropped", details: json.RawMessage(`{}`)}, testBase.Add(time.Hour))

			assert.Equal(t, outcomeStale, got)
			after, ok := h.lookup(testKey)
			require.True(t, ok)
			assert.Equal(t, before, after)
		})
	}
}

func TestProcessEvent_KeysAreIndependent(t *testing.T) {
	h := newTestHandler(t)
	other := key{namespace: "ns", context: "ctx", eventName: "other"}
	h.processEvent(testKey, event{timestamp: testBase}, testBase)

	// An event for another key is a miss even if it is older than the first.
	got := h.processEvent(other, event{timestamp: testBase.Add(-time.Hour)}, testBase)

	assert.Equal(t, outcomeMiss, got)
}

// Run with -race. Concurrent first events for one key must produce exactly one
// miss, and the newest event must win.
func TestProcessEvent_ConcurrentFirstEventsProduceOneMiss(t *testing.T) {
	const goroutines = 64
	h := newTestHandler(t)
	results := make([]outcome, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev := event{timestamp: testBase.Add(time.Duration(i) * time.Second), source: strconv.Itoa(i)}
			results[i] = h.processEvent(testKey, ev, testBase)
		}()
	}
	wg.Wait()

	misses := 0
	for _, r := range results {
		if r == outcomeMiss {
			misses++
		}
	}
	assert.Equal(t, 1, misses)
	e, ok := h.lookup(testKey)
	require.True(t, ok)
	assert.Equal(t, testBase.Add((goroutines-1)*time.Second), e.timestamp)
	assert.Equal(t, strconv.Itoa(goroutines-1), e.source)
}

// Run with -race. Of many concurrent newer events on a clean entry, exactly one
// must see the clean to pending transition, so a flush is scheduled once.
func TestProcessEvent_ConcurrentNewerEventsBecomePendingOnce(t *testing.T) {
	const goroutines = 64
	h := newTestHandler(t)
	h.processEvent(testKey, event{timestamp: testBase}, testBase)
	results := make([]outcome, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev := event{
				timestamp: testBase.Add(time.Duration(i+1) * time.Second),
				source:    strconv.Itoa(i + 1),
				details:   json.RawMessage(strconv.Itoa(i + 1)),
			}
			results[i] = h.processEvent(testKey, ev, testBase)
		}()
	}
	wg.Wait()

	counts := map[outcome]int{}
	for _, r := range results {
		counts[r]++
	}
	assert.Equal(t, 1, counts[outcomeBecamePending])
	assert.Equal(t, 0, counts[outcomeMiss])
	assert.Equal(t, goroutines, counts[outcomeBecamePending]+counts[outcomeAlreadyPending]+counts[outcomeStale])

	e, ok := h.lookup(testKey)
	require.True(t, ok)
	assert.True(t, e.pending)
	assert.Equal(t, testBase.Add(goroutines*time.Second), e.timestamp)
	assert.Equal(t, strconv.Itoa(goroutines), e.source)
	assert.Equal(t, strconv.Itoa(goroutines), string(e.details))
}

func TestInactiveTTL_IsFlushIntervalPlusTenPercent(t *testing.T) {
	tests := []struct {
		name          string
		flushInterval time.Duration
		want          time.Duration
	}{
		{name: "default", flushInterval: 60 * time.Second, want: 66 * time.Second},
		{name: "short interval", flushInterval: 10 * time.Second, want: 11 * time.Second},
		{name: "sub-second", flushInterval: 500 * time.Millisecond, want: 550 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Config{FlushInterval: tt.flushInterval}.inactiveTTL())
		})
	}
}

func TestNewCachingDBHandler_NilInnerHandlerFails(t *testing.T) {
	h, err := NewCachingDBHandler(nil, testConfig(), discardFlush, discardMeter())

	require.ErrorIs(t, err, errNilInnerHandler)
	assert.Nil(t, h)
}

func TestNewCachingDBHandler_TypedNilInnerHandlerFails(t *testing.T) {
	var typedNilHandler *fakeDB

	handler, err := NewCachingDBHandler(typedNilHandler, testConfig(), discardFlush, discardMeter())

	require.ErrorIs(t, err, errNilInnerHandler)
	assert.Nil(t, handler)
}

func TestNewCachingDBHandler_NilFlushFails(t *testing.T) {
	h, err := NewCachingDBHandler(&fakeDB{}, testConfig(), nil, discardMeter())

	require.ErrorIs(t, err, errNilFlush)
	assert.Nil(t, h)
}

func TestNewCachingDBHandler_KeepsInnerHandler(t *testing.T) {
	inner := &fakeDB{}

	h, err := NewCachingDBHandler(inner, testConfig(), discardFlush, discardMeter())

	require.NoError(t, err)
	assert.Same(t, inner, h.DBHandlerV2)
}

func TestNewCachingDBHandler_KeepsConfig(t *testing.T) {
	cfg := Config{MaxSize: 10, FlushInterval: 5 * time.Second}

	h, err := NewCachingDBHandler(&fakeDB{}, cfg, discardFlush, discardMeter())

	require.NoError(t, err)
	assert.Equal(t, cfg, h.cfg)
}

func TestNewCachingDBHandler_RejectsNonPositiveConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{name: "zero value", cfg: Config{}, wantErr: errInvalidMaxSize},
		{name: "zero max size", cfg: Config{MaxSize: 0, FlushInterval: time.Second}, wantErr: errInvalidMaxSize},
		{name: "negative max size", cfg: Config{MaxSize: -1, FlushInterval: time.Second}, wantErr: errInvalidMaxSize},
		{name: "zero flush interval", cfg: Config{MaxSize: 1, FlushInterval: 0}, wantErr: errInvalidFlushInterval},
		{name: "negative flush interval", cfg: Config{MaxSize: 1, FlushInterval: -time.Second}, wantErr: errInvalidFlushInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := NewCachingDBHandler(&fakeDB{}, tt.cfg, discardFlush, discardMeter())

			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, h)
		})
	}
}

func keyN(i int) key {
	return key{namespace: "ns", context: "ctx", eventName: "evt-" + strconv.Itoa(i)}
}

func at(seconds int) time.Time {
	return testBase.Add(time.Duration(seconds) * time.Second)
}

// evictionConfig has a flush interval of 10s, so the inactivity TTL is 11s.
func evictionConfig(maxSize int) Config {
	return Config{MaxSize: maxSize, FlushInterval: 10 * time.Second}
}

func requireKeys(t *testing.T, h *CachingDBHandler, present []int, absent []int) {
	t.Helper()
	for _, i := range present {
		_, ok := h.lookup(keyN(i))
		assert.True(t, ok, "key %d should be cached", i)
	}
	for _, i := range absent {
		_, ok := h.lookup(keyN(i))
		assert.False(t, ok, "key %d should be evicted", i)
	}
}

func TestProcessEvent_InsertBeyondMaxSizeEvictsLeastRecentlyUpdated(t *testing.T) {
	h, _ := newTestHandlerWith(t, evictionConfig(3))
	for i := 1; i <= 3; i++ {
		h.processEvent(keyN(i), event{timestamp: at(i)}, at(i))
	}

	got := h.processEvent(keyN(4), event{timestamp: at(4)}, at(4))

	assert.Equal(t, outcomeMiss, got)
	requireKeys(t, h, []int{2, 3, 4}, []int{1})
	assertConsistent(t, h)
}

func TestProcessEvent_NewerEventRefreshesRecency(t *testing.T) {
	h, _ := newTestHandlerWith(t, evictionConfig(3))
	for i := 1; i <= 3; i++ {
		h.processEvent(keyN(i), event{timestamp: at(i)}, at(i))
	}
	h.processEvent(keyN(1), event{timestamp: at(10)}, at(10))

	h.processEvent(keyN(4), event{timestamp: at(11)}, at(11))

	requireKeys(t, h, []int{1, 3, 4}, []int{2})
	assertConsistent(t, h)
}

func TestProcessEvent_StaleEventDoesNotRefreshRecency(t *testing.T) {
	h, _ := newTestHandlerWith(t, evictionConfig(3))
	for i := 1; i <= 3; i++ {
		h.processEvent(keyN(i), event{timestamp: at(i)}, at(i))
	}
	assert.Equal(t, outcomeStale, h.processEvent(keyN(1), event{timestamp: at(0)}, at(10)))

	h.processEvent(keyN(4), event{timestamp: at(11)}, at(11))

	requireKeys(t, h, []int{2, 3, 4}, []int{1})
}

func TestProcessEvent_MaxSizeOneKeepsNewestEntry(t *testing.T) {
	h, _ := newTestHandlerWith(t, evictionConfig(1))
	h.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

	h.processEvent(keyN(2), event{timestamp: at(2)}, at(2))

	requireKeys(t, h, []int{2}, []int{1})
	assertConsistent(t, h)
}

func TestProcessEvent_CacheStaysAtMaxSizeOverManyInserts(t *testing.T) {
	h, _ := newTestHandlerWith(t, evictionConfig(5))

	for i := 1; i <= 100; i++ {
		h.processEvent(keyN(i), event{timestamp: at(i)}, at(i))
	}

	requireKeys(t, h, []int{96, 97, 98, 99, 100}, []int{1, 50, 95})
	assertConsistent(t, h)
	assert.Len(t, h.entries, 5)
}

func TestProcessEvent_EvictedPendingEntryIsFlushedWithLatestEvent(t *testing.T) {
	h, rec := newTestHandlerWith(t, evictionConfig(2))
	h.processEvent(keyN(1), event{timestamp: at(1), source: "s1", details: json.RawMessage(`{"p":1}`)}, at(1))
	h.processEvent(keyN(1), event{timestamp: at(2), source: "s2", details: json.RawMessage(`{"p":2}`)}, at(2))
	h.processEvent(keyN(2), event{timestamp: at(3)}, at(3))

	h.processEvent(keyN(3), event{timestamp: at(4)}, at(4))

	assert.Equal(t, []data_access.EventV3UpsertRecord{{
		Namespace: "ns",
		Context:   "ctx",
		EventName: "evt-1",
		Source:    "s2",
		Details:   json.RawMessage(`{"p":2}`),
		Timestamp: at(2),
	}}, rec.records())
}

func TestProcessEvent_EvictedCleanEntryIsNotFlushed(t *testing.T) {
	h, rec := newTestHandlerWith(t, evictionConfig(1))
	h.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

	h.processEvent(keyN(2), event{timestamp: at(2)}, at(2))

	assert.Empty(t, rec.records())
}

func TestProcessEvent_FailedEvictionFlushStillEvicts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, recorder := newTestHandlerWith(t, evictionConfig(1))
		recorder.err = errors.New("database unavailable")
		makePending(handler, keyN(1), 1)

		got := handler.processEvent(keyN(2), event{timestamp: at(3)}, at(3))

		assert.Equal(t, outcomeMiss, got)
		assert.Len(t, recorder.records(), evictionFlushAttempts)
		requireKeys(t, handler, []int{2}, []int{1})
		assertConsistent(t, handler)
	})
}

func TestFlushEvicted_RetriesWithDoublingDelays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attemptTimes []time.Time
		flush := func(context.Context, data_access.EventV3UpsertRecord) error {
			attemptTimes = append(attemptTimes, time.Now())
			return errors.New("database unavailable")
		}
		handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
		require.NoError(t, err)
		makePending(handler, keyN(1), 1)
		start := time.Now()

		handler.processEvent(keyN(2), event{timestamp: at(3)}, at(3))

		var offsets []time.Duration
		for _, attemptTime := range attemptTimes {
			offsets = append(offsets, attemptTime.Sub(start))
		}
		assert.Equal(t, []time.Duration{
			0,
			100 * time.Millisecond,
			300 * time.Millisecond,
			700 * time.Millisecond,
		}, offsets)
	})
}

func TestFlushEvicted_StopsRetryingOnceAWriteSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		flush := func(context.Context, data_access.EventV3UpsertRecord) error {
			calls++
			if calls < 3 {
				return errors.New("database unavailable")
			}
			return nil
		}
		handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
		require.NoError(t, err)
		makePending(handler, keyN(1), 1)

		handler.processEvent(keyN(2), event{timestamp: at(3)}, at(3))

		assert.Equal(t, 3, calls)
	})
}

func TestFlushEvicted_StopsRetryingWhenTheTimeoutEndsDuringTheFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		flush := func(ctx context.Context, _ data_access.EventV3UpsertRecord) error {
			calls++
			<-ctx.Done()
			return ctx.Err()
		}
		handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
		require.NoError(t, err)
		makePending(handler, keyN(1), 1)

		handler.processEvent(keyN(2), event{timestamp: at(3)}, at(3))

		assert.Equal(t, 1, calls, "a retry after the deadline would fail at once")
	})
}

func TestFlushEvicted_StopsRetryingWhenTheTimeoutEndsDuringAWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		flush := func(context.Context, data_access.EventV3UpsertRecord) error {
			calls++
			time.Sleep(evictionFlushTimeout - evictionRetryBaseDelay/2)
			return errors.New("database unavailable")
		}
		handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
		require.NoError(t, err)
		makePending(handler, keyN(1), 1)

		handler.processEvent(keyN(2), event{timestamp: at(3)}, at(3))

		assert.Equal(t, 1, calls, "the wait would have ended after the timeout")
	})
}

func TestFlushEvicted_AFailingRecordDoesNotStopTheNextOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		written := map[string]int{}
		flush := func(_ context.Context, rec data_access.EventV3UpsertRecord) error {
			written[rec.EventName]++
			if rec.EventName == keyN(1).eventName {
				return errors.New("database unavailable")
			}
			return nil
		}
		handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
		require.NoError(t, err)

		handler.flushEvicted([]data_access.EventV3UpsertRecord{
			{Namespace: "ns", Context: "ctx", EventName: keyN(1).eventName, Timestamp: at(1)},
			{Namespace: "ns", Context: "ctx", EventName: keyN(2).eventName, Timestamp: at(2)},
		})

		assert.Equal(t, evictionFlushAttempts, written[keyN(1).eventName])
		assert.Equal(t, 1, written[keyN(2).eventName])
	})
}

func TestFlushEvicted_RecordsAfterTheTimeoutAreNotWritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		written := map[string]int{}
		flush := func(ctx context.Context, rec data_access.EventV3UpsertRecord) error {
			written[rec.EventName]++
			<-ctx.Done()
			return ctx.Err()
		}
		handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
		require.NoError(t, err)

		handler.flushEvicted([]data_access.EventV3UpsertRecord{
			{Namespace: "ns", Context: "ctx", EventName: keyN(1).eventName, Timestamp: at(1)},
			{Namespace: "ns", Context: "ctx", EventName: keyN(2).eventName, Timestamp: at(2)},
		})

		assert.Equal(t, 1, written[keyN(1).eventName])
		assert.Zero(t, written[keyN(2).eventName])
	})
}

func TestFlushWithRetry_ReportsHowManyWritesItMade(t *testing.T) {
	tests := []struct {
		name         string
		failFirst    int
		endTimeout   bool
		wantAttempts int
		wantErr      bool
	}{
		{name: "first write succeeds", wantAttempts: 1},
		{name: "third write succeeds", failFirst: 2, wantAttempts: 3},
		{name: "every write fails", failFirst: evictionFlushAttempts, wantAttempts: evictionFlushAttempts, wantErr: true},
		{name: "timeout already ended", endTimeout: true, wantAttempts: 0, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				handler, recorder := newTestHandlerWith(t, evictionConfig(1))
				recorder.failFirst = tt.failFirst
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if tt.endTimeout {
					cancel()
				}

				attempts, err := handler.flushWithRetry(ctx, data_access.EventV3UpsertRecord{EventName: "evt-1"})

				assert.Equal(t, tt.wantAttempts, attempts)
				assert.Equal(t, tt.wantErr, err != nil)
			})
		})
	}
}

func TestWriteMiss_SuccessLeavesTheEntryClean(t *testing.T) {
	handler, recorder := newTestHandlerWith(t, evictionConfig(10))
	handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

	err := handler.writeMiss(context.Background(), keyN(1), event{timestamp: at(1)})

	require.NoError(t, err)
	assert.Len(t, recorder.records(), 1)
	cachedEntry, _ := handler.lookup(keyN(1))
	assert.False(t, cachedEntry.pending)
	_, scheduled := scheduledSlot(handler, keyN(1))
	assert.False(t, scheduled)
}

func TestWriteMiss_RetriesBeforeGivingUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, recorder := newTestHandlerWith(t, evictionConfig(10))
		recorder.failFirst = 2
		handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

		err := handler.writeMiss(context.Background(), keyN(1), event{timestamp: at(1)})

		require.NoError(t, err)
		assert.Len(t, recorder.records(), 3)
		cachedEntry, _ := handler.lookup(keyN(1))
		assert.False(t, cachedEntry.pending, "a write that succeeded on a retry needs no flush")
	})
}

func TestWriteMiss_FailureKeepsTheEventPendingAndScheduled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, recorder := newTestHandlerWith(t, evictionConfig(10))
		recorder.err = errors.New("database unavailable")
		probe := installLockProbe(handler)
		handler.processEvent(keyN(1), event{timestamp: at(1), source: "src"}, at(1))

		err := handler.writeMiss(context.Background(), keyN(1), event{timestamp: at(1), source: "src"})

		require.ErrorIs(t, err, recorder.err)
		assert.Len(t, recorder.records(), evictionFlushAttempts)
		cachedEntry, ok := handler.lookup(keyN(1))
		require.True(t, ok)
		assert.True(t, cachedEntry.pending)
		assert.EqualValues(t, 1, probe.scheduleCalls.Load(), "the flush is scheduled once")
		assert.False(t, probe.calledWithoutLock.Load(), "the wheel must be called with the cache lock held")
		assertConsistent(t, handler)
	})
}

func TestWriteMiss_FailureStillKeepsTheEventWhenTheContextHasEnded(t *testing.T) {
	handler, recorder := newTestHandlerWith(t, evictionConfig(10))
	handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := handler.writeMiss(ctx, keyN(1), event{timestamp: at(1)})

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, recorder.records(), "no write is made once the context has ended")
	cachedEntry, _ := handler.lookup(keyN(1))
	assert.True(t, cachedEntry.pending)
}

func TestWriteMiss_FailureCreatesAgainAnEntryEvictedMeanwhile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, recorder := newTestHandlerWith(t, evictionConfig(1))
		recorder.err = errors.New("database unavailable")
		handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))
		handler.processEvent(keyN(2), event{timestamp: at(2)}, at(2))
		requireKeys(t, handler, []int{2}, []int{1})
		recorder.mu.Lock()
		recorder.recs = nil
		recorder.mu.Unlock()

		err := handler.writeMiss(context.Background(), keyN(1), event{timestamp: at(1)})

		require.Error(t, err)
		cachedEntry, ok := handler.lookup(keyN(1))
		require.True(t, ok, "the event is not lost because its clean entry was evicted")
		assert.True(t, cachedEntry.pending)
		_, scheduled := scheduledSlot(handler, keyN(1))
		assert.True(t, scheduled)
		assertConsistent(t, handler)
	})
}

func TestWriteMiss_FailureWritesThePendingEntryItEvictsToMakeRoom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		written := map[string]int{}
		flush := func(_ context.Context, rec data_access.EventV3UpsertRecord) error {
			written[rec.EventName]++
			if rec.EventName == keyN(1).eventName {
				return errors.New("database unavailable")
			}
			return nil
		}
		handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
		require.NoError(t, err)
		handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))
		makePending(handler, keyN(2), 2)
		written = map[string]int{}

		require.Error(t, handler.writeMiss(context.Background(), keyN(1), event{timestamp: at(1)}))

		assert.Equal(t, 1, written[keyN(2).eventName], "the entry pushed out was written")
		requireKeys(t, handler, []int{1}, []int{2})
		assertConsistent(t, handler)
	})
}

func TestKeepPending_IgnoresAnOlderEventThanTheStoredOne(t *testing.T) {
	handler := newTestHandler(t)
	makePending(handler, keyN(1), 1)

	evicted := handler.keepPending(keyN(1), event{timestamp: at(1), source: "old"}, at(10))

	assert.Empty(t, evicted)
	cachedEntry, _ := handler.lookup(keyN(1))
	assert.Equal(t, at(2), cachedEntry.timestamp)
	assert.Empty(t, cachedEntry.source)
}

func TestKeepPending_ReplacesAnOlderStoredEntry(t *testing.T) {
	handler := newTestHandler(t)
	handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

	handler.keepPending(keyN(1), event{timestamp: at(5), source: "new"}, at(10))

	cachedEntry, _ := handler.lookup(keyN(1))
	assert.Equal(t, at(5), cachedEntry.timestamp)
	assert.Equal(t, "new", cachedEntry.source)
	assert.True(t, cachedEntry.pending)
	_, scheduled := scheduledSlot(handler, keyN(1))
	assert.True(t, scheduled)
}

func TestKeepPending_DoesNotScheduleAnEntryTwice(t *testing.T) {
	handler := newTestHandler(t)
	probe := installLockProbe(handler)
	makePending(handler, keyN(1), 1)
	scheduledBefore := probe.scheduleCalls.Load()

	handler.keepPending(keyN(1), event{timestamp: at(2)}, at(10))

	assert.Equal(t, scheduledBefore, probe.scheduleCalls.Load())
}

func TestEvictInactive_RetriesAFailedWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, recorder := newTestHandlerWith(t, evictionConfig(10))
		recorder.failFirst = 2
		makePending(handler, keyN(1), 1)

		evicted := handler.evictInactive(at(1000))

		assert.Equal(t, 1, evicted)
		assert.Len(t, recorder.records(), 3)
	})
}

func TestProcessEvent_EvictionFlushRunsWithoutTheLock(t *testing.T) {
	var h *CachingDBHandler
	var lockWasFree, entryWasGone bool
	flush := func(_ context.Context, rec data_access.EventV3UpsertRecord) error {
		// Checking the lock first keeps a regression from deadlocking the lookup.
		if lockWasFree = h.entriesMu.TryLock(); !lockWasFree {
			return nil
		}
		h.entriesMu.Unlock()
		_, inCache := h.lookup(key{namespace: rec.Namespace, context: rec.Context, eventName: rec.EventName})
		entryWasGone = !inCache
		return nil
	}
	h, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
	require.NoError(t, err)
	h.processEvent(keyN(1), event{timestamp: at(1)}, at(1))
	h.processEvent(keyN(1), event{timestamp: at(2)}, at(2))

	h.processEvent(keyN(2), event{timestamp: at(3)}, at(3))

	assert.True(t, lockWasFree, "flush must not run while the cache lock is held")
	assert.True(t, entryWasGone, "the evicted entry must already be removed when it is flushed")
}

func TestEvictInactive_UsesInactivityTTL(t *testing.T) {
	tests := []struct {
		name        string
		age         time.Duration
		wantEvicted int
	}{
		{name: "younger than ttl", age: 10 * time.Second, wantEvicted: 0},
		{name: "exactly ttl", age: 11 * time.Second, wantEvicted: 0},
		{name: "older than ttl", age: 12 * time.Second, wantEvicted: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandlerWith(t, evictionConfig(10))
			h.processEvent(keyN(1), event{timestamp: testBase}, testBase)

			got := h.evictInactive(testBase.Add(tt.age))

			assert.Equal(t, tt.wantEvicted, got)
			_, ok := h.lookup(keyN(1))
			assert.Equal(t, tt.wantEvicted == 0, ok)
		})
	}
}

func TestEvictInactive_KeepsActiveEntries(t *testing.T) {
	h, _ := newTestHandlerWith(t, evictionConfig(10))
	h.processEvent(keyN(1), event{timestamp: at(0)}, at(0))
	h.processEvent(keyN(2), event{timestamp: at(10)}, at(10))
	h.processEvent(keyN(3), event{timestamp: at(20)}, at(20))

	got := h.evictInactive(at(25))

	assert.Equal(t, 2, got)
	requireKeys(t, h, []int{3}, []int{1, 2})
	assertConsistent(t, h)
}

func TestEvictInactive_NewerEventRefreshesTTL(t *testing.T) {
	h, _ := newTestHandlerWith(t, evictionConfig(10))
	h.processEvent(keyN(1), event{timestamp: at(0)}, at(0))
	h.processEvent(keyN(1), event{timestamp: at(10)}, at(10))

	got := h.evictInactive(at(15))

	assert.Zero(t, got)
	requireKeys(t, h, []int{1}, nil)
}

func TestEvictInactive_FlushesPendingEntriesOnly(t *testing.T) {
	h, rec := newTestHandlerWith(t, evictionConfig(10))
	h.processEvent(keyN(1), event{timestamp: at(0)}, at(0))
	h.processEvent(keyN(1), event{timestamp: at(1), source: "latest"}, at(1))
	h.processEvent(keyN(2), event{timestamp: at(0)}, at(0))

	got := h.evictInactive(at(100))

	assert.Equal(t, 2, got)
	records := rec.records()
	require.Len(t, records, 1)
	assert.Equal(t, "evt-1", records[0].EventName)
	assert.Equal(t, "latest", records[0].Source)
	assert.Equal(t, at(1), records[0].Timestamp)
	assertConsistent(t, h)
}

func TestEvictInactive_EmptyCache(t *testing.T) {
	h, rec := newTestHandlerWith(t, evictionConfig(10))

	assert.Zero(t, h.evictInactive(at(100)))
	assert.Empty(t, rec.records())
}

// persistedEvent identifies one event that reached the database.
type persistedEvent struct {
	cachedKey key
	nanos     int64
}

func persistedOf(rec data_access.EventV3UpsertRecord) persistedEvent {
	return persistedEvent{
		cachedKey: key{namespace: rec.Namespace, context: rec.Context, eventName: rec.EventName},
		nanos:     rec.Timestamp.UnixNano(),
	}
}

// Run with -race. Writers share a small pool of keys and a cache far smaller
// than the pool, while evictInactive runs concurrently with `now` values that
// arrive out of order. However the goroutines interleave, the newest event sent
// for a key must never be lost: it is either still cached or it reached the
// database, as a miss the caller writes or as a flush when it was evicted.
func TestProcessEvent_ConcurrentEvictionNeverLosesTheLatestEvent(t *testing.T) {
	tests := []struct {
		name       string
		maxSize    int
		evictEvery int // run evictInactive after every n-th event of a writer; 0 never
	}{
		// Fewer slots than keys, so entries leave by size. Most keys still fit, so
		// events often hit a cached key and leave it pending.
		{name: "size eviction only", maxSize: stressKeyCount * 3 / 4, evictEvery: 0},
		// Room for every key, so entries only leave by expiring.
		{name: "inactivity eviction only", maxSize: stressKeyCount, evictEvery: 10},
		{name: "both", maxSize: stressKeyCount * 3 / 4, evictEvery: 10},
		// Very few slots, so almost every event is a miss and evictions are constant.
		{name: "heavy size pressure", maxSize: stressKeyCount / 5, evictEvery: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkConcurrentEviction(t, tt.maxSize, tt.evictEvery)
		})
	}
}

// stressKeyCount is the size of the key pool the concurrent eviction test shares.
const stressKeyCount = 40

func checkConcurrentEviction(t *testing.T, maxSize, evictEvery int) {
	t.Helper()
	const (
		writers    = 8
		iterations = 300
	)
	handler, flushed := newTestHandlerWith(t, evictionConfig(maxSize))

	var (
		clock      atomic.Int64
		mu         sync.Mutex
		missWrites []data_access.EventV3UpsertRecord
		latestSent = map[key]time.Time{}
		recordFor  = func(cachedKey key, ev event) data_access.EventV3UpsertRecord {
			return data_access.EventV3UpsertRecord{
				Namespace: cachedKey.namespace,
				Context:   cachedKey.context,
				EventName: cachedKey.eventName,
				Source:    ev.source,
				Details:   ev.details,
				Timestamp: ev.timestamp,
			}
		}
	)

	var wg sync.WaitGroup
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := range iterations {
				sequence := int(clock.Add(1))
				cachedKey := keyN((writer*7 + iteration) % stressKeyCount)
				timestamp := at(sequence)
				ev := event{
					timestamp: timestamp,
					source:    "writer-" + strconv.Itoa(writer),
					details:   json.RawMessage(strconv.Itoa(sequence)),
				}

				mu.Lock()
				if timestamp.After(latestSent[cachedKey]) {
					latestSent[cachedKey] = timestamp
				}
				mu.Unlock()

				// A miss is written by the caller, so the test records it here.
				if handler.processEvent(cachedKey, ev, timestamp) == outcomeMiss {
					mu.Lock()
					missWrites = append(missWrites, recordFor(cachedKey, ev))
					mu.Unlock()
				}
				if evictEvery > 0 && iteration%evictEvery == 0 {
					handler.evictInactive(timestamp)
				}
			}
		}()
	}
	wg.Wait()

	assertConsistent(t, handler)

	persisted := map[persistedEvent]bool{}
	for _, rec := range append(missWrites, flushed.records()...) {
		id := persistedOf(rec)
		assert.False(t, persisted[id], "event %v was written twice", id)
		persisted[id] = true
		// The payload must belong to the timestamp it was written with.
		assert.Equal(t, strconv.Itoa(int(rec.Timestamp.Sub(testBase)/time.Second)), string(rec.Details))
	}

	for cachedKey, newest := range latestSent {
		lookedUpEntry, inCache := handler.lookup(cachedKey)
		stillCached := inCache && lookedUpEntry.timestamp.Equal(newest)
		wasWritten := persisted[persistedEvent{cachedKey: cachedKey, nanos: newest.UnixNano()}]
		assert.True(t, stillCached || wasWritten, "the newest event for %v was lost", cachedKey)
	}
}

// A newer event for a key can arrive while the flush of that key's evicted
// entry is still in flight. The cache keeps no record of an evicted key, so the
// newer event is an ordinary miss and the caller writes it right away, possibly
// before the older flush lands. The cache cannot order those two writes; the
// database must, which the stats table does with its timestamp guard.
func TestProcessEvent_NewerEventDuringEvictionFlushIsAMiss(t *testing.T) {
	flushStarted := make(chan struct{})
	releaseFlush := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFlush) }) }
	t.Cleanup(release)

	var inFlight data_access.EventV3UpsertRecord
	flush := func(_ context.Context, rec data_access.EventV3UpsertRecord) error {
		inFlight = rec
		close(flushStarted)
		<-releaseFlush
		return nil
	}
	handler, err := NewCachingDBHandler(&fakeDB{}, evictionConfig(1), flush, discardMeter())
	require.NoError(t, err)
	handler.processEvent(keyN(1), event{timestamp: at(1), details: json.RawMessage(`{"progress":1}`)}, at(1))
	handler.processEvent(keyN(1), event{timestamp: at(2), details: json.RawMessage(`{"progress":2}`)}, at(2))

	evictionDone := make(chan struct{})
	go func() {
		defer close(evictionDone)
		// This miss overflows the cache and evicts key 1, whose flush then blocks.
		handler.processEvent(keyN(2), event{timestamp: at(3)}, at(3))
	}()
	<-flushStarted

	newerOutcome := make(chan outcome, 1)
	go func() {
		newerOutcome <- handler.processEvent(keyN(1), event{timestamp: at(10), details: json.RawMessage(`{"progress":10}`)}, at(10))
	}()
	select {
	case got := <-newerOutcome:
		assert.Equal(t, outcomeMiss, got, "an evicted key has no entry, so its next event is a miss")
	case <-time.After(5 * time.Second):
		t.Fatal("a newer event was blocked by an in-flight eviction flush")
	}

	release()
	<-evictionDone

	assert.Equal(t, at(2), inFlight.Timestamp, "the in-flight flush carries the value that was evicted")
	assert.JSONEq(t, `{"progress":2}`, string(inFlight.Details))
	cachedEntry, ok := handler.lookup(keyN(1))
	require.True(t, ok)
	assert.Equal(t, at(10), cachedEntry.timestamp)
	assert.False(t, cachedEntry.pending, "the newer event was stored as a clean miss")
	assertConsistent(t, handler)
}
