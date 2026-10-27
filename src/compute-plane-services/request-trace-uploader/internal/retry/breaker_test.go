// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import "testing"

func TestBreaker(t *testing.T) {
	breaker := NewBreaker(5)
	for i := 1; i <= 4; i++ {
		if breaker.Quarantined() || breaker.Tripped() {
			t.Fatalf("quarantine %d tripped the breaker, want it to trip at 5", i)
		}
	}
	if !breaker.Quarantined() || !breaker.Tripped() {
		t.Fatalf("fifth quarantine in a row did not trip the breaker")
	}
	if breaker.Quarantined() {
		t.Fatalf("a quarantine after the trip reported a new trip, want it reported once")
	}
	breaker.Succeeded()
	if !breaker.Tripped() {
		t.Fatalf("a success closed a tripped breaker, want it latched until Reset")
	}
	breaker.Reset()
	if breaker.Tripped() {
		t.Fatalf("Reset() left the breaker tripped")
	}

	for i := 1; i <= 4; i++ {
		breaker.Quarantined()
	}
	breaker.Succeeded()
	for i := 1; i <= 4; i++ {
		if breaker.Quarantined() {
			t.Fatalf("quarantine %d after a success tripped the breaker, want the run restarted", i)
		}
	}
}
