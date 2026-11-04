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
	"math"
	"time"
)

var _ RateLimiter = (*nopLimiter)(nil)

// Nop returns a [RateLimiter] that does not perform any rate limiting.
func Nop() RateLimiter {
	return nopLimiter{}
}

type nopLimiter struct{}

func (nopLimiter) CheckLimit(
	_ context.Context,
	_ string, // key
	_ RateLimit,
	r int64, // tokens requested
	_ bool, // testonly
	_ string, // requestID
	_ bool, // mustConsume
) (*RateLimitResult, error) {
	return &RateLimitResult{
		CurrentValue: math.MaxInt64,
		Requested:    r,
		RateLimit: RateLimit{
			Limit:  math.MaxInt64,
			Period: time.Nanosecond,
		},
	}, nil
}

func (nopLimiter) Reset(context.Context, ...string /* keys */) error {
	return nil
}
