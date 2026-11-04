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

//! Envelope encryption — wrap/unwrap DEKs with a master key (KEK).
//!
//! Envelope encryption separates the key used for data encryption (DEK)
//! from the key used to protect that DEK (KEK/master key). This enables:
//! - Key rotation without re-encrypting data
//! - Per-object/per-chunk unique keys
//! - Secure key distribution (only wrapped DEKs stored in metadata)

use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use rand::RngCore;

use crate::types::*;

/// Generate a new random 256-bit data encryption key.
pub fn generate_data_key() -> DataKey {
    let key_id = uuid::Uuid::new_v4().to_string();
    let mut plaintext = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut plaintext);
    DataKey { key_id, plaintext }
}

/// Wrap (encrypt) a DEK using a master key.
///
/// Returns a WrappedDataKey that can be safely persisted.
/// The plaintext DEK is encrypted with AES-256-GCM using the master key.
pub fn wrap_data_key(
    dek: &DataKey,
    master_key: &MasterKeyMaterial,
    master_key_id: &MasterKeyId,
) -> Result<WrappedDataKey, String> {
    let cipher = Aes256Gcm::new_from_slice(&master_key.key_bytes)
        .map_err(|e| format!("invalid master key: {e}"))?;

    // Random nonce for wrapping.
    let mut nonce_bytes = [0u8; 12];
    rand::thread_rng().fill_bytes(&mut nonce_bytes);
    let nonce = Nonce::from_slice(&nonce_bytes);

    let ciphertext = cipher
        .encrypt(nonce, dek.plaintext.as_slice())
        .map_err(|e| format!("wrap failed: {e}"))?;

    Ok(WrappedDataKey {
        key_id: dek.key_id.clone(),
        master_key_id: master_key_id.clone(),
        master_key_version: master_key.version,
        ciphertext,
        nonce: nonce_bytes,
        algorithm: EncryptionAlgorithm::Aes256Gcm,
    })
}

/// Unwrap (decrypt) a wrapped DEK using the master key.
///
/// Returns the plaintext DEK. The master key version must match
/// the version used during wrapping.
pub fn unwrap_data_key(
    wrapped: &WrappedDataKey,
    master_key: &MasterKeyMaterial,
) -> Result<DataKey, String> {
    if master_key.version != wrapped.master_key_version {
        return Err(format!(
            "key version mismatch: wrapped with v{}, provided v{}",
            wrapped.master_key_version, master_key.version
        ));
    }

    let cipher = Aes256Gcm::new_from_slice(&master_key.key_bytes)
        .map_err(|e| format!("invalid master key: {e}"))?;

    let nonce = Nonce::from_slice(&wrapped.nonce);
    let plaintext_bytes = cipher
        .decrypt(nonce, wrapped.ciphertext.as_slice())
        .map_err(|_| "unwrap failed — wrong master key or tampered wrapped key".to_string())?;

    if plaintext_bytes.len() != 32 {
        return Err(format!(
            "unwrapped DEK wrong size: {} bytes",
            plaintext_bytes.len()
        ));
    }

    let mut plaintext = [0u8; 32];
    plaintext.copy_from_slice(&plaintext_bytes);

    Ok(DataKey {
        key_id: wrapped.key_id.clone(),
        plaintext,
    })
}

/// Encrypt data using a DEK.
///
/// Returns (ciphertext, nonce) where ciphertext includes the GCM auth tag.
pub fn encrypt_data(dek: &DataKey, plaintext: &[u8]) -> Result<(Vec<u8>, [u8; 12]), String> {
    let cipher =
        Aes256Gcm::new_from_slice(&dek.plaintext).map_err(|e| format!("invalid DEK: {e}"))?;

    let mut nonce_bytes = [0u8; 12];
    rand::thread_rng().fill_bytes(&mut nonce_bytes);
    let nonce = Nonce::from_slice(&nonce_bytes);

    let ciphertext = cipher
        .encrypt(nonce, plaintext)
        .map_err(|e| format!("encrypt failed: {e}"))?;

    Ok((ciphertext, nonce_bytes))
}

/// Decrypt data using a DEK.
pub fn decrypt_data(dek: &DataKey, ciphertext: &[u8], nonce: &[u8; 12]) -> Result<Vec<u8>, String> {
    let cipher =
        Aes256Gcm::new_from_slice(&dek.plaintext).map_err(|e| format!("invalid DEK: {e}"))?;

    let nonce = Nonce::from_slice(nonce);
    cipher
        .decrypt(nonce, ciphertext)
        .map_err(|_| "decrypt failed — wrong key or tampered data".to_string())
}

/// Full envelope encrypt: generate DEK, encrypt data, wrap DEK.
///
/// Returns (ciphertext, EncryptionMetadata). The metadata contains
/// the wrapped DEK and nonces needed for decryption.
pub fn envelope_encrypt(
    plaintext: &[u8],
    master_key: &MasterKeyMaterial,
    master_key_id: &MasterKeyId,
) -> Result<(Vec<u8>, EncryptionMetadata), String> {
    // 1. Generate a fresh DEK.
    let dek = generate_data_key();

    // 2. Encrypt data with DEK.
    let (ciphertext, data_nonce) = encrypt_data(&dek, plaintext)?;

    // 3. Wrap DEK with master key.
    let wrapped_key = wrap_data_key(&dek, master_key, master_key_id)?;

    let metadata = EncryptionMetadata {
        wrapped_key,
        data_algorithm: EncryptionAlgorithm::Aes256Gcm,
        data_nonce,
    };

    Ok((ciphertext, metadata))
}

/// Full envelope decrypt: unwrap DEK, decrypt data.
pub fn envelope_decrypt(
    ciphertext: &[u8],
    metadata: &EncryptionMetadata,
    master_key: &MasterKeyMaterial,
) -> Result<Vec<u8>, String> {
    // 1. Unwrap DEK using master key.
    let dek = unwrap_data_key(&metadata.wrapped_key, master_key)?;

    // 2. Decrypt data with DEK.
    decrypt_data(&dek, ciphertext, &metadata.data_nonce)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_master_key(version: KeyVersion) -> MasterKeyMaterial {
        MasterKeyMaterial {
            version,
            key_bytes: [0x42; 32],
        }
    }

    #[test]
    fn generate_data_key_unique() {
        let k1 = generate_data_key();
        let k2 = generate_data_key();
        assert_ne!(k1.key_id, k2.key_id);
        assert_ne!(k1.plaintext, k2.plaintext);
    }

    #[test]
    fn wrap_unwrap_roundtrip() {
        let mk = test_master_key(1);
        let dek = generate_data_key();
        let wrapped = wrap_data_key(&dek, &mk, &"mk-001".into()).expect("wrap");
        assert_eq!(wrapped.master_key_version, 1);
        assert_eq!(wrapped.key_id, dek.key_id);

        let unwrapped = unwrap_data_key(&wrapped, &mk).expect("unwrap");
        assert_eq!(unwrapped.plaintext, dek.plaintext);
        assert_eq!(unwrapped.key_id, dek.key_id);
    }

    #[test]
    fn wrong_master_key_version_fails() {
        let mk1 = test_master_key(1);
        let mk2 = test_master_key(2);
        let dek = generate_data_key();
        let wrapped = wrap_data_key(&dek, &mk1, &"mk-001".into()).expect("wrap");
        assert!(unwrap_data_key(&wrapped, &mk2).is_err());
    }

    #[test]
    fn wrong_master_key_bytes_fails() {
        let mk1 = MasterKeyMaterial {
            version: 1,
            key_bytes: [0xAA; 32],
        };
        let mk2 = MasterKeyMaterial {
            version: 1,
            key_bytes: [0xBB; 32],
        };
        let dek = generate_data_key();
        let wrapped = wrap_data_key(&dek, &mk1, &"mk-001".into()).expect("wrap");
        assert!(unwrap_data_key(&wrapped, &mk2).is_err());
    }

    #[test]
    fn encrypt_decrypt_data_roundtrip() {
        let dek = generate_data_key();
        let data = b"checkpoint tensor data 12345";
        let (ct, nonce) = encrypt_data(&dek, data).expect("encrypt");
        assert_ne!(ct.as_slice(), data.as_slice());
        let pt = decrypt_data(&dek, &ct, &nonce).expect("decrypt");
        assert_eq!(pt, data);
    }

    #[test]
    fn tampered_ciphertext_fails() {
        let dek = generate_data_key();
        let (mut ct, nonce) = encrypt_data(&dek, b"data").expect("encrypt");
        ct[0] ^= 0xFF;
        assert!(decrypt_data(&dek, &ct, &nonce).is_err());
    }

    #[test]
    fn envelope_encrypt_decrypt_roundtrip() {
        let mk = test_master_key(1);
        let mk_id = "master-key-001".to_string();
        let data = b"model weights layer 42";

        let (ct, meta) = envelope_encrypt(data, &mk, &mk_id).expect("encrypt");
        assert_ne!(ct.as_slice(), data.as_slice());
        assert_eq!(meta.wrapped_key.master_key_id, mk_id);

        let pt = envelope_decrypt(&ct, &meta, &mk).expect("decrypt");
        assert_eq!(pt, data);
    }

    #[test]
    fn envelope_each_encrypt_unique_dek() {
        let mk = test_master_key(1);
        let mk_id = "mk-001".to_string();
        let data = b"same data";

        let (ct1, meta1) = envelope_encrypt(data, &mk, &mk_id).expect("e1");
        let (ct2, meta2) = envelope_encrypt(data, &mk, &mk_id).expect("e2");

        // Different DEKs → different ciphertexts.
        assert_ne!(meta1.wrapped_key.key_id, meta2.wrapped_key.key_id);
        assert_ne!(ct1, ct2);

        // Both decrypt to same plaintext.
        assert_eq!(envelope_decrypt(&ct1, &meta1, &mk).expect("d1"), data);
        assert_eq!(envelope_decrypt(&ct2, &meta2, &mk).expect("d2"), data);
    }

    #[test]
    fn envelope_empty_data() {
        let mk = test_master_key(1);
        let (ct, meta) = envelope_encrypt(b"", &mk, &"mk".into()).expect("enc");
        assert_eq!(envelope_decrypt(&ct, &meta, &mk).expect("dec"), b"");
    }

    #[test]
    fn envelope_large_data() {
        let mk = test_master_key(1);
        let data = vec![0xAB; 16 * 1024 * 1024]; // 16 MB
        let (ct, meta) = envelope_encrypt(&data, &mk, &"mk".into()).expect("enc");
        assert_eq!(envelope_decrypt(&ct, &meta, &mk).expect("dec"), data);
    }

    #[test]
    fn key_rotation_decrypt_with_old_version() {
        let mk_v1 = MasterKeyMaterial {
            version: 1,
            key_bytes: [0xAA; 32],
        };
        let mk_v2 = MasterKeyMaterial {
            version: 2,
            key_bytes: [0xBB; 32],
        };
        let mk_id = "rotatable-key".to_string();

        // Encrypt with v1.
        let data = b"encrypted under v1";
        let (ct, meta) = envelope_encrypt(data, &mk_v1, &mk_id).expect("enc");
        assert_eq!(meta.wrapped_key.master_key_version, 1);

        // Cannot decrypt with v2 (version mismatch).
        assert!(envelope_decrypt(&ct, &meta, &mk_v2).is_err());

        // Can still decrypt with v1.
        assert_eq!(envelope_decrypt(&ct, &meta, &mk_v1).expect("dec"), data);

        // New encryption uses v2.
        let (ct2, meta2) = envelope_encrypt(b"encrypted under v2", &mk_v2, &mk_id).expect("enc2");
        assert_eq!(meta2.wrapped_key.master_key_version, 2);
        assert_eq!(
            envelope_decrypt(&ct2, &meta2, &mk_v2).expect("dec2"),
            b"encrypted under v2"
        );
    }
}
