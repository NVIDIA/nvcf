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
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// closeTrackingDB is an inner handler that records how often it is closed.
type closeTrackingDB struct {
	data_access.DBHandlerV2
	closeCalls atomic.Int32
	closeErr   error
}

func (db *closeTrackingDB) Close() error {
	db.closeCalls.Add(1)
	return db.closeErr
}

// wheelOf returns the handler's real timing wheel, for tests that inspect it.
func wheelOf(handler *CachingDBHandler) *timingWheel {
	wheel, _ := handler.wheel.(*timingWheel)
	return wheel
}

// lockProbe wraps a scheduler and records whether the handler ever called it
// without holding the cache lock. TryLock only succeeds when nobody holds the
// lock, and these tests use one goroutine, so success means the handler did not
// hold it.
type lockProbe struct {
	scheduler
	handler           *CachingDBHandler
	scheduleCalls     atomic.Int32
	unscheduleCalls   atomic.Int32
	calledWithoutLock atomic.Bool
}

func installLockProbe(handler *CachingDBHandler) *lockProbe {
	probe := &lockProbe{scheduler: handler.wheel, handler: handler}
	handler.wheel = probe
	return probe
}

func (probe *lockProbe) checkCacheLockHeld() {
	if probe.handler.entriesMu.TryLock() {
		probe.handler.entriesMu.Unlock()
		probe.calledWithoutLock.Store(true)
	}
}

func (probe *lockProbe) schedule(scheduledKey key, delay time.Duration) bool {
	probe.scheduleCalls.Add(1)
	probe.checkCacheLockHeld()
	return probe.scheduler.schedule(scheduledKey, delay)
}

func (probe *lockProbe) unschedule(scheduledKey key) bool {
	probe.unscheduleCalls.Add(1)
	probe.checkCacheLockHeld()
	return probe.scheduler.unschedule(scheduledKey)
}

// scheduledSlot returns the slot a key is scheduled in on the handler's wheel.
func scheduledSlot(handler *CachingDBHandler, scheduledKey key) (int, bool) {
	wheelOf(handler).wheelMu.Lock()
	defer wheelOf(handler).wheelMu.Unlock()
	slot, scheduled := wheelOf(handler).keyLookup[scheduledKey]
	return slot, scheduled
}

// makePending sends two events so the key's entry is pending, and so scheduled.
func makePending(handler *CachingDBHandler, pendingKey key, firstSecond int) {
	handler.processEvent(pendingKey, event{timestamp: at(firstSecond)}, at(firstSecond))
	handler.processEvent(pendingKey, event{timestamp: at(firstSecond + 1)}, at(firstSecond+1))
}

func TestProcessEvent_SchedulesAFlushOnlyWhenAnEntryBecomesPending(t *testing.T) {
	tests := []struct {
		name          string
		steps         []event
		wantOutcome   outcome
		wantScheduled int
	}{
		{
			name:          "a miss is written by the caller, not scheduled",
			steps:         []event{{timestamp: at(1)}},
			wantOutcome:   outcomeMiss,
			wantScheduled: 0,
		},
		{
			name:          "a newer event on a clean entry is scheduled",
			steps:         []event{{timestamp: at(1)}, {timestamp: at(2)}},
			wantOutcome:   outcomeBecamePending,
			wantScheduled: 1,
		},
		{
			name:          "a newer event on a pending entry is not scheduled again",
			steps:         []event{{timestamp: at(1)}, {timestamp: at(2)}, {timestamp: at(3)}},
			wantOutcome:   outcomeAlreadyPending,
			wantScheduled: 1,
		},
		{
			name:          "a stale event schedules nothing",
			steps:         []event{{timestamp: at(5)}, {timestamp: at(1)}},
			wantOutcome:   outcomeStale,
			wantScheduled: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _ := newTestHandlerWith(t, evictionConfig(10))

			var lastOutcome outcome
			for _, step := range tt.steps {
				lastOutcome = handler.processEvent(keyN(1), step, step.timestamp)
			}

			assert.Equal(t, tt.wantOutcome, lastOutcome)
			assert.Equal(t, tt.wantScheduled, scheduledKeyCount(t, wheelOf(handler)))
		})
	}
}

func TestProcessEvent_ScheduledFlushComesDueAfterFlushInterval(t *testing.T) {
	handler, _ := newTestHandlerWith(t, evictionConfig(10))
	makePending(handler, keyN(1), 1)

	// evictionConfig has a 10 second flush interval and the wheel ticks every second.
	advanceTimes(wheelOf(handler), 9)
	_, stillScheduled := scheduledSlot(handler, keyN(1))
	assert.True(t, stillScheduled, "must not be due before the flush interval has passed")

	wheelOf(handler).advance()
	_, stillScheduled = scheduledSlot(handler, keyN(1))
	assert.False(t, stillScheduled, "must be due once the flush interval has passed")
}

func TestProcessEvent_KeysBecomingPendingAtDifferentTimesAreSpreadAcrossSlots(t *testing.T) {
	handler, _ := newTestHandlerWith(t, evictionConfig(10))
	makePending(handler, keyN(1), 1)
	advanceTimes(wheelOf(handler), 3)
	makePending(handler, keyN(2), 5)

	firstSlot, firstScheduled := scheduledSlot(handler, keyN(1))
	secondSlot, secondScheduled := scheduledSlot(handler, keyN(2))

	require.True(t, firstScheduled)
	require.True(t, secondScheduled)
	assert.NotEqual(t, firstSlot, secondSlot, "writes must not all arrive on the same tick")

	advanceTimes(wheelOf(handler), 7)
	_, firstStill := scheduledSlot(handler, keyN(1))
	_, secondStill := scheduledSlot(handler, keyN(2))
	assert.False(t, firstStill, "the first key is due ten ticks after it was scheduled")
	assert.True(t, secondStill, "the second key was scheduled three ticks later")
	wheelOf(handler).advance()
	advanceTimes(wheelOf(handler), 2)
	_, secondStill = scheduledSlot(handler, keyN(2))
	assert.False(t, secondStill)
}

func TestNewCachingDBHandler_WheelHasOneSlotPerTickOfFlushInterval(t *testing.T) {
	tests := []struct {
		name          string
		flushInterval time.Duration
		wantSlots     int
	}{
		{name: "default flush interval", flushInterval: 60 * time.Second, wantSlots: 60},
		{name: "longer interval", flushInterval: 90 * time.Second, wantSlots: 90},
		{name: "rounds up to a whole tick", flushInterval: 1500 * time.Millisecond, wantSlots: 2},
		{name: "one tick", flushInterval: time.Second, wantSlots: 1},
		{name: "less than a tick still has one slot", flushInterval: 100 * time.Millisecond, wantSlots: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _ := newTestHandlerWith(t, Config{MaxSize: 10, FlushInterval: tt.flushInterval})

			assert.Equal(t, tt.wantSlots, wheelOf(handler).slotCount)
			assert.Equal(t, wheelTick, wheelOf(handler).tickDuration)
		})
	}
}

func TestNewCachingDBHandler_FlushIntervalLimitBoundsTheWheel(t *testing.T) {
	tests := []struct {
		name          string
		flushInterval time.Duration
		wantSlots     int
		wantErr       error
	}{
		{name: "the limit itself is accepted", flushInterval: time.Hour, wantSlots: 3600},
		{name: "just over the limit is rejected", flushInterval: time.Hour + time.Nanosecond, wantErr: errFlushIntervalTooLong},
		{name: "a day is rejected", flushInterval: 24 * time.Hour, wantErr: errFlushIntervalTooLong},
		// Rejected before any slot is built, so this cannot overflow the slot count
		// or try to allocate one map per second of a huge interval.
		{name: "the largest duration is rejected", flushInterval: time.Duration(math.MaxInt64), wantErr: errFlushIntervalTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewCachingDBHandler(&fakeDB{}, Config{MaxSize: 10, FlushInterval: tt.flushInterval}, discardFlush, noopMeter())

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, handler)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSlots, wheelOf(handler).slotCount)
		})
	}
}

func TestNewCachingDBHandler_DoesNotStartTheWheel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, _ := newTestHandlerWith(t, evictionConfig(10))
		makePending(handler, keyN(1), 1)

		time.Sleep(time.Minute)
		synctest.Wait()

		_, scheduled := scheduledSlot(handler, keyN(1))
		assert.True(t, scheduled, "nothing advances the wheel until Start is called")
	})
}

func TestStart_AdvancesTheHandlersWheel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler, err := NewCachingDBHandler(&closeTrackingDB{}, Config{MaxSize: 10, FlushInterval: 5 * time.Second}, discardFlush, noopMeter())
		require.NoError(t, err)
		makePending(handler, keyN(1), 1)

		handler.Start()
		defer handler.Close()

		time.Sleep(4 * time.Second)
		synctest.Wait()
		_, scheduled := scheduledSlot(handler, keyN(1))
		assert.True(t, scheduled, "not due after four ticks")

		time.Sleep(time.Second)
		synctest.Wait()
		_, scheduled = scheduledSlot(handler, keyN(1))
		assert.False(t, scheduled, "due after the five second flush interval")
	})
}

// synctest.Test fails if a goroutine is still running when the test returns, so
// a Close that left the wheel's goroutine behind would fail this test.
func TestClose_StopsTheWheelAndClosesTheWrappedHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &closeTrackingDB{}
		handler, err := NewCachingDBHandler(inner, evictionConfig(10), discardFlush, noopMeter())
		require.NoError(t, err)
		handler.Start()

		require.NoError(t, handler.Close())

		assert.EqualValues(t, 1, inner.closeCalls.Load())
		makePending(handler, keyN(1), 1)
		time.Sleep(time.Minute)
		synctest.Wait()
		_, scheduled := scheduledSlot(handler, keyN(1))
		assert.True(t, scheduled, "a closed handler's wheel must not advance")
	})
}

func TestClose_WithoutStartClosesTheWrappedHandler(t *testing.T) {
	inner := &closeTrackingDB{}
	handler, err := NewCachingDBHandler(inner, evictionConfig(10), discardFlush, noopMeter())
	require.NoError(t, err)

	require.NoError(t, handler.Close())

	assert.EqualValues(t, 1, inner.closeCalls.Load())
}

func TestClose_ReturnsTheWrappedHandlersError(t *testing.T) {
	closeErr := errors.New("database close failed")
	inner := &closeTrackingDB{closeErr: closeErr}
	handler, err := NewCachingDBHandler(inner, evictionConfig(10), discardFlush, noopMeter())
	require.NoError(t, err)
	handler.Start()

	assert.ErrorIs(t, handler.Close(), closeErr)
}

func TestProcessEvent_EvictingAPendingEntryClearsItsScheduledFlush(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(1))
	makePending(handler, keyN(1), 1)
	_, scheduled := scheduledSlot(handler, keyN(1))
	require.True(t, scheduled)

	handler.processEvent(keyN(2), event{timestamp: at(10)}, at(10))

	assert.Len(t, flushed.records(), 1, "the pending entry was written when it was evicted")
	_, scheduled = scheduledSlot(handler, keyN(1))
	assert.False(t, scheduled, "its scheduled flush is replaced by that write")
	assert.Zero(t, scheduledKeyCount(t, wheelOf(handler)))
}

func TestEvictInactive_ClearsTheScheduledFlushOfAnEvictedEntry(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(10))
	makePending(handler, keyN(1), 1)

	evicted := handler.evictInactive(at(1000))

	assert.Equal(t, 1, evicted)
	assert.Len(t, flushed.records(), 1)
	assert.Zero(t, scheduledKeyCount(t, wheelOf(handler)))
}

func TestProcessEvent_EvictingACleanEntryLeavesOtherScheduledFlushesAlone(t *testing.T) {
	handler, flushed := newTestHandlerWith(t, evictionConfig(2))
	handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))
	makePending(handler, keyN(2), 2)

	// Key 1 is the least recently updated and is clean, so it is the one evicted.
	handler.processEvent(keyN(3), event{timestamp: at(10)}, at(10))

	assert.Empty(t, flushed.records(), "a clean entry is already in the database")
	requireKeys(t, handler, []int{2, 3}, []int{1})
	_, scheduled := scheduledSlot(handler, keyN(2))
	assert.True(t, scheduled, "the pending entry keeps its scheduled flush")
}

// An evicted key must not leave a stale schedule behind, or the entry that
// replaces it would be flushed at the old, earlier deadline.
func TestProcessEvent_RecreatedKeyGetsAFullFlushInterval(t *testing.T) {
	handler, _ := newTestHandlerWith(t, evictionConfig(1))
	makePending(handler, keyN(1), 1)
	advanceTimes(wheelOf(handler), 4)
	handler.processEvent(keyN(2), event{timestamp: at(10)}, at(10))
	handler.processEvent(keyN(1), event{timestamp: at(20)}, at(20))
	handler.processEvent(keyN(1), event{timestamp: at(21)}, at(21))
	_, scheduled := scheduledSlot(handler, keyN(1))
	require.True(t, scheduled, "the new pending entry is scheduled")

	// The first schedule would have come due after six more ticks.
	advanceTimes(wheelOf(handler), 9)
	_, scheduled = scheduledSlot(handler, keyN(1))
	assert.True(t, scheduled, "must wait a full flush interval from when it became pending again")
	wheelOf(handler).advance()
	_, scheduled = scheduledSlot(handler, keyN(1))
	assert.False(t, scheduled)
}

// Scheduling and eviction both change the wheel while the cache lock is held, so
// the wheel cannot get out of step with the entries. Scheduling after the lock was
// released could race with an eviction and leave a pending entry unscheduled, and
// that interleaving is too narrow to hit reliably with concurrent tests, so these
// tests check the lock directly.
func TestProcessEvent_SchedulesWhileHoldingTheCacheLock(t *testing.T) {
	handler, _ := newTestHandlerWith(t, evictionConfig(10))
	probe := installLockProbe(handler)

	makePending(handler, keyN(1), 1)

	require.EqualValues(t, 1, probe.scheduleCalls.Load(), "the entry became pending, so it must be scheduled")
	assert.False(t, probe.calledWithoutLock.Load(), "schedule must run while the cache lock is held")
}

func TestEviction_UnschedulesWhileHoldingTheCacheLock(t *testing.T) {
	tests := []struct {
		name  string
		evict func(handler *CachingDBHandler)
	}{
		{
			name: "size eviction",
			evict: func(handler *CachingDBHandler) {
				handler.processEvent(keyN(2), event{timestamp: at(10)}, at(10))
			},
		},
		{
			name: "inactivity eviction",
			evict: func(handler *CachingDBHandler) {
				handler.evictInactive(at(1000))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _ := newTestHandlerWith(t, evictionConfig(1))
			makePending(handler, keyN(1), 1)
			probe := installLockProbe(handler)

			tt.evict(handler)

			require.Positive(t, probe.unscheduleCalls.Load(), "evicting the entry must unschedule its key")
			assert.False(t, probe.calledWithoutLock.Load(), "unschedule must run while the cache lock is held")
		})
	}
}

// assertWheelWithinPendingEntries checks that every key in the wheel has a
// pending entry in the cache. It must be called when nothing else is running.
func assertWheelWithinPendingEntries(t *testing.T, handler *CachingDBHandler) {
	t.Helper()
	handler.entriesMu.RLock()
	defer handler.entriesMu.RUnlock()
	wheelOf(handler).wheelMu.Lock()
	defer wheelOf(handler).wheelMu.Unlock()
	for scheduledKey := range wheelOf(handler).keyLookup {
		cachedEntry, inCache := handler.entries[scheduledKey]
		if assert.True(t, inCache, "scheduled key %v has no entry", scheduledKey) {
			assert.True(t, cachedEntry.pending, "scheduled key %v has a clean entry", scheduledKey)
		}
	}
}

// Run with -race. Few slots for many keys forces constant eviction while the
// wheel advances and inactive entries are evicted. A key must only be in the
// wheel while its entry is pending, so scheduling and eviction cannot get out of
// step however the goroutines interleave. The cases range from mild to very
// heavy contention, because an interleaving that breaks this is rare.
func TestProcessEvent_ConcurrentEvictionKeepsTheWheelWithinPendingEntries(t *testing.T) {
	tests := []struct {
		name            string
		maxSize         int
		keyCount        int
		writers         int
		eventsPerWriter int
	}{
		{name: "mild contention", maxSize: 5, keyCount: 20, writers: 8, eventsPerWriter: 150},
		{name: "two slots for three keys", maxSize: 2, keyCount: 3, writers: 16, eventsPerWriter: 2000},
		{name: "one slot for two keys", maxSize: 1, keyCount: 2, writers: 16, eventsPerWriter: 2000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _ := newTestHandlerWith(t, evictionConfig(tt.maxSize))
			var clock atomic.Int64

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
			for writer := range tt.writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for eventIndex := range tt.eventsPerWriter {
						sequence := int(clock.Add(1))
						handler.processEvent(keyN((writer+eventIndex)%tt.keyCount), event{timestamp: at(sequence)}, at(sequence))
						if eventIndex%7 == 0 {
							handler.evictInactive(at(sequence))
						}
					}
				}()
			}
			wg.Wait()
			close(writersDone)
			<-advancerDone

			assertWheelWithinPendingEntries(t, handler)
			assertConsistent(t, handler)
		})
	}
}

// Run with -race. Events arrive from several goroutines while the wheel is
// advanced from another, as in production. Afterwards the wheel's index and slots
// must still agree, and once it has turned a full rotation it must be empty.
func TestProcessEvent_ConcurrentEventsWhileTheWheelAdvances(t *testing.T) {
	const (
		writers          = 8
		eventsPerWriter  = 100
		keyCount         = 20
		flushIntervalSec = 10
	)
	handler, _ := newTestHandlerWith(t, evictionConfig(keyCount))
	var clock atomic.Int64

	writersDone := make(chan struct{})
	advancerDone := make(chan struct{})
	go func() {
		defer close(advancerDone)
		for {
			select {
			case <-writersDone:
				advanceTimes(wheelOf(handler), flushIntervalSec+1)
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
				handler.processEvent(keyN((writer+eventIndex)%keyCount), event{timestamp: at(sequence)}, at(sequence))
			}
		}()
	}
	wg.Wait()
	close(writersDone)
	<-advancerDone

	assert.Zero(t, scheduledKeyCount(t, wheelOf(handler)))
	assertConsistent(t, handler)
}
