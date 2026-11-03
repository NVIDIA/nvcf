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

func TestDoResourceLimitEnforcesInputOutputTokenDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dimension LimitDimension
		limit     ResourceLimit
		request   ResourceRequest
	}{
		{
			name:      "input tokens per second",
			dimension: InputTokensPerSecond,
			limit:     ResourceLimit{SubjectKey: "t1", InputTokensPerSecond: 10},
			request:   ResourceRequest{InputTokens: 20},
		},
		{
			name:      "input tokens per hour",
			dimension: InputTokensPerHour,
			limit:     ResourceLimit{SubjectKey: "t2", InputTokensPerHour: 10},
			request:   ResourceRequest{InputTokens: 20},
		},
		{
			name:      "input tokens per day",
			dimension: InputTokensPerDay,
			limit:     ResourceLimit{SubjectKey: "t3", InputTokensPerDay: 10},
			request:   ResourceRequest{InputTokens: 20},
		},
		{
			name:      "input tokens per week",
			dimension: InputTokensPerWeek,
			limit:     ResourceLimit{SubjectKey: "t4", InputTokensPerWeek: 10},
			request:   ResourceRequest{InputTokens: 20},
		},
		{
			name:      "output tokens per second",
			dimension: OutputTokensPerSecond,
			limit:     ResourceLimit{SubjectKey: "t5", OutputTokensPerSecond: 10},
			request:   ResourceRequest{OutputTokens: 20},
		},
		{
			name:      "output tokens per hour",
			dimension: OutputTokensPerHour,
			limit:     ResourceLimit{SubjectKey: "t6", OutputTokensPerHour: 10},
			request:   ResourceRequest{OutputTokens: 20},
		},
		{
			name:      "output tokens per day",
			dimension: OutputTokensPerDay,
			limit:     ResourceLimit{SubjectKey: "t7", OutputTokensPerDay: 10},
			request:   ResourceRequest{OutputTokens: 20},
		},
		{
			name:      "output tokens per week",
			dimension: OutputTokensPerWeek,
			limit:     ResourceLimit{SubjectKey: "t8", OutputTokensPerWeek: 10},
			request:   ResourceRequest{OutputTokens: 20},
		},
		{
			name:      "input tokens per month",
			dimension: InputTokensPerMonth,
			limit:     ResourceLimit{SubjectKey: "t9", InputTokensPerMonth: 10},
			request:   ResourceRequest{InputTokens: 20},
		},
		{
			name:      "output tokens per month",
			dimension: OutputTokensPerMonth,
			limit:     ResourceLimit{SubjectKey: "t10", OutputTokensPerMonth: 10},
			request:   ResourceRequest{OutputTokens: 20},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStore()
			limiter, err := NewRateLimiter(store)
			if err != nil {
				t.Fatalf("new rate limiter: %v", err)
			}

			results, err := ConsumeResourceLimit(context.Background(), limiter, tc.limit, tc.request, "req-1")
			if err != nil {
				t.Fatalf("consume resource limit: %v", err)
			}

			result := results[tc.dimension]
			if result == nil {
				t.Fatalf("missing result for dimension %s: %#v", tc.dimension, results)
			}
			if result.Allowed() {
				t.Fatalf("dimension %s: request over limit was allowed", tc.dimension)
			}
		})
	}
}

func TestDoResourceLimitInputOutputDimensionsAreIndependent(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	limiter, err := NewRateLimiter(store)
	if err != nil {
		t.Fatalf("new rate limiter: %v", err)
	}

	limit := ResourceLimit{
		SubjectKey:           "independent",
		InputTokensPerMinute: 5,
	}
	request := ResourceRequest{InputTokens: 3, OutputTokens: 1000}

	results, err := ConsumeResourceLimit(context.Background(), limiter, limit, request, "req-1")
	if err != nil {
		t.Fatalf("consume resource limit: %v", err)
	}

	result := results[InputTokensPerMinute]
	if result == nil {
		t.Fatal("missing result for InputTokensPerMinute")
	}
	if !result.Allowed() {
		t.Fatal("input tokens within limit were disallowed")
	}
	if _, ok := results[OutputTokensPerMinute]; ok {
		t.Fatal("output dimension should not be checked when no output limit is configured")
	}
}

func TestResourceLimitFromOrgLimitCarriesInputOutputFields(t *testing.T) {
	t.Parallel()

	orgLimit := OrgLimit{
		TokensPerMonth:        9,
		InputTokensPerSecond:  1,
		InputTokensPerHour:    2,
		InputTokensPerDay:     3,
		InputTokensPerWeek:    4,
		InputTokensPerMonth:   10,
		OutputTokensPerSecond: 5,
		OutputTokensPerHour:   6,
		OutputTokensPerDay:    7,
		OutputTokensPerWeek:   8,
		OutputTokensPerMonth:  11,
	}

	got := ResourceLimitFromOrgLimit(orgLimit)

	want := ResourceLimit{
		SubjectKey:            got.SubjectKey,
		SubjectRepr:           got.SubjectRepr,
		Level:                 LevelOrg,
		TokensPerMonth:        9,
		InputTokensPerSecond:  1,
		InputTokensPerHour:    2,
		InputTokensPerDay:     3,
		InputTokensPerWeek:    4,
		InputTokensPerMonth:   10,
		OutputTokensPerSecond: 5,
		OutputTokensPerHour:   6,
		OutputTokensPerDay:    7,
		OutputTokensPerWeek:   8,
		OutputTokensPerMonth:  11,
	}
	if got != want {
		t.Fatalf("resource limit = %#v, want %#v", got, want)
	}
}

// TestDoResourceLimitMonthlyLimitResetsAfterPeriodElapses answers "how do we test a
// month-long window without waiting a month": the limiter's clock is injectable, so
// simulating expiry is a single fake-clock advance, exactly like second/week already
// are in ratelimit_test.go's TestBucketsRefillOverTime.
func TestDoResourceLimitMonthlyLimitResetsAfterPeriodElapses(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	now := int64(1_700_000_000_000)
	limiter, err := NewRateLimiter(store, withClock(func() int64 { return now }))
	if err != nil {
		t.Fatalf("new rate limiter: %v", err)
	}

	limit := ResourceLimit{SubjectKey: "monthly", InputTokensPerMonth: 100}
	request := ResourceRequest{InputTokens: 100}

	results, err := ConsumeResourceLimit(context.Background(), limiter, limit, request, "req-1")
	if err != nil {
		t.Fatalf("consume resource limit: %v", err)
	}
	if !results[InputTokensPerMonth].Allowed() {
		t.Fatal("first request within limit was disallowed")
	}

	results, err = ConsumeResourceLimit(context.Background(), limiter, limit, request, "req-2")
	if err != nil {
		t.Fatalf("consume resource limit: %v", err)
	}
	if results[InputTokensPerMonth].Allowed() {
		t.Fatal("second request over the exhausted monthly bucket was allowed")
	}

	// "Wait" 31 days, instantly, via the fake clock.
	now += int64((31 * 24 * time.Hour) / time.Millisecond)

	results, err = ConsumeResourceLimit(context.Background(), limiter, limit, request, "req-3")
	if err != nil {
		t.Fatalf("consume resource limit: %v", err)
	}
	if !results[InputTokensPerMonth].Allowed() {
		t.Fatal("request after the monthly period elapsed was still disallowed")
	}
}
