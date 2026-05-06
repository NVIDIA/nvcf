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
	getters       []ComponentStatusGetter

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

	results := make(chan statusResult)
	for _, getter := range c.getters {
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
	i := len(c.getters)
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
	c.backendStatus.Store(allAH)

	if err := utilerror.NewAggregate(errs); err != nil {
		return nvcatypes.AgentHealth{}, err
	}
	return c.GetStatusForLevel(level), nil
}
