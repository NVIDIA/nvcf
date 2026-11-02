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

//! Encryption at rest — AES-256-GCM per chunk.
//!
//! Each chunk encrypted with a unique nonce derived from its ChunkId (SHA-256).
//! Uses the `aes-gcm` crate for NIST-standard authenticated encryption.

use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use nvstor_core::error::{NvstorError, Result};
use nvstor_core::types::ChunkId;

/// 256-bit encryption key.
pub type EncryptionKey = [u8; 32];

/// Encryption configuration.
#[derive(Clone)]
pub struct EncryptionConfig {
    cipher: Option<Aes256Gcm>,
}

impl EncryptionConfig {
    /// Disabled — passthrough, no encryption.
    pub fn disabled() -> Self {
        Self { cipher: None }
    }

    /// Enable with a 256-bit key.
    pub fn with_key(key: EncryptionKey) -> Self {
        let cipher = Aes256Gcm::new_from_slice(&key).expect("valid 32-byte key");
        Self {
            cipher: Some(cipher),
        }
    }

    /// Parse key from 64 hex characters.
    pub fn from_hex(hex: &str) -> Result<Self> {
        if hex.len() != 64 {
            return Err(NvstorError::InvalidArgument(
                "encryption key must be 64 hex chars".into(),
            ));
        }
        let mut key = [0u8; 32];
        for i in 0..32 {
            key[i] = u8::from_str_radix(&hex[i * 2..i * 2 + 2], 16)
                .map_err(|_| NvstorError::InvalidArgument("invalid hex".into()))?;
        }
        Ok(Self::with_key(key))
    }

    pub fn is_enabled(&self) -> bool {
        self.cipher.is_some()
    }

    /// Encrypt chunk data. Nonce from first 12 bytes of ChunkId.
    pub fn encrypt(&self, chunk_id: &ChunkId, plaintext: &[u8]) -> Result<Vec<u8>> {
        let cipher = match &self.cipher {
            Some(c) => c,
            None => return Ok(plaintext.to_vec()),
        };
        let nonce = Nonce::from_slice(&chunk_id.0[..12]);
        cipher
            .encrypt(nonce, plaintext)
            .map_err(|e| NvstorError::Internal(format!("encrypt: {e}")))
    }

    /// Decrypt chunk data. Input includes 16-byte GCM auth tag.
    pub fn decrypt(&self, chunk_id: &ChunkId, ciphertext: &[u8]) -> Result<Vec<u8>> {
        let cipher = match &self.cipher {
            Some(c) => c,
            None => return Ok(ciphertext.to_vec()),
        };
        let nonce = Nonce::from_slice(&chunk_id.0[..12]);
        cipher
            .decrypt(nonce, ciphertext)
            .map_err(|_| NvstorError::Corrupt {
                reason: "decryption failed — wrong key or tampered data".into(),
            })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_id() -> ChunkId {
        ChunkId::from_content(b"test data")
    }

    #[test]
    fn disabled_passthrough() {
        let c = EncryptionConfig::disabled();
        let data = b"hello";
        assert_eq!(c.encrypt(&test_id(), data).unwrap(), data);
        assert_eq!(c.decrypt(&test_id(), data).unwrap(), data);
    }

    #[test]
    fn encrypt_decrypt_roundtrip() {
        let c = EncryptionConfig::with_key([0x42; 32]);
        let id = test_id();
        let data = b"sensitive checkpoint data";
        let enc = c.encrypt(&id, data).unwrap();
        assert_ne!(enc.as_slice(), data.as_slice());
        assert_eq!(enc.len(), data.len() + 16); // GCM tag
        assert_eq!(c.decrypt(&id, &enc).unwrap(), data);
    }

    #[test]
    fn different_chunks_different_ciphertext() {
        let c = EncryptionConfig::with_key([0xAB; 32]);
        let id1 = ChunkId::from_content(b"chunk 1");
        let id2 = ChunkId::from_content(b"chunk 2");
        let enc1 = c.encrypt(&id1, b"same").unwrap();
        let enc2 = c.encrypt(&id2, b"same").unwrap();
        assert_ne!(enc1, enc2);
    }

    #[test]
    fn tampered_data_fails() {
        let c = EncryptionConfig::with_key([0xCD; 32]);
        let id = test_id();
        let mut enc = c.encrypt(&id, b"authentic").unwrap();
        enc[3] ^= 0xFF;
        assert!(c.decrypt(&id, &enc).is_err());
    }

    #[test]
    fn wrong_key_fails() {
        let c1 = EncryptionConfig::with_key([0xAA; 32]);
        let c2 = EncryptionConfig::with_key([0xBB; 32]);
        let id = test_id();
        let enc = c1.encrypt(&id, b"secret").unwrap();
        assert!(c2.decrypt(&id, &enc).is_err());
    }

    #[test]
    fn wrong_chunk_id_fails() {
        let c = EncryptionConfig::with_key([0xCC; 32]);
        let id1 = ChunkId::from_content(b"a");
        let id2 = ChunkId::from_content(b"b");
        let enc = c.encrypt(&id1, b"data").unwrap();
        assert!(c.decrypt(&id2, &enc).is_err());
    }

    #[test]
    fn from_hex_valid() {
        let hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
        let c = EncryptionConfig::from_hex(hex).unwrap();
        assert!(c.is_enabled());
        let id = test_id();
        let enc = c.encrypt(&id, b"test").unwrap();
        assert_eq!(c.decrypt(&id, &enc).unwrap(), b"test");
    }

    #[test]
    fn from_hex_invalid() {
        assert!(EncryptionConfig::from_hex("short").is_err());
    }

    #[test]
    fn empty_and_large_data() {
        let c = EncryptionConfig::with_key([0x99; 32]);
        let id = test_id();
        // Empty
        let enc = c.encrypt(&id, b"").unwrap();
        assert_eq!(c.decrypt(&id, &enc).unwrap(), b"");
        // 4MB
        let big = vec![0xAA; 4 * 1024 * 1024];
        let enc = c.encrypt(&id, &big).unwrap();
        assert_eq!(c.decrypt(&id, &enc).unwrap(), big);
    }
}
