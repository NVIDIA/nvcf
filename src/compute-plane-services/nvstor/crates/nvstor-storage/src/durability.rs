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

//! Durability contract for storage writes.
//!
//! Background: syncing every write regardless of what the caller's
//! protocol contract requires is far slower than the hardware allows,
//! and the protocols don't ask for it. NBD plain WRITE, NFS
//! WRITE+UNSTABLE4, and the body of an S3 streaming PUT need no sync
//! per write. This module defines the contract each write carries.
//!
//! `SyncMode::None` doesn't mean "data is lost on power loss". It
//! means "the caller will trigger a flush when their semantics
//! require durability."

/// What level of durability this write requires.
///
/// Mapped at the protocol layer:
/// - `None`: NBD plain WRITE, NFS UNSTABLE4, S3 streaming PUT bytes.
///   Returns as soon as data is queued (kernel writeback or buffer
///   pool). The caller is responsible for triggering a flush when
///   durability is needed (NBD CMD_FLUSH, NFS COMMIT, S3 PUT
///   completion).
/// - `Data`: NBD WRITE+FUA, NFS DATA_SYNC4. fdatasync before return —
///   the file's data pages are durable, but its metadata (mtime,
///   etc.) may not yet be. Cheaper than `Full` because it skips the
///   metadata journal write.
/// - `Full`: NFS FILE_SYNC4, S3 DELETE. fsync — both data and metadata
///   durable.
#[derive(Copy, Clone, Debug, PartialEq, Eq)]
pub enum SyncMode {
    /// Don't sync. Caller takes responsibility (or a later explicit
    /// flush op does).
    None,
    /// fdatasync — data is durable, file metadata may not yet be.
    Data,
    /// fsync — data + metadata durable.
    Full,
}

impl SyncMode {
    /// Whether this sync mode requires the data to be on stable
    /// storage before the call returns.
    pub fn is_durable(&self) -> bool {
        !matches!(self, SyncMode::None)
    }

    /// Whether this sync mode requires file metadata (size, mtime,
    /// etc.) to be on stable storage too.
    pub fn requires_metadata(&self) -> bool {
        matches!(self, SyncMode::Full)
    }
}

impl Default for SyncMode {
    /// The conservative default: full sync. Existing call sites that
    /// haven't been updated to be explicit-mode get the current
    /// behavior. New / hot-path callers should pick the mode the
    /// protocol actually contracts for.
    fn default() -> Self {
        SyncMode::Full
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sync_mode_defaults_full() {
        assert_eq!(SyncMode::default(), SyncMode::Full);
    }

    #[test]
    fn sync_mode_durability() {
        assert!(!SyncMode::None.is_durable());
        assert!(SyncMode::Data.is_durable());
        assert!(SyncMode::Full.is_durable());

        assert!(!SyncMode::None.requires_metadata());
        assert!(!SyncMode::Data.requires_metadata());
        assert!(SyncMode::Full.requires_metadata());
    }
}
