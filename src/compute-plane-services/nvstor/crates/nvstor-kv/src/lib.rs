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

//! The nvstor KV tier.
//!
//! - [`server`]: serves KV-cache blocks from the DRAM block store
//!   (`nvstor_storage::kv_cache`) over RDMA, speaking `nvstor_rdma::kv_proto`.
//! - [`client`]: one connection to one server rail: registers a landing
//!   layout, sends signed batched GET / PUT / HEAD.
//! - [`ffi`]: the C ABI (`libnvstor_kv.so`): a client over every rail of a
//!   server with an engine-owned landing pool, for the vLLM adapter.

pub mod client;
pub mod ffi;
pub mod server;
