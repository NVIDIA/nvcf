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
	"errors"
	"sync"
	"testing"
)

func newTestCache() *h3ConnectionCache {
	return &h3ConnectionCache{clients: make(map[string]*roundTripperWithCount)}
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

	dialErr := errors.New("timeout: no recent network activity")
	for i := 1; i < dialFailuresBeforeRotate; i++ {
		c.noteDialResult(before, dialErr)
		if c.quicTransport != before {
			t.Fatalf("rotated after %d failures, want %d", i, dialFailuresBeforeRotate)
		}
	}

	c.noteDialResult(before, dialErr)
	if c.quicTransport != nil {
		t.Fatal("expected the transport to be released at the threshold")
	}

	after, err := c.transport()
	if err != nil {
		t.Fatalf("transport after rotation: %v", err)
	}
	if after.Conn.LocalAddr().String() == portBefore {
		t.Fatal("expected a new source port after rotation; the NLB hashes on it")
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
	dialErr := errors.New("timeout: no recent network activity")
	for range 50 {
		for i := 1; i < dialFailuresBeforeRotate; i++ {
			c.noteDialResult(tr, dialErr)
		}
		c.noteDialResult(tr, nil)
	}
	if c.quicTransport != tr {
		t.Fatal("rotated despite every failure run being broken by a success")
	}
}

// A dial that was in flight across a rotation must not discard the replacement
// socket, which would throw away a transport never shown to be bad.
func TestStaleDialDoesNotRotateReplacement(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	stale, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	dialErr := errors.New("timeout: no recent network activity")
	for range dialFailuresBeforeRotate {
		c.noteDialResult(stale, dialErr)
	}
	replacement, err := c.transport()
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	for range dialFailuresBeforeRotate {
		c.noteDialResult(stale, dialErr)
	}
	if c.quicTransport != replacement {
		t.Fatal("a stale dial rotated the replacement transport")
	}
}

// dial runs on a goroutine that does not hold the client mutex, so the shared
// transport was previously read and written unsynchronised. Run under -race.
func TestConcurrentTransportAccessIsRaceFree(t *testing.T) {
	c := newTestCache()
	defer c.Close()

	dialErr := errors.New("timeout: no recent network activity")
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
				c.noteDialResult(tr, dialErr)
				c.noteDialResult(tr, nil)
			}
		}()
	}
	wg.Wait()
}
