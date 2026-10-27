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
	for _, v := range []string{"router", "pool-frontend", "kv-relay"} {
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
}

func TestDerivePoolState(t *testing.T) {
	// labelled: a pool derived from a labelled export, cluster healthy.
	labelled := PoolStateInput{LabelledExport: true, ClusterLeaseActive: true, ReadyEndpoints: 2, Started: true}
	// apiOnly: an API registration without services, heartbeating.
	apiOnly := PoolStateInput{Overlay: true, PoolLeaseActive: true, Started: true}
	// joined: an API registration with services[], no labelled export.
	joined := PoolStateInput{Overlay: true, OverlayServices: true, ClusterLeaseActive: true, ReadyEndpoints: 1, Started: true}

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
		{"nothing left", PoolStateInput{ClusterLeaseActive: true, ReadyEndpoints: 1}, PoolStateGone},
		{"deregistered API pool", with(apiOnly, func(in *PoolStateInput) { in.Deregistered = true }), PoolStateGone},
		{"deregistered overlay on a labelled pool", with(labelled, func(in *PoolStateInput) { in.Overlay, in.Deregistered = true, true }), PoolStateGone},

		// Label-derived pools.
		{"labelled, ready", labelled, PoolStateActive},
		{"labelled, ready, first sighting", with(labelled, func(in *PoolStateInput) { in.Started = false }), PoolStateActive},
		{"labelled, no ready endpoint yet", with(labelled, func(in *PoolStateInput) { in.ReadyEndpoints, in.Started = 0, false }), PoolStateRegistered},
		{"labelled, lost its ready endpoints", with(labelled, func(in *PoolStateInput) { in.ReadyEndpoints = 0 }), PoolStateUnhealthy},
		{"labelled, cluster lease expired", with(labelled, func(in *PoolStateInput) { in.ClusterLeaseActive = false }), PoolStateUnhealthy},
		{"labelled, cluster expired before first ready", with(labelled, func(in *PoolStateInput) {
			in.ClusterLeaseActive, in.ReadyEndpoints, in.Started = false, 0, false
		}), PoolStateUnhealthy},
		{"labelled, overlay drains", with(labelled, func(in *PoolStateInput) { in.Overlay, in.Desired = true, PoolStateDraining }), PoolStateDraining},
		{"labelled, overlay active", with(labelled, func(in *PoolStateInput) { in.Overlay, in.Desired = true, PoolStateActive }), PoolStateActive},
		{"labelled, draining but unhealthy", with(labelled, func(in *PoolStateInput) {
			in.Overlay, in.Desired, in.ReadyEndpoints = true, PoolStateDraining, 0
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
		{"joined, no ready endpoint yet", with(joined, func(in *PoolStateInput) { in.ReadyEndpoints, in.Started = 0, false }), PoolStateRegistered},
		{"joined, lost its ready endpoints", with(joined, func(in *PoolStateInput) { in.ReadyEndpoints = 0 }), PoolStateUnhealthy},
		{"joined, pool lease expired but endpoints ready", with(joined, func(in *PoolStateInput) { in.PoolLeaseActive = false }), PoolStateActive},
		{"joined, cluster lease expired", with(joined, func(in *PoolStateInput) { in.ClusterLeaseActive = false }), PoolStateUnhealthy},
		{"joined, draining", with(joined, func(in *PoolStateInput) { in.Desired = PoolStateDraining }), PoolStateDraining},
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
	assert.False(t, PoolStateInput{}.APIOnly())
}
