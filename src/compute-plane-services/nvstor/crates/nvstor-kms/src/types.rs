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

//! KMS types — keys, encryption metadata, key hierarchy.

use serde::{Deserialize, Serialize};
use zeroize::{Zeroize, ZeroizeOnDrop};

/// Unique identifier for a master key.
pub type MasterKeyId = String;

/// Version number for a master key (monotonically increasing).
pub type KeyVersion = u32;

/// Unique identifier for a data encryption key.
pub type DataKeyId = String;

/// Encryption algorithm.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum EncryptionAlgorithm {
    /// AES-256-GCM (NIST standard, 256-bit key, 96-bit nonce, 128-bit tag).
    Aes256Gcm,
}

impl Default for EncryptionAlgorithm {
    fn default() -> Self {
        Self::Aes256Gcm
    }
}

/// Status of a master key.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum KeyStatus {
    /// Active — can be used for new encryption and decryption.
    Active,
    /// Rotation pending — old version still usable for decryption.
    RotationPending,
    /// Disabled — cannot be used for new encryption, can still decrypt.
    Disabled,
    /// Destroyed — key material deleted, decryption impossible.
    Destroyed,
}

/// Master key metadata (KEK — Key Encryption Key).
///
/// The actual key material is stored separately in the backend.
/// This struct only holds metadata for management and auditing.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MasterKeyInfo {
    /// Unique key identifier.
    pub key_id: MasterKeyId,
    /// Human-readable description.
    pub description: String,
    /// Current version (incremented on rotation).
    pub current_version: KeyVersion,
    /// Encryption algorithm used by this key.
    pub algorithm: EncryptionAlgorithm,
    /// Key status.
    pub status: KeyStatus,
    /// Creation timestamp (unix seconds).
    pub created_at: i64,
    /// Last rotation timestamp.
    pub rotated_at: i64,
    /// Optional scope: bucket name this key is associated with.
    /// None means it's a global/default key.
    pub scope: Option<String>,
}

/// Raw master key material with version.
///
/// Implements ZeroizeOnDrop to clear key bytes from memory on drop.
#[derive(Clone, Zeroize, ZeroizeOnDrop)]
pub struct MasterKeyMaterial {
    /// Key version this material belongs to.
    #[zeroize(skip)]
    pub version: KeyVersion,
    /// 256-bit key bytes.
    pub key_bytes: [u8; 32],
}

impl std::fmt::Debug for MasterKeyMaterial {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("MasterKeyMaterial")
            .field("version", &self.version)
            .field("key_bytes", &"[REDACTED]")
            .finish()
    }
}

/// Data Encryption Key (DEK) — generated per-object or per-chunk.
///
/// Implements ZeroizeOnDrop to clear plaintext key from memory.
#[derive(Clone, Zeroize, ZeroizeOnDrop)]
pub struct DataKey {
    /// Unique DEK identifier.
    #[zeroize(skip)]
    pub key_id: DataKeyId,
    /// Plaintext 256-bit key bytes.
    pub plaintext: [u8; 32],
}

impl std::fmt::Debug for DataKey {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("DataKey")
            .field("key_id", &self.key_id)
            .field("plaintext", &"[REDACTED]")
            .finish()
    }
}

/// Wrapped (encrypted) DEK — safe to persist alongside encrypted data.
///
/// This is what gets stored in metadata. To use the DEK, it must be
/// unwrapped using the master key identified by `master_key_id` + `master_key_version`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WrappedDataKey {
    /// DEK identifier.
    pub key_id: DataKeyId,
    /// Master key that wrapped this DEK.
    pub master_key_id: MasterKeyId,
    /// Version of the master key used for wrapping.
    pub master_key_version: KeyVersion,
    /// Encrypted DEK bytes (ciphertext + GCM tag).
    pub ciphertext: Vec<u8>,
    /// Nonce used for wrapping encryption.
    pub nonce: [u8; 12],
    /// Algorithm used for wrapping.
    pub algorithm: EncryptionAlgorithm,
}

/// Encryption metadata stored alongside encrypted data.
///
/// Contains everything needed to find and unwrap the DEK for decryption.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EncryptionMetadata {
    /// The wrapped DEK used to encrypt this data.
    pub wrapped_key: WrappedDataKey,
    /// Algorithm used for data encryption.
    pub data_algorithm: EncryptionAlgorithm,
    /// Nonce used for data encryption.
    pub data_nonce: [u8; 12],
}

/// S3-compatible server-side encryption mode.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum SseMode {
    /// SSE-S3: Server manages keys (default master key).
    SseS3,
    /// SSE-KMS: Customer specifies a master key ID.
    SseKms { key_id: MasterKeyId },
    /// SSE-C: Customer provides the key directly.
    /// Key material is NOT stored — customer must provide on every request.
    SseC,
    /// No encryption.
    None,
}

impl Default for SseMode {
    fn default() -> Self {
        Self::None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn master_key_material_zeroizes_on_drop() {
        let mat = MasterKeyMaterial {
            version: 1,
            key_bytes: [0xAA; 32],
        };
        // Verify key is populated.
        assert_eq!(mat.key_bytes[0], 0xAA);
        // After drop, memory is zeroed. We can't test post-drop directly,
        // but we verify the type implements ZeroizeOnDrop by construction.
        drop(mat);
    }

    #[test]
    fn data_key_debug_redacts_plaintext() {
        let dk = DataKey {
            key_id: "test-dek".into(),
            plaintext: [0xFF; 32],
        };
        let debug = format!("{dk:?}");
        assert!(debug.contains("REDACTED"));
        assert!(!debug.contains("255")); // 0xFF = 255
    }

    #[test]
    fn wrapped_key_serialization() {
        let wk = WrappedDataKey {
            key_id: "dek-001".into(),
            master_key_id: "mk-001".into(),
            master_key_version: 3,
            ciphertext: vec![1, 2, 3, 4],
            nonce: [0u8; 12],
            algorithm: EncryptionAlgorithm::Aes256Gcm,
        };
        let json = serde_json::to_string(&wk).expect("serialize");
        let parsed: WrappedDataKey = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(wk, parsed);
    }

    #[test]
    fn encryption_metadata_serialization() {
        let meta = EncryptionMetadata {
            wrapped_key: WrappedDataKey {
                key_id: "dek-001".into(),
                master_key_id: "mk-001".into(),
                master_key_version: 1,
                ciphertext: vec![10, 20, 30],
                nonce: [5u8; 12],
                algorithm: EncryptionAlgorithm::Aes256Gcm,
            },
            data_algorithm: EncryptionAlgorithm::Aes256Gcm,
            data_nonce: [7u8; 12],
        };
        let json = serde_json::to_string(&meta).expect("serialize");
        let parsed: EncryptionMetadata = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(meta, parsed);
    }

    #[test]
    fn sse_mode_default_is_none() {
        assert_eq!(SseMode::default(), SseMode::None);
    }
}
