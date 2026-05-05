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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateLimitResult_Allowed(t *testing.T) {
	r := &RateLimitResult{
		CurrentValue: 10,
		Requested:    10,
		RateLimit: RateLimit{
			Limit:  60,
			Period: time.Minute,
		},
	}
	require.True(t, r.Allowed())

	r = &RateLimitResult{
		CurrentValue: 9,
		Requested:    10,
		RateLimit: RateLimit{
			Limit:  60,
			Period: time.Minute,
		},
	}
	require.False(t, r.Allowed())
}

func TestRateLimitResult_RemainingAndRevert(t *testing.T) {
	r := &RateLimitResult{
		CurrentValue: 10,
		Requested:    3,
		RateLimit: RateLimit{
			Limit:  60,
			Period: time.Minute,
		},
	}
	require.Equal(t, int64(7), r.RemainingValue(), "remaining without revert")

	r.Revert()
	require.Equal(t, int64(10), r.RemainingValue(), "remaining after revert returns current")
}

func TestRateLimitResult_RetryAfter(t *testing.T) {
	// limit: 60 tokens per 60s => 1 token/sec
	// shortage: 5 tokens => expect 5s
	r := &RateLimitResult{
		CurrentValue: 5,
		Requested:    10,
		RateLimit: RateLimit{
			Limit:  60,
			Period: time.Minute,
		},
	}
	require.Equal(t, 5*time.Second, r.RetryAfter())

	// When allowed, retry after should be zero
	r = &RateLimitResult{
		CurrentValue: 10,
		Requested:    10,
		RateLimit: RateLimit{
			Limit:  60,
			Period: time.Minute,
		},
	}
	require.Equal(t, time.Duration(0), r.RetryAfter())
}

func TestRateLimitResult_ResetAfter(t *testing.T) {
	// limit: 100 tokens per 100s => 1 token/sec
	// remaining after request: current - requested = 40 => need 60 tokens to refill => 60s
	r := &RateLimitResult{
		CurrentValue: 50,
		Requested:    10,
		RateLimit: RateLimit{
			Limit:  100,
			Period: 100 * time.Second,
		},
	}
	require.Equal(t, 60*time.Second, r.ResetAfter())

	// After revert, remaining == current => need 50 tokens to refill => 50s
	r.Revert()
	require.Equal(t, 50*time.Second, r.ResetAfter())
}
