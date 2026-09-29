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
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

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
	before := map[string]float64{
		nvcf.DialFailureTimeout: failures(nvcf.DialFailureTimeout),
		nvcf.DialFailureAuth:    failures(nvcf.DialFailureAuth),
		nvcf.DialFailureOther:   failures(nvcf.DialFailureOther),
	}

	recordDialAttempt(context.Background(), dialTimeoutError{})
	recordDialAttempt(context.Background(), ErrAuth)
	recordDialAttempt(context.Background(), errors.New("tls: bad certificate"))

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
	dialsBefore := testutil.ToFloat64(nvcf.QuicDialCounter)
	before := failures(nvcf.DialFailureTimeout)

	recordDialAttempt(context.Background(), nil)

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

func skips(reason string) float64 {
	return testutil.ToFloat64(nvcf.QuicDialSkipCounter.WithLabelValues(reason))
}

// Each skip path is otherwise a silent return, and a silent return that reads
// as "nothing wrong" is the defect class this area keeps producing. Counting
// them separates "rotation never fired" from "rotation fired and did not help".
func TestSkipPathsAreCountedByReason(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	tr, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}

	// A non-timeout error must not count toward rotation, and must be recorded.
	before := skips(nvcf.DialSkipNotTimeout)
	c.noteDialResult(context.Background(), tr, "dest-a", errors.New("connection refused"))
	if got := skips(nvcf.DialSkipNotTimeout) - before; got != 1 {
		t.Errorf("not_timeout skips moved by %v, want 1", got)
	}
	if c.dialFailures["dest-a"] != 0 {
		t.Error("a non-timeout error incremented the rotation counter")
	}

	// A cancelled context is attributed separately, not lumped in with it.
	beforeCtx := skips(nvcf.DialSkipCtxCancelled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.noteDialResult(ctx, tr, "dest-b", &net.DNSError{IsTimeout: true})
	if got := skips(nvcf.DialSkipCtxCancelled) - beforeCtx; got != 1 {
		t.Errorf("ctx_cancelled skips moved by %v, want 1", got)
	}

	// A dial from a superseded transport is attributed to staleness.
	beforeStale := skips(nvcf.DialSkipStaleTransport)
	c.noteDialResult(context.Background(), &quic.Transport{}, "dest-c", &net.DNSError{IsTimeout: true})
	if got := skips(nvcf.DialSkipStaleTransport) - beforeStale; got != 1 {
		t.Errorf("stale_transport skips moved by %v, want 1", got)
	}

	// A genuine network timeout on the current transport still counts.
	c.noteDialResult(context.Background(), tr, "dest-d", &net.DNSError{IsTimeout: true})
	if c.dialFailures["dest-d"] != 1 {
		t.Errorf("dialFailures[dest-d] = %d, want 1", c.dialFailures["dest-d"])
	}
}

// Close clears the client map directly. The cache-removal callback cannot cover
// it: cl.Close fires that callback on another goroutine, which blocks on the
// cache mutex until Close returns and then finds the map already nil. Without
// an explicit decrement the gauge leaks every tunnel the cache ever held.
func TestTunnelGaugeReturnsToZeroAfterClose(t *testing.T) {
	c := newTestCache()

	before := testutil.ToFloat64(nvcf.QuicTunnelGauge)

	c.mutex.Lock()
	for _, h := range []string{"a.example:443", "b.example:443", "c.example:443"} {
		dialed := make(chan struct{})
		close(dialed) // Close waits on this; a live dial would block the test
		c.clients[h] = &roundTripperWithCount{cancel: func() {}, dialing: dialed}
		nvcf.QuicTunnelGauge.Inc()
	}
	c.mutex.Unlock()

	if got := testutil.ToFloat64(nvcf.QuicTunnelGauge) - before; got != 3 {
		t.Fatalf("after three adds: gauge moved by %v, want 3", got)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := testutil.ToFloat64(nvcf.QuicTunnelGauge) - before; got != 0 {
		t.Errorf("after Close: gauge moved by %v, want 0 (tunnels leaked)", got)
	}
}

// A dial that cannot obtain a socket is still a dial attempt. If it were not
// counted, a worker unable to bind would report no dial activity rather than
// failures, which reads as idle rather than broken. That is the same
// silent-zero shape as the defect this package exists to make visible.
func TestTransportInitFailureIsCountedAsADialAttempt(t *testing.T) {
	c := newTestCache()
	defer c.Close()
	c.wrappedTransport = &http3.Transport{}

	orig := listenUDP
	listenUDP = func(string, *net.UDPAddr) (*net.UDPConn, error) {
		return nil, errors.New("bind: cannot allocate memory")
	}
	defer func() { listenUDP = orig }()

	dialsBefore := testutil.ToFloat64(nvcf.QuicDialCounter)
	failBefore := failures(nvcf.DialFailureOther)

	if _, _, err := c.dial(context.Background(), "proxy.example:443"); err == nil {
		t.Fatal("expected dial to fail when the socket cannot be bound")
	}

	if got := testutil.ToFloat64(nvcf.QuicDialCounter) - dialsBefore; got != 1 {
		t.Errorf("dial attempts moved by %v, want 1", got)
	}
	if got := failures(nvcf.DialFailureOther) - failBefore; got != 1 {
		t.Errorf("other failures moved by %v, want 1", got)
	}
}
