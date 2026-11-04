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
	"fmt"
	"time"
)

type RateLimitResult struct {
	CurrentValue int64 // the value "before" the request
	Requested    int64
	RateLimit    RateLimit
	reverted     bool
}

func (rlr *RateLimitResult) String() string {
	return fmt.Sprintf(
		"RateLimitResult{Current: %d, Requested: %d, RateLimit: %s}",
		rlr.CurrentValue,
		rlr.Requested,
		rlr.RateLimit.String(),
	)
}

func (rlr *RateLimitResult) Allowed() bool {
	return rlr.Requested <= rlr.CurrentValue
}

func (rlr *RateLimitResult) Revert() {
	rlr.reverted = true
}

func (rlr *RateLimitResult) RemainingValue() int64 {
	if rlr.reverted {
		return rlr.CurrentValue
	}
	return max(rlr.CurrentValue-rlr.Requested, 0)
}

func (rlr *RateLimitResult) LimitValue() int64 {
	return rlr.RateLimit.Limit
}

func (rlr *RateLimitResult) RetryAfter() time.Duration {
	var (
		n = float64(rlr.Requested - rlr.CurrentValue)
		l = float64(rlr.RateLimit.Limit)
		p = rlr.RateLimit.Period.Seconds()
		t = max(0, n/l*p)
	)

	return time.Duration(t * float64(time.Second))
}

func (rlr *RateLimitResult) ResetAfter() time.Duration {
	var (
		n = float64(rlr.RateLimit.Limit - rlr.RemainingValue())
		l = float64(rlr.RateLimit.Limit)
		p = rlr.RateLimit.Period.Seconds()
		t = max(0, n/l*p)
	)

	return time.Duration(t * float64(time.Second))
}
