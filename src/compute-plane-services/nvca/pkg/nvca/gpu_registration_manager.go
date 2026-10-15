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
const maxGracefulNoGPURegistrationRetryInterval = 5 * time.Minute

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

// gpuRegistrationManager owns GPU-registration readiness, retry, queue, and
// serialization state so all ICMS registration operations share one lifecycle.
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
	newRetryTimer        func(time.Duration) gpuRegistrationRetryTimer
}

// contextAwareRegistrationGate serializes registration operations while
// allowing a queued caller to leave promptly when its context is canceled.
type contextAwareRegistrationGate struct {
	once  sync.Once
	token chan struct{}
}

// gpuRegistrationRetryTimer is the minimal timer surface used by the retry
// loop, allowing deterministic manager-level tests without changing behavior.
type gpuRegistrationRetryTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realGPURegistrationRetryTimer struct {
	timer *time.Timer
}

func (t *realGPURegistrationRetryTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t *realGPURegistrationRetryTimer) Stop() bool {
	return t.timer.Stop()
}

// gpuRegistrationRetryBackoff bounds repeated registration attempts while
// retaining the configured poll interval as the first retry delay.
type gpuRegistrationRetryBackoff struct {
	initial time.Duration
	current time.Duration
	maximum time.Duration
}

// newGPURegistrationRetryBackoff preserves intervals larger than the default
// ceiling so an operator-configured slow retry cadence is never shortened.
func newGPURegistrationRetryBackoff(initial, maximum time.Duration) gpuRegistrationRetryBackoff {
	if maximum < initial {
		maximum = initial
	}
	return gpuRegistrationRetryBackoff{
		initial: initial,
		current: initial,
		maximum: maximum,
	}
}

// next returns the current delay and advances the following delay to its cap.
func (b *gpuRegistrationRetryBackoff) next() time.Duration {
	delay := b.current
	if b.current < b.maximum {
		if b.current > b.maximum/2 {
			b.current = b.maximum
		} else {
			b.current *= 2
		}
	}
	return delay
}

// reset restores the configured first-retry delay after a GPU state change.
func (b *gpuRegistrationRetryBackoff) reset() {
	b.current = b.initial
}

// lock waits for the single registration token or returns when ctx is canceled.
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

// unlock releases the registration token after a successful lock.
func (g *contextAwareRegistrationGate) unlock() {
	g.token <- struct{}{}
}

// withRegistrationOperation protects recovery, renewal, and periodic
// registration from overlapping while honoring cancellation before entry.
func (m *gpuRegistrationManager) withRegistrationOperation(ctx context.Context, operation func() error) error {
	if err := m.operationGate.lock(ctx); err != nil {
		return err
	}
	defer m.operationGate.unlock()
	return operation()
}

// configureGracefulNoGPU initializes state before the monitor and registration
// worker start.
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

// setQueueManager connects queue flow control once startup constructs it.
func (m *gpuRegistrationManager) setQueueManager(queueManager gpuRegistrationQueue) {
	m.queueManager = queueManager
}

// start launches recovery before the monitor can report its first transition.
func (m *gpuRegistrationManager) start(ctx context.Context) {
	go m.run(ctx)
	m.monitor.SetOnGPUStateChange(m.handleGPUStateChange)
	m.monitor.Start(ctx)
}

// enabled reports whether graceful no-GPU registration was configured.
func (m *gpuRegistrationManager) enabled() bool {
	return m.monitor != nil
}

// hasGPUs is nil-safe for agents that do not enable GPU monitoring.
func (m *gpuRegistrationManager) hasGPUs() bool {
	return m.monitor != nil && m.monitor.HasGPUs()
}

// getRegistrationStatus contributes recovery readiness to aggregate health.
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

// handleGPUStateChange immediately marks registration unready and pauses queues;
// GPU arrival then schedules serialized recovery registration.
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

// run consumes coalesced GPU-arrival requests and retries registration with
// bounded backoff until readiness succeeds, GPUs disappear, or ctx ends.
func (m *gpuRegistrationManager) run(ctx context.Context) {
	retryInterval := m.retryInterval
	if retryInterval <= 0 {
		retryInterval = DefaultGPUPollInterval
	}
	backoff := newGPURegistrationRetryBackoff(
		retryInterval,
		maxGracefulNoGPURegistrationRetryInterval,
	)
	newRetryTimer := m.newRetryTimer
	if newRetryTimer == nil {
		newRetryTimer = func(delay time.Duration) gpuRegistrationRetryTimer {
			return &realGPURegistrationRetryTimer{timer: time.NewTimer(delay)}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.registrationRequests:
			backoff.reset()
		}

		for !m.ready.Load() && m.hasGPUs() {
			if !m.tryRegistration(ctx) {
				break
			}

			retryTimer := newRetryTimer(backoff.next())
			select {
			case <-ctx.Done():
				retryTimer.Stop()
				return
			case <-m.registrationRequests:
				retryTimer.Stop()
				backoff.reset()
			case <-retryTimer.C():
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
