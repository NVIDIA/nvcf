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
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// flakyGetter reports one healthy component until told to fail, so a test can
// move a getter in and out of failure across successive refreshes.
type flakyGetter struct {
	mu        sync.Mutex
	component string
	failWith  error
	calls     int
}

func (g *flakyGetter) GetComponentStatus(context.Context) (nvcatypes.AgentHealth, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.failWith != nil {
		return nvcatypes.AgentHealth{}, g.failWith
	}
	return nvcatypes.AgentHealth{
		Components: map[string]nvcatypes.ComponentHealth{
			g.component: {
				Status:      nvcatypes.HealthStatusHealthy,
				StatusLevel: nvcatypes.StatusLevelError,
			},
		},
	}, nil
}

func (g *flakyGetter) setFailure(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failWith = err
}

func (g *flakyGetter) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// TestSustainedGetterErrorStopsReportingHealthy is the regression guard for the
// error half of nvcf#1590.
//
// A getter error set the aggregate Status and dropped the getter's components.
// GetStatusForLevel then rebuilt Status from Components alone and ignored the
// stored aggregate, so the component simply disappeared from the payload and
// readiness answered healthy for a component that had no idea how it was doing.
// The invisibility is the bug: a component that cannot report must not be able
// to vanish.
func TestSustainedGetterErrorStopsReportingHealthy(t *testing.T) {
	ctx := context.Background()
	g := &flakyGetter{component: "queue"}
	cache := NewBackendStatusCache(0, g)

	_, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, nvcatypes.HealthStatusHealthy, cache.GetStatus().Status)

	g.setFailure(errors.New("api server unreachable"))

	// Within the tolerance the last known status is served, so one blip does not
	// move readiness. See refreshTolerance for why that latitude is deliberate.
	for i := 1; i <= refreshTolerance; i++ {
		_, err := cache.RefreshStatus(ctx)
		require.Error(t, err, "refresh %d must still surface the getter error", i)

		ah := cache.GetStatus()
		require.Equal(t, nvcatypes.HealthStatusHealthy, ah.Status,
			"refresh %d is inside the tolerance, so readiness must not move", i)
		require.Contains(t, ah.Components, "queue",
			"refresh %d: the component must stay named while its last status is served", i)
	}

	// Past the tolerance it is reported, rather than quietly leaving the payload.
	_, err = cache.RefreshStatus(ctx)
	require.Error(t, err)

	ah := cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, ah.Status,
		"a component that cannot report must stop readiness answering healthy")

	ch, ok := ah.Components["queue"]
	require.True(t, ok, "the component must be named in the payload, not dropped from it")
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, ch.Status)
	assert.Equal(t, nvcatypes.StatusLevelError, ch.StatusLevel,
		"must be Error level or GetStatusForLevel puts it in the payload and gates nothing")
	assert.NotEmpty(t, ch.Errors, "should say why the status is unavailable")
}

// TestSingleGetterErrorDoesNotMoveReadiness states what the tolerance is for as
// a property rather than in terms of its own constant: one failed refresh must
// not change what readiness reports.
//
// Deliberately written with a literal single refresh. The loops in the tests
// either side are bounded by refreshTolerance, so setting that constant to zero
// makes them empty and they pass having asserted nothing — this is the one that
// fails instead. Zero tolerance is the version of this fix that rebuilds the
// mass-restart problem in readiness, so it needs a test that notices.
func TestSingleGetterErrorDoesNotMoveReadiness(t *testing.T) {
	ctx := context.Background()
	g := &flakyGetter{component: "queue"}
	cache := NewBackendStatusCache(0, g)

	_, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, nvcatypes.HealthStatusHealthy, cache.GetStatus().Status)

	g.setFailure(errors.New("api server unreachable"))

	_, err = cache.RefreshStatus(ctx)
	require.Error(t, err, "the getter error must still reach the caller")

	ah := cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusHealthy, ah.Status,
		"a single failed refresh must not take the backend out of service: these "+
			"getters share an API server and would fail together across the fleet")

	ch, ok := ah.Components["queue"]
	require.True(t, ok, "the component must keep its place in the payload")
	assert.Equal(t, nvcatypes.HealthStatusHealthy, ch.Status,
		"and must still report the status it last actually had")
}

// TestGetterErrorToleranceResetsOnRecovery stops the tolerance from being a
// budget that a flaky dependency slowly exhausts. Failures have to be
// consecutive to count, otherwise an API server that blips every few minutes
// would eventually take the backend out of service anyway, which is the outcome
// the tolerance exists to prevent.
func TestGetterErrorToleranceResetsOnRecovery(t *testing.T) {
	ctx := context.Background()
	g := &flakyGetter{component: "queue"}
	cache := NewBackendStatusCache(0, g)

	_, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)

	for round := 1; round <= 3; round++ {
		g.setFailure(errors.New("api server unreachable"))
		for i := 1; i <= refreshTolerance; i++ {
			_, err := cache.RefreshStatus(ctx)
			require.Error(t, err)
			require.Equal(t, nvcatypes.HealthStatusHealthy, cache.GetStatus().Status,
				"round %d refresh %d: still inside the tolerance", round, i)
		}

		g.setFailure(nil)
		_, err := cache.RefreshStatus(ctx)
		require.NoError(t, err)
		require.Equal(t, nvcatypes.HealthStatusHealthy, cache.GetStatus().Status,
			"round %d: a recovered getter must clear its failure count", round)
	}
}

// TestGetterFailingFromTheStartIsNamedByType covers the getter that has never
// successfully reported, so there is no component name to blame. Something has
// to appear under Components for readiness to gate on, because an empty map is
// exactly what let the failure go unnoticed.
func TestGetterFailingFromTheStartIsNamedByType(t *testing.T) {
	ctx := context.Background()
	g := &flakyGetter{component: "queue", failWith: errors.New("never came up")}
	cache := NewBackendStatusCache(0, g)

	for i := 0; i <= refreshTolerance; i++ {
		_, err := cache.RefreshStatus(ctx)
		require.Error(t, err)
	}

	ah := cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, ah.Status,
		"a getter that has never reported must not be silently absent")

	ch, ok := ah.Components[fmt.Sprintf("%T", g)]
	require.True(t, ok,
		"a getter with no known component name should be named by its type, got %v",
		ah.Components)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, ch.Status)
	assert.Equal(t, nvcatypes.StatusLevelError, ch.StatusLevel)
}

// TestOneFailingGetterDoesNotHideTheHealthyOnes keeps the blast radius of the
// change above to the getter that actually failed. The readiness aggregate is
// shared, so folding a failure in must not stop the other components being
// refreshed or reported — otherwise one broken dependency would blind the probe
// to everything else.
func TestOneFailingGetterDoesNotHideTheHealthyOnes(t *testing.T) {
	ctx := context.Background()
	broken := &flakyGetter{component: "queue"}
	fine := &flakyGetter{component: "backendk8s"}
	cache := NewBackendStatusCache(0, broken, fine)

	_, err := cache.RefreshStatus(ctx)
	require.NoError(t, err)

	broken.setFailure(errors.New("api server unreachable"))
	callsBefore := fine.callCount()

	for i := 0; i <= refreshTolerance; i++ {
		_, err := cache.RefreshStatus(ctx)
		require.Error(t, err)
	}

	ah := cache.GetStatus()
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, ah.Status)

	assert.Greater(t, fine.callCount(), callsBefore,
		"the healthy getter must keep being queried while another is failing")

	healthy, ok := ah.Components["backendk8s"]
	require.True(t, ok, "the healthy component must still be reported, got %v", ah.Components)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, healthy.Status,
		"one getter failing must not relabel a component it knows nothing about")

	failed, ok := ah.Components["queue"]
	require.True(t, ok)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, failed.Status)
}
