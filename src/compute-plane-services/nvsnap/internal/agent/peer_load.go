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

package agent

import (
	"sort"
	"sync"
	"sync/atomic"
)

// peerInfo is the catalog's peer record. Extracted to a named type
// so the cascade-fetch code can sort/iterate slices of peers without
// referring to the anonymous nested struct in catalogSources.
type peerInfo struct {
	NodeName string `json:"node_name"`
	AgentURL string `json:"agent_url"`
}

// peerLoadTracker counts in-flight peer fetches per peer URL. Used
// to spread receivers across available peers when more than one has
// the data.
//
// Phase 1 of the 16-node distribution plan (TRANSPORT-ARCHITECTURE.md):
// without this, all receivers iterate the catalog in identical order
// and pile onto peer[0] simultaneously. With this, each receiver picks
// the peer with the fewest in-flight streams known to THIS agent.
//
// Counter is process-local — we don't try to coordinate global "this
// peer is overloaded right now" across receivers. That's a Phase 4
// problem if it shows up; for 16 nodes the local view is enough.
type peerLoadTracker struct {
	counts sync.Map // peer URL → *atomic.Int64
}

func newPeerLoadTracker() *peerLoadTracker {
	return &peerLoadTracker{}
}

// Nil-safe: a nil receiver acts as a no-op tracker. Lets unit tests
// build minimal Agent values without wiring this in.
func (t *peerLoadTracker) counter(url string) *atomic.Int64 {
	if t == nil {
		return new(atomic.Int64)
	}
	if v, ok := t.counts.Load(url); ok {
		return v.(*atomic.Int64)
	}
	c := new(atomic.Int64)
	v, _ := t.counts.LoadOrStore(url, c)
	return v.(*atomic.Int64)
}

func (t *peerLoadTracker) inc(url string) {
	if t == nil {
		return
	}
	t.counter(url).Add(1)
}
func (t *peerLoadTracker) dec(url string) {
	if t == nil {
		return
	}
	t.counter(url).Add(-1)
}
func (t *peerLoadTracker) get(url string) int64 {
	if t == nil {
		return 0
	}
	if v, ok := t.counts.Load(url); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// sortPeersByLoad orders peers ascending by their current in-flight
// count. Stable sort so peers with equal load keep catalog order
// (typically newest-first, which biases newer peers — they're more
// likely to have free bandwidth than a hot original source).
func sortPeersByLoad(peers []peerInfo, t *peerLoadTracker) {
	if t == nil || len(peers) <= 1 {
		return
	}
	sort.SliceStable(peers, func(i, j int) bool {
		return t.get(peers[i].AgentURL) < t.get(peers[j].AgentURL)
	})
}
