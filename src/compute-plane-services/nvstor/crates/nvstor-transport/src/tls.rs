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

//! TLS configuration and utilities for all nvstor transports.
//!
//! Provides TLS context creation from cert/key files.
//! Used by: S3 HTTP (axum), Raft RPC, wire protocol, NFS.

use std::fs;
use std::io;
use std::sync::Arc;

use rustls::pki_types::{CertificateDer, PrivateKeyDer};

/// TLS configuration for a server endpoint.
#[derive(Debug, Clone)]
pub struct TlsConfig {
    /// Path to PEM-encoded certificate chain file.
    pub cert_path: String,
    /// Path to PEM-encoded private key file.
    pub key_path: String,
}

/// Load a TLS server config from PEM cert + key files.
///
/// Returns a rustls ServerConfig suitable for use with tokio-rustls or axum.
pub fn load_server_config(config: &TlsConfig) -> Result<Arc<rustls::ServerConfig>, TlsError> {
    let certs = load_certs(&config.cert_path)?;
    let key = load_private_key(&config.key_path)?;

    let server_config = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(certs, key)
        .map_err(|e| TlsError::Config(e.to_string()))?;

    Ok(Arc::new(server_config))
}

/// Load PEM-encoded certificates from a file.
fn load_certs(path: &str) -> Result<Vec<CertificateDer<'static>>, TlsError> {
    let cert_file =
        fs::File::open(path).map_err(|e| TlsError::Io(format!("open cert {path}: {e}")))?;
    let mut reader = io::BufReader::new(cert_file);

    let certs: Vec<CertificateDer<'static>> = rustls_pemfile::certs(&mut reader)
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| TlsError::Io(format!("parse certs: {e}")))?;

    if certs.is_empty() {
        return Err(TlsError::Config("no certificates found in file".into()));
    }

    Ok(certs)
}

/// Load a PEM-encoded private key from a file.
fn load_private_key(path: &str) -> Result<PrivateKeyDer<'static>, TlsError> {
    let key_file =
        fs::File::open(path).map_err(|e| TlsError::Io(format!("open key {path}: {e}")))?;
    let mut reader = io::BufReader::new(key_file);

    // Try PKCS#8 first, then RSA, then EC.
    let key = rustls_pemfile::private_key(&mut reader)
        .map_err(|e| TlsError::Io(format!("parse key: {e}")))?
        .ok_or_else(|| TlsError::Config("no private key found in file".into()))?;

    Ok(key)
}

/// TLS errors.
#[derive(Debug)]
pub enum TlsError {
    Io(String),
    Config(String),
}

impl std::fmt::Display for TlsError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Io(s) => write!(f, "TLS I/O: {s}"),
            Self::Config(s) => write!(f, "TLS config: {s}"),
        }
    }
}

impl std::error::Error for TlsError {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_cert_file_errors() {
        let config = TlsConfig {
            cert_path: "/nonexistent/cert.pem".into(),
            key_path: "/nonexistent/key.pem".into(),
        };
        let result = load_server_config(&config);
        assert!(result.is_err());
    }

    #[test]
    fn tls_error_display() {
        let e = TlsError::Io("test".into());
        assert!(e.to_string().contains("test"));
        let e = TlsError::Config("bad config".into());
        assert!(e.to_string().contains("bad config"));
    }
}
