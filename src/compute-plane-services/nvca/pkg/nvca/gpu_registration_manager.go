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

package nvca

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const gracefulNoGPURegistrationComponentName = "icmsregistration"

type gpuRegistrationMonitor interface {
	HasGPUs() bool
	SetOnGPUStateChange(GPUStateChangeCallback)
	Start(context.Context)
	GetComponentStatus(context.Context) (types.AgentHealth, error)
}

type gpuRegistrationQueue interface {
	Pause()
	Resume()
}

type gpuRegistrationManager struct {
	operationGate        contextAwareRegistrationGate
	stateMu              sync.Mutex
	monitor              gpuRegistrationMonitor
	queueManager         gpuRegistrationQueue
	ready                atomic.Bool
	generation           atomic.Uint64
	registrationRequests chan struct{}
	retryInterval        time.Duration
	register             func(context.Context) error
}

type contextAwareRegistrationGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *contextAwareRegistrationGate) lock(ctx context.Context) error {
	g.once.Do(func() {
		g.token = make(chan struct{}, 1)
		g.token <- struct{}{}
	})

	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		if err := ctx.Err(); err != nil {
			g.token <- struct{}{}
			return err
		}
		return nil
	}
}

func (g *contextAwareRegistrationGate) unlock() {
	g.token <- struct{}{}
}

func (m *gpuRegistrationManager) withRegistrationOperation(ctx context.Context, operation func() error) error {
	if err := m.operationGate.lock(ctx); err != nil {
		return err
	}
	defer m.operationGate.unlock()
	return operation()
}

func (m *gpuRegistrationManager) configureGracefulNoGPU(
	monitor gpuRegistrationMonitor,
	initiallyReady bool,
	retryInterval time.Duration,
	register func(context.Context) error,
) {
	m.monitor = monitor
	m.ready.Store(initiallyReady)
	m.retryInterval = retryInterval
	m.register = register
	m.registrationRequests = make(chan struct{}, 1)
}

func (m *gpuRegistrationManager) setQueueManager(queueManager gpuRegistrationQueue) {
	m.queueManager = queueManager
}

func (m *gpuRegistrationManager) start(ctx context.Context) {
	go m.run(ctx)
	m.monitor.SetOnGPUStateChange(m.handleGPUStateChange)
	m.monitor.Start(ctx)
}

func (m *gpuRegistrationManager) enabled() bool {
	return m.monitor != nil
}

func (m *gpuRegistrationManager) hasGPUs() bool {
	return m.monitor != nil && m.monitor.HasGPUs()
}

func (m *gpuRegistrationManager) getRegistrationStatus(context.Context) (types.AgentHealth, error) {
	component := types.ComponentHealth{
		Status:      types.HealthStatusHealthy,
		StatusLevel: types.StatusLevelError,
	}
	if !m.ready.Load() {
		component.Status = types.HealthStatusUnhealthy
		component.Errors = []string{"waiting for successful ICMS registration after GPU discovery"}
	}

	return types.AgentHealth{
		Components: map[string]types.ComponentHealth{
			gracefulNoGPURegistrationComponentName: component,
		},
	}, nil
}

func (m *gpuRegistrationManager) handleGPUStateChange(ctx context.Context, hasGPUs bool) {
	log := core.GetLogger(ctx)
	m.generation.Add(1)

	m.stateMu.Lock()
	m.ready.Store(false)
	if m.queueManager != nil {
		m.queueManager.Pause()
	}
	m.stateMu.Unlock()

	if !hasGPUs {
		log.Warn("GPUs no longer available - pausing queue manager")
		return
	}

	log.Info("GPUs detected - waiting for successful ICMS registration before resuming queue manager")
	select {
	case m.registrationRequests <- struct{}{}:
	default:
	}
}

func (m *gpuRegistrationManager) run(ctx context.Context) {
	retryInterval := m.retryInterval
	if retryInterval <= 0 {
		retryInterval = DefaultGPUPollInterval
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.registrationRequests:
		}

		for !m.ready.Load() && m.hasGPUs() {
			if !m.tryRegistration(ctx) {
				break
			}

			retryTimer := time.NewTimer(retryInterval)
			select {
			case <-ctx.Done():
				retryTimer.Stop()
				return
			case <-m.registrationRequests:
				retryTimer.Stop()
			case <-retryTimer.C:
			}
		}
	}
}

// tryRegistration returns true when registration should be retried.
func (m *gpuRegistrationManager) tryRegistration(ctx context.Context) bool {
	log := core.GetLogger(ctx)
	shouldRetry := false
	err := m.withRegistrationOperation(ctx, func() error {
		generation := m.generation.Load()
		if m.monitor == nil || !m.monitor.HasGPUs() {
			return nil
		}

		log.Info("Registering with ICMS after GPUs became available")
		if err := m.register(ctx); err != nil {
			log.WithError(err).Warn("Failed to register with ICMS after GPUs became available; will retry")
			shouldRetry = m.monitor.HasGPUs()
			return nil
		}

		m.stateMu.Lock()
		defer m.stateMu.Unlock()
		if !m.monitor.HasGPUs() || generation != m.generation.Load() {
			log.Warn("GPU availability changed during ICMS registration - keeping queue manager paused")
			shouldRetry = m.monitor.HasGPUs()
			return nil
		}

		m.ready.Store(true)
		if m.queueManager != nil {
			m.queueManager.Resume()
		}
		log.Info("Successfully registered with ICMS after GPUs became available")
		return nil
	})
	return err == nil && shouldRetry
}
