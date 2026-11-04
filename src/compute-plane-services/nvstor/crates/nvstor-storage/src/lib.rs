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

//! nvstor storage engine — content-addressed chunk storage with io_uring.
//!
//! This crate provides the core storage engine for nvstor:
//! - Content-addressed chunks (SHA-256 hash as ChunkId)
//! - Pluggable I/O backends (POSIX, io_uring)
//! - Reference counting and garbage collection
//! - Integrity verification (IEEE CRC-32 checksums)
//!
//! All data goes through the `StoreBackend` trait for persistence.
//! Metadata is stored via `nvstor-metadata` (RocksDB).

pub mod backend;
pub mod crypto;
pub mod durability;
pub mod erasure;
pub mod kv_cache;
pub mod manager;
pub mod pm;
pub mod pm_object;
pub mod scanner;

pub use durability::SyncMode;
pub use pm::{Generation, PmStore, VersionedRead};
pub use pm_object::PmObjectStore;
