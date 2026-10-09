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

// makePendingWithPayload makes a key's entry pending with a payload that names its key.
func makePendingWithPayload(handler *CachingDBHandler, pendingKey key, firstSecond int) {
	for second := firstSecond; second <= firstSecond+1; second++ {
		handler.processEvent(pendingKey, event{
			timestamp: at(second),
			source:    "source-" + pendingKey.eventName,
			details:   json.RawMessage(`{"second":` + strconv.Itoa(second) + `}`),
		}, at(second))
	}
}

// fireWheel advances the handler's wheel through one full flush interval of the
// test config, which makes every key scheduled before it come due.
func fireWheel(handler *CachingDBHandler) {
	advanceTimes(wheelOf(handler), wheelOf(handler).slotCount)
}

func requireEntryState(t *testing.T, handler *CachingDBHandler, cachedKey key, wantPending bool) entry {
	t.Helper()
	cachedEntry, ok := handler.lookup(cachedKey)
	require.True(t, ok, "entry for %v is missing", cachedKey)
	require.Equal(t, wantPending, cachedEntry.pending, "pending state of %v", cachedKey)
	return cachedEntry
}

func TestFlushDue_WritesAllDuePendingEntriesInOneBatch(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	for keyIndex := 1; keyIndex <= 3; keyIndex++ {
		makePendingWithPayload(handler, keyN(keyIndex), keyIndex)
	}

	fireWheel(handler)

	assert.Equal(t, 1, flushed.flushCalls(), "one write for the whole slot")
	records := flushed.records()
	require.Len(t, records, 3)
	for _, record := range records {
		assert.Equal(t, "source-"+record.EventName, record.Source)
		assert.JSONEq(t, `{"second":`+strconv.Itoa(int(record.Timestamp.Sub(testBase)/time.Second))+`}`, string(record.Details))
	}
	for keyIndex := 1; keyIndex <= 3; keyIndex++ {
		cachedEntry := requireEntryState(t, handler, keyN(keyIndex), false)
		assert.False(t, cachedEntry.lastWritten.IsZero())
	}
	assert.Zero(t, scheduledKeyCount(t, wheelOf(handler)), "a written entry is not scheduled again")
}

func TestFlushDue_WritesTheLatestValueUsingItsOwnTimestamp(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	makePendingWithPayload(handler, keyN(1), 1)
	handler.processEvent(keyN(1), event{timestamp: at(5), source: "latest", details: json.RawMessage(`{"latest":true}`)}, at(5))

	fireWheel(handler)

	require.Equal(t, []data_access.EventV3UpsertRecord{{
		Namespace: "ns", Context: "ctx", EventName: "evt-1",
		Source: "latest", Details: json.RawMessage(`{"latest":true}`), Timestamp: at(5),
	}}, flushed.records())
}

func TestFlushDue_SkipsKeysWithoutAPendingEntry(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1)) // a miss: clean

	handler.flushDue([]key{keyN(1), keyN(99)})

	assert.Zero(t, flushed.flushCalls(), "nothing is pending, so nothing is written")
}

func TestFlushDue_AFailedWriteKeepsTheEntryPendingAndSchedulesItAgain(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	flushed.err = errors.New("database unavailable")
	makePendingWithPayload(handler, keyN(1), 1)

	fireWheel(handler)

	assert.Equal(t, 1, flushed.flushCalls())
	requireEntryState(t, handler, keyN(1), true)
	_, scheduled := scheduledSlot(handler, keyN(1))
	assert.True(t, scheduled, "a failed flush is retried")

	flushed.err = nil
	fireWheel(handler)

	assert.Equal(t, 2, flushed.flushCalls())
	requireEntryState(t, handler, keyN(1), false)
	assert.Zero(t, scheduledKeyCount(t, wheelOf(handler)))
}

// blockingFlush is a flush function whose first call waits to be released.
type blockingFlush struct {
	recorder *flushRecorder
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	blocked  atomic.Bool
}

func newBlockingFlush(t *testing.T) *blockingFlush {
	t.Helper()
	blocking := &blockingFlush{recorder: &flushRecorder{}, started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(blocking.releaseFlush)
	return blocking
}

func (blocking *blockingFlush) releaseFlush() { blocking.once.Do(func() { close(blocking.release) }) }

func (blocking *blockingFlush) flush(ctx context.Context, recs []data_access.EventV3UpsertRecord) error {
	err := blocking.recorder.flush(ctx, recs)
	// Only the first call waits, and later calls, including ones made on the test's
	// own goroutine while the first waits, return at once.
	if blocking.blocked.CompareAndSwap(false, true) {
		close(blocking.started)
		<-blocking.release
	}
	return err
}

// fireWheelWhileBlocked fires the wheel on another goroutine and waits until its
// write is blocked. The returned function releases the write and waits for the fire
// to finish.
func fireWheelWhileBlocked(handler *CachingDBHandler, blocking *blockingFlush) (finish func()) {
	fireDone := make(chan struct{})
	go func() {
		defer close(fireDone)
		fireWheel(handler)
	}()
	<-blocking.started
	return func() {
		blocking.releaseFlush()
		<-fireDone
	}
}

func newBlockedHandler(t *testing.T, cfg Config) (*CachingDBHandler, *blockingFlush) {
	t.Helper()
	blocking := newBlockingFlush(t)
	return newHandlerOver(t, &fakeDB{}, cfg, blocking.flush, discardMeter()), blocking
}

// An event that arrives while its entry is being written must not be marked as
// written, and must be written by the next flush.
func TestFlushDue_ANewerEventDuringTheWriteIsNotLost(t *testing.T) {
	handler, blocking := newBlockedHandler(t, evictionConfig(10))
	makePendingWithPayload(handler, keyN(1), 1)

	finishFire := fireWheelWhileBlocked(handler, blocking)

	got := handler.processEvent(keyN(1), event{timestamp: at(9), source: "during", details: json.RawMessage(`{"during":true}`)}, at(9))
	assert.Equal(t, outcomeAlreadyPending, got, "the entry is still pending while its write is in flight")
	finishFire()

	cachedEntry := requireEntryState(t, handler, keyN(1), true)
	assert.Equal(t, "during", cachedEntry.source, "the newer event is kept")
	_, scheduled := scheduledSlot(handler, keyN(1))
	assert.True(t, scheduled, "and it is scheduled to be written")

	fireWheel(handler)

	records := blocking.recorder.records()
	require.Len(t, records, 2)
	assert.Equal(t, "during", records[1].Source, "the next flush writes the newer event")
	requireEntryState(t, handler, keyN(1), false)
}

// A write that started for an entry must not mark a different entry as written,
// even if the key was evicted and came back with the same number of events.
func TestFlushDue_AnOldWriteDoesNotMarkARecreatedEntryAsWritten(t *testing.T) {
	handler, blocking := newBlockedHandler(t, evictionConfig(1))
	makePendingWithPayload(handler, keyN(1), 1)

	finishFire := fireWheelWhileBlocked(handler, blocking)

	// Evict key 1, then bring it back as a fresh pending entry.
	handler.processEvent(keyN(2), event{timestamp: at(20)}, at(20))
	makePendingWithPayload(handler, keyN(1), 30)
	finishFire()

	cachedEntry := requireEntryState(t, handler, keyN(1), true)
	assert.Equal(t, at(31), cachedEntry.timestamp, "the new entry was not marked written by the old flush")
}

// If the key came back as a clean miss while an old write was in flight, the old
// write must not schedule a flush for it, since only pending entries are scheduled.
func TestFlushDue_AnOldWriteDoesNotScheduleARecreatedCleanEntry(t *testing.T) {
	handler, blocking := newBlockedHandler(t, evictionConfig(1))
	makePendingWithPayload(handler, keyN(1), 1)

	finishFire := fireWheelWhileBlocked(handler, blocking)

	handler.processEvent(keyN(2), event{timestamp: at(20)}, at(20))
	handler.processEvent(keyN(1), event{timestamp: at(30)}, at(30)) // back as a clean miss
	finishFire()

	requireEntryState(t, handler, keyN(1), false)
	assert.Zero(t, scheduledKeyCount(t, wheelOf(handler)))
	assertWheelWithinPendingEntries(t, handler)
}

func TestFlushDue_WritesWithoutHoldingTheCacheLock(t *testing.T) {
	var handler *CachingDBHandler
	var lockWasFree bool
	flush := func(_ context.Context, _ []data_access.EventV3UpsertRecord) error {
		if lockWasFree = handler.entriesMu.TryLock(); lockWasFree {
			handler.entriesMu.Unlock()
		}
		return nil
	}
	handler = newHandlerOver(t, &fakeDB{}, evictionConfig(10), flush, discardMeter())
	makePendingWithPayload(handler, keyN(1), 1)

	fireWheel(handler)

	assert.True(t, lockWasFree, "a flush must not run while the cache lock is held")
}

func TestFlushDue_CountsWritesByResult(t *testing.T) {
	handler, flushed, reader := newMetricsHandler(t, evictionConfig(10))
	makePendingWithPayload(handler, keyN(1), 1)
	makePendingWithPayload(handler, keyN(2), 3)

	fireWheel(handler)
	requireCounter(t, reader, 2, flushesMetricName, successfulFlush)
	requireCounter(t, reader, 0, flushesMetricName, failedFlush)

	flushed.err = errors.New("database unavailable")
	makePendingWithPayload(handler, keyN(3), 10)
	fireWheel(handler)
	requireCounter(t, reader, 2, flushesMetricName, successfulFlush)
	requireCounter(t, reader, 1, flushesMetricName, failedFlush)
}

func TestStart_FlushesAPendingEntryOnceTheFlushIntervalHasPassed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		flushed := &flushRecorder{}
		handler := newHandlerOver(t, &closeTrackingDB{}, Config{MaxSize: 10, FlushInterval: 5 * time.Second}, flushed.flush, discardMeter())
		makePendingWithPayload(handler, keyN(1), 1)
		handler.Start()
		defer handler.Close()

		time.Sleep(3 * time.Second)
		synctest.Wait()
		assert.Zero(t, flushed.flushCalls(), "not written before the flush interval")

		time.Sleep(3 * time.Second)
		synctest.Wait()
		assert.Equal(t, 1, flushed.flushCalls(), "written once the interval has passed")
		requireEntryState(t, handler, keyN(1), false)
	})
}

func TestDrain_WritesEveryPendingEntryInBatches(t *testing.T) {
	const pendingEntries = drainBatchSize*2 + 200
	handler, flushed := newTestHandlerWith(t, Config{MaxSize: pendingEntries * 2, FlushInterval: 10 * time.Second})
	for keyIndex := range pendingEntries {
		makePendingWithPayload(handler, keyN(keyIndex), 1)
	}

	require.NoError(t, handler.Drain(context.Background()))

	assert.Equal(t, 3, flushed.flushCalls(), "500 + 500 + 200")
	assert.Len(t, flushed.records(), pendingEntries)
	for keyIndex := range pendingEntries {
		requireEntryState(t, handler, keyN(keyIndex), false)
	}
}

func TestDrain_WritesTheLeastRecentlyUpdatedEntriesFirst(t *testing.T) {
	const pendingEntries = drainBatchSize + 10
	handler, flushed := newTestHandlerWith(t, Config{MaxSize: pendingEntries * 2, FlushInterval: 10 * time.Second})
	for keyIndex := range pendingEntries {
		makePendingWithPayload(handler, keyN(keyIndex), 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler.flush = func(flushCtx context.Context, records []data_access.EventV3UpsertRecord) error {
		err := flushed.flush(flushCtx, records)
		cancel()
		return err
	}

	require.Error(t, handler.Drain(ctx), "the context ended after the first batch")

	written := map[string]bool{}
	for _, record := range flushed.records() {
		written[record.EventName] = true
	}
	require.Len(t, written, drainBatchSize)
	assert.True(t, written[keyN(0).eventName], "the oldest entry went in the first batch")
	assert.False(t, written[keyN(pendingEntries-1).eventName], "the newest did not")
}

func TestDrain_StopsTheWheel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		flushed := &flushRecorder{}
		handler := newHandlerOver(t, &closeTrackingDB{}, Config{MaxSize: 10, FlushInterval: 5 * time.Second}, flushed.flush, discardMeter())
		handler.Start()

		require.NoError(t, handler.Drain(context.Background()))

		makePendingWithPayload(handler, keyN(1), 1)
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Zero(t, flushed.flushCalls(), "a drained handler's wheel does not run")
	})
}

func TestDrain_WithNothingPendingWritesNothing(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

	require.NoError(t, handler.Drain(context.Background()))

	assert.Zero(t, flushed.flushCalls())
}

func TestDrain_AFailedWriteIsReturnedAndLeavesTheEntriesPending(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	flushed.err = errors.New("database unavailable")
	makePendingWithPayload(handler, keyN(1), 1)

	err := handler.Drain(context.Background())

	require.ErrorIs(t, err, flushed.err)
	requireEntryState(t, handler, keyN(1), true)
}

func TestDrain_StopsWhenTheContextEnds(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	makePendingWithPayload(handler, keyN(1), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := handler.Drain(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, flushed.flushCalls())
	requireEntryState(t, handler, keyN(1), true)
}

func TestClose_DrainsPendingEntriesBeforeClosingTheWrappedHandler(t *testing.T) {
	inner := &closeTrackingDB{}
	flushed := &flushRecorder{}
	handler := newHandlerOver(t, inner, evictionConfig(10), flushed.flush, discardMeter())
	makePendingWithPayload(handler, keyN(1), 1)

	require.NoError(t, handler.Close())

	assert.Len(t, flushed.records(), 1, "the pending entry was written")
	assert.EqualValues(t, 1, inner.closeCalls.Load())
}

func TestClose_ReportsADrainFailureAndStillClosesTheWrappedHandler(t *testing.T) {
	closeErr := errors.New("database close failed")
	inner := &closeTrackingDB{closeErr: closeErr}
	flushed := &flushRecorder{err: errors.New("database unavailable")}
	handler := newHandlerOver(t, inner, evictionConfig(10), flushed.flush, discardMeter())
	makePendingWithPayload(handler, keyN(1), 1)

	err := handler.Close()

	require.ErrorIs(t, err, flushed.err)
	require.ErrorIs(t, err, closeErr)
	assert.EqualValues(t, 1, inner.closeCalls.Load())
}
