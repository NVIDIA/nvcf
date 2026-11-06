// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"testing"
	"time"
)

func TestBreaker(t *testing.T) {
	breaker := NewBreaker(5, time.Hour)
	for i := 1; i <= 4; i++ {
		if breaker.QuarantinedAt(t0) || breaker.TrippedAt(t0) {
			t.Fatalf("quarantine %d tripped the breaker, want it to trip at 5", i)
		}
	}
	if !breaker.QuarantinedAt(t0) || !breaker.TrippedAt(t0) {
		t.Fatalf("fifth quarantine in a row did not trip the breaker")
	}
	if breaker.QuarantinedAt(t0) {
		t.Fatalf("a quarantine after the trip reported a new trip, want it reported once")
	}
	breaker.Succeeded()
	if !breaker.TrippedAt(t0) {
		t.Fatalf("a success closed a tripped breaker, want it tripped until the resume time")
	}
	breaker.Reset()
	if breaker.TrippedAt(t0) {
		t.Fatalf("Reset() left the breaker tripped")
	}

	for i := 1; i <= 4; i++ {
		breaker.QuarantinedAt(t0)
	}
	breaker.Succeeded()
	for i := 1; i <= 4; i++ {
		if breaker.QuarantinedAt(t0) {
			t.Fatalf("quarantine %d after a success tripped the breaker, want the run restarted", i)
		}
	}
}

// tripAt trips a fresh breaker at now with trip quarantines in a row.
func tripAt(t *testing.T, breaker *Breaker, trip int, now time.Time) {
	t.Helper()
	for i := 1; i <= trip; i++ {
		breaker.QuarantinedAt(now)
	}
	if !breaker.TrippedAt(now) {
		t.Fatalf("%d quarantines in a row did not trip the breaker", trip)
	}
}

func TestBreakerResumesAfterTheResumeTime(t *testing.T) {
	breaker := NewBreaker(3, time.Hour)
	tripAt(t, breaker, 3, t0)

	if !breaker.TrippedAt(t0.Add(time.Hour - time.Nanosecond)) {
		t.Fatalf("breaker resumed before the resume time")
	}
	if got := breaker.Resumed(); got != 0 {
		t.Fatalf("Resumed() before the resume time = %d, want 0", got)
	}
	resumeAt := t0.Add(time.Hour)
	if breaker.TrippedAt(resumeAt) {
		t.Fatalf("breaker still tripped at the resume time, want it closed")
	}
	if got := breaker.Resumed(); got != 1 {
		t.Fatalf("Resumed() after one resume = %d, want 1", got)
	}
	if got := breaker.Resumed(); got != 0 {
		t.Fatalf("Resumed() a second time = %d, want each resume reported once", got)
	}

	// The run starts fresh: the same number of quarantines trips it again.
	for i := 1; i <= 2; i++ {
		if breaker.QuarantinedAt(resumeAt) {
			t.Fatalf("quarantine %d after the resume tripped the breaker, want a fresh run of 3", i)
		}
	}
	if !breaker.QuarantinedAt(resumeAt) || !breaker.TrippedAt(resumeAt) {
		t.Fatalf("third quarantine after the resume did not trip the breaker again")
	}
	if !breaker.TrippedAt(resumeAt.Add(time.Hour - time.Nanosecond)) {
		t.Fatalf("second trip resumed early, want a full resume time from the second trip")
	}
	if breaker.TrippedAt(resumeAt.Add(time.Hour)) || breaker.Resumed() != 1 {
		t.Fatalf("second trip did not resume once after its resume time")
	}
}

func TestBreakerQuarantinesWhileTrippedDoNotExtendTheTrip(t *testing.T) {
	breaker := NewBreaker(2, time.Hour)
	tripAt(t, breaker, 2, t0)
	for i := 0; i < 5; i++ {
		if breaker.QuarantinedAt(t0.Add(30 * time.Minute)) {
			t.Fatalf("a quarantine while tripped reported a new trip")
		}
	}
	if breaker.TrippedAt(t0.Add(time.Hour)) {
		t.Fatalf("quarantines while tripped pushed back the resume, want it an hour after the trip")
	}
}

func TestBreakerQuarantinePastTheResumeTimeStartsAFreshRun(t *testing.T) {
	breaker := NewBreaker(2, time.Hour)
	tripAt(t, breaker, 2, t0)

	// An in-flight job is quarantined after the resume time, before intake
	// checked the breaker: it closes the breaker and counts as the first
	// quarantine of a new run.
	late := t0.Add(2 * time.Hour)
	if breaker.QuarantinedAt(late) {
		t.Fatalf("first quarantine after the resume time tripped the breaker, want a fresh run of 2")
	}
	if breaker.TrippedAt(late) {
		t.Fatalf("breaker tripped after one quarantine of a fresh run")
	}
	if got := breaker.Resumed(); got != 1 {
		t.Fatalf("Resumed() = %d, want the resume reported once", got)
	}
	if !breaker.QuarantinedAt(late) {
		t.Fatalf("second quarantine of the fresh run did not trip the breaker")
	}
}
