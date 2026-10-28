// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"fmt"
	"time"
)

// Backoff bounds a delay sequence.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

func (b Backoff) validate(name string) error {
	if b.Base <= 0 || b.Max < b.Base || b.Max > maxSetting {
		return fmt.Errorf("%s needs 0 < base <= max <= %v, got base %v and max %v", name, maxSetting, b.Base, b.Max)
	}
	return nil
}

// Decorrelated returns the delay after prev with decorrelated jitter: a value
// between Base and three times prev, capped at Max. A zero prev counts as
// Base. r is a uniform random value in [0, 1). prev is clamped to Max first,
// so a corrupt stored delay cannot overflow.
func (b Backoff) Decorrelated(prev time.Duration, r float64) time.Duration {
	upper := 3 * min(max(prev, b.Base), b.Max)
	delay := b.Base + time.Duration(r*float64(upper-b.Base))
	return min(delay, b.Max)
}

// Doubling returns Base after a zero prev, then twice prev, capped at Max.
func (b Backoff) Doubling(prev time.Duration) time.Duration {
	if prev <= 0 {
		return b.Base
	}
	return min(2*min(prev, b.Max), b.Max)
}
