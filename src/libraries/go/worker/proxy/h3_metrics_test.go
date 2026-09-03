/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
)

// The counters are process-global, so every assertion is a delta.
func failures(reason string) float64 {
	return testutil.ToFloat64(nvcf.QuicDialFailureCounter.WithLabelValues(reason))
}

// The whole point of the reason label is to separate the two failures that
// share the log message "quic connection attempt failed". A network timeout is
// flow poisoning; a 403 is the backlog wedge. Conflating them is what made the
// original incident untriageable.
func TestDialFailureReasonSeparatesTimeoutFromAuth(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	tr, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}

	before := map[string]float64{
		nvcf.DialFailureTimeout: failures(nvcf.DialFailureTimeout),
		nvcf.DialFailureAuth:    failures(nvcf.DialFailureAuth),
		nvcf.DialFailureOther:   failures(nvcf.DialFailureOther),
	}

	c.noteDialResult(context.Background(), tr, testDestination, dialTimeoutError{})
	c.noteDialResult(context.Background(), c.quicTransport, testDestination, ErrAuth)
	c.noteDialResult(context.Background(), c.quicTransport, testDestination, errors.New("tls: bad certificate"))

	for reason, want := range map[string]float64{
		nvcf.DialFailureTimeout: 1,
		nvcf.DialFailureAuth:    1,
		nvcf.DialFailureOther:   1,
	} {
		if got := failures(reason) - before[reason]; got != want {
			t.Errorf("reason %q: got %v new failures, want %v", reason, got, want)
		}
	}
}

// A successful dial must not register as a failure, otherwise the alert
// expression pages on healthy traffic.
func TestSuccessfulDialRecordsNoFailure(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	tr, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}

	dialsBefore := testutil.ToFloat64(nvcf.QuicDialCounter)
	before := failures(nvcf.DialFailureTimeout)

	c.noteDialResult(context.Background(), tr, testDestination, nil)

	if got := failures(nvcf.DialFailureTimeout) - before; got != 0 {
		t.Errorf("successful dial recorded %v failures, want 0", got)
	}
	if got := testutil.ToFloat64(nvcf.QuicDialCounter) - dialsBefore; got != 1 {
		t.Errorf("successful dial recorded %v attempts, want 1", got)
	}
}

// The alert distinguishes "failing and recovering" from "failing and stuck" by
// comparing these two counters. If rotation did not increment its counter, a
// wedged worker would be indistinguishable from a recovering one.
func TestRotationIncrementsCounterOnlyWhenRotating(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	tr, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}

	before := testutil.ToFloat64(nvcf.QuicTransportRotationCounter)

	// Below the threshold: no rotation, so no increment.
	for i := 1; i < dialFailuresBeforeRotate; i++ {
		c.noteDialResult(context.Background(), tr, testDestination, dialTimeoutError{})
	}
	if got := testutil.ToFloat64(nvcf.QuicTransportRotationCounter) - before; got != 0 {
		t.Fatalf("counted %v rotations below the threshold, want 0", got)
	}

	// Crossing it rotates exactly once.
	c.noteDialResult(context.Background(), tr, testDestination, dialTimeoutError{})
	if got := testutil.ToFloat64(nvcf.QuicTransportRotationCounter) - before; got != 1 {
		t.Fatalf("counted %v rotations at the threshold, want 1", got)
	}
}

// Removal is reached from more than one path for the same hostname: the
// AfterFunc on connection close, and the dial-error path in getClient. If the
// decrement were unguarded, the second removal would drive the gauge negative
// and a negative tunnel count would read as an outage that is not happening.
func TestTunnelGaugeStaysBalancedAcrossRepeatedRemoval(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	before := testutil.ToFloat64(nvcf.QuicTunnelGauge)

	c.mutex.Lock()
	c.clients["proxy.example:443"] = &roundTripperWithCount{}
	nvcf.QuicTunnelGauge.Inc()
	c.mutex.Unlock()

	if got := testutil.ToFloat64(nvcf.QuicTunnelGauge) - before; got != 1 {
		t.Fatalf("after one add: gauge moved by %v, want 1", got)
	}

	for i := 0; i < 3; i++ {
		c.removeClient("proxy.example:443")
	}

	if got := testutil.ToFloat64(nvcf.QuicTunnelGauge) - before; got != 0 {
		t.Errorf("after repeated removal: gauge moved by %v, want 0", got)
	}
}
