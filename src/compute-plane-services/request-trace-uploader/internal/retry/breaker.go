// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import "sync"

// Breaker trips after a run of segment quarantines with no success between
// them. A tripped breaker means the faults are probably not about single
// segments, so the caller stops preparing new segments and raises
// quarantine_tripped. It stays tripped until Reset, which a restart does by
// building a new one.
type Breaker struct {
	trip int

	mu      sync.Mutex
	run     int
	tripped bool
}

// NewBreaker builds a breaker that trips at trip quarantines in a row, at
// least 1 as Config.Validate checks.
func NewBreaker(trip int) *Breaker {
	return &Breaker{trip: trip}
}

// Quarantined records one quarantine. It returns true only on the call that
// trips the breaker, so the caller emits quarantine_tripped once.
func (b *Breaker) Quarantined() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run++
	if b.tripped || b.run < b.trip {
		return false
	}
	b.tripped = true
	return true
}

// Succeeded records a segment that reached success. It ends the current run
// of quarantines but does not close a tripped breaker.
func (b *Breaker) Succeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run = 0
}

// Tripped reports whether the breaker has tripped.
func (b *Breaker) Tripped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped
}

// Reset closes the breaker and clears the run.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run = 0
	b.tripped = false
}
