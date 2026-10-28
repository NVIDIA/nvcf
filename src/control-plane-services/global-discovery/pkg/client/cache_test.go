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

// TestRouterCacheSeedURLs runs a RouterCache over routers that several
// clusters export under one namespace and name, and expects one seed per
// ready replica of each cluster instead of the shared derived VIP.
func TestRouterCacheSeedURLs(t *testing.T) {
	f := startFake(t, "r")
	notReady := &gdsv1.Endpoint{Addresses: []string{"10.1.0.9"}, Conditions: &gdsv1.EndpointConditions{}}
	f.srv.PutExport(testClusterset, "a", "llm", "backend-router", fakegds.RouterSpec(50071, 8080, "https"), fakegds.ReadyEndpoints("10.0.0.1"))
	f.srv.PutExport(testClusterset, "b", "llm", "backend-router", fakegds.RouterSpec(50071, 8080, "https"),
		append(fakegds.ReadyEndpoints("10.1.0.2", "10.1.0.1"), notReady))
	f.srv.PutExport(testClusterset, "c", "edge", "router", fakegds.RouterSpec(50072, 0, "http"), fakegds.ReadyEndpoints("fd00::5"))
	f.srv.PutExport(testClusterset, "c", "edge", "router2", fakegds.RouterSpec(50073, 0, "http"), nil)
	noGRPC := fakegds.RouterSpec(50071, 0, "")
	noGRPC.Ports[0].Name = "other"
	f.srv.PutExport(testClusterset, "d", "llm", "nogrpc", noGRPC, fakegds.ReadyEndpoints("10.3.0.1"))
	dns := newFakeResolver(map[string]string{"gds.test": "127.0.0.1"})
	c := newTestClient(t, Config{URLs: []string{f.url("gds.test")}, Resolver: dns})

	_, err := NewRouterCache(c, RouterCacheConfig{Filter: &gdsv1.ListRoutersRequest{}})
	assert.Error(t, err)
	_, err = NewRouterCache(c, RouterCacheConfig{Filter: &gdsv1.ListRoutersRequest{Clusterset: testClusterset}, DefaultScheme: "grpc"})
	assert.Error(t, err, "the default scheme is http or https")
	rc, err := NewRouterCache(c, RouterCacheConfig{Filter: &gdsv1.ListRoutersRequest{Clusterset: testClusterset}, TTL: time.Hour})
	require.NoError(t, err)
	updates := rc.Updates()
	runCache(t, rc.Run)
	require.NoError(t, rc.WaitSynced(testCtx(t)))
	st := rc.Snapshot()
	require.Len(t, st.Routers, 5)
	assert.Len(t, st.ByCluster("c"), 2)
	assert.Equal(t, []string{"https://10.1.0.1:50071", "https://10.1.0.2:50071"}, st.RouterSeedURLs(st.ByCluster("b")[0]),
		"one URL per ready replica, sorted")
	assert.Equal(t, []string{
		"https://10.0.0.1:50071",
		"https://10.1.0.1:50071",
		"https://10.1.0.2:50071",
		"http://[fd00::5]:50072",
	}, st.SeedURLs(), "a and b keep separate seeds; c/router2 has no replica; d has no gRPC port")
	assert.Equal(t, []string{"a", "b", "c"}, routerClusters(st.SeedRouters()))
	assert.Equal(t, st.SeedURLs(), st.RelayURLs(), "the relay seeds equal the pylon seeds today")

	vip := "https://" + api.DerivedServiceName("llm", "backend-router") + ".llm.svc.cluster.local:50071"
	assert.Equal(t, vip, st.DerivedServiceURL(st.ByCluster("a")[0]))
	assert.Equal(t, vip, st.DerivedServiceURL(st.ByCluster("b")[0]), "a and b share one import and its derived VIP")

	f.srv.PutExport(testClusterset, "e", "llm", "late", fakegds.RouterSpec(50074, 0, "https"), fakegds.ReadyEndpoints("10.4.0.1"))
	waitUpdate(t, updates)
	require.Eventually(t, func() bool { return len(rc.Snapshot().SeedURLs()) == 5 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "https://10.4.0.1:50074", rc.Snapshot().SeedURLs()[4])
	require.NoError(t, rc.Refresh(testCtx(t)))
	assert.NoError(t, rc.LastError())
}

func routerClusters(routers []*gdsv1.Router) []string {
	var out []string
	for _, r := range routers {
		out = append(out, r.GetCluster())
	}
	return out
}

func replica(address string, ready bool) *gdsv1.RouterReplica {
	return &gdsv1.RouterReplica{Address: address, Ready: ready}
}

func TestRouterStateRouterSeedURLs(t *testing.T) {
	r := &gdsv1.Router{Cluster: "a", Namespace: "ns", Name: "r", GrpcPort: 443, Replicas: []*gdsv1.RouterReplica{
		replica("10.0.0.2", true),
		replica("10.0.0.1", true),
		replica("10.0.0.1", true),
		replica("10.0.0.3", false),
		replica("2001:db8::1", true),
		replica("fe80::1%eth0", true),
		replica("router-0.example.internal", true),
		replica("bad:host", true),
		replica(" ", true),
	}}
	assert.Equal(t, []string{
		"https://10.0.0.1:443",
		"https://10.0.0.2:443",
		"https://[2001:db8::1]:443",
		"https://router-0.example.internal:443",
	}, (&RouterState{}).RouterSeedURLs(r), "ready replicas only, IPv6 bracketed, sorted, once each")

	st := &RouterState{DefaultScheme: "HTTP"}
	assert.Equal(t, "http://10.0.0.1:443", st.RouterSeedURLs(r)[0], "the default scheme applies without a dial scheme")
	r.DialScheme = " HTTPS "
	assert.Equal(t, "https://10.0.0.1:443", st.RouterSeedURLs(r)[0], "the router's dial scheme wins")
	r.DialScheme = "grpc"
	assert.Nil(t, st.RouterSeedURLs(r), "unknown dial scheme")
	assert.Nil(t, (&RouterState{DefaultScheme: "grpc"}).RouterSeedURLs(&gdsv1.Router{GrpcPort: 1, Replicas: r.Replicas}))
	assert.Nil(t, st.RouterSeedURLs(&gdsv1.Router{Namespace: "ns", Name: "r", Replicas: r.Replicas}), "no gRPC port")
	assert.Nil(t, st.RouterSeedURLs(&gdsv1.Router{GrpcPort: 1}), "no replica")
}

func TestRouterStateSeedURLsSkipExpiredClusters(t *testing.T) {
	expired := &gdsv1.Router{Cluster: "x", Namespace: "ns", Name: "dead", GrpcPort: 1, ClusterLeaseState: gdsv1.LeaseState_LEASE_STATE_EXPIRED,
		Replicas: []*gdsv1.RouterReplica{replica("10.9.0.1", true)}}
	alive := &gdsv1.Router{Cluster: "x", Namespace: "ns", Name: "zz", GrpcPort: 2, ClusterLeaseState: gdsv1.LeaseState_LEASE_STATE_ACTIVE,
		Replicas: []*gdsv1.RouterReplica{replica("10.9.0.2", true), replica("10.9.0.3", true)}}
	empty := &gdsv1.Router{Cluster: "y", Namespace: "ns", Name: "r", GrpcPort: 2}
	shared := &gdsv1.Router{Cluster: "z", Namespace: "ns", Name: "r", GrpcPort: 2, Replicas: []*gdsv1.RouterReplica{replica("10.9.0.2", true)}}
	st := &RouterState{Routers: []*gdsv1.Router{expired, alive, empty, shared}}
	assert.Equal(t, []string{"https://10.9.0.2:2", "https://10.9.0.3:2"}, st.SeedURLs(),
		"routers of expired clusters are skipped and a URL is listed once")
	assert.Equal(t, []*gdsv1.Router{alive, shared}, st.SeedRouters())
	assert.Empty(t, (&RouterState{}).SeedURLs())
}

func TestRouterStateDerivedServiceURL(t *testing.T) {
	r := &gdsv1.Router{Cluster: "a", Namespace: "ns", Name: "r", GrpcPort: 443, Replicas: []*gdsv1.RouterReplica{replica("10.0.0.1", true)}}
	name := api.DerivedServiceName("ns", "r")
	assert.Equal(t, "https://"+name+".ns.svc.cluster.local:443", (&RouterState{}).DerivedServiceURL(r))
	st := &RouterState{ClusterDomain: "corp.example.", DefaultScheme: "http"}
	assert.Equal(t, "http://"+name+".ns.svc.corp.example:443", st.DerivedServiceURL(r))
	r.DialScheme = "https"
	assert.Equal(t, "https://"+name+".ns.svc.corp.example:443", st.DerivedServiceURL(r))
	r.DialScheme = "grpc"
	assert.Empty(t, st.DerivedServiceURL(r))
	assert.Empty(t, st.DerivedServiceURL(&gdsv1.Router{Namespace: "ns", Name: "r"}), "no gRPC port")
	assert.Empty(t, st.DerivedServiceURL(&gdsv1.Router{GrpcPort: 1}), "no namespace and name")

	r.DialScheme = ""
	st = &RouterState{Routers: []*gdsv1.Router{r}}
	assert.Equal(t, []string{"https://10.0.0.1:443"}, st.SeedURLs(), "the derived VIP is not a seed")
}

func TestRouterStateRelayURLsAndQUICAddrs(t *testing.T) {
	r := &gdsv1.Router{
		Cluster: "a", Namespace: "ns", Name: "r", GrpcPort: 50071, QuicPort: 4433, DialScheme: "http",
		Ports: []*gdsv1.ServicePort{
			{Name: "grpc", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: 50071},
			{Name: "quic", Protocol: gdsv1.Protocol_PROTOCOL_UDP, Port: 4433},
			{Name: "metrics", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: 9090},
		},
		Replicas: []*gdsv1.RouterReplica{
			replica("10.0.0.2", true),
			replica("10.0.0.1", true),
			replica("10.0.0.1", true),
			replica("10.0.0.3", false),
			replica("2001:db8::1", true),
			replica("fe80::1%eth0", true),
			replica("bad:host", true),
		},
	}
	st := &RouterState{}
	assert.Equal(t, []string{"http://10.0.0.1:50071", "http://10.0.0.2:50071", "http://[2001:db8::1]:50071"}, st.RouterRelayURLs(r),
		"one gRPC URL per ready replica, sorted, once each")
	assert.Equal(t, st.RouterSeedURLs(r), st.RouterRelayURLs(r))
	assert.Equal(t, []string{"10.0.0.1:4433", "10.0.0.2:4433", "[2001:db8::1]:4433"}, st.RouterQUICAddrs(r),
		"one QUIC address per ready replica, IPv6 bracketed")
	assert.Len(t, r.GetPorts(), 3, "every Service port stays readable on the router")

	r.DialScheme = "grpc"
	assert.Nil(t, st.RouterRelayURLs(r), "unknown dial scheme")
	assert.Len(t, st.RouterQUICAddrs(r), 3, "QUIC addresses do not depend on the dial scheme")
	assert.Nil(t, st.RouterQUICAddrs(&gdsv1.Router{GrpcPort: 1, Replicas: r.Replicas}), "no QUIC port")
	assert.Nil(t, st.RouterQUICAddrs(&gdsv1.Router{QuicPort: 4433}), "no replica")
	assert.Nil(t, st.RouterRelayURLs(&gdsv1.Router{QuicPort: 4433, Replicas: r.Replicas}), "no gRPC port")

	expired := &gdsv1.Router{Cluster: "x", Namespace: "ns", Name: "dead", GrpcPort: 1, ClusterLeaseState: gdsv1.LeaseState_LEASE_STATE_EXPIRED,
		Replicas: []*gdsv1.RouterReplica{replica("10.9.0.1", true)}}
	alive := &gdsv1.Router{Cluster: "x", Namespace: "ns", Name: "zz", GrpcPort: 2, ClusterLeaseState: gdsv1.LeaseState_LEASE_STATE_ACTIVE,
		Replicas: []*gdsv1.RouterReplica{replica("10.9.0.2", true), replica("10.9.0.3", true)}}
	shared := &gdsv1.Router{Cluster: "z", Namespace: "ns", Name: "r", GrpcPort: 2, Replicas: []*gdsv1.RouterReplica{replica("10.9.0.2", true)}}
	st = &RouterState{Routers: []*gdsv1.Router{expired, alive, shared}, DefaultScheme: "http"}
	assert.Equal(t, []string{"http://10.9.0.2:2", "http://10.9.0.3:2"}, st.RelayURLs(),
		"routers of expired clusters are skipped and a URL is listed once")
	assert.Equal(t, st.SeedURLs(), st.RelayURLs())
	assert.Empty(t, (&RouterState{}).RelayURLs())
}
