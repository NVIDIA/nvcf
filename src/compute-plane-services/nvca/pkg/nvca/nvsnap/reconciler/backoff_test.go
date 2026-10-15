/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconciler

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestBackoffWindow_ScheduleMatchesIntent guards against silent
// schedule tweaks. If somebody changes the timings, the test fails
// loudly and forces a deliberate discussion in code review.
func TestBackoffWindow_ScheduleMatchesIntent(t *testing.T) {
	cases := []struct {
		attempts int64
		want     time.Duration
	}{
		{0, 0},                      // no prior failure → no backoff
		{-1, 0},                     // defensive: negative should be no backoff
		{1, 5 * time.Minute},        // 1st failure
		{2, 30 * time.Minute},       // 2nd
		{3, 2 * time.Hour},          // 3rd
		{4, 24 * time.Hour},         // 4th — cap engages
		{17, 24 * time.Hour},        // far above cap → still cap
		{1_000_000, 24 * time.Hour}, // overflow stress → still cap
	}
	for _, tc := range cases {
		got := backoffWindow(tc.attempts)
		if got != tc.want {
			t.Errorf("backoffWindow(%d) = %v, want %v", tc.attempts, got, tc.want)
		}
	}
}

// TestShouldSuppressAttempt_NoPriorFailure verifies the gate is a
// no-op for a fresh function (the common case — most reconciles are
// for warm captures or first attempts).
func TestShouldSuppressAttempt_NoPriorFailure(t *testing.T) {
	now := time.Now()
	cases := []cfsStatus{
		{}, // brand new CFS
		{AttemptCount: 0, LastAttemptAt: ptrTime(now)}, // count=0, attempt timestamp present (shouldn't happen but be tolerant)
		{AttemptCount: 5, LastAttemptAt: nil},          // count>0 but timestamp lost (controller restart on old CRD)
	}
	for i, prev := range cases {
		suppress, until := shouldSuppressAttempt(prev, now)
		if suppress {
			t.Errorf("case %d: shouldSuppressAttempt returned true for %+v; want false (no usable prior failure record)", i, prev)
		}
		if !until.IsZero() {
			t.Errorf("case %d: until=%v, want zero", i, until)
		}
	}
}

// TestShouldSuppressAttempt_WithinWindowSuppresses confirms the
// central case: a recent failure suppresses a new attempt and the
// `until` timestamp reflects window expiry.
func TestShouldSuppressAttempt_WithinWindowSuppresses(t *testing.T) {
	now := time.Now()
	prev := cfsStatus{
		AttemptCount:  1,
		LastAttemptAt: ptrTime(now.Add(-1 * time.Minute)), // 1 min ago — well inside the 5 min window
	}
	suppress, until := shouldSuppressAttempt(prev, now)
	if !suppress {
		t.Fatal("expected suppression 1 min after failure with 5-min window")
	}
	want := prev.LastAttemptAt.Time.Add(5 * time.Minute)
	if !until.Equal(want) {
		t.Errorf("until = %v, want %v (last attempt + 5 min)", until, want)
	}
}

// TestShouldSuppressAttempt_WindowExpiredAllows verifies that
// suppression eventually clears. A failure 6 minutes ago (past the
// 5-min window for AttemptCount=1) must not block a new attempt.
func TestShouldSuppressAttempt_WindowExpiredAllows(t *testing.T) {
	now := time.Now()
	prev := cfsStatus{
		AttemptCount:  1,
		LastAttemptAt: ptrTime(now.Add(-6 * time.Minute)), // past 5-min window
	}
	if suppress, _ := shouldSuppressAttempt(prev, now); suppress {
		t.Fatal("expected NO suppression once 5-min window for attempt #1 has elapsed")
	}
}

// TestShouldSuppressAttempt_ExponentialEscalation verifies the
// schedule's exponential character: a 4th failure imposes a much
// longer window than a 1st failure, so a sustained-broken workload
// doesn't get re-attempted at the same cadence forever.
func TestShouldSuppressAttempt_ExponentialEscalation(t *testing.T) {
	now := time.Now()
	// 4th attempt failed 20 hours ago. Cap is 24h, so it should
	// still suppress (20h < 24h).
	prev := cfsStatus{
		AttemptCount:  4,
		LastAttemptAt: ptrTime(now.Add(-20 * time.Hour)),
	}
	suppress, _ := shouldSuppressAttempt(prev, now)
	if !suppress {
		t.Fatal("4th failure 20h ago should still be within the 24h cap; expected suppress=true")
	}

	// Same status but 25 hours ago — past the cap, should allow.
	prev.LastAttemptAt = ptrTime(now.Add(-25 * time.Hour))
	if s, _ := shouldSuppressAttempt(prev, now); s {
		t.Fatal("4th failure 25h ago should be past the 24h cap; expected suppress=false")
	}
}

// TestShouldSuppressAttempt_BoundaryAtExactExpiry pins behavior at
// the exact expiry instant (now == LastAttemptAt + window). Strict
// less-than means we ALLOW at the boundary — preferred so a polling
// loop with timestamp resolution >= 1s can't get stuck one tick on
// the suppress side forever.
func TestShouldSuppressAttempt_BoundaryAtExactExpiry(t *testing.T) {
	last := time.Now().Add(-5 * time.Minute)
	now := last.Add(5 * time.Minute) // exactly the boundary
	prev := cfsStatus{
		AttemptCount:  1,
		LastAttemptAt: ptrTime(last),
	}
	if s, _ := shouldSuppressAttempt(prev, now); s {
		t.Fatal("boundary semantics: now==last+window should ALLOW (not suppress)")
	}
}

// ptrTime wraps t in metav1.Time and returns a pointer — matches how
// readStatus populates cfsStatus.LastAttemptAt.
func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}
