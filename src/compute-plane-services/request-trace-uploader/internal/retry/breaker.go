// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"sync"
	"time"
)

// Breaker trips after a run of segment quarantines with no success between
// them. A tripped breaker means the faults are probably not about single
// segments, so the caller stops preparing new segments and raises
// quarantine_tripped. A success does not close it. It closes on its own once
// the resume time has passed since the trip, with a fresh run, so a fault
// that is still there trips it again after the same number of quarantines.
// Quarantines while tripped do not move the resume time.
type Breaker struct {
	trip   int
	resume time.Duration

	mu        sync.Mutex
	run       int
	tripped   bool
	trippedAt time.Time
	resumed   int
}

// NewBreaker builds a breaker that trips at trip quarantines in a row and
// closes resume after each trip. Config.Validate checks both are positive.
func NewBreaker(trip int, resume time.Duration) *Breaker {
	return &Breaker{trip: trip, resume: resume}
}

// QuarantinedAt records one quarantine at now. It returns true only on the
// call that trips the breaker, so the caller emits quarantine_tripped once
// per trip.
func (b *Breaker) QuarantinedAt(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resumeLocked(now)
	b.run++
	if b.tripped || b.run < b.trip {
		return false
	}
	b.tripped = true
	b.trippedAt = now
	return true
}

// Quarantined is QuarantinedAt at the wall clock.
func (b *Breaker) Quarantined() bool { return b.QuarantinedAt(time.Now()) }

// Succeeded records a segment that reached success. It ends the current run
// of quarantines but does not close a tripped breaker.
func (b *Breaker) Succeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run = 0
}

// TrippedAt reports whether the breaker is tripped at now. A breaker whose
// resume time has passed closes here.
func (b *Breaker) TrippedAt(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resumeLocked(now)
	return b.tripped
}

// Tripped is TrippedAt at the wall clock.
func (b *Breaker) Tripped() bool { return b.TrippedAt(time.Now()) }

// Resumed returns how many times the breaker closed on its own since the
// last call, so the caller counts each resume once.
func (b *Breaker) Resumed() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.resumed
	b.resumed = 0
	return n
}

// Reset closes the breaker and clears the run. It is not counted as a
// resume.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run = 0
	b.tripped = false
	b.trippedAt = time.Time{}
}

func (b *Breaker) resumeLocked(now time.Time) {
	if !b.tripped || now.Sub(b.trippedAt) < b.resume {
		return
	}
	b.run = 0
	b.tripped = false
	b.trippedAt = time.Time{}
	b.resumed++
}
