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
	"sync/atomic"
	"time"

	echo "github.com/labstack/echo/v4"
)

// canceledRequestGrace bounds how long Shutdown waits, after a timed-out
// drain closes the remaining connections, for their canceled handlers to
// return before stopping dependencies.
const canceledRequestGrace = 5 * time.Second

// teardowns maps each Echo instance built by New to its teardown.
var teardowns sync.Map

// teardown stops what requests depend on (auth client, rate-limit sync,
// Olric). It is not registered with http.Server.RegisterOnShutdown, because
// those hooks start as soon as Shutdown begins, concurrently with the drain,
// and would stop dependencies under requests that are still running.
type teardown struct {
	mu     sync.Mutex
	steps  []func()
	once   sync.Once
	active atomic.Int64
}

func newTeardown(e *echo.Echo) *teardown {
	t := &teardown{}
	e.Pre(t.track)
	teardowns.Store(e, t)
	return t
}

// track counts running handlers, so a timed-out Shutdown can wait for the
// requests it canceled to unwind.
func (t *teardown) track(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		t.active.Add(1)
		defer t.active.Add(-1)
		return next(c)
	}
}

func (t *teardown) waitIdle(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for t.active.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

func teardownOf(e *echo.Echo) *teardown {
	t, _ := teardowns.Load(e)
	return t.(*teardown)
}

// add registers a step. Steps run once, in registration order, and must
// bound their own duration.
func (t *teardown) add(step func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
}

func (t *teardown) run() {
	t.once.Do(func() {
		t.mu.Lock()
		steps := t.steps
		t.mu.Unlock()
		for _, step := range steps {
			step()
		}
	})
}

// Shutdown stops e gracefully. It stops accepting connections and waits for
// in-flight requests, including streams, until ctx is done. If ctx expires
// first, it closes the remaining connections, which cancels their requests,
// and waits briefly for their handlers to return. Either way it then stops
// the dependencies New started and returns the drain error, if any. It is
// safe to call more than once.
func Shutdown(ctx context.Context, e *echo.Echo) error {
	v, tracked := teardowns.Load(e)
	err := e.Shutdown(ctx)
	if err != nil {
		_ = e.Close()
		if tracked {
			v.(*teardown).waitIdle(canceledRequestGrace)
		}
	}
	if tracked {
		v.(*teardown).run()
	}
	return err
}
