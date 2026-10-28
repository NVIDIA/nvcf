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

// PoolStateInput is what DerivePoolState needs to know about a pool at one
// point in time. The view fills it from the pool's API record, the exports
// that name the pool, the pool's agent registrations and the lease of the
// pool's cluster.
type PoolStateInput struct {
	// Desired is the overlay's desired state: PoolStateActive or
	// PoolStateDraining. Anything else counts as active.
	Desired PoolState
	// Deregistered is set after DeregisterPool, while the tombstone is kept.
	Deregistered bool
	// LabelledExport reports that at least one export labelled with the
	// pool's name and a pool role exists in the pool's cluster.
	LabelledExport bool
	// Overlay reports that an API registration exists.
	Overlay bool
	// OverlayServices reports that the API registration lists services[]
	// for the endpoint join.
	OverlayServices bool
	// Agents reports that at least one agent registration
	// (RegisterPoolAgent) of the pool exists, whatever its lease state.
	Agents bool
	// ClusterLeaseActive reports that the lease of the pool's cluster is
	// active. False when the cluster is not registered.
	ClusterLeaseActive bool
	// PoolLeaseActive reports that the lease of the API registration is
	// active.
	PoolLeaseActive bool
	// AgentLeaseActive reports that the lease of at least one agent
	// registration of any role is active.
	AgentLeaseActive bool
	// IngressAgentLeaseActive reports that the lease of at least one
	// ingress agent registration (OPENAI or PYLON) is active.
	IngressAgentLeaseActive bool
	// ReadyIngressEndpoints counts the ready endpoints of the pool's
	// ingress Services (OPENAI and PYLON) in its cluster.
	ReadyIngressEndpoints int
	// Started reports that the pool has been live once since it was
	// registered: an API-only pool has heartbeated after registration, any
	// other pool has been live. A pool that is live counts as started even
	// when Started is false, except an API-only pool, which starts with its
	// first heartbeat.
	Started bool
}

// APIOnly reports whether liveness comes from the pool's own lease: an API
// registration without services[], without a labelled export and without
// agent registrations.
func (in PoolStateInput) APIOnly() bool {
	return in.Overlay && !in.OverlayServices && !in.LabelledExport && !in.Agents
}

// Live reports whether the pool currently serves. An API-only pool is live
// while its lease is active. Any other pool is live while its cluster lease
// is active and an ingress Service has a ready endpoint, or while an
// ingress agent's lease is active. A KV_RELAY agent alone never makes a
// pool live.
func (in PoolStateInput) Live() bool {
	if in.APIOnly() {
		return in.PoolLeaseActive
	}
	return (in.ClusterLeaseActive && in.ReadyIngressEndpoints > 0) || in.IngressAgentLeaseActive
}

// DerivePoolState computes the state of a pool:
//
//   - GONE after DeregisterPool, or when no labelled export, no API
//     registration and no agent registration exists.
//   - REGISTERED until the pool has started (first heartbeat of an API-only
//     pool, first time live otherwise) while a lease it depends on is
//     active: the pool lease for an API-only pool, else the cluster lease,
//     an agent lease or the pool lease. UNHEALTHY when none is.
//   - Once started, the desired state (ACTIVE or DRAINING) while the pool is
//     live, UNHEALTHY otherwise.
//
// A pool whose only live agent is a KV_RELAY therefore stays REGISTERED.
func DerivePoolState(in PoolStateInput) PoolState {
	if in.Deregistered || (!in.LabelledExport && !in.Overlay && !in.Agents) {
		return PoolStateGone
	}
	apiOnly := in.APIOnly()
	started := in.Started || (!apiOnly && in.Live())
	if !started {
		leaseActive := in.ClusterLeaseActive || in.AgentLeaseActive || in.PoolLeaseActive
		if apiOnly {
			leaseActive = in.PoolLeaseActive
		}
		if leaseActive {
			return PoolStateRegistered
		}
		return PoolStateUnhealthy
	}
	if !in.Live() {
		return PoolStateUnhealthy
	}
	if in.Desired == PoolStateDraining {
		return PoolStateDraining
	}
	return PoolStateActive
}
