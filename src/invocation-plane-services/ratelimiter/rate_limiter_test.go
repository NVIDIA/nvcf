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

package ratelimiter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulule/limiter/v3/drivers/store/memory"
)

func TestParseRates_SingleRate(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "4-S")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "4-S", entries[0].Rate)
	assert.NotNil(t, entries[0].Limiter)
}

func TestParseRates_MultipleRates(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "5-S,300-H,5000-D")
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "5-S", entries[0].Rate)
	assert.Equal(t, "300-H", entries[1].Rate)
	assert.Equal(t, "5000-D", entries[2].Rate)
	for _, e := range entries {
		assert.NotNil(t, e.Limiter)
	}
}

func TestParseRates_WithWhitespace(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "5-S , 300-H , 5000-D")
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "5-S", entries[0].Rate)
	assert.Equal(t, "300-H", entries[1].Rate)
	assert.Equal(t, "5000-D", entries[2].Rate)
}

func TestParseRates_InvalidRate(t *testing.T) {
	store := memory.NewStore()

	_, err := parseRates(store, "invalid")
	assert.Error(t, err)
}

func TestParseRates_PartiallyInvalid(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "5-S,invalid,300-H")
	require.NoError(t, err, "valid rates should be kept, malformed entries skipped")
	require.Len(t, entries, 2)
	assert.Equal(t, "5-S", entries[0].Rate)
	assert.Equal(t, "300-H", entries[1].Rate)
}

func TestParseRates_AllInvalid(t *testing.T) {
	store := memory.NewStore()

	_, err := parseRates(store, "foo,bar")
	assert.Error(t, err, "all-invalid input should produce an error")
}

func TestParseRates_ExactDuplicates(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "5-S,10-M,5-S")
	require.NoError(t, err)
	require.Len(t, entries, 2, "duplicate 5-S should be removed")
	assert.Equal(t, "5-S", entries[0].Rate)
	assert.Equal(t, "10-M", entries[1].Rate)
}

func TestParseRates_AllDuplicates(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "4-S,4-S,4-S")
	require.NoError(t, err)
	require.Len(t, entries, 1, "all duplicates should collapse to one")
	assert.Equal(t, "4-S", entries[0].Rate)
}

func TestParseRates_DuplicatePeriodKeepsStricter(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "4-S,5-S,8-D")
	require.NoError(t, err)
	require.Len(t, entries, 2, "two per-second rates should collapse to the stricter one")
	assert.Equal(t, "4-S", entries[0].Rate)
	assert.Equal(t, "8-D", entries[1].Rate)
}

func TestParseRates_DuplicatePeriodKeepsStricterReversed(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "5-S,4-S")
	require.NoError(t, err)
	require.Len(t, entries, 1, "stricter rate should be kept even when listed second")
	assert.Equal(t, "4-S", entries[0].Rate)
}

func TestParseRates_DuplicatePeriodAndExactDuplicate(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "4-S,4-S,5-S")
	require.NoError(t, err)
	require.Len(t, entries, 1, "exact dedup then period dedup should leave one entry")
	assert.Equal(t, "4-S", entries[0].Rate)
}

func TestParseRates_EmptyString(t *testing.T) {
	store := memory.NewStore()

	_, err := parseRates(store, "")
	assert.Error(t, err, "empty string should produce an error")
}

func TestParseRates_SingleComma(t *testing.T) {
	store := memory.NewStore()

	_, err := parseRates(store, ",")
	assert.Error(t, err, "bare comma should produce an error")
}

func TestParseRates_TrailingComma(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "5-S,")
	require.NoError(t, err, "trailing comma should be tolerated")
	require.Len(t, entries, 1)
	assert.Equal(t, "5-S", entries[0].Rate)
}

func TestParseRates_LeadingComma(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, ",5-S")
	require.NoError(t, err, "leading comma should be tolerated")
	require.Len(t, entries, 1)
	assert.Equal(t, "5-S", entries[0].Rate)
}

func TestParseRates_LeadingAndTrailingCommas(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, ",5-S,10-M,")
	require.NoError(t, err, "leading and trailing commas should be tolerated")
	require.Len(t, entries, 2)
	assert.Equal(t, "5-S", entries[0].Rate)
	assert.Equal(t, "10-M", entries[1].Rate)
}

func TestParseRates_WhitespaceOnlyParts(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "5-S, , ,10-M")
	require.NoError(t, err, "whitespace-only parts should be skipped")
	require.Len(t, entries, 2)
	assert.Equal(t, "5-S", entries[0].Rate)
	assert.Equal(t, "10-M", entries[1].Rate)
}

func TestParseRates_OnlyWhitespace(t *testing.T) {
	store := memory.NewStore()

	_, err := parseRates(store, "  ")
	assert.Error(t, err, "whitespace-only string should produce an error")
}

func TestLimiterEntry_MultipleRates_IndependentCounters(t *testing.T) {
	store := memory.NewStore()

	// 3 per second AND 5 per minute
	entries, err := parseRates(store, "3-S,5-M")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	ncaId := "test_nca"
	fvId := "test_fv"

	// Make 3 requests - all should pass both limits
	for i := 0; i < 3; i++ {
		allPassed := true
		for _, entry := range entries {
			key := ncaId + ":" + fvId + ":" + entry.Rate
			ctx, err := entry.Limiter.Get(t.Context(), key)
			require.NoError(t, err)
			if ctx.Reached {
				allPassed = false
				break
			}
		}
		assert.True(t, allPassed, "request %d should pass all limits", i)
	}

	// 4th request should fail per-second (3/3 reached) but per-minute still has room
	perSecKey := ncaId + ":" + fvId + ":" + entries[0].Rate
	perSecCtx, err := entries[0].Limiter.Get(t.Context(), perSecKey)
	require.NoError(t, err)
	assert.True(t, perSecCtx.Reached, "per-second limit should be reached")

	perMinKey := ncaId + ":" + fvId + ":" + entries[1].Rate
	perMinCtx, err := entries[1].Limiter.Get(t.Context(), perMinKey)
	require.NoError(t, err)
	assert.False(t, perMinCtx.Reached, "per-minute limit should NOT be reached yet (4/5)")
}

func TestLimiterEntry_SingleRate_BackwardCompatible(t *testing.T) {
	store := memory.NewStore()

	entries, err := parseRates(store, "2-S")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	key := "nca:fv:" + entries[0].Rate

	// 2 requests should pass
	for i := 0; i < 2; i++ {
		ctx, err := entries[0].Limiter.Get(t.Context(), key)
		require.NoError(t, err)
		assert.False(t, ctx.Reached, "request %d should pass", i)
	}

	// 3rd request should fail
	ctx, err := entries[0].Limiter.Get(t.Context(), key)
	require.NoError(t, err)
	assert.True(t, ctx.Reached, "3rd request should be rate limited")
}

// TestLimiterEntry_UnorderedRates verifies that rate limits work correctly
// regardless of the order they appear in. Here the per-minute limit (5-M) is
// listed before the per-second limit (3-S). The tighter per-second limit
// should still be enforced first.
func TestLimiterEntry_UnorderedRates(t *testing.T) {
	store := memory.NewStore()

	// Per-minute BEFORE per-second -- order shouldn't matter
	entries, err := parseRates(store, "5-M,3-S")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "5-M", entries[0].Rate)
	assert.Equal(t, "3-S", entries[1].Rate)

	ncaId := "test_nca"
	fvId := "test_fv"

	checkAll := func() bool {
		for _, entry := range entries {
			key := ncaId + ":" + fvId + ":" + entry.Rate
			ctx, err := entry.Limiter.Get(t.Context(), key)
			require.NoError(t, err)
			if ctx.Reached {
				return false
			}
		}
		return true
	}

	// First 3 requests should pass (within both 3-S and 5-M)
	for i := 0; i < 3; i++ {
		assert.True(t, checkAll(), "request %d should pass all limits", i)
	}

	// 4th request: per-second (3) is reached, per-minute (3/5) still has room
	// Even though per-minute is checked first (index 0), per-second blocks it
	assert.False(t, checkAll(), "4th request should be blocked by per-second limit")

	// Verify per-minute was incremented to 4 (3 passed + 1 from the failed 4th request)
	// and per-second is at 4 (3 passed + 1 failed = over limit)
	perMinKey := ncaId + ":" + fvId + ":" + entries[0].Rate
	perMinCtx, err := entries[0].Limiter.Peek(t.Context(), perMinKey)
	require.NoError(t, err)
	assert.False(t, perMinCtx.Reached, "per-minute should NOT be reached (4/5)")
}

func TestLimiterEntry_ExcludedNcaIds(t *testing.T) {
	excluded := make(ExcludedNcaIds)
	excluded["exempt_nca"] = struct{}{}

	entry := LimiterEntry{
		Rates:          nil,
		ExcludedNcaIds: excluded,
	}

	_, exists := entry.ExcludedNcaIds["exempt_nca"]
	assert.True(t, exists, "exempt NCA should be in exclusion list")

	_, exists = entry.ExcludedNcaIds["other_nca"]
	assert.False(t, exists, "other NCA should NOT be in exclusion list")
}
