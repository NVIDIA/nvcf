/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package server

import (
	"context"
	"sync"

	echo "github.com/labstack/echo/v4"
)

// teardowns maps each Echo instance built by New to its teardown.
var teardowns sync.Map

// teardown stops what in-flight requests depend on (auth client, rate-limit
// sync, Olric) only after those requests finish. http.Server runs
// RegisterOnShutdown hooks as soon as Shutdown starts, concurrently with the
// drain, so stopping dependencies there breaks requests that are still
// running.
type teardown struct {
	mu       sync.Mutex
	idle     *sync.Cond
	inFlight int
	steps    []func()
	done     chan struct{}
}

func newTeardown(e *echo.Echo) *teardown {
	t := &teardown{done: make(chan struct{})}
	t.idle = sync.NewCond(&t.mu)
	e.Pre(t.track)
	e.Server.RegisterOnShutdown(t.run)
	teardowns.Store(e, t)
	return t
}

func teardownOf(e *echo.Echo) *teardown {
	t, _ := teardowns.Load(e)
	return t.(*teardown)
}

// add registers a step to run once in-flight requests finish. Steps run in
// registration order.
func (t *teardown) add(step func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
}

func (t *teardown) track(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		t.mu.Lock()
		t.inFlight++
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			t.inFlight--
			if t.inFlight == 0 {
				t.idle.Broadcast()
			}
			t.mu.Unlock()
		}()
		return next(c)
	}
}

func (t *teardown) run() {
	t.mu.Lock()
	for t.inFlight > 0 {
		t.idle.Wait()
	}
	steps := t.steps
	t.mu.Unlock()

	for _, step := range steps {
		step()
	}
	close(t.done)
}

// Shutdown drains in-flight requests on e, then waits for the dependencies
// New started to stop, both within ctx. It returns ctx's error if either
// does not finish in time.
func Shutdown(ctx context.Context, e *echo.Echo) error {
	defer teardowns.Delete(e)
	if err := e.Shutdown(ctx); err != nil {
		return err
	}
	t, ok := teardowns.Load(e)
	if !ok {
		return nil
	}
	select {
	case <-t.(*teardown).done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
