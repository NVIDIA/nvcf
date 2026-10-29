// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

func newTestPolicy(t *testing.T, mutate func(*Config)) *Policy {
	t.Helper()
	cfg := noJitterConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	policy, err := NewPolicy(cfg, fixed(0))
	if err != nil {
		t.Fatalf("NewPolicy() = %v", err)
	}
	return policy
}

func TestNewPolicyRejectsInvalidConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QuarantineTrip = 0
	if _, err := NewPolicy(cfg, nil); err == nil {
		t.Fatalf("NewPolicy() with an invalid config = nil error, want an error")
	}
}

func TestPolicyDecide(t *testing.T) {
	asyncJobs := backend.Capabilities{MaxBatchFiles: 16}
	resubmitSafe := backend.Capabilities{ResubmitSafe: true}
	fault := func(class backend.Class, scope backend.Scope, reason backend.Reason) backend.Fault {
		return backend.Fault{Class: class, Scope: scope, Reason: reason}
	}
	ambiguousTimeout := fault(backend.ClassAmbiguous, backend.ScopeBackend, backend.ReasonTimeout)

	tests := []struct {
		name      string
		mutate    func(*Config)
		caps      backend.Capabilities
		failure   Failure
		want      Action
		wantScope backend.Scope
		wantHold  bool
	}{
		{
			name:      "auth holds even when a classifier claims segment terminal",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageSubmit, Fault: fault(backend.ClassTerminal, backend.ScopeSegment, backend.ReasonAuth), Attempts: 1},
			want:      ActionHold,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "auth on a failed batch job never splits or quarantines",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageJob, JobID: true, Fault: fault(backend.ClassRetryable, backend.ScopeBackend, backend.ReasonAuth), Attempts: 100},
			want:      ActionHold,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "auth on a status read keeps polling and holds submits",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageStatus, JobID: true, Fault: fault(backend.ClassRetryable, backend.ScopeBackend, backend.ReasonAuth)},
			want:      ActionPoll,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "auth on a submit that returned a job id keeps polling and holds submits",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageSubmit, JobID: true, Fault: fault(backend.ClassRetryable, backend.ScopeBackend, backend.ReasonAuth), Attempts: 1},
			want:      ActionPoll,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "failed job with no reason is an unknown retryable fault",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageJob, JobID: true, Fault: backend.Fault{}, Attempts: 1},
			want:      ActionRetry,
			wantScope: backend.ScopeUnknown,
		},
		{
			name:      "backend outage holds every submit and counts nothing against the item",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageSubmit, Fault: fault(backend.ClassRetryable, backend.ScopeBackend, backend.ReasonUnavailable), Attempts: 9},
			want:      ActionHold,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "submit error with a job id polls that job",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageSubmit, JobID: true, Fault: fault(backend.ClassRetryable, backend.ScopeUnknown, backend.ReasonUnknown), Attempts: 1},
			want:      ActionPoll,
			wantScope: backend.ScopeUnknown,
		},
		{
			name:      "ambiguous submit with no id is resubmitted and counted by default",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageSubmit, Fault: ambiguousTimeout, Attempts: 1},
			want:      ActionResubmit,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "ambiguous submit with no id is retained under the retain policy",
			mutate:    func(c *Config) { c.AmbiguousWithoutID = AmbiguousRetain },
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageSubmit, Fault: ambiguousTimeout, Attempts: 1},
			want:      ActionRetain,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "ambiguous submit on a resubmit-safe backend is a plain hold",
			caps:      resubmitSafe,
			failure:   Failure{Key: "s1", Stage: StageSubmit, Fault: ambiguousTimeout, Attempts: 1},
			want:      ActionHold,
			wantScope: backend.ScopeBackend,
			wantHold:  true,
		},
		{
			name:      "ambiguous job with an id keeps polling",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageJob, JobID: true, Fault: fault(backend.ClassAmbiguous, backend.ScopeSegment, "temp_upload_failed"), Attempts: 1},
			want:      ActionPoll,
			wantScope: backend.ScopeSegment,
		},
		{
			name:      "segment terminal job failure is quarantined",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageJob, JobID: true, Fault: fault(backend.ClassTerminal, backend.ScopeSegment, "canceled"), Attempts: 1},
			want:      ActionQuarantine,
			wantScope: backend.ScopeSegment,
		},
		{
			name:      "file over the backend limit is quarantined",
			caps:      asyncJobs,
			failure:   Failure{Key: "s1", Stage: StageSubmit, Fault: fault(backend.ClassTerminal, backend.ScopeSegment, backend.ReasonTooLarge), Attempts: 1},
			want:      ActionQuarantine,
			wantScope: backend.ScopeSegment,
		},
		{
			name:      "segment retryable is retried",
			caps:      asyncJobs,
			failure:   Failure{Key: "s1", Stage: StageJob, JobID: true, Fault: fault(backend.ClassRetryable, backend.ScopeSegment, "rejected_file"), Attempts: 2},
			want:      ActionRetry,
			wantScope: backend.ScopeSegment,
		},
		{
			name:      "segment retryable stops retrying at the failure limit",
			caps:      asyncJobs,
			failure:   Failure{Key: "s1", Stage: StageJob, JobID: true, Fault: fault(backend.ClassRetryable, backend.ScopeSegment, "rejected_file"), Attempts: 5},
			want:      ActionQuarantine,
			wantScope: backend.ScopeSegment,
		},
		{
			name:      "unknown submit failure is retried while its scope is unclear",
			caps:      asyncJobs,
			failure:   Failure{Key: "s1", Stage: StageSubmit, Fault: fault(backend.ClassRetryable, backend.ScopeUnknown, backend.ReasonUnknown), Attempts: 1},
			want:      ActionRetry,
			wantScope: backend.ScopeUnknown,
		},
		{
			name:      "unknown status read failure keeps polling",
			caps:      asyncJobs,
			failure:   Failure{Key: "b1", Stage: StageStatus, JobID: true, Fault: fault(backend.ClassRetryable, backend.ScopeUnknown, backend.ReasonUnknown)},
			want:      ActionPoll,
			wantScope: backend.ScopeUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := newTestPolicy(t, tt.mutate)
			got := policy.Failed(t0, tt.caps, tt.failure)
			if got.Action != tt.want || got.Scope != tt.wantScope {
				t.Fatalf("Failed() = %s with scope %s, want %s with scope %s", got.Action, got.Scope, tt.want, tt.wantScope)
			}
			if got.Hold.Active(t0) != tt.wantHold || policy.Gate().Hold(t0).Active(t0) != tt.wantHold {
				t.Fatalf("hold active = %v, gate active = %v, want %v", got.Hold.Active(t0), policy.Gate().Hold(t0).Active(t0), tt.wantHold)
			}
		})
	}
}

func TestPolicyCorrelatesUnknownFaults(t *testing.T) {
	unknown := backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeUnknown, Reason: "http_418"}

	t.Run("three items failing alike hold every submit", func(t *testing.T) {
		policy := newTestPolicy(t, nil)
		caps := backend.Capabilities{}
		for i, key := range []string{"a", "b"} {
			if got := policy.Failed(t0.Add(time.Duration(i)*time.Minute), caps, Failure{Key: key, Stage: StageSubmit, Fault: unknown, Attempts: 1}); got.Action != ActionRetry {
				t.Fatalf("failure of %s = %s, want retry", key, got.Action)
			}
		}
		got := policy.Failed(t0.Add(2*time.Minute), caps, Failure{Key: "c", Stage: StageSubmit, Fault: unknown, Attempts: 1})
		if got.Action != ActionHold || got.Scope != backend.ScopeBackend || !policy.Gate().Hold(t0.Add(2*time.Minute)).Active(t0.Add(2*time.Minute)) {
			t.Fatalf("third failure = %s with scope %s, want hold with backend scope and the gate held", got.Action, got.Scope)
		}
	})

	t.Run("failed jobs with no reason are correlated too", func(t *testing.T) {
		policy := newTestPolicy(t, nil)
		var got Decision
		for _, key := range []string{"a", "b", "c"} {
			got = policy.Failed(t0, backend.Capabilities{}, Failure{Key: key, Stage: StageJob, JobID: true, Fault: backend.Fault{}, Attempts: 1})
		}
		if got.Action != ActionHold || got.Scope != backend.ScopeBackend {
			t.Fatalf("third failed job with no reason = %s with scope %s, want hold with backend scope", got.Action, got.Scope)
		}
	})

	t.Run("one item failing while others succeed is quarantined", func(t *testing.T) {
		policy := newTestPolicy(t, nil)
		caps := backend.Capabilities{}
		policy.Succeeded(t0, "other")
		got := policy.Failed(t0.Add(time.Minute), caps, Failure{Key: "a", Stage: StageSubmit, Fault: unknown, Attempts: 5})
		if got.Action != ActionQuarantine || got.Scope != backend.ScopeSegment {
			t.Fatalf("fifth failure = %s with scope %s, want quarantine with segment scope", got.Action, got.Scope)
		}
		if policy.Gate().Hold(t0.Add(time.Minute)).Active(t0.Add(time.Minute)) {
			t.Fatalf("a segment fault held the gate")
		}
	})
}

func TestPolicySuccessClearsAuth(t *testing.T) {
	policy := newTestPolicy(t, nil)
	auth := backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeBackend, Reason: backend.ReasonAuth}
	policy.Failed(t0, backend.Capabilities{}, Failure{Key: "a", Stage: StageSubmit, Fault: auth, Attempts: 1})
	if !policy.Gate().AuthFailing() {
		t.Fatalf("AuthFailing() after an auth fault = false, want true")
	}
	policy.Breaker().Quarantined()
	policy.Succeeded(t0.Add(time.Hour), "a")
	if policy.Gate().AuthFailing() {
		t.Fatalf("AuthFailing() after a success = true, want false")
	}
	for i := 1; i < DefaultConfig().QuarantineTrip; i++ {
		if policy.Breaker().Quarantined() {
			t.Fatalf("breaker tripped at quarantine %d after a success, want the run restarted", i)
		}
	}
}

// TestPolicyWaitSubmitSpacesCreatesAfterHold runs several submitters against
// a hold that starts while they wait for the create budget. No create may
// start during the hold, and creates after it must keep the budget spacing
// instead of starting together.
func TestPolicyWaitSubmitSpacesCreatesAfterHold(t *testing.T) {
	const interval = 100 * time.Millisecond
	cfg := noJitterConfig()
	cfg.CreatesPerMinute = int(time.Minute / interval)
	cfg.Transient = Backoff{Base: 300 * time.Millisecond, Max: 300 * time.Millisecond}
	policy, err := NewPolicy(cfg, fixed(0))
	if err != nil {
		t.Fatalf("NewPolicy() = %v", err)
	}

	if err := policy.WaitSubmit(context.Background()); err != nil {
		t.Fatalf("first WaitSubmit() = %v", err)
	}

	// The workers pass the open gate and queue on the budget; the hold
	// starts while they wait, as when the first probe fails.
	const workers = 4
	starts := make(chan time.Time, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := policy.WaitSubmit(context.Background()); err != nil {
				t.Errorf("WaitSubmit() = %v", err)
				return
			}
			starts <- time.Now()
		}()
	}
	time.Sleep(30 * time.Millisecond)
	holdUntil := policy.Gate().Report(time.Now(), outage).Until
	wg.Wait()
	close(starts)

	var got []time.Time
	for start := range starts {
		if start.Before(holdUntil) {
			t.Fatalf("a create started at %v, during the hold that ends at %v", start, holdUntil)
		}
		got = append(got, start)
	}
	slices.SortFunc(got, func(a, b time.Time) int { return a.Compare(b) })
	for i := 1; i < len(got); i++ {
		if gap := got[i].Sub(got[i-1]); gap < interval/2 {
			t.Fatalf("creates %d and %d started %v apart after the hold, want about %v", i, i+1, gap, interval)
		}
	}
}

// TestPolicyDefaultCreateBudget pins the default create budget: 30 a minute
// with a burst of 1, so creates are 2 seconds apart even after a long idle.
func TestPolicyDefaultCreateBudget(t *testing.T) {
	policy, err := NewPolicy(DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("NewPolicy() = %v", err)
	}
	limiter := policy.Limiter()
	for i, want := range []time.Duration{0, 2 * time.Second, 4 * time.Second} {
		if got := limiter.Reserve(t0); got != want {
			t.Fatalf("create %d: wait %v, want %v", i+1, got, want)
		}
	}
	idle := t0.Add(time.Hour)
	for i, want := range []time.Duration{0, 2 * time.Second} {
		if got := limiter.Reserve(idle); got != want {
			t.Fatalf("create %d after an hour idle: wait %v, want %v", i+1, got, want)
		}
	}
}

// TestPolicyBreakerLatchesUntilRestart pins the default breaker: five
// quarantines in a row trip it, a later success does not close it, and only a
// new policy, as at restart, starts closed.
func TestPolicyBreakerLatchesUntilRestart(t *testing.T) {
	policy, err := NewPolicy(DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("NewPolicy() = %v", err)
	}
	for i := 1; i <= 4; i++ {
		if policy.Breaker().Quarantined() {
			t.Fatalf("quarantine %d tripped the breaker, want it to trip at 5", i)
		}
	}
	if !policy.Breaker().Quarantined() {
		t.Fatalf("fifth quarantine in a row did not trip the breaker")
	}
	policy.Succeeded(t0, "a")
	if !policy.Breaker().Tripped() {
		t.Fatalf("a success closed the tripped breaker, want it tripped until restart")
	}

	restarted, err := NewPolicy(DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("NewPolicy() = %v", err)
	}
	if restarted.Breaker().Tripped() {
		t.Fatalf("a new policy starts with the breaker tripped, want it closed")
	}
}

// httpStatus is an HTTP client error that exposes its response status.
type httpStatus int

func (s httpStatus) Error() string       { return "http " + strconv.Itoa(int(s)) }
func (s httpStatus) HTTPStatusCode() int { return int(s) }

// TestPolicyServerErrorOnCreate pins what a 5xx on job create does through
// the default classifier: 500, 502 and 504 may have created the job, so with
// no job id the work is resubmitted and counted; 503 created nothing and only
// holds; a job id that came back is polled.
func TestPolicyServerErrorOnCreate(t *testing.T) {
	asyncJobs := backend.Capabilities{MaxBatchFiles: 16}
	resubmitSafe := backend.Capabilities{ResubmitSafe: true}
	tests := []struct {
		name  string
		code  int
		jobID bool
		caps  backend.Capabilities
		want  Action
	}{
		{name: "500 with no job id", code: 500, caps: asyncJobs, want: ActionResubmit},
		{name: "502 with no job id", code: 502, caps: asyncJobs, want: ActionResubmit},
		{name: "504 with no job id", code: 504, caps: asyncJobs, want: ActionResubmit},
		{name: "503 with no job id", code: 503, caps: asyncJobs, want: ActionHold},
		{name: "502 with a job id", code: 502, jobID: true, caps: asyncJobs, want: ActionPoll},
		{name: "502 on a resubmit-safe backend", code: 502, caps: resubmitSafe, want: ActionHold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := NewPolicy(DefaultConfig(), fixed(0))
			if err != nil {
				t.Fatalf("NewPolicy() = %v", err)
			}
			fault := backend.ClassifyDefault(fmt.Errorf("create job: %w", httpStatus(tt.code)))
			got := policy.Failed(t0, tt.caps, Failure{Key: "b1", Stage: StageSubmit, JobID: tt.jobID, Fault: fault, Attempts: 1})
			if got.Action != tt.want {
				t.Fatalf("Failed() = %s, want %s", got.Action, tt.want)
			}
			if !got.Hold.Active(t0) {
				t.Fatalf("a %d on create did not hold submits, want the backend held", tt.code)
			}
		})
	}
}
