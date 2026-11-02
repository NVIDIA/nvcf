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

//! Core types, error handling, and constants for nvstor distributed storage.
//!
//! This crate provides the foundational types shared across all nvstor components:
//! storage engine, S3 gateway, block devices, NFS server, and client libraries.

pub mod checksum;
pub mod error;
pub mod types;
pub mod wal;
pub mod xxh3;

// Re-export key types at crate root for convenience.
pub use error::{NvstorError, Result};
pub use types::*;
