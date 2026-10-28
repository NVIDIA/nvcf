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
	"testing"

	"github.com/stretchr/testify/assert"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

const (
	roleOpenAI  = gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI
	roleKVRelay = gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY
	rolePylon   = gdsv1.FrontendRole_FRONTEND_ROLE_PYLON
)

func poolAgent(role gdsv1.FrontendRole, live bool) *gdsv1.PoolAgent {
	return &gdsv1.PoolAgent{Role: role, Live: live}
}

func TestPoolStateIngressAndAgents(t *testing.T) {
	frontend := &gdsv1.Pool{PoolId: "1", Name: "frontend", State: gdsv1.PoolState_POOL_STATE_ACTIVE,
		ReadyEndpointCount: 2, ReadyIngressCount: 2, Agents: []*gdsv1.PoolAgent{poolAgent(roleOpenAI, true)}}
	pylon := &gdsv1.Pool{PoolId: "2", Name: "pylon", State: gdsv1.PoolState_POOL_STATE_ACTIVE,
		Agents: []*gdsv1.PoolAgent{poolAgent(roleKVRelay, false), poolAgent(rolePylon, true)}}
	draining := &gdsv1.Pool{PoolId: "3", Name: "draining", State: gdsv1.PoolState_POOL_STATE_DRAINING, ReadyIngressCount: 1}
	relayOnly := &gdsv1.Pool{PoolId: "4", Name: "relay-only", State: gdsv1.PoolState_POOL_STATE_REGISTERED, KvRelayLive: true,
		Agents: []*gdsv1.PoolAgent{poolAgent(roleKVRelay, true), poolAgent(rolePylon, false)}}
	down := &gdsv1.Pool{PoolId: "5", Name: "down", State: gdsv1.PoolState_POOL_STATE_UNHEALTHY,
		Agents: []*gdsv1.PoolAgent{poolAgent(roleOpenAI, false), poolAgent(rolePylon, false)}}
	apiOnly := &gdsv1.Pool{PoolId: "6", Name: "api-only", State: gdsv1.PoolState_POOL_STATE_ACTIVE,
		Frontends: []*gdsv1.FrontendEndpoint{{Role: roleOpenAI, Url: "https://pool.example.internal"}}}
	st := &PoolState{Pools: []*gdsv1.Pool{frontend, pylon, draining, relayOnly, down, apiOnly}}

	assert.Equal(t, []string{"frontend", "pylon", "draining"}, names(st.Ingress()),
		"a ready ingress endpoint or a live OPENAI or PYLON agent; the state is not checked")
	assert.Equal(t, []string{"frontend", "pylon", "api-only"}, names(st.Active()), "Active still selects by state alone")
	assert.Empty(t, (&PoolState{}).Ingress())

	assert.True(t, IngressLive(pylon), "a Pylon-only pool has no ready frontend endpoint but a live ingress")
	assert.False(t, IngressLive(relayOnly), "a relay is not an ingress")
	assert.False(t, IngressLive(down))
	assert.False(t, IngressLive(nil))

	assert.Same(t, pylon.GetAgents()[1], Agent(pylon, rolePylon))
	assert.Same(t, pylon.GetAgents()[0], Agent(pylon, roleKVRelay))
	assert.Nil(t, Agent(pylon, roleOpenAI), "no agent of the role")
	assert.Nil(t, Agent(nil, rolePylon))
	assert.False(t, Agent(down, roleOpenAI).GetLive())

	pods := []*gdsv1.PoolAgentRegistration{{AgentId: "pylon-0"}, {AgentId: "pylon-1"}}
	pylon.Agents[1].Registrations = pods
	assert.Equal(t, pods, Registrations(pylon, rolePylon), "one registration per process instance")
	assert.Nil(t, Registrations(pylon, roleKVRelay), "an agent without registrations")
	assert.Nil(t, Registrations(pylon, roleOpenAI), "no agent of the role")
	assert.Nil(t, Registrations(nil, rolePylon))

	assert.True(t, KVRelayLive(relayOnly))
	assert.True(t, KVRelayLive(&gdsv1.Pool{KvRelayLive: true}), "the derived flag alone")
	assert.True(t, KVRelayLive(&gdsv1.Pool{Agents: []*gdsv1.PoolAgent{poolAgent(roleKVRelay, true)}}), "a live relay agent alone")
	assert.False(t, KVRelayLive(pylon), "the relay agent is not live")
	assert.False(t, KVRelayLive(frontend))
	assert.False(t, KVRelayLive(nil))
}
