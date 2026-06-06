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

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result};
use quinn::{ClientConfig, Endpoint};
use rustls::RootCertStore;
use stargate_forwarding::{
    HostnameMatcher, PeerTarget, RelayEndpointConfig, RelayEndpoints, build_relay_endpoints,
    build_relay_transport_config, forward_quic_connection,
};
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use crate::endpoints::TargetSnapshot;
use crate::metrics::RouterMetrics;

#[derive(Clone, Debug)]
pub struct QuicRouterConfig {
    pub listen_addr: SocketAddr,
    pub advertised_hostname_template: String,
    pub target_namespace: String,
    pub connect_timeout: Duration,
    pub relay_max_idle_timeout: Duration,
    pub relay_keep_alive_interval: Option<Duration>,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub tls_key_pem: Option<Vec<u8>>,
    pub quic_insecure: bool,
}

#[derive(Clone, Debug)]
struct QuicRouterRuntimeConfig {
    config: QuicRouterConfig,
    hostname_matcher: Option<HostnameMatcher>,
}

impl QuicRouterRuntimeConfig {
    fn new(config: QuicRouterConfig) -> Self {
        let hostname_matcher = HostnameMatcher::new(
            &config.advertised_hostname_template,
            &config.target_namespace,
        );
        Self {
            config,
            hostname_matcher,
        }
    }
}

pub async fn serve_quic_router(
    config: QuicRouterConfig,
    targets: watch::Receiver<TargetSnapshot>,
    metrics: Arc<RouterMetrics>,
    shutdown: CancellationToken,
) -> Result<()> {
    let relay_config = RelayEndpointConfig {
        max_idle_timeout: config.relay_max_idle_timeout,
        keep_alive_interval: config.relay_keep_alive_interval,
    };
    let server_config = build_server_config(
        config.tls_cert_pem.as_deref(),
        config.tls_key_pem.as_deref(),
        relay_config,
    )?;
    let endpoint = Endpoint::server(server_config, config.listen_addr)?;
    let bound_addr = endpoint.local_addr()?;
    let client_config = build_client_config(config.tls_cert_pem.as_deref(), config.quic_insecure)?;
    let relay_endpoints = Arc::new(build_relay_endpoints(relay_config, client_config)?);
    let config = Arc::new(QuicRouterRuntimeConfig::new(config));
    info!(
        addr = %bound_addr,
        relay_max_idle_timeout_ms = relay_config.max_idle_timeout.as_millis(),
        relay_keep_alive_interval_ms =
            relay_config.keep_alive_interval.map(|duration| duration.as_millis()),
        "QUIC router listening"
    );

    loop {
        tokio::select! {
            _ = shutdown.cancelled() => {
                endpoint.close(0u32.into(), b"shutdown");
                return Ok(());
            }
            incoming = endpoint.accept() => {
                let Some(incoming) = incoming else {
                    warn!("QUIC router endpoint stopped accepting");
                    return Ok(());
                };
                let targets = targets.clone();
                let relay_endpoints = relay_endpoints.clone();
                let metrics = metrics.clone();
                let config = config.clone();
                tokio::spawn(async move {
                    if let Err(error) = dispatch_incoming(incoming, targets, relay_endpoints, metrics, config).await {
                        warn!(%error, "QUIC router connection failed");
                    }
                });
            }
        }
    }
}

async fn dispatch_incoming(
    incoming: quinn::Incoming,
    targets: watch::Receiver<TargetSnapshot>,
    relay_endpoints: Arc<RelayEndpoints>,
    metrics: Arc<RouterMetrics>,
    config: Arc<QuicRouterRuntimeConfig>,
) -> Result<()> {
    let connection = incoming.await.context("accept QUIC connection")?;
    let sni = connection
        .handshake_data()
        .and_then(|data| data.downcast::<quinn::crypto::rustls::HandshakeData>().ok())
        .and_then(|hd| hd.server_name);

    let route = {
        let snapshot = targets.borrow();
        match route_for_sni(sni.as_deref(), &snapshot, &config) {
            QuicRoute::Ready {
                target,
                server_name,
            } => QuicDispatchRoute::Ready {
                target_pod: target.pod_name.clone(),
                peer: PeerTarget {
                    dial_addr: target.quic_addr.clone(),
                    server_name: server_name.to_string(),
                },
            },
            QuicRoute::MissingSni => QuicDispatchRoute::MissingSni,
            QuicRoute::UnknownSni => QuicDispatchRoute::UnknownSni,
            QuicRoute::TargetUnavailable => QuicDispatchRoute::TargetUnavailable,
        }
    };
    let peer = match route {
        QuicDispatchRoute::Ready { target_pod, peer } => {
            metrics.observe_quic_connection("accepted");
            info!(
                target_pod = %target_pod,
                peer = %peer.dial_addr,
                server_name = %peer.server_name,
                "relaying QUIC connection to stargate target"
            );
            peer
        }
        QuicDispatchRoute::MissingSni => {
            metrics.observe_quic_connection("missing_sni");
            connection.close(0u32.into(), b"missing target SNI");
            return Ok(());
        }
        QuicDispatchRoute::UnknownSni => {
            metrics.observe_quic_connection("unknown_sni");
            connection.close(0u32.into(), b"unknown target SNI");
            return Ok(());
        }
        QuicDispatchRoute::TargetUnavailable => {
            metrics.observe_quic_connection("target_unavailable");
            connection.close(0u32.into(), b"target stargate not ready");
            return Ok(());
        }
    };
    match forward_quic_connection(
        connection,
        &peer,
        &relay_endpoints,
        config.config.connect_timeout,
    )
    .await
    {
        Ok(()) => {
            metrics.observe_quic_connection("completed");
            Ok(())
        }
        Err(error) => {
            metrics.observe_quic_connection("relay_error");
            Err(error)
        }
    }
}

enum QuicDispatchRoute {
    Ready {
        target_pod: String,
        peer: PeerTarget,
    },
    MissingSni,
    UnknownSni,
    TargetUnavailable,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum QuicRoute<'a> {
    Ready {
        target: &'a crate::endpoints::PodTarget,
        server_name: &'a str,
    },
    MissingSni,
    UnknownSni,
    TargetUnavailable,
}

fn route_for_sni<'a>(
    sni: Option<&'a str>,
    targets: &'a TargetSnapshot,
    config: &QuicRouterRuntimeConfig,
) -> QuicRoute<'a> {
    let Some(sni) = sni else {
        return QuicRoute::MissingSni;
    };
    let Some(pod_name) = config
        .hostname_matcher
        .as_ref()
        .and_then(|matcher| matcher.extract_pod(sni))
    else {
        return QuicRoute::UnknownSni;
    };
    let Some(target) = targets.target_for_pod_ref(pod_name) else {
        return QuicRoute::TargetUnavailable;
    };

    QuicRoute::Ready {
        target,
        server_name: sni,
    }
}

fn build_client_config(cert_pem: Option<&[u8]>, insecure: bool) -> Result<ClientConfig> {
    if insecure {
        return stargate_tls::build_insecure_quic_client_config();
    }
    let cert_data = cert_pem.context("TLS cert required when --quic-insecure is not set")?;
    let mut roots = RootCertStore::empty();
    for cert in rustls_pemfile::certs(&mut &*cert_data) {
        roots
            .add(cert.context("failed to parse router target cert PEM")?)
            .context("failed to add router target cert to root store")?;
    }

    let tls_config = rustls::ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();

    Ok(ClientConfig::new(Arc::new(
        quinn::crypto::rustls::QuicClientConfig::try_from(tls_config)?,
    )))
}

fn build_server_config(
    cert_pem: Option<&[u8]>,
    key_pem: Option<&[u8]>,
    relay_config: RelayEndpointConfig,
) -> Result<quinn::ServerConfig> {
    let (cert_owned, key_owned);
    let (cert_data, key_data) = match (cert_pem, key_pem) {
        (Some(c), Some(k)) => (c, k),
        (Some(_), None) => {
            anyhow::bail!("router TLS key required when TLS cert is provided");
        }
        (None, Some(_)) => {
            anyhow::bail!("router TLS cert required when TLS key is provided");
        }
        _ => {
            info!("no router TLS cert/key provided, generating self-signed certificate");
            let (c, k) = stargate_tls::generate_self_signed_cert()?;
            cert_owned = c;
            key_owned = k;
            (cert_owned.as_slice(), key_owned.as_slice())
        }
    };
    let cert_chain: Vec<rustls::pki_types::CertificateDer<'static>> =
        rustls_pemfile::certs(&mut &*cert_data)
            .collect::<std::result::Result<_, _>>()
            .context("failed to parse router cert PEM")?;
    let key = rustls_pemfile::private_key(&mut &*key_data)
        .context("failed to parse router key PEM")?
        .context("no private key found in router key PEM")?;
    let mut server_config = quinn::ServerConfig::with_single_cert(cert_chain, key)
        .context("build router QUIC server config failed")?;
    server_config.transport_config(build_relay_transport_config(relay_config)?);
    Ok(server_config)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;
    use std::hint::black_box;
    use std::time::Instant;

    use crate::endpoints::PodTarget;
    use crate::perf_tests::assert_twenty_percent_faster;

    fn cert_and_key() -> (Vec<u8>, Vec<u8>) {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        stargate_tls::generate_self_signed_cert().expect("self-signed cert should generate")
    }

    fn cert_and_key_for_names(names: Vec<String>) -> (Vec<u8>, Vec<u8>) {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        stargate_tls::generate_self_signed_cert_for_names(names)
            .expect("self-signed cert should generate")
    }

    fn test_config() -> QuicRouterConfig {
        QuicRouterConfig {
            listen_addr: "127.0.0.1:0".parse().expect("valid listen addr"),
            advertised_hostname_template: "{pod_name}.stargate.external".to_string(),
            target_namespace: String::new(),
            connect_timeout: Duration::from_secs(5),
            relay_max_idle_timeout: Duration::from_secs(60),
            relay_keep_alive_interval: Some(Duration::from_secs(5)),
            tls_cert_pem: None,
            tls_key_pem: None,
            quic_insecure: true,
        }
    }

    fn snapshot_with_quic_target(pod_name: &str, quic_addr: SocketAddr) -> TargetSnapshot {
        TargetSnapshot::initialized(BTreeMap::from([(
            pod_name.to_string(),
            PodTarget {
                pod_name: pod_name.to_string(),
                grpc_addr: "127.0.0.1:50071".to_string(),
                quic_addr: quic_addr.to_string(),
            },
        )]))
    }

    fn synthetic_snapshot(count: usize) -> TargetSnapshot {
        TargetSnapshot::initialized(
            (0..count)
                .map(|index| {
                    let pod_name = format!("stargate-{index}");
                    (
                        pod_name.clone(),
                        PodTarget {
                            pod_name,
                            grpc_addr: format!("10.0.0.{index}:50071"),
                            quic_addr: format!("10.0.0.{index}:50072"),
                        },
                    )
                })
                .collect::<BTreeMap<_, _>>(),
        )
    }

    fn server_config_from_pem(
        cert_pem: &[u8],
        key_pem: &[u8],
    ) -> anyhow::Result<quinn::ServerConfig> {
        let cert_chain: Vec<rustls::pki_types::CertificateDer<'static>> =
            rustls_pemfile::certs(&mut &*cert_pem).collect::<std::result::Result<_, _>>()?;
        let key =
            rustls_pemfile::private_key(&mut &*key_pem)?.expect("test private key should exist");
        quinn::ServerConfig::with_single_cert(cert_chain, key).map_err(Into::into)
    }

    async fn assert_bidi_relay(
        router_server_config: quinn::ServerConfig,
        target_server_config: quinn::ServerConfig,
        router_client_config: quinn::ClientConfig,
        router_target_client_config: quinn::ClientConfig,
    ) {
        let target_server =
            quinn::Endpoint::server(target_server_config, "127.0.0.1:0".parse().unwrap())
                .expect("target server endpoint");
        let target_addr = target_server.local_addr().expect("target local addr");
        let router_server =
            quinn::Endpoint::server(router_server_config, "127.0.0.1:0".parse().unwrap())
                .expect("router server endpoint");
        let router_addr = router_server.local_addr().expect("router local addr");
        let snapshot = snapshot_with_quic_target("stargate-1", target_addr);
        let (_targets_tx, targets_rx) = watch::channel(snapshot);
        let relay_endpoints = Arc::new(
            build_relay_endpoints(RelayEndpointConfig::default(), router_target_client_config)
                .expect("relay endpoints"),
        );
        let metrics = Arc::new(RouterMetrics::new().expect("router metrics"));
        let config = Arc::new(QuicRouterRuntimeConfig::new(test_config()));

        let target_task = tokio::spawn(async move {
            let incoming = target_server.accept().await.expect("target should accept");
            let connection = incoming.await.expect("target connection should finish");
            let (mut send, mut recv) = connection
                .accept_bi()
                .await
                .expect("target should accept relayed bidi stream");
            let body = recv
                .read_to_end(1024)
                .await
                .expect("target should read body");
            assert_eq!(body, b"hello through router");
            send.write_all(b"hello from target")
                .await
                .expect("target should write response");
            send.finish().expect("target should finish response");
            let _ = connection.closed().await;
        });

        let router_task = tokio::spawn(async move {
            let incoming = router_server.accept().await.expect("router should accept");
            dispatch_incoming(incoming, targets_rx, relay_endpoints, metrics, config)
                .await
                .expect("router should dispatch incoming connection");
        });

        let mut client_endpoint =
            quinn::Endpoint::client("127.0.0.1:0".parse().unwrap()).expect("client endpoint");
        client_endpoint.set_default_client_config(router_client_config);
        let client_connection = client_endpoint
            .connect(router_addr, "stargate-1.stargate.external")
            .expect("connect should start")
            .await
            .expect("connect to router should finish");
        let (mut send, mut recv) = client_connection
            .open_bi()
            .await
            .expect("client should open bidi stream");
        send.write_all(b"hello through router")
            .await
            .expect("client should write body");
        send.finish().expect("client should finish request");
        let response = recv
            .read_to_end(1024)
            .await
            .expect("client should read target response");
        assert_eq!(response, b"hello from target");

        client_connection.close(0u32.into(), b"client complete");
        target_task.await.expect("target task should complete");
        tokio::time::timeout(Duration::from_secs(5), router_task)
            .await
            .expect("router relay should shut down after connections close")
            .expect("router task should not panic");
    }

    #[test]
    fn server_config_rejects_cert_without_key() {
        let (cert, _) = cert_and_key();

        let error =
            build_server_config(Some(cert.as_slice()), None, RelayEndpointConfig::default())
                .expect_err("key should be required");

        assert!(error.to_string().contains("TLS key required"));
    }

    #[test]
    fn server_config_rejects_key_without_cert() {
        let (_, key) = cert_and_key();

        let error = build_server_config(None, Some(key.as_slice()), RelayEndpointConfig::default())
            .expect_err("cert should be required");

        assert!(error.to_string().contains("TLS cert required"));
    }

    #[test]
    fn client_config_requires_cert_in_secure_mode() {
        let error =
            build_client_config(None, false).expect_err("secure client config should require cert");

        assert!(error.to_string().contains("TLS cert required"));
    }

    #[test]
    fn route_for_sni_returns_ready_peer_for_matching_target() {
        let target_addr: SocketAddr = "127.0.0.1:50072".parse().expect("valid target addr");
        let snapshot = snapshot_with_quic_target("stargate-1", target_addr);
        let route = route_for_sni(
            Some("stargate-1.stargate.external"),
            &snapshot,
            &QuicRouterRuntimeConfig::new(test_config()),
        );

        match route {
            QuicRoute::Ready {
                target,
                server_name,
            } => {
                assert_eq!(target.pod_name, "stargate-1");
                assert_eq!(target.quic_addr, "127.0.0.1:50072");
                assert_eq!(server_name, "stargate-1.stargate.external");
            }
            route => panic!("unexpected route: {route:?}"),
        }
    }

    #[test]
    fn route_for_sni_rejects_missing_unknown_and_unready_targets() {
        let target_addr: SocketAddr = "127.0.0.1:50072".parse().expect("valid target addr");
        let snapshot = snapshot_with_quic_target("stargate-1", target_addr);
        let config = QuicRouterRuntimeConfig::new(test_config());

        assert_eq!(
            route_for_sni(None, &snapshot, &config),
            QuicRoute::MissingSni
        );
        assert_eq!(
            route_for_sni(Some("stargate-1.other.example"), &snapshot, &config),
            QuicRoute::UnknownSni
        );
        assert_eq!(
            route_for_sni(Some("stargate-2.stargate.external"), &snapshot, &config),
            QuicRoute::TargetUnavailable
        );
    }

    #[test]
    fn route_for_sni_matches_namespace_hostname_template() {
        let target_addr: SocketAddr = "127.0.0.1:50072".parse().expect("valid target addr");
        let snapshot = snapshot_with_quic_target("stargate-1", target_addr);
        let mut config = test_config();
        config.advertised_hostname_template =
            "{pod_name}.{namespace}.stargate.external".to_string();
        config.target_namespace = "prod".to_string();
        let config = QuicRouterRuntimeConfig::new(config);

        let route = route_for_sni(
            Some("stargate-1.prod.stargate.external"),
            &snapshot,
            &config,
        );

        match route {
            QuicRoute::Ready {
                target,
                server_name,
            } => {
                assert_eq!(target.pod_name, "stargate-1");
                assert_eq!(target.quic_addr, "127.0.0.1:50072");
                assert_eq!(server_name, "stargate-1.prod.stargate.external");
            }
            route => panic!("unexpected route: {route:?}"),
        }
    }

    #[test]
    #[ignore = "performance benchmark; run with --ignored --nocapture"]
    fn bench_quic_sni_route_resolution() {
        const BASELINE_NS_PER_OP: f64 = 265.32;

        let snapshot = synthetic_snapshot(128);
        let config = QuicRouterRuntimeConfig::new(test_config());
        let iterations = 1_000_000usize;
        let started = Instant::now();
        let mut checksum = 0usize;

        for _ in 0..iterations {
            match route_for_sni(
                black_box(Some("stargate-64.stargate.external")),
                black_box(&snapshot),
                black_box(&config),
            ) {
                QuicRoute::Ready {
                    target,
                    server_name,
                } => {
                    checksum = checksum
                        .wrapping_add(target.pod_name.len())
                        .wrapping_add(target.quic_addr.len())
                        .wrapping_add(server_name.len());
                }
                route => panic!("unexpected route: {route:?}"),
            }
        }

        let elapsed = started.elapsed();
        let ns_per_op = elapsed.as_nanos() as f64 / iterations as f64;
        eprintln!(
            "bench_quic_sni_route_resolution: iterations={iterations} elapsed={elapsed:?} ns_per_op={ns_per_op:.2} checksum={checksum}"
        );
        assert!(checksum > 0);
        assert_twenty_percent_faster(
            "bench_quic_sni_route_resolution",
            BASELINE_NS_PER_OP,
            ns_per_op,
        );
    }

    #[tokio::test]
    async fn quic_router_relays_custom_bidi_streams_by_sni() {
        let (router_cert, router_key) = cert_and_key();
        let (target_cert, target_key) = cert_and_key();
        assert_bidi_relay(
            server_config_from_pem(&router_cert, &router_key).expect("router server config"),
            server_config_from_pem(&target_cert, &target_key).expect("target server config"),
            build_client_config(None, true).expect("insecure client config"),
            build_client_config(None, true).expect("insecure relay client config"),
        )
        .await;
    }

    #[tokio::test]
    async fn quic_router_secure_relay_preserves_target_sni() {
        let names = vec!["stargate-1.stargate.external".to_string()];
        let (router_cert, router_key) = cert_and_key_for_names(names.clone());
        let (target_cert, target_key) = cert_and_key_for_names(names);
        assert_bidi_relay(
            server_config_from_pem(&router_cert, &router_key).expect("router server config"),
            server_config_from_pem(&target_cert, &target_key).expect("target server config"),
            build_client_config(None, true).expect("insecure client config"),
            build_client_config(Some(&target_cert), false).expect("secure relay client config"),
        )
        .await;
    }
}
