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

// refreshTolerance is how many consecutive failed refreshes of one getter may
// pass before readiness reports its components unhealthy.
//
// Deliberately not zero. Most of these getters read through shared informers
// against the same API server, so they fail together, and reporting on the
// first error would take every backend in the fleet out of service at once on a
// single blip — the operator writes agentStatus Unknown on a non-200, so
// nothing self-corrects quickly. That is the mass-restart shape this PR removes
// from liveness, and it would be no better arriving through readiness.
// Tolerating a few refreshes serves the last known status through a blip and
// still reports a sustained failure.
//
// Counted in refreshes rather than elapsed time for the same reason
// maxCyclesWithoutPoll is: the cadence here follows SyncQueueInterval and
// MinHealthcheckRefreshWait, both operator-tunable with no upper bound, so any
// fixed duration is wrong for some valid configuration.
const refreshTolerance = 3

// getterHistory carries what a getter reported last and how long it has been
// failing, so a refresh can tell a blip from a sustained failure.
//
// Index-aligned with BackendStatusCache.getters, which is only ever appended
// to, so an index stays valid for the life of the process. Guarded by gmu.
type getterHistory struct {
	consecutiveFails int
	components       []string
}

type BackendStatusCache struct {
	backendStatus atomic.Value

	// gmu guards getters, which can be appended to after construction via
	// AddGetter while RefreshStatusForLevel reads it from the refresh loop. It
	// also serialises the two writers of backendStatus against each other; see
	// storeRefreshed.
	gmu     sync.RWMutex
	getters []ComponentStatusGetter
	// history is index-aligned with getters. See getterHistory.
	history []getterHistory
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
		history:        make([]getterHistory, len(getters)),
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
	// Seeded with whatever it just reported, so a later failure knows which
	// components to keep serving and then to blame.
	c.history = append(c.history, getterHistory{components: componentNames(ah.Components)})
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

// publishRefresh folds one refresh's results into the cache and reports getters
// that could not answer.
//
// A getter error used to set the aggregate Status and drop the getter's
// components. GetStatusForLevel then rebuilt Status from Components alone and
// ignored the stored aggregate, so the names simply vanished from the payload
// and readiness answered 200 with a component that had no idea how it was
// doing. Same shape as the stale-poll half of nvcf#1590, reached through the
// error path: a component that cannot report must not be able to disappear.
//
// Failures are held for refreshTolerance refreshes before they change what
// readiness reports, serving the getter's last known components in the
// meantime. See refreshTolerance for why that latitude is not optional.
//
// It also publishes without discarding a component that was registered while
// the refresh was in flight.
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
func (c *BackendStatusCache) publishRefresh(
	ctx context.Context,
	results []statusResult,
	snapGen, seq uint64,
) {
	c.gmu.Lock()
	defer c.gmu.Unlock()

	if seq < c.lastStoredSeq {
		// A refresh that started after this one has already published. Its view
		// is newer, so discard this result rather than reverting to it.
		return
	}
	c.lastStoredSeq = seq

	cur, _ := c.backendStatus.Load().(nvcatypes.AgentHealth)
	ah := nvcatypes.AgentHealth{
		Status:     nvcatypes.HealthStatusHealthy,
		GPUUsage:   map[nvcatypes.GPUName]nvcatypes.GPUResource{},
		Components: map[string]nvcatypes.ComponentHealth{},
	}

	for _, res := range results {
		h := &c.history[res.idx]

		if res.err == nil {
			h.consecutiveFails = 0
			h.components = componentNames(res.agentHealth.Components)
			for gk, gv := range res.agentHealth.GPUUsage {
				ah.GPUUsage[gk] = gv
			}
			addComponents(&ah, res.agentHealth.Components)
			continue
		}

		h.consecutiveFails++
		if h.consecutiveFails <= refreshTolerance {
			// Serve what this getter last reported. GPU usage is deliberately
			// not carried: a stale capacity number is worse than none, and it
			// only feeds reporting rather than the readiness decision.
			for _, name := range h.components {
				if ch, ok := cur.Components[name]; ok {
					addComponents(&ah, map[string]nvcatypes.ComponentHealth{name: ch})
				}
			}
			continue
		}

		core.GetLogger(ctx).WithError(res.err).
			WithField("getter", res.name).
			WithField("consecutive_failures", h.consecutiveFails).
			Error("Component status unavailable past the refresh tolerance, reporting unhealthy")
		addComponents(&ah, unavailableComponents(res.name, h.components, h.consecutiveFails, res.err))
	}

	if c.gen != snapGen {
		for ck, cv := range cur.Components {
			// Only components this refresh knew nothing about. Anything it did
			// query is authoritative and must be allowed to go healthy again.
			if _, queried := ah.Components[ck]; queried {
				continue
			}
			ah.Components[ck] = cv
			if cv.Status == nvcatypes.HealthStatusUnhealthy {
				ah.Status = cv.Status
			}
		}
	}

	c.backendStatus.Store(ah)
}

// addComponents merges components into the aggregate, letting any unhealthy one
// take the aggregate with it.
func addComponents(ah *nvcatypes.AgentHealth, cs map[string]nvcatypes.ComponentHealth) {
	for name, ch := range cs {
		ah.Components[name] = ch
		if ch.Status == nvcatypes.HealthStatusUnhealthy {
			ah.Status = nvcatypes.HealthStatusUnhealthy
		}
	}
}

// componentNames lists the component keys a getter reported, for when a later
// call fails and only the names are still needed. Sorted so the payload and the
// logs stay stable across refreshes.
func componentNames(cs map[string]nvcatypes.ComponentHealth) []string {
	if len(cs) == 0 {
		return nil
	}
	names := make([]string, 0, len(cs))
	for name := range cs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// unavailableComponents reports a getter that has failed past the tolerance.
//
// The names it last reported are reused, so the payload keeps blaming the same
// components across the transition instead of renaming them mid-outage. A getter
// that has never reported anything is named by its Go type, because something
// has to appear under Components for readiness to gate on it: an empty map is
// precisely what let the failure go unnoticed.
func unavailableComponents(
	getterName string,
	names []string,
	fails int,
	err error,
) map[string]nvcatypes.ComponentHealth {
	if len(names) == 0 {
		names = []string{getterName}
	}
	out := make(map[string]nvcatypes.ComponentHealth, len(names))
	for _, name := range names {
		out[name] = nvcatypes.ComponentHealth{
			Status: nvcatypes.HealthStatusUnhealthy,
			// StatusLevelError, not Warn: GetStatusForLevel only lets components
			// strictly below Warn set the aggregate, so a Warn entry would show
			// up in the payload and gate nothing.
			StatusLevel: nvcatypes.StatusLevelError,
			Errors: []string{
				fmt.Sprintf("status unavailable for %d consecutive refreshes: %v", fails, err),
			},
		}
	}
	return out
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
	// idx locates the getter in this refresh's snapshot, so its history can be
	// updated and its error placed in a stable position in the aggregate.
	idx int
	// name identifies the getter in logs, and in the payload when it has never
	// reported a component to be named by.
	name        string
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
	for idx, getter := range getters {
		go func(idx int, getter ComponentStatusGetter) {
			ah, err := getter.GetComponentStatus(ctx)
			if err != nil && !nvcaerrors.IsNotExist(err) {
				log.WithError(err).Error("Failed to retrieve component status")
				ah.Status = nvcatypes.HealthStatusUnhealthy
			} else if nvcaerrors.IsNotExist(err) {
				log.WithError(err).Debug("ignoring NotExist error")
				err = nil
			}
			results <- statusResult{
				idx:         idx,
				name:        fmt.Sprintf("%T", getter),
				agentHealth: ah,
				err:         err,
			}
		}(idx, getter)
	}

	gathered := make([]statusResult, 0, len(getters))
	errs := make([]error, len(getters))
	for range getters {
		res := <-results
		errs[res.idx] = res.err
		gathered = append(gathered, res)
	}

	// Create a new health status instance and update the cache
	c.publishRefresh(ctx, gathered, snapGen, seq)

	if err := utilerror.NewAggregate(errs); err != nil {
		return nvcatypes.AgentHealth{}, err
	}
	return c.GetStatusForLevel(level), nil
}
