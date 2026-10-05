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
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// firedRecorder records the batches of keys a wheel reports as due.
type firedRecorder struct {
	mu      sync.Mutex
	batches [][]key
}

func (recorder *firedRecorder) onFire(dueKeys []key) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.batches = append(recorder.batches, dueKeys)
}

func (recorder *firedRecorder) firedBatches() [][]key {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([][]key(nil), recorder.batches...)
}

func (recorder *firedRecorder) firedCount(scheduledKey key) int {
	count := 0
	for _, batch := range recorder.firedBatches() {
		for _, firedKey := range batch {
			if firedKey == scheduledKey {
				count++
			}
		}
	}
	return count
}

// newTestWheel returns a wheel of slotCount slots with a one second tick. Tests
// move it on with advance, so they do not depend on real time.
func newTestWheel(t *testing.T, slotCount int) (*timingWheel, *firedRecorder) {
	t.Helper()
	recorder := &firedRecorder{}
	wheel, err := newTimingWheel(slotCount, time.Second, recorder.onFire)
	require.NoError(t, err)
	return wheel, recorder
}

func advanceTimes(wheel *timingWheel, ticks int) {
	for range ticks {
		wheel.advance()
	}
}

// scheduledKeyCount counts the keys in the slots and checks it matches the index.
func scheduledKeyCount(t *testing.T, wheel *timingWheel) int {
	t.Helper()
	wheel.wheelMu.Lock()
	defer wheel.wheelMu.Unlock()
	inSlots := 0
	for _, slot := range wheel.slots {
		inSlots += len(slot)
	}
	require.Equal(t, inSlots, len(wheel.keyLookup), "every scheduled key must be in exactly one slot")
	return inSlots
}

func TestNewTimingWheel_RejectsInvalidArguments(t *testing.T) {
	noop := func([]key) {}
	tests := []struct {
		name         string
		slotCount    int
		tickDuration time.Duration
		onFire       func([]key)
		wantErr      error
	}{
		{name: "zero slots", slotCount: 0, tickDuration: time.Second, onFire: noop, wantErr: errInvalidSlotCount},
		{name: "negative slots", slotCount: -1, tickDuration: time.Second, onFire: noop, wantErr: errInvalidSlotCount},
		{name: "zero tick", slotCount: 10, tickDuration: 0, onFire: noop, wantErr: errInvalidTick},
		{name: "negative tick", slotCount: 10, tickDuration: -time.Second, onFire: noop, wantErr: errInvalidTick},
		{name: "nil fire function", slotCount: 10, tickDuration: time.Second, onFire: nil, wantErr: errNilOnFire},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wheel, err := newTimingWheel(tt.slotCount, tt.tickDuration, tt.onFire)

			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, wheel)
		})
	}
}

func TestSchedule_KeyComesDueAfterTheDelayRoundedUpToWholeTicks(t *testing.T) {
	const slotCount = 10
	tests := []struct {
		name      string
		delay     time.Duration
		wantTicks int
	}{
		{name: "zero delay is due on the next tick", delay: 0, wantTicks: 1},
		{name: "negative delay is due on the next tick", delay: -5 * time.Second, wantTicks: 1},
		{name: "less than a tick rounds up", delay: time.Millisecond, wantTicks: 1},
		{name: "exactly one tick", delay: time.Second, wantTicks: 1},
		{name: "one and a half ticks rounds up", delay: 1500 * time.Millisecond, wantTicks: 2},
		{name: "three ticks", delay: 3 * time.Second, wantTicks: 3},
		{name: "a full rotation", delay: slotCount * time.Second, wantTicks: slotCount},
		{name: "longer than the wheel is brought forward", delay: 25 * time.Second, wantTicks: slotCount},
		{name: "huge delay does not overflow", delay: time.Duration(math.MaxInt64), wantTicks: slotCount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wheel, fired := newTestWheel(t, slotCount)
			scheduledKey := keyN(1)
			require.True(t, wheel.schedule(scheduledKey, tt.delay))

			advanceTimes(wheel, tt.wantTicks-1)
			assert.Zero(t, fired.firedCount(scheduledKey), "must not be due before %d ticks", tt.wantTicks)
			wheel.advance()
			assert.Equal(t, 1, fired.firedCount(scheduledKey), "must be due on tick %d", tt.wantTicks)

			advanceTimes(wheel, 2*slotCount)
			assert.Equal(t, 1, fired.firedCount(scheduledKey), "must not come due again")
			assert.Zero(t, scheduledKeyCount(t, wheel))
		})
	}
}

func TestSchedule_WrapsAroundTheWheel(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)
	advanceTimes(wheel, 7)
	scheduledKey := keyN(1)

	// From slot 7, five ticks lands in slot 2 after wrapping past the end.
	require.True(t, wheel.schedule(scheduledKey, 5*time.Second))

	advanceTimes(wheel, 4)
	assert.Zero(t, fired.firedCount(scheduledKey))
	wheel.advance()
	assert.Equal(t, 1, fired.firedCount(scheduledKey))
}

func TestSchedule_SameKeyTwiceDoesNotCreateDuplicate(t *testing.T) {
	tests := []struct {
		name        string
		firstDelay  time.Duration
		secondDelay time.Duration
		wantTicks   int
	}{
		{name: "same delay", firstDelay: 3 * time.Second, secondDelay: 3 * time.Second, wantTicks: 3},
		{name: "later second delay keeps the first deadline", firstDelay: 3 * time.Second, secondDelay: 8 * time.Second, wantTicks: 3},
		{name: "earlier second delay keeps the first deadline", firstDelay: 8 * time.Second, secondDelay: 2 * time.Second, wantTicks: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wheel, fired := newTestWheel(t, 10)
			scheduledKey := keyN(1)

			assert.True(t, wheel.schedule(scheduledKey, tt.firstDelay))
			assert.False(t, wheel.schedule(scheduledKey, tt.secondDelay))

			assert.Equal(t, 1, scheduledKeyCount(t, wheel))
			advanceTimes(wheel, tt.wantTicks-1)
			assert.Zero(t, fired.firedCount(scheduledKey))
			wheel.advance()
			assert.Equal(t, 1, fired.firedCount(scheduledKey))
			advanceTimes(wheel, 20)
			assert.Equal(t, 1, fired.firedCount(scheduledKey), "a duplicate entry would fire a second time")
		})
	}
}

func TestSchedule_KeysInDifferentSlotsComeDueOnDifferentTicks(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)
	for tick := 1; tick <= 3; tick++ {
		require.True(t, wheel.schedule(keyN(tick), time.Duration(tick)*time.Second))
	}

	for tick := 1; tick <= 3; tick++ {
		wheel.advance()
		batches := fired.firedBatches()
		require.Len(t, batches, tick)
		assert.Equal(t, []key{keyN(tick)}, batches[tick-1])
	}
}

func TestSchedule_KeysInTheSameSlotComeDueTogether(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)
	require.True(t, wheel.schedule(keyN(1), 3*time.Second))
	require.True(t, wheel.schedule(keyN(2), 3*time.Second))

	advanceTimes(wheel, 2)
	assert.Empty(t, fired.firedBatches())
	wheel.advance()

	batches := fired.firedBatches()
	require.Len(t, batches, 1)
	assert.ElementsMatch(t, []key{keyN(1), keyN(2)}, batches[0])
}

func TestAdvance_SlotsWithNothingDueDoNotCallOnFire(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)

	advanceTimes(wheel, 25)

	assert.Empty(t, fired.firedBatches())
}

func TestSchedule_KeyCanBeScheduledAgainOnceItHasFired(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)
	scheduledKey := keyN(1)
	require.True(t, wheel.schedule(scheduledKey, time.Second))
	wheel.advance()
	require.Equal(t, 1, fired.firedCount(scheduledKey))

	assert.True(t, wheel.schedule(scheduledKey, 2*time.Second))

	advanceTimes(wheel, 2)
	assert.Equal(t, 2, fired.firedCount(scheduledKey))
}

func TestAdvance_OnFireMayScheduleKeysAgain(t *testing.T) {
	var wheel *timingWheel
	var mu sync.Mutex
	firedTimes := 0
	onFire := func(dueKeys []key) {
		mu.Lock()
		defer mu.Unlock()
		firedTimes++
		if firedTimes == 1 {
			// Rescheduling from the callback must not deadlock on the wheel's lock.
			for _, dueKey := range dueKeys {
				wheel.schedule(dueKey, 2*time.Second)
			}
		}
	}
	wheel, err := newTimingWheel(10, time.Second, onFire)
	require.NoError(t, err)
	require.True(t, wheel.schedule(keyN(1), time.Second))

	advanceTimes(wheel, 1+2)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, firedTimes)
}

// Run with -race. Of many goroutines scheduling one key at once, exactly one
// must be admitted, and the key must come due exactly once.
func TestSchedule_ConcurrentSchedulesOfOneKeyAdmitExactlyOne(t *testing.T) {
	const goroutines = 64
	wheel, fired := newTestWheel(t, 10)
	scheduledKey := keyN(1)
	admitted := make([]bool, goroutines)

	var wg sync.WaitGroup
	for goroutine := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			admitted[goroutine] = wheel.schedule(scheduledKey, time.Duration(goroutine%5+1)*time.Second)
		}()
	}
	wg.Wait()

	admittedCount := 0
	for _, wasAdmitted := range admitted {
		if wasAdmitted {
			admittedCount++
		}
	}
	assert.Equal(t, 1, admittedCount)
	assert.Equal(t, 1, scheduledKeyCount(t, wheel))
	advanceTimes(wheel, 20)
	assert.Equal(t, 1, fired.firedCount(scheduledKey))
}

// Run with -race. Keys are scheduled from several goroutines while the wheel is
// advanced from one, as in production. Every key must come due exactly once.
func TestSchedule_ConcurrentSchedulesWhileAdvancingFireEveryKeyOnce(t *testing.T) {
	const (
		writers       = 8
		keysPerWriter = 50
		slotCount     = 10
	)
	wheel, fired := newTestWheel(t, slotCount)

	writersDone := make(chan struct{})
	advancerDone := make(chan struct{})
	go func() {
		defer close(advancerDone)
		for {
			select {
			case <-writersDone:
				advanceTimes(wheel, slotCount+1)
				return
			default:
				wheel.advance()
			}
		}
	}()

	var wg sync.WaitGroup
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for keyIndex := range keysPerWriter {
				wheel.schedule(keyN(writer*keysPerWriter+keyIndex), time.Duration(keyIndex%slotCount+1)*time.Second)
			}
		}()
	}
	wg.Wait()
	close(writersDone)
	<-advancerDone

	for keyIndex := range writers * keysPerWriter {
		assert.Equal(t, 1, fired.firedCount(keyN(keyIndex)), "key %d", keyIndex)
	}
	assert.Zero(t, scheduledKeyCount(t, wheel))
}

func TestUnschedule_KeyNoLongerComesDue(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)
	scheduledKey := keyN(1)
	require.True(t, wheel.schedule(scheduledKey, 3*time.Second))

	assert.True(t, wheel.unschedule(scheduledKey))

	advanceTimes(wheel, 20)
	assert.Zero(t, fired.firedCount(scheduledKey))
	assert.Zero(t, scheduledKeyCount(t, wheel))
}

func TestUnschedule_UnknownOrAlreadyDueKeyReturnsFalse(t *testing.T) {
	wheel, _ := newTestWheel(t, 10)
	assert.False(t, wheel.unschedule(keyN(1)), "never scheduled")

	require.True(t, wheel.schedule(keyN(2), time.Second))
	wheel.advance()
	assert.False(t, wheel.unschedule(keyN(2)), "already came due and left the wheel")
}

func TestUnschedule_KeyCanBeScheduledAgainWithAFreshDeadline(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)
	scheduledKey := keyN(1)
	require.True(t, wheel.schedule(scheduledKey, 3*time.Second))
	advanceTimes(wheel, 2)
	require.True(t, wheel.unschedule(scheduledKey))

	require.True(t, wheel.schedule(scheduledKey, 5*time.Second))

	// The first schedule would have come due on the next tick.
	advanceTimes(wheel, 4)
	assert.Zero(t, fired.firedCount(scheduledKey))
	wheel.advance()
	assert.Equal(t, 1, fired.firedCount(scheduledKey))
}

func TestUnschedule_OtherKeysInTheSameSlotStillComeDue(t *testing.T) {
	wheel, fired := newTestWheel(t, 10)
	require.True(t, wheel.schedule(keyN(1), 3*time.Second))
	require.True(t, wheel.schedule(keyN(2), 3*time.Second))

	require.True(t, wheel.unschedule(keyN(1)))

	advanceTimes(wheel, 3)
	batches := fired.firedBatches()
	require.Len(t, batches, 1)
	assert.Equal(t, []key{keyN(2)}, batches[0])
}

// Run with -race. Scheduling and unscheduling the same keys from several
// goroutines while the wheel advances must leave the slots and the index in
// agreement.
func TestUnschedule_ConcurrentChurnKeepsSlotsAndIndexInAgreement(t *testing.T) {
	const (
		goroutines = 8
		rounds     = 200
		keyCount   = 10
	)
	wheel, _ := newTestWheel(t, 10)

	writersDone := make(chan struct{})
	advancerDone := make(chan struct{})
	go func() {
		defer close(advancerDone)
		for {
			select {
			case <-writersDone:
				return
			default:
				wheel.advance()
			}
		}
	}()

	var wg sync.WaitGroup
	for goroutine := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := range rounds {
				churnedKey := keyN((goroutine + round) % keyCount)
				wheel.schedule(churnedKey, time.Duration(round%5+1)*time.Second)
				if round%2 == 0 {
					wheel.unschedule(churnedKey)
				}
			}
		}()
	}
	wg.Wait()
	close(writersDone)
	<-advancerDone

	scheduledKeyCount(t, wheel)
}

func TestStart_AdvancesTheWheelOnEveryTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wheel, fired := newTestWheel(t, 5)
		scheduledKey := keyN(1)
		require.True(t, wheel.schedule(scheduledKey, 3*time.Second))

		wheel.start()
		defer wheel.stop()

		time.Sleep(2 * time.Second)
		synctest.Wait()
		assert.Zero(t, fired.firedCount(scheduledKey), "not due after two ticks")

		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, 1, fired.firedCount(scheduledKey), "due on the third tick")
	})
}

func TestStart_CalledTwiceRunsOneGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wheel, fired := newTestWheel(t, 10)
		scheduledKey := keyN(1)
		require.True(t, wheel.schedule(scheduledKey, 3*time.Second))

		wheel.start()
		wheel.start()
		defer wheel.stop()

		// A second goroutine would advance the wheel twice per tick and fire early.
		time.Sleep(2 * time.Second)
		synctest.Wait()
		assert.Zero(t, fired.firedCount(scheduledKey))
		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, 1, fired.firedCount(scheduledKey))
	})
}

// synctest.Test fails if a goroutine is still running when the test returns, so
// a stop that left the goroutine behind would fail these tests.
func TestStop_EndsTheGoroutineAndStopsFiring(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wheel, fired := newTestWheel(t, 10)
		scheduledKey := keyN(1)
		require.True(t, wheel.schedule(scheduledKey, 5*time.Second))
		wheel.start()
		time.Sleep(2 * time.Second)
		synctest.Wait()

		wheel.stop()

		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Zero(t, fired.firedCount(scheduledKey), "a stopped wheel must not fire")
		assert.Equal(t, 1, scheduledKeyCount(t, wheel), "keys stay in a stopped wheel")
	})
}

func TestStop_WithoutStartReturns(t *testing.T) {
	wheel, _ := newTestWheel(t, 10)

	done := make(chan struct{})
	go func() {
		defer close(done)
		wheel.stop()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop blocked on a wheel that was never started")
	}
}

func TestStop_CalledTwiceIsSafe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wheel, _ := newTestWheel(t, 10)
		wheel.start()

		wheel.stop()
		wheel.stop()
	})
}

func TestStart_AfterStopDoesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wheel, fired := newTestWheel(t, 10)
		scheduledKey := keyN(1)
		require.True(t, wheel.schedule(scheduledKey, time.Second))
		wheel.stop()

		wheel.start()

		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Zero(t, fired.firedCount(scheduledKey))
	})
}
