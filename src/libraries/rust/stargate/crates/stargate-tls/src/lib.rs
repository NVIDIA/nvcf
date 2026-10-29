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

use std::borrow::Cow;
use std::fmt;
use std::fs;
use std::io::Read;
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, anyhow, bail, ensure};
use quinn::{ClientConfig, ServerConfig};
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{DigitallySignedStruct, Error, SignatureScheme};

const MAX_TLS_MATERIAL_BYTES: u64 = 1024 * 1024;

/// Default interval between mounted TLS material polls.
///
/// Certificate renewal happens hours ahead of expiry and kubelet projects a
/// Secret update on a minute-granular sync period, so a bounded poll detects a
/// rotation well inside the window where the outgoing identity is still valid.
pub const DEFAULT_TLS_RELOAD_INTERVAL: Duration = Duration::from_secs(30);

/// `material_type` label value for the mounted server certificate and key.
///
/// The label exists so the metric contract does not change when client trust
/// reload lands and adds a second value.
pub const SERVER_IDENTITY_MATERIAL: &str = "server_identity";

/// Result of attempting to activate changed TLS material.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum TlsReloadOutcome {
    Success,
    Rejected,
}

impl TlsReloadOutcome {
    pub const ALL: [Self; 2] = [Self::Success, Self::Rejected];

    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Success => "success",
            Self::Rejected => "rejected",
        }
    }
}

/// Generates a PEM self-signed certificate and key with SANs for `localhost` and `stargate`.
pub fn generate_self_signed_cert() -> Result<(Vec<u8>, Vec<u8>)> {
    generate_self_signed_cert_for_names(vec!["localhost".to_string(), "stargate".to_string()])
}

/// Generates a self-signed certificate and private key for the supplied DNS names.
pub fn generate_self_signed_cert_for_names(names: Vec<String>) -> Result<(Vec<u8>, Vec<u8>)> {
    let cert = rcgen::generate_simple_self_signed(names)
        .context("failed to generate self-signed certificate")?;
    let cert_pem = cert.cert.pem().into_bytes();
    let key_pem = cert.key_pair.serialize_pem().into_bytes();
    Ok((cert_pem, key_pem))
}

pub type ServerTlsPemPair<'a> = (Cow<'a, [u8]>, Cow<'a, [u8]>);

/// Prefers IPv4 dial addresses, preserving each family's resolver order and IPv6 fallback.
pub fn ordered_dial_candidates(
    resolved_addrs: impl IntoIterator<Item = SocketAddr>,
) -> Vec<SocketAddr> {
    let (mut ipv4, ipv6): (Vec<_>, Vec<_>) =
        resolved_addrs.into_iter().partition(SocketAddr::is_ipv4);
    ipv4.extend(ipv6);
    ipv4
}

/// Returns an ephemeral unspecified local address compatible with `remote_addr`.
pub fn quic_client_bind_addr(remote_addr: SocketAddr) -> SocketAddr {
    match remote_addr {
        SocketAddr::V4(_) => SocketAddr::new(Ipv4Addr::UNSPECIFIED.into(), 0),
        SocketAddr::V6(_) => SocketAddr::new(Ipv6Addr::UNSPECIFIED.into(), 0),
    }
}

/// TLS identity used by QUIC tunnel servers.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum ServerTlsIdentity {
    #[default]
    SelfSigned,
    Provided {
        cert_pem: Vec<u8>,
        key_pem: Vec<u8>,
    },
}

/// Validity window for the leaf certificate in a provided server identity.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CertificateValidity {
    pub not_before_unix_seconds: i64,
    pub not_after_unix_seconds: i64,
}

impl ServerTlsIdentity {
    /// Builds a server identity from optional certificate/key PEM inputs.
    pub fn from_optional_pem(cert_pem: Option<Vec<u8>>, key_pem: Option<Vec<u8>>) -> Result<Self> {
        match (cert_pem, key_pem) {
            (Some(cert_pem), Some(key_pem)) => Ok(Self::Provided { cert_pem, key_pem }),
            (None, None) => Ok(Self::SelfSigned),
            (Some(_), None) => bail!("TLS key PEM is required when TLS cert PEM is provided"),
            (None, Some(_)) => bail!("TLS cert PEM is required when TLS key PEM is provided"),
        }
    }

    /// Returns the PEM pair to parse when building a server config.
    pub fn pem_pair(&self) -> Result<ServerTlsPemPair<'_>> {
        match self {
            Self::SelfSigned => generate_self_signed_cert()
                .map(|(cert_pem, key_pem)| (Cow::Owned(cert_pem), Cow::Owned(key_pem))),
            Self::Provided { cert_pem, key_pem } => Ok((cert_pem.into(), key_pem.into())),
        }
    }
}

/// A server identity that passed validation, with the window it was accepted under.
///
/// Deliberately crate-private. Splitting load from commit outside this module
/// would let a caller record a candidate the endpoint never activated, which is
/// the one ordering that breaks last-known-good retention.
#[derive(Clone, Debug, PartialEq, Eq)]
struct ValidatedServerIdentity {
    identity: ServerTlsIdentity,
    validity: Option<CertificateValidity>,
}

/// Reloads a complete certificate and private-key pair while retaining the last valid identity.
#[derive(Clone)]
pub struct ServerIdentityReloader {
    cert_path: PathBuf,
    key_path: PathBuf,
    current: ValidatedServerIdentity,
    last_rejection: Option<String>,
}

impl fmt::Debug for ServerIdentityReloader {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("ServerIdentityReloader")
            .field("cert_path", &self.cert_path)
            .field("key_path", &self.key_path)
            .field("has_suppressed_rejection", &self.last_rejection.is_some())
            .finish()
    }
}

impl ServerIdentityReloader {
    /// Loads and validates the initial identity from `cert_path` and `key_path`.
    pub fn load(cert_path: PathBuf, key_path: PathBuf) -> Result<Self> {
        let current = read_server_identity(&cert_path, &key_path)?;
        Ok(Self {
            cert_path,
            key_path,
            current,
            last_rejection: None,
        })
    }

    /// Returns the active last-known-good identity.
    pub fn current_identity(&self) -> &ServerTlsIdentity {
        &self.current.identity
    }

    /// Returns the validity window of the active identity.
    pub fn current_validity(&self) -> Option<CertificateValidity> {
        self.current.validity
    }

    /// Loads a changed, validated identity without changing active state.
    ///
    /// Kubernetes swaps the projected `..data` symlink atomically, so a reader
    /// normally sees one complete generation. A torn or partial read fails
    /// certificate and key validation, which keeps the active identity in place
    /// and leaves the next poll to retry. An identical
    /// repeated failure reports `Ok(None)` so a stuck generation cannot flood the
    /// log or the rejection counter during an event storm.
    fn load_candidate(&mut self) -> Result<Option<ValidatedServerIdentity>> {
        match read_server_identity(&self.cert_path, &self.key_path) {
            Ok(candidate) => {
                self.last_rejection = None;
                Ok((candidate != self.current).then_some(candidate))
            }
            Err(error) => {
                let rejection = format!("{error:#}");
                if self.last_rejection.as_deref() == Some(rejection.as_str()) {
                    return Ok(None);
                }
                self.last_rejection = Some(rejection);
                Err(error)
            }
        }
    }

    /// Records a candidate after the connection owner activates it.
    fn commit(&mut self, candidate: ValidatedServerIdentity) {
        self.current = candidate;
    }

    /// Installs a valid changed identity for new QUIC handshakes.
    ///
    /// `build` must produce the same server configuration the endpoint was
    /// created with, including ALPN and transport settings. Anything it omits is
    /// dropped for every connection accepted after the reload.
    ///
    /// `quinn::Endpoint::set_server_config` applies to subsequent handshakes and
    /// leaves established connections alone, so no connection is disturbed by an
    /// ordinary leaf renewal.
    pub fn reload_quic_server_config_if_changed(
        &mut self,
        endpoint: &quinn::Endpoint,
        build: impl Fn(&ServerTlsIdentity) -> Result<ServerConfig>,
    ) -> Result<bool> {
        let Some(candidate) = self.load_candidate()? else {
            return Ok(false);
        };
        let server_config = build(&candidate.identity)?;
        endpoint.set_server_config(Some(server_config));
        self.commit(candidate);
        Ok(true)
    }
}

/// Rebuilds a QUIC server configuration from a validated server identity.
pub type BuildServerConfig = Box<dyn Fn(&ServerTlsIdentity) -> Result<ServerConfig> + Send>;

/// Internal marker for a component that serves a generated self-signed identity.
///
/// Never published. A metric value of `i64::MAX` scrapes as a float that no
/// dashboard can subtract from `time()`, so consumers read
/// `TlsIdentityStatus::active_expiry_unix_seconds` and publish nothing when it
/// returns `None`.
const NO_SERVER_IDENTITY_EXPIRY: i64 = i64::MAX;

/// Single source of truth for the active server identity expiry.
///
/// The reload task publishes here, the expiry gauge reads from here, and
/// readiness reads from here, so the three cannot disagree.
#[derive(Debug)]
pub struct TlsIdentityStatus {
    not_after_unix_seconds: std::sync::atomic::AtomicI64,
}

impl Default for TlsIdentityStatus {
    fn default() -> Self {
        Self {
            not_after_unix_seconds: std::sync::atomic::AtomicI64::new(NO_SERVER_IDENTITY_EXPIRY),
        }
    }
}

impl TlsIdentityStatus {
    /// Creates a status that reports ready until a provided identity is published.
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }

    /// Publishes the validity window of the active identity.
    pub fn set_validity(&self, validity: Option<CertificateValidity>) {
        self.not_after_unix_seconds.store(
            validity.map_or(NO_SERVER_IDENTITY_EXPIRY, |validity| {
                validity.not_after_unix_seconds
            }),
            std::sync::atomic::Ordering::Release,
        );
    }

    /// Returns the active certificate expiry, or `None` for a generated identity.
    ///
    /// A component with no mounted identity has no expiry to report, so its
    /// consumers publish no expiry series rather than a placeholder value.
    pub fn active_expiry_unix_seconds(&self) -> Option<i64> {
        let not_after = self
            .not_after_unix_seconds
            .load(std::sync::atomic::Ordering::Acquire);
        (not_after != NO_SERVER_IDENTITY_EXPIRY).then_some(not_after)
    }

    /// Reports whether the active identity is still inside its validity window.
    pub fn is_ready(&self) -> bool {
        match self.active_expiry_unix_seconds() {
            None => true,
            Some(not_after) => unix_now_seconds().is_ok_and(|now| now <= not_after),
        }
    }
}

/// Reloads the mounted server identity for the lifetime of one QUIC endpoint.
///
/// The endpoint is cloned by the caller. `quinn::Endpoint` is reference counted,
/// so the task needs no lock and never touches the accept or dispatch path.
pub struct ServerIdentityReloadTask {
    /// Bounded component name attached to the reload logs.
    ///
    /// This is a log field only. Reload metrics are labelled by material type
    /// and result, so adding a component here would not change their cardinality.
    pub component: &'static str,
    /// Reloader owning the last-known-good identity.
    pub reloader: ServerIdentityReloader,
    /// Endpoint whose server config is replaced for new handshakes.
    pub endpoint: quinn::Endpoint,
    /// Rebuilds the endpoint server config from a validated identity.
    ///
    /// This must reproduce the ALPN and transport settings the endpoint was
    /// created with. Anything it omits is dropped for connections accepted after
    /// the reload.
    pub build_server_config: BuildServerConfig,
    /// Interval between polls of the mounted material.
    pub poll_interval: Duration,
    /// Expiry state shared with the expiry gauge and readiness.
    pub status: Arc<TlsIdentityStatus>,
}

impl ServerIdentityReloadTask {
    /// Runs until `shutdown` is cancelled.
    pub async fn run<Observe>(
        self,
        shutdown: tokio_util::sync::CancellationToken,
        mut observe: Observe,
    ) -> Result<()>
    where
        Observe: FnMut(TlsReloadOutcome),
    {
        let Self {
            component,
            mut reloader,
            endpoint,
            build_server_config,
            poll_interval,
            status,
        } = self;
        ensure!(
            !poll_interval.is_zero(),
            "TLS poll interval must be positive"
        );
        let mut poll = tokio::time::interval(poll_interval);
        poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        // The first tick completes immediately, and the initial identity is
        // already active, so consume it rather than reloading on entry.
        poll.tick().await;
        status.set_validity(reloader.current_validity());
        loop {
            tokio::select! {
                _ = shutdown.cancelled() => return Ok(()),
                _ = poll.tick() => {
                    match reloader
                        .reload_quic_server_config_if_changed(&endpoint, &build_server_config)
                    {
                        Ok(true) => {
                            status.set_validity(reloader.current_validity());
                            observe(TlsReloadOutcome::Success);
                            tracing::info!(
                                component,
                                material_type = SERVER_IDENTITY_MATERIAL,
                                result = TlsReloadOutcome::Success.as_str(),
                                "TLS material reloaded"
                            );
                        }
                        Ok(false) => {}
                        Err(error) => {
                            observe(TlsReloadOutcome::Rejected);
                            tracing::warn!(
                                component,
                                material_type = SERVER_IDENTITY_MATERIAL,
                                result = TlsReloadOutcome::Rejected.as_str(),
                                %error,
                                "TLS material reload rejected; retaining last-known-good configuration"
                            );
                        }
                    }
                }
            }
        }
    }
}

fn unix_now_seconds() -> Result<i64> {
    Ok(SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .context("system time is before the Unix epoch")?
        .as_secs() as i64)
}

fn read_server_identity(cert_path: &Path, key_path: &Path) -> Result<ValidatedServerIdentity> {
    // Read through the configured paths. A projected volume resolves both
    // through `..data` into one generation, and a read that straddles a swap
    // fails the certificate and key validation below, so no path-resolution
    // comparison is needed to keep the last-known-good identity.
    let cert_pem = read_bounded_file(cert_path, "TLS certificate")?;
    let key_pem = read_bounded_file(key_path, "TLS private key")?;
    let identity = ServerTlsIdentity::from_optional_pem(Some(cert_pem), Some(key_pem))?;
    // rustls rejects a certificate and private key that do not match, so a torn
    // read across two generations fails here and the caller retains its active
    // identity until a complete generation is visible.
    build_quic_server_config(&identity, Vec::new()).context("invalid TLS server identity")?;
    validate_server_identity_chain(&identity)?;
    let validity = server_identity_effective_validity(&identity)?;
    validate_certificate_validity(validity)?;
    Ok(ValidatedServerIdentity { identity, validity })
}

/// Verifies the supplied server certificate path using its final certificate
/// as the deployment-provided trust anchor.
fn validate_server_identity_chain(identity: &ServerTlsIdentity) -> Result<()> {
    let ServerTlsIdentity::Provided { cert_pem, .. } = identity else {
        return Ok(());
    };
    let certificates = rustls_pemfile::certs(&mut &**cert_pem)
        .collect::<std::result::Result<Vec<_>, _>>()
        .context("failed to parse TLS certificate chain PEM")?;
    ensure!(!certificates.is_empty(), "no certificate found in TLS PEM");

    // A single-certificate identity has no supplied issuer path to validate.
    // rustls has already verified its key match, and the validity-window check
    // in `validate_certificate_validity` still applies, which preserves support
    // for an explicitly trusted self-signed or leaf-only identity.
    if certificates.len() == 1 {
        return Ok(());
    }

    let trust_anchor =
        webpki::anchor_from_trusted_cert(certificates.last().context("missing TLS trust anchor")?)
            .context("failed to parse TLS chain trust anchor")?;
    let end_entity = webpki::EndEntityCert::try_from(&certificates[0])
        .context("failed to parse TLS leaf certificate")?;
    let provider = rustls::crypto::aws_lc_rs::default_provider();
    end_entity
        .verify_for_usage(
            provider.signature_verification_algorithms.all,
            &[trust_anchor],
            &certificates[1..certificates.len() - 1],
            UnixTime::now(),
            webpki::KeyUsage::server_auth(),
            None,
            None,
        )
        .context("TLS server certificate chain validation failed")?;
    Ok(())
}

/// Returns the validity window shared by every certificate in the served chain.
fn server_identity_effective_validity(
    identity: &ServerTlsIdentity,
) -> Result<Option<CertificateValidity>> {
    let ServerTlsIdentity::Provided { cert_pem, .. } = identity else {
        return Ok(None);
    };
    let mut effective: Option<CertificateValidity> = None;
    for certificate in rustls_pemfile::certs(&mut &**cert_pem) {
        let certificate = certificate.context("failed to parse TLS certificate chain PEM")?;
        let validity = parse_certificate_validity(certificate.as_ref())?;
        effective = Some(match effective {
            Some(current) => CertificateValidity {
                not_before_unix_seconds: current
                    .not_before_unix_seconds
                    .max(validity.not_before_unix_seconds),
                not_after_unix_seconds: current
                    .not_after_unix_seconds
                    .min(validity.not_after_unix_seconds),
            },
            None => validity,
        });
    }
    effective
        .context("no certificate found in TLS PEM")
        .map(Some)
}

/// Rejects a validity window that does not contain the current time.
///
/// The window is the intersection across the served chain, so this rejects an
/// expired or not-yet-valid leaf, intermediate, or supplied root alike.
fn validate_certificate_validity(validity: Option<CertificateValidity>) -> Result<()> {
    let Some(validity) = validity else {
        return Ok(());
    };
    let now = unix_now_seconds()?;
    ensure!(
        now >= validity.not_before_unix_seconds,
        "TLS server certificate chain is not yet valid"
    );
    ensure!(
        now <= validity.not_after_unix_seconds,
        "TLS server certificate chain is expired"
    );
    Ok(())
}

fn parse_certificate_validity(cert_der: &[u8]) -> Result<CertificateValidity> {
    let (_, certificate) = x509_parser::parse_x509_certificate(cert_der)
        .map_err(|error| anyhow!("failed to parse X.509 certificate: {error}"))?;
    let validity = certificate.validity();
    let not_before_unix_seconds = validity.not_before.timestamp();
    let not_after_unix_seconds = validity.not_after.timestamp();
    ensure!(
        not_before_unix_seconds <= not_after_unix_seconds,
        "TLS certificate has an invalid validity window"
    );
    Ok(CertificateValidity {
        not_before_unix_seconds,
        not_after_unix_seconds,
    })
}

fn read_bounded_file(path: &Path, material: &str) -> Result<Vec<u8>> {
    let file = fs::File::open(path)
        .with_context(|| format!("failed to open {material} file {}", path.display()))?;
    let mut contents = Vec::new();
    file.take(MAX_TLS_MATERIAL_BYTES + 1)
        .read_to_end(&mut contents)
        .with_context(|| format!("failed to read {material} file {}", path.display()))?;
    ensure!(
        contents.len() as u64 <= MAX_TLS_MATERIAL_BYTES,
        "{material} exceeds {MAX_TLS_MATERIAL_BYTES} bytes"
    );
    Ok(contents)
}

/// Builds a QUIC client config that skips server certificate verification.
pub fn build_insecure_quic_client_config() -> Result<ClientConfig> {
    build_insecure_quic_client_config_with_alpn(Vec::new())
}

/// Builds a QUIC client config that skips verification and advertises the supplied ALPN list.
pub fn build_insecure_quic_client_config_with_alpn(
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ClientConfig> {
    let mut tls_config = rustls::ClientConfig::builder()
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(InsecureServerCertVerifier))
        .with_no_client_auth();
    tls_config.alpn_protocols = alpn_protocols;
    quic_client_config(tls_config)
}

/// Selects verified or insecure QUIC client TLS from the supplied trust policy.
pub fn build_quic_client_config(
    cert_pem: Option<&[u8]>,
    insecure: bool,
    alpn_protocols: Vec<Vec<u8>>,
    missing_trust_error: &'static str,
) -> Result<ClientConfig> {
    if insecure {
        return build_insecure_quic_client_config_with_alpn(alpn_protocols);
    }
    build_trusted_quic_client_config_with_alpn(
        cert_pem.context(missing_trust_error)?,
        alpn_protocols,
    )
}

/// Builds a QUIC client config using the supplied PEM trust anchor and ALPN list.
pub fn build_trusted_quic_client_config_with_alpn(
    cert_pem: &[u8],
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ClientConfig> {
    let mut roots = rustls::RootCertStore::empty();
    for cert in rustls_pemfile::certs(&mut &*cert_pem) {
        roots
            .add(cert.context("failed to parse cert PEM")?)
            .context("failed to add cert to root store")?;
    }
    ensure!(
        !roots.is_empty(),
        "TLS trust bundle contains no certificates"
    );
    let mut tls_config = rustls::ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();
    tls_config.alpn_protocols = alpn_protocols;
    quic_client_config(tls_config)
}

/// Builds a QUIC server config from one identity and ALPN policy.
pub fn build_quic_server_config(
    identity: &ServerTlsIdentity,
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ServerConfig> {
    let (cert_pem, key_pem) = identity.pem_pair()?;
    build_quic_server_config_from_pem(&cert_pem, &key_pem, alpn_protocols)
}

/// Builds a QUIC server config from PEM-encoded identity material and ALPN policy.
pub fn build_quic_server_config_from_pem(
    cert_pem: &[u8],
    key_pem: &[u8],
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ServerConfig> {
    let cert_chain = rustls_pemfile::certs(&mut &*cert_pem)
        .collect::<std::result::Result<Vec<_>, _>>()
        .context("failed to parse TLS certificate PEM")?;
    ensure!(!cert_chain.is_empty(), "no certificate found in TLS PEM");
    let key = rustls_pemfile::private_key(&mut &*key_pem)
        .context("failed to parse TLS private key PEM")?
        .context("no private key found in TLS PEM")?;
    let mut tls = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(cert_chain, key)
        .context("failed to build TLS server config")?;
    tls.alpn_protocols = alpn_protocols;
    Ok(ServerConfig::with_crypto(Arc::new(
        quinn::crypto::rustls::QuicServerConfig::try_from(tls)
            .context("failed to build QUIC server config")?,
    )))
}

fn quic_client_config(tls: rustls::ClientConfig) -> Result<ClientConfig> {
    Ok(ClientConfig::new(Arc::new(
        quinn::crypto::rustls::QuicClientConfig::try_from(tls)?,
    )))
}

#[derive(Debug)]
struct InsecureServerCertVerifier;

impl ServerCertVerifier for InsecureServerCertVerifier {
    fn verify_server_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        _now: UnixTime,
    ) -> std::result::Result<ServerCertVerified, Error> {
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn verify_tls13_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        rustls::crypto::aws_lc_rs::default_provider()
            .signature_verification_algorithms
            .supported_schemes()
    }
}

#[cfg(test)]
mod tests {
    use std::fs;
    use std::net::SocketAddr;
    use std::path::Path;

    use tempfile::TempDir;

    use super::*;

    fn test_dir() -> TempDir {
        TempDir::new().expect("create TLS test directory")
    }

    #[cfg(unix)]
    fn install_projected_generation(
        root: &Path,
        generation: &str,
        cert_pem: &[u8],
        key_pem: &[u8],
    ) {
        use std::os::unix::fs::symlink;

        let generation_dir = root.join(generation);
        fs::create_dir(&generation_dir).expect("create projected generation");
        fs::write(generation_dir.join("tls.crt"), cert_pem).expect("write projected cert");
        fs::write(generation_dir.join("tls.key"), key_pem).expect("write projected key");

        let next_data = root.join("..data-next");
        let _ = fs::remove_file(&next_data);
        symlink(generation, &next_data).expect("create projected data symlink");
        fs::rename(next_data, root.join("..data")).expect("swap projected data symlink");

        if !root.join("tls.crt").exists() {
            symlink("..data/tls.crt", root.join("tls.crt")).expect("create projected cert symlink");
            symlink("..data/tls.key", root.join("tls.key")).expect("create projected key symlink");
        }
    }

    #[cfg(unix)]
    #[test]
    fn server_identity_reloader_follows_atomic_projected_volume_update() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let (first_cert, first_key) =
            generate_self_signed_cert_for_names(vec!["first.example.test".to_string()]).unwrap();
        let (second_cert, second_key) =
            generate_self_signed_cert_for_names(vec!["second.example.test".to_string()]).unwrap();
        install_projected_generation(root.path(), "..2026_01", &first_cert, &first_key);

        let mut reloader =
            ServerIdentityReloader::load(root.path().join("tls.crt"), root.path().join("tls.key"))
                .expect("load initial identity");
        assert_eq!(
            reloader.current_identity(),
            &ServerTlsIdentity::Provided {
                cert_pem: first_cert,
                key_pem: first_key,
            }
        );

        install_projected_generation(root.path(), "..2026_02", &second_cert, &second_key);

        let replacement = reloader
            .load_candidate()
            .expect("load projected identity")
            .expect("replacement should be detected");
        assert_eq!(
            &replacement.identity,
            &ServerTlsIdentity::Provided {
                cert_pem: second_cert.clone(),
                key_pem: second_key.clone(),
            }
        );
        reloader.commit(replacement);
        assert_eq!(
            reloader.current_identity(),
            &ServerTlsIdentity::Provided {
                cert_pem: second_cert,
                key_pem: second_key,
            }
        );
    }

    async fn connect_with_trust(server: &quinn::Endpoint, trusted_cert_pem: &[u8]) -> Result<()> {
        let mut client = quinn::Endpoint::client("127.0.0.1:0".parse().unwrap())?;
        client.set_default_client_config(build_trusted_quic_client_config_with_alpn(
            trusted_cert_pem,
            Vec::new(),
        )?);
        let connecting = client.connect(server.local_addr()?, "localhost")?;
        let incoming = server.accept().await.context("server endpoint closed")?;
        let (client_connection, server_connection) = tokio::join!(connecting, incoming);
        let client_connection = client_connection?;
        let server_connection = server_connection?;
        client_connection.close(0u32.into(), b"test complete");
        server_connection.close(0u32.into(), b"test complete");
        Ok(())
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn server_identity_reloader_updates_new_quic_handshakes() -> Result<()> {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let (first_cert, first_key) = generate_self_signed_cert().unwrap();
        let (second_cert, second_key) = generate_self_signed_cert().unwrap();
        install_projected_generation(root.path(), "..2026_01", &first_cert, &first_key);
        let mut reloader =
            ServerIdentityReloader::load(root.path().join("tls.crt"), root.path().join("tls.key"))
                .unwrap();
        let initial_config = build_quic_server_config(reloader.current_identity(), Vec::new())?;
        let server = quinn::Endpoint::server(initial_config, "127.0.0.1:0".parse().unwrap())?;

        connect_with_trust(&server, &first_cert).await?;
        install_projected_generation(root.path(), "..2026_02", &second_cert, &second_key);

        assert!(
            reloader.reload_quic_server_config_if_changed(&server, |identity| {
                build_quic_server_config(identity, Vec::new())
            })?
        );
        connect_with_trust(&server, &second_cert).await?;
        assert!(connect_with_trust(&server, &first_cert).await.is_err());
        Ok(())
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn server_identity_poll_rejects_invalid_then_activates_valid_generation() -> Result<()> {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let (first_cert, first_key) = generate_self_signed_cert().unwrap();
        let (second_cert, second_key) = generate_self_signed_cert().unwrap();
        install_projected_generation(root.path(), "..2026_01", &first_cert, &first_key);
        let mut reloader =
            ServerIdentityReloader::load(root.path().join("tls.crt"), root.path().join("tls.key"))?;
        let initial_config = build_quic_server_config(reloader.current_identity(), Vec::new())?;
        let server = quinn::Endpoint::server(initial_config, "127.0.0.1:0".parse().unwrap())?;

        install_projected_generation(root.path(), "..2026_bad", &second_cert, &first_key);
        assert!(reloader.load_candidate().is_err());
        connect_with_trust(&server, &first_cert).await?;
        assert!(connect_with_trust(&server, &second_cert).await.is_err());

        install_projected_generation(root.path(), "..2026_02", &second_cert, &second_key);
        let replacement = reloader
            .load_candidate()?
            .context("valid replacement was not loaded")?;
        server.set_server_config(Some(build_quic_server_config(
            &replacement.identity,
            Vec::new(),
        )?));
        reloader.commit(replacement);
        connect_with_trust(&server, &second_cert).await?;
        assert!(connect_with_trust(&server, &first_cert).await.is_err());
        Ok(())
    }

    #[test]
    fn self_signed_cert_produces_nonempty_pem() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (cert_pem, key_pem) = generate_self_signed_cert().unwrap();
        assert!(!cert_pem.is_empty());
        assert!(!key_pem.is_empty());
    }

    #[test]
    fn self_signed_cert_for_names_produces_nonempty_pem() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (cert_pem, key_pem) =
            generate_self_signed_cert_for_names(vec!["sg-b.stargate.external".to_string()])
                .unwrap();
        assert!(!cert_pem.is_empty());
        assert!(!key_pem.is_empty());
    }

    fn cert_with_validity(
        not_before: (i32, u8, u8),
        not_after: (i32, u8, u8),
    ) -> (Vec<u8>, Vec<u8>) {
        let mut params = rcgen::CertificateParams::new(vec!["localhost".to_string()]).unwrap();
        params.not_before = rcgen::date_time_ymd(not_before.0, not_before.1, not_before.2);
        params.not_after = rcgen::date_time_ymd(not_after.0, not_after.1, not_after.2);
        let key = rcgen::KeyPair::generate().unwrap();
        let cert = params.self_signed(&key).unwrap();
        (cert.pem().into_bytes(), key.serialize_pem().into_bytes())
    }

    #[test]
    fn server_identity_reloader_rejects_expired_and_not_yet_valid_certificates() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let cert_path = root.path().join("tls.crt");
        let key_path = root.path().join("tls.key");

        let (expired_cert, expired_key) = cert_with_validity((2020, 1, 1), (2021, 1, 1));
        fs::write(&cert_path, expired_cert).unwrap();
        fs::write(&key_path, expired_key).unwrap();
        assert!(ServerIdentityReloader::load(cert_path.clone(), key_path.clone()).is_err());

        let (future_cert, future_key) = cert_with_validity((2035, 1, 1), (2036, 1, 1));
        fs::write(&cert_path, future_cert).unwrap();
        fs::write(&key_path, future_key).unwrap();
        assert!(ServerIdentityReloader::load(cert_path, key_path).is_err());
    }

    #[test]
    fn expired_leaf_only_certificate_from_an_external_issuer_is_rejected() {
        // A leaf with no supplied issuer gives webpki no path to build, so the
        // validity-window check is the only thing that rejects this generation.
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let cert_path = root.path().join("tls.crt");
        let key_path = root.path().join("tls.key");

        let mut issuer_params = rcgen::CertificateParams::default();
        issuer_params.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
        let issuer_key = rcgen::KeyPair::generate().unwrap();
        let issuer = issuer_params.self_signed(&issuer_key).unwrap();

        let mut leaf_params = rcgen::CertificateParams::new(vec!["localhost".to_string()]).unwrap();
        leaf_params.not_before = rcgen::date_time_ymd(2020, 1, 1);
        leaf_params.not_after = rcgen::date_time_ymd(2021, 1, 1);
        let leaf_key = rcgen::KeyPair::generate().unwrap();
        let leaf = leaf_params
            .signed_by(&leaf_key, &issuer, &issuer_key)
            .unwrap();

        fs::write(&cert_path, leaf.pem().into_bytes()).unwrap();
        fs::write(&key_path, leaf_key.serialize_pem().into_bytes()).unwrap();

        let error = ServerIdentityReloader::load(cert_path, key_path)
            .expect_err("an expired leaf-only identity must be rejected");
        assert!(
            format!("{error:#}").contains("expired"),
            "unexpected rejection reason: {error:#}"
        );
    }

    #[test]
    fn tls_identity_status_reports_ready_until_the_active_identity_expires() {
        let status = TlsIdentityStatus::new();
        assert!(status.is_ready(), "a generated identity is always ready");
        assert_eq!(
            status.active_expiry_unix_seconds(),
            None,
            "a generated identity publishes no expiry"
        );

        let now = unix_now_seconds().unwrap();
        status.set_validity(Some(CertificateValidity {
            not_before_unix_seconds: now - 60,
            not_after_unix_seconds: now + 3_600,
        }));
        assert!(status.is_ready());
        assert_eq!(status.active_expiry_unix_seconds(), Some(now + 3_600));

        status.set_validity(Some(CertificateValidity {
            not_before_unix_seconds: now - 3_600,
            not_after_unix_seconds: now - 60,
        }));
        assert!(
            !status.is_ready(),
            "an expired active identity is not ready"
        );

        status.set_validity(None);
        assert!(status.is_ready());
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn run_server_identity_reload_activates_a_projected_replacement() -> Result<()> {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let (first_cert, first_key) = generate_self_signed_cert().unwrap();
        let (second_cert, second_key) = generate_self_signed_cert().unwrap();
        install_projected_generation(root.path(), "..2026_01", &first_cert, &first_key);

        let reloader =
            ServerIdentityReloader::load(root.path().join("tls.crt"), root.path().join("tls.key"))?;
        let server = quinn::Endpoint::server(
            build_quic_server_config(reloader.current_identity(), Vec::new())?,
            "127.0.0.1:0".parse().unwrap(),
        )?;
        let status = TlsIdentityStatus::new();
        let shutdown = tokio_util::sync::CancellationToken::new();
        let (outcomes_tx, mut outcomes) = tokio::sync::mpsc::unbounded_channel();
        let reload = ServerIdentityReloadTask {
            component: "test",
            reloader,
            endpoint: server.clone(),
            build_server_config: Box::new(|identity| {
                build_quic_server_config(identity, Vec::new())
            }),
            poll_interval: Duration::from_millis(20),
            status: status.clone(),
        };
        let task = tokio::spawn(reload.run(shutdown.clone(), move |outcome| {
            let _ = outcomes_tx.send(outcome);
        }));

        connect_with_trust(&server, &first_cert).await?;
        install_projected_generation(root.path(), "..2026_02", &second_cert, &second_key);

        let outcome = tokio::time::timeout(Duration::from_secs(5), outcomes.recv())
            .await
            .context("reload task did not report an outcome")?
            .context("reload task dropped the outcome channel")?;
        assert_eq!(outcome, TlsReloadOutcome::Success);

        connect_with_trust(&server, &second_cert).await?;
        assert!(connect_with_trust(&server, &first_cert).await.is_err());
        assert!(status.is_ready());
        assert!(status.active_expiry_unix_seconds().is_some());

        shutdown.cancel();
        let joined = tokio::time::timeout(Duration::from_secs(5), task)
            .await
            .context("reload task did not stop on shutdown")?;
        joined.context("reload task panicked")??;
        Ok(())
    }

    #[test]
    fn server_identity_reloader_rejects_expired_intermediate_certificate() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let cert_path = root.path().join("tls.crt");
        let key_path = root.path().join("tls.key");

        let mut issuer_params = rcgen::CertificateParams::default();
        issuer_params.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
        issuer_params.not_before = rcgen::date_time_ymd(2020, 1, 1);
        issuer_params.not_after = rcgen::date_time_ymd(2021, 1, 1);
        let issuer_key = rcgen::KeyPair::generate().unwrap();
        let issuer = issuer_params.self_signed(&issuer_key).unwrap();

        let leaf_params = rcgen::CertificateParams::new(vec!["localhost".to_string()]).unwrap();
        let leaf_key = rcgen::KeyPair::generate().unwrap();
        let leaf = leaf_params
            .signed_by(&leaf_key, &issuer, &issuer_key)
            .unwrap();
        let chain_pem = format!("{}{}", leaf.pem(), issuer.pem()).into_bytes();
        let key_pem = leaf_key.serialize_pem().into_bytes();
        fs::write(&cert_path, chain_pem).unwrap();
        fs::write(&key_path, key_pem).unwrap();

        assert!(ServerIdentityReloader::load(cert_path, key_path).is_err());
    }

    #[tokio::test]
    async fn server_identity_reloader_rejects_wrong_intermediate_before_handshake_activation() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let cert_path = root.path().join("tls.crt");
        let key_path = root.path().join("tls.key");
        let (initial_cert, initial_key) = generate_self_signed_cert().unwrap();
        fs::write(&cert_path, &initial_cert).unwrap();
        fs::write(&key_path, &initial_key).unwrap();
        let mut reloader =
            ServerIdentityReloader::load(cert_path.clone(), key_path.clone()).unwrap();
        let server_config =
            build_quic_server_config(reloader.current_identity(), Vec::new()).unwrap();
        let server =
            quinn::Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap()).unwrap();

        let mut signing_issuer_params = rcgen::CertificateParams::default();
        signing_issuer_params.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
        let signing_issuer_key = rcgen::KeyPair::generate().unwrap();
        let signing_issuer = signing_issuer_params
            .self_signed(&signing_issuer_key)
            .unwrap();

        let mut wrong_issuer_params = rcgen::CertificateParams::default();
        wrong_issuer_params.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
        let wrong_issuer_key = rcgen::KeyPair::generate().unwrap();
        let wrong_issuer = wrong_issuer_params.self_signed(&wrong_issuer_key).unwrap();

        let leaf_params = rcgen::CertificateParams::new(vec!["localhost".to_string()]).unwrap();
        let leaf_key = rcgen::KeyPair::generate().unwrap();
        let leaf = leaf_params
            .signed_by(&leaf_key, &signing_issuer, &signing_issuer_key)
            .unwrap();
        let wrong_issuer_pem = wrong_issuer.pem();
        fs::write(&cert_path, format!("{}{}", leaf.pem(), wrong_issuer_pem)).unwrap();
        fs::write(&key_path, leaf_key.serialize_pem()).unwrap();

        assert!(
            reloader
                .reload_quic_server_config_if_changed(&server, |identity| {
                    build_quic_server_config(identity, Vec::new())
                })
                .is_err()
        );
        connect_with_trust(&server, &initial_cert).await.unwrap();
        assert!(
            connect_with_trust(&server, wrong_issuer_pem.as_bytes())
                .await
                .is_err()
        );
    }

    #[test]
    fn reloader_rejects_oversized_material_before_activation() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let oversized = vec![b'x'; MAX_TLS_MATERIAL_BYTES as usize + 1];
        let cert_path = root.path().join("tls.crt");
        let key_path = root.path().join("tls.key");
        fs::write(&cert_path, oversized).unwrap();
        fs::write(&key_path, b"key").unwrap();
        assert!(ServerIdentityReloader::load(cert_path, key_path).is_err());
    }

    #[test]
    fn repeated_rejection_is_reported_once_until_the_failure_changes() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let root = test_dir();
        let cert_path = root.path().join("tls.crt");
        let key_path = root.path().join("tls.key");
        let (initial_cert, initial_key) = generate_self_signed_cert().unwrap();
        fs::write(&cert_path, &initial_cert).unwrap();
        fs::write(&key_path, &initial_key).unwrap();
        let mut reloader =
            ServerIdentityReloader::load(cert_path.clone(), key_path.clone()).unwrap();

        fs::write(&cert_path, b"invalid certificate generation").unwrap();
        assert!(reloader.load_candidate().is_err());
        assert!(
            reloader.load_candidate().unwrap().is_none(),
            "an unchanged failure must not be reported twice"
        );

        let (replacement_cert, replacement_key) = generate_self_signed_cert().unwrap();
        fs::write(&cert_path, &replacement_cert).unwrap();
        fs::write(&key_path, &replacement_key).unwrap();
        let candidate = reloader
            .load_candidate()
            .unwrap()
            .expect("changed valid generation should be retried");
        reloader.commit(candidate);
        assert_eq!(
            reloader.current_identity(),
            &ServerTlsIdentity::Provided {
                cert_pem: replacement_cert,
                key_pem: replacement_key,
            }
        );
    }

    #[test]
    fn insecure_client_config_succeeds() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let _config = build_insecure_quic_client_config().unwrap();
    }

    #[test]
    fn insecure_client_config_with_alpn_succeeds() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let _config = build_insecure_quic_client_config_with_alpn(vec![b"h3".to_vec()]).unwrap();
    }

    #[test]
    fn trusted_client_config_with_alpn_accepts_pem_root() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (cert_pem, _) = generate_self_signed_cert().unwrap();
        let _config =
            build_trusted_quic_client_config_with_alpn(&cert_pem, vec![b"h3".to_vec()]).unwrap();
    }

    #[test]
    fn trusted_client_config_rejects_empty_trust_bundle() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        assert!(build_trusted_quic_client_config_with_alpn(b"", Vec::new()).is_err());
    }

    #[test]
    fn server_tls_identity_requires_complete_pem_pair() {
        let cert_pem = b"cert".to_vec();
        let key_pem = b"key".to_vec();

        assert!(matches!(
            ServerTlsIdentity::from_optional_pem(None, None).unwrap(),
            ServerTlsIdentity::SelfSigned
        ));
        assert_eq!(
            ServerTlsIdentity::from_optional_pem(Some(cert_pem.clone()), Some(key_pem.clone()))
                .unwrap(),
            ServerTlsIdentity::Provided {
                cert_pem: cert_pem.clone(),
                key_pem: key_pem.clone(),
            }
        );
        assert!(ServerTlsIdentity::from_optional_pem(Some(cert_pem), None).is_err());
        assert!(ServerTlsIdentity::from_optional_pem(None, Some(key_pem)).is_err());
    }

    #[test]
    fn ordered_dial_candidates_prioritize_ipv4_without_discarding_ipv6() {
        let ipv6: SocketAddr = "[fd00::1]:50072"
            .parse()
            .expect("IPv6 address should parse");
        let ipv4: SocketAddr = "10.0.0.4:50072".parse().expect("IPv4 address should parse");

        assert_eq!(ordered_dial_candidates([ipv6, ipv4]), vec![ipv4, ipv6]);
        assert_eq!(quic_client_bind_addr(ipv4), "0.0.0.0:0".parse().unwrap());
        assert_eq!(quic_client_bind_addr(ipv6), "[::]:0".parse().unwrap());
    }
}
