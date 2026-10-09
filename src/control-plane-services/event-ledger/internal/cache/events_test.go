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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// recordingDB is a wrapped handler that records the writes it receives. Methods it
// does not define panic, which shows that the cache did not call them.
type recordingDB struct {
	data_access.DBHandlerV2

	mu           sync.Mutex
	singleWrites []data_access.EventV3UpsertRecord
	bulkWrites   [][]data_access.EventV3UpsertRecord
	statsWrites  int
	err          error
}

func (db *recordingDB) UpsertEventV3(_ context.Context, namespace, eventContext, eventName, source string, details json.RawMessage, timestamp time.Time) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.singleWrites = append(db.singleWrites, data_access.EventV3UpsertRecord{
		Namespace: namespace, Context: eventContext, EventName: eventName, Source: source, Details: details, Timestamp: timestamp,
	})
	return db.err
}

func (db *recordingDB) BulkUpsertEventsV3(_ context.Context, events []data_access.EventV3UpsertRecord) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.bulkWrites = append(db.bulkWrites, append([]data_access.EventV3UpsertRecord(nil), events...))
	return db.err
}

func (db *recordingDB) UpsertStatsV3(context.Context, string, string, string, time.Time) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.statsWrites++
	return nil
}

func (db *recordingDB) setErr(err error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.err = err
}

func (db *recordingDB) singleWriteCount() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.singleWrites)
}

func (db *recordingDB) bulkWriteCount() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.bulkWrites)
}

// newEventsHandler returns a handler over a recording database and flush recorder.
func newEventsHandler(t *testing.T, cfg Config) (*CachingDBHandler, *recordingDB, *flushRecorder) {
	t.Helper()
	db := &recordingDB{}
	flushed := &flushRecorder{}
	return newHandlerOver(t, db, cfg, flushed.flush, discardMeter()), db, flushed
}

func upsertEvent(handler *CachingDBHandler, eventKey key, second int, details string) error {
	return handler.UpsertEventV3(context.Background(), eventKey.namespace, eventKey.context, eventKey.eventName,
		"source", json.RawMessage(details), at(second))
}

func eventRecord(eventKey key, second int, details string) data_access.EventV3UpsertRecord {
	return data_access.EventV3UpsertRecord{
		Namespace: eventKey.namespace, Context: eventKey.context, EventName: eventKey.eventName,
		Source: "source", Details: json.RawMessage(details), Timestamp: at(second),
	}
}

func TestUpsertEventV3_AMissIsWrittenAtOnce(t *testing.T) {
	handler, db, flushed := newEventsHandler(t, evictionConfig(10))

	require.NoError(t, upsertEvent(handler, keyN(1), 1, `{"n":1}`))

	assert.Equal(t, []data_access.EventV3UpsertRecord{eventRecord(keyN(1), 1, `{"n":1}`)}, flushed.records(),
		"a miss goes through the flush function, which must only write newer rows")
	assert.Zero(t, db.singleWriteCount(), "and not through the plain upsert")
	requireEntryState(t, handler, keyN(1), false)
}

func TestUpsertEventV3_ARepeatForACachedKeyIsHeldForALaterFlush(t *testing.T) {
	handler, _, flushed := newEventsHandler(t, evictionConfig(10))
	require.NoError(t, upsertEvent(handler, keyN(1), 1, `{"n":1}`))

	require.NoError(t, upsertEvent(handler, keyN(1), 2, `{"n":2}`))
	require.NoError(t, upsertEvent(handler, keyN(1), 3, `{"n":3}`))

	assert.Equal(t, 1, flushed.flushCalls(), "only the miss was written at once")
	cachedEntry := requireEntryState(t, handler, keyN(1), true)
	assert.JSONEq(t, `{"n":3}`, string(cachedEntry.details), "the latest value is held")

	fireWheel(handler)

	require.Len(t, flushed.records(), 2)
	assert.Equal(t, eventRecord(keyN(1), 3, `{"n":3}`), flushed.records()[1], "the flush wrote the latest value")
}

func TestUpsertEventV3_AnOlderOrEqualEventIsNotWritten(t *testing.T) {
	handler, _, flushed := newEventsHandler(t, evictionConfig(10))
	require.NoError(t, upsertEvent(handler, keyN(1), 5, `{"n":5}`))

	require.NoError(t, upsertEvent(handler, keyN(1), 3, `{"n":3}`))
	require.NoError(t, upsertEvent(handler, keyN(1), 5, `{"n":"same"}`))

	assert.Equal(t, 1, flushed.flushCalls())
	requireEntryState(t, handler, keyN(1), false)
}

func TestUpsertEventV3_AFailedMissWriteIsReturnedAndKeptForALaterFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, _, flushed := newEventsHandler(t, evictionConfig(10))
		writeErr := errors.New("database unavailable")
		flushed.err = writeErr

		err := upsertEvent(handler, keyN(1), 1, `{"n":1}`)

		require.ErrorIs(t, err, writeErr)
		assert.Equal(t, evictionFlushAttempts, flushed.flushCalls(), "the write was retried")
		requireEntryState(t, handler, keyN(1), true)

		require.NoError(t, upsertEvent(handler, keyN(1), 1, `{"n":1}`), "a redelivery is discarded as stale, which is safe")
		assert.Equal(t, evictionFlushAttempts, flushed.flushCalls())

		flushed.err = nil
		fireWheel(handler)

		requireEntryState(t, handler, keyN(1), false)
		assert.Equal(t, eventRecord(keyN(1), 1, `{"n":1}`), flushed.records()[evictionFlushAttempts], "the flush wrote it")
	})
}

// If a newer event replaces the entry while the miss is being written, a failure of
// that write must not throw the newer event away.
func TestUpsertEventV3_AFailedMissWriteKeepsANewerEventThatArrivedMeanwhile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var handler *CachingDBHandler
		var newerSent bool
		flush := func(context.Context, []data_access.EventV3UpsertRecord) error {
			if !newerSent {
				newerSent = true
				require.NoError(t, upsertEvent(handler, keyN(1), 2, `{"n":2}`))
			}
			return errors.New("database unavailable")
		}
		handler = newHandlerOver(t, &recordingDB{}, evictionConfig(10), flush, discardMeter())

		require.Error(t, upsertEvent(handler, keyN(1), 1, `{"n":1}`))

		cachedEntry := requireEntryState(t, handler, keyN(1), true)
		assert.JSONEq(t, `{"n":2}`, string(cachedEntry.details), "the newer event is still held, and will be flushed")
	})
}

func TestUpsertEventV3_ALargePayloadIsWrittenAtOnceAndNotCached(t *testing.T) {
	handler, db, _ := newEventsHandler(t, evictionConfig(10))
	large := `"` + strings.Repeat("x", maxCachedDetailsBytes) + `"`

	require.NoError(t, upsertEvent(handler, keyN(1), 1, large))
	require.NoError(t, upsertEvent(handler, keyN(1), 2, large))

	assert.Equal(t, 2, db.singleWriteCount(), "every large event is written at once")
	assert.Zero(t, handler.entryCount(), "and none is cached")
}

func TestUpsertEventV3_APayloadAtTheLimitIsCached(t *testing.T) {
	handler, _, _ := newEventsHandler(t, evictionConfig(10))
	atLimit := `"` + strings.Repeat("x", maxCachedDetailsBytes-2) + `"`
	require.Len(t, atLimit, maxCachedDetailsBytes)

	require.NoError(t, upsertEvent(handler, keyN(1), 1, atLimit))

	assert.EqualValues(t, 1, handler.entryCount())
}

func TestBulkUpsertEventsV3_WritesMissesInOneFlushAndLargeEventsInOneDirectCall(t *testing.T) {
	handler, db, flushed := newEventsHandler(t, evictionConfig(10))
	require.NoError(t, upsertEvent(handler, keyN(1), 1, `{"n":1}`)) // key 1 is now cached
	large := `"` + strings.Repeat("x", maxCachedDetailsBytes) + `"`

	err := handler.BulkUpsertEventsV3(context.Background(), []data_access.EventV3UpsertRecord{
		eventRecord(keyN(1), 2, `{"n":2}`), // a hit: held for a flush
		eventRecord(keyN(2), 2, `{"n":2}`), // a miss
		eventRecord(keyN(3), 2, large),     // too large to cache
		eventRecord(keyN(4), 2, `{"n":2}`), // a miss
	})

	require.NoError(t, err)
	require.Equal(t, 1, db.bulkWriteCount(), "one direct call, for the large event")
	require.Len(t, db.bulkWrites[0], 1)
	assert.Equal(t, "evt-3", db.bulkWrites[0][0].EventName)
	assert.Equal(t, 2, flushed.flushCalls(), "the first miss, then one call for both misses")
	written := flushed.records()
	require.Len(t, written, 3)
	assert.Equal(t, []string{"evt-1", "evt-2", "evt-4"}, []string{written[0].EventName, written[1].EventName, written[2].EventName})
	requireEntryState(t, handler, keyN(1), true)
	requireEntryState(t, handler, keyN(2), false)
	requireEntryState(t, handler, keyN(4), false)
	_, largeCached := handler.lookup(keyN(3))
	assert.False(t, largeCached)
}

func TestBulkUpsertEventsV3_WithNothingToWriteMakesNoCall(t *testing.T) {
	handler, db, flushed := newEventsHandler(t, evictionConfig(10))
	require.NoError(t, upsertEvent(handler, keyN(1), 1, `{"n":1}`))

	require.NoError(t, handler.BulkUpsertEventsV3(context.Background(), []data_access.EventV3UpsertRecord{eventRecord(keyN(1), 2, `{"n":2}`)}))
	require.NoError(t, handler.BulkUpsertEventsV3(context.Background(), nil))

	assert.Zero(t, db.bulkWriteCount())
	assert.Equal(t, 1, flushed.flushCalls(), "only the first miss was written")
}

func TestBulkUpsertEventsV3_AFailureKeepsTheMissesForALaterFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, _, flushed := newEventsHandler(t, evictionConfig(10))
		writeErr := errors.New("database unavailable")
		flushed.err = writeErr
		batch := []data_access.EventV3UpsertRecord{eventRecord(keyN(1), 1, `{"n":1}`), eventRecord(keyN(2), 1, `{"n":1}`)}

		err := handler.BulkUpsertEventsV3(context.Background(), batch)

		require.ErrorIs(t, err, writeErr)
		assert.Equal(t, evictionFlushAttempts, flushed.flushCalls(), "both misses were retried together")
		requireEntryState(t, handler, keyN(1), true)
		requireEntryState(t, handler, keyN(2), true)

		flushed.err = nil
		fireWheel(handler)

		requireEntryState(t, handler, keyN(1), false)
		requireEntryState(t, handler, keyN(2), false)
	})
}

func TestBulkUpsertEventsV3_AFailedLargeEventIsReturnedWithoutAffectingTheMisses(t *testing.T) {
	handler, db, flushed := newEventsHandler(t, evictionConfig(10))
	writeErr := errors.New("database unavailable")
	db.setErr(writeErr)
	large := `"` + strings.Repeat("x", maxCachedDetailsBytes) + `"`

	err := handler.BulkUpsertEventsV3(context.Background(), []data_access.EventV3UpsertRecord{
		eventRecord(keyN(1), 1, `{"n":1}`),
		eventRecord(keyN(2), 1, large),
	})

	require.ErrorIs(t, err, writeErr)
	assert.Equal(t, 1, flushed.flushCalls(), "the miss was written once")
	requireEntryState(t, handler, keyN(1), false)
}

func TestBulkUpsertEventsV3_ARepeatedKeyInOneBatchIsWrittenOnceAndTheRestHeld(t *testing.T) {
	handler, db, flushed := newEventsHandler(t, evictionConfig(10))

	require.NoError(t, handler.BulkUpsertEventsV3(context.Background(), []data_access.EventV3UpsertRecord{
		eventRecord(keyN(1), 1, `{"n":1}`),
		eventRecord(keyN(1), 2, `{"n":2}`),
	}))

	require.Equal(t, 1, flushed.flushCalls())
	assert.Len(t, flushed.records(), 1, "the second event for the key is a hit")
	assert.Zero(t, db.bulkWriteCount())
	cachedEntry := requireEntryState(t, handler, keyN(1), true)
	assert.JSONEq(t, `{"n":2}`, string(cachedEntry.details))
}

func TestHandler_OtherWritesAndReadsGoStraightToTheWrappedHandler(t *testing.T) {
	handler, db, _ := newEventsHandler(t, evictionConfig(10))

	require.NoError(t, handler.UpsertStatsV3(context.Background(), "ns", "ctx", "evt", at(1)))

	assert.Equal(t, 1, db.statsWrites)
	assert.Zero(t, handler.entryCount(), "stats writes do not touch the cache")
	assert.Panics(t, func() { _, _ = handler.GetEventsV3(context.Background(), "ns", "ctx") },
		"reads are not defined on the fake, which shows they reach the wrapped handler")
}

// Run with -race. Events for a small pool of keys are written concurrently, singly
// and in batches, while the wheel advances and entries are evicted.
// Whatever the interleaving, the newest event for every key must be persisted:
// written at once, flushed, or drained at the end.
func TestEvents_ConcurrentWritesNeverLoseTheNewestEvent(t *testing.T) {
	const (
		writers         = 8
		eventsPerWriter = 200
		keyCount        = 24
	)
	db := &recordingDB{}
	flushed := &flushRecorder{}
	handler := newHandlerOver(t, db, evictionConfig(10), flushed.flush, discardMeter())

	var (
		clock      atomic.Int64
		mu         sync.Mutex
		newestSent = map[key]time.Time{}
	)

	writersDone := make(chan struct{})
	advancerDone := make(chan struct{})
	go func() {
		defer close(advancerDone)
		for {
			select {
			case <-writersDone:
				return
			default:
				wheelOf(handler).advance()
			}
		}
	}()

	var wg sync.WaitGroup
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for eventIndex := range eventsPerWriter {
				sequence := int(clock.Add(1))
				eventKey := keyN((writer*5 + eventIndex) % keyCount)
				details := `{"sequence":` + strconv.Itoa(sequence) + `}`
				mu.Lock()
				if at(sequence).After(newestSent[eventKey]) {
					newestSent[eventKey] = at(sequence)
				}
				mu.Unlock()
				if eventIndex%3 == 0 {
					_ = handler.BulkUpsertEventsV3(context.Background(), []data_access.EventV3UpsertRecord{eventRecord(eventKey, sequence, details)})
				} else {
					_ = upsertEvent(handler, eventKey, sequence, details)
				}
				if eventIndex%11 == 0 {
					handler.evictInactive(at(sequence))
				}
			}
		}()
	}
	wg.Wait()
	close(writersDone)
	<-advancerDone
	require.NoError(t, handler.Drain(context.Background()))

	persisted := map[persistedEvent]bool{}
	db.mu.Lock()
	for _, record := range db.singleWrites {
		persisted[persistedOf(record)] = true
	}
	for _, batch := range db.bulkWrites {
		for _, record := range batch {
			persisted[persistedOf(record)] = true
		}
	}
	db.mu.Unlock()
	for _, record := range flushed.records() {
		persisted[persistedOf(record)] = true
	}
	for eventKey, newest := range newestSent {
		assert.True(t, persisted[persistedEvent{cachedKey: eventKey, nanos: newest.UnixNano()}], "the newest event for %v was lost", eventKey)
	}
	assertConsistent(t, handler)
}
