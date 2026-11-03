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

//! Copy a directory from one node to another over RDMA.
//!
//! The source runs [`export::DirExport`] behind the S3-over-RDMA server
//! (one transport per NIC, so every rail serves); the destination runs
//! [`fetch::fetch_dir`], which pulls the manifest and then every file's
//! ranges in parallel into a local directory. Built for moving a
//! checkpoint or model weights from the node that has them to the node
//! that needs them, without a storage tier in between. The fetcher is
//! generic over `nvstor_rdma::hydrate::RangeSource`, so other transports
//! plug in without touching it.
//!
//! Fan-out: a `serve` takes at most `--max-fetchers` fetch sessions at
//! once and tells the rest BUSY before creating any QP
//! ([`sessions::FetcherCap`]); a fetch may name several sources and moves
//! between them ([`fetch::fetch_from`], [`rdma_sources::RdmaSources`]);
//! a finished copy can serve the next fetchers under the same content
//! identity ([`content`], [`export::DirExport::open_as`]), so sources
//! multiply as a tree instead of all fetchers landing on one NIC.

pub mod bench;
pub mod content;
mod dirfd;
pub mod export;
pub mod fetch;
pub mod gpu;
pub mod manifest;
pub mod outcome;
pub mod rdma_sources;
pub mod server;
pub mod sessions;

/// Where tests make their temporary trees: `NVSTOR_TEST_TMPDIR`, else
/// `TMPDIR`, else the crate directory (a disk filesystem, so the O_DIRECT
/// paths run; but a path baked in at build time, which a cross build run
/// elsewhere does not have).
#[cfg(test)]
pub(crate) fn test_tmp() -> std::path::PathBuf {
    ["NVSTOR_TEST_TMPDIR", "TMPDIR"]
        .iter()
        .find_map(|v| std::env::var_os(v).filter(|p| !p.is_empty()))
        .map_or_else(
            || std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR")),
            std::path::PathBuf::from,
        )
}
