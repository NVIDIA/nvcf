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

import "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"

// poolStateInput is registry.PoolStateInput with the pool agent facts of
// the typed pool agents revision (design v0.5, section 4.6).
//
// The server's rule takes the same facts as new fields of
// registry.PoolStateInput. Until that change is in this module, the fake
// derives the state here: derivePoolState equals registry.DerivePoolState
// whenever the agent facts are absent (no agent registration and
// ReadyIngressEndpoints equal to ReadyEndpoints), which
// TestDerivePoolStateWithoutAgentsMatchesRegistry checks.
type poolStateInput struct {
	registry.PoolStateInput
	// ReadyIngressEndpoints counts the ready endpoints of the pool's
	// ingress Services (roles OPENAI and PYLON) while the cluster lease is
	// active. It replaces ReadyEndpoints in the derivation.
	ReadyIngressEndpoints int
	// IngressAgentLeaseActive reports that the lease of an OPENAI or PYLON
	// agent registration is active.
	IngressAgentLeaseActive bool
	// Agents reports that at least one agent registration exists.
	Agents bool
	// AgentLeaseActive reports that the lease of any agent registration,
	// KV_RELAY included, is active.
	AgentLeaseActive bool
}

// apiOnly reports a pool registered through RegisterPool with no services,
// no labelled export and no agent registration: it lives on its pool lease.
func (in poolStateInput) apiOnly() bool {
	return in.APIOnly() && !in.Agents
}

// live reports that the pool can take requests: for an API-only pool its
// lease is active; otherwise an ingress Service has a ready endpoint while
// the cluster lease is active, or an ingress agent's lease is active.
func (in poolStateInput) live() bool {
	if in.apiOnly() {
		return in.PoolLeaseActive
	}
	return (in.ClusterLeaseActive && in.ReadyIngressEndpoints > 0) || in.IngressAgentLeaseActive
}

// started reports that the pool has been live once, given whether it was
// before this evaluation.
func (in poolStateInput) started() bool {
	return in.Started || (!in.apiOnly() && in.live())
}

// derivePoolState derives the pool state: GONE when deregistered or when no
// labelled export, overlay or agent registration is left; REGISTERED before
// the pool was first live while a lease is active (the pool lease for an
// API-only pool, else the cluster lease or an agent lease), else UNHEALTHY;
// once started, the desired state while live and UNHEALTHY otherwise. A pool
// whose only live agent is a KV_RELAY is never live, so it stays REGISTERED.
func derivePoolState(in poolStateInput) registry.PoolState {
	if in.Deregistered || (!in.LabelledExport && !in.Overlay && !in.Agents) {
		return registry.PoolStateGone
	}
	if !in.started() {
		leaseActive := in.ClusterLeaseActive || in.AgentLeaseActive
		if in.apiOnly() {
			leaseActive = in.PoolLeaseActive
		}
		if leaseActive {
			return registry.PoolStateRegistered
		}
		return registry.PoolStateUnhealthy
	}
	if !in.live() {
		return registry.PoolStateUnhealthy
	}
	if in.Desired == registry.PoolStateDraining {
		return registry.PoolStateDraining
	}
	return registry.PoolStateActive
}
