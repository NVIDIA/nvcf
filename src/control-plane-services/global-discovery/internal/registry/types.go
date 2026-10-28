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

// Package registry holds the GDS domain rules that do not depend on storage
// or transport: lease evaluation, lease TTL bounds, writer generations, pool
// roles and pool state derivation.
package registry

import (
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
)

// Lease defaults and bounds.
const (
	// DefaultClusterLeaseTTL applies when RegisterCluster names no TTL.
	DefaultClusterLeaseTTL = 30 * time.Second
	// DefaultPoolLeaseTTL applies when RegisterPool names no TTL.
	DefaultPoolLeaseTTL = 60 * time.Second
	// DefaultPoolAgentLeaseTTL applies when RegisterPoolAgent names no TTL.
	DefaultPoolAgentLeaseTTL = 60 * time.Second
	// MinLeaseTTL and MaxLeaseTTL bound every requested TTL.
	MinLeaseTTL = 10 * time.Second
	MaxLeaseTTL = 5 * time.Minute
	// DefaultLeaseSkew is the clock skew allowance between replicas and
	// writers before a lease counts as expired.
	DefaultLeaseSkew = 2 * time.Second
)

// LeaseState is the state of a lease evaluated at read time.
type LeaseState string

const (
	LeaseStateActive  LeaseState = "ACTIVE"
	LeaseStateExpired LeaseState = "EXPIRED"
)

// PoolState is the desired or derived state of a pool.
type PoolState string

const (
	// PoolStateRegistered: known, but not yet heartbeating or serving.
	PoolStateRegistered PoolState = "REGISTERED"
	// PoolStateActive: live and accepting traffic.
	PoolStateActive PoolState = "ACTIVE"
	// PoolStateDraining: live, but routers stop choosing it.
	PoolStateDraining PoolState = "DRAINING"
	// PoolStateUnhealthy: lease expired or no ready endpoint.
	PoolStateUnhealthy PoolState = "UNHEALTHY"
	// PoolStateGone: deregistered, or both the labelled export and the API
	// registration are gone. Kept briefly as a tombstone.
	PoolStateGone PoolState = "GONE"
)

// AgentSource says where a pool agent is known from.
type AgentSource string

const (
	// AgentSourceLabel: an exported Service with the agent's role.
	AgentSourceLabel AgentSource = "LABEL"
	// AgentSourceAPI: a RegisterPoolAgent registration.
	AgentSourceAPI AgentSource = "API"
	// AgentSourceBoth: both.
	AgentSourceBoth AgentSource = "BOTH"
)

// AgentSourceOf returns the source of an agent known from an exported
// Service (label), a registration (api), or both. It is empty for neither.
func AgentSourceOf(label, api bool) AgentSource {
	switch {
	case label && api:
		return AgentSourceBoth
	case label:
		return AgentSourceLabel
	case api:
		return AgentSourceAPI
	}
	return ""
}

// DialMode says how routers and a pool agent connect.
type DialMode string

const (
	// DialModeInbound: routers dial the agent (the OPENAI frontend).
	DialModeInbound DialMode = "INBOUND"
	// DialModeOutbound: the agent dials the routers (PYLON, KV_RELAY).
	DialModeOutbound DialMode = "OUTBOUND"
)

// Role is the role of an exported Service, from the gds.nvidia.com/role
// label.
type Role string

const (
	RoleRouter       Role = api.RoleRouter
	RolePoolFrontend Role = api.RolePoolFrontend
	RoleKVRelay      Role = api.RoleKVRelay
	RolePylon        Role = api.RolePylon
)

// ParseRole returns the Role of a gds.nvidia.com/role label value.
func ParseRole(value string) (Role, bool) {
	if !api.IsRole(value) {
		return "", false
	}
	return Role(value), true
}

// IsPoolRole reports whether the role attaches an export to a pool.
func (r Role) IsPoolRole() bool {
	return api.IsPoolRole(string(r))
}
