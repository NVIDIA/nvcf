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
)

func TestShouldSynchronizeRateLimitEvent(t *testing.T) {
	t.Parallel()

	rate := RateLimit{Limit: 10, Period: time.Minute}

	testCases := []struct {
		name        string
		testOnly    bool
		mustConsume bool
		result      *RateLimitResult
		want        bool
	}{
		{
			name:        "test_only_never_syncs",
			testOnly:    true,
			mustConsume: false,
			result: &RateLimitResult{
				CurrentValue: 10,
				Requested:    1,
				RateLimit:    rate,
			},
			want: false,
		},
		{
			name:        "allowed_request_syncs",
			testOnly:    false,
			mustConsume: false,
			result: &RateLimitResult{
				CurrentValue: 10,
				Requested:    1,
				RateLimit:    rate,
			},
			want: true,
		},
		{
			name:        "rejected_request_without_must_consume_does_not_sync",
			testOnly:    false,
			mustConsume: false,
			result: &RateLimitResult{
				CurrentValue: 0,
				Requested:    1,
				RateLimit:    rate,
			},
			want: false,
		},
		{
			name:        "must_consume_syncs_even_when_rejected",
			testOnly:    false,
			mustConsume: true,
			result: &RateLimitResult{
				CurrentValue: 0,
				Requested:    1,
				RateLimit:    rate,
			},
			want: true,
		},
		{
			name:        "nil_result_does_not_sync",
			testOnly:    false,
			mustConsume: true,
			result:      nil,
			want:        false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := shouldSynchronizeRateLimitEvent(tc.testOnly, tc.mustConsume, tc.result)
			if got != tc.want {
				t.Fatalf("shouldSynchronizeRateLimitEvent() = %t, want %t", got, tc.want)
			}
		})
	}
}
