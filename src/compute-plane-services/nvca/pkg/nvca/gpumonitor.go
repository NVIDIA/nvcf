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
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// GPUMonitorComponentName is the name used in health status reporting.
	GPUMonitorComponentName = "gpumonitor"

	// DefaultGPUPollInterval is the default interval between GPU availability checks.
	// With event-driven monitoring via node informers, this acts as a fallback safety net.
	DefaultGPUPollInterval = 60 * time.Second

	// DefaultGPUDebounceTime is the default time a state must be stable before triggering a change.
	DefaultGPUDebounceTime = 30 * time.Second
)

// GPUStateChangeCallback is called when GPU availability changes.
// The hasGPUs parameter indicates whether GPUs are now available.
type GPUStateChangeCallback func(ctx context.Context, hasGPUs bool)

// GPUMonitorConfig holds configuration for the GPU monitor.
type GPUMonitorConfig struct {
	// PollInterval is the interval between GPU availability checks.
	PollInterval time.Duration
	// DebounceTime is the time a state must be stable before triggering a change.
	DebounceTime time.Duration
}

// GPUMonitor continuously monitors GPU availability and notifies on state changes.
// It implements health.ComponentStatusGetter for readiness checks.
type GPUMonitor struct {
	nfClient nodefeatures.Client
	config   GPUMonitorConfig

	// Node informer for event-driven GPU checks (optional)
	nodeInformer cache.SharedIndexInformer

	// Current GPU state
	hasGPUs atomic.Bool

	// Debounce state
	pendingState      bool
	pendingStateStart time.Time
	stateMu           sync.Mutex

	// Event coalescing channel for triggered checks
	triggerCh chan struct{}

	// Callback for state changes
	onStateChange GPUStateChangeCallback

	// For testing
	nowFunc func() time.Time

	// Shutdown
	stopCh chan struct{}
	wg     sync.WaitGroup
}

var _ health.ComponentStatusGetter = (*GPUMonitor)(nil)

// GPUMonitorOption is a functional option for configuring the GPU monitor.
type GPUMonitorOption func(*GPUMonitor)

// WithGPUPollInterval sets the polling interval.
func WithGPUPollInterval(d time.Duration) GPUMonitorOption {
	return func(m *GPUMonitor) {
		m.config.PollInterval = d
	}
}

// WithGPUDebounceTime sets the debounce time.
func WithGPUDebounceTime(d time.Duration) GPUMonitorOption {
	return func(m *GPUMonitor) {
		m.config.DebounceTime = d
	}
}

// WithGPUStateChangeCallback sets the callback for state changes.
func WithGPUStateChangeCallback(cb GPUStateChangeCallback) GPUMonitorOption {
	return func(m *GPUMonitor) {
		m.onStateChange = cb
	}
}

// WithNodeInformer sets a node informer for event-driven GPU checks.
// When provided, node add/update/delete events will trigger immediate GPU checks
// (with coalescing) instead of waiting for the next poll interval.
func WithNodeInformer(inf cache.SharedIndexInformer) GPUMonitorOption {
	return func(m *GPUMonitor) {
		m.nodeInformer = inf
	}
}

// withNowFunc sets the time function (for testing).
func withNowFunc(f func() time.Time) GPUMonitorOption {
	return func(m *GPUMonitor) {
		m.nowFunc = f
	}
}

// NewGPUMonitor creates a new GPU monitor.
func NewGPUMonitor(nfClient nodefeatures.Client, opts ...GPUMonitorOption) *GPUMonitor {
	m := &GPUMonitor{
		nfClient:  nfClient,
		triggerCh: make(chan struct{}, 1),
		config: GPUMonitorConfig{
			PollInterval: DefaultGPUPollInterval,
			DebounceTime: DefaultGPUDebounceTime,
		},
		stopCh:  make(chan struct{}),
		nowFunc: time.Now,
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// Start begins the GPU monitoring loop.
// It performs an initial check and then polls at the configured interval.
// If a node informer is configured, node events will also trigger GPU checks.
func (m *GPUMonitor) Start(ctx context.Context) {
	log := core.GetLogger(ctx)
	log.Info("Starting GPU monitor")

	// Register node informer event handlers if configured
	if m.nodeInformer != nil {
		log.Info("Registering node informer event handlers for event-driven GPU monitoring")
		_, _ = m.nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(_ interface{}) {
				m.triggerCheck()
			},
			UpdateFunc: func(_, _ interface{}) {
				m.triggerCheck()
			},
			DeleteFunc: func(_ interface{}) {
				m.triggerCheck()
			},
		})
	}

	// Perform initial GPU check
	m.checkGPUs(ctx)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.config.PollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Info("GPU monitor stopped due to context cancellation")
				return
			case <-m.stopCh:
				log.Info("GPU monitor stopped")
				return
			case <-ticker.C:
				m.checkGPUs(ctx)
			case <-m.triggerCh:
				log.Debug("GPU check triggered by node event")
				m.checkGPUs(ctx)
			}
		}
	}()
}

// triggerCheck requests a GPU check due to an external event (e.g., node change).
// Multiple rapid calls are coalesced into a single check via a buffered channel.
func (m *GPUMonitor) triggerCheck() {
	select {
	case m.triggerCh <- struct{}{}:
	default:
		// Channel already has a pending trigger, coalesce this one
	}
}

// Stop stops the GPU monitoring loop.
func (m *GPUMonitor) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// HasGPUs returns whether GPUs are currently available.
func (m *GPUMonitor) HasGPUs() bool {
	return m.hasGPUs.Load()
}

// SetHasGPUs sets the GPU availability state directly.
// This is primarily used during initial startup to set the state before monitoring begins.
func (m *GPUMonitor) SetHasGPUs(hasGPUs bool) {
	m.hasGPUs.Store(hasGPUs)
}

// SetOnGPUStateChange sets the callback for GPU state changes.
// This allows setting the callback after construction, which is useful when
// the callback depends on other components that are created later.
func (m *GPUMonitor) SetOnGPUStateChange(cb GPUStateChangeCallback) {
	m.onStateChange = cb
}

// GetComponentStatus implements health.ComponentStatusGetter.
// Returns unhealthy status when no GPUs are available.
func (m *GPUMonitor) GetComponentStatus(ctx context.Context) (types.AgentHealth, error) {
	ch := types.ComponentHealth{
		Status:      types.HealthStatusHealthy,
		StatusLevel: types.StatusLevelError,
	}

	if !m.HasGPUs() {
		ch.Status = types.HealthStatusUnhealthy
		ch.Errors = []string{"no GPUs available in cluster"}
	}

	return types.AgentHealth{
		Components: map[string]types.ComponentHealth{
			GPUMonitorComponentName: ch,
		},
	}, nil
}

// checkGPUs performs a GPU availability check and handles state transitions.
func (m *GPUMonitor) checkGPUs(ctx context.Context) {
	log := core.GetLogger(ctx)

	gpus, err := m.nfClient.GetAllBackendGPUs(ctx)

	// Determine if we have GPUs
	var newHasGPUs bool
	if err != nil {
		if nvcaerrors.IsNotExist(err) {
			// No GPUs found
			newHasGPUs = false
		} else {
			// Error checking GPUs - log but don't change state
			log.WithError(err).Warn("Failed to check GPU availability, keeping current state")
			return
		}
	} else {
		newHasGPUs = len(gpus) > 0
	}

	currentHasGPUs := m.HasGPUs()

	// If state hasn't changed from current, reset debounce
	if newHasGPUs == currentHasGPUs {
		m.stateMu.Lock()
		m.pendingStateStart = time.Time{}
		m.stateMu.Unlock()
		return
	}

	// State is different - apply debounce
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	now := m.nowFunc()

	// If debounce is disabled, apply change immediately
	if m.config.DebounceTime == 0 {
		log.WithField("previous_state", currentHasGPUs).
			WithField("new_state", newHasGPUs).
			Info("GPU availability state changed (no debounce)")

		m.hasGPUs.Store(newHasGPUs)

		// Notify callback if set
		if m.onStateChange != nil {
			// Release lock before calling callback to avoid potential deadlocks
			m.stateMu.Unlock()
			m.onStateChange(ctx, newHasGPUs)
			m.stateMu.Lock()
		}
		return
	}

	// Check if this is a new pending state or continuation
	if m.pendingStateStart.IsZero() || m.pendingState != newHasGPUs {
		// Start new debounce period
		m.pendingState = newHasGPUs
		m.pendingStateStart = now
		log.WithField("pending_state", newHasGPUs).
			WithField("debounce_time", m.config.DebounceTime).
			Info("GPU state change detected, starting debounce period")
		return
	}

	// Check if debounce period has elapsed
	if now.Sub(m.pendingStateStart) < m.config.DebounceTime {
		log.WithField("pending_state", newHasGPUs).
			WithField("elapsed", now.Sub(m.pendingStateStart)).
			WithField("remaining", m.config.DebounceTime-now.Sub(m.pendingStateStart)).
			Debug("GPU state change pending, debounce in progress")
		return
	}

	// Debounce period elapsed - commit the state change
	log.WithField("previous_state", currentHasGPUs).
		WithField("new_state", newHasGPUs).
		Info("GPU availability state changed")

	m.hasGPUs.Store(newHasGPUs)
	m.pendingStateStart = time.Time{}

	// Notify callback if set
	if m.onStateChange != nil {
		// Release lock before calling callback to avoid potential deadlocks
		m.stateMu.Unlock()
		m.onStateChange(ctx, newHasGPUs)
		m.stateMu.Lock()
	}
}
