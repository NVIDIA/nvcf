// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func fixed(r float64) func() float64 { return func() float64 { return r } }

func noJitterConfig() Config {
	cfg := DefaultConfig()
	cfg.ResumeJitter = 0
	return cfg
}

var (
	outage    = backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeBackend, Reason: backend.ReasonUnavailable}
	authFault = backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeBackend, Reason: backend.ReasonAuth}
)

func TestGateHoldsOnlyBackendScope(t *testing.T) {
	tests := []struct {
		name      string
		fault     backend.Fault
		wantUntil time.Time
	}{
		{name: "segment fault holds nothing", fault: backend.Fault{Class: backend.ClassTerminal, Scope: backend.ScopeSegment, Reason: backend.ReasonRejected}},
		{name: "unknown fault holds nothing", fault: backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeUnknown, Reason: backend.ReasonUnknown}},
		{name: "backend outage holds every submit", fault: outage, wantUntil: t0.Add(5 * time.Second)},
		{name: "auth with a wrong scope still holds", fault: backend.Fault{Class: backend.ClassTerminal, Scope: backend.ScopeSegment, Reason: backend.ReasonAuth}, wantUntil: t0.Add(60 * time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := NewGate(noJitterConfig(), fixed(0))
			hold := gate.Report(t0, tt.fault)
			if !hold.Until.Equal(tt.wantUntil) {
				t.Fatalf("Report() hold until %v, want %v", hold.Until, tt.wantUntil)
			}
			if tt.wantUntil.IsZero() {
				if gate.Hold(t0).Active(t0) {
					t.Fatalf("Hold(t0) is active, want submits allowed")
				}
				return
			}
			if !gate.Hold(tt.wantUntil.Add(-time.Nanosecond)).Active(tt.wantUntil.Add(-time.Nanosecond)) {
				t.Fatalf("Hold() just before %v is not active", tt.wantUntil)
			}
			if gate.Hold(tt.wantUntil).Active(tt.wantUntil) {
				t.Fatalf("Hold() at %v is still active", tt.wantUntil)
			}
			if hold.Reason != tt.fault.Reason {
				t.Fatalf("hold reason = %q, want %q", hold.Reason, tt.fault.Reason)
			}
		})
	}
}

func TestGateHonorsRetryAfter(t *testing.T) {
	rateLimited := func(after time.Duration) backend.Fault {
		return backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeBackend, Reason: backend.ReasonRateLimit, RetryAfter: after}
	}
	tests := []struct {
		name  string
		after time.Duration
		want  time.Duration
	}{
		{name: "longer than the backoff", after: 2 * time.Minute, want: 2 * time.Minute},
		{name: "shorter than the backoff", after: time.Second, want: 5 * time.Second},
		{name: "not given", after: 0, want: 5 * time.Second},
		{name: "capped", after: 2 * time.Hour, want: 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := NewGate(noJitterConfig(), fixed(0))
			if hold := gate.Report(t0, rateLimited(tt.after)); !hold.Until.Equal(t0.Add(tt.want)) {
				t.Fatalf("hold until %v, want %v", hold.Until, t0.Add(tt.want))
			}
		})
	}

	gate := NewGate(noJitterConfig(), fixed(0))
	gate.Report(t0, outage)
	if hold := gate.Report(t0.Add(time.Second), rateLimited(10*time.Minute)); !hold.Until.Equal(t0.Add(time.Second + 10*time.Minute)) {
		t.Fatalf("Retry-After during a hold: until %v, want it extended to %v", hold.Until, t0.Add(time.Second+10*time.Minute))
	}
}

func TestGateAuthBackoff(t *testing.T) {
	gate := NewGate(noJitterConfig(), fixed(0))
	now := t0
	for i, seconds := range []int{60, 120, 240, 480, 960, 1800, 1800} {
		want := time.Duration(seconds) * time.Second
		hold := gate.Report(now, authFault)
		if got := hold.Until.Sub(now); got != want {
			t.Fatalf("auth failure %d: hold %v, want %v", i+1, got, want)
		}
		if !gate.AuthFailing() {
			t.Fatalf("auth failure %d: AuthFailing() = false, want true", i+1)
		}
		// Calls that started before the hold and fail during it must not
		// grow the backoff.
		gate.Report(now.Add(time.Second), authFault)
		gate.Report(now.Add(2*time.Second), authFault)
		now = hold.Until
	}

	gate.Succeeded()
	if gate.AuthFailing() {
		t.Fatalf("AuthFailing() after a success = true, want false")
	}
	if hold := gate.Report(now, authFault); hold.Until.Sub(now) != 60*time.Second {
		t.Fatalf("auth hold after a success = %v, want the backoff reset to 60s", hold.Until.Sub(now))
	}
}

func TestGateAuthDuringTransientHold(t *testing.T) {
	gate := NewGate(noJitterConfig(), fixed(0))
	gate.Report(t0, outage)
	if hold := gate.Report(t0.Add(time.Second), authFault); !hold.Until.Equal(t0.Add(61 * time.Second)) {
		t.Fatalf("first auth failure during a 5s outage hold: until %v, want the 60s auth hold from it", hold.Until)
	}
	if hold := gate.Report(t0.Add(2*time.Second), authFault); !hold.Until.Equal(t0.Add(61 * time.Second)) {
		t.Fatalf("second auth failure during the auth hold: until %v, want it unchanged", hold.Until)
	}
}

func TestGateSuccessKeepsRetryAfter(t *testing.T) {
	gate := NewGate(noJitterConfig(), fixed(0))
	gate.Report(t0, backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeBackend, Reason: backend.ReasonRateLimit, RetryAfter: time.Minute})
	gate.Succeeded()
	if !gate.Hold(t0.Add(30 * time.Second)).Active(t0.Add(30 * time.Second)) {
		t.Fatalf("a success ended the Retry-After hold early")
	}
}

// TestGateResumeJitter pins the default resume jitter: up to 30 seconds on
// top of each hold.
func TestGateResumeJitter(t *testing.T) {
	tests := []struct {
		r    float64
		want time.Duration
	}{
		{r: 0, want: 60 * time.Second},
		{r: 0.5, want: 75 * time.Second},
		{r: 0.999, want: 60*time.Second + time.Duration(0.999*float64(30*time.Second))},
	}
	for _, tt := range tests {
		gate := NewGate(DefaultConfig(), fixed(tt.r))
		if hold := gate.Report(t0, authFault); hold.Until.Sub(t0) != tt.want {
			t.Fatalf("r=%v: auth hold %v, want the 60s auth hold plus %v of jitter", tt.r, hold.Until.Sub(t0), tt.want-60*time.Second)
		}
	}
}

func TestGateWait(t *testing.T) {
	cfg := noJitterConfig()
	cfg.Transient = Backoff{Base: 30 * time.Millisecond, Max: 30 * time.Millisecond}
	gate := NewGate(cfg, fixed(0))

	start := time.Now()
	gate.Report(start, outage)
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Fatalf("Wait() returned after %v, want at least the 30ms hold", waited)
	}

	cfg.Transient = Backoff{Base: time.Hour, Max: time.Hour}
	gate = NewGate(cfg, fixed(0))
	gate.Report(time.Now(), outage)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() during a long hold = %v, want the context error", err)
	}
}
