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

//! Error types for nvstor storage operations.
//!
//! Covers storage, validation, permission, filesystem, WAL, and internal errors.

/// Unified error type for all nvstor operations.
#[derive(Debug, thiserror::Error)]
pub enum NvstorError {
    // -- Storage --
    /// Resource not found.
    #[error("not found")]
    NotFound,

    /// Underlying I/O error.
    #[error("I/O error: {0}")]
    Io(#[from] std::io::Error),

    /// Storage device or quota is full.
    #[error("no space left")]
    NoSpace,

    /// Stored checksum (IEEE CRC-32 or CRC-32C, per call site) did not match expected value.
    #[error("checksum mismatch: expected {expected:#010x}, got {actual:#010x}")]
    ChecksumMismatch {
        /// Expected checksum.
        expected: u32,
        /// Actual computed checksum.
        actual: u32,
    },

    /// Data corruption detected.
    #[error("corrupt: {reason}")]
    Corrupt {
        /// Human-readable corruption description.
        reason: String,
    },

    // -- Validation --
    /// Invalid argument provided by caller.
    #[error("invalid argument: {0}")]
    InvalidArgument(String),

    /// Offset or index out of bounds.
    #[error("out of bounds")]
    OutOfBounds,

    /// Invalid size parameter.
    #[error("invalid size: {0}")]
    InvalidSize(String),

    // -- Permission --
    /// POSIX permission denied.
    #[error("permission denied")]
    PermissionDenied,

    /// Access control denied (authentication/authorization).
    #[error("access denied")]
    AccessDenied,

    /// Operation not permitted (POSIX EPERM): only the owner or a
    /// privileged caller may do this, e.g. chmod or chown of another
    /// user's file.
    #[error("operation not permitted")]
    NotPermitted,

    /// Volume is locked by another writer (RWO violation).
    #[error("volume locked by another writer")]
    VolumeLocked,

    // -- Metadata --
    /// Optimistic concurrency conflict on metadata generation.
    #[error("generation mismatch: expected {expected}, got {actual}")]
    GenerationMismatch {
        /// Expected generation.
        expected: u64,
        /// Actual generation found.
        actual: u64,
    },

    /// Fencing epoch is stale (leader has changed).
    #[error("stale epoch: current {current}, received {received}")]
    EpochStale {
        /// Current epoch on the server.
        current: u64,
        /// Epoch received in the request.
        received: u64,
    },

    // -- Filesystem --
    /// Entry already exists.
    #[error("already exists")]
    Exists,

    /// Path component is not a directory.
    #[error("not a directory")]
    NotDir,

    /// Path is a directory (expected a file).
    #[error("is a directory")]
    IsDir,

    /// Directory is not empty (cannot remove).
    #[error("directory not empty")]
    NotEmpty,

    /// Filename exceeds maximum length.
    #[error("name too long")]
    NameTooLong,

    /// NFS file handle is stale.
    #[error("stale file handle")]
    StaleHandle,

    /// The object is a symbolic link where the operation needs a directory
    /// or a regular file (NFS4ERR_SYMLINK; symlinks are never followed).
    #[error("is a symbolic link")]
    Symlink,

    // -- WAL --
    /// WAL entry at the given sequence is corrupted.
    #[error("WAL corrupted at sequence {sequence}")]
    WalCorrupted {
        /// Sequence number of the corrupted entry.
        sequence: u64,
    },

    /// WAL epoch does not match expected epoch.
    #[error("WAL epoch mismatch")]
    WalEpochMismatch,

    // -- Internal --
    /// Catch-all for unexpected internal errors.
    #[error("internal error: {0}")]
    Internal(String),
}

/// Convenience alias used throughout nvstor crates.
pub type Result<T> = std::result::Result<T, NvstorError>;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn error_display_not_found() {
        assert_eq!(NvstorError::NotFound.to_string(), "not found");
    }

    #[test]
    fn error_display_checksum_mismatch() {
        let err = NvstorError::ChecksumMismatch {
            expected: 0xDEADBEEF,
            actual: 0xCAFEBABE,
        };
        assert_eq!(
            err.to_string(),
            "checksum mismatch: expected 0xdeadbeef, got 0xcafebabe"
        );
    }

    #[test]
    fn error_display_generation_mismatch() {
        let err = NvstorError::GenerationMismatch {
            expected: 5,
            actual: 3,
        };
        assert_eq!(err.to_string(), "generation mismatch: expected 5, got 3");
    }

    #[test]
    fn error_display_epoch_stale() {
        let err = NvstorError::EpochStale {
            current: 10,
            received: 7,
        };
        assert_eq!(err.to_string(), "stale epoch: current 10, received 7");
    }

    #[test]
    fn error_display_wal_corrupted() {
        let err = NvstorError::WalCorrupted { sequence: 42 };
        assert_eq!(err.to_string(), "WAL corrupted at sequence 42");
    }

    #[test]
    fn error_display_all_simple_variants() {
        // Verify every variant has a working Display impl.
        let variants: Vec<NvstorError> = vec![
            NvstorError::NotFound,
            NvstorError::NoSpace,
            NvstorError::ChecksumMismatch {
                expected: 0,
                actual: 1,
            },
            NvstorError::Corrupt {
                reason: "bad".into(),
            },
            NvstorError::InvalidArgument("x".into()),
            NvstorError::OutOfBounds,
            NvstorError::InvalidSize("0".into()),
            NvstorError::PermissionDenied,
            NvstorError::AccessDenied,
            NvstorError::VolumeLocked,
            NvstorError::GenerationMismatch {
                expected: 0,
                actual: 0,
            },
            NvstorError::EpochStale {
                current: 0,
                received: 0,
            },
            NvstorError::Exists,
            NvstorError::NotDir,
            NvstorError::IsDir,
            NvstorError::NotEmpty,
            NvstorError::NameTooLong,
            NvstorError::StaleHandle,
            NvstorError::Symlink,
            NvstorError::WalCorrupted { sequence: 0 },
            NvstorError::WalEpochMismatch,
            NvstorError::Internal("err".into()),
        ];
        for err in &variants {
            // Just verify Display doesn't panic and produces non-empty output.
            assert!(!err.to_string().is_empty());
        }
    }

    #[test]
    fn error_from_io() {
        let io_err = std::io::Error::new(std::io::ErrorKind::NotFound, "gone");
        let err: NvstorError = io_err.into();
        assert!(matches!(err, NvstorError::Io(_)));
        assert!(err.to_string().contains("gone"));
    }

    #[test]
    fn result_type_alias() {
        fn returns_ok() -> Result<u32> {
            Ok(42)
        }
        fn returns_err() -> Result<u32> {
            Err(NvstorError::NotFound)
        }
        assert_eq!(returns_ok().unwrap(), 42);
        assert!(returns_err().is_err());
    }

    // Send + Sync
    fn _assert_send<T: Send>() {}
    fn _assert_sync<T: Sync>() {}

    #[test]
    fn error_is_send_sync() {
        _assert_send::<NvstorError>();
        _assert_sync::<NvstorError>();
    }
}
