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
	"sync"
	"testing"
)

const testDestination = "192.0.2.1:443"

type dialTimeoutError struct{}

func (dialTimeoutError) Error() string   { return "timeout: no recent network activity" }
func (dialTimeoutError) Timeout() bool   { return true }
func (dialTimeoutError) Temporary() bool { return false }

func newTestCache() *h3ConnectionCache {
	return &h3ConnectionCache{
		clients:      make(map[string]*roundTripperWithCount),
		dialFailures: make(map[string]int),
	}
}

// One transport is one socket: every dial shares a single source port. This is
// the property that makes retrying useless when the flow is pinned to a dead
// proxy pod, and the reason rotation is the only worker-side remedy.
func TestTransportIsSharedUntilRotated(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	first, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	second, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if first != second {
		t.Fatal("expected the transport to be reused across dials")
	}
	if first.Conn.LocalAddr().String() != second.Conn.LocalAddr().String() {
		t.Fatal("expected a stable source port while the transport is shared")
	}
}

func TestRotatesOnlyAfterThresholdAndChangesSourcePort(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	before, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	portBefore := before.Conn.LocalAddr().String()

	dialErr := dialTimeoutError{}
	for i := 1; i < dialFailuresBeforeRotate; i++ {
		c.noteDialResult(context.Background(), before, testDestination, dialErr)
		if c.quicTransport != before {
			t.Fatalf("rotated after %d failures, want %d", i, dialFailuresBeforeRotate)
		}
	}

	c.noteDialResult(context.Background(), before, testDestination, dialErr)
	after, err := c.transport()
	if err != nil {
		t.Fatalf("transport after rotation: %v", err)
	}
	if after == before {
		t.Fatal("expected the transport to be replaced at the threshold")
	}
	// The replacement is bound before the old socket is closed precisely so
	// the kernel cannot hand back the port that was just freed. If it did, the
	// worker would stay on the same 5-tuple and the rotation would be a no-op.
	if after.Conn.LocalAddr().String() == portBefore {
		t.Fatal("expected a new source port after rotation; the NLB hashes on it")
	}
	if len(c.dialFailures) != 0 {
		t.Fatalf("dialFailures = %v after rotation, want empty", c.dialFailures)
	}
}

func TestDialFailuresAreScopedToDestination(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	tr, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	dialErr := dialTimeoutError{}
	destinationA := "192.0.2.1:443"
	destinationB := "192.0.2.2:443"

	for range dialFailuresBeforeRotate - 1 {
		c.noteDialResult(context.Background(), tr, destinationA, dialErr)
	}
	c.noteDialResult(context.Background(), tr, destinationB, dialErr)
	if c.quicTransport != tr {
		t.Fatal("failures to different destinations triggered rotation")
	}

	c.noteDialResult(context.Background(), tr, destinationB, nil)
	if got := c.dialFailures[destinationA]; got != dialFailuresBeforeRotate-1 {
		t.Fatalf("success on destination B reset destination A to %d", got)
	}

	c.noteDialResult(context.Background(), tr, destinationA, dialErr)
	if c.quicTransport == tr {
		t.Fatal("destination A did not rotate after reaching its threshold")
	}
}

func TestOnlyUncancelledNetworkTimeoutsCountAsDialFailures(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	tr, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}

	for range dialFailuresBeforeRotate {
		c.noteDialResult(context.Background(), tr, testDestination, errors.New("tls: failed to verify certificate"))
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	for range dialFailuresBeforeRotate {
		c.noteDialResult(canceledCtx, tr, testDestination, dialTimeoutError{})
	}
	if c.quicTransport != tr {
		t.Fatal("non-transport dial errors triggered rotation")
	}
	if len(c.dialFailures) != 0 {
		t.Fatalf("non-transport dial errors changed failure state: %v", c.dialFailures)
	}
}

// A dial that began before a rotation must not move the counter. Otherwise
// stale failures accumulate against the replacement and one genuine failure of
// its own is enough to discard a socket that was never shown to be bad.
func TestStaleFailuresDoNotCountAgainstReplacement(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	stale, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	dialErr := dialTimeoutError{}
	for range dialFailuresBeforeRotate {
		c.noteDialResult(context.Background(), stale, testDestination, dialErr)
	}
	replacement := c.quicTransport
	if replacement == stale {
		t.Fatal("expected a rotation")
	}

	// Several more results from the old socket arrive late.
	for range dialFailuresBeforeRotate * 2 {
		c.noteDialResult(context.Background(), stale, testDestination, dialErr)
	}
	if len(c.dialFailures) != 0 {
		t.Fatalf("stale failures changed failure state to %v, want empty", c.dialFailures)
	}

	// One genuine failure of the replacement's own must not be enough.
	c.noteDialResult(context.Background(), replacement, testDestination, dialErr)
	if c.quicTransport != replacement {
		t.Fatal("replacement rotated after a single failure of its own")
	}
}

// A stale success must not clear failures the current socket really had.
func TestStaleSuccessDoesNotResetReplacement(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	stale, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	dialErr := dialTimeoutError{}
	for range dialFailuresBeforeRotate {
		c.noteDialResult(context.Background(), stale, testDestination, dialErr)
	}
	replacement := c.quicTransport

	for i := 1; i < dialFailuresBeforeRotate; i++ {
		c.noteDialResult(context.Background(), replacement, testDestination, dialErr)
	}
	c.noteDialResult(context.Background(), stale, testDestination, nil) // late success from the discarded socket
	if got := c.dialFailures[testDestination]; got != dialFailuresBeforeRotate-1 {
		t.Fatalf("a stale success reset the counter to %d", got)
	}
	c.noteDialResult(context.Background(), replacement, testDestination, dialErr)
	if c.quicTransport == replacement {
		t.Fatal("expected rotation once the replacement reached the threshold")
	}
}

// A worker that is dialling successfully must never rotate, however many
// isolated failures it accumulates over its lifetime.
func TestSuccessResetsFailureCount(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	tr, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	dialErr := dialTimeoutError{}
	for range 50 {
		for i := 1; i < dialFailuresBeforeRotate; i++ {
			c.noteDialResult(context.Background(), tr, testDestination, dialErr)
		}
		c.noteDialResult(context.Background(), tr, testDestination, nil)
	}
	if c.quicTransport != tr {
		t.Fatal("rotated despite every failure run being broken by a success")
	}
}

// dial runs on a goroutine that does not hold the client mutex, so the shared
// transport was previously read and written unsynchronised. Run under -race.
func TestConcurrentTransportAccessIsRaceFree(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	dialErr := dialTimeoutError{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				tr, err := c.transport()
				if err != nil {
					continue
				}
				c.noteDialResult(context.Background(), tr, testDestination, dialErr)
				c.noteDialResult(context.Background(), tr, testDestination, nil)
			}
		}()
	}
	wg.Wait()
}
