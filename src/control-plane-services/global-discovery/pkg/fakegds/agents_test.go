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

package fakegds_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/fakegds"
)

const (
	openai  = gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI
	kvRelay = gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY
	pylon   = gdsv1.FrontendRole_FRONTEND_ROLE_PYLON

	registered = gdsv1.PoolState_POOL_STATE_REGISTERED
	active     = gdsv1.PoolState_POOL_STATE_ACTIVE
	unhealthy  = gdsv1.PoolState_POOL_STATE_UNHEALTHY
)

// getPool returns a pool with its endpoint lists.
func getPool(t *testing.T, c clients, id string) *gdsv1.Pool {
	t.Helper()
	resp, err := c.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: id, IncludeEndpoints: true})
	require.NoError(t, err)
	return resp.GetPool()
}

// agentOf returns the agent of a role, or nil.
func agentOf(p *gdsv1.Pool, role gdsv1.FrontendRole) *gdsv1.PoolAgent {
	for _, a := range p.GetAgents() {
		if a.GetRole() == role {
			return a
		}
	}
	return nil
}

func roles(p *gdsv1.Pool) []gdsv1.FrontendRole {
	var out []gdsv1.FrontendRole
	for _, a := range p.GetAgents() {
		out = append(out, a.GetRole())
	}
	return out
}

func registerAgent(t *testing.T, c clients, cluster, pool string, role gdsv1.FrontendRole, agentID string) *gdsv1.RegisterPoolAgentResponse {
	t.Helper()
	resp, err := c.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: cluster, PoolName: pool,
		Agent: &gdsv1.PoolAgent{Role: role, AgentId: agentID},
	})
	require.NoError(t, err)
	return resp
}

func requireReason(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, status.Code(err), "%v", err)
	info := errorInfo(t, err)
	assert.Equal(t, reason, info.GetReason())
	assert.Equal(t, api.ErrorDomain, info.GetDomain())
}

func TestRoutersQUICPortAndPorts(t *testing.T) {
	srv, c := start(t)
	srv.PutExport(cs, "a", "llm", "router", fakegds.RouterSpecWithQUIC(50071, 8080, "http", 4433), fakegds.ReadyEndpoints("10.0.0.1"))
	custom := fakegds.RouterSpecWithQUIC(50071, 0, "", 5000)
	custom.Ports[1].Name = "tunnel"
	custom.Ports[0].AppProtocol = "kubernetes.io/h2c"
	custom.Annotations = map[string]string{api.AnnotationQUICPortName: "tunnel"}
	srv.PutExport(cs, "b", "llm", "router", custom, nil)
	srv.PutExport(cs, "c", "llm", "router", fakegds.RouterSpec(50071, 0, ""), nil)

	routers, err := c.pools.ListRouters(ctx(t), &gdsv1.ListRoutersRequest{Clusterset: cs})
	require.NoError(t, err)
	require.Len(t, routers.GetRouters(), 3)

	a := routers.GetRouters()[0]
	assert.Equal(t, int32(50071), a.GetGrpcPort())
	assert.Equal(t, int32(8080), a.GetHttpPort())
	assert.Equal(t, int32(4433), a.GetQuicPort())
	require.Len(t, a.GetPorts(), 3)
	assert.Equal(t, api.DefaultQUICPortName, a.GetPorts()[2].GetName())
	assert.Equal(t, gdsv1.Protocol_PROTOCOL_UDP, a.GetPorts()[2].GetProtocol())
	assert.Equal(t, int32(4433), a.GetPorts()[2].GetPort())
	assert.Equal(t, "http", a.GetDialScheme())

	b := routers.GetRouters()[1]
	assert.Equal(t, int32(5000), b.GetQuicPort(), "the port named by quic-port-name")
	require.Len(t, b.GetPorts(), 2)
	assert.Equal(t, "kubernetes.io/h2c", b.GetPorts()[0].GetAppProtocol())
	assert.Empty(t, b.GetDialScheme(), "the dial scheme is passed through as annotated")

	c3 := routers.GetRouters()[2]
	assert.Zero(t, c3.GetQuicPort())
	require.Len(t, c3.GetPorts(), 1)
	assert.Equal(t, api.DefaultGRPCPortName, c3.GetPorts()[0].GetName())
}

func TestRoutersDirectMode(t *testing.T) {
	srv, c := start(t)
	srv.EnsureCluster(cs, "a", "us-east-1")
	// A StatefulSet keeps hostnames; a Deployment's pods have only their
	// names as targetRef.name; the third endpoint has neither.
	eps := fakegds.WithPodNames(
		fakegds.WithHostnames(fakegds.ReadyEndpoints("10.0.0.1", "10.0.0.2", "10.0.0.3"), "stargate-0"),
		"stargate-0-pod", "stargate-7f9c-abcde")
	srv.PutExport(cs, "a", "llm", "stargate", fakegds.RouterSpecDirect(50071, 50072, "http", "{pod_name}.stargate.example.com"), eps)
	srv.PutExport(cs, "a", "llm", "backend", fakegds.WithRouterMode(fakegds.RouterSpec(50071, 0, ""), " Backend-Router ", ""), nil)
	srv.PutExport(cs, "a", "llm", "legacy", fakegds.RouterSpec(50071, 0, ""), nil)
	srv.PutExport(cs, "a", "llm", "unknown", fakegds.WithRouterMode(fakegds.RouterSpec(50071, 0, ""), "k8s-router", " raw "), nil)

	routers, err := c.pools.ListRouters(ctx(t), &gdsv1.ListRoutersRequest{Clusterset: cs})
	require.NoError(t, err)
	require.Len(t, routers.GetRouters(), 4)
	byName := map[string]*gdsv1.Router{}
	for _, r := range routers.GetRouters() {
		byName[r.GetName()] = r
	}

	d := byName["stargate"]
	assert.Equal(t, gdsv1.RouterMode_ROUTER_MODE_DIRECT, d.GetRouterMode())
	assert.Equal(t, "{pod_name}.stargate.example.com", d.GetHostnameTemplate())
	assert.Equal(t, api.RouterModeDirect, d.GetAnnotations()[api.AnnotationRouterMode])
	assert.Equal(t, gdsv1.SessionAffinity_SESSION_AFFINITY_NONE, fakegds.RouterSpecDirect(1, 0, "", "").GetSessionAffinity())
	assert.Equal(t, int32(50071), d.GetGrpcPort())
	assert.Equal(t, int32(50072), d.GetQuicPort())
	assert.Zero(t, d.GetHttpPort())
	require.Len(t, d.GetReplicas(), 3)
	assert.Equal(t, "stargate-0", d.GetReplicas()[0].GetIdentity(), "the hostname wins over targetRef.name")
	assert.Equal(t, "stargate-7f9c-abcde", d.GetReplicas()[1].GetIdentity())
	assert.Empty(t, d.GetReplicas()[1].GetHostname())
	assert.Empty(t, d.GetReplicas()[2].GetIdentity())
	assert.Equal(t, "stargate-0.stargate.example.com:50071",
		api.ReplicaAuthority(d.GetHostnameTemplate(), d.GetReplicas()[0].GetIdentity(), d.GetGrpcPort()))

	assert.Equal(t, gdsv1.RouterMode_ROUTER_MODE_BACKEND_ROUTER, byName["backend"].GetRouterMode())
	assert.Empty(t, byName["backend"].GetHostnameTemplate())
	assert.Equal(t, gdsv1.RouterMode_ROUTER_MODE_UNSPECIFIED, byName["legacy"].GetRouterMode())
	assert.Equal(t, gdsv1.RouterMode_ROUTER_MODE_UNSPECIFIED, byName["unknown"].GetRouterMode())
	assert.Equal(t, " raw ", byName["unknown"].GetHostnameTemplate(), "the template is passed through verbatim")

	// Clearing the mode removes the annotations.
	spec := fakegds.WithRouterMode(fakegds.RouterSpecDirect(50071, 0, "", "{pod_name}.x"), "", "")
	assert.Empty(t, spec.GetAnnotations())
	assert.Len(t, fakegds.WithHostnames(fakegds.ReadyEndpoints("10.0.0.1"), "a", "b"), 1, "extra names are ignored")
}

func TestPylonExportPool(t *testing.T) {
	srv, c := start(t, fakegds.WithRefreshInterval(0))
	srv.EnsureCluster(cs, "a", "us-east-1")
	spec := fakegds.PylonSpec("blue", 9090, "llama-3")
	spec.Labels["team"] = "llm"
	srv.PutExport(cs, "a", "pools", "blue-pylon", spec, []*gdsv1.Endpoint{})
	id := fakegds.LabelPoolID(cs, "a", "blue")

	p := getPool(t, c, id)
	assert.Equal(t, registered, p.GetState(), "no ready Pylon yet")
	assert.Equal(t, "blue", p.GetName())
	assert.Empty(t, p.GetFrontends(), "a Pylon-only pool has no dispatch entry")
	assert.Equal(t, "llm", p.GetLabels()["team"], "labels come from the Pylon export")
	require.Len(t, p.GetModels(), 1)
	require.Len(t, p.GetServices(), 1)
	assert.Equal(t, pylon, p.GetServices()[0].GetRole())
	require.Equal(t, []gdsv1.FrontendRole{pylon}, roles(p))
	ag := p.GetAgents()[0]
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_LABEL, ag.GetSource())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, ag.GetDialMode())
	assert.Equal(t, "blue-pylon", ag.GetService().GetName())
	assert.Equal(t, pylon, ag.GetService().GetRole())
	assert.Empty(t, ag.GetPortName())
	assert.Nil(t, ag.GetLease())
	assert.False(t, ag.GetLive())

	srv.SetEndpoints(cs, "a", "pools", "blue-pylon", append(fakegds.ReadyEndpoints("10.0.1.1", "10.0.1.2"),
		&gdsv1.Endpoint{Addresses: []string{"10.0.1.3"}, Conditions: &gdsv1.EndpointConditions{}}))
	p = getPool(t, c, id)
	assert.Equal(t, active, p.GetState())
	assert.Zero(t, p.GetReadyEndpointCount(), "ready_endpoint_count counts OPENAI only")
	assert.Equal(t, int32(2), p.GetReadyIngressCount())
	assert.False(t, p.GetKvRelayLive())
	ag = p.GetAgents()[0]
	assert.Equal(t, int32(3), ag.GetEndpointCount())
	assert.Equal(t, int32(2), ag.GetReadyEndpointCount())
	assert.True(t, ag.GetLive())
	require.Len(t, p.GetEndpoints(), 1)
	assert.Len(t, p.GetEndpoints()[0].GetEndpoints(), 3)

	// The pylon role selects the pool for a model like any other.
	list, err := c.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, Model: "llama-3"})
	require.NoError(t, err)
	assert.Len(t, list.GetPools(), 1)
}

func TestCombinedRolesAnnotation(t *testing.T) {
	srv, c := start(t, fakegds.WithRefreshInterval(0))
	srv.EnsureCluster(cs, "a", "")
	spec := fakegds.WithRoles(fakegds.PoolFrontendSpec("blue", 8000, "llama-3"), api.RolePylon, " pool-frontend", "router", api.RolePylon)
	spec.Annotations[api.AnnotationHTTPPortName] = "openai"
	srv.PutExport(cs, "a", "pools", "frontend", spec, fakegds.ReadyEndpoints("10.0.0.1", "10.0.0.2"))

	p := getPool(t, c, fakegds.LabelPoolID(cs, "a", "blue"))
	assert.Equal(t, active, p.GetState())
	require.Equal(t, []gdsv1.FrontendRole{openai, pylon}, roles(p), "one agent per role, sorted by role")
	fe, py := p.GetAgents()[0], p.GetAgents()[1]
	assert.Equal(t, "frontend", fe.GetService().GetName())
	assert.Equal(t, "frontend", py.GetService().GetName())
	assert.Equal(t, openai, fe.GetService().GetRole())
	assert.Equal(t, pylon, py.GetService().GetRole())
	assert.Equal(t, "openai", fe.GetPortName())
	assert.Empty(t, py.GetPortName())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_INBOUND, fe.GetDialMode())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, py.GetDialMode())
	for _, ag := range p.GetAgents() {
		assert.Equal(t, int32(2), ag.GetReadyEndpointCount())
		assert.True(t, ag.GetLive())
	}
	assert.Equal(t, int32(2), p.GetReadyEndpointCount())
	assert.Equal(t, int32(2), p.GetReadyIngressCount(), "one Service with two ingress roles counts once")
	require.Len(t, p.GetServices(), 1)
	assert.Equal(t, openai, p.GetServices()[0].GetRole(), "the frontend role comes first")
	require.Len(t, p.GetFrontends(), 1)
	assert.Equal(t, "openai", p.GetFrontends()[0].GetPortName())
	require.Len(t, p.GetEndpoints(), 1)

	// A separate relay joins as the third agent; its endpoints are not
	// ingress.
	srv.PutExport(cs, "a", "pools", "relay", fakegds.KVRelaySpec("blue", 50051), fakegds.ReadyEndpoints("10.0.0.9"))
	p = getPool(t, c, p.GetPoolId())
	require.Equal(t, []gdsv1.FrontendRole{openai, kvRelay, pylon}, roles(p))
	relay := agentOf(p, kvRelay)
	assert.Equal(t, api.DefaultGRPCPortName, relay.GetPortName())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, relay.GetDialMode())
	assert.True(t, p.GetKvRelayLive())
	assert.Equal(t, int32(2), p.GetReadyIngressCount())
	assert.Len(t, p.GetServices(), 2)

	// Without the annotation the label alone counts again.
	srv.PutExport(cs, "a", "pools", "frontend", fakegds.PoolFrontendSpec("blue", 8000), nil)
	p = getPool(t, c, p.GetPoolId())
	assert.Equal(t, []gdsv1.FrontendRole{openai, kvRelay}, roles(p))
}

func TestRelayOnlyPoolStaysRegistered(t *testing.T) {
	srv, c := start(t, fakegds.WithRefreshInterval(0))
	srv.EnsureCluster(cs, "a", "")
	srv.PutExport(cs, "a", "pools", "relay", fakegds.KVRelaySpec("blue", 50051), fakegds.ReadyEndpoints("10.0.0.9"))
	id := fakegds.LabelPoolID(cs, "a", "blue")

	p := getPool(t, c, id)
	assert.Equal(t, registered, p.GetState(), "a live relay alone does not make the pool routable")
	assert.True(t, p.GetKvRelayLive())
	assert.Zero(t, p.GetReadyEndpointCount())
	assert.Zero(t, p.GetReadyIngressCount())
	relay := agentOf(p, kvRelay)
	require.NotNil(t, relay)
	assert.True(t, relay.GetLive())
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_LABEL, relay.GetSource())

	// An API-registered relay does not make it ACTIVE either.
	registerAgent(t, c, "a", "blue", kvRelay, "relay-0")
	p = getPool(t, c, id)
	assert.Equal(t, registered, p.GetState())
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_BOTH, agentOf(p, kvRelay).GetSource())

	// A ready frontend does.
	srv.PutExport(cs, "a", "pools", "frontend", fakegds.PoolFrontendSpec("blue", 8000), fakegds.ReadyEndpoints("10.0.0.1"))
	p = getPool(t, c, id)
	assert.Equal(t, active, p.GetState())
	assert.Equal(t, int32(1), p.GetReadyIngressCount())

	// Once started, losing the frontend is UNHEALTHY, not REGISTERED.
	srv.DeleteExport(cs, "a", "pools", "frontend")
	p = getPool(t, c, id)
	assert.Equal(t, unhealthy, p.GetState())
	assert.True(t, p.GetKvRelayLive())
}

func TestAgentOnlyPool(t *testing.T) {
	clk := newClock()
	_, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0))

	// The relay registers first: the pool exists, known but not routable.
	relay, err := c.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: "a", PoolName: "blue", LeaseTtl: durationpb.New(30 * time.Second),
		Agent: &gdsv1.PoolAgent{Role: kvRelay, AgentId: "relay-0", Attributes: map[string]string{"kv_block_size": "16"},
			Source: gdsv1.AgentSource_AGENT_SOURCE_LABEL, Live: true},
	})
	require.NoError(t, err)
	id := relay.GetPoolId()
	assert.Equal(t, fakegds.LabelPoolID(cs, "a", "blue"), id, "the deterministic pool id")
	assert.Equal(t, registered, relay.GetPoolState())
	assert.Equal(t, clk.Now(), relay.GetServerTime().AsTime())
	ra := relay.GetAgent()
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_API, ra.GetSource(), "server-owned fields of the request are ignored")
	assert.Equal(t, "relay-0", ra.GetAgentId())
	assert.Equal(t, "16", ra.GetAttributes()["kv_block_size"])
	assert.Equal(t, uint64(clk.Now().UnixMilli()), ra.GetLease().GetEpoch())
	assert.Equal(t, 30*time.Second, ra.GetLease().GetTtl().AsDuration())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_ACTIVE, ra.GetLease().GetState())
	assert.Equal(t, uint64(clk.Now().UnixMilli()), ra.GetGeneration(), "an unset generation is assigned")
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, ra.GetDialMode())
	assert.Nil(t, ra.GetService())
	assert.True(t, ra.GetLive())
	assert.Equal(t, int32(1), ra.GetActiveRegistrationCount())
	require.Len(t, ra.GetRegistrations(), 1)
	assert.Equal(t, relay.GetRegistration(), ra.GetRegistrations()[0], "the response carries this process's registration")
	assert.Equal(t, "relay-0", relay.GetRegistration().GetAgentId())
	assert.Equal(t, ra.GetLease(), relay.GetRegistration().GetLease())

	p := getPool(t, c, id)
	assert.Equal(t, "blue", p.GetName())
	assert.Equal(t, "a", p.GetLocation().GetCluster())
	assert.Empty(t, p.GetLocation().GetRegion(), "the cluster is not registered")
	assert.Nil(t, p.GetLease(), "an agent-only pool has no pool lease")
	assert.True(t, p.GetKvRelayLive())
	assert.Equal(t, clk.Now(), p.GetRegisteredAt().AsTime())

	// Pylon registers: an ingress agent lease makes the pool ACTIVE.
	clk.Advance(time.Second)
	py := registerAgent(t, c, "a", "blue", pylon, "pylon-0")
	assert.Equal(t, id, py.GetPoolId())
	assert.Equal(t, active, py.GetPoolState())
	p = getPool(t, c, id)
	assert.Equal(t, active, p.GetState())
	assert.Equal(t, []gdsv1.FrontendRole{kvRelay, pylon}, roles(p))
	assert.Zero(t, p.GetReadyIngressCount(), "no endpoints without an export")
	assert.Equal(t, 60*time.Second, agentOf(p, pylon).GetLease().GetTtl().AsDuration(), "default agent TTL")
	assert.Equal(t, clk.Now(), p.GetUpdatedAt().AsTime())

	// Pylon's lease expires while the relay heartbeats: UNHEALTHY.
	var hb *gdsv1.HeartbeatPoolAgentResponse
	for range 2 {
		clk.Advance(25 * time.Second)
		hb, err = c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
			Clusterset: cs, PoolId: id, Role: kvRelay, AgentId: "relay-0", Epoch: ra.GetLease().GetEpoch(),
		})
		require.NoError(t, err)
		assert.Equal(t, clk.Now(), hb.GetLease().GetRenewedAt().AsTime())
		assert.Equal(t, active, hb.GetPoolState())
	}
	clk.Advance(15 * time.Second)
	p = getPool(t, c, id)
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, agentOf(p, pylon).GetLease().GetState())
	assert.False(t, agentOf(p, pylon).GetLive())
	assert.True(t, p.GetKvRelayLive())
	assert.Equal(t, unhealthy, p.GetState())
	assert.Equal(t, "16", agentOf(p, kvRelay).GetAttributes()["kv_block_size"], "empty attributes keep the stored ones")

	// A heartbeat with the current epoch revives it and replaces the
	// attributes.
	hb, err = c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-0", Epoch: py.GetAgent().GetLease().GetEpoch(),
		Attributes: map[string]string{"inference_server_id": "pylon-0"},
	})
	require.NoError(t, err)
	assert.Equal(t, active, hb.GetPoolState())
	assert.Equal(t, map[string]string{"inference_server_id": "pylon-0"}, agentOf(getPool(t, c, id), pylon).GetAttributes())

	// Re-registration of the same agent id mints a new epoch, keeps
	// registered_at and replaces the attributes.
	clk.Advance(time.Second)
	again := registerAgent(t, c, "a", "blue", pylon, "pylon-0")
	assert.Greater(t, again.GetRegistration().GetLease().GetEpoch(), py.GetRegistration().GetLease().GetEpoch())
	assert.Equal(t, py.GetRegistration().GetRegisteredAt().AsTime(), again.GetRegistration().GetRegisteredAt().AsTime())
	assert.Empty(t, again.GetRegistration().GetAttributes(), "registration replaces the attributes")
	assert.Len(t, again.GetAgent().GetRegistrations(), 1, "idempotent on the agent id")
	_, err = c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-0", Epoch: py.GetRegistration().GetLease().GetEpoch(),
	})
	requireReason(t, err, codes.Aborted, api.ReasonLeaseEpochMismatch)

	// Deregistering Pylon leaves the relay: UNHEALTHY, since the pool has
	// been live. Deregistering the relay removes the pool.
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-0"})
	require.NoError(t, err)
	assert.Equal(t, unhealthy, getPool(t, c, id).GetState())
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: id, Role: kvRelay, AgentId: "relay-0"})
	require.NoError(t, err)
	_, err = c.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: id})
	requireReason(t, err, codes.NotFound, api.ReasonNotFound)
}

func TestAgentMergesWithExportAndRegisteredPool(t *testing.T) {
	clk := newClock()
	srv, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0))
	srv.EnsureCluster(cs, "a", "us-east-1")
	srv.PutExport(cs, "a", "pools", "blue-pylon", fakegds.PylonSpec("blue", 9090), []*gdsv1.Endpoint{})
	id := fakegds.LabelPoolID(cs, "a", "blue")
	assert.Equal(t, registered, getPool(t, c, id).GetState())

	// The registration joins the label-derived pool and makes it ACTIVE
	// by its lease, without a ready endpoint.
	resp, err := c.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: "a", PoolName: "blue", PoolId: id, Generation: 7,
		Agent: &gdsv1.PoolAgent{Role: pylon, AgentId: "pylon-0", Attributes: map[string]string{"models": "llama-3"}},
	})
	require.NoError(t, err)
	assert.Equal(t, id, resp.GetPoolId())
	assert.Equal(t, active, resp.GetPoolState())
	ag := resp.GetAgent()
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_BOTH, ag.GetSource())
	assert.Equal(t, "blue-pylon", ag.GetService().GetName())
	assert.Equal(t, "pylon-0", ag.GetAgentId())
	assert.Equal(t, uint64(7), ag.GetGeneration())
	assert.Zero(t, ag.GetReadyEndpointCount())
	assert.True(t, ag.GetLive())
	p := getPool(t, c, id)
	assert.Equal(t, "us-east-1", p.GetLocation().GetRegion())
	assert.Len(t, p.GetAgents(), 1)

	// RegisterPool without a pool id joins the same pool.
	reg, err := c.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{
		Clusterset: cs, Generation: 1, Pool: &gdsv1.Pool{Name: "blue", Location: &gdsv1.Location{Cluster: "a"}, WorkerCount: 4},
	})
	require.NoError(t, err)
	assert.Equal(t, id, reg.GetPool().GetPoolId())
	assert.Equal(t, int32(4), reg.GetPool().GetWorkerCount())
	assert.Len(t, reg.GetPool().GetAgents(), 1)
	assert.Equal(t, active, reg.GetPool().GetState())

	// DeregisterPool removes the overlay and the agents; the export stays,
	// and the pool, live once, is UNHEALTHY without a ready Pylon.
	_, err = c.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err)
	p = getPool(t, c, id)
	assert.Equal(t, unhealthy, p.GetState())
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_LABEL, agentOf(p, pylon).GetSource())
	_, err = c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-0", Epoch: ag.GetLease().GetEpoch(),
	})
	requireReason(t, err, codes.NotFound, api.ReasonNotFound)

	// Removing the cluster removes its agents.
	registerAgent(t, c, "a", "green", pylon, "pylon-0")
	srv.RemoveCluster(cs, "a")
	list, err := c.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs})
	require.NoError(t, err)
	assert.Empty(t, list.GetPools())
}

func TestPoolAgentErrors(t *testing.T) {
	_, c := start(t, fakegds.WithRefreshInterval(0))
	valid := func() *gdsv1.RegisterPoolAgentRequest {
		return &gdsv1.RegisterPoolAgentRequest{
			Clusterset: cs, Cluster: "a", PoolName: "blue",
			Agent: &gdsv1.PoolAgent{Role: pylon, AgentId: "pylon-0"},
		}
	}
	invalid := map[string]func(*gdsv1.RegisterPoolAgentRequest){
		"no clusterset":     func(r *gdsv1.RegisterPoolAgentRequest) { r.Clusterset = "" },
		"no cluster":        func(r *gdsv1.RegisterPoolAgentRequest) { r.Cluster = "" },
		"no pool name":      func(r *gdsv1.RegisterPoolAgentRequest) { r.PoolName = "" },
		"pool name not DNS": func(r *gdsv1.RegisterPoolAgentRequest) { r.PoolName = "Blue_Pool" },
		"bad pool id":       func(r *gdsv1.RegisterPoolAgentRequest) { r.PoolId = "-x" },
		"no agent":          func(r *gdsv1.RegisterPoolAgentRequest) { r.Agent = nil },
		"no role":           func(r *gdsv1.RegisterPoolAgentRequest) { r.Agent.Role = gdsv1.FrontendRole_FRONTEND_ROLE_UNSPECIFIED },
		"unknown role":      func(r *gdsv1.RegisterPoolAgentRequest) { r.Agent.Role = 9 },
		"no agent id":       func(r *gdsv1.RegisterPoolAgentRequest) { r.Agent.AgentId = "" },
		"agent id too long": func(r *gdsv1.RegisterPoolAgentRequest) { r.Agent.AgentId = strings.Repeat("x", api.MaxAgentIDBytes+1) },
		"too many attributes": func(r *gdsv1.RegisterPoolAgentRequest) {
			r.Agent.Attributes = map[string]string{}
			for i := range api.MaxAgentAttributes + 1 {
				r.Agent.Attributes[strings.Repeat("k", i+1)] = "v"
			}
		},
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			req := valid()
			mutate(req)
			_, err := c.pools.RegisterPoolAgent(ctx(t), req)
			requireReason(t, err, codes.InvalidArgument, api.ReasonInvalidArgument)
		})
	}
	req := valid()
	req.PoolName = "max"
	req.Agent.AgentId = strings.Repeat("x", api.MaxAgentIDBytes)
	_, err := c.pools.RegisterPoolAgent(ctx(t), req)
	require.NoError(t, err, "an agent id of the maximum length")

	// Generations.
	req = valid()
	req.Generation = 10
	first, err := c.pools.RegisterPoolAgent(ctx(t), req)
	require.NoError(t, err)
	id := first.GetPoolId()
	_, err = c.pools.RegisterPoolAgent(ctx(t), req)
	requireReason(t, err, codes.FailedPrecondition, api.ReasonStaleGeneration)
	assert.Equal(t, "10", errorInfo(t, err).GetMetadata()[api.MetadataCurrentGeneration])
	req.Generation = 0
	again, err := c.pools.RegisterPoolAgent(ctx(t), req)
	require.NoError(t, err)
	assert.Greater(t, again.GetAgent().GetGeneration(), uint64(10))

	// Pool ids.
	req = valid()
	req.PoolId = "other"
	_, err = c.pools.RegisterPoolAgent(ctx(t), req)
	requireReason(t, err, codes.AlreadyExists, api.ReasonPoolNameTaken)
	_, err = c.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{
		Clusterset: cs, Generation: 1, Pool: &gdsv1.Pool{PoolId: "other", Name: "blue", Location: &gdsv1.Location{Cluster: "a"}},
	})
	requireReason(t, err, codes.AlreadyExists, api.ReasonPoolNameTaken)
	req = valid()
	req.PoolName, req.PoolId = "green", "custom-green"
	green, err := c.pools.RegisterPoolAgent(ctx(t), req)
	require.NoError(t, err)
	assert.Equal(t, "custom-green", green.GetPoolId(), "an explicit id names a new pool")
	req.PoolName, req.PoolId = "red", id
	_, err = c.pools.RegisterPoolAgent(ctx(t), req)
	requireReason(t, err, codes.AlreadyExists, api.ReasonPoolNameTaken)
	req.PoolId = ""
	red, err := c.pools.RegisterPoolAgent(ctx(t), req)
	require.NoError(t, err)
	assert.Equal(t, fakegds.LabelPoolID(cs, "a", "red"), red.GetPoolId())

	// Heartbeats and deregistration.
	_, err = c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-0", Epoch: again.GetRegistration().GetLease().GetEpoch() + 1,
	})
	requireReason(t, err, codes.Aborted, api.ReasonLeaseEpochMismatch)
	for name, hb := range map[string]*gdsv1.HeartbeatPoolAgentRequest{
		"unknown role":     {Clusterset: cs, PoolId: id, Role: kvRelay, AgentId: "pylon-0", Epoch: 1},
		"unknown agent id": {Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-9", Epoch: 1},
		"unknown pool":     {Clusterset: cs, PoolId: "unknown", Role: pylon, AgentId: "pylon-0", Epoch: 1},
	} {
		_, err = c.pools.HeartbeatPoolAgent(ctx(t), hb)
		requireReason(t, err, codes.NotFound, api.ReasonNotFound)
		assert.Contains(t, status.Convert(err).Message(), hb.GetAgentId(), name)
	}
	for name, hb := range map[string]*gdsv1.HeartbeatPoolAgentRequest{
		"no role":           {Clusterset: cs, PoolId: id, AgentId: "pylon-0", Epoch: 1},
		"no pool id":        {Clusterset: cs, Role: pylon, AgentId: "pylon-0", Epoch: 1},
		"no agent id":       {Clusterset: cs, PoolId: id, Role: pylon, Epoch: 1},
		"agent id too long": {Clusterset: cs, PoolId: id, Role: pylon, AgentId: strings.Repeat("x", api.MaxAgentIDBytes+1), Epoch: 1},
	} {
		_, err = c.pools.HeartbeatPoolAgent(ctx(t), hb)
		requireReason(t, err, codes.InvalidArgument, api.ReasonInvalidArgument)
		assert.NotEmpty(t, name)
	}
	attrs := map[string]string{}
	for i := range api.MaxAgentAttributes + 1 {
		attrs[strings.Repeat("k", i+1)] = "v"
	}
	_, err = c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-0", Epoch: again.GetRegistration().GetLease().GetEpoch(), Attributes: attrs,
	})
	requireReason(t, err, codes.InvalidArgument, api.ReasonInvalidArgument)
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: "unknown", Role: pylon, AgentId: "pylon-0"})
	require.NoError(t, err, "deregistering an unknown registration succeeds")
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: id, AgentId: "pylon-0"})
	requireReason(t, err, codes.InvalidArgument, api.ReasonInvalidArgument)
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: id, Role: pylon})
	requireReason(t, err, codes.InvalidArgument, api.ReasonInvalidArgument)
	for range 2 {
		_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pylon-0"})
		require.NoError(t, err)
	}
	_, err = c.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: id})
	requireReason(t, err, codes.NotFound, api.ReasonNotFound)
}

func TestPoolAgentWatchEvents(t *testing.T) {
	clk := newClock()
	_, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0), fakegds.WithBookmarkInterval(0))
	w, err := c.pools.WatchPools(ctx(t), &gdsv1.WatchPoolsRequest{Clusterset: cs})
	require.NoError(t, err)
	assert.Equal(t, []gdsv1.EventType{start_, end}, types(recvPools(t, w, 2)))

	reg := registerAgent(t, c, "a", "blue", pylon, "pylon-0")
	ev := recvPools(t, w, 1)[0]
	assert.Equal(t, upsert, ev.GetType())
	assert.Equal(t, reg.GetPoolId(), ev.GetPool().GetPoolId())
	assert.Equal(t, active, ev.GetPool().GetState())
	require.Len(t, ev.GetPool().GetAgents(), 1)

	// A heartbeat that renews the lease changes the pool: an UPSERT.
	clk.Advance(5 * time.Second)
	_, err = c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: reg.GetPoolId(), Role: pylon, AgentId: "pylon-0", Epoch: reg.GetAgent().GetLease().GetEpoch(),
	})
	require.NoError(t, err)
	ev = recvPools(t, w, 1)[0]
	assert.Equal(t, upsert, ev.GetType())
	assert.Equal(t, clk.Now(), ev.GetPool().GetAgents()[0].GetLease().GetRenewedAt().AsTime())

	// A second agent is an UPSERT; removing one of two is an UPSERT;
	// removing the last source is a DELETE carrying GONE.
	registerAgent(t, c, "a", "blue", kvRelay, "relay-0")
	ev = recvPools(t, w, 1)[0]
	assert.Equal(t, upsert, ev.GetType())
	assert.True(t, ev.GetPool().GetKvRelayLive())
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: reg.GetPoolId(), Role: kvRelay, AgentId: "relay-0"})
	require.NoError(t, err)
	ev = recvPools(t, w, 1)[0]
	assert.Equal(t, upsert, ev.GetType())
	assert.False(t, ev.GetPool().GetKvRelayLive())
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: reg.GetPoolId(), Role: pylon, AgentId: "pylon-0"})
	require.NoError(t, err)
	ev = recvPools(t, w, 1)[0]
	assert.Equal(t, del, ev.GetType())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, ev.GetPool().GetState())
	assert.Equal(t, reg.GetPoolId(), ev.GetPool().GetPoolId())

	// DeregisterPool on an agent-only pool removes its agents.
	reg = registerAgent(t, c, "a", "green", pylon, "pylon-0")
	assert.Equal(t, upsert, recvPools(t, w, 1)[0].GetType())
	_, err = c.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: reg.GetPoolId()})
	require.NoError(t, err)
	ev = recvPools(t, w, 1)[0]
	assert.Equal(t, del, ev.GetType())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, ev.GetPool().GetState())
}

func TestTwoRegistrationsOfOneRole(t *testing.T) {
	clk := newClock()
	srv, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0), fakegds.WithBookmarkInterval(0))
	srv.EnsureCluster(cs, "a", "")
	srv.PutExport(cs, "a", "pools", "blue-pylon", fakegds.PylonSpec("blue", 9090), []*gdsv1.Endpoint{})
	register := func(agentID string, ttl time.Duration, attrs map[string]string) *gdsv1.RegisterPoolAgentResponse {
		t.Helper()
		resp, err := c.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
			Clusterset: cs, Cluster: "a", PoolName: "blue", LeaseTtl: durationpb.New(ttl),
			Agent: &gdsv1.PoolAgent{Role: pylon, AgentId: agentID, Attributes: attrs},
		})
		require.NoError(t, err)
		return resp
	}

	// Two Pylon pods register the same role with their own ids: neither
	// fences the other.
	p1 := register("pylon-b", 20*time.Second, map[string]string{"pod": "b"})
	clk.Advance(time.Second)
	p0 := register("pylon-a", 60*time.Second, map[string]string{"pod": "a"})
	assert.Equal(t, p1.GetPoolId(), p0.GetPoolId())
	assert.NotEqual(t, p1.GetRegistration().GetLease().GetEpoch(), p0.GetRegistration().GetLease().GetEpoch())
	ag := p0.GetAgent()
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_BOTH, ag.GetSource())
	require.Len(t, ag.GetRegistrations(), 2)
	assert.Equal(t, "pylon-a", ag.GetRegistrations()[0].GetAgentId(), "sorted by agent id")
	assert.Equal(t, "pylon-b", ag.GetRegistrations()[1].GetAgentId())
	assert.Equal(t, int32(2), ag.GetActiveRegistrationCount())
	assert.Equal(t, "pylon-a", ag.GetAgentId(), "the agent mirrors the registration that expires last")
	assert.Equal(t, map[string]string{"pod": "a"}, ag.GetAttributes())
	assert.Equal(t, p0.GetRegistration().GetLease(), ag.GetLease())

	// Both heartbeat with their own epochs (and the cluster renews).
	clk.Advance(5 * time.Second)
	srv.EnsureCluster(cs, "a", "")
	for _, r := range []*gdsv1.RegisterPoolAgentResponse{p0, p1} {
		_, err := c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
			Clusterset: cs, PoolId: r.GetPoolId(), Role: pylon,
			AgentId: r.GetRegistration().GetAgentId(), Epoch: r.GetRegistration().GetLease().GetEpoch(),
		})
		require.NoError(t, err)
	}
	_, err := c.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: p0.GetPoolId(), Role: pylon, AgentId: "pylon-b", Epoch: p0.GetRegistration().GetLease().GetEpoch(),
	})
	requireReason(t, err, codes.Aborted, api.ReasonLeaseEpochMismatch)

	// pylon-b stops heartbeating and expires; pylon-a keeps the agent and
	// the pool live.
	w, err := c.pools.WatchPools(ctx(t), &gdsv1.WatchPoolsRequest{Clusterset: cs})
	require.NoError(t, err)
	recvPools(t, w, 3)
	clk.Advance(30 * time.Second)
	srv.Refresh()
	ev := recvPools(t, w, 1)[0]
	assert.Equal(t, upsert, ev.GetType(), "an expiring registration changes the pool")
	p := ev.GetPool()
	assert.Equal(t, active, p.GetState())
	ag = agentOf(p, pylon)
	require.Len(t, ag.GetRegistrations(), 2, "an expired registration stays until deregistered")
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_ACTIVE, ag.GetRegistrations()[0].GetLease().GetState())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, ag.GetRegistrations()[1].GetLease().GetState())
	assert.Equal(t, int32(1), ag.GetActiveRegistrationCount())
	assert.True(t, ag.GetLive())
	assert.Equal(t, "pylon-a", ag.GetAgentId())

	// Deregistering pylon-a leaves only the expired registration: the
	// agent stays (registration and export) but is not live.
	_, err = c.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{
		Clusterset: cs, PoolId: p0.GetPoolId(), Role: pylon, AgentId: "pylon-a",
	})
	require.NoError(t, err)
	p = recvPools(t, w, 1)[0].GetPool()
	ag = agentOf(p, pylon)
	require.Len(t, ag.GetRegistrations(), 1)
	assert.Equal(t, "pylon-b", ag.GetAgentId())
	assert.Zero(t, ag.GetActiveRegistrationCount())
	assert.False(t, ag.GetLive())
	assert.Equal(t, unhealthy, p.GetState())

	// A ready Pylon endpoint makes it live again; DeregisterPool removes
	// every registration and leaves the label source.
	srv.SetEndpoints(cs, "a", "pools", "blue-pylon", fakegds.ReadyEndpoints("10.0.1.1"))
	assert.Equal(t, active, recvPools(t, w, 1)[0].GetPool().GetState())
	_, err = c.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: p0.GetPoolId()})
	require.NoError(t, err)
	p = recvPools(t, w, 1)[0].GetPool()
	ag = agentOf(p, pylon)
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_LABEL, ag.GetSource())
	assert.Empty(t, ag.GetRegistrations())
	assert.Nil(t, ag.GetLease())
	assert.True(t, ag.GetLive())
}
