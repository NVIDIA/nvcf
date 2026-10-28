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

// Package store defines the persistence contract of the GDS server: one
// interface per aggregate (clusters, exports, endpoint sets, pools, pool
// agents) composed
// into Store, the record types they persist, and typed errors.
//
// Every record belongs to a clusterset. Records are written by exactly one
// logical writer (the controller of a cluster, the operator of a pool), and
// every write is conditional:
//
//   - PutCluster mints a new lease epoch max(stored+1, now in unix ms), and
//     so do PutPool and PutPoolAgent when asked to.
//   - RenewClusterLease, RenewPoolLease and RenewPoolAgentLease require the
//     current epoch.
//   - PutExport, PutEndpointSet, PutPool and PutPoolAgent require a
//     generation greater than the stored one and report the stored one in
//     ErrStaleGeneration.
//   - PutPool keeps pool names unique per (clusterset, cluster).
//   - Deletes are idempotent; DeletePool cascades to the pool's agent
//     registrations and DeleteCluster to everything the cluster owns.
//
// Leases are never expired by the store. The server evaluates them at read
// time from Lease.RenewedAt and Lease.TTL. Timestamps passed in as now are
// server time.
//
// Implementations: memory (development, tests, single replica) and dynamodb
// (one Global Table, added later). storetest.Run is the conformance suite
// both must pass.
package store

import (
	"context"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
)

// Store is the complete persistence contract of the server.
type Store interface {
	ClusterStore
	ExportStore
	EndpointStore
	PoolStore
	PoolAgentStore

	// ListClusterSets returns the names of every clusterset that has had a
	// record written, sorted. A clusterset stays listed after its records
	// are deleted until compaction removes it.
	ListClusterSets(ctx context.Context) ([]string, error)
	// LoadClusterSet returns every record of one clusterset. An unknown
	// clusterset yields an empty snapshot. The snapshot may be eventually
	// consistent across records but each record is whole.
	LoadClusterSet(ctx context.Context, clusterSet string) (ClusterSetSnapshot, error)
	// Ping reports whether the backend is reachable. It returns
	// ErrUnavailable, possibly wrapped, when it is not.
	Ping(ctx context.Context) error
	// Close releases the store's resources. Calls after Close fail.
	Close() error
}

// ClusterStore persists cluster registrations and their leases.
type ClusterStore interface {
	// PutCluster registers or re-registers a cluster. It mints a new lease
	// epoch max(stored epoch + 1, now.UnixMilli()), sets Lease.RenewedAt and
	// UpdatedAt to now, increments Generation, keeps the RegisteredAt of an
	// existing registration (now for a new one) and stores rec.Lease.TTL.
	// It returns the stored record.
	PutCluster(ctx context.Context, rec ClusterRecord, now time.Time) (ClusterRecord, error)
	// RenewClusterLease sets Lease.RenewedAt to now when epoch equals the
	// stored lease epoch. It returns ErrNotFound for an unknown cluster and
	// ErrLeaseEpochMismatch for another epoch.
	RenewClusterLease(ctx context.Context, key ClusterKey, epoch uint64, now time.Time) (ClusterRecord, error)
	// GetCluster returns ErrNotFound for an unknown cluster.
	GetCluster(ctx context.Context, key ClusterKey) (ClusterRecord, error)
	// DeleteCluster removes the cluster and every export, endpoint set, pool
	// and pool agent registration of the cluster, including the pools' name
	// reservations. It is idempotent: deleting an unknown cluster succeeds,
	// and records of the cluster written without a registration are removed
	// as well.
	DeleteCluster(ctx context.Context, key ClusterKey) error
}

// ExportStore persists ServiceExport records.
type ExportStore interface {
	// PutExport creates or replaces an export when rec.Generation is greater
	// than the stored generation, and returns ErrStaleGeneration with the
	// stored generation otherwise. The record is stored as given.
	PutExport(ctx context.Context, rec ExportRecord) error
	// DeleteExport removes the export and its endpoint set. It is
	// idempotent.
	DeleteExport(ctx context.Context, key ExportKey) error
}

// EndpointStore persists the endpoint set of each export. A set is only ever
// replaced as a whole.
type EndpointStore interface {
	// PutEndpointSet replaces the endpoint set when rec.Generation is
	// greater than the stored generation, and returns ErrStaleGeneration
	// with the stored generation otherwise. It returns ErrTooLarge for more
	// than MaxEndpointsPerSet endpoints or a set above the backend's item
	// size limit. An endpoint set may exist without its export.
	PutEndpointSet(ctx context.Context, rec EndpointSetRecord) error
	// DeleteEndpointSet removes the endpoint set. It is idempotent.
	DeleteEndpointSet(ctx context.Context, key ExportKey) error
}

// PoolStore persists API-registered pools. Label-derived pools are computed
// from exports and never stored.
type PoolStore interface {
	// PutPool creates or replaces a pool when rec.Generation is greater than
	// the stored generation (ErrStaleGeneration otherwise). The pool's name
	// must be unused by other pools of the same (clusterset, cluster)
	// (ErrPoolNameTaken otherwise); a rename releases the old name.
	//
	// With opts.MintLease the store mints a new lease epoch
	// max(stored epoch + 1, opts.Now.UnixMilli()) with TTL rec.Lease.TTL
	// and RenewedAt opts.Now; otherwise it keeps the stored lease (none for
	// a new pool). RegisteredAt is kept from an existing pool and set to
	// opts.Now for a new one; UpdatedAt is set to opts.Now.
	// Spec.Location.Cluster is set to the key's cluster. It returns the
	// stored record.
	PutPool(ctx context.Context, rec PoolRecord, opts PutPoolOptions) (PoolRecord, error)
	// RenewPoolLease sets Lease.RenewedAt and UpdatedAt to now and applies
	// the patch when epoch equals the stored, non-zero lease epoch. It
	// returns ErrNotFound for an unknown pool and ErrLeaseEpochMismatch for
	// another epoch or a pool without a lease. Generation is unchanged.
	RenewPoolLease(ctx context.Context, key PoolKey, epoch uint64, now time.Time, patch PoolHeartbeatPatch) (PoolRecord, error)
	// DeletePool removes the pool, releases its name and removes every
	// agent registration of the pool (same cluster and pool id), also when
	// no pool record exists. It is idempotent.
	DeletePool(ctx context.Context, key PoolKey) error
}

// PoolAgentStore persists the API registrations of pool agents: one per
// process instance, keyed by (cluster, pool id, role, agent id), each with
// its own lease, so that the instances of one role never fence each other.
// Label-derived agents are computed from exports and never stored.
type PoolAgentStore interface {
	// PutPoolAgent creates or replaces an agent registration when
	// rec.Generation is greater than the stored generation
	// (ErrStaleGeneration otherwise).
	//
	// With opts.MintLease the store mints a new lease epoch
	// max(stored epoch + 1, opts.Now.UnixMilli()) with TTL rec.Lease.TTL
	// and RenewedAt opts.Now; otherwise it keeps the stored lease (none for
	// a new registration). RegisteredAt is kept from an existing
	// registration and set to opts.Now for a new one; UpdatedAt is set to
	// opts.Now. It returns the stored record.
	PutPoolAgent(ctx context.Context, rec PoolAgentRecord, opts PutPoolAgentOptions) (PoolAgentRecord, error)
	// RenewPoolAgentLease sets Lease.RenewedAt to now and applies the patch
	// when epoch equals the stored, non-zero lease epoch; UpdatedAt is set
	// to now when the patch changes the attributes. It returns ErrNotFound
	// for an unknown registration and ErrLeaseEpochMismatch for another
	// epoch or a registration without a lease. Generation is unchanged.
	RenewPoolAgentLease(ctx context.Context, key PoolAgentKey, epoch uint64, now time.Time, patch PoolAgentHeartbeatPatch) (PoolAgentRecord, error)
	// DeletePoolAgent removes the registration. It is idempotent.
	DeletePoolAgent(ctx context.Context, key PoolAgentKey) error
}

// PutPoolAgentOptions controls PutPoolAgent.
type PutPoolAgentOptions struct {
	// Now is the server time of the write.
	Now time.Time
	// MintLease starts a new lease incarnation, as RegisterPoolAgent does.
	MintLease bool
}

// PutPoolOptions controls PutPool.
type PutPoolOptions struct {
	// Now is the server time of the write.
	Now time.Time
	// MintLease starts a new lease incarnation, as RegisterPool does.
	// UpdatePool leaves it false.
	MintLease bool
}

// ChangeHinter is an optional Store extension that signals that a clusterset
// may have changed in another region, so the server can refresh it before
// its next tick. The DynamoDB store can implement it with DynamoDB Streams.
// No implementation exists in v1; the server polls with LoadClusterSet.
type ChangeHinter interface {
	// ChangeHints returns a channel of clusterset names. The channel is
	// closed when ctx is done.
	ChangeHints(ctx context.Context) (<-chan string, error)
}

// MaxEndpointsPerSet is the largest endpoint set PutEndpointSet accepts.
const MaxEndpointsPerSet = api.MaxEndpointsPerExport

// ClusterKey identifies a cluster record.
type ClusterKey struct {
	ClusterSet string
	Cluster    string
}

// ExportKey identifies an export record and its endpoint set.
type ExportKey struct {
	ClusterSet string
	Cluster    string
	Namespace  string
	Name       string
}

// ClusterKey returns the key of the exporting cluster.
func (k ExportKey) ClusterKey() ClusterKey {
	return ClusterKey{ClusterSet: k.ClusterSet, Cluster: k.Cluster}
}

// PoolKey identifies a pool record. The cluster is part of the key because
// pools are partitioned by cluster; the API addresses pools by
// (clusterset, pool id) and resolves the cluster from the view.
type PoolKey struct {
	ClusterSet string
	Cluster    string
	PoolID     string
}

// ClusterKey returns the key of the pool's cluster.
func (k PoolKey) ClusterKey() ClusterKey {
	return ClusterKey{ClusterSet: k.ClusterSet, Cluster: k.Cluster}
}

// PoolAgentKey identifies a pool agent registration: one per process
// instance (AgentID) of a role of a pool. The cluster is part of the key, as
// for pools.
type PoolAgentKey struct {
	ClusterSet string
	Cluster    string
	PoolID     string
	Role       FrontendRole
	// AgentID identifies the registering process, for example a Pylon
	// inference_server_id or a relay pod name.
	AgentID string
}

// PoolKey returns the key of the agent's pool.
func (k PoolAgentKey) PoolKey() PoolKey {
	return PoolKey{ClusterSet: k.ClusterSet, Cluster: k.Cluster, PoolID: k.PoolID}
}

// ClusterKey returns the key of the agent's cluster.
func (k PoolAgentKey) ClusterKey() ClusterKey {
	return ClusterKey{ClusterSet: k.ClusterSet, Cluster: k.Cluster}
}

// ClusterSetSnapshot is every record of one clusterset. Each slice is sorted
// by key.
type ClusterSetSnapshot struct {
	ClusterSet   string
	Clusters     []ClusterRecord
	Exports      []ExportRecord
	EndpointSets []EndpointSetRecord
	Pools        []PoolRecord
	PoolAgents   []PoolAgentRecord
}

// Lease is a server-minted liveness lease. It is evaluated at read time; see
// registry.LeaseExpired.
type Lease struct {
	// Epoch is the incarnation; 0 means no lease.
	Epoch     uint64
	TTL       time.Duration
	RenewedAt time.Time
}

// ExpiresAt returns RenewedAt plus TTL.
func (l Lease) ExpiresAt() time.Time {
	return l.RenewedAt.Add(l.TTL)
}

// ClusterRecord is a cluster registration.
type ClusterRecord struct {
	Key          ClusterKey
	Region       string
	Zones        []string
	Provider     string
	Labels       map[string]string
	AgentID      string
	AgentVersion string
	Lease        Lease
	// Generation is incremented by the store on every registration.
	Generation   uint64
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

// ExportRecord is the export of one Service from one cluster.
type ExportRecord struct {
	Key        ExportKey
	Spec       ExportSpec
	Generation uint64
	UpdatedAt  time.Time
}

// EndpointSetRecord is the full endpoint set of one export.
type EndpointSetRecord struct {
	Key        ExportKey
	Endpoints  []Endpoint
	Generation uint64
	UpdatedAt  time.Time
}

// PoolRecord is an API-registered pool.
type PoolRecord struct {
	Key  PoolKey
	Name string
	Spec PoolSpec
	// Lease has Epoch 0 when the pool has none.
	Lease      Lease
	Generation uint64
	// OwnerID identifies the registering writer.
	OwnerID      string
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

// PoolAgentRecord is the API registration of one pool agent.
type PoolAgentRecord struct {
	Key PoolAgentKey
	// PoolName is the name of the agent's pool, the join key with labelled
	// exports and pool records.
	PoolName string
	// Attributes are writer-owned.
	Attributes map[string]string
	// Lease has Epoch 0 when the registration has none.
	Lease        Lease
	Generation   uint64
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

// PoolAgentHeartbeatPatch is the optional state an agent heartbeat reports.
type PoolAgentHeartbeatPatch struct {
	// Attributes replace the stored attributes when non-empty.
	Attributes map[string]string
}

// PoolHeartbeatPatch is the optional state a pool heartbeat reports. Nil
// fields and an empty DesiredState leave the stored values unchanged.
type PoolHeartbeatPatch struct {
	WorkerCount      *int32
	ReadyWorkerCount *int32
	DesiredState     DesiredState
}
