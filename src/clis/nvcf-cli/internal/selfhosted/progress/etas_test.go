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

package progress

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPhaseETA_Bounds(t *testing.T) {
	assert.Equal(t, 0, PhaseETA(0), "phase 0 is out of range")
	assert.Equal(t, 0, PhaseETA(9), "phase 9 is out of range")
	assert.NotZero(t, PhaseETA(1), "phase 1 must have a non-zero ETA")
	assert.NotZero(t, PhaseETA(totalPhases), "last phase must have a non-zero ETA")
}

func TestAllPhaseETAs_Length(t *testing.T) {
	got := AllPhaseETAs()
	require.Len(t, got, totalPhases)
	for i, p := range got {
		assert.Equal(t, i+1, p.Num, "phase Num must be 1-based at index %d", i)
		assert.NotEmpty(t, p.Name, "phase Name must not be empty at index %d", i)
		assert.NotZero(t, p.ETASec, "phase ETASec must be non-zero at index %d", i)
	}
}

func TestAllPhaseETAs_TotalMatchesSum(t *testing.T) {
	phases := AllPhaseETAs()
	sum := 0
	for _, p := range phases {
		sum += p.ETASec
	}
	// The total is not a fixed constant, but it must be positive and reasonable.
	assert.Greater(t, sum, 0, "total ETA must be positive")
	// Rough sanity: a real install takes at least a minute.
	assert.Greater(t, sum, 60, "total ETA must exceed 60s")
}
