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
package proxy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvocationGateAdmitsUntilClosed(t *testing.T) {
	gate := newInvocationGate()

	require.True(t, gate.begin())
	gate.end()

	gate.closeAndDrain(time.Second)

	assert.False(t, gate.begin(), "admission must be closed after drain")
}

// The point of the gate: an invocation admitted before shutdown finishes
// publishing before the purge takes its snapshot. Draining must therefore block
// until that invocation calls end.
func TestInvocationGateDrainWaitsForAdmittedInvocation(t *testing.T) {
	gate := newInvocationGate()
	require.True(t, gate.begin())

	const held = 100 * time.Millisecond
	var finished atomic.Bool
	// Recorded before the goroutine starts. Taking it afterwards allows the
	// sleep to begin first, which makes the measured elapsed time slightly
	// shorter than held and fails the assertion for no real reason.
	start := time.Now()
	go func() {
		time.Sleep(held)
		// Set before end so that observing "not finished" after the drain
		// returns can only mean the drain did not wait.
		finished.Store(true)
		gate.end()
	}()

	gate.closeAndDrain(5 * time.Second)

	assert.True(t, finished.Load(), "drain returned before the admitted invocation finished")
	assert.GreaterOrEqual(t, time.Since(start), held, "drain returned without waiting out the invocation")
}

// A stuck invocation must not hold shutdown open indefinitely.
func TestInvocationGateDrainGivesUpAfterTimeout(t *testing.T) {
	gate := newInvocationGate()
	require.True(t, gate.begin())
	t.Cleanup(gate.end)

	start := time.Now()
	gate.closeAndDrain(200 * time.Millisecond)
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 200*time.Millisecond)
	assert.Less(t, elapsed, 5*time.Second, "drain should give up rather than block shutdown")
}

// Draining with nothing in flight is the common case and must not wait.
func TestInvocationGateDrainReturnsImmediatelyWhenIdle(t *testing.T) {
	gate := newInvocationGate()

	start := time.Now()
	gate.closeAndDrain(5 * time.Second)

	assert.Less(t, time.Since(start), time.Second)
}

func TestInvocationGateConcurrentUse(t *testing.T) {
	gate := newInvocationGate()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if gate.begin() {
				gate.end()
			}
		}()
	}

	gate.closeAndDrain(5 * time.Second)
	wg.Wait()

	assert.False(t, gate.begin())
}
