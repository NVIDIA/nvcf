// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"context"
	"sync"
	"time"
)

// Limiter is the per-pod budget for creating jobs: a token bucket that
// refills at a fixed rate and holds at most burst tokens. It also caps how
// fast a backlog drains after a hold, since every create draws from it.
type Limiter struct {
	interval time.Duration
	burst    float64
	now      func() time.Time

	mu     sync.Mutex
	tokens float64
	last   time.Time
	// seq numbers reservations, so a canceled one is refunded only when no
	// later reservation was scheduled behind it.
	seq uint64
}

// NewLimiter builds a budget of perMinute creates with the given burst.
// Config.Validate requires both to be at least 1; a lower value is treated
// as 1 here so a direct call cannot divide by zero. The bucket starts full.
func NewLimiter(perMinute, burst int) *Limiter {
	return &Limiter{
		interval: time.Minute / time.Duration(max(perMinute, 1)),
		burst:    float64(max(burst, 1)),
		now:      time.Now,
	}
}

// Reserve takes one create from the budget at now and returns how long the
// caller must wait before starting it.
func (l *Limiter) Reserve(now time.Time) time.Duration {
	wait, _ := l.reserve(now)
	return wait
}

func (l *Limiter) reserve(now time.Time) (time.Duration, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last.IsZero() {
		l.tokens, l.last = l.burst, now
	}
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens = min(l.burst, l.tokens+float64(elapsed)/float64(l.interval))
		l.last = now
	}
	l.tokens--
	l.seq++
	if l.tokens >= 0 {
		return 0, l.seq
	}
	return time.Duration(-l.tokens * float64(l.interval)), l.seq
}

// Wait takes one create from the budget and blocks until it may start. If ctx
// ends first, the create goes back to the budget, unless a later reservation
// is already scheduled behind it: returning it then would give its slot to
// two creates.
func (l *Limiter) Wait(ctx context.Context) error {
	wait, seq := l.reserve(l.now())
	if wait <= 0 {
		return nil
	}
	if err := sleep(ctx, wait); err != nil {
		l.mu.Lock()
		if l.seq == seq {
			l.tokens++
		}
		l.mu.Unlock()
		return err
	}
	return nil
}
