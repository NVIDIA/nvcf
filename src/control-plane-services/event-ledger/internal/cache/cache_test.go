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
	"encoding/json"
	"strconv"
	"sync"
	"testing"
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

func (h *CachingDBHandler) insert(k key, e entry) {
	h.entriesMu.Lock()
	defer h.entriesMu.Unlock()
	h.entries[k] = &e
}

func testConfig() Config {
	return Config{MaxSize: 100, FlushInterval: 60 * time.Second}
}

func newTestHandler(t *testing.T) *CachingDBHandler {
	t.Helper()
	h, err := NewCachingDBHandler(&fakeDB{}, testConfig())
	require.NoError(t, err)
	return h
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
	h, err := NewCachingDBHandler(nil, testConfig())

	require.ErrorIs(t, err, errNilInnerHandler)
	assert.Nil(t, h)
}

func TestNewCachingDBHandler_TypedNilInnerHandlerFails(t *testing.T) {
	var typedNilHandler *fakeDB

	handler, err := NewCachingDBHandler(typedNilHandler, testConfig())

	require.ErrorIs(t, err, errNilInnerHandler)
	assert.Nil(t, handler)
}

func TestNewCachingDBHandler_KeepsInnerHandler(t *testing.T) {
	inner := &fakeDB{}

	h, err := NewCachingDBHandler(inner, testConfig())

	require.NoError(t, err)
	assert.Same(t, inner, h.DBHandlerV2)
}

func TestNewCachingDBHandler_KeepsConfig(t *testing.T) {
	cfg := Config{MaxSize: 10, FlushInterval: 5 * time.Second}

	h, err := NewCachingDBHandler(&fakeDB{}, cfg)

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
			h, err := NewCachingDBHandler(&fakeDB{}, tt.cfg)

			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, h)
		})
	}
}
