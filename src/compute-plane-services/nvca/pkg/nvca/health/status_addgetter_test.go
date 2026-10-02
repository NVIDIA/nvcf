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
	"time"

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

	cache.AddGetter(context.Background(), componentGetter("queue", nvcatypes.HealthStatusUnhealthy, nvcatypes.StatusLevelError))

	status, err = cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Contains(t, status.Components, "queue", "late-registered component must be gathered")
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, status.Status,
		"an unhealthy Error-level component must flip the aggregate")
}

// TestAddGetterGatesReadinessBeforeAnyRefresh covers the window between
// registering a component and the first refresh that includes it. The readiness
// route reads GetStatusForLevel, which never gathers, and Agent.Start registers
// the queue manager and arms readiness with no refresh in between. Appending to
// getters alone would leave /healthz answering from the pre-registration
// aggregate, reporting healthy without the queue until the refresh at the top of
// the next QueueManager.SyncQueues tick.
func TestAddGetterGatesReadinessBeforeAnyRefresh(t *testing.T) {
	ctx := context.Background()
	cache := NewBackendStatusCache(0,
		componentGetter("gpu", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError))

	// Stands in for the startup health gate: healthy, and no queue component yet.
	status, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, nvcatypes.HealthStatusHealthy, status.Status)
	require.NotContains(t, status.Components, "queue")

	cache.AddGetter(ctx, componentGetter("queue", nvcatypes.HealthStatusUnhealthy, nvcatypes.StatusLevelError))

	// Deliberately no refresh here, matching the order in Agent.Start.
	readiness := cache.GetStatusForLevel(nvcatypes.StatusLevelWarn)
	assert.Contains(t, readiness.Components, "queue",
		"readiness must see a late-registered component without waiting for a refresh")
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, readiness.Status,
		"readiness must not report healthy before the queue has been polled")
}

// TestAddGetterGatesReadinessWhenRefreshIsThrottled is why the status has to be
// folded in by AddGetter itself rather than by refreshing after the call.
// minRefreshWait makes RefreshStatus hand back the cached aggregate without
// gathering, so a refresh issued right after the startup gate can be a no-op in
// any deployment that sets MinHealthcheckRefreshWait.
func TestAddGetterGatesReadinessWhenRefreshIsThrottled(t *testing.T) {
	ctx := context.Background()
	cache := NewBackendStatusCache(time.Hour,
		componentGetter("gpu", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError))

	_, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)

	cache.AddGetter(ctx, componentGetter("queue", nvcatypes.HealthStatusUnhealthy, nvcatypes.StatusLevelError))

	// Throttled, so this returns the cache instead of querying the getters.
	refreshed, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, refreshed.Status,
		"a throttled refresh must still reflect the registered component")

	readiness := cache.GetStatusForLevel(nvcatypes.StatusLevelWarn)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, readiness.Status)
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
			cache.AddGetter(context.Background(), componentGetter("queue", nvcatypes.HealthStatusUnhealthy, tc.level))

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
			cache.AddGetter(context.Background(), componentGetter("late", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError))
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

	// The race detector alone would pass against a cache that silently dropped
	// every late registration, so assert the outcome too.
	assert.Contains(t, cache.GetStatus().Components, "late",
		"late registrations must survive concurrent refreshes, not just avoid racing")
}

// blockingGetter holds a refresh inside its gather phase until released, so a
// registration can be placed in the exact window between the refresh
// snapshotting the getter set and storing its result.
type blockingGetter struct {
	name    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *blockingGetter) GetComponentStatus(context.Context) (nvcatypes.AgentHealth, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	return nvcatypes.AgentHealth{
		Components: map[string]nvcatypes.ComponentHealth{
			g.name: {Status: nvcatypes.HealthStatusHealthy, StatusLevel: nvcatypes.StatusLevelError},
		},
	}, nil
}

// TestAddGetterSurvivesConcurrentRefreshStore pins the interleaving that makes
// AddGetter's fold pointless. A refresh gathers from the getters it snapshotted
// and then overwrites the cache wholesale, so a registration that lands in that
// gap is folded in and immediately thrown away, leaving readiness answering
// without the component until the next tick. Deterministic rather than timing
// dependent, because this window is narrow enough that a stress loop would
// mostly miss it.
func TestAddGetterSurvivesConcurrentRefreshStore(t *testing.T) {
	ctx := context.Background()
	blocker := &blockingGetter{
		name:    "gpu",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	cache := NewBackendStatusCache(0, blocker)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := cache.RefreshStatus(ctx); err != nil {
			t.Errorf("refresh failed: %v", err)
		}
	}()

	// The refresh has snapshotted the getter set and is gathering.
	<-blocker.entered

	cache.AddGetter(ctx, componentGetter("queue", nvcatypes.HealthStatusUnhealthy, nvcatypes.StatusLevelError))
	require.Contains(t, cache.GetStatus().Components, "queue",
		"AddGetter must fold the component in immediately")

	close(blocker.release)
	<-done

	status := cache.GetStatus()
	assert.Contains(t, status.Components, "queue",
		"a refresh in flight must not discard a component registered while it gathered")
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, status.Status,
		"and readiness must still be gated by it")
}

// TestRefreshReplacesComponentsItQueried is the counterweight: preserving late
// registrations must not turn the cache append-only. A component the refresh did
// query is authoritative and has to be allowed to go healthy again, or a single
// transient failure would pin the backend unready for good.
func TestRefreshReplacesComponentsItQueried(t *testing.T) {
	ctx := context.Background()
	unhealthy := componentGetter("gpu", nvcatypes.HealthStatusUnhealthy, nvcatypes.StatusLevelError)
	cache := NewBackendStatusCache(0, unhealthy)

	status, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, nvcatypes.HealthStatusUnhealthy, status.Status)

	// Registration bumps the generation, so the next refresh takes the
	// preserving path; the queried component must still be replaced.
	cache.AddGetter(ctx, componentGetter("queue", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError))
	cache.getters[0] = componentGetter("gpu", nvcatypes.HealthStatusHealthy, nvcatypes.StatusLevelError)

	status, err = cache.RefreshStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, status.Status,
		"a recovered component must be able to clear the aggregate")
}

// slowFirstRefreshGetter returns healthy on its first call and blocks there
// until released, then returns unhealthy immediately on later calls. That lets a
// test interleave two refreshes deterministically instead of racing them.
type slowFirstRefreshGetter struct {
	mu      sync.Mutex
	calls   int
	entered chan int
	release chan struct{}
}

func (g *slowFirstRefreshGetter) GetComponentStatus(context.Context) (nvcatypes.AgentHealth, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()

	g.entered <- n

	status := nvcatypes.HealthStatusUnhealthy
	if n == 1 {
		status = nvcatypes.HealthStatusHealthy
		<-g.release
	}
	return nvcatypes.AgentHealth{
		Components: map[string]nvcatypes.ComponentHealth{
			"queue": {Status: status, StatusLevel: nvcatypes.StatusLevelError},
		},
	}, nil
}

// TestOlderRefreshDoesNotOverwriteNewerResult covers refresh-against-refresh
// ordering. SyncQueues and the heartbeat both call RefreshStatus from separate
// workers and nothing serialises them, so a refresh that started earlier can
// finish later. If its result still wins, /healthz goes back to 200 after a
// newer refresh has already seen the queue broken — which would quietly undo
// the readiness gate this PR exists to add.
func TestOlderRefreshDoesNotOverwriteNewerResult(t *testing.T) {
	ctx := context.Background()
	getter := &slowFirstRefreshGetter{
		entered: make(chan int, 2),
		release: make(chan struct{}),
	}
	cache := NewBackendStatusCache(0, getter)

	// Refresh 1 starts first, so it takes the lower sequence, and parks inside
	// the getter holding a healthy result.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = cache.RefreshStatus(ctx)
	}()
	require.Equal(t, 1, <-getter.entered, "refresh 1 must be the first to gather")

	// Refresh 2 starts second, finishes first, and publishes unhealthy.
	_, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, <-getter.entered)
	require.Equal(t, nvcatypes.HealthStatusUnhealthy,
		cache.GetStatus().Status, "refresh 2 must have published unhealthy")

	// Now let the older refresh finish. Its healthy result is stale.
	close(getter.release)
	<-firstDone

	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, cache.GetStatus().Status,
		"an older refresh finishing late must not revert readiness to healthy")
}
