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
	"sync"
	"testing"
)

func TestPeerLoadTracker_BasicOps(t *testing.T) {
	tr := newPeerLoadTracker()
	if got := tr.get("peer-a"); got != 0 {
		t.Errorf("initial load should be 0, got %d", got)
	}
	tr.inc("peer-a")
	tr.inc("peer-a")
	tr.inc("peer-b")
	if got := tr.get("peer-a"); got != 2 {
		t.Errorf("peer-a load = %d, want 2", got)
	}
	if got := tr.get("peer-b"); got != 1 {
		t.Errorf("peer-b load = %d, want 1", got)
	}
	tr.dec("peer-a")
	if got := tr.get("peer-a"); got != 1 {
		t.Errorf("peer-a load after dec = %d, want 1", got)
	}
}

func TestPeerLoadTracker_ConcurrentInc(t *testing.T) {
	tr := newPeerLoadTracker()
	const N = 1000
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.inc("hot-peer")
		}()
	}
	wg.Wait()
	if got := tr.get("hot-peer"); got != N {
		t.Errorf("after %d concurrent incs, load = %d, want %d", N, got, N)
	}
}

func TestSortPeersByLoad_OrdersByCount(t *testing.T) {
	tr := newPeerLoadTracker()
	tr.inc("peer-a")
	tr.inc("peer-a")
	tr.inc("peer-a") // 3
	tr.inc("peer-b") // 1
	// peer-c remains at 0

	peers := []peerInfo{
		{NodeName: "A", AgentURL: "peer-a"},
		{NodeName: "B", AgentURL: "peer-b"},
		{NodeName: "C", AgentURL: "peer-c"},
	}
	sortPeersByLoad(peers, tr)

	// Should be C (0), B (1), A (3)
	got := []string{peers[0].NodeName, peers[1].NodeName, peers[2].NodeName}
	want := []string{"C", "B", "A"}
	for i, name := range got {
		if name != want[i] {
			t.Errorf("position %d: got %q want %q (full order: %v)", i, name, want[i], got)
		}
	}
}

func TestSortPeersByLoad_StableOnEqual(t *testing.T) {
	tr := newPeerLoadTracker()
	// All peers at load 0 — stable sort preserves input order
	peers := []peerInfo{
		{NodeName: "first", AgentURL: "p1"},
		{NodeName: "second", AgentURL: "p2"},
		{NodeName: "third", AgentURL: "p3"},
	}
	sortPeersByLoad(peers, tr)
	if peers[0].NodeName != "first" || peers[1].NodeName != "second" || peers[2].NodeName != "third" {
		t.Errorf("equal-load sort should preserve input order, got: %v", peers)
	}
}

func TestSortPeersByLoad_NilTrackerNoOp(t *testing.T) {
	peers := []peerInfo{{NodeName: "a"}, {NodeName: "b"}}
	sortPeersByLoad(peers, nil)
	if peers[0].NodeName != "a" || peers[1].NodeName != "b" {
		t.Errorf("nil tracker should be a no-op, got: %v", peers)
	}
}
