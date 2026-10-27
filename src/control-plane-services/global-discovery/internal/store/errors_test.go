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

package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrStaleGeneration(t *testing.T) {
	err := fmt.Errorf("put export: %w", ErrStaleGeneration{Current: 42})

	assert.ErrorIs(t, err, ErrStaleGeneration{})
	assert.ErrorIs(t, err, ErrStaleGeneration{Current: 7}, "Is ignores Current")
	assert.NotErrorIs(t, err, ErrNotFound)
	assert.EqualError(t, err, "put export: store: stale generation, current generation is 42")

	var stale ErrStaleGeneration
	if assert.ErrorAs(t, err, &stale) {
		assert.Equal(t, uint64(42), stale.Current)
	}
	current, ok := StaleGeneration(err)
	assert.True(t, ok)
	assert.Equal(t, uint64(42), current)

	_, ok = StaleGeneration(ErrNotFound)
	assert.False(t, ok)
	_, ok = StaleGeneration(nil)
	assert.False(t, ok)
}

func TestSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{ErrNotFound, ErrLeaseEpochMismatch, ErrPoolNameTaken, ErrTooLarge, ErrUnavailable, ErrStaleGeneration{}}
	for i, a := range sentinels {
		for j, b := range sentinels {
			assert.Equal(t, i == j, errors.Is(a, b), "%v vs %v", a, b)
		}
	}
}
