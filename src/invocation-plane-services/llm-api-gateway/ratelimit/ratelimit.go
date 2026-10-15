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
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/rs/zerolog"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// limiterMode is the state-transition mode used inside leakyBucket. It is an
// internal enum (not exported) because callers only ever control it
// indirectly via the testOnly and mustConsume flags on CheckLimit.
type limiterMode uint8

const (
	// limiterModeTestOnly observes the bucket without committing a new state.
	limiterModeTestOnly limiterMode = iota
	// limiterModeConsumeIfAllowed commits a new state only when the request
	// would have been allowed on the observed bucket.
	limiterModeConsumeIfAllowed
	// limiterModeMustConsume commits a new state unconditionally, even when
	// the request would not have been allowed on the observed bucket.
	// Primarily used to accrue synthetic debt (e.g. negative-token refunds).
	limiterModeMustConsume
)

// CAS retry tuning. A single-bucket transition in Olric runs on the partition
// owner under a fine-grained atomic-key lock, so contention only shows up as a
// handful of failed swaps under high concurrency. These values are intentionally
// low: many retries in a hot path would signal misuse, not a tuning gap.
const (
	defaultCASMaxAttempts    = 16
	defaultCASInitialBackoff = 50 * time.Microsecond
	defaultCASMaxBackoff     = 5 * time.Millisecond
)

// ErrCASExhausted is returned when the CAS-with-retry loop fails to commit a
// bucket transition within the configured attempt budget. Callers that run the
// limiter in fail-open mode will convert this into an allowed response; the
// fail-closed path surfaces the error to the caller.
var ErrCASExhausted = errors.New("rate limit bucket CAS retries exhausted")

type RateLimit struct {
	Limit  int64
	Period time.Duration
}

func (l RateLimit) String() string {
	return fmt.Sprintf("%d/%s", l.Limit, l.Period.String())
}

//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -package ratelimitmock -destination ratelimitmock/rate_limiter.go . RateLimiter
type RateLimiter interface {
	CheckLimit(
		ctx context.Context,
		key string,
		l RateLimit,
		tokensRequested int64,
		testOnly bool,
		requestID string,
		mustConsume bool,
	) (*RateLimitResult, error)
	Reset(ctx context.Context, key ...string) error
}

type rateLimiter struct {
	store          OlricStore
	failOpen       bool
	synchronizer   Synchronizer
	casMaxAttempts int
	casInitialWait time.Duration
	casMaxWait     time.Duration
	nowMs          func() int64
	// sleep is injected in tests so the CAS retry loop can be driven without
	// sitting on real wall-clock jitter.
	sleep func(time.Duration)
}

func (rl *rateLimiter) CheckLimit(
	ctx context.Context,
	key string,
	l RateLimit,
	tokensRequested int64,
	testOnly bool,
	requestID string,
	mustConsume bool,
) (*RateLimitResult, error) {
	return rl.checkLimit(ctx, key, l, tokensRequested, testOnly, requestID, mustConsume, true)
}

func (rl *rateLimiter) checkLimit(
	ctx context.Context,
	key string,
	l RateLimit,
	tokensRequested int64,
	testOnly bool,
	requestID string,
	mustConsume bool,
	synchronize bool,
) (*RateLimitResult, error) {
	log := telemetry.Logger(ctx)

	log.Debug().
		Dur("period", l.Period).
		Str("key", key).
		Interface("rate", l).
		Bool("testOnly", testOnly).
		Int64("tokensRequested", tokensRequested).
		Str("requestID", requestID).
		Msg("checking rate limit")

	currentValue, err := rl.leakyBucket(ctx, key, l.Limit, l.Period, tokensRequested, testOnly, mustConsume)
	if err != nil {
		if rl.failOpen {
			log := log.Sample(zerolog.Often)
			log.Error().Err(err).Msg("olric returned an error, still allowing request")

			return &RateLimitResult{
				CurrentValue: math.MaxInt64,
				Requested:    tokensRequested,
				RateLimit:    l,
			}, nil
		}

		return nil, err
	}

	log.Debug().Interface("currentValue", currentValue).Msg("rate limit result")

	rlr := &RateLimitResult{
		CurrentValue: currentValue,
		Requested:    tokensRequested,
		RateLimit:    l,
	}

	if synchronize && shouldSynchronizeRateLimitEvent(testOnly, mustConsume, rlr) {
		// When consuming limit, send the event to pubsub for synchronization.
		rle := &RateLimitEvent{
			Key:         key,
			Result:      rlr,
			RequestID:   requestID,
			MustConsume: mustConsume,
		}
		if err := rl.synchronizer.Send(ctx, rle); err != nil {
			log.Error().Interface("rate limit event", rle).Msg("failed to send rate limit event")
		}
	}

	return rlr, nil
}

func shouldSynchronizeRateLimitEvent(
	testOnly bool,
	mustConsume bool,
	result *RateLimitResult,
) bool {
	if testOnly || result == nil {
		return false
	}

	return mustConsume || result.Allowed()
}

func (rl *rateLimiter) Reset(ctx context.Context, key ...string) error {
	// TODO: synchronize these events across regions.
	return rl.store.Delete(ctx, key...)
}

// leakyBucket runs one continuous-refill bucket transition for key: read the
// bucket, apply refill based on elapsed time, optionally deduct tokensRequested,
// then publish the new state via CompareAndSwap so concurrent callers serialize
// on the partition owner. Returns the post-refill bucket value the caller
// observed.
func (rl *rateLimiter) leakyBucket(
	ctx context.Context,
	key string,
	rate int64,
	period time.Duration,
	tokensRequested int64,
	testOnly bool,
	mustConsume bool,
) (int64, error) {
	var mode limiterMode
	switch {
	case testOnly:
		mode = limiterModeTestOnly
	case mustConsume:
		mode = limiterModeMustConsume
	default:
		mode = limiterModeConsumeIfAllowed
	}

	periodMs := period.Milliseconds()

	return rl.casRetry(ctx, func() (int64, bool, error) {
		state, exists, err := rl.store.GetBucket(ctx, key)
		if err != nil {
			return 0, false, fmt.Errorf("get rate limit bucket: %w", err)
		}

		nowMs := rl.nowMs()

		value := state.Value
		lastRefill := state.LastRefill
		if !exists {
			value = rate
			lastRefill = nowMs
		}

		elapsed := nowMs - lastRefill
		if elapsed < 0 {
			elapsed = 0
		}
		var refill int64
		if periodMs > 0 {
			refill = elapsed * rate / periodMs
		}
		currentValue := value + refill
		if currentValue > rate {
			currentValue = rate
		}
		allowed := currentValue >= tokensRequested

		shouldWrite := mode == limiterModeMustConsume ||
			(mode == limiterModeConsumeIfAllowed && allowed)

		if !shouldWrite {
			return currentValue, true, nil
		}

		newValue := currentValue - tokensRequested
		if newValue < 0 {
			newValue = 0
		}
		if newValue > rate {
			newValue = rate
		}

		var expected *bucketState
		if exists {
			expected = &state
		}

		swapped, err := rl.store.CompareAndSetBucket(
			ctx,
			key,
			expected,
			bucketState{Value: newValue, LastRefill: nowMs},
			period,
		)
		if err != nil {
			return 0, false, fmt.Errorf("cas rate limit bucket: %w", err)
		}
		return currentValue, swapped, nil
	})
}

// casRetry drives an optimistic-concurrency closure. attempt returns
// (result, done, err): done=true commits result as the final answer, done=false
// signals CAS contention so the loop re-runs attempt after a short jittered
// backoff. Exhausting casMaxAttempts returns ErrCASExhausted. attempt must
// re-read any state it needs on every call, since prior observations become
// stale under contention.
func (rl *rateLimiter) casRetry(
	ctx context.Context,
	attempt func() (int64, bool, error),
) (int64, error) {
	backoff := rl.casInitialWait
	for i := 0; i < rl.casMaxAttempts; i++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		result, done, err := attempt()
		if err != nil {
			return 0, err
		}
		if done {
			return result, nil
		}
		if rl.sleep != nil && backoff > 0 {
			rl.sleep(jitter(backoff))
		}
		if backoff > 0 && backoff < rl.casMaxWait {
			backoff *= 2
			if backoff > rl.casMaxWait {
				backoff = rl.casMaxWait
			}
		}
	}
	return 0, fmt.Errorf("%w: after %d attempts", ErrCASExhausted, rl.casMaxAttempts)
}

// jitter returns a duration in [d/2, d], avoiding lockstep retries between
// contending nodes. We purposely skew the range high so the average wait stays
// close to the configured backoff and observed contention times stay bounded.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := int64(d / 2)
	if half <= 0 {
		return d
	}
	return time.Duration(half) + time.Duration(rand.Int64N(half+1))
}

type RateLimiterOption interface {
	apply(*rateLimiter)
}

type rateLimiterFunc func(*rateLimiter)

func (f rateLimiterFunc) apply(dst *rateLimiter) {
	f(dst)
}

func WithFailOpen(failOpen bool) RateLimiterOption {
	return rateLimiterFunc(func(r *rateLimiter) {
		r.failOpen = failOpen
	})
}

func WithSynchronizer(s Synchronizer) RateLimiterOption {
	return rateLimiterFunc(func(r *rateLimiter) {
		r.synchronizer = s
	})
}

// withCASRetry overrides the attempt budget and backoff bounds used by the
// CAS-with-retry loop. A non-positive value for any parameter leaves the
// corresponding default in place. The defaults comfortably absorb typical
// cross-region contention on a single bucket; callers currently use this knob
// only from tests, so it stays unexported. If you find yourself wanting to
// tune these in production, promote this to an exported option and document
// the rationale at the same time.
func withCASRetry(maxAttempts int, initialWait, maxWait time.Duration) RateLimiterOption {
	return rateLimiterFunc(func(r *rateLimiter) {
		if maxAttempts > 0 {
			r.casMaxAttempts = maxAttempts
		}
		if initialWait > 0 {
			r.casInitialWait = initialWait
		}
		if maxWait > 0 {
			r.casMaxWait = maxWait
		}
	})
}

// withClock is used by tests to control elapsed-time math deterministically.
func withClock(nowMs func() int64) RateLimiterOption {
	return rateLimiterFunc(func(r *rateLimiter) {
		r.nowMs = nowMs
	})
}

// withSleep is used by tests to observe or neutralize the CAS retry backoff.
func withSleep(sleep func(time.Duration)) RateLimiterOption {
	return rateLimiterFunc(func(r *rateLimiter) {
		r.sleep = sleep
	})
}

func NewRateLimiter(store OlricStore, opts ...RateLimiterOption) (RateLimiter, error) {
	if store == nil {
		return nil, fmt.Errorf("olric store is required")
	}

	rl := &rateLimiter{
		store:          store,
		failOpen:       false,
		synchronizer:   &nopSynchronizer{},
		casMaxAttempts: defaultCASMaxAttempts,
		casInitialWait: defaultCASInitialBackoff,
		casMaxWait:     defaultCASMaxBackoff,
		nowMs:          func() int64 { return time.Now().UnixMilli() },
		sleep:          time.Sleep,
	}

	for _, opt := range opts {
		opt.apply(rl)
	}

	return rl, nil
}
