// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDecorrelatedBackoffBounds(t *testing.T) {
	b := Backoff{Base: 5 * time.Second, Max: 5 * time.Minute}

	if got := b.Decorrelated(0, 0); got != b.Base {
		t.Fatalf("first delay at r=0 = %v, want base %v", got, b.Base)
	}
	if got := b.Decorrelated(0, 0.999); got <= b.Base || got > 3*b.Base {
		t.Fatalf("first delay at r=0.999 = %v, want in (%v, %v]", got, b.Base, 3*b.Base)
	}
	if got := b.Decorrelated(20*time.Second, 0.5); got != 5*time.Second+(55*time.Second)/2 {
		t.Fatalf("delay after 20s at r=0.5 = %v, want halfway between base and 60s", got)
	}

	prev := time.Duration(0)
	for i := 0; i < 200; i++ {
		r := float64(i%10) / 10
		prev = b.Decorrelated(prev, r)
		if prev < b.Base || prev > b.Max {
			t.Fatalf("step %d: delay %v outside [%v, %v]", i, prev, b.Base, b.Max)
		}
	}
	if got := b.Decorrelated(b.Max, 0.999); got != b.Max {
		t.Fatalf("delay after max at r=0.999 = %v, want capped at %v", got, b.Max)
	}
}

func TestDoublingBackoff(t *testing.T) {
	b := Backoff{Base: 60 * time.Second, Max: 1800 * time.Second}
	want := []time.Duration{60, 120, 240, 480, 960, 1800, 1800}
	prev := time.Duration(0)
	for i, w := range want {
		prev = b.Doubling(prev)
		if prev != w*time.Second {
			t.Fatalf("step %d: delay %v, want %v", i, prev, w*time.Second)
		}
	}
}

func TestStateSurvivesReload(t *testing.T) {
	b := Backoff{Base: 5 * time.Second, Max: 5 * time.Minute}
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	var state State
	state = state.Failed(t0, b, 0.5)
	state = state.Failed(t0.Add(time.Minute), b, 0.5)
	if state.Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2", state.Attempts)
	}
	if state.Due(state.NextAt.Add(-time.Nanosecond)) || !state.Due(state.NextAt) {
		t.Fatalf("Due() is not false just before NextAt %v and true at it", state.NextAt)
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var reloaded State
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if reloaded.Attempts != state.Attempts || reloaded.Delay != state.Delay || !reloaded.NextAt.Equal(state.NextAt) {
		t.Fatalf("reloaded state = %+v, want %+v", reloaded, state)
	}

	t2 := t0.Add(10 * time.Minute)
	before, after := state.Failed(t2, b, 0.7), reloaded.Failed(t2, b, 0.7)
	if before.Delay != after.Delay || !before.NextAt.Equal(after.NextAt) || before.Attempts != after.Attempts {
		t.Fatalf("next failure after reload = %+v, want %+v", after, before)
	}
}
