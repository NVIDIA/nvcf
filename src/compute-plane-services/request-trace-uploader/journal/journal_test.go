// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journal_test

import (
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

const (
	contentA = "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4"
	contentB = "f2ca1bb6c7e907d06dafe4687e579fce76b37e4e93b7605022da52e6ccc26fd2"
)

func TestSegmentIDIsStableHexForSameIdentity(t *testing.T) {
	first := journal.SegmentID("request-trace", 7, 1024, contentA)
	second := journal.SegmentID("request-trace", 7, 1024, contentA)
	if first != second {
		t.Fatalf("SegmentID not stable: %q then %q", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("SegmentID = %q, want 64 lowercase hex characters", first)
	}
}

func TestSegmentIDChangesWhenAnyIdentityFieldChanges(t *testing.T) {
	base := journal.SegmentID("request-trace", 7, 1024, contentA)
	cases := []struct {
		name string
		id   string
	}{
		// F22: Dynamo restarted on an empty spool and reused the name with
		// new content of the same size.
		{"reused name, new content", journal.SegmentID("request-trace", 7, 1024, contentB)},
		{"different index", journal.SegmentID("request-trace", 8, 1024, contentA)},
		{"different size", journal.SegmentID("request-trace", 7, 1025, contentA)},
		{"different prefix", journal.SegmentID("audit-trace", 7, 1024, contentA)},
		// Field boundaries must not be ambiguous when values are joined.
		{"prefix and index boundary", journal.SegmentID("request-trace1", 7, 1024, contentA)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.id == base {
				t.Fatalf("SegmentID did not change for %s", tc.name)
			}
		})
	}
}

func TestSegmentTransitionsFollowTheStateMachine(t *testing.T) {
	cases := []struct {
		from, to journal.State
		want     bool
	}{
		{journal.StateDiscovered, journal.StatePreparing, true},
		{journal.StatePreparing, journal.StatePrepared, true},
		{journal.StatePreparing, journal.StateEmpty, true},
		{journal.StatePreparing, journal.StateQuarantined, true},
		{journal.StatePreparing, journal.StateDiscovered, true},
		{journal.StatePrepared, journal.StateSubmitting, true},
		{journal.StateSubmitting, journal.StatePrepared, true},
		{journal.StateSubmitting, journal.StateConfirmed, true},
		{journal.StateSubmitting, journal.StateQuarantined, true},
		{journal.StateConfirmed, journal.StateCleaning, true},
		{journal.StateCleaning, journal.StateDone, true},
		{journal.StateEmpty, journal.StateDone, true},
		{journal.StateQuarantined, journal.StateDone, true},

		{journal.StateDiscovered, journal.StateSubmitting, false},
		{journal.StateDiscovered, journal.StateDone, false},
		{journal.StatePrepared, journal.StateConfirmed, false},
		{journal.StateSubmitting, journal.StateDone, false},
		{journal.StateConfirmed, journal.StateDone, false},
		{journal.StateDone, journal.StateDiscovered, false},
		{journal.StateDone, journal.StateCleaning, false},
		{journal.StateQuarantined, journal.StateSubmitting, false},
		{journal.StateCleaning, journal.StateConfirmed, false},
		{journal.State("bogus"), journal.StatePreparing, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			if got := journal.CanTransition(tc.from, tc.to); got != tc.want {
				t.Fatalf("CanTransition(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestBatchTransitionsFollowTheStateMachine(t *testing.T) {
	cases := []struct {
		from, to journal.State
		want     bool
	}{
		{journal.StateSubmitting, journal.StateSubmitted, true},
		{journal.StateSubmitting, journal.StateRetryWait, true},
		{journal.StateSubmitting, journal.StateAmbiguous, true},
		{journal.StateSubmitting, journal.StateReleased, true},
		{journal.StateRetryWait, journal.StateSubmitting, true},
		{journal.StateAmbiguous, journal.StateSubmitting, true},
		{journal.StateAmbiguous, journal.StateReleased, true},
		{journal.StateSubmitted, journal.StateConfirmed, true},
		{journal.StateSubmitted, journal.StateAmbiguous, true},
		{journal.StateSubmitted, journal.StateStranded, true},
		{journal.StateSubmitted, journal.StateReleased, true},
		{journal.StateStranded, journal.StateConfirmed, true},
		// An ambiguous batch with a receipt is polled, so SUCCESS confirms it.
		{journal.StateAmbiguous, journal.StateConfirmed, true},
		// An ambiguous batch with no receipt is parked when retained.
		{journal.StateAmbiguous, journal.StateRetryWait, true},

		// A known job id is polled, never resubmitted.
		{journal.StateSubmitted, journal.StateSubmitting, false},
		{journal.StateSubmitted, journal.StateRetryWait, false},
		// Stranded is kept and never resubmitted automatically.
		{journal.StateStranded, journal.StateSubmitting, false},
		{journal.StateStranded, journal.StateReleased, false},
		{journal.StateConfirmed, journal.StateSubmitting, false},
		{journal.StateReleased, journal.StateSubmitting, false},
		{journal.StateRetryWait, journal.StateSubmitted, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			if got := journal.CanTransitionBatch(tc.from, tc.to); got != tc.want {
				t.Fatalf("CanTransitionBatch(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestRestartActionPerSegmentState(t *testing.T) {
	cases := []struct {
		state journal.State
		want  journal.Action
	}{
		{journal.StateDiscovered, journal.ActionPrepare},
		// F1: a crash during prepare redoes it from the source.
		{journal.StatePreparing, journal.ActionPrepare},
		{journal.StatePrepared, journal.ActionBatch},
		{journal.StateConfirmed, journal.ActionCleanup},
		// F7: a crash mid-cleanup finishes cleanup.
		{journal.StateCleaning, journal.ActionCleanup},
		{journal.StateEmpty, journal.ActionCleanup},
		{journal.StateDone, journal.ActionNone},
		{journal.StateQuarantined, journal.ActionNone},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := journal.RestartAction(tc.state); got != tc.want {
				t.Fatalf("RestartAction(%s) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

func TestNotReadyReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unreadable state", journal.ErrUnreadableState, "unreadable_state"},
		{"wrapped unreadable state", fmt.Errorf("open journal: %w", journal.ErrUnreadableState), "unreadable_state"},
		{"joined unreadable state", errors.Join(errors.New("other"), journal.ErrUnreadableState), "unreadable_state"},
		{"lock held", journal.ErrLocked, ""},
		{"no error", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := journal.NotReadyReason(tc.err); got != tc.want {
				t.Fatalf("NotReadyReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
