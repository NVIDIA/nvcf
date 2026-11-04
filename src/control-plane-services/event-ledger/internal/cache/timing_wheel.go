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
	"sync"
	"time"
)

var (
	errInvalidSlotCount = errors.New("cache: timing wheel slot count must be greater than 0")
	errInvalidTick      = errors.New("cache: timing wheel tick must be greater than 0")
	errNilOnFire        = errors.New("cache: timing wheel fire function must not be nil")
)

// timingWheel schedules keys to come due after a delay. It is a circular buffer
// of slots, and one background goroutine advances it a slot per tick. Keys that
// are scheduled at different times land in different slots, so the work they
// trigger is spread over the whole rotation instead of arriving in a burst. The
// wheel only tracks scheduling; what a due key means is up to onFire.
type timingWheel struct {
	slotCount    int
	tickDuration time.Duration
	// onFire receives the keys of each slot as the wheel reaches it. It runs on
	// the wheel's goroutine without the wheel's lock held, so it can schedule
	// keys, but it must not call stop.
	onFire func(dueKeys []key)

	// wheelMu guards the fields below.
	wheelMu sync.Mutex
	// slots holds the keys due when the wheel reaches each slot.
	// It answers "which keys are due in this slot?"
	slots []map[key]struct{}
	// keyLookup records the slot each scheduled key is in, so a key is never in
	// the wheel twice.
	// It answers "is this key scheduled, and where?"
	keyLookup map[key]int
	// cursor is the slot most recently reached.
	cursor int

	// lifecycleMu guards the fields below, and is held while stop waits.
	lifecycleMu sync.Mutex
	running     bool
	stopped     bool
	stopSignal  chan struct{}
	done        chan struct{}
}

// newTimingWheel returns a wheel of slotCount slots that advances one slot every
// tickDuration. The wheel does nothing until start is called.
func newTimingWheel(slotCount int, tickDuration time.Duration, onFire func(dueKeys []key)) (*timingWheel, error) {
	if slotCount <= 0 {
		return nil, errInvalidSlotCount
	}
	if tickDuration <= 0 {
		return nil, errInvalidTick
	}
	if onFire == nil {
		return nil, errNilOnFire
	}
	slots := make([]map[key]struct{}, slotCount)
	for slotIndex := range slots {
		slots[slotIndex] = make(map[key]struct{})
	}
	return &timingWheel{
		slotCount:    slotCount,
		tickDuration: tickDuration,
		onFire:       onFire,
		slots:        slots,
		keyLookup:    make(map[key]int),
		stopSignal:   make(chan struct{}),
		done:         make(chan struct{}),
	}, nil
}

// schedule places scheduledKey in the slot the wheel reaches after delay. The
// delay is rounded up to a whole number of ticks, and kept between one tick and
// one full rotation, so a key is never due in the past and a delay longer than
// the wheel makes it due early, which only brings the work forward. A key that
// is already scheduled is left where it is, so it is never in the wheel twice and
// keeps its earlier deadline. It reports whether the key was newly scheduled.
func (wheel *timingWheel) schedule(scheduledKey key, delay time.Duration) bool {
	ticks := delay / wheel.tickDuration
	if delay%wheel.tickDuration > 0 {
		ticks++
	}
	ticks = min(max(ticks, 1), time.Duration(wheel.slotCount))

	wheel.wheelMu.Lock()
	defer wheel.wheelMu.Unlock()

	if _, alreadyScheduled := wheel.keyLookup[scheduledKey]; alreadyScheduled {
		return false
	}
	slotIndex := (wheel.cursor + int(ticks)) % wheel.slotCount
	wheel.slots[slotIndex][scheduledKey] = struct{}{}
	wheel.keyLookup[scheduledKey] = slotIndex
	return true
}

// unschedule removes scheduledKey from the wheel so it never comes due, and
// reports whether it was scheduled. A key that has already come due is no longer
// in the wheel, so unscheduling it does nothing.
func (wheel *timingWheel) unschedule(scheduledKey key) bool {
	wheel.wheelMu.Lock()
	defer wheel.wheelMu.Unlock()

	slotIndex, scheduled := wheel.keyLookup[scheduledKey]
	if !scheduled {
		return false
	}
	delete(wheel.slots[slotIndex], scheduledKey)
	delete(wheel.keyLookup, scheduledKey)
	return true
}

// advance moves the wheel on by one slot and passes the keys in that slot to
// onFire. Those keys leave the wheel first, so they can be scheduled again.
func (wheel *timingWheel) advance() {
	dueKeys := wheel.takeDueKeys()
	if len(dueKeys) > 0 {
		wheel.onFire(dueKeys)
	}
}

func (wheel *timingWheel) takeDueKeys() []key {
	wheel.wheelMu.Lock()
	defer wheel.wheelMu.Unlock()

	wheel.cursor = (wheel.cursor + 1) % wheel.slotCount
	dueSlot := wheel.slots[wheel.cursor]
	wheel.slots[wheel.cursor] = make(map[key]struct{})

	dueKeys := make([]key, 0, len(dueSlot))
	for dueKey := range dueSlot {
		delete(wheel.keyLookup, dueKey)
		dueKeys = append(dueKeys, dueKey)
	}
	return dueKeys
}

// start begins advancing the wheel in a background goroutine. It does nothing
// if the wheel is already running or has been stopped.
func (wheel *timingWheel) start() {
	wheel.lifecycleMu.Lock()
	defer wheel.lifecycleMu.Unlock()

	if wheel.running || wheel.stopped {
		return
	}
	wheel.running = true
	go wheel.run()
}

func (wheel *timingWheel) run() {
	defer close(wheel.done)
	ticker := time.NewTicker(wheel.tickDuration)
	defer ticker.Stop()
	for {
		select {
		case <-wheel.stopSignal:
			return
		case <-ticker.C:
			wheel.advance()
		}
	}
}

// stop ends the background goroutine and waits for it to exit. Keys still in the
// wheel stay there. It is safe to call more than once, or without start, and a
// stopped wheel cannot be started again.
func (wheel *timingWheel) stop() {
	wheel.lifecycleMu.Lock()
	defer wheel.lifecycleMu.Unlock()

	if wheel.stopped {
		return
	}
	wheel.stopped = true
	close(wheel.stopSignal)
	if wheel.running {
		<-wheel.done
	}
}
