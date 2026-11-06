// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskguard

import (
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultMinFreeBytes leaves room for Dynamo to finish its active
	// segment while the uploader stops preparing.
	DefaultMinFreeBytes          = int64(512) << 20
	DefaultPrepareAhead          = 16
	DefaultHighWatermark         = 0.85
	DefaultQuarantineMaxFraction = 0.2
	DefaultQuarantineMaxAge      = 7 * 24 * time.Hour
)

// Policy is what the guard deletes when the spool is above the high
// watermark. Every deleted item is counted as loss.
type Policy string

const (
	// PolicyQuarantineThenOldest deletes quarantined entries first, then the
	// oldest closed segments.
	PolicyQuarantineThenOldest Policy = "quarantine-then-oldest"
	// PolicyOldest deletes the oldest closed segments and keeps quarantine.
	PolicyOldest Policy = "oldest"
	// PolicyNone deletes nothing; preparing pauses until space frees up.
	PolicyNone Policy = "none"
)

// Config bounds spool use.
type Config struct {
	// MinFreeBytes is the least free space preparing leaves. Before a
	// transport is written, free space must be at least the larger of twice
	// the transport estimate and this floor.
	MinFreeBytes int64
	// PrepareAhead bounds the prepared segments waiting for a batch. It must
	// be at least the batch size, so a full batch can form.
	PrepareAhead int
	// HighWatermark is the used fraction of the spool above which preparing
	// pauses and Policy applies.
	HighWatermark float64
	Policy        Policy
	// QuarantineMaxFraction bounds quarantine to a fraction of the spool.
	QuarantineMaxFraction float64
	// QuarantineMaxAge bounds how long a quarantined entry is kept.
	QuarantineMaxAge time.Duration
}

// DefaultConfig returns the proposed defaults.
func DefaultConfig() Config {
	return Config{
		MinFreeBytes:          DefaultMinFreeBytes,
		PrepareAhead:          DefaultPrepareAhead,
		HighWatermark:         DefaultHighWatermark,
		Policy:                PolicyQuarantineThenOldest,
		QuarantineMaxFraction: DefaultQuarantineMaxFraction,
		QuarantineMaxAge:      DefaultQuarantineMaxAge,
	}
}

// Validate rejects invalid or unknown values.
func (c Config) Validate() error {
	var errs []error
	if c.MinFreeBytes < 0 {
		errs = append(errs, fmt.Errorf("disk guard minimum free bytes must not be negative, got %d", c.MinFreeBytes))
	}
	if c.PrepareAhead < 1 {
		errs = append(errs, fmt.Errorf("disk guard prepare-ahead must be at least 1, got %d", c.PrepareAhead))
	}
	if !(c.HighWatermark > 0 && c.HighWatermark < 1) {
		errs = append(errs, fmt.Errorf("disk guard high watermark must be between 0 and 1, got %v", c.HighWatermark))
	}
	switch c.Policy {
	case PolicyQuarantineThenOldest, PolicyOldest, PolicyNone:
	default:
		errs = append(errs, fmt.Errorf("disk guard policy must be %q, %q or %q, got %q", PolicyQuarantineThenOldest, PolicyOldest, PolicyNone, c.Policy))
	}
	if !(c.QuarantineMaxFraction > 0 && c.QuarantineMaxFraction <= 1) {
		errs = append(errs, fmt.Errorf("quarantine maximum fraction must be above 0 and at most 1, got %v", c.QuarantineMaxFraction))
	}
	if c.QuarantineMaxAge <= 0 {
		errs = append(errs, fmt.Errorf("quarantine maximum age must be positive, got %s", c.QuarantineMaxAge))
	}
	return errors.Join(errs...)
}

// Resolve raises PrepareAhead to batchSegments when it is lower, so a full
// batch can form, and returns a warning for the change.
func (c Config) Resolve(batchSegments int) (Config, []string) {
	if c.PrepareAhead >= batchSegments {
		return c, nil
	}
	warning := fmt.Sprintf("prepare-ahead %d is below the batch size, using %d", c.PrepareAhead, batchSegments)
	c.PrepareAhead = batchSegments
	return c, []string{warning}
}
