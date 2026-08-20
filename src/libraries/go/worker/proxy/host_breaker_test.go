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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock drives the state machine without sleeping.
type fakeClock struct{ t atomic.Int64 }

func (c *fakeClock) now() time.Time      { return time.Unix(0, c.t.Load()) }
func (c *fakeClock) add(d time.Duration) { c.t.Add(int64(d)) }

func newTestBreaker() (*hostBreaker, *fakeClock) {
	clock := &fakeClock{}
	clock.t.Store(int64(time.Hour)) // away from the zero time
	b := newHostBreaker()
	b.now = clock.now
	return b, clock
}

func TestHostBreakerOpensOnlyAfterThresholdFailures(t *testing.T) {
	b, _ := newTestBreaker()
	const host = "10-0-0-1.example:443"

	for i := 0; i < hostFailureThreshold-1; i++ {
		require.NoError(t, b.allow(host))
		b.recordFailure(host)
		assert.False(t, b.isOpen(host), "must not open before the threshold")
	}

	require.NoError(t, b.allow(host))
	b.recordFailure(host)
	assert.True(t, b.isOpen(host))
	assert.ErrorIs(t, b.allow(host), ErrHostUnreachable)
}

// One success clears the count, so an occasional failure never accumulates into
// a trip against a host that is working.
func TestHostBreakerSuccessResetsFailureCount(t *testing.T) {
	b, _ := newTestBreaker()
	const host = "10-0-0-2.example:443"

	for i := 0; i < hostFailureThreshold-1; i++ {
		require.NoError(t, b.allow(host))
		b.recordFailure(host)
	}
	require.NoError(t, b.allow(host))
	b.recordSuccess(host)

	for i := 0; i < hostFailureThreshold-1; i++ {
		require.NoError(t, b.allow(host), "count should have restarted after the success")
		b.recordFailure(host)
	}
	assert.False(t, b.isOpen(host))
}

// The retry loop must abort rather than burn its budget failing fast, so the
// refusal has to be permanent as far as backoff is concerned.
func TestHostBreakerRefusalIsPermanent(t *testing.T) {
	b, _ := newTestBreaker()
	const host = "10-0-0-3.example:443"
	for i := 0; i < hostFailureThreshold; i++ {
		require.NoError(t, b.allow(host))
		b.recordFailure(host)
	}

	err := b.allow(host)
	require.Error(t, err)

	var permanent *backoff.PermanentError
	assert.ErrorAs(t, err, &permanent, "refusal must stop the retry loop")
}

func TestHostBreakerAllowsOneProbeAfterOpenDuration(t *testing.T) {
	b, clock := newTestBreaker()
	const host = "10-0-0-4.example:443"
	for i := 0; i < hostFailureThreshold; i++ {
		require.NoError(t, b.allow(host))
		b.recordFailure(host)
	}
	require.ErrorIs(t, b.allow(host), ErrHostUnreachable)

	clock.add(hostOpenDuration + time.Second)

	assert.NoError(t, b.allow(host), "first caller after the open window should probe")
	assert.ErrorIs(t, b.allow(host), ErrHostUnreachable, "only one probe may go out at a time")
}

// A host that comes back must be usable immediately, not after another window.
func TestHostBreakerClosesWhenProbeSucceeds(t *testing.T) {
	b, clock := newTestBreaker()
	const host = "10-0-0-5.example:443"
	for i := 0; i < hostFailureThreshold; i++ {
		require.NoError(t, b.allow(host))
		b.recordFailure(host)
	}
	clock.add(hostOpenDuration + time.Second)
	require.NoError(t, b.allow(host))

	b.recordSuccess(host)

	assert.False(t, b.isOpen(host))
	assert.NoError(t, b.allow(host))
}

// A failed probe restarts the window rather than reopening immediately, so a
// host that stays dead is retried once per window and no more.
func TestHostBreakerFailedProbeReopensForAnotherWindow(t *testing.T) {
	b, clock := newTestBreaker()
	const host = "10-0-0-6.example:443"
	for i := 0; i < hostFailureThreshold; i++ {
		require.NoError(t, b.allow(host))
		b.recordFailure(host)
	}
	clock.add(hostOpenDuration + time.Second)
	require.NoError(t, b.allow(host))

	b.recordFailure(host)

	assert.True(t, b.isOpen(host))
	assert.ErrorIs(t, b.allow(host), ErrHostUnreachable)

	clock.add(hostOpenDuration + time.Second)
	assert.NoError(t, b.allow(host), "a new window should permit another probe")
}

// Hosts are pod addresses and turn over constantly, so the table must not grow
// for the life of the worker.
func TestHostBreakerEvictsIdleHosts(t *testing.T) {
	b, clock := newTestBreaker()

	require.NoError(t, b.allow("10-0-0-7.example:443"))
	clock.add(hostIdleRetention + time.Minute)
	require.NoError(t, b.allow("10-0-0-8.example:443"))

	b.mu.Lock()
	_, stale := b.hosts["10-0-0-7.example:443"]
	b.mu.Unlock()
	assert.False(t, stale, "idle host should have been evicted")
}

func TestHostBreakerStaysBounded(t *testing.T) {
	b, _ := newTestBreaker()
	for i := 0; i < hostBreakerCapacity*2; i++ {
		_ = b.allow(hostName(i))
	}
	b.mu.Lock()
	size := len(b.hosts)
	b.mu.Unlock()
	assert.LessOrEqual(t, size, hostBreakerCapacity)
}

func hostName(i int) string {
	return "host-" + time.Duration(i).String() + ".example:443"
}

func TestHostBreakerConcurrentUse(t *testing.T) {
	b, _ := newTestBreaker()
	const host = "10-0-0-9.example:443"

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := b.allow(host); err != nil {
				return
			}
			if i%2 == 0 {
				b.recordFailure(host)
				return
			}
			b.recordSuccess(host)
		}(i)
	}
	wg.Wait()
}
