// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import "time"

// State is one item's retry state. The journal stores it with the item, so a
// restart resumes the same attempt count and backoff instead of starting
// over.
type State struct {
	Attempts int           `json:"attempts"`
	Delay    time.Duration `json:"delay"`
	NextAt   time.Time     `json:"next_at"`
}

// Failed records one more failed attempt at now and schedules the next one
// with b's decorrelated jitter. r is a uniform random value in [0, 1).
func (s State) Failed(now time.Time, b Backoff, r float64) State {
	delay := b.Decorrelated(s.Delay, r)
	return State{Attempts: s.Attempts + 1, Delay: delay, NextAt: now.Add(delay)}
}

// Due reports whether the next attempt may start at now.
func (s State) Due(now time.Time) bool {
	return !now.Before(s.NextAt)
}
