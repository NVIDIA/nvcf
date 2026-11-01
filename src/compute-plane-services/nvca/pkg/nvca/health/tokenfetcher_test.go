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

package health

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenFetcherHealthCheckDefault(t *testing.T) {
	type test struct {
		unauthorizedFailureThreshold uint64
		message                      string
	}

	tests := []test{
		{
			message: "with MaxUnauthorizedFailures default",
		},
		{
			message:                      "with MaxUnauthorizedFailures default",
			unauthorizedFailureThreshold: 100,
		},
	}

	for _, tc := range tests {
		t.Run(tc.message, func(t *testing.T) {
			var options []TokenFetcherHealthCheckOption
			if tc.unauthorizedFailureThreshold > 0 {
				options = append(options, WithUnauthorizedFailureThreshold(tc.unauthorizedFailureThreshold))
			}
			healthCheck := NewTokenFetcherHealthCheck("mock", options...)
			require.NotNil(t, healthCheck)
			assert.True(t, healthCheck.StatusOK())

			// Test with default
			for i := 0; i <= int(healthCheck.unauthorizedFailureThreshold); i++ {
				healthCheck.OnFetchTokenResponse(http.StatusUnauthorized)
			}
			assert.False(t, healthCheck.StatusOK())
			// Flip back to true
			healthCheck.OnFetchTokenResponse(http.StatusOK)
			assert.True(t, healthCheck.StatusOK())
		})
	}
}

func TestSuccessfulTokenFetcherHealthCheck(t *testing.T) {
	healthCheck := SuccessfulTokenFetcherHealthCheck("mock")
	require.NotNil(t, healthCheck)
	assert.True(t, healthCheck.StatusOK())

	// Test with default
	for i := 0; i <= int(DefaultUnauthorizedFailureThreshold); i++ {
		healthCheck.OnFetchTokenResponse(http.StatusUnauthorized)
	}
	assert.True(t, healthCheck.StatusOK())
}
