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

//! TLS listener for worker callbacks.
//!
//! Serves the same router as the plain listener, so workers can reach this pod
//! over `https://` through a CONNECT tunnel and verify it end to end. The
//! certificate is re-read from disk on an interval, so a renewed Secret is
//! picked up without a restart.

use anyhow::Context as _;
use rustls::crypto::ring;
use rustls::pki_types::{pem::PemObject, CertificateDer, PrivateKeyDer};
use rustls::server::{ClientHello, ResolvesServerCert};
use rustls::sign::CertifiedKey;
use serde::{Deserialize, Serialize};
use std::{
    fmt,
    net::SocketAddr,
    path::{Path, PathBuf},
    sync::{Arc, RwLock},
    time::Duration,
};
use tokio::{
    net::{TcpSocket, TcpStream},
    sync::{mpsc, Semaphore},
    task::JoinHandle,
};
use tokio_rustls::{server::TlsStream, TlsAcceptor};

/// A handshake that has not finished by then is dropped, so slow or idle
/// clients cannot hold sockets open.
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);

/// Caps accepted connections that are still in the handshake or waiting for
/// the server to take them. Past it, new clients wait in the kernel backlog.
const MAX_PENDING_CONNECTIONS: usize = 1024;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct WorkerTlsSettings {
    pub enabled: bool,
    pub port: u16,
    /// PEM certificate chain, leaf first.
    pub cert_path: PathBuf,
    /// PEM private key for the leaf certificate.
    pub key_path: PathBuf,
    pub reload_interval_seconds: u64,
}

impl Default for WorkerTlsSettings {
    fn default() -> Self {
        Self {
            enabled: false,
            port: 8443,
            cert_path: "/etc/invocation-service/worker-tls/tls.crt".into(),
            key_path: "/etc/invocation-service/worker-tls/tls.key".into(),
            reload_interval_seconds: 30,
        }
    }
}

struct LoadedCert {
    cert_pem: Vec<u8>,
    key_pem: Vec<u8>,
    certified: Arc<CertifiedKey>,
}

/// Serves the most recent valid certificate and key from disk.
pub struct ReloadingCertResolver {
    cert_path: PathBuf,
    key_path: PathBuf,
    current: RwLock<LoadedCert>,
}

// Hand-written so the private key never reaches a log line.
impl fmt::Debug for ReloadingCertResolver {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ReloadingCertResolver")
            .field("cert_path", &self.cert_path)
            .field("key_path", &self.key_path)
            .finish_non_exhaustive()
    }
}

fn certified_key(cert_pem: &[u8], key_pem: &[u8]) -> anyhow::Result<CertifiedKey> {
    let certs = CertificateDer::pem_slice_iter(cert_pem)
        .collect::<Result<Vec<_>, _>>()
        .context("parsing certificate PEM")?;
    anyhow::ensure!(!certs.is_empty(), "no certificate in PEM");
    let key = PrivateKeyDer::from_pem_slice(key_pem).context("parsing private key PEM")?;
    let signing_key = ring::sign::any_supported_type(&key).context("unsupported private key")?;
    let certified = CertifiedKey::new(certs, signing_key);
    certified
        .keys_match()
        .context("private key does not match the certificate")?;
    Ok(certified)
}

impl ReloadingCertResolver {
    pub async fn load(cert_path: PathBuf, key_path: PathBuf) -> anyhow::Result<Self> {
        let (cert_pem, key_pem) = read_pair(&cert_path, &key_path).await?;
        let certified = Arc::new(certified_key(&cert_pem, &key_pem)?);
        Ok(Self {
            cert_path,
            key_path,
            current: RwLock::new(LoadedCert {
                cert_pem,
                key_pem,
                certified,
            }),
        })
    }

    /// Re-reads the files. Returns `Ok(true)` when a new certificate was
    /// installed. On error the current certificate stays in use.
    pub async fn reload(&self) -> anyhow::Result<bool> {
        let (cert_pem, key_pem) = read_pair(&self.cert_path, &self.key_path).await?;
        {
            let current = self.current.read().expect("worker TLS lock poisoned");
            if current.cert_pem == cert_pem && current.key_pem == key_pem {
                return Ok(false);
            }
        }
        let certified = Arc::new(certified_key(&cert_pem, &key_pem)?);
        *self.current.write().expect("worker TLS lock poisoned") = LoadedCert {
            cert_pem,
            key_pem,
            certified,
        };
        Ok(true)
    }

    fn spawn_reload(self: &Arc<Self>, interval: Duration) -> JoinHandle<()> {
        let resolver = self.clone();
        tokio::spawn(async move {
            let mut ticker = tokio::time::interval(interval);
            ticker.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
            ticker.tick().await;
            loop {
                ticker.tick().await;
                match resolver.reload().await {
                    Ok(true) => tracing::info!(
                        cert_path = %resolver.cert_path.display(),
                        "reloaded worker TLS certificate"
                    ),
                    Ok(false) => {}
                    Err(err) => tracing::warn!(
                        cert_path = %resolver.cert_path.display(),
                        "keeping the current worker TLS certificate: {err:#}"
                    ),
                }
            }
        })
    }
}

impl ResolvesServerCert for ReloadingCertResolver {
    fn resolve(&self, _client_hello: ClientHello<'_>) -> Option<Arc<CertifiedKey>> {
        Some(
            self.current
                .read()
                .expect("worker TLS lock poisoned")
                .certified
                .clone(),
        )
    }
}

async fn read_pair(cert_path: &Path, key_path: &Path) -> anyhow::Result<(Vec<u8>, Vec<u8>)> {
    let cert = tokio::fs::read(cert_path)
        .await
        .with_context(|| format!("reading {}", cert_path.display()))?;
    let key = tokio::fs::read(key_path)
        .await
        .with_context(|| format!("reading {}", key_path.display()))?;
    Ok((cert, key))
}

/// An [`axum::serve::Listener`] that yields connections after their TLS
/// handshake. Handshakes run in their own tasks, so one slow client does not
/// delay the others.
pub struct TlsListener {
    connections: mpsc::Receiver<(TlsStream<TcpStream>, SocketAddr)>,
    local_addr: SocketAddr,
    tasks: [JoinHandle<()>; 2],
}

impl TlsListener {
    pub async fn bind(
        addr: SocketAddr,
        settings: &WorkerTlsSettings,
        tcp_nodelay: bool,
    ) -> anyhow::Result<Self> {
        let resolver = Arc::new(
            ReloadingCertResolver::load(settings.cert_path.clone(), settings.key_path.clone())
                .await
                .context("loading the worker TLS certificate")?,
        );
        let mut config =
            rustls::ServerConfig::builder_with_provider(Arc::new(ring::default_provider()))
                .with_safe_default_protocol_versions()?
                .with_no_client_auth()
                .with_cert_resolver(resolver.clone());
        config.alpn_protocols = vec![b"h2".to_vec(), b"http/1.1".to_vec()];
        let acceptor = TlsAcceptor::from(Arc::new(config));

        let socket = match addr {
            SocketAddr::V4(_) => TcpSocket::new_v4()?,
            SocketAddr::V6(_) => TcpSocket::new_v6()?,
        };
        socket.set_reuseaddr(true)?;
        socket.bind(addr)?;
        let listener = socket.listen(1024)?;
        let local_addr = listener.local_addr()?;

        let (tx, connections) = mpsc::channel(MAX_PENDING_CONNECTIONS);
        let pending = Arc::new(Semaphore::new(MAX_PENDING_CONNECTIONS));
        let accept = tokio::spawn(async move {
            loop {
                let Ok(permit) = pending.clone().acquire_owned().await else {
                    return;
                };
                let (stream, peer) = match listener.accept().await {
                    Ok(conn) => conn,
                    Err(err) => {
                        tracing::warn!("worker TLS accept failed: {err}");
                        tokio::time::sleep(Duration::from_millis(50)).await;
                        continue;
                    }
                };
                if let Err(err) = stream.set_nodelay(tcp_nodelay) {
                    tracing::trace!("failed to set TCP_NODELAY on incoming connection: {err:#}");
                }
                let acceptor = acceptor.clone();
                let tx = tx.clone();
                tokio::spawn(async move {
                    let _permit = permit;
                    match tokio::time::timeout(HANDSHAKE_TIMEOUT, acceptor.accept(stream)).await {
                        Ok(Ok(tls)) => {
                            let _ = tx.send((tls, peer)).await;
                        }
                        Ok(Err(err)) => {
                            tracing::debug!(%peer, "worker TLS handshake failed: {err}")
                        }
                        Err(_) => tracing::debug!(%peer, "worker TLS handshake timed out"),
                    }
                });
            }
        });
        let reload =
            resolver.spawn_reload(Duration::from_secs(settings.reload_interval_seconds.max(1)));

        Ok(Self {
            connections,
            local_addr,
            tasks: [accept, reload],
        })
    }
}

impl Drop for TlsListener {
    fn drop(&mut self) {
        for task in &self.tasks {
            task.abort();
        }
    }
}

impl axum::serve::Listener for TlsListener {
    type Io = TlsStream<TcpStream>;
    type Addr = SocketAddr;

    async fn accept(&mut self) -> (Self::Io, Self::Addr) {
        match self.connections.recv().await {
            Some(conn) => conn,
            None => std::future::pending().await,
        }
    }

    fn local_addr(&self) -> tokio::io::Result<Self::Addr> {
        Ok(self.local_addr)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{routing::get, Router};
    use rcgen::{BasicConstraints, CertificateParams, IsCa, KeyPair};
    use rustls::pki_types::ServerName;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    /// A CA and two `localhost` leaves it signed, generated per test so no
    /// private key is committed.
    struct TestPki {
        ca: String,
        one: (String, String),
        two: (String, String),
    }

    impl TestPki {
        fn new() -> Self {
            let ca_key = KeyPair::generate().unwrap();
            let mut ca_params = CertificateParams::new(Vec::<String>::new()).unwrap();
            ca_params.is_ca = IsCa::Ca(BasicConstraints::Unconstrained);
            let ca = ca_params.self_signed(&ca_key).unwrap();
            let leaf = || {
                let key = KeyPair::generate().unwrap();
                let cert = CertificateParams::new(vec!["localhost".to_string()])
                    .unwrap()
                    .signed_by(&key, &ca, &ca_key)
                    .unwrap();
                (cert.pem(), key.serialize_pem())
            };
            Self {
                one: leaf(),
                two: leaf(),
                ca: ca.pem(),
            }
        }
    }

    fn write_pair(dir: &Path, cert: &str, key: &str) -> WorkerTlsSettings {
        std::fs::write(dir.join("tls.crt"), cert).unwrap();
        std::fs::write(dir.join("tls.key"), key).unwrap();
        WorkerTlsSettings {
            enabled: true,
            port: 0,
            cert_path: dir.join("tls.crt"),
            key_path: dir.join("tls.key"),
            reload_interval_seconds: 1,
        }
    }

    fn der(pem: &str) -> CertificateDer<'static> {
        CertificateDer::from_pem_slice(pem.as_bytes()).unwrap()
    }

    async fn serve(settings: &WorkerTlsSettings) -> SocketAddr {
        let listener = TlsListener::bind("127.0.0.1:0".parse().unwrap(), settings, true)
            .await
            .unwrap();
        let addr = listener.local_addr;
        let router = Router::new().route("/ping", get(|| async { "pong" }));
        tokio::spawn(async move { axum::serve(listener, router).await });
        addr
    }

    /// Connects trusting only `ca` and returns the leaf the server presented
    /// and the raw HTTP response to `GET /ping`.
    async fn get_ping(
        addr: SocketAddr,
        ca: &str,
    ) -> anyhow::Result<(CertificateDer<'static>, String)> {
        let mut roots = rustls::RootCertStore::empty();
        roots.add(der(ca))?;
        let config =
            rustls::ClientConfig::builder_with_provider(Arc::new(ring::default_provider()))
                .with_safe_default_protocol_versions()?
                .with_root_certificates(roots)
                .with_no_client_auth();
        let tcp = TcpStream::connect(addr).await?;
        let mut tls = tokio_rustls::TlsConnector::from(Arc::new(config))
            .connect(ServerName::try_from("localhost")?, tcp)
            .await?;
        let leaf = tls
            .get_ref()
            .1
            .peer_certificates()
            .context("no peer certificate")?[0]
            .clone();
        tls.write_all(b"GET /ping HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
            .await?;
        let mut response = String::new();
        tls.read_to_string(&mut response).await?;
        Ok((leaf, response))
    }

    #[tokio::test]
    async fn serves_the_router_over_tls() {
        let pki = TestPki::new();
        let dir = tempfile::tempdir().unwrap();
        let addr = serve(&write_pair(dir.path(), &pki.one.0, &pki.one.1)).await;

        let (leaf, response) = get_ping(addr, &pki.ca).await.unwrap();
        assert_eq!(leaf, der(&pki.one.0));
        assert!(response.starts_with("HTTP/1.1 200 OK"), "{response}");
        assert!(response.ends_with("pong"), "{response}");
    }

    #[tokio::test]
    async fn rejects_a_client_that_does_not_trust_the_ca() {
        let pki = TestPki::new();
        let other = TestPki::new();
        let dir = tempfile::tempdir().unwrap();
        let addr = serve(&write_pair(dir.path(), &pki.one.0, &pki.one.1)).await;

        let err = get_ping(addr, &other.ca).await.unwrap_err();
        assert!(
            format!("{err:#}").contains("invalid peer certificate"),
            "{err:#}"
        );
    }

    #[tokio::test]
    async fn picks_up_a_rotated_certificate_without_a_restart() {
        let pki = TestPki::new();
        let dir = tempfile::tempdir().unwrap();
        let addr = serve(&write_pair(dir.path(), &pki.one.0, &pki.one.1)).await;
        assert_eq!(get_ping(addr, &pki.ca).await.unwrap().0, der(&pki.one.0));

        write_pair(dir.path(), &pki.two.0, &pki.two.1);
        let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
        while get_ping(addr, &pki.ca).await.unwrap().0 != der(&pki.two.0) {
            assert!(
                tokio::time::Instant::now() < deadline,
                "rotated certificate was not served"
            );
            tokio::time::sleep(Duration::from_millis(200)).await;
        }
    }

    #[tokio::test]
    async fn keeps_the_current_certificate_when_the_new_pair_is_invalid() {
        let pki = TestPki::new();
        let dir = tempfile::tempdir().unwrap();
        let settings = write_pair(dir.path(), &pki.one.0, &pki.one.1);
        let resolver =
            ReloadingCertResolver::load(settings.cert_path.clone(), settings.key_path.clone())
                .await
                .unwrap();
        let served =
            |r: &ReloadingCertResolver| r.current.read().unwrap().certified.cert[0].clone();

        // New certificate, old key: a half-written rotation.
        std::fs::write(&settings.cert_path, &pki.two.0).unwrap();
        let err = resolver.reload().await.unwrap_err();
        assert!(format!("{err:#}").contains("does not match"), "{err:#}");
        assert_eq!(served(&resolver), der(&pki.one.0));

        std::fs::write(&settings.cert_path, "not a certificate").unwrap();
        assert!(resolver.reload().await.is_err());
        assert_eq!(served(&resolver), der(&pki.one.0));

        write_pair(dir.path(), &pki.two.0, &pki.two.1);
        assert!(resolver.reload().await.unwrap());
        assert_eq!(served(&resolver), der(&pki.two.0));
        assert!(
            !resolver.reload().await.unwrap(),
            "unchanged files must not reload"
        );
    }

    #[tokio::test]
    async fn refuses_to_start_with_a_mismatched_key() {
        let pki = TestPki::new();
        let dir = tempfile::tempdir().unwrap();
        let settings = write_pair(dir.path(), &pki.one.0, &pki.two.1);
        let err = TlsListener::bind("127.0.0.1:0".parse().unwrap(), &settings, true)
            .await
            .err()
            .expect("bind must fail");
        assert!(format!("{err:#}").contains("does not match"), "{err:#}");
    }

    #[test]
    fn debug_output_omits_the_key() {
        let pki = TestPki::new();
        let (cert, key) = &pki.one;
        let resolver = ReloadingCertResolver {
            cert_path: "/c".into(),
            key_path: "/k".into(),
            current: RwLock::new(LoadedCert {
                cert_pem: cert.clone().into_bytes(),
                key_pem: key.clone().into_bytes(),
                certified: Arc::new(certified_key(cert.as_bytes(), key.as_bytes()).unwrap()),
            }),
        };
        assert!(!format!("{resolver:?}").contains("PRIVATE KEY"));
    }

    #[test]
    fn settings_default_to_disabled_on_8443() {
        let settings: WorkerTlsSettings = serde_json::from_str(r#"{"enabled": true}"#).unwrap();
        assert!(settings.enabled);
        assert_eq!(settings.port, 8443);
        assert!(!WorkerTlsSettings::default().enabled);
    }
}
