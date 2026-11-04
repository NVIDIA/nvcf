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

//! Native Rust client library for nvstor distributed storage.
//!
//! Provides a high-level async API for object and volume operations over
//! nvstor's length-prefixed bincode wire protocol.
//!
//! # Example
//!
//! ```no_run
//! use nvstor_client::NvstorClient;
//!
//! # async fn example() -> Result<(), nvstor_client::ClientError> {
//! let client = NvstorClient::connect("127.0.0.1:9002").await?;
//! client.ping().await?;
//!
//! // Object API
//! let put = client.put("my/key", b"data", "application/octet-stream").await?;
//! let get = client.get("my/key").await?;
//! assert_eq!(get.data, b"data");
//!
//! // Volume API
//! let id = uuid::Uuid::new_v4();
//! client.volume_create(id, "vol-0", 1024 * 1024 * 1024).await?;
//! client.volume_write(id, 0, b"block data").await?;
//! let block = client.volume_read(id, 0, 4096).await?;
//! # Ok(())
//! # }
//! ```

pub mod client;
pub mod error;
pub mod protocol;

pub use client::{ClientConfig, GetResult, HeadResult, ListResult, NvstorClient, PutResult};
pub use error::ClientError;
pub use protocol::{ObjectEntry, Request, Response};
