/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Per-function checkpoint-attempt backoff (nvca#167).
//
// Problem: prior to this code, every pod-ready event for a function
// with no warm cache triggered a fresh checkpoint attempt — even
// when the previous attempt had just failed seconds before. For
// untested or structurally-incompatible workloads (any new container
// a customer ships that NvSnap doesn't yet support), this produced an
// infinite ~10-minute capture loop: pod ready → POST → capture runs
// for ~10 min → fail → pod cycles → ready again → repeat.
//
// Customer-trust issue, not just an internal nuisance: a new BYOC
// workload that happens to incompatibly mix with NvSnap would be
// hammered with capture attempts forever, burning GPU minutes and
// blocking the operator's ability to even diagnose what was wrong.
//
// Fix: per-function exponential backoff keyed by CFS.AttemptCount.
// Reconcile consults the backoff window before issuing
// CreateCheckpoint; if we're still within the window after the
// previous failure, the attempt is suppressed (logged at Warn, no
// requeue, function continues cold) until the window elapses.
//
// AttemptCount resets to 0 on every successful checkpoint (already
// wired in reconciler.go's success path). So the backoff only
// applies to true failure streaks; a single bad attempt on an
// otherwise-healthy function doesn't impose any future penalty.

package reconciler

import (
	"time"
)

// checkpointBackoffSchedule is the suppression window after the Nth
// consecutive failed attempt. After N=4, we cap at 24h — that gives
// the operator a full ops-cycle to notice and intervene without the
// function being abandoned forever (the 24h tick still lets a fixed
// agent or rebuilt cluster recover automatically).
//
// Exposed as a var (not const) so tests can shrink the windows
// without monkey-patching time.Now.
var checkpointBackoffSchedule = []time.Duration{
	5 * time.Minute,  // after 1st failure
	30 * time.Minute, // after 2nd
	2 * time.Hour,    // after 3rd
	24 * time.Hour,   // after 4th and beyond
}

// backoffWindow returns the suppression duration appropriate for the
// given prior attempt count. attemptCount==0 → no backoff (first
// attempt has nothing to wait on). attemptCount>=len(schedule) → cap.
func backoffWindow(attemptCount int64) time.Duration {
	if attemptCount <= 0 {
		return 0
	}
	idx := int(attemptCount) - 1
	if idx >= len(checkpointBackoffSchedule) {
		idx = len(checkpointBackoffSchedule) - 1
	}
	return checkpointBackoffSchedule[idx]
}

// shouldSuppressAttempt reports whether a new checkpoint attempt
// should be skipped because the previous failure is still inside
// the backoff window. Returns the wall-clock time the suppression
// expires; callers log this so the operator can see when the next
// attempt is eligible.
//
// AttemptCount==0 OR LastAttemptAt==nil → never suppress (no prior
// failure on record).
//
// Suppression decision is "(now - LastAttemptAt) < window". We do
// NOT also gate on CapturedHere/LocalCacheState because NvSnap treats
// a Warm cache as a success path already: AttemptCount is reset on
// success, so a Warm function naturally has count=0 and skips this
// path. A function that's been Warm and then needs a fresh capture
// (hash invalidation, NVCA restart) starts fresh with count=0.
func shouldSuppressAttempt(prev cfsStatus, now time.Time) (suppress bool, until time.Time) {
	if prev.AttemptCount <= 0 || prev.LastAttemptAt == nil {
		return false, time.Time{}
	}
	window := backoffWindow(prev.AttemptCount)
	if window <= 0 {
		return false, time.Time{}
	}
	expires := prev.LastAttemptAt.Time.Add(window)
	if now.Before(expires) {
		return true, expires
	}
	return false, time.Time{}
}
