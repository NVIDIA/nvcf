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

func newTestHandler(t *testing.T) *CachingDBHandler {
	t.Helper()
	h, err := NewCachingDBHandler(&fakeDB{}, DefaultConfig())
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

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, 100_000, cfg.MaxSize)
	assert.Equal(t, 60*time.Second, cfg.FlushInterval)
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
	h, err := NewCachingDBHandler(nil, DefaultConfig())

	require.ErrorIs(t, err, errNilInnerHandler)
	assert.Nil(t, h)
}

func TestNewCachingDBHandler_KeepsInnerHandler(t *testing.T) {
	inner := &fakeDB{}

	h, err := NewCachingDBHandler(inner, DefaultConfig())

	require.NoError(t, err)
	assert.Same(t, inner, h.DBHandlerV2)
}

func TestNewCachingDBHandler_FillsZeroConfigWithDefaults(t *testing.T) {
	h, err := NewCachingDBHandler(&fakeDB{}, Config{})

	require.NoError(t, err)
	assert.Equal(t, DefaultConfig(), h.cfg)
}

func TestNewCachingDBHandler_KeepsExplicitConfig(t *testing.T) {
	cfg := Config{MaxSize: 10, FlushInterval: 5 * time.Second}

	h, err := NewCachingDBHandler(&fakeDB{}, cfg)

	require.NoError(t, err)
	assert.Equal(t, cfg, h.cfg)
}
