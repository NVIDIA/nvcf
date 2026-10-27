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
//   - Cluster and pool leases with server-minted epochs. Heartbeats with
//     another epoch fail with ABORTED and reason LEASE_EPOCH_MISMATCH.
//     Leases are evaluated at read time against the server clock (see
//     WithClock) with the default 2 s skew allowance.
//   - Writer generations. A PutServiceExport, ReplaceEndpoints, RegisterPool
//     or UpdatePool whose generation does not exceed the stored one fails with
//     FAILED_PRECONDITION, reason STALE_GENERATION, ErrorInfo metadata
//     current_generation and a PreconditionFailure detail, like the server.
//   - Pools registered through the API (RegisterPool, UpdatePool with a field
//     mask, HeartbeatPool, DeregisterPool) and pools derived from exports
//     labelled gds.nvidia.com/role=pool-frontend or kv-relay with
//     gds.nvidia.com/pool, merged by (cluster, pool name). Pool state is
//     derived with the server's rules (internal/registry).
//   - Routers derived from exports labelled gds.nvidia.com/role=router.
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
// Seeding helpers (EnsureCluster, PutExport, DeleteExport) write state
// directly, with RouterSpec, PoolFrontendSpec and ReadyEndpoints building
// typical export specs. They broadcast to watchers like the RPCs do.
//
// Differences from gds-server: one replica and no store, so writes are
// visible at once; no conflict detection (Conflict is never set); IP
// families come from the winning export; a deregistered pool is removed at
// once instead of being kept as a GONE tombstone (watchers still receive a
// DELETE carrying state GONE); no limits beyond required fields are
// validated. PutServiceExport and ReplaceEndpoints require a registered
// cluster (FAILED_PRECONDITION, CLUSTER_NOT_REGISTERED).
package fakegds
