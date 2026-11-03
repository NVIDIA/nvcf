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

//! KMS backend — pluggable key storage.
//!
//! The `KmsBackend` trait abstracts key storage. Implementations:
//! - `LocalBackend`: RocksDB-backed via MetadataStore (default)
//! - Future: HashiCorp Vault, AWS KMS, GCP Cloud KMS

use std::sync::Arc;

use nvstor_metadata::MetadataStore;
use rand::RngCore;
use tracing::debug;

use crate::types::*;

/// Pluggable backend for master key storage.
pub trait KmsBackend: Send + Sync {
    /// Create a new master key. Returns the key info (material stored internally).
    fn create_master_key(
        &self,
        key_id: &str,
        description: &str,
        scope: Option<&str>,
    ) -> Result<MasterKeyInfo, String>;

    /// Get master key info (metadata only, no key material).
    fn get_master_key_info(&self, key_id: &str) -> Result<Option<MasterKeyInfo>, String>;

    /// Get master key material for a specific version.
    fn get_key_material(
        &self,
        key_id: &str,
        version: KeyVersion,
    ) -> Result<Option<MasterKeyMaterial>, String>;

    /// Get master key material for the current (latest) version.
    fn get_current_key_material(&self, key_id: &str) -> Result<Option<MasterKeyMaterial>, String>;

    /// Rotate a master key: generate new version, old versions kept for decryption.
    fn rotate_master_key(&self, key_id: &str) -> Result<MasterKeyInfo, String>;

    /// Update key status (disable, destroy).
    fn set_key_status(&self, key_id: &str, status: KeyStatus) -> Result<(), String>;

    /// List all master keys.
    fn list_master_keys(&self) -> Result<Vec<MasterKeyInfo>, String>;

    /// Delete key material for a specific version (hard destroy).
    fn destroy_key_version(&self, key_id: &str, version: KeyVersion) -> Result<(), String>;
}

/// One atomic key mutation. Values contain root-wrapped material, never plaintext keys.
pub struct KeyMutation {
    pub check_key: String,
    pub expected: Option<Vec<u8>>,
    pub puts: Vec<(String, Vec<u8>)>,
    pub deletes: Vec<String>,
}

pub type KeyCoordinator = dyn Fn(KeyMutation) -> Result<(), String> + Send + Sync;

/// Local KMS backend — stores master keys in MetadataStore (RocksDB).
///
/// Key schema in inline CF:
///   "kms/key/{key_id}" → MasterKeyInfo (JSON)
///   "kms/material/{key_id}/v{version}" → encrypted key bytes
///
/// Key material is encrypted with a root key derived from a passphrase.
/// For development/testing, a fixed derivation is used.
pub struct LocalBackend {
    metadata: Arc<MetadataStore>,
    /// Root key for encrypting stored master key material.
    /// In production, this would come from a secure source (TPM, HSM, passphrase KDF).
    root_key: [u8; 32],
    coordinator: Option<Arc<KeyCoordinator>>,
    mutations: std::sync::Mutex<()>,
}

impl LocalBackend {
    /// Create with a root key for protecting stored master keys.
    pub fn new(metadata: Arc<MetadataStore>, root_key: [u8; 32]) -> Self {
        Self {
            metadata,
            root_key,
            coordinator: None,
            mutations: std::sync::Mutex::new(()),
        }
    }

    /// Create with a passphrase. Derives root key via SHA-256.
    /// In production, use a proper KDF (Argon2, scrypt). SHA-256 is used
    /// here because the passphrase is expected to be high-entropy (hex key).
    pub fn from_passphrase(metadata: Arc<MetadataStore>, passphrase: &str) -> Self {
        use sha2::{Digest, Sha256};
        let mut hasher = Sha256::new();
        hasher.update(passphrase.as_bytes());
        let root_key: [u8; 32] = hasher.finalize().into();
        Self::new(metadata, root_key)
    }

    fn info_key(key_id: &str) -> String {
        format!("kms/key/{key_id}")
    }

    fn material_key(key_id: &str, version: KeyVersion) -> String {
        format!("kms/material/{key_id}/v{version}")
    }

    /// Refuse to silently replace pre-existing, independently generated cluster keys.
    /// Such stores need explicit key reconciliation before joining a replicated key catalog.
    pub fn with_coordinator(mut self, coordinator: Arc<KeyCoordinator>) -> Result<Self, String> {
        for (key, encrypted) in self
            .metadata
            .scan_inline_prefix("kms/material/")
            .map_err(|e| e.to_string())?
        {
            let marker = key.replacen("kms/material/", "kms/managed/", 1);
            if self
                .metadata
                .get_inline(&marker)
                .map_err(|e| e.to_string())?
                != Some(encrypted)
            {
                return Err(
                    "legacy local KMS material requires reconciliation before cluster mode".into(),
                );
            }
        }
        self.coordinator = Some(coordinator);
        Ok(self)
    }

    fn commit(&self, mutation: KeyMutation) -> Result<(), String> {
        if let Some(coordinator) = &self.coordinator {
            coordinator(mutation)
        } else {
            if self
                .metadata
                .get_inline(&mutation.check_key)
                .map_err(|e| e.to_string())?
                != mutation.expected
            {
                return Err("concurrent key mutation; retry".into());
            }
            let puts: Vec<_> = mutation
                .puts
                .iter()
                .map(|(k, v)| (k.as_str(), v.as_slice()))
                .collect();
            let deletes: Vec<_> = mutation.deletes.iter().map(String::as_str).collect();
            self.metadata
                .batch_inline(&puts, &deletes)
                .map_err(|e| e.to_string())
        }
    }

    fn info_mutation(&self, info: &MasterKeyInfo) -> Result<KeyMutation, String> {
        let key = Self::info_key(&info.key_id);
        Ok(KeyMutation {
            expected: self.metadata.get_inline(&key).map_err(|e| e.to_string())?,
            check_key: key.clone(),
            puts: vec![(key, serde_json::to_vec(info).map_err(|e| e.to_string())?)],
            deletes: vec![],
        })
    }

    fn add_material(
        &self,
        mutation: &mut KeyMutation,
        key_id: &str,
        version: KeyVersion,
        encrypted: Vec<u8>,
    ) {
        if self.coordinator.is_some() {
            mutation.puts.push((
                format!("kms/managed/{key_id}/v{version}"),
                encrypted.clone(),
            ));
        }
        mutation
            .puts
            .push((Self::material_key(key_id, version), encrypted));
    }

    fn get_info(&self, key_id: &str) -> Result<Option<MasterKeyInfo>, String> {
        match self.metadata.get_inline(&Self::info_key(key_id)) {
            Ok(Some(bytes)) => {
                let info: MasterKeyInfo =
                    serde_json::from_slice(&bytes).map_err(|e| format!("deserialize info: {e}"))?;
                Ok(Some(info))
            }
            Ok(None) => Ok(None),
            Err(e) => Err(format!("get info: {e}")),
        }
    }

    /// Random nonces also keep losing concurrent proposals and recreated IDs distinct.
    fn encrypt_material(&self, material: &[u8; 32], key_id: &str, version: KeyVersion) -> Vec<u8> {
        use aes_gcm::aead::{Aead, KeyInit, Payload};
        use aes_gcm::{Aes256Gcm, Nonce};
        let cipher = Aes256Gcm::new_from_slice(&self.root_key).expect("32-byte root key");
        let mut nonce = [0; 12];
        rand::thread_rng().fill_bytes(&mut nonce);
        let aad = format!("{key_id}\0{version}");
        let encrypted = cipher
            .encrypt(
                Nonce::from_slice(&nonce),
                Payload {
                    msg: material,
                    aad: aad.as_bytes(),
                },
            )
            .expect("key wrapping");
        let mut result = b"BRK1".to_vec();
        result.extend_from_slice(&nonce);
        result.extend(encrypted);
        result
    }

    fn decrypt_material(
        &self,
        ciphertext: &[u8],
        key_id: &str,
        version: KeyVersion,
    ) -> Result<[u8; 32], String> {
        use aes_gcm::aead::{Aead, KeyInit, Payload};
        use aes_gcm::{Aes256Gcm, Nonce};
        use sha2::{Digest, Sha256};
        let cipher = Aes256Gcm::new_from_slice(&self.root_key).map_err(|e| e.to_string())?;
        let plaintext = if ciphertext.len() == 64 && ciphertext.starts_with(b"BRK1") {
            let aad = format!("{key_id}\0{version}");
            cipher.decrypt(
                Nonce::from_slice(&ciphertext[4..16]),
                Payload {
                    msg: &ciphertext[16..],
                    aad: aad.as_bytes(),
                },
            )
        } else if ciphertext.len() == 48 {
            // Read-only compatibility with the original wrapping format.
            let mut hasher = Sha256::new();
            hasher.update(key_id.as_bytes());
            hasher.update(version.to_le_bytes());
            let hash = hasher.finalize();
            cipher.decrypt(Nonce::from_slice(&hash[..12]), ciphertext)
        } else {
            return Err("invalid wrapped key format".into());
        }
        .map_err(|_| "failed to decrypt key material — wrong root key".to_string())?;
        plaintext
            .try_into()
            .map_err(|_| "invalid key material size".into())
    }

    fn now_unix() -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0)
    }
}

impl KmsBackend for LocalBackend {
    fn create_master_key(
        &self,
        key_id: &str,
        description: &str,
        scope: Option<&str>,
    ) -> Result<MasterKeyInfo, String> {
        let _mutation = self
            .mutations
            .lock()
            .map_err(|_| "key mutation lock poisoned")?;
        // Check if key already exists.
        if self.get_info(key_id)?.is_some() {
            return Err(format!("master key '{key_id}' already exists"));
        }

        // Generate random key material.
        let mut key_bytes = [0u8; 32];
        rand::thread_rng().fill_bytes(&mut key_bytes);

        let now = Self::now_unix();
        let info = MasterKeyInfo {
            key_id: key_id.into(),
            description: description.into(),
            current_version: 1,
            algorithm: EncryptionAlgorithm::Aes256Gcm,
            status: KeyStatus::Active,
            created_at: now,
            rotated_at: now,
            scope: scope.map(|s| s.into()),
        };

        let mut mutation = self.info_mutation(&info)?;
        mutation.expected = None;
        self.add_material(
            &mut mutation,
            key_id,
            1,
            self.encrypt_material(&key_bytes, key_id, 1),
        );
        self.commit(mutation)?;

        debug!(key_id, "created master key v1");
        Ok(info)
    }

    fn get_master_key_info(&self, key_id: &str) -> Result<Option<MasterKeyInfo>, String> {
        self.get_info(key_id)
    }

    fn get_key_material(
        &self,
        key_id: &str,
        version: KeyVersion,
    ) -> Result<Option<MasterKeyMaterial>, String> {
        let storage_key = Self::material_key(key_id, version);
        match self.metadata.get_inline(&storage_key) {
            Ok(Some(encrypted)) => {
                let key_bytes = self.decrypt_material(&encrypted, key_id, version)?;
                Ok(Some(MasterKeyMaterial { version, key_bytes }))
            }
            Ok(None) => Ok(None),
            Err(e) => Err(format!("get material: {e}")),
        }
    }

    fn get_current_key_material(&self, key_id: &str) -> Result<Option<MasterKeyMaterial>, String> {
        let info = match self.get_info(key_id)? {
            Some(i) => i,
            None => return Ok(None),
        };
        self.get_key_material(key_id, info.current_version)
    }

    fn rotate_master_key(&self, key_id: &str) -> Result<MasterKeyInfo, String> {
        let _mutation = self
            .mutations
            .lock()
            .map_err(|_| "key mutation lock poisoned")?;
        let expected = self
            .metadata
            .get_inline(&Self::info_key(key_id))
            .map_err(|e| e.to_string())?
            .ok_or_else(|| format!("master key '{key_id}' not found"))?;
        let mut info: MasterKeyInfo =
            serde_json::from_slice(&expected).map_err(|e| e.to_string())?;

        if info.status == KeyStatus::Destroyed {
            return Err(format!("cannot rotate destroyed key '{key_id}'"));
        }

        let new_version = info
            .current_version
            .checked_add(1)
            .ok_or("key version exhausted")?;

        // Generate new key material.
        let mut key_bytes = [0u8; 32];
        rand::thread_rng().fill_bytes(&mut key_bytes);

        info.current_version = new_version;
        info.rotated_at = Self::now_unix();
        let mut mutation = self.info_mutation(&info)?;
        mutation.expected = Some(expected);
        self.add_material(
            &mut mutation,
            key_id,
            new_version,
            self.encrypt_material(&key_bytes, key_id, new_version),
        );
        self.commit(mutation)?;

        debug!(key_id, version = new_version, "rotated master key");
        Ok(info)
    }

    fn set_key_status(&self, key_id: &str, status: KeyStatus) -> Result<(), String> {
        let _mutation = self
            .mutations
            .lock()
            .map_err(|_| "key mutation lock poisoned")?;
        let expected = self
            .metadata
            .get_inline(&Self::info_key(key_id))
            .map_err(|e| e.to_string())?
            .ok_or_else(|| format!("master key '{key_id}' not found"))?;
        let mut info: MasterKeyInfo =
            serde_json::from_slice(&expected).map_err(|e| e.to_string())?;

        if info.status == KeyStatus::Destroyed {
            return Err("cannot change status of destroyed key".into());
        }

        info.status = status;
        let mut mutation = self.info_mutation(&info)?;
        mutation.expected = Some(expected);
        self.commit(mutation)
    }

    fn list_master_keys(&self) -> Result<Vec<MasterKeyInfo>, String> {
        let entries = self
            .metadata
            .scan_inline_prefix("kms/key/")
            .map_err(|e| format!("scan keys: {e}"))?;

        let mut results = Vec::with_capacity(entries.len());
        for (key, bytes) in entries {
            let info: MasterKeyInfo =
                serde_json::from_slice(&bytes).map_err(|e| format!("deserialize {key}: {e}"))?;
            results.push(info);
        }
        Ok(results)
    }

    fn destroy_key_version(&self, key_id: &str, version: KeyVersion) -> Result<(), String> {
        let _mutation = self
            .mutations
            .lock()
            .map_err(|_| "key mutation lock poisoned")?;
        let storage_key = Self::material_key(key_id, version);
        self.commit(KeyMutation {
            expected: self
                .metadata
                .get_inline(&storage_key)
                .map_err(|e| e.to_string())?,
            check_key: storage_key.clone(),
            puts: vec![],
            deletes: vec![storage_key, format!("kms/managed/{key_id}/v{version}")],
        })?;
        debug!(key_id, version, "destroyed key version material");
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp_backend() -> (tempfile::TempDir, LocalBackend) {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let backend = LocalBackend::new(metadata, [0x42; 32]);
        (dir, backend)
    }

    #[test]
    fn coordinated_keys_decrypt_on_replica_and_keep_old_versions() {
        let (_a, primary) = tmp_backend();
        let (_b, replica) = tmp_backend();
        let primary_meta = primary.metadata.clone();
        let replica_meta = replica.metadata.clone();
        let coordinator = Arc::new(move |mutation: KeyMutation| {
            let puts: Vec<_> = mutation
                .puts
                .iter()
                .map(|(k, v)| (k.as_str(), v.as_slice()))
                .collect();
            let deletes: Vec<_> = mutation.deletes.iter().map(String::as_str).collect();
            for meta in [&primary_meta, &replica_meta] {
                assert_eq!(
                    meta.get_inline(&mutation.check_key).unwrap(),
                    mutation.expected
                );
                meta.batch_inline(&puts, &deletes)
                    .map_err(|e| e.to_string())?;
            }
            Ok(())
        });
        let primary = primary.with_coordinator(coordinator.clone()).unwrap();
        primary.create_master_key("shared", "test", None).unwrap();
        primary.rotate_master_key("shared").unwrap();
        for version in [1, 2] {
            assert_eq!(
                primary
                    .get_key_material("shared", version)
                    .unwrap()
                    .unwrap()
                    .key_bytes,
                replica
                    .get_key_material("shared", version)
                    .unwrap()
                    .unwrap()
                    .key_bytes
            );
        }
        let replica = replica.with_coordinator(coordinator).unwrap();
        assert_eq!(
            replica
                .get_current_key_material("shared")
                .unwrap()
                .unwrap()
                .version,
            2
        );
    }

    #[test]
    fn cluster_kms_rejects_unreconciled_local_keys_and_failed_commits() {
        let (_tmp, backend) = tmp_backend();
        backend.create_master_key("local", "test", None).unwrap();
        assert!(backend.with_coordinator(Arc::new(|_| Ok(()))).is_err());
        let (_tmp, backend) = tmp_backend();
        let backend = backend
            .with_coordinator(Arc::new(|_| Err("no quorum".into())))
            .unwrap();
        assert!(backend.create_master_key("shared", "test", None).is_err());
        assert!(backend.get_key_material("shared", 1).unwrap().is_none());
        assert!(backend.get_master_key_info("shared").unwrap().is_none());
    }

    #[test]
    fn wrapping_uses_fresh_nonce_and_authenticates_key_identity() {
        let (_tmp, backend) = tmp_backend();
        let a = backend.encrypt_material(&[7; 32], "key", 1);
        let b = backend.encrypt_material(&[7; 32], "key", 1);
        assert_ne!(a, b);
        assert_eq!(backend.decrypt_material(&a, "key", 1).unwrap(), [7; 32]);
        assert!(backend.decrypt_material(&a, "another", 1).is_err());
    }

    #[test]
    fn create_and_get_master_key() {
        let (_dir, backend) = tmp_backend();

        let info = backend
            .create_master_key("mk-001", "test key", None)
            .expect("create");
        assert_eq!(info.key_id, "mk-001");
        assert_eq!(info.current_version, 1);
        assert_eq!(info.status, KeyStatus::Active);

        let loaded = backend
            .get_master_key_info("mk-001")
            .expect("ok")
            .expect("exists");
        assert_eq!(loaded.key_id, "mk-001");
        assert_eq!(loaded.description, "test key");
    }

    #[test]
    fn create_duplicate_fails() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "first", None)
            .expect("create");
        assert!(backend.create_master_key("mk-001", "second", None).is_err());
    }

    #[test]
    fn get_key_material() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "test", None)
            .expect("create");

        let mat = backend
            .get_key_material("mk-001", 1)
            .expect("ok")
            .expect("exists");
        assert_eq!(mat.version, 1);
        // Key should be 32 random bytes (not all zeros).
        assert_ne!(mat.key_bytes, [0u8; 32]);
    }

    #[test]
    fn get_current_key_material() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "test", None)
            .expect("create");

        let mat = backend
            .get_current_key_material("mk-001")
            .expect("ok")
            .expect("exists");
        assert_eq!(mat.version, 1);
    }

    #[test]
    fn key_material_persists() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "test", None)
            .expect("create");

        let mat1 = backend
            .get_key_material("mk-001", 1)
            .expect("ok")
            .expect("exists");
        let mat2 = backend
            .get_key_material("mk-001", 1)
            .expect("ok")
            .expect("exists");
        assert_eq!(mat1.key_bytes, mat2.key_bytes);
    }

    #[test]
    fn rotate_master_key() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "test", None)
            .expect("create");

        let v1_mat = backend
            .get_key_material("mk-001", 1)
            .expect("ok")
            .expect("v1");

        let info = backend.rotate_master_key("mk-001").expect("rotate");
        assert_eq!(info.current_version, 2);

        // Old version still accessible.
        let v1_after = backend
            .get_key_material("mk-001", 1)
            .expect("ok")
            .expect("v1 still");
        assert_eq!(v1_mat.key_bytes, v1_after.key_bytes);

        // New version has different material.
        let v2_mat = backend
            .get_key_material("mk-001", 2)
            .expect("ok")
            .expect("v2");
        assert_ne!(v1_mat.key_bytes, v2_mat.key_bytes);

        // Current points to v2.
        let current = backend
            .get_current_key_material("mk-001")
            .expect("ok")
            .expect("current");
        assert_eq!(current.key_bytes, v2_mat.key_bytes);
    }

    #[test]
    fn rotate_nonexistent_fails() {
        let (_dir, backend) = tmp_backend();
        assert!(backend.rotate_master_key("nope").is_err());
    }

    #[test]
    fn set_key_status() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "test", None)
            .expect("create");

        backend
            .set_key_status("mk-001", KeyStatus::Disabled)
            .expect("disable");
        let info = backend
            .get_master_key_info("mk-001")
            .expect("ok")
            .expect("exists");
        assert_eq!(info.status, KeyStatus::Disabled);
    }

    #[test]
    fn destroy_key_version() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "test", None)
            .expect("create");
        backend.rotate_master_key("mk-001").expect("rotate");

        // Destroy v1.
        backend.destroy_key_version("mk-001", 1).expect("destroy");

        // v1 gone.
        assert!(backend.get_key_material("mk-001", 1).expect("ok").is_none());

        // v2 still there.
        assert!(backend.get_key_material("mk-001", 2).expect("ok").is_some());
    }

    #[test]
    fn scoped_key() {
        let (_dir, backend) = tmp_backend();
        let info = backend
            .create_master_key("bucket-key", "for my-bucket", Some("my-bucket"))
            .expect("create");
        assert_eq!(info.scope, Some("my-bucket".into()));
    }

    #[test]
    fn wrong_root_key_cant_read_material() {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));

        // Create with root key A.
        let backend_a = LocalBackend::new(metadata.clone(), [0xAA; 32]);
        backend_a
            .create_master_key("mk-001", "test", None)
            .expect("create");

        // Try to read with root key B.
        let backend_b = LocalBackend::new(metadata, [0xBB; 32]);
        assert!(backend_b.get_key_material("mk-001", 1).is_err());
    }

    #[test]
    fn from_passphrase() {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let backend = LocalBackend::from_passphrase(metadata, "my-secret-passphrase");
        backend
            .create_master_key("mk-001", "test", None)
            .expect("create");
        assert!(backend.get_key_material("mk-001", 1).expect("ok").is_some());
    }

    #[test]
    fn list_master_keys() {
        let (_dir, backend) = tmp_backend();
        backend
            .create_master_key("mk-001", "first key", None)
            .expect("create1");
        backend
            .create_master_key("mk-002", "second key", Some("my-bucket"))
            .expect("create2");

        let keys = backend.list_master_keys().expect("list");
        assert_eq!(keys.len(), 2);
        let ids: Vec<&str> = keys.iter().map(|k| k.key_id.as_str()).collect();
        assert!(ids.contains(&"mk-001"));
        assert!(ids.contains(&"mk-002"));
    }
}
