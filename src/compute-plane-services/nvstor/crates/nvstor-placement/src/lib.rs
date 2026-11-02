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

//! nvstor placement engine — distributed chunk placement across nodes.
//!
//! Decides which nodes store which chunks, supports both replication
//! and erasure coding, and handles node failures with automatic repair.
//!
//! Two placement strategies:
//! - **Replication**: N identical copies on N different nodes
//! - **Erasure Coding**: k data + m parity shards across k+m nodes
//!
//! Uses consistent hashing (jump hash) for deterministic placement
//! that minimizes data movement when nodes join/leave.

pub mod discovery;
pub mod distributed;
pub mod node;
pub mod placement;
pub mod policy;
