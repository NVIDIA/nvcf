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

//! KMS manager — orchestrates key management and encryption operations.
//!
//! The KmsManager is the main entry point for applications. It combines:
//! - A KmsBackend for key storage
//! - Envelope encryption for data protection
//! - S3 SSE header interpretation
//! - Key resolution (bucket-scoped → default → error)

use std::sync::Arc;

use tracing::debug;

use crate::backend::KmsBackend;
use crate::envelope;
use crate::types::*;

/// Default master key ID used for SSE-S3 encryption.
const DEFAULT_MASTER_KEY_ID: &str = "nvstor-default";

/// KMS manager — wraps a backend and provides high-level encryption operations.
pub struct KmsManager {
    backend: Arc<dyn KmsBackend>,
}

impl KmsManager {
    pub fn new(backend: Arc<dyn KmsBackend>) -> Self {
        Self { backend }
    }

    /// Ensure the default master key exists. Call once at startup.
    pub fn ensure_default_key(&self) -> Result<MasterKeyInfo, String> {
        match self.backend.get_master_key_info(DEFAULT_MASTER_KEY_ID)? {
            Some(info) => Ok(info),
            None => {
                debug!("creating default master key");
                self.backend
                    .create_master_key(
                        DEFAULT_MASTER_KEY_ID,
                        "nvstor default server-side encryption key",
                        None,
                    )
                    .or_else(|error| {
                        // Another leader request may have committed the same default ID.
                        self.backend
                            .get_master_key_info(DEFAULT_MASTER_KEY_ID)?
                            .ok_or(error)
                    })
            }
        }
    }

    /// Create a new master key (e.g., per-bucket key for SSE-KMS).
    pub fn create_key(
        &self,
        key_id: &str,
        description: &str,
        scope: Option<&str>,
    ) -> Result<MasterKeyInfo, String> {
        self.backend.create_master_key(key_id, description, scope)
    }

    /// Rotate a master key. Old versions kept for decrypting existing data.
    pub fn rotate_key(&self, key_id: &str) -> Result<MasterKeyInfo, String> {
        self.backend.rotate_master_key(key_id)
    }

    /// Get key info.
    pub fn get_key_info(&self, key_id: &str) -> Result<Option<MasterKeyInfo>, String> {
        self.backend.get_master_key_info(key_id)
    }

    /// Disable a key (can still decrypt, cannot encrypt new data).
    pub fn disable_key(&self, key_id: &str) -> Result<(), String> {
        self.backend.set_key_status(key_id, KeyStatus::Disabled)
    }

    /// Resolve which master key to use for encryption based on SSE mode.
    fn resolve_encrypt_key(
        &self,
        sse: &SseMode,
    ) -> Result<(MasterKeyId, MasterKeyMaterial), String> {
        match sse {
            SseMode::None => Err("encryption not requested".into()),
            SseMode::SseS3 => {
                self.ensure_default_key()?;
                let mat = self
                    .backend
                    .get_current_key_material(DEFAULT_MASTER_KEY_ID)?
                    .ok_or("default key material not found")?;
                Ok((DEFAULT_MASTER_KEY_ID.into(), mat))
            }
            SseMode::SseKms { key_id } => {
                let info = self
                    .backend
                    .get_master_key_info(key_id)?
                    .ok_or_else(|| format!("KMS key '{key_id}' not found"))?;
                if info.status == KeyStatus::Destroyed {
                    return Err(format!("KMS key '{key_id}' is destroyed"));
                }
                if info.status == KeyStatus::Disabled {
                    return Err(format!("KMS key '{key_id}' is disabled for encryption"));
                }
                let mat = self
                    .backend
                    .get_current_key_material(key_id)?
                    .ok_or_else(|| format!("key material for '{key_id}' not found"))?;
                Ok((key_id.clone(), mat))
            }
            SseMode::SseC => Err("SSE-C: customer must provide key — not managed by KMS".into()),
        }
    }

    /// Encrypt data using envelope encryption with the appropriate master key.
    ///
    /// Returns (ciphertext, EncryptionMetadata).
    /// The metadata should be stored alongside the encrypted data.
    pub fn encrypt(
        &self,
        data: &[u8],
        sse: &SseMode,
    ) -> Result<(Vec<u8>, EncryptionMetadata), String> {
        let (mk_id, mk_mat) = self.resolve_encrypt_key(sse)?;
        envelope::envelope_encrypt(data, &mk_mat, &mk_id)
    }

    /// Decrypt data using the encryption metadata to find the right master key.
    pub fn decrypt(
        &self,
        ciphertext: &[u8],
        metadata: &EncryptionMetadata,
    ) -> Result<Vec<u8>, String> {
        let mk_id = &metadata.wrapped_key.master_key_id;
        let mk_version = metadata.wrapped_key.master_key_version;

        let mk_mat = self
            .backend
            .get_key_material(mk_id, mk_version)?
            .ok_or_else(|| {
                format!(
                    "master key '{mk_id}' v{mk_version} not found — key may have been destroyed"
                )
            })?;

        envelope::envelope_decrypt(ciphertext, metadata, &mk_mat)
    }

    /// Encrypt with SSE-C (customer-provided key).
    ///
    /// The customer key is used directly as the master key for wrapping.
    /// Key material is NOT stored — customer must provide it on every request.
    pub fn encrypt_ssec(
        &self,
        data: &[u8],
        customer_key: &[u8; 32],
    ) -> Result<(Vec<u8>, EncryptionMetadata), String> {
        let mk_mat = MasterKeyMaterial {
            version: 0, // SSE-C has no versioning.
            key_bytes: *customer_key,
        };
        envelope::envelope_encrypt(data, &mk_mat, &"SSE-C".into())
    }

    /// Decrypt SSE-C data. Customer must provide the same key used for encryption.
    pub fn decrypt_ssec(
        &self,
        ciphertext: &[u8],
        metadata: &EncryptionMetadata,
        customer_key: &[u8; 32],
    ) -> Result<Vec<u8>, String> {
        let mk_mat = MasterKeyMaterial {
            version: metadata.wrapped_key.master_key_version,
            key_bytes: *customer_key,
        };
        envelope::envelope_decrypt(ciphertext, metadata, &mk_mat)
    }

    /// Parse S3 SSE headers into an SseMode.
    ///
    /// Examines:
    /// - x-amz-server-side-encryption: "AES256" (SSE-S3) or "aws:kms" (SSE-KMS)
    /// - x-amz-server-side-encryption-aws-kms-key-id: KMS key ID
    /// - x-amz-server-side-encryption-customer-algorithm: "AES256" (SSE-C)
    pub fn parse_sse_headers(
        encryption_header: Option<&str>,
        kms_key_id: Option<&str>,
        customer_algorithm: Option<&str>,
    ) -> SseMode {
        // SSE-C takes priority.
        if let Some(algo) = customer_algorithm {
            if algo == "AES256" {
                return SseMode::SseC;
            }
        }

        match encryption_header {
            Some("AES256") => SseMode::SseS3,
            Some("aws:kms") => {
                let key_id = kms_key_id.unwrap_or(DEFAULT_MASTER_KEY_ID).to_string();
                SseMode::SseKms { key_id }
            }
            _ => SseMode::None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::LocalBackend;
    use nvstor_metadata::MetadataStore;

    fn tmp_manager() -> (tempfile::TempDir, KmsManager) {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let backend = Arc::new(LocalBackend::new(metadata, [0x42; 32]));
        let manager = KmsManager::new(backend);
        (dir, manager)
    }

    #[test]
    fn ensure_default_key_creates_once() {
        let (_dir, mgr) = tmp_manager();

        let info1 = mgr.ensure_default_key().expect("first");
        assert_eq!(info1.key_id, DEFAULT_MASTER_KEY_ID);

        let info2 = mgr.ensure_default_key().expect("second");
        assert_eq!(info2.key_id, info1.key_id);
        assert_eq!(info2.current_version, 1); // Not re-created.
    }

    #[test]
    fn encrypt_decrypt_sse_s3() {
        let (_dir, mgr) = tmp_manager();
        let data = b"model weights for layer 42";

        let (ct, meta) = mgr.encrypt(data, &SseMode::SseS3).expect("encrypt");
        assert_ne!(ct.as_slice(), data.as_slice());
        assert_eq!(meta.wrapped_key.master_key_id, DEFAULT_MASTER_KEY_ID);

        let pt = mgr.decrypt(&ct, &meta).expect("decrypt");
        assert_eq!(pt, data);
    }

    #[test]
    fn encrypt_decrypt_sse_kms() {
        let (_dir, mgr) = tmp_manager();

        // Create a custom key.
        mgr.create_key("my-key", "test", Some("my-bucket"))
            .expect("create");

        let sse = SseMode::SseKms {
            key_id: "my-key".into(),
        };
        let data = b"sensitive training data";

        let (ct, meta) = mgr.encrypt(data, &sse).expect("encrypt");
        assert_eq!(meta.wrapped_key.master_key_id, "my-key");

        let pt = mgr.decrypt(&ct, &meta).expect("decrypt");
        assert_eq!(pt, data);
    }

    #[test]
    fn encrypt_with_nonexistent_key_fails() {
        let (_dir, mgr) = tmp_manager();
        let sse = SseMode::SseKms {
            key_id: "nope".into(),
        };
        assert!(mgr.encrypt(b"data", &sse).is_err());
    }

    #[test]
    fn encrypt_with_disabled_key_fails() {
        let (_dir, mgr) = tmp_manager();
        mgr.create_key("my-key", "test", None).expect("create");
        mgr.disable_key("my-key").expect("disable");

        let sse = SseMode::SseKms {
            key_id: "my-key".into(),
        };
        assert!(mgr.encrypt(b"data", &sse).is_err());
    }

    #[test]
    fn decrypt_after_rotation() {
        let (_dir, mgr) = tmp_manager();
        let data = b"data encrypted with v1";

        // Encrypt with default key v1.
        let (ct, meta) = mgr.encrypt(data, &SseMode::SseS3).expect("encrypt");
        assert_eq!(meta.wrapped_key.master_key_version, 1);

        // Rotate the key.
        let info = mgr.rotate_key(DEFAULT_MASTER_KEY_ID).expect("rotate");
        assert_eq!(info.current_version, 2);

        // Can still decrypt data encrypted with v1.
        let pt = mgr.decrypt(&ct, &meta).expect("decrypt after rotation");
        assert_eq!(pt, data);

        // New encryption uses v2.
        let (ct2, meta2) = mgr
            .encrypt(b"new data", &SseMode::SseS3)
            .expect("encrypt v2");
        assert_eq!(meta2.wrapped_key.master_key_version, 2);
        assert_eq!(mgr.decrypt(&ct2, &meta2).expect("dec v2"), b"new data");
    }

    #[test]
    fn ssec_encrypt_decrypt() {
        let (_dir, mgr) = tmp_manager();
        let customer_key = [0xAB; 32];
        let data = b"customer-encrypted data";

        let (ct, meta) = mgr.encrypt_ssec(data, &customer_key).expect("encrypt");
        assert_eq!(meta.wrapped_key.master_key_id, "SSE-C");

        let pt = mgr
            .decrypt_ssec(&ct, &meta, &customer_key)
            .expect("decrypt");
        assert_eq!(pt, data);
    }

    #[test]
    fn ssec_wrong_key_fails() {
        let (_dir, mgr) = tmp_manager();
        let key1 = [0xAA; 32];
        let key2 = [0xBB; 32];

        let (ct, meta) = mgr.encrypt_ssec(b"data", &key1).expect("encrypt");
        assert!(mgr.decrypt_ssec(&ct, &meta, &key2).is_err());
    }

    #[test]
    fn parse_sse_headers_s3() {
        let mode = KmsManager::parse_sse_headers(Some("AES256"), None, None);
        assert_eq!(mode, SseMode::SseS3);
    }

    #[test]
    fn parse_sse_headers_kms() {
        let mode = KmsManager::parse_sse_headers(Some("aws:kms"), Some("my-key"), None);
        assert_eq!(
            mode,
            SseMode::SseKms {
                key_id: "my-key".into()
            }
        );
    }

    #[test]
    fn parse_sse_headers_kms_default() {
        let mode = KmsManager::parse_sse_headers(Some("aws:kms"), None, None);
        assert_eq!(
            mode,
            SseMode::SseKms {
                key_id: DEFAULT_MASTER_KEY_ID.into()
            }
        );
    }

    #[test]
    fn parse_sse_headers_ssec() {
        let mode = KmsManager::parse_sse_headers(None, None, Some("AES256"));
        assert_eq!(mode, SseMode::SseC);
    }

    #[test]
    fn parse_sse_headers_none() {
        let mode = KmsManager::parse_sse_headers(None, None, None);
        assert_eq!(mode, SseMode::None);
    }

    #[test]
    fn encrypt_none_mode_errors() {
        let (_dir, mgr) = tmp_manager();
        assert!(mgr.encrypt(b"data", &SseMode::None).is_err());
    }

    #[test]
    fn large_data_envelope() {
        let (_dir, mgr) = tmp_manager();
        let data = vec![0xAB; 4 * 1024 * 1024]; // 4 MB
        let (ct, meta) = mgr.encrypt(&data, &SseMode::SseS3).expect("encrypt");
        let pt = mgr.decrypt(&ct, &meta).expect("decrypt");
        assert_eq!(pt, data);
    }

    #[test]
    fn key_persistence_across_manager_instances() {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let root_key = [0x42; 32];

        // First manager: encrypt.
        let backend1 = Arc::new(LocalBackend::new(metadata.clone(), root_key));
        let mgr1 = KmsManager::new(backend1);
        let data = b"persistent data";
        let (ct, meta) = mgr1.encrypt(data, &SseMode::SseS3).expect("encrypt");

        // Second manager: same backend, can decrypt.
        let backend2 = Arc::new(LocalBackend::new(metadata, root_key));
        let mgr2 = KmsManager::new(backend2);
        let pt = mgr2.decrypt(&ct, &meta).expect("decrypt");
        assert_eq!(pt, data);
    }
}
