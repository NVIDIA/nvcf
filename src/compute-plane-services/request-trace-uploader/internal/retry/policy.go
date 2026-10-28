// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"context"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

// Stage says which call failed.
type Stage string

const (
	// StageSubmit means Submit returned an error.
	StageSubmit Stage = "submit"
	// StageStatus means a Status read failed, so the job's state is unknown.
	StageStatus Stage = "status"
	// StageJob means Status read the job and reported StatusFailure.
	StageJob Stage = "job"
)

// Action is what the caller does with the failed item: one segment, or one
// batch of segments submitted as one job.
type Action string

const (
	// ActionHold keeps the item and sends it again once the gate opens. No
	// attempt is counted, and a batch is neither split nor quarantined.
	ActionHold Action = "hold"
	// ActionRetry counts an attempt and sends the item again when its own
	// State.Failed backoff is due.
	ActionRetry Action = "retry"
	// ActionQuarantine quarantines the item. A batch first splits into solo
	// jobs to find the member at fault.
	ActionQuarantine Action = "quarantine"
	// ActionPoll keeps polling the item's job. A job that exists is never
	// submitted again.
	ActionPoll Action = "poll"
	// ActionResubmit sends the item again and counts it as a possible
	// duplicate.
	ActionResubmit Action = "resubmit"
	// ActionRetain keeps the item and waits for an operator.
	ActionRetain Action = "retain"
)

// Failure is one failed call for one item.
type Failure struct {
	// Key identifies the item: a segment id, or a batch id for a batch job.
	Key   string
	Stage Stage
	Fault backend.Fault
	// JobID reports whether a job id is known for the item.
	JobID bool
	// Attempts counts the item's failed attempts so far, including this
	// one, as the journal records them.
	Attempts int
}

// Decision is what Policy.Failed decided.
type Decision struct {
	Action Action
	// Scope is the fault's scope after auth and correlation are applied.
	Scope backend.Scope
	// Hold is the hold on every submit in effect after this failure.
	Hold Hold
}

// Policy turns failures into actions and keeps the gate, the correlator, the
// create budget and the breaker for one uploader.
type Policy struct {
	cfg        Config
	gate       *Gate
	correlator *Correlator
	limiter    *Limiter
	breaker    *Breaker
}

// NewPolicy validates cfg and builds a policy. rand returns uniform values in
// [0, 1); nil uses math/rand/v2.
func NewPolicy(cfg Config, rand func() float64) (*Policy, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("fault handling config: %w", err)
	}
	return &Policy{
		cfg:        cfg,
		gate:       NewGate(cfg, rand),
		correlator: NewCorrelator(cfg.CorrelationWindow, cfg.BackendScopeSegments, cfg.SegmentScopeFailures),
		limiter:    NewLimiter(cfg.CreatesPerMinute, cfg.CreateBurst),
		breaker:    NewBreaker(cfg.QuarantineTrip),
	}, nil
}

// Gate returns the hold on every submit. Submitters call WaitSubmit instead
// of waiting on it directly.
func (p *Policy) Gate() *Gate { return p.gate }

// Limiter returns the per-pod create budget that WaitSubmit draws from.
func (p *Policy) Limiter() *Limiter { return p.limiter }

// Breaker returns the quarantine breaker. The caller records every
// quarantine on it and stops preparing new segments while it is tripped.
func (p *Policy) Breaker() *Breaker { return p.breaker }

// Failed decides what to do after f and updates the gate. An auth fault is
// always backend scope, so it holds every submit whatever it claims. A fault
// with empty fields, such as a failed job that gave no reason, counts as
// retryable with unknown scope. The action rules, in order:
//
//  1. A failed status read keeps polling: the job may still land.
//  2. A submit that returned a job id keeps polling that job.
//  3. An auth fault holds, so auth never splits a batch or quarantines
//     anything.
//  4. An ambiguous job keeps polling. An ambiguous submit with no job id
//     follows Config.AmbiguousWithoutID, unless the backend is resubmit safe,
//     where it is an ordinary retryable fault.
//  5. Backend scope holds.
//  6. Segment scope quarantines when terminal or after
//     Config.SegmentScopeFailures attempts, and retries otherwise.
//  7. An unknown scope that correlation cannot settle retries.
func (p *Policy) Failed(now time.Time, caps backend.Capabilities, f Failure) Decision {
	fault := normalize(f.Fault)
	auth := fault.Reason == backend.ReasonAuth
	switch {
	case auth:
		fault.Scope = backend.ScopeBackend
	case fault.Scope == backend.ScopeUnknown:
		fault.Scope = p.correlator.Failure(now, f.Key, fault.Reason, f.Attempts)
	}
	hold := p.gate.Report(now, fault)
	return Decision{Action: p.action(caps, f, fault, auth), Scope: fault.Scope, Hold: hold}
}

func normalize(fault backend.Fault) backend.Fault {
	if fault.Class == "" {
		fault.Class = backend.ClassRetryable
	}
	if fault.Scope == "" {
		fault.Scope = backend.ScopeUnknown
	}
	if fault.Reason == "" {
		fault.Reason = backend.ReasonUnknown
	}
	return fault
}

func (p *Policy) action(caps backend.Capabilities, f Failure, fault backend.Fault, auth bool) Action {
	switch {
	case f.Stage == StageStatus:
		return ActionPoll
	case f.Stage == StageSubmit && f.JobID:
		return ActionPoll
	case auth:
		return ActionHold
	case fault.Class == backend.ClassAmbiguous && f.Stage == StageJob:
		return ActionPoll
	case fault.Class == backend.ClassAmbiguous && !caps.ResubmitSafe:
		if p.cfg.AmbiguousWithoutID == AmbiguousRetain {
			return ActionRetain
		}
		return ActionResubmit
	}
	switch fault.Scope {
	case backend.ScopeBackend:
		return ActionHold
	case backend.ScopeSegment:
		if fault.Class == backend.ClassTerminal || f.Attempts >= p.cfg.SegmentScopeFailures {
			return ActionQuarantine
		}
		return ActionRetry
	default:
		return ActionRetry
	}
}

// Succeeded records that the item key reached success at now. It clears an
// auth failure, resets the hold backoff, ends a run of quarantines, and
// counts as another item succeeding for correlation.
func (p *Policy) Succeeded(now time.Time, key string) {
	p.gate.Succeeded()
	p.correlator.Success(now, key)
	p.breaker.Succeeded()
}

// WaitSubmit blocks until one create may start: the create budget allows it
// and no hold is in effect. A hold that begins during the budget wait sends
// the caller back to wait it out and spends that budget slot, so creates
// queued behind a hold resume at the budget's pace instead of together.
func (p *Policy) WaitSubmit(ctx context.Context) error {
	for {
		if err := p.gate.Wait(ctx); err != nil {
			return err
		}
		if err := p.limiter.Wait(ctx); err != nil {
			return err
		}
		now := p.gate.now()
		if !p.gate.Hold(now).Active(now) {
			return nil
		}
	}
}
