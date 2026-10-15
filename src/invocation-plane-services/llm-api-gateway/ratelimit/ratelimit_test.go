/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package ratelimit

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type RateLimiterTestSuite struct {
	suite.Suite

	ctx   context.Context
	store *fakeStore
	now   *atomic.Int64
}

func (s *RateLimiterTestSuite) SetupTest() {
	s.ctx = context.Background()
	s.store = newFakeStore()
	s.now = &atomic.Int64{}
	s.now.Store(time.Unix(1_700_000_000, 0).UnixMilli())
}

func (s *RateLimiterTestSuite) newLimiter(opts ...RateLimiterOption) RateLimiter {
	s.T().Helper()

	opts = append([]RateLimiterOption{
		withClock(func() int64 { return s.now.Load() }),
	}, opts...)

	rl, err := NewRateLimiter(s.store, opts...)
	s.Require().NoError(err)
	return rl
}

func (s *RateLimiterTestSuite) advance(d time.Duration) {
	s.now.Add(d.Milliseconds())
}

func (s *RateLimiterTestSuite) TestTestOnlyDoesNotConsume() {
	var (
		rl = RateLimit{
			Limit:  10,
			Period: time.Minute,
		}
		key = "rl:test:testonly"
	)
	limiter := s.newLimiter()
	res, err := limiter.CheckLimit(s.ctx, key, rl, 3, true, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed())
	s.Equal(int64(10), res.CurrentValue)

	res2, err := limiter.CheckLimit(s.ctx, key, rl, 3, false, "", false)
	s.Require().NoError(err)
	s.True(res2.Allowed())
	s.Equal(int64(10), res2.CurrentValue)
}

func (s *RateLimiterTestSuite) TestConsumeIfAllowedReducesBucket() {
	var (
		rl = RateLimit{
			Limit:  10,
			Period: time.Minute,
		}
		key = "rl:test:consume"
	)
	limiter := s.newLimiter()

	res, err := limiter.CheckLimit(s.ctx, key, rl, 3, false, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed())
	s.Equal(int64(10), res.CurrentValue)
	s.Equal(int64(7), res.RemainingValue())

	res2, err := limiter.CheckLimit(s.ctx, key, rl, 3, true, "", false)
	s.Require().NoError(err)
	s.True(res2.Allowed())
	s.Equal(int64(7), res2.CurrentValue)

	res3, err := limiter.CheckLimit(s.ctx, key, rl, 8, true, "", false)
	s.Require().NoError(err)
	s.False(res3.Allowed())
	s.Equal(6*time.Second, res3.RetryAfter())

	res4, err := limiter.CheckLimit(s.ctx, key, rl, 3, true, "", false)
	s.Require().NoError(err)
	s.True(res4.Allowed())
	s.Equal(int64(7), res4.CurrentValue)
}

func (s *RateLimiterTestSuite) TestMustConsumeConsumesEvenIfNotAllowed() {
	var (
		rl = RateLimit{
			Limit:  10,
			Period: time.Minute,
		}
		key = "rl:test:mustconsume"
	)
	limiter := s.newLimiter()

	res, err := limiter.CheckLimit(s.ctx, key, rl, 15, false, "", true /* mustConsume */)
	s.Require().NoError(err)
	s.False(res.Allowed())
	s.Equal(int64(10), res.CurrentValue)

	res2, err := limiter.CheckLimit(s.ctx, key, rl, 1, true, "", false)
	s.Require().NoError(err)
	s.False(res2.Allowed())
	s.Equal(int64(0), res2.CurrentValue)
}

func (s *RateLimiterTestSuite) TestResetRestoresFullBucket() {
	var (
		rl = RateLimit{
			Limit:  5,
			Period: 10 * time.Second,
		}
		key = "rl:test:reset"
	)
	limiter := s.newLimiter()

	_, err := limiter.CheckLimit(s.ctx, key, rl, 3, false, "", false)
	s.Require().NoError(err)

	s.Require().NoError(limiter.Reset(s.ctx, key))
	res2, err := limiter.CheckLimit(s.ctx, key, rl, 5, true, "", false)
	s.Require().NoError(err)
	s.True(res2.Allowed())
	s.Equal(int64(5), res2.CurrentValue)
}

func (s *RateLimiterTestSuite) TestBucketsRefilledManuallyWithNegativeTokensRequested() {
	var (
		rl = RateLimit{
			Limit:  100,
			Period: time.Minute,
		}
		key = "rl:test:refill_manually"
	)
	limiter := s.newLimiter()

	res, err := limiter.CheckLimit(s.ctx, key, rl, 50, false, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed())
	s.Equal(int64(100), res.CurrentValue)
	s.Equal(int64(50), res.RemainingValue())

	res2, err := limiter.CheckLimit(s.ctx, key, rl, 50, true, "", false)
	s.Require().NoError(err)
	s.True(res2.Allowed())
	s.Equal(int64(50), res2.CurrentValue)
	s.Equal(int64(0), res2.RemainingValue())

	res3, err := limiter.CheckLimit(s.ctx, key, rl, -20, false, "", true)
	s.Require().NoError(err)
	s.True(res3.Allowed())
	s.Equal(int64(50), res3.CurrentValue)
	s.Equal(int64(70), res3.RemainingValue())

	res4, err := limiter.CheckLimit(s.ctx, key, rl, 70, true, "", false)
	s.Require().NoError(err)
	s.True(res4.Allowed())
	s.Equal(int64(70), res4.CurrentValue)
	s.Equal(int64(0), res4.RemainingValue())
}

func (s *RateLimiterTestSuite) TestBucketsRefillOverTime() {
	var (
		rl = RateLimit{
			Limit:  100,
			Period: time.Second,
		}
		key = "rl:test:refill_over_time"
	)

	limiter := s.newLimiter()

	res, err := limiter.CheckLimit(s.ctx, key, rl, 50, false, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed())
	s.Equal(int64(100), res.CurrentValue)
	s.Equal(int64(50), res.RemainingValue())

	res2, err := limiter.CheckLimit(s.ctx, key, rl, 50, true, "", false)
	s.Require().NoError(err)
	s.True(res2.Allowed())
	s.Equal(int64(50), res2.CurrentValue)
	s.Equal(int64(0), res2.RemainingValue())

	// Simulate 200 ms of elapsed time so the bucket refills 20 tokens
	// (200ms / 1000ms * 100 = 20).
	s.advance(200 * time.Millisecond)

	res3, err := limiter.CheckLimit(s.ctx, key, rl, 70, true, "", false)
	s.Require().NoError(err)
	s.True(res3.Allowed())
	s.Equal(int64(70), res3.CurrentValue)
}

func (s *RateLimiterTestSuite) TestFailOpenAllowsOnStoreError() {
	var (
		rl = RateLimit{
			Limit:  10,
			Period: time.Minute,
		}
		key = "rl:test:failopen"
	)
	s.store.failGet = true

	limiter := s.newLimiter(WithFailOpen(true))

	res, err := limiter.CheckLimit(s.ctx, key, rl, 100, false, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed())
	s.Greater(res.CurrentValue, rl.Limit)
}

func (s *RateLimiterTestSuite) TestFailClosedReturnsErrorOnStoreError() {
	var (
		rl = RateLimit{
			Limit:  10,
			Period: time.Minute,
		}
		key = "rl:test:failclosed"
	)
	s.store.failGet = true

	limiter := s.newLimiter()

	_, err := limiter.CheckLimit(s.ctx, key, rl, 100, false, "", false)
	s.Require().Error(err)
}

// TestTestOnlyReadsDoNotCallCAS is the guard rail for the CAS-with-retry
// refactor: a read-only probe (testOnly=true) must never touch the CAS path,
// because the read-only path has no reason to commit a new state and issuing
// a CAS would both burn a round trip and invent a synthetic write contention.
func (s *RateLimiterTestSuite) TestTestOnlyReadsDoNotCallCAS() {
	rl := RateLimit{
		Limit:  10,
		Period: time.Minute,
	}
	key := "rl:test:testonly-no-cas"

	limiter := s.newLimiter()

	_, err := limiter.CheckLimit(s.ctx, key, rl, 3, true, "", false)
	s.Require().NoError(err)

	s.Equal(1, s.store.getCalls, "testOnly should issue exactly one Get")
	s.Equal(0, s.store.casCalls, "testOnly must not issue any CAS call")
}

// TestCASRetriesOnContention drives the CAS-with-retry loop via the fake
// store's forcedMismatches knob. We simulate two lost races, then let the
// third attempt succeed. The limiter must:
//
//   - re-read the bucket between mismatches (getCalls increments every retry),
//   - issue exactly attempts CAS calls (casCalls == 3),
//   - back off via the injected sleep between failed attempts (sleeps == 2),
//   - return a correct final state that reflects the swap committed on the
//     final attempt, not on the first.
func (s *RateLimiterTestSuite) TestCASRetriesOnContention() {
	rl := RateLimit{
		Limit:  100,
		Period: time.Minute,
	}
	key := "rl:test:cas-contention"

	var sleeps atomic.Int32
	limiter := s.newLimiter(withSleep(func(time.Duration) {
		sleeps.Add(1)
	}))

	s.store.setForcedMismatches(2)

	res, err := limiter.CheckLimit(s.ctx, key, rl, 5, false, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed(), "swap eventually succeeds; request is allowed")
	s.Equal(int64(100), res.CurrentValue)
	s.Equal(int64(95), res.RemainingValue(), "the state committed on the final attempt must be the post-consume value")

	s.Equal(3, s.store.getCalls, "one Get per CAS attempt, including retries")
	s.Equal(3, s.store.casCalls, "one CAS attempt per iteration of the retry loop")
	s.Equal(int32(2), sleeps.Load(), "two lost races => two backoffs between attempts")

	final := s.store.mustContain(key)
	s.Equal(int64(95), final.Value, "store holds the successful swap only")
}

// TestCASExhaustionReturnsError locks the fake into unconditional mismatches
// until the attempt budget is burned. Fail-closed mode must surface an
// ErrCASExhausted; no silent allow, no data corruption. The store must still
// be in its pre-call state because every attempt was declined.
func (s *RateLimiterTestSuite) TestCASExhaustionReturnsError() {
	rl := RateLimit{
		Limit:  10,
		Period: time.Minute,
	}
	key := "rl:test:cas-exhausted"

	limiter := s.newLimiter(
		withSleep(func(time.Duration) {}),
		withCASRetry(3, time.Microsecond, time.Microsecond),
	)

	s.store.setForcedMismatches(1_000)

	_, err := limiter.CheckLimit(s.ctx, key, rl, 1, false, "", false)
	s.Require().Error(err)
	s.True(errors.Is(err, ErrCASExhausted), "error must wrap ErrCASExhausted: %v", err)

	s.Equal(3, s.store.casCalls, "attempt budget consumed exactly once")
}

// TestCASExhaustionFailsOpen ensures the fail-open knob converts the exhaustion
// error into an allowed response, matching the behavior of other transient
// store errors. A rate limiter that hard-rejects traffic during transient CAS
// contention would be an unacceptable regression.
func (s *RateLimiterTestSuite) TestCASExhaustionFailsOpen() {
	rl := RateLimit{
		Limit:  10,
		Period: time.Minute,
	}
	key := "rl:test:cas-exhausted-failopen"

	limiter := s.newLimiter(
		withSleep(func(time.Duration) {}),
		withCASRetry(2, time.Microsecond, time.Microsecond),
		WithFailOpen(true),
	)

	s.store.setForcedMismatches(1_000)

	res, err := limiter.CheckLimit(s.ctx, key, rl, 1, false, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed(), "fail-open absorbs CAS exhaustion like any other store error")
	s.Greater(res.CurrentValue, rl.Limit, "fail-open signals the synthetic unlimited budget")
}

// TestCASInitialInsertUsesNilExpected asserts the insert-if-absent branch is
// exercised on the first-ever write to a key: the limiter passes no expected
// bytes (expected == nil), and the store successfully installs the bucket.
// Any subsequent probe must observe the newly-installed state.
func (s *RateLimiterTestSuite) TestCASInitialInsertUsesNilExpected() {
	rl := RateLimit{
		Limit:  4,
		Period: time.Minute,
	}
	key := "rl:test:cas-initial-insert"

	limiter := s.newLimiter()

	res, err := limiter.CheckLimit(s.ctx, key, rl, 3, false, "", false)
	s.Require().NoError(err)
	s.True(res.Allowed())
	s.Equal(int64(4), res.CurrentValue, "first write sees the bucket as full")

	probe, err := limiter.CheckLimit(s.ctx, key, rl, 1, true, "", false)
	s.Require().NoError(err)
	s.Equal(int64(1), probe.CurrentValue, "insert must have persisted the consume")
}

func TestRateLimiterTestSuite(t *testing.T) {
	suite.Run(t, new(RateLimiterTestSuite))
}
