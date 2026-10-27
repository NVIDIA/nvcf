// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package retry decides what the uploader does after a backend fault: hold
// every submit, retry one item, quarantine it, poll it, or resubmit it. It
// also paces creates per pod and trips a breaker after a run of quarantines.
//
// Nothing here exits the process or touches readiness. Every type takes the
// current time from its caller, so the decisions are deterministic in tests.
package retry

import (
	"errors"
	"fmt"
	"time"
)

// AmbiguousPolicy says what to do with a submit whose outcome is unknown and
// for which no job id came back.
type AmbiguousPolicy string

const (
	// AmbiguousResubmit submits the work again and counts it as a possible
	// duplicate. Duplicates are accepted because the consumer deduplicates;
	// holding instead turns a rare duplicate into loss unless someone acts.
	AmbiguousResubmit AmbiguousPolicy = "resubmit"
	// AmbiguousRetain keeps the work and waits for an operator.
	AmbiguousRetain AmbiguousPolicy = "retain"
)

// Config bounds fault handling. DefaultConfig holds the design's proposed
// values; Validate rejects anything out of range instead of correcting it.
type Config struct {
	// Transient paces holds for backend outages and rate limits, and the
	// per-item retries, with decorrelated jitter.
	Transient Backoff
	// Auth paces holds after the destination refuses the credentials. It
	// doubles from Base to Max with no jitter.
	Auth Backoff
	// RetryAfterMax caps a destination's Retry-After, so a bad header cannot
	// stop uploads for longer than this. Default DefaultRetryAfterMax.
	RetryAfterMax time.Duration
	// ResumeJitter is the most random delay added at the end of each hold,
	// so pods that held together do not resume together. Default
	// DefaultResumeJitter.
	ResumeJitter time.Duration
	// CreatesPerMinute is the per-pod budget for creating jobs.
	CreatesPerMinute int
	// CreateBurst is how many creates may start back to back when the budget
	// has been idle. Default DefaultCreateBurst.
	CreateBurst int
	// CorrelationWindow is how far back unknown faults are correlated.
	CorrelationWindow time.Duration
	// BackendScopeSegments is how many distinct items failing with one
	// reason inside the window make an unknown fault backend scope.
	BackendScopeSegments int
	// SegmentScopeFailures is how many failures of one item, while other
	// items succeed, make an unknown fault segment scope. It also bounds
	// retries of a retryable segment-scope fault.
	SegmentScopeFailures int
	// QuarantineTrip is how many quarantines in a row trip the breaker. A
	// tripped breaker stays tripped until the process restarts; a later
	// success does not close it. Default DefaultQuarantineTrip.
	QuarantineTrip int
	// AmbiguousWithoutID applies to an ambiguous submit with no job id on a
	// backend that is not resubmit safe.
	AmbiguousWithoutID AmbiguousPolicy
}

// maxSetting bounds every duration setting. It is far above any useful value
// and keeps every sum and product of delays well inside time.Duration.
const maxSetting = 24 * time.Hour

// Defaults for Config.
const (
	DefaultTransientBase = 5 * time.Second
	DefaultTransientMax  = 5 * time.Minute
	DefaultAuthBase      = 60 * time.Second
	DefaultAuthMax       = 30 * time.Minute
	// DefaultRetryAfterMax caps a Retry-After at the same 30 minutes as the
	// longest auth hold.
	DefaultRetryAfterMax = 30 * time.Minute
	// DefaultResumeJitter spreads the first create after a hold over up to
	// 30 seconds per pod.
	DefaultResumeJitter     = 30 * time.Second
	DefaultCreatesPerMinute = 30
	// DefaultCreateBurst of 1 spaces creates evenly at the budget rate, so a
	// backlog after a hold drains without a burst.
	DefaultCreateBurst          = 1
	DefaultCorrelationWindow    = 10 * time.Minute
	DefaultBackendScopeSegments = 3
	DefaultSegmentScopeFailures = 5
	// DefaultQuarantineTrip quarantines in a row trip the breaker, which then
	// stays tripped until restart.
	DefaultQuarantineTrip     = 5
	DefaultAmbiguousWithoutID = AmbiguousResubmit
)

// DefaultConfig returns the defaults above.
func DefaultConfig() Config {
	return Config{
		Transient:            Backoff{Base: DefaultTransientBase, Max: DefaultTransientMax},
		Auth:                 Backoff{Base: DefaultAuthBase, Max: DefaultAuthMax},
		RetryAfterMax:        DefaultRetryAfterMax,
		ResumeJitter:         DefaultResumeJitter,
		CreatesPerMinute:     DefaultCreatesPerMinute,
		CreateBurst:          DefaultCreateBurst,
		CorrelationWindow:    DefaultCorrelationWindow,
		BackendScopeSegments: DefaultBackendScopeSegments,
		SegmentScopeFailures: DefaultSegmentScopeFailures,
		QuarantineTrip:       DefaultQuarantineTrip,
		AmbiguousWithoutID:   DefaultAmbiguousWithoutID,
	}
}

// Validate reports every out-of-range setting.
func (c Config) Validate() error {
	var errs []error
	errs = append(errs, c.Transient.validate("transient backoff"), c.Auth.validate("auth backoff"))
	if c.RetryAfterMax <= 0 || c.RetryAfterMax > maxSetting {
		errs = append(errs, fmt.Errorf("retry-after cap must be positive and at most %v, got %v", maxSetting, c.RetryAfterMax))
	}
	if c.ResumeJitter < 0 || c.ResumeJitter > maxSetting {
		errs = append(errs, fmt.Errorf("resume jitter must be between 0 and %v, got %v", maxSetting, c.ResumeJitter))
	}
	positive := []struct {
		name  string
		value int
	}{
		{"creates per minute", c.CreatesPerMinute},
		{"create burst", c.CreateBurst},
		{"backend scope segments", c.BackendScopeSegments},
		{"segment scope failures", c.SegmentScopeFailures},
		{"quarantine trip", c.QuarantineTrip},
	}
	for _, setting := range positive {
		if setting.value < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1, got %d", setting.name, setting.value))
		}
	}
	if c.CorrelationWindow <= 0 || c.CorrelationWindow > maxSetting {
		errs = append(errs, fmt.Errorf("correlation window must be positive and at most %v, got %v", maxSetting, c.CorrelationWindow))
	}
	switch c.AmbiguousWithoutID {
	case AmbiguousResubmit, AmbiguousRetain:
	default:
		errs = append(errs, fmt.Errorf("ambiguous submit policy must be %q or %q, got %q", AmbiguousResubmit, AmbiguousRetain, c.AmbiguousWithoutID))
	}
	return errors.Join(errs...)
}
