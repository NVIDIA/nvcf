// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! nvstor RDMA transport — zero-copy data transfer via libibverbs.
//!
//! Provides RDMA-based inter-node communication for:
//! - Object transfer (`s3_rdma`): SEND/RECV control messages; GET is a
//!   server RDMA WRITE into the client's buffer, PUT a server RDMA READ
//!   from it
//! - Chunk replication (`s3_rdma` REPLICATE: the replica RDMA-READs the
//!   writer's buffer; cluster peers only), with a TCP RPC fallback (`rpc`)
//! - The NFS-over-RDMA listener's connection setup (`cm`, RDMA-CM)
//!
//! Every transport connection starts with an authenticated TCP bootstrap
//! (`bootstrap`, protocol v2): no QP exists before the connector has
//! proven a credential, no memory address or rkey is ever advertised, and
//! a server's QPs grant peers no remote access.
//!
//! Architecture:
//!   - RdmaDevice: manages ibverbs context, protection domain, memory regions
//!   - QueuePair/CompletionQueue: per-peer connection with RDMA operations
//!   - RdmaTransport: authenticated multi-lane connections to peers
//!   - Rails: one RdmaTransport per NIC (`rail`)
//!
//! FFI bindings are hand-written (ffi module) to avoid bindgen compatibility
//! issues. Only the subset of libibverbs we actually use is bound.

pub mod admission;
pub mod auth;
pub mod bootstrap;
#[cfg(feature = "rdma-cm")]
pub mod cm;
pub mod completion;
pub mod connection;
pub mod control_channel;
pub mod device;
pub mod exchange;
pub mod ffi;
pub mod hydrate;
pub mod kv_proto;
pub mod object;
pub mod pool;
pub mod rail;
pub mod region;
pub mod rpc;
pub mod s3_rdma;
pub mod s3_rdma_proto;
pub mod tcp;
pub mod transport;
