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

package view

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/memory"
)

// agentRec returns a registration of agentID for role in pool (cluster,
// name), registered and renewed at renewed with a 60 s lease.
func agentRec(cluster, name string, role store.FrontendRole, agentID string, renewed time.Time) store.PoolAgentRecord {
	return store.PoolAgentRecord{
		Key: store.PoolAgentKey{
			ClusterSet: cs, Cluster: cluster, PoolID: registry.PoolID(cs, cluster, name), Role: role, AgentID: agentID,
		},
		PoolName:     name,
		Attributes:   map[string]string{"agent": agentID},
		Lease:        store.Lease{Epoch: uint64(renewed.UnixMilli()), TTL: time.Minute, RenewedAt: renewed},
		Generation:   1,
		RegisteredAt: renewed,
		UpdatedAt:    renewed,
	}
}

func agentOf(t *testing.T, p *gdsv1.Pool, role gdsv1.FrontendRole) *gdsv1.PoolAgent {
	t.Helper()
	for _, a := range p.GetAgents() {
		if a.GetRole() == role {
			return a
		}
	}
	require.Failf(t, "no agent", "role %s in %v", role, p.GetAgents())
	return nil
}

func TestCombinedFrontendAndPylonExport(t *testing.T) {
	f := newFixture(t)
	snap := store.ClusterSetSnapshot{
		ClusterSet: cs,
		Clusters:   []store.ClusterRecord{clusterRec("a", "us-east-1", t0)},
		Exports: []store.ExportRecord{
			exportRec("a", "dyn", "frontend", 1, t0, poolLabels("llama", api.RolePoolFrontend),
				map[string]string{api.AnnotationRoles: "pylon, pool-frontend"}, httpPort),
			exportRec("a", "dyn", "relay", 1, t0, poolLabels("llama", api.RoleKVRelay), nil, store.ServicePort{Name: "grpc", Port: 9000}),
		},
		EndpointSets: []store.EndpointSetRecord{
			endpointSet("a", "dyn", "frontend", 1, true, true, false),
			endpointSet("a", "dyn", "relay", 1, true),
		},
	}
	f.view.Replace(snap, t0)
	p := poolOf(t, f.view, registry.PoolID(cs, "a", "llama"))
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState())

	var roles []gdsv1.FrontendRole
	for _, s := range p.GetServices() {
		roles = append(roles, s.GetRole())
	}
	assert.Equal(t, []gdsv1.FrontendRole{
		gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON,
	}, roles, "one entry per (export, role), OPENAI first")
	require.Len(t, p.GetFrontends(), 3)
	assert.Equal(t, "http", p.GetFrontends()[0].GetPortName())
	assert.Equal(t, "grpc", p.GetFrontends()[1].GetPortName())
	assert.Empty(t, p.GetFrontends()[2].GetPortName(), "routers never dial a pylon")
	assert.Len(t, p.GetEndpoints(), 2, "one endpoint join per Service")
	assert.Equal(t, int32(2), p.GetReadyEndpointCount())
	assert.Equal(t, int32(2), p.GetReadyIngressCount(), "a Service in two ingress roles counts once")
	assert.True(t, p.GetKvRelayLive())

	require.Len(t, p.GetAgents(), 3)
	fe := p.GetAgents()[0]
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, fe.GetRole())
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_LABEL, fe.GetSource())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_INBOUND, fe.GetDialMode())
	assert.Equal(t, "http", fe.GetPortName())
	assert.Equal(t, "frontend", fe.GetService().GetName())
	assert.Equal(t, int32(3), fe.GetEndpointCount())
	assert.Equal(t, int32(2), fe.GetReadyEndpointCount())
	assert.True(t, fe.GetLive())
	assert.Nil(t, fe.GetLease())
	relay := p.GetAgents()[1]
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY, relay.GetRole())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, relay.GetDialMode())
	assert.Empty(t, relay.GetPortName(), "an outbound agent has no dial port")
	pylon := p.GetAgents()[2]
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON, pylon.GetRole())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, pylon.GetDialMode())
	assert.Equal(t, "frontend", pylon.GetService().GetName())
	assert.Equal(t, int32(2), pylon.GetReadyEndpointCount())

	// The cluster lease expiring takes every label-derived agent down.
	f.clock.Advance(33 * time.Second)
	f.view.EvaluateLeases()
	p = poolOf(t, f.view, registry.PoolID(cs, "a", "llama"))
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_UNHEALTHY, p.GetState())
	assert.False(t, p.GetKvRelayLive())
	assert.Zero(t, p.GetReadyIngressCount())
	for _, a := range p.GetAgents() {
		assert.False(t, a.GetLive(), a.GetRole().String())
		assert.Zero(t, a.GetReadyEndpointCount())
	}
	assert.Equal(t, int32(3), agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI).GetEndpointCount(), "endpoint counts stay")
}

func TestPylonOnlyLabelledPool(t *testing.T) {
	f := newFixture(t)
	snap := store.ClusterSetSnapshot{
		ClusterSet:   cs,
		Clusters:     []store.ClusterRecord{clusterRec("a", "us-east-1", t0)},
		Exports:      []store.ExportRecord{exportRec("a", "llm", "pylon", 1, t0, poolLabels("ie-1", api.RolePylon), nil, store.ServicePort{Name: "metrics", Port: 9090})},
		EndpointSets: []store.EndpointSetRecord{endpointSet("a", "llm", "pylon", 1, true, true)},
	}
	f.view.Replace(snap, t0)
	p := poolOf(t, f.view, registry.PoolID(cs, "a", "ie-1"))
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState())
	assert.Zero(t, p.GetReadyEndpointCount(), "no OPENAI frontend")
	assert.Equal(t, int32(2), p.GetReadyIngressCount())
	require.Len(t, p.GetFrontends(), 1)
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON, p.GetFrontends()[0].GetRole())
	require.Len(t, p.GetAgents(), 1)
	assert.True(t, p.GetAgents()[0].GetLive())
}

func TestRelayOnlyPoolStaysRegistered(t *testing.T) {
	f := newFixture(t)
	snap := store.ClusterSetSnapshot{
		ClusterSet:   cs,
		Clusters:     []store.ClusterRecord{clusterRec("a", "us-east-1", t0)},
		Exports:      []store.ExportRecord{exportRec("a", "dyn", "relay", 1, t0, poolLabels("kv", api.RoleKVRelay), nil, store.ServicePort{Name: "grpc", Port: 9000})},
		EndpointSets: []store.EndpointSetRecord{endpointSet("a", "dyn", "relay", 1, true)},
	}
	f.view.Replace(snap, t0)
	f.view.ApplyPoolAgent(agentRec("a", "kv", store.FrontendRoleKVRelay, "relay-0", t0))
	p := poolOf(t, f.view, registry.PoolID(cs, "a", "kv"))
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, p.GetState(), "a relay alone is not an ingress")
	assert.True(t, p.GetKvRelayLive())
	assert.Zero(t, p.GetReadyIngressCount())
	relay := agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY)
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_BOTH, relay.GetSource())
	assert.Equal(t, int32(1), relay.GetActiveRegistrationCount())
	assert.Equal(t, "relay-0", relay.GetAgentId())
}

func TestAgentOnlyPool(t *testing.T) {
	f := newFixture(t)
	id := registry.PoolID(cs, "c9", "ie-1")
	pod0 := agentRec("c9", "ie-1", store.FrontendRolePylon, "pod-0", t0)
	f.view.ApplyPoolAgent(pod0)
	p := poolOf(t, f.view, id)
	assert.Equal(t, "ie-1", p.GetName())
	assert.Equal(t, "c9", p.GetLocation().GetCluster())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState(), "a live ingress agent registration makes the pool live")
	assert.Equal(t, t0, p.GetRegisteredAt().AsTime())
	assert.Zero(t, p.GetReadyIngressCount())
	a := agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON)
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_API, a.GetSource())
	assert.Nil(t, a.GetService())
	assert.True(t, a.GetLive())
	assert.Equal(t, "pod-0", a.GetAgentId())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_ACTIVE, a.GetLease().GetState())

	// A second pod registers later; the agent mirrors the registration
	// whose lease expires last and lists both.
	f.clock.Advance(30 * time.Second)
	pod1 := agentRec("c9", "ie-1", store.FrontendRolePylon, "pod-1", f.clock.Now())
	f.view.ApplyPoolAgent(pod1)
	a = agentOf(t, poolOf(t, f.view, id), gdsv1.FrontendRole_FRONTEND_ROLE_PYLON)
	require.Len(t, a.GetRegistrations(), 2)
	assert.Equal(t, "pod-0", a.GetRegistrations()[0].GetAgentId())
	assert.Equal(t, "pod-1", a.GetRegistrations()[1].GetAgentId())
	assert.Equal(t, int32(2), a.GetActiveRegistrationCount())
	assert.Equal(t, "pod-1", a.GetAgentId())
	assert.Equal(t, map[string]string{"agent": "pod-1"}, a.GetAttributes())

	// pod-0 stops heartbeating; pod-1 keeps the pool live.
	f.clock.Advance(33 * time.Second)
	f.view.EvaluateLeases()
	p = poolOf(t, f.view, id)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState())
	a = agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON)
	assert.Equal(t, int32(1), a.GetActiveRegistrationCount())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, a.GetRegistrations()[0].GetLease().GetState())
	assert.Equal(t, 1.0, testutilValue(f.metrics.LeaseExpirations.WithLabelValues(metrics.LeasePoolAgent)))

	// Both expire: the pool was live, so it turns UNHEALTHY.
	f.clock.Advance(30 * time.Second)
	f.view.EvaluateLeases()
	p = poolOf(t, f.view, id)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_UNHEALTHY, p.GetState())
	assert.False(t, agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON).GetLive())

	// Deregistering one keeps the agent; deregistering the last removes
	// the pool.
	f.view.ApplyPoolAgentDelete(pod0.Key)
	assert.Len(t, agentOf(t, poolOf(t, f.view, id), gdsv1.FrontendRole_FRONTEND_ROLE_PYLON).GetRegistrations(), 1)
	f.view.ApplyPoolAgentDelete(pod1.Key)
	p = poolOf(t, f.view, id)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, p.GetState())
	assert.Empty(t, p.GetAgents())
}

func TestAgentJoinsLabelledPoolAndOverlay(t *testing.T) {
	f := newFixture(t)
	f.view.Replace(labelledPoolSnapshot(), t0)
	id := registry.PoolID(cs, "a", "llama")
	f.view.ApplyPoolAgent(agentRec("a", "llama", store.FrontendRoleOpenAI, "fe-0", t0))
	f.view.ApplyPoolAgent(agentRec("a", "llama", store.FrontendRolePylon, "pod-0", t0))
	p := poolOf(t, f.view, id)
	require.Len(t, p.GetAgents(), 3)
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_BOTH, agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI).GetSource())
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_LABEL, agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY).GetSource())
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_API, agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON).GetSource())

	// The cluster lease expires; the agent leases keep the pool live.
	f.clock.Advance(33 * time.Second)
	f.view.EvaluateLeases()
	p = poolOf(t, f.view, id)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState())
	assert.False(t, p.GetKvRelayLive(), "the relay has no registration")
	assert.True(t, agentOf(t, p, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI).GetLive())

	// An overlay drains the pool; its id wins.
	overlay := apiPool("custom", "a", "llama", store.Lease{})
	overlay.Spec.DesiredState = store.DesiredStateDraining
	f.view.ApplyPool(overlay)
	p = poolOf(t, f.view, "custom")
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_DRAINING, p.GetState())
	assert.Len(t, p.GetAgents(), 3)

	// Deleting the overlay's pool cascades to its agents only.
	f.view.ApplyPoolDelete(overlay.Key)
	assert.Len(t, f.view.PoolAgentRecords(cs, id), 2, "the agents are under the derived id")
	f.view.ApplyPoolDelete(store.PoolKey{ClusterSet: cs, Cluster: "a", PoolID: id})
	assert.Empty(t, f.view.PoolAgentRecords(cs, id))
	assert.Empty(t, f.view.Records(cs).PoolAgents)
}

func TestAgentRecordsFollowSnapshotsAndDeletes(t *testing.T) {
	f := newFixture(t)
	rec := agentRec("a", "ie-1", store.FrontendRolePylon, "pod-0", t0)
	snap := store.ClusterSetSnapshot{ClusterSet: cs, Clusters: []store.ClusterRecord{clusterRec("a", "us-east-1", t0)}, PoolAgents: []store.PoolAgentRecord{rec}}
	f.view.Replace(snap, t0)
	got, ok := f.view.PoolAgentRecord(cs, rec.Key.PoolID, store.FrontendRolePylon, "pod-0")
	require.True(t, ok)
	assert.Equal(t, rec, got)
	_, ok = f.view.PoolAgentRecord(cs, rec.Key.PoolID, store.FrontendRolePylon, "pod-1")
	assert.False(t, ok)
	_, ok = f.view.PoolAgentRecord("x", rec.Key.PoolID, store.FrontendRolePylon, "pod-0")
	assert.False(t, ok)
	assert.Nil(t, f.view.PoolAgentRecords("x", rec.Key.PoolID))

	// A local re-registration survives a snapshot that predates it.
	f.clock.Advance(time.Second)
	newer := rec
	newer.Generation = 2
	newer.Lease.Epoch++
	require.NoError(t, f.view.ApplyLocal(newer))
	f.view.Replace(snap, t0)
	got, _ = f.view.PoolAgentRecord(cs, rec.Key.PoolID, store.FrontendRolePylon, "pod-0")
	assert.Equal(t, uint64(2), got.Generation)

	// A local delete survives too, as does a cluster delete.
	f.view.ApplyPoolAgentDelete(rec.Key)
	f.view.Replace(snap, t0)
	_, ok = f.view.PoolAgentRecord(cs, rec.Key.PoolID, store.FrontendRolePylon, "pod-0")
	assert.False(t, ok)

	other := agentRec("b", "ie-2", store.FrontendRoleKVRelay, "relay-0", t0)
	f.view.ApplyPoolAgent(other)
	f.view.ApplyClusterDelete(store.ClusterKey{ClusterSet: cs, Cluster: "b"})
	snap.PoolAgents = append(snap.PoolAgents, other)
	f.view.Replace(snap, f.clock.Now())
	assert.Empty(t, f.view.PoolAgentRecords(cs, other.Key.PoolID), "a cluster delete hides the cluster's agents from an older snapshot")

	// After the window the store is the truth again.
	f.clock.Advance(time.Minute)
	f.view.Replace(snap, f.clock.Now())
	assert.Len(t, f.view.Records(cs).PoolAgents, 2)
}

func TestRouterPorts(t *testing.T) {
	f := newFixture(t)
	ports := []store.ServicePort{
		{Name: "grpc", Protocol: store.ProtocolTCP, Port: 50071},
		{Name: "tunnel", Protocol: store.ProtocolUDP, Port: 50072},
		{Name: "quic", Protocol: store.ProtocolUDP, Port: 50073},
	}
	snap := store.ClusterSetSnapshot{
		ClusterSet: cs,
		Clusters:   []store.ClusterRecord{clusterRec("a", "us-east-1", t0)},
		Exports: []store.ExportRecord{
			exportRec("a", "llm", "custom", 1, t0, map[string]string{api.LabelRole: api.RoleRouter},
				map[string]string{api.AnnotationQUICPortName: "tunnel"}, ports...),
			exportRec("a", "llm", "default", 1, t0, map[string]string{api.LabelRole: api.RoleRouter}, nil, ports...),
			exportRec("a", "llm", "noquic", 1, t0, map[string]string{api.LabelRole: api.RoleRouter}, nil, ports[0]),
		},
	}
	f.view.Replace(snap, t0)
	router := func(name string) *gdsv1.Router {
		o, ok := f.view.Get(cs, KindRouter, RouterKey("a", "llm", name))
		require.True(t, ok)
		return o.Full.(*gdsv1.Router)
	}
	assert.Equal(t, int32(50072), router("custom").GetQuicPort())
	assert.Equal(t, int32(50073), router("default").GetQuicPort())
	assert.Zero(t, router("noquic").GetQuicPort())
	got := router("custom").GetPorts()
	require.Len(t, got, 3)
	assert.True(t, proto.Equal(&gdsv1.ServicePort{Name: "tunnel", Protocol: gdsv1.Protocol_PROTOCOL_UDP, Port: 50072}, got[1]))
}

func TestCompactionOfAgents(t *testing.T) {
	f := newFixture(t)
	st := &faultyStore{Store: memory.New()}
	ctx := context.Background()
	old := t0.Add(-25 * time.Hour)
	_, err := st.PutCluster(ctx, clusterRec("b", "us-west-2", t0), t0)
	require.NoError(t, err)
	put := func(rec store.PoolAgentRecord, now time.Time, mint bool) {
		t.Helper()
		_, err := st.PutPoolAgent(ctx, rec, store.PutPoolAgentOptions{Now: now, MintLease: mint})
		require.NoError(t, err)
	}
	// A dead registration, a dead one without a lease, and a live one whose
	// dead pool record must stay.
	put(agentRec("b", "dead", store.FrontendRolePylon, "pod-0", old), old, true)
	put(agentRec("b", "dead", store.FrontendRolePylon, "pod-1", old), old, false)
	put(agentRec("b", "live", store.FrontendRolePylon, "pod-0", t0), t0, true)
	livePool := apiPool(registry.PoolID(cs, "b", "live"), "b", "live", store.Lease{TTL: time.Minute})
	_, err = st.PutPool(ctx, livePool, store.PutPoolOptions{Now: old, MintLease: true})
	require.NoError(t, err)

	require.NoError(t, NewRefresher(f.view, st, RefresherOptions{}).Sync(ctx))
	c := NewCompactor(f.view, st, CompactorOptions{Retention: 24 * time.Hour, LeaseSkew: 2 * time.Second, Metrics: f.metrics})
	require.NoError(t, c.Compact(ctx))

	snap, err := st.LoadClusterSet(ctx, cs)
	require.NoError(t, err)
	require.Len(t, snap.PoolAgents, 1)
	assert.Equal(t, "live", snap.PoolAgents[0].PoolName)
	require.Len(t, snap.Pools, 1, "a pool with a live agent is kept")
	assert.Len(t, f.view.Records(cs).PoolAgents, 1, "the view follows")
	assert.Equal(t, 2.0, testutilValue(f.metrics.CompactionDeleted.WithLabelValues(metrics.RecordPoolAgent)))
}

func TestPoolRolesAndOrder(t *testing.T) {
	assert.Equal(t, []store.FrontendRole{store.FrontendRoleOpenAI, store.FrontendRolePylon},
		poolRoles(map[string]string{api.LabelRole: api.RoleRouter}, map[string]string{api.AnnotationRoles: "pool-frontend, pylon, router"}),
		"the roles annotation replaces the label")
	assert.Equal(t, []store.FrontendRole{store.FrontendRoleKVRelay}, poolRoles(map[string]string{api.LabelRole: api.RoleKVRelay}, nil))
	assert.Empty(t, poolRoles(map[string]string{api.LabelRole: api.RoleRouter}, nil))

	assert.Less(t, roleRank(store.FrontendRoleOpenAI), roleRank(store.FrontendRoleKVRelay))
	assert.Less(t, roleRank(store.FrontendRoleKVRelay), roleRank(store.FrontendRolePylon))
	assert.Equal(t, math.MaxInt, roleRank("ROUTER"))
	assert.Empty(t, frontendPortName(nil, store.ServiceRef{Name: "x", Role: store.FrontendRoleOpenAI}))

	a := agentRec("a", "p", store.FrontendRolePylon, "pod-0", t0)
	b := a
	assert.False(t, newerPoolAgent(a, b))
	b.Generation++
	assert.True(t, newerPoolAgent(b, a))
	b = a
	b.Lease.Epoch++
	assert.True(t, newerPoolAgent(b, a))
	b = a
	b.Lease.RenewedAt = t0.Add(time.Second)
	assert.True(t, newerPoolAgent(b, a))
	b = a
	b.UpdatedAt = t0.Add(time.Second)
	assert.True(t, newerPoolAgent(b, a))
}
