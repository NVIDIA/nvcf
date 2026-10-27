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
// that name the pool and the lease of the pool's cluster.
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
	// ClusterLeaseActive reports that the lease of the pool's cluster is
	// active. False when the cluster is not registered.
	ClusterLeaseActive bool
	// PoolLeaseActive reports that the lease of the API registration is
	// active.
	PoolLeaseActive bool
	// ReadyEndpoints counts the ready endpoints of the pool's Services in
	// its cluster.
	ReadyEndpoints int
	// Started reports that the pool has been live once since it was
	// registered: an API-only pool has heartbeated after registration, a
	// joined pool has had a ready endpoint. A joined pool with a ready
	// endpoint counts as started even when Started is false.
	Started bool
}

// APIOnly reports whether liveness comes from the pool's own lease: an API
// registration without services[] and without a labelled export.
func (in PoolStateInput) APIOnly() bool {
	return in.Overlay && !in.OverlayServices && !in.LabelledExport
}

// Live reports whether the pool currently serves. An API-only pool is live
// while its lease is active. A pool joined to exports is live while its
// cluster lease is active and at least one of its endpoints is ready.
func (in PoolStateInput) Live() bool {
	if in.APIOnly() {
		return in.PoolLeaseActive
	}
	return in.ClusterLeaseActive && in.ReadyEndpoints > 0
}

// DerivePoolState computes the state of a pool:
//
//   - GONE after DeregisterPool, or when neither a labelled export nor an
//     API registration exists.
//   - REGISTERED until the pool has started (first heartbeat of an API-only
//     pool, first ready endpoint of a joined pool) while the lease it
//     depends on is active; UNHEALTHY if that lease expires first.
//   - Once started, the desired state (ACTIVE or DRAINING) while the pool is
//     live, UNHEALTHY otherwise.
func DerivePoolState(in PoolStateInput) PoolState {
	if in.Deregistered || (!in.LabelledExport && !in.Overlay) {
		return PoolStateGone
	}
	apiOnly := in.APIOnly()
	started := in.Started || (!apiOnly && in.ReadyEndpoints > 0)
	if !started {
		leaseActive := in.ClusterLeaseActive
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
