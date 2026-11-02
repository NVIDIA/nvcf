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

//! nvstor Raft consensus — metadata replication across cluster nodes.
//!
//! Implements the Raft protocol from scratch (no external Raft crate).
//!
//! Replicates metadata (manifests, volumes, buckets, VFS inodes).
//! Chunk data is replicated separately by the storage engine.

pub mod log;
pub mod network;
pub mod node;
pub mod rpc;
pub mod server;
pub mod state_machine;
pub mod types;
