// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

// Hold is a pause on every submit. The zero Hold means submits may go.
type Hold struct {
	// Reason is the fault that set or last extended the hold.
	Reason backend.Reason
	Until  time.Time
}

// Active reports whether the hold still pauses submits at now.
func (h Hold) Active(now time.Time) bool {
	return now.Before(h.Until)
}

// Gate holds every submit while the backend is failing. Backend-scope faults
// hold with decorrelated jitter and honor Retry-After. Auth faults hold with
// a doubling backoff whatever scope they claim. Nothing else holds.
//
// The first submit after a hold is the probe: if it fails, the next hold is
// longer. Faults from calls that were already running when the hold began do
// not grow the backoff, so concurrent submitters cannot inflate it.
type Gate struct {
	cfg  Config
	rand func() float64
	now  func() time.Time

	mu             sync.Mutex
	hold           Hold
	transientDelay time.Duration
	authDelay      time.Duration
	authFailing    bool
}

// NewGate builds a gate from a valid cfg. rand returns uniform values in
// [0, 1); nil uses math/rand/v2.
func NewGate(cfg Config, rand func() float64) *Gate {
	return &Gate{cfg: cfg, rand: orDefaultRand(rand), now: time.Now}
}

func orDefaultRand(r func() float64) func() float64 {
	if r == nil {
		return rand.Float64
	}
	return r
}

// Report records a fault seen at now and returns the hold in effect after
// it. An auth fault also marks auth as failing until the next success; the
// caller should then re-read credentials and raise the auth alert.
func (g *Gate) Report(now time.Time, f backend.Fault) Hold {
	g.mu.Lock()
	defer g.mu.Unlock()

	auth := f.Reason == backend.ReasonAuth
	if !auth && f.Scope != backend.ScopeBackend {
		return g.holdLocked(now)
	}
	if auth {
		g.authFailing = true
	}

	// The first auth failure since the last success always gets the auth
	// hold, even inside a shorter transient hold.
	var delay time.Duration
	if !g.hold.Active(now) || (auth && g.authDelay == 0) {
		if auth {
			g.authDelay = g.cfg.Auth.Doubling(g.authDelay)
			delay = g.authDelay
		} else {
			g.transientDelay = g.cfg.Transient.Decorrelated(g.transientDelay, g.rand())
			delay = g.transientDelay
		}
	}
	if f.RetryAfter > 0 {
		delay = max(delay, min(f.RetryAfter, g.cfg.RetryAfterMax))
	}
	if delay > 0 {
		jitter := time.Duration(g.rand() * float64(g.cfg.ResumeJitter))
		if until := now.Add(delay + jitter); until.After(g.hold.Until) {
			g.hold = Hold{Reason: f.Reason, Until: until}
		}
	}
	return g.holdLocked(now)
}

// Succeeded records a backend call that worked. It clears the auth failure
// and resets both backoffs, but it does not end a hold early, so a
// Retry-After is still honored.
func (g *Gate) Succeeded() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.authFailing = false
	g.authDelay = 0
	g.transientDelay = 0
}

// Hold returns the hold in effect at now, or the zero Hold.
func (g *Gate) Hold(now time.Time) Hold {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.holdLocked(now)
}

func (g *Gate) holdLocked(now time.Time) Hold {
	if !g.hold.Active(now) {
		return Hold{}
	}
	return g.hold
}

// AuthFailing reports whether the destination refused the credentials and no
// backend call has worked since.
func (g *Gate) AuthFailing() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.authFailing
}

// Wait blocks until no hold is in effect or ctx ends. A hold extended while
// it waits is waited out too.
func (g *Gate) Wait(ctx context.Context) error {
	for {
		now := g.now()
		hold := g.Hold(now)
		if !hold.Active(now) {
			return nil
		}
		if err := sleep(ctx, hold.Until.Sub(now)); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
