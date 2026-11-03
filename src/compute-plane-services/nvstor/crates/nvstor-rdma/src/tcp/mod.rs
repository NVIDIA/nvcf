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

//! TCP-backed S3 transport — same dispatcher shape as
//! [`crate::s3_rdma`], same `WriteFn`/`ReadFn`/`DeleteFn` callbacks
//! (`crate::object`), but uses plain TCP sockets instead of ibverbs for the
//! data path. Lets s3-rdma's protocol surface run on clouds /
//! clusters that don't expose `/dev/infiniband` to pods (cloud VMs
//! without RDMA NICs, generic Ethernet datacenters, etc.).
//!
//! Key differences from [`crate::s3_rdma`]:
//!
//! * **4-verb protocol** (Put/Get/Delete/Head) — TCP
//!   doesn't need the ALLOC/PERSIST split that RDMA uses to expose
//!   server memory addresses. The client just streams payload
//!   bytes inline with the PUT request; the server reads them and
//!   commits in one round-trip.
//! * **No RDMA verbs, no memory registration, no QP/CQ.** Just a
//!   `TcpStream` per peer with length-prefixed frames. Multi-stream
//!   per peer is a follow-up knob.
//! * Same callback surface — `nvstor-server` picks RDMA vs TCP via
//!   a single config flag; nothing else in the wiring changes.

pub mod client;
pub mod hydrate;
pub mod proto;
pub mod server;
pub mod transport;

pub use client::S3TcpClient;
pub use hydrate::{hydrate_to_file, HydrateError, HydrateOptions, HydrateReport};
pub use proto::{
    Request, Response, MAX_GET_RANGE, OP_DELETE, OP_GET, OP_HEAD, OP_PUT, STATUS_ERROR,
    STATUS_GENERATION_CHANGED, STATUS_NOT_FOUND, STATUS_OK,
};
pub use server::S3TcpServer;
pub use transport::TcpTransport;
