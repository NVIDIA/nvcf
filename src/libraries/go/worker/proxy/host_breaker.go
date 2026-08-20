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

package proxy

import (
	"errors"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// ErrHostUnreachable reports that a dial was refused because the host has just
// failed repeatedly. Callers match on this.
var ErrHostUnreachable = errors.New("proxy host is not accepting connections")

// errHostRefused is what allow actually returns. The permanent wrapper stops
// the caller's retry loop, which would otherwise spend its whole budget failing
// fast against the same dead host. backoff unwraps it before returning, so
// callers still see ErrHostUnreachable.
var errHostRefused = backoff.Permanent(ErrHostUnreachable)

const (
	// hostFailureThreshold is how many consecutive failed dials to one host
	// open the breaker. Dials to a host are coalesced, so this counts dial
	// rounds rather than callers: three means three genuinely failed dials, no
	// matter how many sessions were waiting on them. Low enough to stop a dead
	// pod quickly, high enough to ride out a single blip.
	hostFailureThreshold = 3
	// hostOpenDuration is how long a host is refused before a probe is allowed
	// through. Deliberately short: pod IPs get reused, so a stale entry must
	// not be able to hold down an address that now belongs to a healthy pod.
	hostOpenDuration = 30 * time.Second
	// hostIdleRetention drops hosts nothing has talked to recently, so the
	// table does not accumulate an entry per pod IP ever seen.
	hostIdleRetention = 5 * time.Minute
	// hostBreakerCapacity is a hard bound on the table.
	hostBreakerCapacity = 4096
)

// hostBreaker refuses to dial proxy hosts that have just failed repeatedly.
//
// A work request carries the address of the proxy pod that issued it, so when
// that pod goes away every request naming it is doomed. Without any memory of
// that, each one pays the full dial-and-retry budget on its own while holding a
// worker concurrency slot, which is what turns a proxy restart into a backlog
// that takes hours to drain rather than seconds.
//
// It records only dial outcomes, which is what makes it safe. A 403 arrives on
// a connection that dialled successfully, so an authentication failure can
// never open the breaker; by construction this cannot blackhole a pod that is
// alive and answering.
type hostBreaker struct {
	mu    sync.Mutex
	hosts map[string]*hostState

	// now is injectable so the state machine can be tested without sleeping.
	now func() time.Time
}

type hostState struct {
	consecutiveFailures int
	// openedAt is the zero time while the breaker is closed.
	openedAt time.Time
	// probing is set while a single half-open dial is in flight, so one caller
	// probes and the rest are still refused.
	probing  bool
	lastSeen time.Time
}

func newHostBreaker() *hostBreaker {
	return &hostBreaker{hosts: map[string]*hostState{}, now: time.Now}
}

// allow reports whether a dial to host may proceed, returning
// ErrHostUnreachable when the host is being refused.
func (b *hostBreaker) allow(host string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.evictLocked(now)

	st, ok := b.hosts[host]
	if !ok {
		b.hosts[host] = &hostState{lastSeen: now}
		return nil
	}
	st.lastSeen = now

	if st.openedAt.IsZero() {
		return nil
	}
	if now.Sub(st.openedAt) < hostOpenDuration {
		return errHostRefused
	}
	// Half-open: exactly one dial is let through to find out whether the host
	// is back. Everything else keeps being refused until it reports.
	if st.probing {
		return errHostRefused
	}
	st.probing = true
	return nil
}

// recordFailure reports a failed dial. Called once per dial round.
func (b *hostBreaker) recordFailure(host string) (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	st, ok := b.hosts[host]
	if !ok {
		st = &hostState{}
		b.hosts[host] = st
	}
	st.lastSeen = now

	if st.probing {
		// The probe failed, so the host is still gone. Restart the clock
		// without letting the failure count run away.
		st.probing = false
		st.openedAt = now
		return false
	}

	st.consecutiveFailures++
	if st.consecutiveFailures >= hostFailureThreshold && st.openedAt.IsZero() {
		st.openedAt = now
		return true
	}
	return false
}

// recordSuccess reports a dial that connected, which clears the host outright.
func (b *hostBreaker) recordSuccess(host string) (closed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	st, ok := b.hosts[host]
	if !ok {
		b.hosts[host] = &hostState{lastSeen: b.now()}
		return false
	}
	wasOpen := !st.openedAt.IsZero()
	st.consecutiveFailures = 0
	st.openedAt = time.Time{}
	st.probing = false
	st.lastSeen = b.now()
	return wasOpen
}

// isOpen is for tests and logging; it does not change the state machine.
func (b *hostBreaker) isOpen(host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.hosts[host]
	return ok && !st.openedAt.IsZero()
}

// evictLocked keeps the table bounded. Hosts are pod addresses, so the set
// turns over continuously and would otherwise grow for the life of the worker.
func (b *hostBreaker) evictLocked(now time.Time) {
	if len(b.hosts) < hostBreakerCapacity {
		// Cheap path: only drop entries nothing has touched in a long time.
		for host, st := range b.hosts {
			if now.Sub(st.lastSeen) > hostIdleRetention {
				delete(b.hosts, host)
			}
		}
		return
	}
	// At capacity, drop the least recently used entry as well so an insert can
	// always make room.
	var oldestHost string
	var oldest time.Time
	for host, st := range b.hosts {
		if now.Sub(st.lastSeen) > hostIdleRetention {
			delete(b.hosts, host)
			continue
		}
		if oldest.IsZero() || st.lastSeen.Before(oldest) {
			oldest, oldestHost = st.lastSeen, host
		}
	}
	if len(b.hosts) >= hostBreakerCapacity && oldestHost != "" {
		delete(b.hosts, oldestHost)
	}
}
