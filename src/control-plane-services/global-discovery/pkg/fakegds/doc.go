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

// Package fakegds is an in-memory Global Discovery Service for tests.
//
// It implements the five GDS gRPC services (ClusterService,
// ServiceExportService, ServiceImportService, PoolService and
// ServerInfoService) with the semantics clients rely on, so that client,
// controller and integration tests can run against a real gRPC endpoint
// without a store or a server process:
//
//   - Cluster, pool and pool agent leases with server-minted epochs.
//     Heartbeats with another epoch fail with ABORTED and reason
//     LEASE_EPOCH_MISMATCH. Leases are evaluated at read time against the
//     server clock (see WithClock) with the default 2 s skew allowance.
//   - Writer generations. A PutServiceExport, ReplaceEndpoints,
//     RegisterPool, UpdatePool or RegisterPoolAgent whose generation does
//     not exceed the stored one fails with FAILED_PRECONDITION, reason
//     STALE_GENERATION, ErrorInfo metadata current_generation and a
//     PreconditionFailure detail, like the server. RegisterPoolAgent assigns
//     a generation when none is given, as the server does.
//   - Pools from three sources merged by (cluster, pool name): the Pool API
//     (RegisterPool, UpdatePool with a field mask, HeartbeatPool,
//     DeregisterPool), exports with a pool role and gds.nvidia.com/pool
//     (roles pool-frontend, kv-relay and pylon from gds.nvidia.com/role, or
//     several from the gds.nvidia.com/roles annotation, api.ParseRoles), and
//     pool agent registrations (RegisterPoolAgent, HeartbeatPoolAgent,
//     DeregisterPoolAgent), one per (pool, role, agent_id), each with its
//     own lease and epoch (default TTL 60 s), so N Pylon pods of one pool do
//     not fence each other. A pool registered without pool_id, created by
//     an agent or known only from exports has the deterministic id of the
//     server (LabelPoolID). DeregisterPool removes every registration of the
//     pool.
//   - Pool agents: Pool.agents has one entry per role present, sorted by
//     role, merging the exported Service (LABEL) and the registrations
//     (API) into BOTH; registrations[] is sorted by agent_id, and the
//     agent's lease, agent_id, attributes, generation and timestamps mirror
//     the registration whose lease expires last. OPENAI dials INBOUND,
//     KV_RELAY and PYLON OUTBOUND. An agent is live with a ready endpoint
//     while the cluster lease is active or with any active registration
//     lease. kv_relay_live reports a live KV_RELAY,
//     ready_ingress_count the ready endpoints of the OPENAI and PYLON
//     Services (a Service with both roles counts once), ready_endpoint_count
//     those of OPENAI only.
//   - Pool state with the server's rules (internal/registry) extended by the
//     agents: a pool is live while an ingress Service has a ready endpoint
//     and the cluster lease is active, or while an OPENAI or PYLON
//     registration lease is active; an API-only pool without agents lives
//     on its pool lease. REGISTERED until first live while the cluster
//     lease or a registration lease is active, so a pool whose only live
//     agent is a KV_RELAY stays REGISTERED.
//   - Routers derived from exports labelled gds.nvidia.com/role=router, with
//     the gRPC, HTTP and QUIC port numbers (gds.nvidia.com/quic-port-name,
//     default quic), every Service port, the router mode
//     (gds.nvidia.com/router-mode, api.RouterModeOf), the hostname template
//     (gds.nvidia.com/hostname-template, verbatim) and each replica's
//     identity (endpoint hostname, else the targetRef.name attribute,
//     api.ReplicaIdentity).
//   - Merged ServiceImports, simplified: the oldest export of a cluster with
//     an active lease wins (export creation time, then cluster name), ports
//     are unioned by (port, protocol) and no conflicts are reported.
//   - Watches for clusters, service imports, pools and routers: a snapshot
//     (SNAPSHOT_START, one UPSERT per object, SNAPSHOT_END), then UPSERT and
//     DELETE deltas and periodic BOOKMARKs, all carrying revisions of the
//     form <view_epoch>:<seq>. Every change is broadcast to the watchers of
//     its kind and clusterset; filters are applied per watcher, and an
//     object that stops matching a watcher's filter is sent to it as a
//     DELETE. A resume_revision of this server that is still in the history
//     replays the missed deltas; any other revision yields a new snapshot.
//
// Fault injection: FailNext makes the next call of a method fail with a
// given code, CloseWatches ends every open watch, and Stop on a Running
// listener drops its connections. CallCount and WatcherCount let tests
// observe traffic.
//
// Usage:
//
//	srv := fakegds.New()
//	run := srv.ServeBufconn()          // or srv.ServeLocal() on 127.0.0.1:0
//	defer run.Stop()
//	conn, _ := grpc.NewClient("passthrough:///fakegds",
//		grpc.WithContextDialer(run.Dialer()),
//		grpc.WithTransportCredentials(insecure.NewCredentials()))
//
// Errors carry an ErrorInfo with the GDS domain and the server's reason,
// INVALID_ARGUMENT (with a BadRequest) and NOT_FOUND included.
//
// Seeding helpers (EnsureCluster, PutExport, DeleteExport) write state
// directly, with RouterSpec, RouterSpecWithQUIC, PoolFrontendSpec,
// PylonSpec, KVRelaySpec, WithRoles and ReadyEndpoints building typical
// export specs. They broadcast to watchers like the RPCs do.
//
// Differences from gds-server: one replica and no store, so writes are
// visible at once; no conflict detection (Conflict is never set); IP
// families come from the winning export; a deregistered pool is removed at
// once instead of being kept as a GONE tombstone (watchers still receive a
// DELETE carrying state GONE); beyond required fields only pool agent
// requests are validated (role, agent_id length, attribute count, pool name
// and id). PutServiceExport and ReplaceEndpoints require a registered
// cluster (FAILED_PRECONDITION, CLUSTER_NOT_REGISTERED).
package fakegds
