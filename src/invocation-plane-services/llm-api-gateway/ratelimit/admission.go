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
	"sync"
)

type AdmissionPlan struct {
	limiter              RateLimiter
	limit                ResourceLimit
	requestID            string
	lock                 sync.Mutex
	consumedInputTokens  int64
	reservedOutputTokens int64
}

func NewAdmissionPlan(
	limiter RateLimiter,
	limit ResourceLimit,
	requestID string,
) *AdmissionPlan {
	if limiter == nil || limit.Empty() {
		return nil
	}

	return &AdmissionPlan{
		limiter:   limiter,
		limit:     limit,
		requestID: requestID,
	}
}

func (p *AdmissionPlan) CheckRequests(
	ctx context.Context,
	requests int64,
) (map[LimitDimension]*RateLimitResult, error) {
	if p == nil || requests == 0 {
		return nil, nil
	}

	return TestResourceLimit(
		ctx,
		p.limiter,
		p.limit,
		ResourceRequest{Requests: requests},
		p.requestID,
	)
}

func (p *AdmissionPlan) CheckTokens(
	ctx context.Context,
	request ResourceRequest,
) (map[LimitDimension]*RateLimitResult, error) {
	if p == nil {
		return nil, nil
	}

	request.Requests = 0
	return TestResourceLimit(
		ctx,
		p.limiter,
		p.limit,
		request,
		p.requestID,
	)
}

func (p *AdmissionPlan) Commit(
	ctx context.Context,
	request ResourceRequest,
) (map[LimitDimension]*RateLimitResult, error) {
	if p == nil {
		return nil, nil
	}

	if request.Requests == 0 && request.InputTokens == 0 && request.OutputTokens == 0 {
		return nil, nil
	}

	results, err := MustConsumeResourceLimit(
		ctx,
		p.limiter,
		p.limit,
		request,
		p.requestID,
	)
	if err != nil {
		return nil, err
	}

	p.lock.Lock()
	p.consumedInputTokens += request.InputTokens
	p.reservedOutputTokens += request.OutputTokens
	p.lock.Unlock()

	return results, nil
}

func (p *AdmissionPlan) FinalizeTokens(
	ctx context.Context,
	request ResourceRequest,
) (map[LimitDimension]*RateLimitResult, error) {
	if p == nil {
		return nil, nil
	}

	request.Requests = 0
	p.lock.Lock()
	request.InputTokens -= p.consumedInputTokens
	request.OutputTokens -= p.reservedOutputTokens
	p.consumedInputTokens = 0
	p.reservedOutputTokens = 0
	p.lock.Unlock()

	if request.InputTokens == 0 && request.OutputTokens == 0 {
		return nil, nil
	}

	return MustConsumeResourceLimit(
		ctx,
		p.limiter,
		p.limit,
		request,
		p.requestID,
	)
}

func (p *AdmissionPlan) ReleaseOutputReservation(
	ctx context.Context,
) (map[LimitDimension]*RateLimitResult, error) {
	if p == nil {
		return nil, nil
	}

	p.lock.Lock()
	outputTokens := -p.reservedOutputTokens
	p.consumedInputTokens = 0
	p.reservedOutputTokens = 0
	p.lock.Unlock()

	if outputTokens == 0 {
		return nil, nil
	}

	return MustConsumeResourceLimit(
		ctx,
		p.limiter,
		p.limit,
		ResourceRequest{OutputTokens: outputTokens},
		p.requestID,
	)
}
