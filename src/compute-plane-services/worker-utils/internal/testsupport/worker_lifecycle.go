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

// Package testsupport contains helpers shared by worker-utils tests.
package testsupport

import (
	"context"
	"errors"
	"sync"
)

// CallbackGroup tracks asynchronous test callbacks and prevents callbacks
// scheduled after teardown starts from running against cleaned-up resources.
type CallbackGroup struct {
	mu      sync.Mutex
	stopped bool
	started bool
	active  int
	idle    chan struct{}
}

// NewCallbackGroup creates a tracker with no callbacks in flight.
func NewCallbackGroup() *CallbackGroup {
	idle := make(chan struct{})
	close(idle)
	return &CallbackGroup{idle: idle}
}

// Run executes callback unless teardown has started. StopAndWait prevents new
// callbacks from starting and joins callbacks already in flight.
func (g *CallbackGroup) Run(callback func() error) error {
	if callback == nil {
		return nil
	}

	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return nil
	}
	if g.active == 0 {
		g.idle = make(chan struct{})
	}
	g.active++
	g.started = true
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.active--
		if g.active == 0 {
			close(g.idle)
		}
		g.mu.Unlock()
	}()

	return callback()
}

// StopAndWait prevents further callbacks from starting and waits for callbacks
// already in flight. It reports whether a callback started before teardown.
func (g *CallbackGroup) StopAndWait() bool {
	g.mu.Lock()
	g.stopped = true
	started := g.started
	idle := g.idle
	g.mu.Unlock()

	<-idle
	return started
}

// WaitForTrackedCallback preserves the wait error while ensuring a tracked
// asynchronous callback and its waiter have finished before cleanup proceeds.
// The second wait is needed only if the callback started; this avoids blocking
// when worker startup fails before ConnectOnce launches it.
func WaitForTrackedCallback(
	ctx context.Context,
	wait func(context.Context) error,
	callbacks *CallbackGroup,
) error {
	waitErr := wait(ctx)
	if callbacks == nil || !callbacks.StopAndWait() {
		return waitErr
	}

	return errors.Join(waitErr, wait(context.Background()))
}

// RunWithWait runs a worker and waits for its mock server to finish before
// returning. The waiter cancels the worker when its work completes; if the
// worker fails first, cancellation releases the waiter. Returning both errors
// lets callers report failures from the test goroutine rather than a helper
// goroutine.
func RunWithWait(
	ctx context.Context,
	cancel context.CancelFunc,
	run func(context.Context) error,
	wait func(context.Context) error,
) (runErr, waitErr error) {
	waitResult := make(chan error, 1)
	go func() {
		waitErr := wait(ctx)
		waitResult <- waitErr
		cancel()
	}()

	runErr = run(ctx)
	if runErr != nil {
		cancel()
	}
	waitErr = <-waitResult
	cancel()
	return runErr, waitErr
}
