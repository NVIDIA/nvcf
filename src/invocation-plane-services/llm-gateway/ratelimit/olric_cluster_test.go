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

package ratelimit

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/util"
)

// OlricClusterTestSuite spins up a real multi-node embedded Olric cluster so we
// can verify that two gateway instances (each with its own limiter built on top
// of its own local Olric node) agree on the leaky-bucket state for a shared
// key. This is the property the fakeStore cannot exercise on its own: discovery
// over memberlist, partition ownership, and cross-node DMap visibility.
type OlricClusterTestSuite struct {
	suite.Suite

	ctx   context.Context
	nodes []*util.OlricNode
}

func (s *OlricClusterTestSuite) SetupTest() {
	s.ctx = context.Background()
	s.nodes = nil
}

func (s *OlricClusterTestSuite) TearDownTest() {
	for _, n := range s.nodes {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = n.Shutdown(ctx)
		cancel()
	}
	s.nodes = nil
}

// newCluster boots `size` embedded Olric nodes wired together via memberlist.
// Node 0 is the seed; every subsequent node is given the previous nodes'
// memberlist addresses as peers. All nodes share the same DMap name so keys
// written via one node's limiter are visible to the others after partition
// assignment.
func (s *OlricClusterTestSuite) newCluster(size int) []*util.OlricNode {
	s.T().Helper()
	s.Require().Greater(size, 1, "multi-node test requires at least 2 nodes")

	const dmapName = "rate-limit-cluster-test"

	clientPorts := make([]int, size)
	memberlistPorts := make([]int, size)
	for i := 0; i < size; i++ {
		cp, err := freeTCPPort()
		s.Require().NoError(err)
		mp, err := freeTCPPort()
		s.Require().NoError(err)
		clientPorts[i] = cp
		memberlistPorts[i] = mp
	}

	var nodes []*util.OlricNode
	for i := 0; i < size; i++ {
		var peers []string
		for j := 0; j < i; j++ {
			peers = append(peers, net.JoinHostPort("127.0.0.1", strconv.Itoa(memberlistPorts[j])))
		}

		cfg := config.OlricConfig{
			Enabled:            true,
			Environment:        "local",
			BindAddr:           "127.0.0.1",
			BindPort:           clientPorts[i],
			MemberlistBindAddr: "127.0.0.1",
			MemberlistBindPort: memberlistPorts[i],
			Peers:              peers,
			ReplicaCount:       1,
			PartitionCount:     7,
			DMapName:           dmapName,
			StartupTimeout:     15 * time.Second,
			ShutdownTimeout:    5 * time.Second,
			LogLevel:           "ERROR",
			LogOutput:          io.Discard,
		}

		node, err := util.NewOlricNode(s.ctx, cfg)
		s.Require().NoError(err, "failed to start olric node %d", i)
		nodes = append(nodes, node)
	}

	s.nodes = nodes

	// Confirm discovery has actually wired the nodes together before running
	// limiter assertions. Without this wait, the first check can race the
	// partition balancer and land on a node that hasn't seen its peer yet.
	s.waitForClusterSize(nodes, size, 10*time.Second)

	return nodes
}

func (s *OlricClusterTestSuite) waitForClusterSize(nodes []*util.OlricNode, want int, timeout time.Duration) {
	s.T().Helper()

	deadline := time.Now().Add(timeout)
	for {
		allReady := true
		for _, n := range nodes {
			members, err := n.Client.Members(s.ctx)
			if err != nil || len(members) < want {
				allReady = false
				break
			}
		}
		if allReady {
			return
		}
		if time.Now().After(deadline) {
			for i, n := range nodes {
				members, err := n.Client.Members(s.ctx)
				s.T().Logf("node %d members err=%v count=%d", i, err, len(members))
			}
			s.FailNowf("cluster did not converge", "wanted %d members within %s", want, timeout)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// newLimiterOnNode returns a limiter wired to the given cluster node. A shared
// clock is supplied so rate-limit tests can advance time deterministically even
// though two separate limiter instances are in play.
func (s *OlricClusterTestSuite) newLimiterOnNode(n *util.OlricNode, clock func() int64) RateLimiter {
	s.T().Helper()
	store := NewOlricStore(n.DMap)
	rl, err := NewRateLimiter(store, withClock(clock))
	s.Require().NoError(err)
	return rl
}

// TestSharedBucketAcrossTwoNodes verifies that two independent limiters
// (backed by their own local embedded Olric node in the same cluster) agree on
// the leaky-bucket state for a shared key. Each node consumes half the bucket;
// neither should then be able to consume the remaining budget twice.
func (s *OlricClusterTestSuite) TestSharedBucketAcrossTwoNodes() {
	nodes := s.newCluster(2)

	now := &atomic.Int64{}
	now.Store(time.Unix(1_700_000_000, 0).UnixMilli())
	clock := func() int64 { return now.Load() }

	limiterA := s.newLimiterOnNode(nodes[0], clock)
	limiterB := s.newLimiterOnNode(nodes[1], clock)

	rl := RateLimit{
		Limit:  10,
		Period: time.Minute,
	}
	key := "rl:cluster:shared"

	// Node A consumes 6 tokens from a full bucket of 10.
	resA, err := limiterA.CheckLimit(s.ctx, key, rl, 6, false, "reqA", false)
	s.Require().NoError(err)
	s.True(resA.Allowed())
	s.EqualValues(10, resA.CurrentValue)
	s.EqualValues(4, resA.RemainingValue())

	// Node B must see the 6 already consumed - bucket now has 4 tokens.
	resB, err := limiterB.CheckLimit(s.ctx, key, rl, 3, true, "reqB-probe", false)
	s.Require().NoError(err)
	s.True(resB.Allowed())
	s.EqualValues(4, resB.CurrentValue, "node B should see state written by node A")

	// Node B consumes 4 tokens - should succeed exactly.
	resB2, err := limiterB.CheckLimit(s.ctx, key, rl, 4, false, "reqB-drain", false)
	s.Require().NoError(err)
	s.True(resB2.Allowed())
	s.EqualValues(4, resB2.CurrentValue)
	s.EqualValues(0, resB2.RemainingValue())

	// Total consumed across the cluster is now 10. Neither node should be able
	// to consume a single additional token: that would mean the two nodes
	// disagree on the bucket state.
	resDenyA, err := limiterA.CheckLimit(s.ctx, key, rl, 1, false, "reqA-deny", false)
	s.Require().NoError(err)
	s.False(resDenyA.Allowed(), "node A must not consume past the shared limit")

	resDenyB, err := limiterB.CheckLimit(s.ctx, key, rl, 1, false, "reqB-deny", false)
	s.Require().NoError(err)
	s.False(resDenyB.Allowed(), "node B must not consume past the shared limit")
}

// TestConcurrentConsumeNeverExceedsLimit hammers the same key from both nodes
// concurrently and asserts that the cluster never admits more than `Limit`
// tokens in total. The CompareAndSwap primitive underlying OlricStore must
// serialize cluster-wide on the partition owner - without that, two racing
// CheckLimit calls on different nodes could both observe the same stale
// bucket and both swap past the limit.
func (s *OlricClusterTestSuite) TestConcurrentConsumeNeverExceedsLimit() {
	nodes := s.newCluster(2)

	// Use real time here - we don't care about refill math, just that the lock
	// is correctly serialising admission across the whole cluster.
	clock := func() int64 { return time.Now().UnixMilli() }

	limiterA := s.newLimiterOnNode(nodes[0], clock)
	limiterB := s.newLimiterOnNode(nodes[1], clock)

	rl := RateLimit{
		Limit: 50,
		// Long period so no refill happens during the test.
		Period: time.Hour,
	}
	key := "rl:cluster:concurrent"

	const attempts = 80
	var admitted atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		// Alternate which node services each request.
		limiter := limiterA
		if i%2 == 1 {
			limiter = limiterB
		}
		go func(l RateLimiter, i int) {
			defer wg.Done()
			res, err := l.CheckLimit(s.ctx, key, rl, 1, false, fmt.Sprintf("req-%d", i), false)
			if err != nil {
				s.T().Errorf("CheckLimit returned error: %v", err)
				return
			}
			if res.Allowed() {
				admitted.Add(1)
			}
		}(limiter, i)
	}
	wg.Wait()

	got := admitted.Load()
	s.EqualValues(rl.Limit, got, "cluster admitted exactly `Limit` requests across both nodes")

	// One final probe from each node should confirm the bucket is empty and
	// both nodes see the same state.
	for idx, limiter := range []RateLimiter{limiterA, limiterB} {
		res, err := limiter.CheckLimit(s.ctx, key, rl, 1, true, "final-probe", false)
		s.Require().NoError(err)
		s.EqualValues(0, res.CurrentValue, "node %d disagrees on final bucket value", idx)
	}
}

// TestResetVisibleAcrossNodes verifies that calling Reset on one node is seen
// by the other - i.e. the DMap delete propagates through the cluster.
func (s *OlricClusterTestSuite) TestResetVisibleAcrossNodes() {
	nodes := s.newCluster(2)

	now := &atomic.Int64{}
	now.Store(time.Unix(1_700_000_000, 0).UnixMilli())
	clock := func() int64 { return now.Load() }

	limiterA := s.newLimiterOnNode(nodes[0], clock)
	limiterB := s.newLimiterOnNode(nodes[1], clock)

	rl := RateLimit{
		Limit:  5,
		Period: time.Minute,
	}
	key := "rl:cluster:reset"

	resA, err := limiterA.CheckLimit(s.ctx, key, rl, 5, false, "", false)
	s.Require().NoError(err)
	s.True(resA.Allowed())

	resBProbe, err := limiterB.CheckLimit(s.ctx, key, rl, 1, true, "", false)
	s.Require().NoError(err)
	s.EqualValues(0, resBProbe.CurrentValue, "node B should see drained state from node A")

	s.Require().NoError(limiterB.Reset(s.ctx, key))

	resAProbe, err := limiterA.CheckLimit(s.ctx, key, rl, 1, true, "", false)
	s.Require().NoError(err)
	s.EqualValues(5, resAProbe.CurrentValue, "node A should see reset issued by node B")
}

func TestOlricClusterTestSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-node Olric cluster test in -short mode")
	}
	suite.Run(t, new(OlricClusterTestSuite))
}

// freeTCPPort binds to 127.0.0.1:0 and returns the port it was assigned. The
// listener is closed immediately; the port is then reusable for the duration
// of the test. Races with unrelated processes are possible but negligible
// on a developer/CI box.
func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener addr type %T", l.Addr())
	}
	return addr.Port, nil
}
