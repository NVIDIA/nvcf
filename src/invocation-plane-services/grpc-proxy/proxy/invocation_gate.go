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
package proxy

import (
	"sync"
	"time"
)

// invocationGate counts stateful invocations that are between recording their
// pending work and publishing the work request, and lets shutdown close
// admission and wait for the ones already inside.
//
// Without it the shutdown purge is not sound. An invocation records its pending
// work first and publishes the work request afterwards, so a purge landing
// between those two steps finds nothing to remove and then the publish leaves a
// request in the queue with no surviving token to authenticate it, which is the
// exact situation the purge exists to prevent.
//
// It covers only the invocation itself, never the session that follows, so
// shutdown is never held up for the length of a session.
type invocationGate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	active  int
	closed  bool
	expired bool
}

func newInvocationGate() *invocationGate {
	g := &invocationGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// begin admits an invocation, reporting false once admission has closed. A
// refused caller must not go on to publish a work request.
func (g *invocationGate) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.active++
	return true
}

func (g *invocationGate) end() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active == 0 {
		g.cond.Broadcast()
	}
}

// closeAndDrain stops admitting invocations and waits for those already
// admitted, giving up after timeout so a stuck invocation cannot hold up
// shutdown. Closing happens under the same lock that begin checks, so once this
// returns no further invocation can publish a work request.
func (g *invocationGate) closeAndDrain(timeout time.Duration) {
	g.mu.Lock()
	g.closed = true
	if g.active == 0 {
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()

	// sync.Cond has no deadline of its own, so a timer sets the expiry flag and
	// wakes the wait. Waiting in place rather than in a helper goroutine means
	// an invocation that never finishes cannot leave one behind.
	timer := time.AfterFunc(timeout, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.expired = true
		g.cond.Broadcast()
	})
	defer timer.Stop()

	g.mu.Lock()
	defer g.mu.Unlock()
	for g.active > 0 && !g.expired {
		g.cond.Wait()
	}
}
