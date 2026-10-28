// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

type event struct {
	at       time.Duration
	key      string
	reason   backend.Reason
	attempts int
	success  bool
}

func TestCorrelator(t *testing.T) {
	const x, y backend.Reason = "x", "y"
	tests := []struct {
		name   string
		events []event
		want   backend.Scope
	}{
		{
			name:   "three segments fail alike in the window",
			events: []event{{at: 0, key: "a", reason: x, attempts: 1}, {at: time.Minute, key: "b", reason: x, attempts: 1}, {at: 9 * time.Minute, key: "c", reason: x, attempts: 1}},
			want:   backend.ScopeBackend,
		},
		{
			name:   "two segments fail alike",
			events: []event{{at: 0, key: "a", reason: x, attempts: 1}, {at: time.Minute, key: "b", reason: x, attempts: 1}},
			want:   backend.ScopeUnknown,
		},
		{
			name:   "three segments fail with different reasons",
			events: []event{{at: 0, key: "a", reason: x, attempts: 1}, {at: 0, key: "b", reason: y, attempts: 1}, {at: 0, key: "c", reason: x, attempts: 1}},
			want:   backend.ScopeUnknown,
		},
		{
			name:   "an old failure has left the window",
			events: []event{{at: 0, key: "a", reason: x, attempts: 1}, {at: 5 * time.Minute, key: "b", reason: x, attempts: 1}, {at: 11 * time.Minute, key: "c", reason: x, attempts: 1}},
			want:   backend.ScopeUnknown,
		},
		{
			name:   "one segment failing repeatedly is not three segments",
			events: []event{{at: 0, key: "a", reason: x, attempts: 1}, {at: time.Minute, key: "a", reason: x, attempts: 2}, {at: 2 * time.Minute, key: "a", reason: x, attempts: 3}},
			want:   backend.ScopeUnknown,
		},
		{
			name:   "one segment fails five times while another succeeds",
			events: []event{{at: 0, key: "b", success: true}, {at: time.Minute, key: "a", reason: x, attempts: 5}},
			want:   backend.ScopeSegment,
		},
		{
			name:   "five failures with no other success",
			events: []event{{at: time.Minute, key: "a", reason: x, attempts: 5}},
			want:   backend.ScopeUnknown,
		},
		{
			name:   "the only success is the same segment",
			events: []event{{at: 0, key: "a", success: true}, {at: time.Minute, key: "a", reason: x, attempts: 5}},
			want:   backend.ScopeUnknown,
		},
		{
			name:   "the other success has left the window",
			events: []event{{at: 0, key: "b", success: true}, {at: 11 * time.Minute, key: "a", reason: x, attempts: 5}},
			want:   backend.ScopeUnknown,
		},
		{
			name:   "four failures are not yet segment scope",
			events: []event{{at: 0, key: "b", success: true}, {at: time.Minute, key: "a", reason: x, attempts: 4}},
			want:   backend.ScopeUnknown,
		},
		{
			name: "backend scope wins over segment scope",
			events: []event{
				{at: 0, key: "z", success: true},
				{at: 0, key: "b", reason: x, attempts: 1},
				{at: 0, key: "c", reason: x, attempts: 1},
				{at: time.Minute, key: "a", reason: x, attempts: 5},
			},
			want: backend.ScopeBackend,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			correlator := NewCorrelator(cfg.CorrelationWindow, cfg.BackendScopeSegments, cfg.SegmentScopeFailures)
			var got backend.Scope
			for _, e := range tt.events {
				if e.success {
					correlator.Success(t0.Add(e.at), e.key)
					continue
				}
				got = correlator.Failure(t0.Add(e.at), e.key, e.reason, e.attempts)
			}
			if got != tt.want {
				t.Fatalf("scope of the last failure = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCorrelatorForgetsOldEntries(t *testing.T) {
	correlator := NewCorrelator(10*time.Minute, 3, 5)
	for i := 0; i < 100; i++ {
		at := t0.Add(time.Duration(i) * time.Minute)
		correlator.Success(at, string(rune('a'+i%26))+"-ok")
		correlator.Failure(at, string(rune('a'+i%26))+"-bad", backend.Reason("r"+string(rune('a'+i%7))), 1)
	}
	if n := correlator.size(); n > 2*11 {
		t.Fatalf("correlator holds %d entries after 100 minutes, want only the last window", n)
	}
}
