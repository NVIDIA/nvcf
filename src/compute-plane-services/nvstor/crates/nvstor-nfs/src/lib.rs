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

//! nvstor NFS v4.2 server.
//!
//! Provides kernel-native file access to nvstor storage via NFS v4.
//! Built from scratch per RFC 7530 (NFSv4.0), RFC 5661 (NFSv4.1),
//! and RFC 7862 (NFSv4.2).

pub mod ddp;
mod kernel_handle;
pub mod metrics;
pub mod metrics_server;
pub mod nfs4;
pub mod passthrough;
pub mod rpc;
pub mod rpcrdma;
pub mod server;
pub mod vfs;
pub mod vfs_backend;
pub mod xdr;
