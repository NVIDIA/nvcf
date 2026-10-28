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

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"

	gds "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

const (
	pylon = gdsv1.FrontendRole_FRONTEND_ROLE_PYLON
	relay = gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY
)

func (e *env) registerAgent(t *testing.T, cluster, pool string, role gdsv1.FrontendRole, agentID string) *gdsv1.RegisterPoolAgentResponse {
	t.Helper()
	resp, err := e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: cluster, PoolName: pool,
		Agent: &gdsv1.PoolAgent{Role: role, AgentId: agentID, Attributes: map[string]string{"pod": agentID}},
	})
	require.NoError(t, err)
	return resp
}

func (e *env) pool(t *testing.T, id string) *gdsv1.Pool {
	t.Helper()
	got, err := e.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err)
	return got.GetPool()
}

func agentWithRole(p *gdsv1.Pool, role gdsv1.FrontendRole) *gdsv1.PoolAgent {
	for _, a := range p.GetAgents() {
		if a.GetRole() == role {
			return a
		}
	}
	return nil
}

func TestPoolAgentFlow(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	e.register(t, "a", "us-east-1")
	id := registry.PoolID(cs, "a", "ie-1")

	// The first registration creates the pool, live through its lease.
	reg := e.registerAgent(t, "a", "ie-1", pylon, "pod-0")
	assert.Equal(t, id, reg.GetPoolId())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, reg.GetPoolState())
	assert.Equal(t, "pod-0", reg.GetRegistration().GetAgentId())
	assert.Equal(t, uint64(t0.UnixMilli()), reg.GetRegistration().GetLease().GetEpoch())
	assert.Equal(t, 60*time.Second, reg.GetRegistration().GetLease().GetTtl().AsDuration())
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_API, reg.GetAgent().GetSource())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, reg.GetAgent().GetDialMode())
	assert.True(t, reg.GetAgent().GetLive())
	assert.Equal(t, t0, reg.GetServerTime().AsTime())
	epoch0 := reg.GetRegistration().GetLease().GetEpoch()

	p := e.pool(t, id)
	assert.Equal(t, "ie-1", p.GetName())
	assert.Equal(t, "us-east-1", p.GetLocation().GetRegion(), "region from the cluster registration")

	// A second pod registers the same role without fencing the first.
	e.clock.Advance(time.Second)
	reg1 := e.registerAgent(t, "a", "ie-1", pylon, "pod-1")
	assert.Equal(t, id, reg1.GetPoolId())
	require.Len(t, reg1.GetAgent().GetRegistrations(), 2)
	assert.Equal(t, int32(2), reg1.GetAgent().GetActiveRegistrationCount())
	hb, err := e.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pod-0", Epoch: epoch0,
		Attributes: map[string]string{"models": "llama-3"},
	})
	require.NoError(t, err)
	assert.Equal(t, t0.Add(time.Second), hb.GetLease().GetRenewedAt().AsTime())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, hb.GetPoolState())
	a := agentWithRole(e.pool(t, id), pylon)
	require.NotNil(t, a)
	assert.Equal(t, map[string]string{"models": "llama-3"}, a.GetRegistrations()[0].GetAttributes())

	// Re-registering pod-0 mints a new epoch; the old one is fenced.
	e.clock.Advance(time.Second)
	again := e.registerAgent(t, "a", "ie-1", pylon, "pod-0")
	assert.Greater(t, again.GetRegistration().GetLease().GetEpoch(), epoch0)
	assert.Equal(t, t0, again.GetRegistration().GetRegisteredAt().AsTime(), "registered_at is kept")
	_, err = e.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pod-0", Epoch: epoch0,
	})
	requireError(t, err, codes.Aborted, gds.ReasonLeaseEpochMismatch)
	_, err = e.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pod-9", Epoch: epoch0,
	})
	requireError(t, err, codes.NotFound, gds.ReasonNotFound)

	// A generation below the stored one is refused.
	_, err = e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: "a", PoolName: "ie-1", Generation: 5,
		Agent: &gdsv1.PoolAgent{Role: pylon, AgentId: "pod-0"},
	})
	requireError(t, err, codes.FailedPrecondition, gds.ReasonStaleGeneration)

	// A relay makes KV facts available.
	rel := e.registerAgent(t, "a", "ie-1", relay, "relay-0")
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_OUTBOUND, rel.GetAgent().GetDialMode())
	p = e.pool(t, id)
	assert.True(t, p.GetKvRelayLive())
	require.Len(t, p.GetAgents(), 2)
	assert.Equal(t, relay, p.GetAgents()[0].GetRole(), "agents are sorted by role")

	// Deregistering one pod keeps the agent; unknown registrations succeed.
	_, err = e.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pod-1"})
	require.NoError(t, err)
	_, err = e.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pod-1"})
	require.NoError(t, err, "idempotent")
	a = agentWithRole(e.pool(t, id), pylon)
	require.NotNil(t, a)
	require.Len(t, a.GetRegistrations(), 1)
	assert.Equal(t, "pod-0", a.GetAgentId())

	// The pylon stops heartbeating: only the relay is live.
	e.clock.Advance(time.Minute + 3*time.Second)
	e.view.EvaluateLeases()
	rel = e.registerAgent(t, "a", "ie-1", relay, "relay-0")
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_UNHEALTHY, rel.GetPoolState(), "the pool was live and lost its ingress")

	// DeregisterPool removes every registration of the pool.
	_, err = e.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, e.pool(t, id).GetState())
	snap, err := e.store.LoadClusterSet(ctx(t), cs)
	require.NoError(t, err)
	assert.Empty(t, snap.PoolAgents)
	_, err = e.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err, "idempotent")
}

func TestPoolAgentRelayOnlyIsRegistered(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	resp, err := e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: "a", PoolName: "kv", LeaseTtl: durationpb.New(time.Second),
		Agent: &gdsv1.PoolAgent{Role: relay, AgentId: "relay-0"},
	})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, resp.GetPoolState())
	assert.Equal(t, 10*time.Second, resp.GetRegistration().GetLease().GetTtl().AsDuration(), "clamped to the minimum")
	assert.True(t, resp.GetAgent().GetLive())

	resp, err = e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: "a", PoolName: "kv", LeaseTtl: durationpb.New(time.Hour),
		Agent: &gdsv1.PoolAgent{Role: relay, AgentId: "relay-0"},
	})
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, resp.GetRegistration().GetLease().GetTtl().AsDuration(), "clamped to the maximum")
}

func TestPoolAgentJoinsLabelledPool(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	e.register(t, "a", "us-east-1")
	labelledExport(t, e, "a", "dyn", "frontend",
		map[string]string{gds.LabelExport: "true", gds.LabelRole: gds.RolePoolFrontend, gds.LabelPool: "demo"},
		map[string]string{gds.AnnotationRoles: "pool-frontend,pylon"}, &gdsv1.ServicePort{Name: "http", Port: 8000})
	id := registry.PoolID(cs, "a", "demo")
	p := e.pool(t, id)
	assert.Equal(t, int32(2), p.GetReadyIngressCount())
	require.Len(t, p.GetAgents(), 2)

	resp, err := e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: "a", PoolName: "demo", PoolId: id,
		Agent: &gdsv1.PoolAgent{Role: pylon, AgentId: "pylon-0"},
	})
	require.NoError(t, err, "the pool's own id is accepted")
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_BOTH, resp.GetAgent().GetSource())
	assert.Equal(t, "frontend", resp.GetAgent().GetService().GetName())

	// Explicit pool ids that disagree with the pools are refused.
	for _, req := range []*gdsv1.RegisterPoolAgentRequest{
		{Clusterset: cs, Cluster: "a", PoolName: "demo", PoolId: "custom"},
		{Clusterset: cs, Cluster: "a", PoolName: "other", PoolId: id},
		{Clusterset: cs, Cluster: "b", PoolName: "demo", PoolId: id},
	} {
		req.Agent = &gdsv1.PoolAgent{Role: pylon, AgentId: "pylon-0"}
		_, err = e.pools.RegisterPoolAgent(ctx(t), req)
		requireError(t, err, codes.AlreadyExists, gds.ReasonPoolNameTaken)
	}
}

func TestPoolAgentSeesOtherReplicaWrites(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	e.registerAgent(t, "a", "ie-1", pylon, "pod-0")
	id := registry.PoolID(cs, "a", "ie-1")

	// Another replica registered pod-1 with a generation from the future
	// and this replica's view has not seen it: the server-assigned
	// generation is retried above the stored one.
	key := store.PoolAgentKey{ClusterSet: cs, Cluster: "a", PoolID: id, Role: store.FrontendRolePylon, AgentID: "pod-1"}
	stored, err := e.store.PutPoolAgent(ctx(t), store.PoolAgentRecord{Key: key, PoolName: "ie-1", Generation: 1 << 50, Lease: store.Lease{TTL: time.Minute}},
		store.PutPoolAgentOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	reg := e.registerAgent(t, "a", "ie-1", pylon, "pod-1")
	assert.Equal(t, uint64(1<<50+1), reg.GetRegistration().GetGeneration())

	// A heartbeat and a deregistration of a registration this replica has
	// not seen reload the clusterset.
	e.view.ApplyPoolAgentDelete(key)
	_, err = e.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pod-1", Epoch: reg.GetRegistration().GetLease().GetEpoch(),
	})
	require.NoError(t, err)
	assert.NotEqual(t, stored.Lease.Epoch, reg.GetRegistration().GetLease().GetEpoch())

	// A registration deleted behind this replica's back is not found.
	require.NoError(t, e.store.DeletePoolAgent(ctx(t), key))
	_, err = e.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: id, Role: pylon, AgentId: "pod-1", Epoch: reg.GetRegistration().GetLease().GetEpoch(),
	})
	requireError(t, err, codes.NotFound, gds.ReasonNotFound)
}

func TestDeregisterPoolWithRecordAndAgents(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	reg, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: apiPool("mistral", "a")})
	require.NoError(t, err)
	id := reg.GetPool().GetPoolId()
	e.registerAgent(t, "a", "mistral", pylon, "pod-0")
	p := e.pool(t, id)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState(), "the agent, not the pool lease, makes it live")
	require.Len(t, p.GetAgents(), 1)

	_, err = e.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, e.pool(t, id).GetState())
	assert.Empty(t, e.view.PoolAgentRecords(cs, id))
}

func TestPoolAgentValidation(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	tooMany := map[string]string{}
	for i := range gds.MaxAgentAttributes + 1 {
		tooMany[strings.Repeat("k", i+1)] = "v"
	}
	cases := []struct {
		name   string
		call   func() error
		fields []string
	}{
		{"register", func() error {
			_, err := e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
				Clusterset: cs, PoolName: "Bad", PoolId: "bad/id", LeaseTtl: durationpb.New(-time.Second),
				Agent: &gdsv1.PoolAgent{Role: gdsv1.FrontendRole_FRONTEND_ROLE_UNSPECIFIED, AgentId: strings.Repeat("x", gds.MaxAgentIDBytes+1), Attributes: tooMany},
			})
			return err
		}, []string{"cluster", "pool_name", "pool_id", "lease_ttl", "agent.role", "agent.agent_id", "agent.attributes"}},
		{"register attribute sizes", func() error {
			_, err := e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
				Clusterset: cs, Cluster: "a", PoolName: "p",
				Agent: &gdsv1.PoolAgent{Role: pylon, AgentId: "x", Attributes: map[string]string{"": "v", "k": strings.Repeat("v", 1025)}},
			})
			return err
		}, []string{"agent.attributes", "agent.attributes[k]"}},
		{"register without agent", func() error {
			_, err := e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{Clusterset: cs, Cluster: "a", PoolName: "p"})
			return err
		}, []string{"agent"}},
		{"heartbeat", func() error {
			_, err := e.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{Clusterset: cs, Attributes: tooMany})
			return err
		}, []string{"pool_id", "role", "agent_id", "epoch", "attributes"}},
		{"deregister", func() error {
			_, err := e.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{Clusterset: cs, PoolId: "p", Role: 9})
			return err
		}, []string{"role", "agent_id"}},
		{"pool services with an unknown role", func() error {
			p := apiPool("x", "a")
			p.Services = []*gdsv1.ServiceRef{{Namespace: "ns", Name: "svc", Role: 9}}
			_, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: p})
			return err
		}, []string{"pool.services[0].role"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := requireError(t, tc.call(), codes.InvalidArgument, gds.ReasonInvalidArgument)
			fields := badRequestFields(st)
			for _, f := range tc.fields {
				assert.Contains(t, fields, f)
			}
		})
	}

	// PYLON is accepted in services[] and frontends[].
	p := apiPool("pylon-pool", "a")
	p.Services = []*gdsv1.ServiceRef{{Namespace: "ns", Name: "pylon", Role: pylon}}
	p.Frontends = append(p.Frontends, &gdsv1.FrontendEndpoint{Role: pylon, Service: &gdsv1.ServiceRef{Namespace: "ns", Name: "pylon", Role: pylon}})
	_, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: p})
	require.NoError(t, err)
}

func TestPoolAgentStoreUnavailable(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	reg := e.registerAgent(t, "a", "ie-1", pylon, "pod-0")
	require.NoError(t, e.store.Close())
	_, err := e.pools.RegisterPoolAgent(ctx(t), &gdsv1.RegisterPoolAgentRequest{
		Clusterset: cs, Cluster: "a", PoolName: "ie-1", Agent: &gdsv1.PoolAgent{Role: pylon, AgentId: "pod-0"},
	})
	requireError(t, err, codes.Unavailable, gds.ReasonStoreUnavailable)
	_, err = e.pools.HeartbeatPoolAgent(ctx(t), &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: cs, PoolId: reg.GetPoolId(), Role: pylon, AgentId: "pod-0", Epoch: reg.GetRegistration().GetLease().GetEpoch(),
	})
	requireError(t, err, codes.Unavailable, gds.ReasonStoreUnavailable)
	_, err = e.pools.DeregisterPoolAgent(ctx(t), &gdsv1.DeregisterPoolAgentRequest{
		Clusterset: cs, PoolId: reg.GetPoolId(), Role: pylon, AgentId: "pod-0",
	})
	requireError(t, err, codes.Unavailable, gds.ReasonStoreUnavailable)
	_, err = e.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: reg.GetPoolId()})
	requireError(t, err, codes.Unavailable, gds.ReasonStoreUnavailable)
}

func TestMergedAgentFallback(t *testing.T) {
	rec := store.PoolAgentRecord{Key: store.PoolAgentKey{Role: store.FrontendRoleOpenAI, AgentID: "fe-0"}}
	reg := &gdsv1.PoolAgentRegistration{AgentId: "fe-0"}
	a := mergedAgent(&gdsv1.Pool{}, rec, reg)
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, a.GetRole())
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_INBOUND, a.GetDialMode())
	assert.Equal(t, "fe-0", a.GetAgentId())
	assert.Len(t, a.GetRegistrations(), 1)
}
