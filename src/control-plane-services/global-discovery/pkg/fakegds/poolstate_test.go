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

package fakegds

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
)

// TestDerivePoolStateWithoutAgentsMatchesRegistry checks that the local
// rule is an extension of registry.DerivePoolState: without agent facts it
// gives the same state for every input.
func TestDerivePoolStateWithoutAgentsMatchesRegistry(t *testing.T) {
	n := 0
	for bits := range 1 << 7 {
		for _, desired := range []registry.PoolState{registry.PoolStateActive, registry.PoolStateDraining} {
			for _, ready := range []int{0, 2} {
				in := registry.PoolStateInput{
					Desired:            desired,
					Deregistered:       bits&1 != 0,
					LabelledExport:     bits&2 != 0,
					Overlay:            bits&4 != 0,
					OverlayServices:    bits&8 != 0,
					ClusterLeaseActive: bits&16 != 0,
					PoolLeaseActive:    bits&32 != 0,
					Started:            bits&64 != 0,
					ReadyEndpoints:     ready,
				}
				local := poolStateInput{PoolStateInput: in, ReadyIngressEndpoints: ready}
				assert.Equal(t, registry.DerivePoolState(in), derivePoolState(local), fmt.Sprintf("%+v", in))
				n++
			}
		}
	}
	assert.Equal(t, 512, n)
}

func TestDerivePoolStateWithAgents(t *testing.T) {
	// agentOnly: agent registrations only, cluster not registered.
	agentOnly := poolStateInput{Agents: true}
	// relayLabel: a labelled relay export with ready endpoints.
	relayLabel := poolStateInput{PoolStateInput: registry.PoolStateInput{
		LabelledExport: true, ClusterLeaseActive: true, ReadyEndpoints: 1,
	}}
	with := func(in poolStateInput, f func(*poolStateInput)) poolStateInput {
		f(&in)
		return in
	}
	tests := []struct {
		name string
		in   poolStateInput
		want registry.PoolState
	}{
		{"agent registrations keep the pool", with(agentOnly, func(in *poolStateInput) {}), registry.PoolStateUnhealthy},
		{"deregistered", with(agentOnly, func(in *poolStateInput) { in.Deregistered, in.IngressAgentLeaseActive = true, true }), registry.PoolStateGone},
		{"nothing left", poolStateInput{IngressAgentLeaseActive: true}, registry.PoolStateGone},

		{"relay agent only", with(agentOnly, func(in *poolStateInput) { in.AgentLeaseActive = true }), registry.PoolStateRegistered},
		{"relay agent only, after a live ingress", with(agentOnly, func(in *poolStateInput) {
			in.AgentLeaseActive, in.Started = true, true
		}), registry.PoolStateUnhealthy},
		{"ingress agent lease", with(agentOnly, func(in *poolStateInput) {
			in.AgentLeaseActive, in.IngressAgentLeaseActive = true, true
		}), registry.PoolStateActive},
		{"ingress agent lease, draining", with(agentOnly, func(in *poolStateInput) {
			in.AgentLeaseActive, in.IngressAgentLeaseActive, in.Desired = true, true, registry.PoolStateDraining
		}), registry.PoolStateDraining},
		{"ingress agent lease expired", with(agentOnly, func(in *poolStateInput) { in.Started = true }), registry.PoolStateUnhealthy},

		{"labelled relay, ready", relayLabel, registry.PoolStateRegistered},
		{"labelled relay, ready, started before", with(relayLabel, func(in *poolStateInput) { in.Started = true }), registry.PoolStateUnhealthy},
		{"labelled relay and ready ingress", with(relayLabel, func(in *poolStateInput) { in.ReadyIngressEndpoints = 1 }), registry.PoolStateActive},
		{"ready ingress, cluster lease expired", with(relayLabel, func(in *poolStateInput) {
			in.ReadyIngressEndpoints, in.ClusterLeaseActive, in.Started = 1, false, true
		}), registry.PoolStateUnhealthy},
		{"ingress agent lease, cluster lease expired", with(relayLabel, func(in *poolStateInput) {
			in.ClusterLeaseActive, in.IngressAgentLeaseActive, in.Agents = false, true, true
		}), registry.PoolStateActive},

		// An API registration with agents follows the agents, not its
		// pool lease.
		{"API pool with an ingress agent", poolStateInput{
			PoolStateInput: registry.PoolStateInput{Overlay: true}, Agents: true, IngressAgentLeaseActive: true,
		}, registry.PoolStateActive},
		{"API pool lease without a live agent", poolStateInput{
			PoolStateInput: registry.PoolStateInput{Overlay: true, PoolLeaseActive: true, Started: true}, Agents: true,
		}, registry.PoolStateUnhealthy},
		{"API pool without agents keeps the pool lease rule", poolStateInput{
			PoolStateInput: registry.PoolStateInput{Overlay: true, PoolLeaseActive: true, Started: true},
		}, registry.PoolStateActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, derivePoolState(tt.in))
		})
	}
}
