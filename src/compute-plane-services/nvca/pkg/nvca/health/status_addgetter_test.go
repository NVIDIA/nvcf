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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func componentGetter(name string, status nvcatypes.HealthStatus, level nvcatypes.StatusLevel) ComponentStatusGetter {
	return &mockBackendStatusGetter{
		hs: nvcatypes.AgentHealth{
			Components: map[string]nvcatypes.ComponentHealth{
				name: {Status: status, StatusLevel: level},
			},
		},
	}
}

// TestAddGetterIncludesComponentAfterConstruction covers the ordering problem
// behind nvcf#1590: components built after this cache (the queue manager) have
// to be registerable later, or they cannot gate readiness at all.
func TestAddGetterIncludesComponentAfterConstruction(t *testing.T) {
	ctx := context.Background()
	cache := NewBackendStatusCache(0,
		componentGetter("gpu", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError))

	status, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, status.Status)
	assert.NotContains(t, status.Components, "queue")

	cache.AddGetter(componentGetter("queue", nvcatypes.HealthStatusUnhealthy, nvcatypes.StatusLevelError))

	status, err = cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Contains(t, status.Components, "queue", "late-registered component must be gathered")
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, status.Status,
		"an unhealthy Error-level component must flip the aggregate")
}

// TestAddGetterErrorLevelRequiredToGateReadiness documents why the queue
// component is registered at Error rather than Warn level. The readiness route
// serves GetStatusForLevel(StatusLevelWarn), and that aggregation only flips
// unhealthy for components strictly below Warn, so a Warn-level component would
// show up in the payload while gating nothing.
func TestAddGetterErrorLevelRequiredToGateReadiness(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		level nvcatypes.StatusLevel
		want  nvcatypes.HealthStatus
	}{
		{"error level gates", nvcatypes.StatusLevelError, nvcatypes.HealthStatusUnhealthy},
		{"warn level does not gate", nvcatypes.StatusLevelWarn, nvcatypes.HealthStatusHealthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := NewBackendStatusCache(0)
			cache.AddGetter(componentGetter("queue", nvcatypes.HealthStatusUnhealthy, tc.level))

			_, err := cache.RefreshStatus(ctx)
			require.NoError(t, err)

			// This is the level the readiness route actually serves.
			readiness := cache.GetStatusForLevel(nvcatypes.StatusLevelWarn)
			assert.Equal(t, tc.want, readiness.Status)
			assert.Contains(t, readiness.Components, "queue",
				"component should be reported either way")
		})
	}
}

// TestAddGetterConcurrentWithRefresh guards the mutex added alongside
// AddGetter: registration happens during startup while the periodic refresh
// loop is already iterating the getter set. Run with -race.
func TestAddGetterConcurrentWithRefresh(t *testing.T) {
	ctx := context.Background()
	cache := NewBackendStatusCache(0,
		componentGetter("gpu", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError))

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			cache.AddGetter(componentGetter("late", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := cache.RefreshStatus(ctx); err != nil {
				t.Errorf("refresh failed: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}
