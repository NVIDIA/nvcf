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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type fakeGPUAvailability struct {
	hasGPUs       atomic.Bool
	onStateChange GPUStateChangeCallback
}

func (f *fakeGPUAvailability) HasGPUs() bool {
	return f.hasGPUs.Load()
}

func (f *fakeGPUAvailability) SetOnGPUStateChange(callback GPUStateChangeCallback) {
	f.onStateChange = callback
}

func (f *fakeGPUAvailability) Start(context.Context) {}

func (f *fakeGPUAvailability) GetComponentStatus(context.Context) (types.AgentHealth, error) {
	return types.AgentHealth{}, nil
}

type fakeGPURegistrationQueue struct {
	paused atomic.Bool
}

func (f *fakeGPURegistrationQueue) Pause() {
	f.paused.Store(true)
}

func (f *fakeGPURegistrationQueue) Resume() {
	f.paused.Store(false)
}

type observedDoneContext struct {
	context.Context
	doneObserved chan struct{}
	once         sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.doneObserved)
	})
	return c.Context.Done()
}

type fakeGPURegistrationRetryTimer struct {
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newFakeGPURegistrationRetryTimer() *fakeGPURegistrationRetryTimer {
	return &fakeGPURegistrationRetryTimer{
		ch:      make(chan time.Time, 1),
		stopped: make(chan struct{}),
	}
}

func (t *fakeGPURegistrationRetryTimer) C() <-chan time.Time {
	return t.ch
}

func (t *fakeGPURegistrationRetryTimer) Stop() bool {
	t.once.Do(func() {
		close(t.stopped)
	})
	return true
}

func (t *fakeGPURegistrationRetryTimer) fire() {
	t.ch <- time.Time{}
}

type gpuRegistrationRetryTimerRequest struct {
	delay time.Duration
	timer *fakeGPURegistrationRetryTimer
}

type fakeGPURegistrationRetryTimerFactory struct {
	requests chan gpuRegistrationRetryTimerRequest
}

func newFakeGPURegistrationRetryTimerFactory() *fakeGPURegistrationRetryTimerFactory {
	return &fakeGPURegistrationRetryTimerFactory{
		requests: make(chan gpuRegistrationRetryTimerRequest, 16),
	}
}

func (f *fakeGPURegistrationRetryTimerFactory) newTimer(delay time.Duration) gpuRegistrationRetryTimer {
	timer := newFakeGPURegistrationRetryTimer()
	f.requests <- gpuRegistrationRetryTimerRequest{delay: delay, timer: timer}
	return timer
}

func waitForGPURegistrationCall(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("registration callback was not invoked")
	}
}

func waitForGPURegistrationRetryTimer(t *testing.T, factory *fakeGPURegistrationRetryTimerFactory) gpuRegistrationRetryTimerRequest {
	t.Helper()
	select {
	case request := <-factory.requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("registration retry timer was not requested")
		return gpuRegistrationRetryTimerRequest{}
	}
}

func TestGPURegistrationManagerCancellationWhileWaiting(t *testing.T) {
	var manager gpuRegistrationManager

	holderEntered := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- manager.withRegistrationOperation(context.Background(), func() error {
			close(holderEntered)
			<-releaseHolder
			return nil
		})
	}()
	<-holderEntered

	baseContext, cancel := context.WithCancel(context.Background())
	ctx := &observedDoneContext{
		Context:      baseContext,
		doneObserved: make(chan struct{}),
	}
	waiterDone := make(chan error, 1)
	var waiterRan atomic.Bool
	go func() {
		waiterDone <- manager.withRegistrationOperation(ctx, func() error {
			waiterRan.Store(true)
			return nil
		})
	}()
	<-ctx.doneObserved
	cancel()

	select {
	case err := <-waiterDone:
		require.ErrorIs(t, err, context.Canceled)
		assert.False(t, waiterRan.Load())
	case <-time.After(time.Second):
		t.Fatal("canceled registration operation remained blocked behind the active operation")
	}

	close(releaseHolder)
	require.NoError(t, <-holderDone)
}

func TestGPURegistrationRetryBackoffIsBoundedAndResettable(t *testing.T) {
	backoff := newGPURegistrationRetryBackoff(5*time.Second, 20*time.Second)

	assert.Equal(t, 5*time.Second, backoff.next())
	assert.Equal(t, 10*time.Second, backoff.next())
	assert.Equal(t, 20*time.Second, backoff.next())
	assert.Equal(t, 20*time.Second, backoff.next())

	backoff.reset()
	assert.Equal(t, 5*time.Second, backoff.next())
}

func TestGPURegistrationManagerRunUsesBoundedRetryBackoff(t *testing.T) {
	monitor := &fakeGPUAvailability{}
	monitor.hasGPUs.Store(true)
	registrationCalls := make(chan struct{}, 16)
	timers := newFakeGPURegistrationRetryTimerFactory()
	var manager gpuRegistrationManager
	manager.configureGracefulNoGPU(monitor, false, 5*time.Second, func(context.Context) error {
		registrationCalls <- struct{}{}
		return errors.New("registration unavailable")
	})
	manager.newRetryTimer = timers.newTimer

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		manager.run(ctx)
	}()
	manager.handleGPUStateChange(ctx, true)

	expectedDelays := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		80 * time.Second,
		160 * time.Second,
		5 * time.Minute,
		5 * time.Minute,
	}
	var activeTimer *fakeGPURegistrationRetryTimer
	for index, expectedDelay := range expectedDelays {
		waitForGPURegistrationCall(t, registrationCalls)
		request := waitForGPURegistrationRetryTimer(t, timers)
		assert.Equal(t, expectedDelay, request.delay)
		activeTimer = request.timer
		if index < len(expectedDelays)-1 {
			request.timer.fire()
		}
	}

	cancel()
	select {
	case <-activeTimer.stopped:
	case <-time.After(time.Second):
		t.Fatal("active retry timer was not stopped after cancellation")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("registration retry loop did not stop after cancellation")
	}
}

func TestGPURegistrationManagerRunResetsBackoffOnGPUArrival(t *testing.T) {
	monitor := &fakeGPUAvailability{}
	monitor.hasGPUs.Store(true)
	registrationCalls := make(chan struct{}, 4)
	timers := newFakeGPURegistrationRetryTimerFactory()
	var manager gpuRegistrationManager
	manager.configureGracefulNoGPU(monitor, false, 5*time.Second, func(context.Context) error {
		registrationCalls <- struct{}{}
		return errors.New("registration unavailable")
	})
	manager.newRetryTimer = timers.newTimer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		manager.run(ctx)
	}()
	manager.handleGPUStateChange(ctx, true)

	waitForGPURegistrationCall(t, registrationCalls)
	firstTimer := waitForGPURegistrationRetryTimer(t, timers)
	assert.Equal(t, 5*time.Second, firstTimer.delay)
	firstTimer.timer.fire()

	waitForGPURegistrationCall(t, registrationCalls)
	secondTimer := waitForGPURegistrationRetryTimer(t, timers)
	assert.Equal(t, 10*time.Second, secondTimer.delay)
	manager.handleGPUStateChange(ctx, true)

	select {
	case <-secondTimer.timer.stopped:
	case <-time.After(time.Second):
		t.Fatal("GPU arrival did not wake and stop the active retry timer")
	}
	waitForGPURegistrationCall(t, registrationCalls)
	resetTimer := waitForGPURegistrationRetryTimer(t, timers)
	assert.Equal(t, 5*time.Second, resetTimer.delay)

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("registration retry loop did not stop")
	}
}

func TestGPURegistrationManagerRunCancellationStopsBackoffTimer(t *testing.T) {
	monitor := &fakeGPUAvailability{}
	monitor.hasGPUs.Store(true)
	registrationCalls := make(chan struct{}, 1)
	timers := newFakeGPURegistrationRetryTimerFactory()
	var manager gpuRegistrationManager
	manager.configureGracefulNoGPU(monitor, false, time.Second, func(context.Context) error {
		registrationCalls <- struct{}{}
		return errors.New("registration unavailable")
	})
	manager.newRetryTimer = timers.newTimer

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		manager.run(ctx)
	}()
	manager.handleGPUStateChange(ctx, true)
	waitForGPURegistrationCall(t, registrationCalls)
	activeTimer := waitForGPURegistrationRetryTimer(t, timers)

	cancel()
	select {
	case <-activeTimer.timer.stopped:
	case <-time.After(time.Second):
		t.Fatal("active retry timer was not stopped")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("registration retry loop did not exit")
	}
}

func TestGPURegistrationManagerRunPreservesConfiguredSlowRetry(t *testing.T) {
	monitor := &fakeGPUAvailability{}
	monitor.hasGPUs.Store(true)
	registrationCalls := make(chan struct{}, 2)
	timers := newFakeGPURegistrationRetryTimerFactory()
	var manager gpuRegistrationManager
	manager.configureGracefulNoGPU(monitor, false, 10*time.Minute, func(context.Context) error {
		registrationCalls <- struct{}{}
		return errors.New("registration unavailable")
	})
	manager.newRetryTimer = timers.newTimer

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		manager.run(ctx)
	}()
	manager.handleGPUStateChange(ctx, true)

	waitForGPURegistrationCall(t, registrationCalls)
	firstTimer := waitForGPURegistrationRetryTimer(t, timers)
	assert.Equal(t, 10*time.Minute, firstTimer.delay)
	firstTimer.timer.fire()
	waitForGPURegistrationCall(t, registrationCalls)
	secondTimer := waitForGPURegistrationRetryTimer(t, timers)
	assert.Equal(t, 10*time.Minute, secondTimer.delay)

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("registration retry loop did not stop")
	}
}

func TestGPURegistrationManagerGPULossMarksUnreadyAndPausesQueue(t *testing.T) {
	monitor := &fakeGPUAvailability{}
	monitor.hasGPUs.Store(true)
	queue := &fakeGPURegistrationQueue{}
	var manager gpuRegistrationManager
	manager.configureGracefulNoGPU(monitor, true, time.Second, nil)
	manager.setQueueManager(queue)

	manager.handleGPUStateChange(context.Background(), false)

	status, err := manager.getRegistrationStatus(context.Background())
	require.NoError(t, err)
	require.Contains(t, status.Components, gracefulNoGPURegistrationComponentName)
	assert.Equal(t, types.HealthStatusUnhealthy,
		status.Components[gracefulNoGPURegistrationComponentName].Status)
	assert.True(t, queue.paused.Load())
}

func TestGPURegistrationManagerGPUChangeDuringRegistrationKeepsQueuePaused(t *testing.T) {
	monitor := &fakeGPUAvailability{}
	monitor.hasGPUs.Store(true)
	queue := &fakeGPURegistrationQueue{}
	queue.Pause()
	registrationStarted := make(chan struct{})
	releaseRegistration := make(chan struct{})
	var manager gpuRegistrationManager
	manager.configureGracefulNoGPU(monitor, false, time.Second, func(context.Context) error {
		close(registrationStarted)
		<-releaseRegistration
		return nil
	})
	manager.setQueueManager(queue)

	registrationDone := make(chan bool, 1)
	go func() {
		registrationDone <- manager.tryRegistration(context.Background())
	}()
	<-registrationStarted

	monitor.hasGPUs.Store(false)
	manager.handleGPUStateChange(context.Background(), false)
	close(releaseRegistration)

	assert.False(t, <-registrationDone)
	status, err := manager.getRegistrationStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, types.HealthStatusUnhealthy,
		status.Components[gracefulNoGPURegistrationComponentName].Status)
	assert.True(t, queue.paused.Load())
}
