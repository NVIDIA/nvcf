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
)

var (
	AllowAll  RateLimiter = &allowAll{}
	RejectAll RateLimiter = &rejectAll{}
)

type allowAll struct{}

func (a *allowAll) CheckLimit(
	_ context.Context,
	_ string,
	l RateLimit,
	tokensRequested int64,
	_ bool,
	_ string,
	_ bool,
) (*RateLimitResult, error) {
	return &RateLimitResult{
		CurrentValue: math.MaxInt64,
		Requested:    tokensRequested,
		RateLimit:    l,
	}, nil
}

func (a *allowAll) Reset(_ context.Context, _ ...string) error {
	return nil
}

func (a *allowAll) ResetPattern(_ context.Context, _ string) (uint64, error) {
	return 0, nil
}

type rejectAll struct{}

func (r *rejectAll) CheckLimit(
	_ context.Context,
	_ string,
	l RateLimit,
	tokensRequested int64,
	_ bool,
	_ string,
	_ bool,
) (*RateLimitResult, error) {
	return &RateLimitResult{
		CurrentValue: 0,
		Requested:    tokensRequested,
		RateLimit:    l,
	}, nil
}

func (r *rejectAll) Reset(_ context.Context, _ ...string) error {
	return nil
}

func (r *rejectAll) ResetPattern(_ context.Context, _ string) (uint64, error) {
	return 0, nil
}
