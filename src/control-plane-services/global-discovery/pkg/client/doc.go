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

// Package client is the Go client of the Global Discovery Service (GDS).
//
// A Client holds one gRPC connection for unary calls and watches, with:
//
//   - Endpoint failover. Config.URLs is an ordered list, normally the global
//     name first and per-region names after it. A per-client gRPC resolver
//     resolves every host name in URL order on start and on every
//     re-resolution gRPC asks for (after each lost or failed connection),
//     with the injected Config.Resolver, and sets each address's TLS server
//     name to its URL's host. pick_first, without shuffling, connects to
//     the first address that answers. The GDS endpoints the server
//     advertises in GetServerInfo are appended after the configured URLs.
//     Every PrimaryRetryInterval a client connected elsewhere probes the
//     primary URL and, when it answers, moves its connection and its
//     WatchLoops back to it.
//   - Unary calls with a default deadline (RPCTimeout) and gRPC retries on
//     UNAVAILABLE (MaxAttempts, Backoff).
//   - Typed sub-clients: Clusters, ServiceExports, ServiceImports, Pools,
//     Routers and ServerInfo.
//   - OnReconnect hooks for writers that resend state after a reconnect.
//   - nvcf_gds_client_* metrics when Config.Registerer is set.
//
// Helpers built on it:
//
//   - WatchLoop: keeps one watch open, reconnects with backoff, resumes from
//     the last revision, and maps SNAPSHOT_START, UPSERT, DELETE,
//     SNAPSHOT_END and BOOKMARK to handler calls, with an OnResync callback
//     when the server sends a new snapshot.
//   - PoolCache and RouterCache: an initial list, watch deltas, a full list
//     every TTL (anti-entropy) and a Stale flag after 2 x TTL without
//     success; Snapshot, Updates and WaitSynced. RouterState.SeedURLs
//     returns the pylon seeds, one URL per ready router replica of every
//     router cluster.
//   - NextGeneration and Generation for writer generations,
//     max(previous + 1, unix milliseconds), and StaleGeneration and Reason
//     to read GDS error details.
//
// pkg/fakegds is an in-memory GDS for tests of code using this package.
package client
