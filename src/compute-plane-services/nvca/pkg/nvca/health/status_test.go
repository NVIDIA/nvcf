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
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type mockBackendStatusGetter struct {
	k8sVersion string
	hs         nvcatypes.AgentHealth
	err        error
}

func (mbsg *mockBackendStatusGetter) GetComponentStatus(ctx context.Context) (nvcatypes.AgentHealth, error) {
	if mbsg.err != nil {
		return nvcatypes.AgentHealth{}, mbsg.err
	}
	return mbsg.hs, nil
}

func TestBackendStatusCache(t *testing.T) {
	ctx := context.Background()

	mockGetter := &mockBackendStatusGetter{
		k8sVersion: "1.0.0",
		hs: nvcatypes.AgentHealth{
			GPUUsage: map[nvcatypes.GPUName]nvcatypes.GPUResource{
				"A100": {
					Capacity:  1,
					Allocated: 1,
				},
			},
			Components: map[string]nvcatypes.ComponentHealth{
				"kata": {
					Status: nvcatypes.HealthStatusHealthy,
				},
			},
		},
	}

	cache := NewBackendStatusCache(0, mockGetter)
	require.NotNil(t, cache)
	status := cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusHealthy, status.Status)
	assert.Empty(t, status.GPUUsage)

	// Change the status ensuring no error
	cachedStatus, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, cachedStatus.Status)
	assert.Equal(t, mockGetter.hs.GPUUsage, cachedStatus.GPUUsage)
	if assert.Contains(t, cachedStatus.Components, "kata") {
		assert.Equal(t, cachedStatus.Components["kata"].Status, nvcatypes.HealthStatusHealthy)
	}

	// attempt with an error
	mockGetter.err = errors.New("some error")
	cachedStatus, err = cache.RefreshStatus(ctx)
	require.EqualError(t, err, "some error")
	assert.Empty(t, cachedStatus)
	cachedStatus = cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusHealthy, cachedStatus.Status)

	// attempt with unhealthy component
	mockGetter.err = nil
	kataStatus := mockGetter.hs.Components["kata"]
	kataStatus.Status = nvcatypes.HealthStatusUnhealthy
	mockGetter.hs.Components["kata"] = kataStatus
	cachedStatus, err = cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, cachedStatus.Status)
	assert.Equal(t, mockGetter.hs.GPUUsage, cachedStatus.GPUUsage)
	if assert.Contains(t, cachedStatus.Components, "kata") {
		assert.Equal(t, cachedStatus.Components["kata"].Status, nvcatypes.HealthStatusUnhealthy)
	}
	cachedStatus = cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, cachedStatus.Status)
	assert.Equal(t, mockGetter.hs.GPUUsage, cachedStatus.GPUUsage)
	if assert.Contains(t, cachedStatus.Components, "kata") {
		assert.Equal(t, cachedStatus.Components["kata"].Status, nvcatypes.HealthStatusUnhealthy)
	}

	// component is healthy now
	kataStatus.Status = nvcatypes.HealthStatusHealthy
	mockGetter.hs.Components["kata"] = kataStatus
	cachedStatus, err = cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, cachedStatus.Status)
	assert.Equal(t, mockGetter.hs.GPUUsage, cachedStatus.GPUUsage)
	if assert.Contains(t, cachedStatus.Components, "kata") {
		assert.Equal(t, cachedStatus.Components["kata"].Status, nvcatypes.HealthStatusHealthy)
	}

	// run a lot of gets and changes to the status at once to ensure no data race
	jobQueue := make(chan int, 1000)
	for i := 0; i < cap(jobQueue); i++ {
		jobQueue <- i
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			for range jobQueue {
				cachedStatus, err := cache.RefreshStatus(ctx)
				require.NoError(t, err)
				assert.Equal(t, nvcatypes.HealthStatusHealthy, cachedStatus.Status)
				assert.Equal(t, mockGetter.hs.GPUUsage, cachedStatus.GPUUsage)
				if assert.Contains(t, cachedStatus.Components, "kata") {
					assert.Equal(t, cachedStatus.Components["kata"].Status, nvcatypes.HealthStatusHealthy)
				}
			}
			wg.Done()
		}()
	}
	time.Sleep(250 * time.Millisecond)
	close(jobQueue)
	wg.Wait()
}

type mockStatusRefresher struct {
	responses []mockResponse
	callCount int
}

type mockResponse struct {
	health nvcatypes.AgentHealth
	err    error
}

func (m *mockStatusRefresher) RefreshStatus(ctx context.Context) (nvcatypes.AgentHealth, error) {
	if m.callCount >= len(m.responses) {
		return m.responses[len(m.responses)-1].health, m.responses[len(m.responses)-1].err
	}
	response := m.responses[m.callCount]
	m.callCount++
	return response.health, response.err
}

func TestWaitForHealthyStatus(t *testing.T) {
	tests := []struct {
		name           string
		responses      []mockResponse
		expectedError  error
		contextTimeout time.Duration
		checkInterval  time.Duration
	}{
		{
			name: "immediately healthy",
			responses: []mockResponse{
				{
					health: nvcatypes.AgentHealth{
						Status: nvcatypes.HealthStatusHealthy,
					},
					err: nil,
				},
			},
			expectedError:  nil,
			contextTimeout: 1 * time.Second,
			checkInterval:  100 * time.Millisecond,
		},
		{
			name: "becomes healthy after retries",
			responses: []mockResponse{
				{
					health: nvcatypes.AgentHealth{
						Status: nvcatypes.HealthStatusUnhealthy,
						Components: map[string]nvcatypes.ComponentHealth{
							"comp1": {
								Status: nvcatypes.HealthStatusUnhealthy,
								Errors: []string{"not ready"},
							},
						},
					},
					err: nil,
				},
				{
					health: nvcatypes.AgentHealth{
						Status: nvcatypes.HealthStatusUnhealthy,
						Components: map[string]nvcatypes.ComponentHealth{
							"comp1": {
								Status: nvcatypes.HealthStatusUnhealthy,
								Errors: []string{"still not ready"},
							},
						},
					},
					err: nil,
				},
				{
					health: nvcatypes.AgentHealth{
						Status: nvcatypes.HealthStatusHealthy,
					},
					err: nil,
				},
			},
			expectedError:  nil,
			contextTimeout: 1 * time.Second,
			checkInterval:  100 * time.Millisecond,
		},
		{
			name: "refresh errors then becomes healthy",
			responses: []mockResponse{
				{
					health: nvcatypes.AgentHealth{},
					err:    errors.New("refresh failed"),
				},
				{
					health: nvcatypes.AgentHealth{
						Status: nvcatypes.HealthStatusHealthy,
					},
					err: nil,
				},
			},
			expectedError:  nil,
			contextTimeout: 1 * time.Second,
			checkInterval:  100 * time.Millisecond,
		},
		{
			name: "context timeout before healthy",
			responses: []mockResponse{
				{
					health: nvcatypes.AgentHealth{
						Status: nvcatypes.HealthStatusUnhealthy,
						Components: map[string]nvcatypes.ComponentHealth{
							"comp1": {
								Status: nvcatypes.HealthStatusUnhealthy,
								Errors: []string{"not ready"},
							},
						},
					},
					err: nil,
				},
			},
			expectedError:  context.DeadlineExceeded,
			contextTimeout: 200 * time.Millisecond,
			checkInterval:  100 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()

			mockRefresher := &mockStatusRefresher{
				responses: tt.responses,
			}

			err := WaitForHealthyStatus(ctx, tt.checkInterval, tt.contextTimeout, mockRefresher)

			if tt.expectedError != nil {
				assert.ErrorIs(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			// For cases where we expect success, verify we got the expected number of calls
			if tt.expectedError == nil {
				expectedCalls := len(tt.responses)
				if expectedCalls > 1 {
					// Verify we made all the calls needed to get to healthy state
					assert.Equal(t, expectedCalls, mockRefresher.callCount)
				}
			}
		})
	}
}

func TestBackendStatusCacheWithStatusLevels(t *testing.T) {
	ctx := context.Background()

	mockGetter := &mockBackendStatusGetter{
		k8sVersion: "1.0.0",
		hs: nvcatypes.AgentHealth{
			GPUUsage: map[nvcatypes.GPUName]nvcatypes.GPUResource{
				"A100": {
					Capacity:  1,
					Allocated: 1,
				},
			},
			Components: map[string]nvcatypes.ComponentHealth{
				"kata": {
					Status:      nvcatypes.HealthStatusHealthy,
					StatusLevel: nvcatypes.StatusLevelError,
				},
				"storage-error": {
					Status:      nvcatypes.HealthStatusUnhealthy,
					StatusLevel: nvcatypes.StatusLevelError,
					Errors:      []string{"critical error"},
				},
				"storage-warning": {
					Status:      nvcatypes.HealthStatusUnhealthy,
					StatusLevel: nvcatypes.StatusLevelWarn,
					Errors:      []string{"non-critical warning"},
				},
			},
		},
	}

	cache := NewBackendStatusCache(0, mockGetter)
	require.NotNil(t, cache)

	// Test RefreshStatus which should internally call RefreshStatusForLevel with Error level
	status, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, status.Status)
	assert.Contains(t, status.Components, "storage-error")

	// Test GetStatus which should internally call GetStatusForLevel with Error level
	status = cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, status.Status)
	assert.Contains(t, status.Components, "storage-error")

	// Remove the error-level component
	delete(mockGetter.hs.Components, "storage-error")

	// Refresh the status
	status, err = cache.RefreshStatus(ctx)
	require.NoError(t, err)

	// With Error level filtering, the status should now be healthy
	// since the only unhealthy component has StatusLevelWarn
	status = cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusHealthy, status.Status)
	assert.NotContains(t, status.Components, "storage-error")
}

func TestGetStatusForLevel(t *testing.T) {
	ctx := context.Background()

	mockGetter := &mockBackendStatusGetter{
		hs: nvcatypes.AgentHealth{
			Components: map[string]nvcatypes.ComponentHealth{
				"error-component": {
					Status:      nvcatypes.HealthStatusUnhealthy,
					StatusLevel: nvcatypes.StatusLevelError,
					Errors:      []string{"critical error"},
				},
				"warn-component": {
					Status:      nvcatypes.HealthStatusUnhealthy,
					StatusLevel: nvcatypes.StatusLevelWarn,
					Errors:      []string{"non-critical warning"},
				},
				"healthy-component": {
					Status:      nvcatypes.HealthStatusHealthy,
					StatusLevel: nvcatypes.StatusLevelError,
				},
			},
		},
	}

	cache := NewBackendStatusCache(0, mockGetter)
	require.NotNil(t, cache)

	// First refresh to populate the cache
	_, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)

	// Test with Error level - should treat only error-level unhealthy components as unhealthy
	statusErrorLevel := cache.GetStatusForLevel(nvcatypes.StatusLevelError)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, statusErrorLevel.Status, "Status should be unhealthy at error level")
	assert.Contains(t, statusErrorLevel.Components, "error-component")
	assert.Contains(t, statusErrorLevel.Components, "healthy-component")

	// Update to remove error component, keeping only warning
	delete(mockGetter.hs.Components, "error-component")
	_, err = cache.RefreshStatus(ctx)
	require.NoError(t, err)

	// With error level, should now be healthy since warning components don't affect error-level status
	statusErrorLevel = cache.GetStatusForLevel(nvcatypes.StatusLevelError)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, statusErrorLevel.Status, "Status should be healthy at error level with only warning component")

	// With warn level, should still be healthy due to warning component
	statusWarnLevel := cache.GetStatusForLevel(nvcatypes.StatusLevelWarn)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, statusWarnLevel.Status, "Status should be healthy at warn level")
}

func TestRefreshStatusForLevel(t *testing.T) {
	ctx := context.Background()

	mockGetter := &mockBackendStatusGetter{
		hs: nvcatypes.AgentHealth{
			Components: map[string]nvcatypes.ComponentHealth{
				"error-component": {
					Status:      nvcatypes.HealthStatusUnhealthy,
					StatusLevel: nvcatypes.StatusLevelError,
					Errors:      []string{"critical error"},
				},
				"warn-component": {
					Status:      nvcatypes.HealthStatusUnhealthy,
					StatusLevel: nvcatypes.StatusLevelWarn,
					Errors:      []string{"non-critical warning"},
				},
			},
		},
	}

	cache := NewBackendStatusCache(0, mockGetter)
	require.NotNil(t, cache)

	// Test with Error level
	statusErrorLevel, err := cache.RefreshStatusForLevel(ctx, nvcatypes.StatusLevelError)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, statusErrorLevel.Status, "Status should be unhealthy at error level")

	// Update to remove error component, keeping only warning
	delete(mockGetter.hs.Components, "error-component")

	// Refresh with Error level - should be healthy since only warning component remains
	statusErrorLevel, err = cache.RefreshStatusForLevel(ctx, nvcatypes.StatusLevelError)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, statusErrorLevel.Status, "Status should be healthy at error level with only warning component")

	// Refresh with Warn level - should be healthy due to warning component
	statusWarnLevel, err := cache.RefreshStatusForLevel(ctx, nvcatypes.StatusLevelWarn)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, statusWarnLevel.Status, "Status should be healthy at warn level")

	// Now remove the warning component too
	delete(mockGetter.hs.Components, "warn-component")

	// Refresh with Warn level - should be healthy now
	statusWarnLevel, err = cache.RefreshStatusForLevel(ctx, nvcatypes.StatusLevelWarn)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, statusWarnLevel.Status, "Status should be healthy with no unhealthy components")
}
