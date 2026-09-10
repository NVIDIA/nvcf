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
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	utilerror "k8s.io/apimachinery/pkg/util/errors"

	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type StatusRefresher interface {
	RefreshStatus(ctx context.Context) (nvcatypes.AgentHealth, error)
}

type StatusGetter interface {
	StatusRefresher
	GetStatus() nvcatypes.AgentHealth
}

type StatusCache interface {
	StatusGetter
	GetStatusForLevel(level nvcatypes.StatusLevel) nvcatypes.AgentHealth
	// AddGetter registers a component whose construction happens after this
	// cache is built. See BackendStatusCache.AddGetter.
	AddGetter(ctx context.Context, g ComponentStatusGetter)
}

type ComponentStatusGetter interface {
	GetComponentStatus(context.Context) (nvcatypes.AgentHealth, error)
}

type GetComponentStatusFunc func(context.Context) (nvcatypes.AgentHealth, error)

func (f GetComponentStatusFunc) GetComponentStatus(ctx context.Context) (nvcatypes.AgentHealth, error) {
	return f(ctx)
}

type BackendStatusCache struct {
	backendStatus atomic.Value

	// gmu guards getters, which can be appended to after construction via
	// AddGetter while RefreshStatusForLevel reads it from the refresh loop. It
	// also serialises the two writers of backendStatus against each other; see
	// storeRefreshed.
	gmu     sync.RWMutex
	getters []ComponentStatusGetter
	// gen counts registrations, so a refresh can tell that the getter set
	// changed while it was gathering and avoid clobbering the new component.
	gen uint64
	// refreshSeq issues an increasing ticket per refresh and lastStoredSeq
	// records the newest one published, so a slow refresh cannot overwrite a
	// newer result with its own older one. Both guarded by gmu.
	refreshSeq    uint64
	lastStoredSeq uint64

	// If set, wait at least this long before the next refresh.
	// This prevents chatty component queries.
	minRefreshWait time.Duration
	lastRefresh    time.Time
	// For testing.
	nowFunc func() time.Time
}

var _ StatusGetter = (*BackendStatusCache)(nil)

func NewBackendStatusCache(
	minRefreshWait time.Duration,
	getters ...ComponentStatusGetter,
) *BackendStatusCache {
	cache := &BackendStatusCache{
		getters:        getters,
		minRefreshWait: minRefreshWait,
		nowFunc:        time.Now,
	}

	// Store an empty value for the initial value
	cache.backendStatus.Store(nvcatypes.AgentHealth{
		Status: nvcatypes.HealthStatusHealthy,
	})

	return cache
}

// AddGetter registers a component after construction, mirroring the liveness
// getter's AddChecker. It exists because some components are not constructed
// until after the startup health gate has already run against this cache, so
// they cannot be passed to NewBackendStatusCache without deadlocking startup
// on a component that is not-ready by construction.
//
// The component's current status is folded into the cached aggregate before
// returning. That fold is load-bearing rather than an optimization:
// GetStatusForLevel serves readiness out of the cache without refreshing, and
// the only routine refresh is the one at the top of QueueManager.SyncQueues, so
// appending to getters alone would leave /healthz answering from an aggregate
// that predates the component and reporting healthy without it until the next
// tick.
func (c *BackendStatusCache) AddGetter(ctx context.Context, g ComponentStatusGetter) {
	// Queried before taking gmu because getters may block on I/O, and because
	// RefreshStatusForLevel deliberately calls them outside the lock too.
	ah, err := g.GetComponentStatus(ctx)
	if err != nil && !nvcaerrors.IsNotExist(err) {
		core.GetLogger(ctx).WithError(err).
			Error("Failed to retrieve status of newly registered component")
		ah.Status = nvcatypes.HealthStatusUnhealthy
	}

	c.gmu.Lock()
	defer c.gmu.Unlock()
	c.getters = append(c.getters, g)
	c.gen++

	if len(ah.Components) == 0 {
		// Nothing nameable to fold. Reached when a getter errors, or returns an
		// empty payload; the error is logged above. The component is registered
		// either way, so the next refresh reports it properly — this only leaves
		// it missing from readiness for one refresh interval, which is the
		// pre-existing behaviour rather than a new hole.
		return
	}

	cur := c.backendStatus.Load().(nvcatypes.AgentHealth)
	merged := nvcatypes.AgentHealth{
		Status:     cur.Status,
		GPUUsage:   cur.GPUUsage,
		Components: make(map[string]nvcatypes.ComponentHealth, len(cur.Components)+len(ah.Components)),
	}
	for ck, cv := range cur.Components {
		merged.Components[ck] = cv
	}
	for ck, cv := range ah.Components {
		merged.Components[ck] = cv
		if cv.Status == nvcatypes.HealthStatusUnhealthy {
			merged.Status = cv.Status
		}
	}
	c.backendStatus.Store(merged)
}

// snapshotGetters returns a stable copy so a concurrent AddGetter cannot change
// the set between the fan-out and the result count that terminates the gather.
// snapshotGetters takes the getter set for one refresh, along with the
// registration generation and this refresh's ordering ticket.
func (c *BackendStatusCache) snapshotGetters() ([]ComponentStatusGetter, uint64, uint64) {
	c.gmu.Lock()
	defer c.gmu.Unlock()
	c.refreshSeq++
	return slices.Clone(c.getters), c.gen, c.refreshSeq
}

// storeRefreshed publishes a refresh result without discarding a component that
// was registered while the refresh was in flight.
//
// A refresh gathers from the getters it snapshotted and then overwrites the
// cache wholesale. An AddGetter that lands in that gap has already folded its
// component in, and the overwrite would drop it again, leaving readiness
// answering without that component until the next tick — which is the window
// AddGetter's fold exists to close. Comparing the generation under the same lock
// AddGetter writes under makes the check and the store atomic with respect to
// it; a plain read-then-store outside the lock would just move the race.
// Refreshes are also ordered against each other. Nothing serialises them —
// SyncQueues and the heartbeat both call RefreshStatus from separate workers —
// and the getter fan-out means a slow one can finish after a refresh that
// started later. Without the sequence check that older result overwrites the
// newer one, so /healthz can go back to 200 after a refresh has already seen the
// queue broken. Ordering only the store keeps the fan-out concurrent; taking the
// lock across the gather would let one slow getter block every refresh caller.
func (c *BackendStatusCache) storeRefreshed(ah nvcatypes.AgentHealth, snapGen, seq uint64) {
	c.gmu.Lock()
	defer c.gmu.Unlock()

	if seq < c.lastStoredSeq {
		// A refresh that started after this one has already published. Its view
		// is newer, so discard this result rather than reverting to it.
		return
	}
	c.lastStoredSeq = seq

	if c.gen != snapGen {
		cur, _ := c.backendStatus.Load().(nvcatypes.AgentHealth)
		for ck, cv := range cur.Components {
			// Only components this refresh knew nothing about. Anything it did
			// query is authoritative and must be allowed to go healthy again.
			if _, queried := ah.Components[ck]; queried {
				continue
			}
			if ah.Components == nil {
				ah.Components = map[string]nvcatypes.ComponentHealth{}
			}
			ah.Components[ck] = cv
			if cv.Status == nvcatypes.HealthStatusUnhealthy {
				ah.Status = cv.Status
			}
		}
	}

	c.backendStatus.Store(ah)
}

// WaitForHealthSuccess blocks the current thread until the AgentHealth is completely healthy
func WaitForHealthyStatus(ctx context.Context, interval, timeout time.Duration, statusRefresher StatusRefresher) error {
	log := core.GetLogger(ctx)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	healthTickerCh := core.NewTickerStream().WithImmediate(true).WithInterval(interval).Start(ctx)
	for {
		select {
		case <-healthTickerCh:
			// Refresh the health status cache, and then check the health status
			agentHealth, err := statusRefresher.RefreshStatus(ctx)
			if err != nil {
				log.WithError(err).Errorf("failed to retrieve the current NVCA status. Retrying in %s", interval)
			} else if agentHealth.Status == nvcatypes.HealthStatusUnhealthy {
				failedLogFields := logrus.Fields{}
				// Aggregate and print out the unhealthy components
				for compName, v := range agentHealth.Components {
					failedLogFields[fmt.Sprintf("%s_errors", compName)] = v.Errors
				}
				log.WithFields(failedLogFields).
					WithField("nvca_health", agentHealth.Status).
					Errorf("NVCA status is %s, will retry in %s", agentHealth.Status, interval)
			} else {
				// This is healthy log check is complete and move on
				log.Infof("NVCA status is %s. Startup will continue.", agentHealth.Status)
				return nil
			}
		case <-ctx.Done():
			log.Error("NVCA did not become healthy within timeout")
			return ctx.Err()
		}
	}
}

// GetStatus retrieves the cached status of the health status request
func (c *BackendStatusCache) GetStatus() nvcatypes.AgentHealth {
	return c.GetStatusForLevel(nvcatypes.StatusLevelError)
}

// GetStatusForLevel retrieves the cached status of the health status request for a specific level
func (c *BackendStatusCache) GetStatusForLevel(level nvcatypes.StatusLevel) nvcatypes.AgentHealth {
	ah := c.backendStatus.Load().(nvcatypes.AgentHealth)
	ahCopy := nvcatypes.AgentHealth{
		Status:     nvcatypes.HealthStatusHealthy,
		GPUUsage:   ah.GPUUsage,
		Components: map[string]nvcatypes.ComponentHealth{},
	}
	// Re-evaluate the status for the given level if the component is less than or equal to the given level
	for cmpName, component := range ah.Components {
		if component.StatusLevel <= level {
			// Copy status if unhealthy, otherwise ignore since we can assume healthy
			if component.Status == nvcatypes.HealthStatusUnhealthy &&
				component.StatusLevel < nvcatypes.StatusLevelWarn {
				ahCopy.Status = nvcatypes.HealthStatusUnhealthy
			}
			ahCopy.Components[cmpName] = component
		}
	}
	return ahCopy
}

type statusResult struct {
	agentHealth nvcatypes.AgentHealth
	err         error
}

// RefreshStatus refreshes the cached status for all status getters in the cache.
func (c *BackendStatusCache) RefreshStatus(ctx context.Context) (nvcatypes.AgentHealth, error) {
	return c.RefreshStatusForLevel(ctx, nvcatypes.StatusLevelError)
}

// RefreshStatusForLevel refreshes the cached status for all status getters in the cache.
func (c *BackendStatusCache) RefreshStatusForLevel(ctx context.Context, level nvcatypes.StatusLevel) (nvcatypes.AgentHealth, error) {
	log := core.GetLogger(ctx)

	if c.minRefreshWait != 0 {
		if !c.lastRefresh.IsZero() &&
			c.lastRefresh.Add(1*c.minRefreshWait).After(c.nowFunc()) {
			return c.GetStatusForLevel(level), nil
		}
		c.lastRefresh = c.nowFunc()
	}

	getters, snapGen, seq := c.snapshotGetters()

	results := make(chan statusResult)
	for _, getter := range getters {
		go func(getter ComponentStatusGetter) {
			ah, err := getter.GetComponentStatus(ctx)
			if err != nil && !nvcaerrors.IsNotExist(err) {
				log.WithError(err).Error("Failed to retrieve component status")
				ah.Status = nvcatypes.HealthStatusUnhealthy
			} else if nvcaerrors.IsNotExist(err) {
				log.WithError(err).Debug("ignoring NotExist error")
				err = nil
			}
			results <- statusResult{agentHealth: ah, err: err}
		}(getter)
	}

	allAH := nvcatypes.AgentHealth{
		Status:     nvcatypes.HealthStatusHealthy,
		GPUUsage:   map[nvcatypes.GPUName]nvcatypes.GPUResource{},
		Components: map[string]nvcatypes.ComponentHealth{},
	}
	i := len(getters)
	errs := make([]error, i)
	for res := range results {
		if res.err != nil {
			errs[i-1] = res.err
			allAH.Status = nvcatypes.HealthStatusUnhealthy
		} else {
			for gk, gv := range res.agentHealth.GPUUsage {
				allAH.GPUUsage[gk] = gv
			}
			for ck, cv := range res.agentHealth.Components {
				allAH.Components[ck] = cv
				if cv.Status == nvcatypes.HealthStatusUnhealthy {
					allAH.Status = cv.Status
				}
			}
		}
		i--
		if i == 0 {
			break
		}
	}
	close(results)

	// Create a new health status instance and update the cache
	c.storeRefreshed(allAH, snapGen, seq)

	if err := utilerror.NewAggregate(errs); err != nil {
		return nvcatypes.AgentHealth{}, err
	}
	return c.GetStatusForLevel(level), nil
}
