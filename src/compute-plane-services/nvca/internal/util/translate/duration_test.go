/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package translate

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMaxRuntimeDuration(t *testing.T) {
	tests := []struct {
		name                   string
		maxRuntimeDurationStr  string
		expectedDuration       time.Duration
		expectError            bool
		expectedErrorSubstring string
	}{
		{
			name:                  "empty string returns max duration",
			maxRuntimeDurationStr: "",
			expectedDuration:      time.Duration(math.MaxInt64),
			expectError:           false,
		},
		{
			name:                  "PT1H parses to 1 hour",
			maxRuntimeDurationStr: "PT1H",
			expectedDuration:      time.Hour,
			expectError:           false,
		},
		{
			name:                  "PT2H parses to 2 hours",
			maxRuntimeDurationStr: "PT2H",
			expectedDuration:      2 * time.Hour,
			expectError:           false,
		},
		{
			name:                  "PT30M parses to 30 minutes",
			maxRuntimeDurationStr: "PT30M",
			expectedDuration:      30 * time.Minute,
			expectError:           false,
		},
		{
			name:                  "PT1H30M parses to 1.5 hours",
			maxRuntimeDurationStr: "PT1H30M",
			expectedDuration:      time.Hour + 30*time.Minute,
			expectError:           false,
		},
		{
			name:                  "PT45S parses to 45 seconds",
			maxRuntimeDurationStr: "PT45S",
			expectedDuration:      45 * time.Second,
			expectError:           false,
		},
		{
			name:                  "PT1H30M45S parses to complex duration",
			maxRuntimeDurationStr: "PT1H30M45S",
			expectedDuration:      time.Hour + 30*time.Minute + 45*time.Second,
			expectError:           false,
		},
		{
			name:                  "P1D parses to 1 day",
			maxRuntimeDurationStr: "P1D",
			expectedDuration:      24 * time.Hour,
			expectError:           false,
		},
		{
			name:                  "P1DT1H parses to 25 hours",
			maxRuntimeDurationStr: "P1DT1H",
			expectedDuration:      25 * time.Hour,
			expectError:           false,
		},
		{
			name:                   "invalid format returns error",
			maxRuntimeDurationStr:  "invalid",
			expectError:            true,
			expectedErrorSubstring: "could not parse duration string",
		},
		{
			name:                   "invalid ISO8601 format returns error",
			maxRuntimeDurationStr:  "P1H", // missing T before time component
			expectError:            true,
			expectedErrorSubstring: "could not parse duration string",
		},
		{
			name:                   "malformed duration returns error",
			maxRuntimeDurationStr:  "PTXH",
			expectError:            true,
			expectedErrorSubstring: "could not parse duration string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			duration, err := ParseMaxRuntimeDuration(tt.maxRuntimeDurationStr)

			if tt.expectError {
				require.Error(t, err)
				if tt.expectedErrorSubstring != "" {
					assert.Contains(t, err.Error(), tt.expectedErrorSubstring)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedDuration, duration)
			}
		})
	}
}

func TestParseMaxRuntimeDuration_EdgeCases(t *testing.T) {
	t.Run("zero duration", func(t *testing.T) {
		duration, err := ParseMaxRuntimeDuration("PT0S")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), duration)
	})

	t.Run("very large duration", func(t *testing.T) {
		duration, err := ParseMaxRuntimeDuration("PT8760H") // 1 year in hours
		require.NoError(t, err)
		assert.Equal(t, 8760*time.Hour, duration)
	})

	t.Run("whitespace only string", func(t *testing.T) {
		_, err := ParseMaxRuntimeDuration("   ")
		// This should be treated as non-empty, so it should try to parse
		require.Error(t, err)
	})
}

func TestParseMaxRuntimeDuration_DefaultBehavior(t *testing.T) {
	t.Run("empty string returns near-infinite duration", func(t *testing.T) {
		duration, err := ParseMaxRuntimeDuration("")
		require.NoError(t, err)

		// Verify it's the maximum possible duration
		assert.Equal(t, time.Duration(math.MaxInt64), duration)

		// Verify it's practically infinite (more than 290 years)
		assert.True(t, duration > 290*365*24*time.Hour)
	})
}

// Benchmark tests to ensure performance is acceptable
func BenchmarkParseMaxRuntimeDuration(b *testing.B) {
	testCases := []string{
		"",
		"PT1H",
		"PT2H30M",
		"P1DT1H30M45S",
	}

	for _, tc := range testCases {
		b.Run(tc, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ParseMaxRuntimeDuration(tc)
			}
		})
	}
}
