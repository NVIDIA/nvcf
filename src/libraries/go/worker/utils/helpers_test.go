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

package utils

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/semaphore"
)

func TestAcquireUpToMax(t *testing.T) {
	t.Run("grabs all available tokens when free", func(t *testing.T) {
		sem := semaphore.NewWeighted(3)
		n, err := AcquireUpToMax(context.Background(), sem)
		require.NoError(t, err)
		require.Equal(t, uint32(3), n)
	})
	t.Run("returns ctx error when full and context canceled", func(t *testing.T) {
		sem := semaphore.NewWeighted(1)
		require.True(t, sem.TryAcquire(1)) // full, nothing available
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		n, err := AcquireUpToMax(ctx, sem)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, uint32(0), n)
	})
}

func TestConvertISO8601Duration(t *testing.T) {
	t.Run("PT24H is 24 hours", func(t *testing.T) {
		d, err := ConvertISO8601Duration("PT24H")
		require.NoError(t, err)
		require.Equal(t, 24*time.Hour, d)
	})
	t.Run("PT1H30M is 90 minutes", func(t *testing.T) {
		d, err := ConvertISO8601Duration("PT1H30M")
		require.NoError(t, err)
		require.Equal(t, 90*time.Minute, d)
	})
	t.Run("invalid returns error", func(t *testing.T) {
		d, err := ConvertISO8601Duration("not-a-duration")
		require.Error(t, err)
		require.Equal(t, time.Duration(0), d)
	})
}

func TestEffectiveEnvironment(t *testing.T) {
	require.Equal(t, "icms", EffectiveEnvironment("icms", "spot"), "icms preferred when set")
	require.Equal(t, "spot", EffectiveEnvironment("", "spot"), "falls back to spot when icms empty")
	require.Equal(t, "", EffectiveEnvironment("", ""))
}
