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
	"testing"
	"time"
)

type countingRateLimiter struct {
	calls int
}

func (l *countingRateLimiter) CheckLimit(
	_ context.Context,
	_ string,
	_ RateLimit,
	_ int64,
	_ bool,
	_ string,
	_ bool,
) (*RateLimitResult, error) {
	l.calls++
	return &RateLimitResult{}, nil
}

func (l *countingRateLimiter) Reset(context.Context, ...string) error {
	return nil
}

func TestApplySynchronizedEventDoesNotDeduplicateRequestID(t *testing.T) {
	limiter := &countingRateLimiter{}
	event := &RateLimitEventWireFormat{
		Key:         "org:123",
		Units:       1,
		Rate:        10,
		Period:      time.Minute,
		RequestID:   "req-123",
		ClusterName: "cluster-a",
		CreatedAt:   time.Now().Unix(),
	}

	if err := ApplySynchronizedEvent(
		context.Background(),
		limiter,
		"cluster-b",
		true,
		event,
	); err != nil {
		t.Fatalf("first ApplySynchronizedEvent() error = %v", err)
	}

	if err := ApplySynchronizedEvent(
		context.Background(),
		limiter,
		"cluster-b",
		true,
		event,
	); err != nil {
		t.Fatalf("second ApplySynchronizedEvent() error = %v", err)
	}

	if limiter.calls != 2 {
		t.Fatalf("CheckLimit() calls = %d, want 2", limiter.calls)
	}
}

func TestApplySynchronizedEventCarriesDebt(t *testing.T) {
	store := newFakeStore()
	limiter, err := NewRateLimiter(store)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}
	event := &RateLimitEventWireFormat{
		Key:         "nvcf:nca-1:output_tokens_minute",
		Units:       150,
		Rate:        100,
		Period:      time.Minute,
		RequestID:   "req-debt",
		ClusterName: "cluster-a",
		CreatedAt:   time.Now().Unix(),
		MustConsume: true,
		AllowDebt:   true,
	}

	if err := ApplySynchronizedEvent(context.Background(), limiter, "cluster-b", true, event); err != nil {
		t.Fatalf("ApplySynchronizedEvent() error = %v", err)
	}

	res, err := limiter.CheckLimit(
		context.Background(), event.Key,
		RateLimit{Limit: 100, Period: time.Minute, AllowDebt: true}, 1, true, "", false)
	if err != nil {
		t.Fatalf("CheckLimit() error = %v", err)
	}
	if res.CurrentValue >= 0 {
		t.Fatalf("current value = %d, want negative (debt replicated)", res.CurrentValue)
	}
}
