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

package client

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/fakegds"
)

func names(pools []*gdsv1.Pool) []string {
	var out []string
	for _, p := range pools {
		out = append(out, p.GetName())
	}
	return out
}

func runCache(t *testing.T, run func(context.Context) error) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		assert.NoError(t, <-done)
	}
	t.Cleanup(stop)
	return stop
}

func waitUpdate(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("no cache update")
	}
}

func TestPoolCacheSyncDeltasAndHelpers(t *testing.T) {
	f := startFake(t, "p")
	f.srv.PutExport(testClusterset, "a", "pools", "blue", fakegds.PoolFrontendSpec("blue", 8000, "llama-3"), fakegds.ReadyEndpoints("10.0.0.1"))
	f.srv.PutExport(testClusterset, "b", "pools", "red", fakegds.PoolFrontendSpec("red", 8000, "mistral"), nil)
	dns := newFakeResolver(map[string]string{"gds.test": "127.0.0.1"})
	reg := prometheus.NewRegistry()
	c := newTestClient(t, Config{URLs: []string{f.url("gds.test")}, Resolver: dns, Registerer: reg})

	_, err := NewPoolCache(c, PoolCacheConfig{})
	assert.Error(t, err)
	pc, err := NewPoolCache(c, PoolCacheConfig{Filter: &gdsv1.ListPoolsRequest{Clusterset: testClusterset}, TTL: time.Hour})
	require.NoError(t, err)
	assert.False(t, pc.Snapshot().Synced)
	updates := pc.Updates()
	stop := runCache(t, pc.Run)
	require.NoError(t, pc.WaitSynced(testCtx(t)))
	waitUpdate(t, updates)

	st := pc.Snapshot()
	assert.True(t, st.Synced)
	assert.False(t, st.Stale)
	assert.NotEmpty(t, st.Revision)
	assert.False(t, st.UpdatedAt.IsZero())
	require.Len(t, st.Pools, 2)
	assert.Equal(t, []string{"blue"}, names(st.ByModel("llama-3")))
	assert.Equal(t, []string{"red"}, names(st.ByCluster("b")))
	assert.Equal(t, []string{"blue"}, names(st.Active()), "red has no ready endpoint")
	blueID := fakegds.LabelPoolID(testClusterset, "a", "blue")
	assert.Equal(t, "blue", st.Get(blueID).GetName())
	assert.Nil(t, st.Get("missing"))
	_, ok := metricValue(t, reg, MetricCacheAgeSeconds, map[string]string{"cache": WatchPools})
	assert.True(t, ok)

	// Deltas arrive through the watch.
	require.Eventually(t, func() bool { return f.srv.WatcherCount() == 1 }, 5*time.Second, 5*time.Millisecond)
	f.srv.PutExport(testClusterset, "a", "pools", "green", fakegds.PoolFrontendSpec("green", 8000, "llama-3"), fakegds.ReadyEndpoints("10.0.0.2"))
	require.Eventually(t, func() bool { return len(pc.Snapshot().ByModel("llama-3")) == 2 }, 5*time.Second, 5*time.Millisecond)
	f.srv.SetEndpoints(testClusterset, "b", "pools", "red", fakegds.ReadyEndpoints("10.1.0.1"))
	require.Eventually(t, func() bool { return len(pc.Snapshot().Active()) == 3 }, 5*time.Second, 5*time.Millisecond)
	f.srv.DeleteExport(testClusterset, "a", "pools", "blue")
	require.Eventually(t, func() bool { return pc.Snapshot().Get(blueID) == nil }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, 1, f.srv.CallCount("ListPools"), "no re-list within the TTL")

	require.NoError(t, pc.Refresh(testCtx(t)))
	assert.Equal(t, 2, f.srv.CallCount("ListPools"))
	assert.NoError(t, pc.LastError())

	stop()
	for range updates {
	}
	_, open := <-pc.Updates()
	assert.False(t, open, "Updates after Run returned is closed")
	_, ok = metricValue(t, reg, MetricCacheAgeSeconds, map[string]string{"cache": WatchPools})
	assert.False(t, ok, "a stopped cache leaves the age metric")
	assert.Error(t, func() error {
		pc2, _ := NewPoolCache(c, PoolCacheConfig{Filter: &gdsv1.ListPoolsRequest{Clusterset: testClusterset}})
		pc2.core.running.Store(true)
		return pc2.Run(context.Background())
	}(), "a cache runs once at a time")
}

// TestPoolCacheTTLRelist keeps the watch failing and expects the periodic
// full list to bring changes in.
func TestPoolCacheTTLRelist(t *testing.T) {
	f := startFake(t, "p")
	dns := newFakeResolver(map[string]string{"gds.test": "127.0.0.1"})
	c := newTestClient(t, Config{URLs: []string{f.url("gds.test")}, Resolver: dns})
	for range 1000 {
		f.srv.FailNext("WatchPools", codes.Unavailable)
	}
	pc, err := NewPoolCache(c, PoolCacheConfig{
		Filter: &gdsv1.ListPoolsRequest{Clusterset: testClusterset, Model: "llama-3"},
		TTL:    50 * time.Millisecond,
	})
	require.NoError(t, err)
	runCache(t, pc.Run)
	require.NoError(t, pc.WaitSynced(testCtx(t)))
	assert.Empty(t, pc.Snapshot().Pools)

	f.srv.PutExport(testClusterset, "a", "pools", "blue", fakegds.PoolFrontendSpec("blue", 8000, "llama-3"), fakegds.ReadyEndpoints("10.0.0.1"))
	f.srv.PutExport(testClusterset, "a", "pools", "red", fakegds.PoolFrontendSpec("red", 8000, "mistral"), fakegds.ReadyEndpoints("10.0.0.2"))
	require.Eventually(t, func() bool { return len(pc.Snapshot().Pools) == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "blue", pc.Snapshot().Pools[0].GetName(), "the filter applies to lists")
	require.Eventually(t, func() bool { return f.srv.CallCount("ListPools") >= 4 }, 5*time.Second, 5*time.Millisecond)
	assert.Zero(t, f.srv.WatcherCount())
	assert.False(t, pc.Snapshot().Stale)
}

// TestPoolCacheStale stops the server and expects the cache to turn stale
// after 2 x TTL, keep its data, and recover when the server returns.
func TestPoolCacheStale(t *testing.T) {
	f := startFake(t, "p")
	f.srv.PutExport(testClusterset, "a", "pools", "blue", fakegds.PoolFrontendSpec("blue", 8000, "llama-3"), fakegds.ReadyEndpoints("10.0.0.1"))
	dns := newFakeResolver(map[string]string{"gds.test": "127.0.0.1"})
	c := newTestClient(t, Config{URLs: []string{f.url("gds.test")}, Resolver: dns, RPCTimeout: 200 * time.Millisecond})
	pc, err := NewPoolCache(c, PoolCacheConfig{
		Filter:      &gdsv1.ListPoolsRequest{Clusterset: testClusterset},
		TTL:         50 * time.Millisecond,
		IdleTimeout: -1,
	})
	require.NoError(t, err)
	updates := pc.Updates()
	runCache(t, pc.Run)
	require.NoError(t, pc.WaitSynced(testCtx(t)))

	f.stop()
	start := time.Now()
	require.Eventually(t, func() bool { return pc.Snapshot().Stale }, 10*time.Second, 5*time.Millisecond)
	assert.GreaterOrEqual(t, time.Since(start), 90*time.Millisecond, "stale only after 2 x TTL")
	assert.Len(t, pc.Snapshot().Pools, 1, "a stale cache keeps the last state")
	assert.Error(t, pc.LastError())
	waitUpdate(t, updates)

	f.restart(t)
	require.Eventually(t, func() bool { return !pc.Snapshot().Stale }, 10*time.Second, 5*time.Millisecond)
	assert.NoError(t, pc.LastError())
}

func TestRouterCacheSeedURLs(t *testing.T) {
	f := startFake(t, "r")
	f.srv.PutExport(testClusterset, "a", "llm", "backend-router", fakegds.RouterSpec(50071, 8080, "https"), fakegds.ReadyEndpoints("10.0.0.1"))
	f.srv.PutExport(testClusterset, "b", "llm", "backend-router", fakegds.RouterSpec(50071, 8080, "https"), fakegds.ReadyEndpoints("10.1.0.1"))
	f.srv.PutExport(testClusterset, "c", "edge", "router", fakegds.RouterSpec(50072, 0, "http"), nil)
	f.srv.PutExport(testClusterset, "c", "edge", "router2", fakegds.RouterSpec(50073, 0, "http"), nil)
	noGRPC := fakegds.RouterSpec(50071, 0, "")
	noGRPC.Ports[0].Name = "other"
	f.srv.PutExport(testClusterset, "d", "llm", "nogrpc", noGRPC, nil)
	dns := newFakeResolver(map[string]string{"gds.test": "127.0.0.1"})
	c := newTestClient(t, Config{URLs: []string{f.url("gds.test")}, Resolver: dns})

	_, err := NewRouterCache(c, RouterCacheConfig{Filter: &gdsv1.ListRoutersRequest{}})
	assert.Error(t, err)
	rc, err := NewRouterCache(c, RouterCacheConfig{Filter: &gdsv1.ListRoutersRequest{Clusterset: testClusterset}, TTL: time.Hour})
	require.NoError(t, err)
	updates := rc.Updates()
	runCache(t, rc.Run)
	require.NoError(t, rc.WaitSynced(testCtx(t)))
	st := rc.Snapshot()
	require.Len(t, st.Routers, 5)
	assert.Len(t, st.ByCluster("c"), 2)
	llm := "https://" + api.DerivedServiceName("llm", "backend-router") + ".llm.svc.cluster.local:50071"
	edge := "http://" + api.DerivedServiceName("edge", "router") + ".edge.svc.cluster.local:50072"
	assert.Equal(t, llm, st.SeedURL(st.ByCluster("a")[0]))
	assert.Equal(t, []string{llm, edge}, st.SeedURLs(),
		"one URL per router cluster; a and b share the derived Service; d has no gRPC port")

	f.srv.PutExport(testClusterset, "e", "llm", "late", fakegds.RouterSpec(50074, 0, "https"), nil)
	waitUpdate(t, updates)
	require.Eventually(t, func() bool { return len(rc.Snapshot().SeedURLs()) == 3 }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, rc.Refresh(testCtx(t)))
	assert.NoError(t, rc.LastError())
}

func TestRouterStateSeedURL(t *testing.T) {
	r := &gdsv1.Router{Cluster: "a", Namespace: "ns", Name: "r", GrpcPort: 443}
	name := api.DerivedServiceName("ns", "r")
	assert.Equal(t, "https://"+name+".ns.svc.cluster.local:443", (&RouterState{}).SeedURL(r))
	st := &RouterState{ClusterDomain: "corp.example", DefaultScheme: "http"}
	assert.Equal(t, "http://"+name+".ns.svc.corp.example:443", st.SeedURL(r))
	r.DialScheme = "https"
	assert.Equal(t, "https://"+name+".ns.svc.corp.example:443", st.SeedURL(r))
	assert.Empty(t, st.SeedURL(&gdsv1.Router{Namespace: "ns", Name: "r"}))

	expired := &gdsv1.Router{Cluster: "x", Namespace: "ns", Name: "dead", GrpcPort: 1, ClusterLeaseState: gdsv1.LeaseState_LEASE_STATE_EXPIRED}
	alive := &gdsv1.Router{Cluster: "x", Namespace: "ns", Name: "zz", GrpcPort: 2, ClusterLeaseState: gdsv1.LeaseState_LEASE_STATE_ACTIVE}
	st = &RouterState{Routers: []*gdsv1.Router{expired, alive}}
	assert.Equal(t, []string{st.SeedURL(alive)}, st.SeedURLs(), "routers of expired clusters are skipped")
}
