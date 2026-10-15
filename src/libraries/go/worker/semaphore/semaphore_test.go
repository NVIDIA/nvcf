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

package semaphore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// waitForCond polls cond until it returns true or a generous deadline elapses.
// It synchronizes on observable semaphore state (a waiter enqueuing, a token
// freeing) so the concurrency tests stay deterministic without asserting on
// wall-clock durations.
func waitForCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestTryAcquireAndRelease(t *testing.T) {
	s := NewWeighted(2)
	require.True(t, s.TryAcquire(1))
	require.True(t, s.TryAcquire(1))
	require.False(t, s.TryAcquire(1), "semaphore is full")
	s.Release(1)
	require.True(t, s.TryAcquire(1), "a slot freed up")
}

func TestAcquireSucceedsWhenCapacityAvailable(t *testing.T) {
	s := NewWeighted(2)
	require.NoError(t, s.Acquire(context.Background(), 2))
	require.False(t, s.TryAcquire(1), "no capacity left after acquiring all")
}

func TestAcquireLargerThanSizeReturnsCtxErr(t *testing.T) {
	s := NewWeighted(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// n > size can never succeed; Acquire must return ctx.Err() rather than
	// block forever or wedge other callers.
	require.ErrorIs(t, s.Acquire(ctx, 2), context.Canceled)
	// The semaphore is left unchanged: a valid acquire still works.
	require.True(t, s.TryAcquire(1))
}

func TestAcquireBlocksThenReleaseWakesWaiter(t *testing.T) {
	s := NewWeighted(1)
	require.True(t, s.TryAcquire(1)) // full

	done := make(chan error, 1)
	go func() { done <- s.Acquire(context.Background(), 1) }()

	// MaxAvailable = size - waiters - cur = 1 - 1 - 1 = -1 once the waiter enqueues.
	waitForCond(t, func() bool { return s.MaxAvailable() == -1 })

	s.Release(1) // should hand the freed token to the blocked waiter

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Acquire was not woken by Release")
	}
}

func TestAcquireCanceledWhileWaitingReturnsErrAndRemovesWaiter(t *testing.T) {
	s := NewWeighted(1)
	require.True(t, s.TryAcquire(1)) // full

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Acquire(ctx, 1) }()

	waitForCond(t, func() bool { return s.MaxAvailable() == -1 }) // waiter enqueued
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("canceled Acquire did not return")
	}
	// The canceled waiter is removed from the queue (back to size - cur = 0).
	waitForCond(t, func() bool { return s.MaxAvailable() == 0 })
}

func TestReleaseMoreThanHeldPanics(t *testing.T) {
	s := NewWeighted(1)
	require.PanicsWithValue(t, "semaphore: released more than held", func() {
		s.Release(1) // nothing held
	})
}

func TestAcquireMaxFresh(t *testing.T) {
	s := NewWeighted(5)
	require.Equal(t, int64(5), s.AcquireMax(), "grabs all tokens on a fresh semaphore")
	require.Equal(t, int64(0), s.MaxAvailable())
}

func TestAcquireMaxPartial(t *testing.T) {
	s := NewWeighted(5)
	require.True(t, s.TryAcquire(2))
	require.Equal(t, int64(3), s.AcquireMax(), "grabs the remaining tokens")
	require.Equal(t, int64(0), s.MaxAvailable())
}

func TestMaxAvailableDoesNotMutate(t *testing.T) {
	s := NewWeighted(3)
	require.True(t, s.TryAcquire(1))
	require.Equal(t, int64(2), s.MaxAvailable())
	require.Equal(t, int64(2), s.MaxAvailable(), "MaxAvailable must not consume tokens")
	require.True(t, s.TryAcquire(2), "two tokens are still available")
}

// A queued waiter blocks TryAcquire and smaller requests even when a token is
// free. This is the anti-starvation property: a large request at the front of
// the queue holds the line rather than letting smaller requests jump ahead.
func TestQueuedWaiterBlocksTryAcquireAndSmallerRequests(t *testing.T) {
	s := NewWeighted(2)
	require.True(t, s.TryAcquire(2)) // full

	done := make(chan error, 1)
	go func() { done <- s.Acquire(context.Background(), 2) }() // waiter wants 2

	waitForCond(t, func() bool { return s.MaxAvailable() == -1 }) // waiter enqueued

	s.Release(1) // frees one token, but the waiter needs 2, so it stays queued
	require.False(t, s.TryAcquire(1), "a queued waiter blocks TryAcquire even with a free token")

	s.Release(1) // now 2 free, the waiter is satisfied
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("waiter was not satisfied once enough tokens freed")
	}
}

// Race-detector smoke test: many concurrent Acquire/Release must not panic,
// deadlock, or race. Meaningful under `go test -race`.
func TestConcurrentAcquireReleaseNoRace(t *testing.T) {
	s := NewWeighted(4)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Acquire(context.Background(), 1)
			if err == nil {
				s.Release(1)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int64(4), s.MaxAvailable(), "all tokens returned")
}
