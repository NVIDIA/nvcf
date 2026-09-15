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

// Copyright 2024-2025, NVIDIA CORPORATION & AFFILIATES. All rights reserved.
//
// NOTICE TO USER:
// This source code is licensed under NVIDIA Software License Agreement
// available at https://developer.nvidia.com/nvca-license
//
// NVIDIA is strictly protected under copyright and patent laws.

package nvca

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// mockNodeFeaturesClient implements nodefeatures.Client for testing.
type mockNodeFeaturesClient struct {
	mu      sync.Mutex
	gpus    []types.BackendGPU
	err     error
	callCnt int
}

func (m *mockNodeFeaturesClient) GetAllBackendGPUs(ctx context.Context) ([]types.BackendGPU, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCnt++
	return m.gpus, m.err
}

func (m *mockNodeFeaturesClient) GetGPUResources(ctx context.Context, gpuName types.GPUName) (types.GPUResource, error) {
	return types.GPUResource{}, nil
}

func (m *mockNodeFeaturesClient) setGPUs(gpus []types.BackendGPU) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gpus = gpus
	m.err = nil
}

func (m *mockNodeFeaturesClient) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
	m.gpus = nil
}

func (m *mockNodeFeaturesClient) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCnt
}

func TestNewGPUMonitor(t *testing.T) {
	client := &mockNodeFeaturesClient{}

	m := NewGPUMonitor(client)

	assert.NotNil(t, m)
	assert.Equal(t, DefaultGPUPollInterval, m.config.PollInterval)
	assert.Equal(t, DefaultGPUDebounceTime, m.config.DebounceTime)
	assert.False(t, m.HasGPUs())
}

func TestNewGPUMonitorWithOptions(t *testing.T) {
	client := &mockNodeFeaturesClient{}

	callback := func(ctx context.Context, hasGPUs bool) {
		// callback for testing
	}

	m := NewGPUMonitor(client,
		WithGPUPollInterval(10*time.Second),
		WithGPUDebounceTime(5*time.Second),
		WithGPUStateChangeCallback(callback),
	)

	assert.NotNil(t, m)
	assert.Equal(t, 10*time.Second, m.config.PollInterval)
	assert.Equal(t, 5*time.Second, m.config.DebounceTime)
	assert.NotNil(t, m.onStateChange)
}

func TestGPUMonitor_HasGPUs(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	m := NewGPUMonitor(client)

	// Initially no GPUs
	assert.False(t, m.HasGPUs())

	// Set GPUs available
	m.SetHasGPUs(true)
	assert.True(t, m.HasGPUs())

	// Set GPUs unavailable
	m.SetHasGPUs(false)
	assert.False(t, m.HasGPUs())
}

func TestGPUMonitor_GetComponentStatus_NoGPUs(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	m := NewGPUMonitor(client)
	m.SetHasGPUs(false)

	ctx := context.Background()
	health, err := m.GetComponentStatus(ctx)

	require.NoError(t, err)
	assert.Contains(t, health.Components, GPUMonitorComponentName)
	assert.Equal(t, types.HealthStatusUnhealthy, health.Components[GPUMonitorComponentName].Status)
	assert.Contains(t, health.Components[GPUMonitorComponentName].Errors, "no GPUs available in cluster")
}

func TestGPUMonitor_GetComponentStatus_HasGPUs(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	m := NewGPUMonitor(client)
	m.SetHasGPUs(true)

	ctx := context.Background()
	health, err := m.GetComponentStatus(ctx)

	require.NoError(t, err)
	assert.Contains(t, health.Components, GPUMonitorComponentName)
	assert.Equal(t, types.HealthStatusHealthy, health.Components[GPUMonitorComponentName].Status)
	assert.Empty(t, health.Components[GPUMonitorComponentName].Errors)
}

func TestGPUMonitor_checkGPUs_InitialCheck(t *testing.T) {
	tests := []struct {
		name           string
		gpus           []types.BackendGPU
		err            error
		expectedState  bool
		expectCallback bool
	}{
		{
			name:           "GPUs available",
			gpus:           []types.BackendGPU{{Name: "A100"}},
			expectedState:  true,
			expectCallback: true,
		},
		{
			name:           "No GPUs - empty slice",
			gpus:           []types.BackendGPU{},
			expectedState:  false,
			expectCallback: false, // state was already false
		},
		{
			name:           "No GPUs - NotExistError",
			err:            nvcaerrors.NotExistError(errors.New("no GPUs")),
			expectedState:  false,
			expectCallback: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockNodeFeaturesClient{gpus: tc.gpus, err: tc.err}

			var callbackCalled atomic.Bool
			var callbackValue atomic.Bool
			callback := func(ctx context.Context, hasGPUs bool) {
				callbackCalled.Store(true)
				callbackValue.Store(hasGPUs)
			}

			// Use zero debounce for immediate state change in tests
			m := NewGPUMonitor(client,
				WithGPUDebounceTime(0),
				WithGPUStateChangeCallback(callback),
			)

			ctx := context.Background()
			m.checkGPUs(ctx)

			assert.Equal(t, tc.expectedState, m.HasGPUs())
			assert.Equal(t, tc.expectCallback, callbackCalled.Load())
			if tc.expectCallback {
				assert.Equal(t, tc.expectedState, callbackValue.Load())
			}
		})
	}
}

func TestGPUMonitor_checkGPUs_KeepsStateOnError(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	m := NewGPUMonitor(client, WithGPUDebounceTime(0))

	// Set initial state to true
	m.SetHasGPUs(true)

	// Set error (not NotExistError)
	client.setError(errors.New("connection failed"))

	ctx := context.Background()
	m.checkGPUs(ctx)

	// State should remain unchanged
	assert.True(t, m.HasGPUs())
}

func TestGPUMonitor_Debounce(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})

	var callbackCalled atomic.Bool
	callback := func(ctx context.Context, hasGPUs bool) {
		callbackCalled.Store(true)
	}

	// Use controlled time
	currentTime := time.Now()
	timeMu := sync.Mutex{}
	nowFunc := func() time.Time {
		timeMu.Lock()
		defer timeMu.Unlock()
		return currentTime
	}
	advanceTime := func(d time.Duration) {
		timeMu.Lock()
		defer timeMu.Unlock()
		currentTime = currentTime.Add(d)
	}

	m := NewGPUMonitor(client,
		WithGPUDebounceTime(30*time.Second),
		WithGPUStateChangeCallback(callback),
		withNowFunc(nowFunc),
	)

	ctx := context.Background()

	// Initial state is false, GPUs are available
	// First check should start debounce
	m.checkGPUs(ctx)
	assert.False(t, m.HasGPUs(), "state should not change immediately")
	assert.False(t, callbackCalled.Load(), "callback should not be called yet")

	// Advance time but not enough
	advanceTime(15 * time.Second)
	m.checkGPUs(ctx)
	assert.False(t, m.HasGPUs(), "state should still be false during debounce")
	assert.False(t, callbackCalled.Load())

	// Advance time past debounce period
	advanceTime(20 * time.Second) // total 35 seconds
	m.checkGPUs(ctx)
	assert.True(t, m.HasGPUs(), "state should change after debounce")
	assert.True(t, callbackCalled.Load(), "callback should be called after debounce")
}

func TestGPUMonitor_DebounceResetOnStateFlap(t *testing.T) {
	client := &mockNodeFeaturesClient{}

	// Use controlled time
	currentTime := time.Now()
	timeMu := sync.Mutex{}
	nowFunc := func() time.Time {
		timeMu.Lock()
		defer timeMu.Unlock()
		return currentTime
	}
	advanceTime := func(d time.Duration) {
		timeMu.Lock()
		defer timeMu.Unlock()
		currentTime = currentTime.Add(d)
	}

	m := NewGPUMonitor(client,
		WithGPUDebounceTime(30*time.Second),
		withNowFunc(nowFunc),
	)

	ctx := context.Background()

	// Initial state is false, set GPUs available
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})
	m.checkGPUs(ctx)
	assert.False(t, m.HasGPUs(), "debounce should be in progress")

	// Advance time but not enough
	advanceTime(20 * time.Second)
	m.checkGPUs(ctx)
	assert.False(t, m.HasGPUs())

	// Now GPUs disappear - this should reset the debounce
	client.setGPUs([]types.BackendGPU{})
	m.checkGPUs(ctx)
	// State goes back to matching current (false), debounce should reset
	assert.False(t, m.HasGPUs())

	// GPUs come back
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})
	m.checkGPUs(ctx) // Starts new debounce period
	assert.False(t, m.HasGPUs(), "debounce should have restarted")

	// Advance 10 seconds - still within debounce
	advanceTime(10 * time.Second)
	m.checkGPUs(ctx)
	assert.False(t, m.HasGPUs(), "debounce should still be in progress")

	// Now wait remaining debounce time (20 more seconds = 30 total)
	advanceTime(25 * time.Second) // 35 seconds total from restart, should exceed 30s debounce
	m.checkGPUs(ctx)
	assert.True(t, m.HasGPUs(), "state should change after full debounce")
}

func TestGPUMonitor_StartStop(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})

	m := NewGPUMonitor(client,
		WithGPUPollInterval(10*time.Millisecond),
		WithGPUDebounceTime(0), // No debounce for faster test
	)

	ctx := context.Background()
	m.Start(ctx)

	// Wait for at least one poll
	time.Sleep(50 * time.Millisecond)
	assert.True(t, m.HasGPUs())
	assert.Greater(t, client.getCallCount(), 1)

	// Stop the monitor
	m.Stop()

	// Verify no more calls after stop
	countAfterStop := client.getCallCount()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, countAfterStop, client.getCallCount(), "no more calls should occur after stop")
}

func TestGPUMonitor_StartWithContextCancel(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})

	m := NewGPUMonitor(client,
		WithGPUPollInterval(10*time.Millisecond),
		WithGPUDebounceTime(0),
	)

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)

	// Wait for at least one poll
	time.Sleep(30 * time.Millisecond)

	// Cancel context
	cancel()

	// Give time for goroutine to exit
	time.Sleep(20 * time.Millisecond)

	countAfterCancel := client.getCallCount()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, countAfterCancel, client.getCallCount(), "no more calls should occur after cancel")
}

func TestGPUMonitor_SetOnGPUStateChange(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	m := NewGPUMonitor(client, WithGPUDebounceTime(0))

	var callbackCalled atomic.Bool
	var callbackValue atomic.Bool

	// Set callback after construction
	m.SetOnGPUStateChange(func(ctx context.Context, hasGPUs bool) {
		callbackCalled.Store(true)
		callbackValue.Store(hasGPUs)
	})

	// Trigger a state change
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})
	ctx := context.Background()
	m.checkGPUs(ctx)

	assert.True(t, callbackCalled.Load())
	assert.True(t, callbackValue.Load())
	assert.True(t, m.HasGPUs())
}

func TestGPUMonitor_StateTransitions(t *testing.T) {
	client := &mockNodeFeaturesClient{}

	var transitions []bool
	var mu sync.Mutex
	callback := func(ctx context.Context, hasGPUs bool) {
		mu.Lock()
		defer mu.Unlock()
		transitions = append(transitions, hasGPUs)
	}

	// Use controlled time
	currentTime := time.Now()
	timeMu := sync.Mutex{}
	nowFunc := func() time.Time {
		timeMu.Lock()
		defer timeMu.Unlock()
		return currentTime
	}
	advanceTime := func(d time.Duration) {
		timeMu.Lock()
		defer timeMu.Unlock()
		currentTime = currentTime.Add(d)
	}

	m := NewGPUMonitor(client,
		WithGPUDebounceTime(10*time.Second),
		WithGPUStateChangeCallback(callback),
		withNowFunc(nowFunc),
	)

	ctx := context.Background()

	// Start with no GPUs
	client.setGPUs([]types.BackendGPU{})
	m.checkGPUs(ctx)
	assert.False(t, m.HasGPUs())

	// GPUs appear
	client.setGPUs([]types.BackendGPU{{Name: "A100"}, {Name: "H100"}})
	m.checkGPUs(ctx)
	advanceTime(15 * time.Second)
	m.checkGPUs(ctx)
	assert.True(t, m.HasGPUs())

	// GPUs disappear
	client.setGPUs([]types.BackendGPU{})
	m.checkGPUs(ctx)
	advanceTime(15 * time.Second)
	m.checkGPUs(ctx)
	assert.False(t, m.HasGPUs())

	// GPUs come back
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})
	m.checkGPUs(ctx)
	advanceTime(15 * time.Second)
	m.checkGPUs(ctx)
	assert.True(t, m.HasGPUs())

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []bool{true, false, true}, transitions)
}

func TestNewGPUMonitorWithNodeInformer(t *testing.T) {
	client := &mockNodeFeaturesClient{}

	// Just test that the option is accepted (we can't easily mock SharedIndexInformer)
	m := NewGPUMonitor(client, WithNodeInformer(nil))

	assert.NotNil(t, m)
	assert.Nil(t, m.nodeInformer)
}

func TestGPUMonitor_TriggerCheck_Coalescing(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	client.setGPUs([]types.BackendGPU{})

	m := NewGPUMonitor(client,
		WithGPUPollInterval(1*time.Hour), // Very long poll interval to not interfere
		WithGPUDebounceTime(0),
	)

	// Trigger multiple checks rapidly - they should coalesce
	for i := 0; i < 10; i++ {
		m.triggerCheck()
	}

	// Only one should be buffered due to the channel capacity of 1
	select {
	case <-m.triggerCh:
		// Good, got one
	default:
		t.Fatal("expected one trigger in channel")
	}

	// Channel should now be empty
	select {
	case <-m.triggerCh:
		t.Fatal("expected channel to be empty after draining")
	default:
		// Good, channel is empty
	}
}

func TestGPUMonitor_EventTriggeredCheck(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	client.setGPUs([]types.BackendGPU{})

	var callbackCalled atomic.Bool
	callback := func(ctx context.Context, hasGPUs bool) {
		callbackCalled.Store(true)
	}

	m := NewGPUMonitor(client,
		WithGPUPollInterval(1*time.Hour), // Very long poll interval
		WithGPUDebounceTime(0),
		WithGPUStateChangeCallback(callback),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Start(ctx)

	// Initial check happened in Start, should have no GPUs
	assert.False(t, m.HasGPUs())
	initialCallCount := client.getCallCount()

	// Now add GPUs
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})

	// Trigger a check via the trigger channel (simulating a node event)
	m.triggerCheck()

	// Wait for the triggered check to be processed
	time.Sleep(50 * time.Millisecond)

	// Should have detected GPUs now
	assert.True(t, m.HasGPUs(), "should detect GPUs after triggered check")
	assert.True(t, callbackCalled.Load(), "callback should have been called")
	assert.Greater(t, client.getCallCount(), initialCallCount, "should have made additional GPU check")

	m.Stop()
}

func TestGPUMonitor_TriggerCheckDuringPolling(t *testing.T) {
	client := &mockNodeFeaturesClient{}
	client.setGPUs([]types.BackendGPU{})

	m := NewGPUMonitor(client,
		WithGPUPollInterval(50*time.Millisecond),
		WithGPUDebounceTime(0),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Start(ctx)

	// Wait for a few poll cycles
	time.Sleep(120 * time.Millisecond)
	pollCallCount := client.getCallCount()

	// Add GPUs and trigger a check
	client.setGPUs([]types.BackendGPU{{Name: "A100"}})
	m.triggerCheck()

	// Wait for triggered check to be processed
	time.Sleep(30 * time.Millisecond)

	// Should have made additional call(s)
	assert.Greater(t, client.getCallCount(), pollCallCount)
	assert.True(t, m.HasGPUs())

	m.Stop()
}
