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

package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func TestLeaseExpired(t *testing.T) {
	ttl := 30 * time.Second
	skew := 2 * time.Second
	tests := []struct {
		name    string
		renewed time.Time
		now     time.Time
		skew    time.Duration
		want    bool
	}{
		{"just renewed", now, now, skew, false},
		{"within ttl", now, now.Add(29 * time.Second), skew, false},
		{"at expires_at", now, now.Add(ttl), skew, false},
		{"within skew", now, now.Add(ttl + skew - time.Millisecond), skew, false},
		{"at expires_at plus skew", now, now.Add(ttl + skew), skew, false},
		{"past expires_at plus skew", now, now.Add(ttl + skew + time.Nanosecond), skew, true},
		{"no skew allowance", now, now.Add(ttl + time.Nanosecond), 0, true},
		{"renewed in the future of the evaluating replica", now.Add(5 * time.Second), now, skew, false},
		{"long expired", now, now.Add(time.Hour), skew, true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, LeaseExpired(tt.renewed, ttl, tt.now, tt.skew), tt.name)
		want := LeaseStateActive
		if tt.want {
			want = LeaseStateExpired
		}
		assert.Equal(t, want, EvaluateLease(tt.renewed, ttl, tt.now, tt.skew), tt.name)
	}
}

func TestLeaseExpiresAt(t *testing.T) {
	assert.Equal(t, now.Add(30*time.Second), LeaseExpiresAt(now, 30*time.Second))
}

func TestClampLeaseTTL(t *testing.T) {
	tests := []struct {
		requested, def, want time.Duration
	}{
		{0, DefaultClusterLeaseTTL, 30 * time.Second},
		{-time.Second, DefaultPoolLeaseTTL, 60 * time.Second},
		{time.Second, DefaultClusterLeaseTTL, MinLeaseTTL},
		{10 * time.Second, DefaultClusterLeaseTTL, 10 * time.Second},
		{45 * time.Second, DefaultClusterLeaseTTL, 45 * time.Second},
		{5 * time.Minute, DefaultClusterLeaseTTL, 5 * time.Minute},
		{time.Hour, DefaultClusterLeaseTTL, MaxLeaseTTL},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ClampLeaseTTL(tt.requested, tt.def), "requested %s", tt.requested)
	}
}

func TestHeartbeatInterval(t *testing.T) {
	assert.Equal(t, 10*time.Second, HeartbeatInterval(DefaultClusterLeaseTTL))
	assert.Equal(t, 20*time.Second, HeartbeatInterval(DefaultPoolLeaseTTL))
}

func TestNextGeneration(t *testing.T) {
	ms := uint64(now.UnixMilli())
	assert.Equal(t, ms, NextGeneration(0, now), "a restarted writer takes the clock")
	assert.Equal(t, ms, NextGeneration(ms-1, now))
	assert.Equal(t, ms+1, NextGeneration(ms, now), "two writes in one millisecond")
	assert.Equal(t, ms+1001, NextGeneration(ms+1000, now), "the clock went backwards")
	assert.Equal(t, uint64(1), NextGeneration(0, time.Time{}))
}

func TestParseRole(t *testing.T) {
	for _, v := range []string{"router", "pool-frontend", "kv-relay", "pylon"} {
		r, ok := ParseRole(v)
		assert.True(t, ok, v)
		assert.Equal(t, Role(v), r)
	}
	r, ok := ParseRole("frontend")
	assert.False(t, ok)
	assert.Empty(t, r)

	assert.False(t, RoleRouter.IsPoolRole())
	assert.True(t, RolePoolFrontend.IsPoolRole())
	assert.True(t, RoleKVRelay.IsPoolRole())
	assert.True(t, RolePylon.IsPoolRole())
}

func TestAgentSourceOf(t *testing.T) {
	assert.Equal(t, AgentSourceBoth, AgentSourceOf(true, true))
	assert.Equal(t, AgentSourceLabel, AgentSourceOf(true, false))
	assert.Equal(t, AgentSourceAPI, AgentSourceOf(false, true))
	assert.Empty(t, AgentSourceOf(false, false))
}

func TestDerivePoolState(t *testing.T) {
	// labelled: a pool derived from a labelled export, cluster healthy.
	labelled := PoolStateInput{LabelledExport: true, ClusterLeaseActive: true, ReadyIngressEndpoints: 2, Started: true}
	// apiOnly: an API registration without services, heartbeating.
	apiOnly := PoolStateInput{Overlay: true, PoolLeaseActive: true, Started: true}
	// joined: an API registration with services[], no labelled export.
	joined := PoolStateInput{Overlay: true, OverlayServices: true, ClusterLeaseActive: true, ReadyIngressEndpoints: 1, Started: true}
	// pylon: an ingress agent registration (PYLON) only, heartbeating.
	pylon := PoolStateInput{Agents: true, AgentLeaseActive: true, IngressAgentLeaseActive: true}
	// relay: a KV_RELAY agent registration only, heartbeating.
	relay := PoolStateInput{Agents: true, AgentLeaseActive: true}

	with := func(in PoolStateInput, f func(*PoolStateInput)) PoolStateInput {
		f(&in)
		return in
	}

	tests := []struct {
		name string
		in   PoolStateInput
		want PoolState
	}{
		// GONE.
		{"nothing left", PoolStateInput{ClusterLeaseActive: true, ReadyIngressEndpoints: 1}, PoolStateGone},
		{"deregistered API pool", with(apiOnly, func(in *PoolStateInput) { in.Deregistered = true }), PoolStateGone},
		{"deregistered overlay on a labelled pool", with(labelled, func(in *PoolStateInput) { in.Overlay, in.Deregistered = true, true }), PoolStateGone},

		// Label-derived pools.
		{"labelled, ready", labelled, PoolStateActive},
		{"labelled, ready, first sighting", with(labelled, func(in *PoolStateInput) { in.Started = false }), PoolStateActive},
		{"labelled, no ready endpoint yet", with(labelled, func(in *PoolStateInput) { in.ReadyIngressEndpoints, in.Started = 0, false }), PoolStateRegistered},
		{"labelled, lost its ready endpoints", with(labelled, func(in *PoolStateInput) { in.ReadyIngressEndpoints = 0 }), PoolStateUnhealthy},
		{"labelled, cluster lease expired", with(labelled, func(in *PoolStateInput) { in.ClusterLeaseActive = false }), PoolStateUnhealthy},
		{"labelled, cluster expired before first ready", with(labelled, func(in *PoolStateInput) {
			in.ClusterLeaseActive, in.ReadyIngressEndpoints, in.Started = false, 0, false
		}), PoolStateUnhealthy},
		{"labelled, overlay drains", with(labelled, func(in *PoolStateInput) { in.Overlay, in.Desired = true, PoolStateDraining }), PoolStateDraining},
		{"labelled, overlay active", with(labelled, func(in *PoolStateInput) { in.Overlay, in.Desired = true, PoolStateActive }), PoolStateActive},
		{"labelled, draining but unhealthy", with(labelled, func(in *PoolStateInput) {
			in.Overlay, in.Desired, in.ReadyIngressEndpoints = true, PoolStateDraining, 0
		}), PoolStateUnhealthy},
		{"labelled, pool lease ignored", with(labelled, func(in *PoolStateInput) { in.Overlay, in.PoolLeaseActive = true, false }), PoolStateActive},

		// API-only pools.
		{"API-only, heartbeating", apiOnly, PoolStateActive},
		{"API-only, before first heartbeat", with(apiOnly, func(in *PoolStateInput) { in.Started = false }), PoolStateRegistered},
		{"API-only, draining before first heartbeat", with(apiOnly, func(in *PoolStateInput) {
			in.Started, in.Desired = false, PoolStateDraining
		}), PoolStateRegistered},
		{"API-only, lease expired before first heartbeat", with(apiOnly, func(in *PoolStateInput) {
			in.Started, in.PoolLeaseActive = false, false
		}), PoolStateUnhealthy},
		{"API-only, lease expired", with(apiOnly, func(in *PoolStateInput) { in.PoolLeaseActive = false }), PoolStateUnhealthy},
		{"API-only, draining", with(apiOnly, func(in *PoolStateInput) { in.Desired = PoolStateDraining }), PoolStateDraining},
		{"API-only, unknown desired state counts as active", with(apiOnly, func(in *PoolStateInput) { in.Desired = PoolStateGone }), PoolStateActive},
		{"API-only, cluster lease ignored", with(apiOnly, func(in *PoolStateInput) { in.ClusterLeaseActive = false }), PoolStateActive},

		// API pools joined to exports through services[].
		{"joined, ready", joined, PoolStateActive},
		{"joined, no ready endpoint yet", with(joined, func(in *PoolStateInput) { in.ReadyIngressEndpoints, in.Started = 0, false }), PoolStateRegistered},
		{"joined, lost its ready endpoints", with(joined, func(in *PoolStateInput) { in.ReadyIngressEndpoints = 0 }), PoolStateUnhealthy},
		{"joined, pool lease expired but endpoints ready", with(joined, func(in *PoolStateInput) { in.PoolLeaseActive = false }), PoolStateActive},
		{"joined, cluster lease expired", with(joined, func(in *PoolStateInput) { in.ClusterLeaseActive = false }), PoolStateUnhealthy},
		{"joined, draining", with(joined, func(in *PoolStateInput) { in.Desired = PoolStateDraining }), PoolStateDraining},
		{"joined, cluster expired before first ready, pool lease active", with(joined, func(in *PoolStateInput) {
			in.ClusterLeaseActive, in.ReadyIngressEndpoints, in.Started, in.PoolLeaseActive = false, 0, false, true
		}), PoolStateRegistered},

		// Pools with agent registrations.
		{"ingress agent, first sighting", pylon, PoolStateActive},
		{"ingress agent, started", with(pylon, func(in *PoolStateInput) { in.Started = true }), PoolStateActive},
		{"ingress agent lease expired", with(pylon, func(in *PoolStateInput) {
			in.Started, in.AgentLeaseActive, in.IngressAgentLeaseActive = true, false, false
		}), PoolStateUnhealthy},
		{"ingress agent expired before it was seen live", with(pylon, func(in *PoolStateInput) {
			in.AgentLeaseActive, in.IngressAgentLeaseActive = false, false
		}), PoolStateUnhealthy},
		{"ingress agent, cluster lease ignored", with(pylon, func(in *PoolStateInput) { in.ClusterLeaseActive = false }), PoolStateActive},
		{"ingress agent, draining", with(pylon, func(in *PoolStateInput) { in.Overlay, in.Desired = true, PoolStateDraining }), PoolStateDraining},
		{"ingress agent, pool lease ignored", with(pylon, func(in *PoolStateInput) { in.Overlay, in.PoolLeaseActive = true, false }), PoolStateActive},
		{"relay only stays registered", relay, PoolStateRegistered},
		{"relay only, cluster ready endpoints elsewhere do not count", with(relay, func(in *PoolStateInput) { in.ClusterLeaseActive = true }), PoolStateRegistered},
		{"relay only, lease expired", with(relay, func(in *PoolStateInput) { in.AgentLeaseActive = false }), PoolStateUnhealthy},
		{"relay only after the ingress left", with(relay, func(in *PoolStateInput) { in.Started = true }), PoolStateUnhealthy},
		{"relay only, API overlay heartbeating: the static ingress keeps it live", with(relay, func(in *PoolStateInput) {
			in.Overlay, in.PoolLeaseActive, in.AgentLeaseActive, in.Started = true, true, false, true
		}), PoolStateActive},
		{"relay with a registered API pool, pool lease active", with(relay, func(in *PoolStateInput) {
			in.Overlay, in.PoolLeaseActive, in.AgentLeaseActive = true, true, false
		}), PoolStateActive},
		{"relay with a registered API pool, pool lease expired", with(relay, func(in *PoolStateInput) {
			in.Overlay, in.PoolLeaseActive, in.Started = true, false, true
		}), PoolStateUnhealthy},
		{"relay with a registered API pool, pool lease expired before first heartbeat", with(relay, func(in *PoolStateInput) {
			in.Overlay, in.PoolLeaseActive = true, false
		}), PoolStateRegistered},
		{"agent registrations without a live lease", PoolStateInput{Agents: true}, PoolStateUnhealthy},
		{"labelled relay endpoints only", with(labelled, func(in *PoolStateInput) { in.ReadyIngressEndpoints, in.Started = 0, false }), PoolStateRegistered},
		{"labelled, no ready endpoint, ingress agent lease", with(labelled, func(in *PoolStateInput) {
			in.ReadyIngressEndpoints, in.Agents, in.AgentLeaseActive, in.IngressAgentLeaseActive = 0, true, true, true
		}), PoolStateActive},
		{"labelled, cluster lease expired, ingress agent lease", with(labelled, func(in *PoolStateInput) {
			in.ClusterLeaseActive, in.Agents, in.AgentLeaseActive, in.IngressAgentLeaseActive = false, true, true, true
		}), PoolStateActive},
		{"deregistered pool with agents", with(pylon, func(in *PoolStateInput) { in.Deregistered = true }), PoolStateGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DerivePoolState(tt.in))
		})
	}
}

func TestPoolStateInputAPIOnly(t *testing.T) {
	assert.True(t, PoolStateInput{Overlay: true}.APIOnly())
	assert.False(t, PoolStateInput{Overlay: true, OverlayServices: true}.APIOnly())
	assert.False(t, PoolStateInput{Overlay: true, LabelledExport: true}.APIOnly())
	assert.False(t, PoolStateInput{LabelledExport: true}.APIOnly())
	assert.False(t, PoolStateInput{Overlay: true, Agents: true}.APIOnly())
	assert.True(t, PoolStateInput{Overlay: true, Agents: true}.StaticIngress())
	assert.False(t, PoolStateInput{Overlay: true, OverlayServices: true}.StaticIngress())
	assert.False(t, PoolStateInput{Overlay: true, LabelledExport: true}.StaticIngress())
	assert.False(t, PoolStateInput{}.APIOnly())
}

func TestPoolStateInputLive(t *testing.T) {
	assert.True(t, PoolStateInput{Overlay: true, PoolLeaseActive: true}.Live(), "API-only")
	assert.False(t, PoolStateInput{Overlay: true, PoolLeaseActive: true, Agents: true}.Live(), "agents replace the pool lease")
	assert.True(t, PoolStateInput{LabelledExport: true, ClusterLeaseActive: true, ReadyIngressEndpoints: 1}.Live())
	assert.False(t, PoolStateInput{LabelledExport: true, ReadyIngressEndpoints: 1}.Live(), "without the cluster lease")
	assert.True(t, PoolStateInput{Agents: true, IngressAgentLeaseActive: true}.Live())
	assert.False(t, PoolStateInput{Agents: true, AgentLeaseActive: true}.Live(), "a relay lease")
}
