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

use std::future::Future;
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, anyhow};
use futures::{Stream, StreamExt};
use quinn::{ClientConfig, Endpoint, TransportConfig};
use tracing::{info, warn};

pub const DEFAULT_RELAY_MAX_IDLE_TIMEOUT: Duration = Duration::from_secs(300);
pub const DEFAULT_RELAY_KEEP_ALIVE_INTERVAL: Duration = Duration::from_secs(10);

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RelayEndpointConfig {
    pub max_idle_timeout: Duration,
    pub keep_alive_interval: Option<Duration>,
}

impl Default for RelayEndpointConfig {
    fn default() -> Self {
        Self {
            max_idle_timeout: DEFAULT_RELAY_MAX_IDLE_TIMEOUT,
            keep_alive_interval: Some(DEFAULT_RELAY_KEEP_ALIVE_INTERVAL),
        }
    }
}

#[derive(Debug)]
pub struct RelayEndpoints {
    endpoint_v4: Endpoint,
    endpoint_v6: std::result::Result<Endpoint, String>,
}

impl RelayEndpoints {
    fn endpoint_for_resolved_addrs(
        &self,
        addrs: impl IntoIterator<Item = SocketAddr>,
    ) -> Result<(SocketAddr, &Endpoint)> {
        let mut unavailable_reasons = Vec::new();
        for addr in addrs {
            match self.endpoint_for_addr(&addr) {
                Ok(endpoint) => return Ok((addr, endpoint)),
                Err(err) => unavailable_reasons.push(format!("{addr}: {err}")),
            }
        }

        if unavailable_reasons.is_empty() {
            Err(anyhow!("no addresses for peer"))
        } else {
            Err(anyhow!(
                "no usable relay endpoint for peer addresses: {}",
                unavailable_reasons.join(", ")
            ))
        }
    }

    fn endpoint_for_addr(&self, addr: &SocketAddr) -> Result<&Endpoint> {
        if addr.is_ipv4() {
            return Ok(&self.endpoint_v4);
        }
        self.endpoint_v6
            .as_ref()
            .map_err(|reason| anyhow!("IPv6 relay endpoint unavailable: {reason}"))
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PeerTarget {
    pub dial_addr: String,
    pub server_name: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PeerResolution {
    NotPeer,
    Local,
    Peer(PeerTarget),
}

pub trait ForwardingResolver: Send + Sync {
    fn resolve_peer(&self, host: &str, port: u16) -> PeerResolution;
}

/// Forwards successes while a bounded error side channel applies backpressure until consumption.
pub fn forward_stream_messages<S, T, E>(
    inbound: S,
    report_error: fn(&E),
) -> (impl Stream<Item = T> + Send, tokio::sync::mpsc::Receiver<E>)
where
    S: Stream<Item = std::result::Result<T, E>> + Send + 'static,
    T: Send + 'static,
    E: Send + 'static,
{
    let (error_tx, error_rx) = tokio::sync::mpsc::channel(1);
    let messages = inbound.filter_map(move |result| {
        let error_tx = error_tx.clone();
        async move {
            match result {
                Ok(message) => Some(message),
                Err(error) => {
                    report_error(&error);
                    let _ = error_tx.send(error).await;
                    None
                }
            }
        }
    });
    (messages, error_rx)
}

pub struct HeadlessDnsResolver {
    pub self_pod_name: String,
    pub advertised_hostname_template: String,
    pub namespace: String,
    pub headless_dns_suffix: String,
}

impl ForwardingResolver for HeadlessDnsResolver {
    fn resolve_peer(&self, host: &str, port: u16) -> PeerResolution {
        match extract_pod_from_hostname(host, &self.advertised_hostname_template, &self.namespace) {
            None => PeerResolution::NotPeer,
            Some(pod_name) if pod_name == self.self_pod_name => PeerResolution::Local,
            Some(pod_name) => PeerResolution::Peer(PeerTarget {
                // Headless Service DNS is backed by ready EndpointSlices. Keep the
                // original advertised hostname as the QUIC server name so verified
                // relays still validate the client-facing certificate identity.
                dial_addr: format!("{pod_name}.{}:{port}", self.headless_dns_suffix),
                server_name: host.to_string(),
            }),
        }
    }
}

pub async fn forward_quic_connection(
    client_connection: quinn::Connection,
    peer: &PeerTarget,
    endpoints: &RelayEndpoints,
    peer_connect_timeout: Duration,
) -> Result<()> {
    let peer_connect_timeout_ms = peer_connect_timeout.as_millis();
    let mut resolved_addrs = tokio::time::timeout(
        peer_connect_timeout,
        tokio::net::lookup_host(&peer.dial_addr),
    )
    .await
    .with_context(|| format!("DNS resolve peer timed out after {peer_connect_timeout_ms}ms"))?
    .context("DNS resolve peer")?;
    let (resolved, endpoint) = endpoints.endpoint_for_resolved_addrs(&mut resolved_addrs)?;
    let connecting = endpoint
        .connect(resolved, &peer.server_name)
        .context("initiate peer QUIC connect")?;
    let peer_connection = await_peer_connect(connecting, peer_connect_timeout).await?;

    info!(
        peer = %peer.dial_addr,
        server_name = %peer.server_name,
        "QUIC connection relay started"
    );

    relay_connections(client_connection, peer_connection).await;

    info!(
        peer = %peer.dial_addr,
        server_name = %peer.server_name,
        "QUIC connection relay finished"
    );
    Ok(())
}

async fn await_peer_connect<F, T, E>(connect: F, peer_connect_timeout: Duration) -> Result<T>
where
    F: Future<Output = std::result::Result<T, E>>,
    E: std::error::Error + Send + Sync + 'static,
{
    let peer_connect_timeout_ms = peer_connect_timeout.as_millis();
    tokio::time::timeout(peer_connect_timeout, connect)
        .await
        .with_context(|| format!("peer QUIC connect timed out after {peer_connect_timeout_ms}ms"))?
        .context("peer QUIC connect failed")
}

async fn relay_connections(client: quinn::Connection, peer: quinn::Connection) {
    let client_to_peer = relay_direction(client.clone(), peer.clone());
    let peer_to_client = relay_direction(peer.clone(), client.clone());

    tokio::select! {
        _ = client_to_peer => {}
        _ = peer_to_client => {}
        _ = client.closed() => {}
        _ = peer.closed() => {}
    }
}

macro_rules! spawn_stream_relay {
    ($tasks:expr, $stream:expr, $initiator:expr, $relay:ident, $accept_error:literal, $relay_error:literal) => {{
        let stream = match $stream {
            Ok(stream) => stream,
            Err(error) => {
                warn!(%error, $accept_error);
                break;
            }
        };
        let initiator = $initiator.clone();
        $tasks.spawn(async move {
            if let Err(error) = $relay(stream, &initiator).await {
                warn!(%error, $relay_error);
            }
        });
    }};
}

async fn relay_direction(acceptor: quinn::Connection, initiator: quinn::Connection) {
    let mut tasks = tokio::task::JoinSet::new();
    loop {
        tokio::select! {
            bi = acceptor.accept_bi() => {
                spawn_stream_relay!(tasks, bi, initiator, relay_bi_stream,
                    "accept_bi failed in relay", "bi-stream relay error");
            }
            uni = acceptor.accept_uni() => {
                spawn_stream_relay!(tasks, uni, initiator, relay_uni_stream,
                    "accept_uni failed in relay", "uni-stream relay error");
            }
        }
    }
    tasks.shutdown().await;
}

async fn relay_bi_stream(
    (a_send, a_recv): (quinn::SendStream, quinn::RecvStream),
    initiator: &quinn::Connection,
) -> Result<()> {
    let (b_send, b_recv) = initiator
        .open_bi()
        .await
        .context("open bi-stream on peer")?;

    let (r1, r2) = tokio::join!(relay_stream(a_recv, b_send), relay_stream(b_recv, a_send));
    r1.and(r2)
}

async fn relay_uni_stream(a_recv: quinn::RecvStream, initiator: &quinn::Connection) -> Result<()> {
    let b_send = initiator
        .open_uni()
        .await
        .context("open uni-stream on peer")?;

    relay_stream(a_recv, b_send).await
}

async fn relay_stream(mut recv: quinn::RecvStream, mut send: quinn::SendStream) -> Result<()> {
    tokio::io::copy(&mut recv, &mut send).await?;
    send.finish()?;
    Ok(())
}

pub fn build_relay_transport_config(config: RelayEndpointConfig) -> Result<Arc<TransportConfig>> {
    let mut transport = TransportConfig::default();
    let idle_timeout = config
        .max_idle_timeout
        .try_into()
        .context("relay max idle timeout must fit QUIC transport parameters")?;
    transport.max_idle_timeout(Some(idle_timeout));
    transport.keep_alive_interval(config.keep_alive_interval);
    Ok(Arc::new(transport))
}

pub fn build_relay_endpoints(
    config: RelayEndpointConfig,
    client_config: ClientConfig,
) -> Result<RelayEndpoints> {
    build_relay_endpoints_with_factory(config, client_config, Endpoint::client)
}

fn build_relay_endpoints_with_factory(
    config: RelayEndpointConfig,
    client_config: ClientConfig,
    mut build_client_endpoint: impl FnMut(SocketAddr) -> std::io::Result<Endpoint>,
) -> Result<RelayEndpoints> {
    let mut client_config = client_config;
    client_config.transport_config(build_relay_transport_config(config)?);

    let mut endpoint_v4 = build_client_endpoint((Ipv4Addr::UNSPECIFIED, 0).into())
        .context("bind IPv4 relay endpoint")?;
    let endpoint_v6 = build_client_endpoint((Ipv6Addr::UNSPECIFIED, 0).into())
        .map(|mut endpoint| {
            endpoint.set_default_client_config(client_config.clone());
            endpoint
        })
        .map_err(|error| {
            warn!(
                %error,
                "IPv6 relay endpoint unavailable; continuing with IPv4-only relay endpoint"
            );
            error.to_string()
        });
    endpoint_v4.set_default_client_config(client_config);
    Ok(RelayEndpoints {
        endpoint_v4,
        endpoint_v6,
    })
}

pub fn render_hostname(template: &str, pod_name: &str, namespace: &str) -> String {
    template
        .replace("{pod_name}", pod_name)
        .replace("{namespace}", namespace)
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HostnameMatcher {
    prefix: String,
    suffix: String,
}

impl HostnameMatcher {
    pub fn new(template: &str, namespace: &str) -> Option<Self> {
        let resolved = template.replace("{namespace}", namespace);
        let (prefix, suffix) = resolved.split_once("{pod_name}")?;
        Some(Self {
            prefix: prefix.to_string(),
            suffix: suffix.to_string(),
        })
    }

    pub fn extract_pod<'a>(&self, hostname: &'a str) -> Option<&'a str> {
        let pod = hostname
            .strip_prefix(&self.prefix)?
            .strip_suffix(&self.suffix)?;
        (!pod.is_empty()).then_some(pod)
    }
}

pub fn extract_pod_from_hostname(
    hostname: &str,
    template: &str,
    namespace: &str,
) -> Option<String> {
    HostnameMatcher::new(template, namespace)
        .and_then(|matcher| matcher.extract_pod(hostname).map(str::to_string))
}

#[cfg(test)]
mod tests {
    use super::*;
    use futures::{FutureExt, StreamExt};

    #[tokio::test]
    async fn forwarded_messages_keep_order_and_apply_error_backpressure() {
        let (messages, mut errors) = forward_stream_messages(
            futures::stream::iter([Ok(1), Err("first"), Err("second"), Ok(2)]),
            |_| {},
        );
        futures::pin_mut!(messages);

        assert_eq!(messages.next().await, Some(1));
        assert!(messages.next().now_or_never().is_none());
        assert_eq!(errors.recv().await, Some("first"));
        assert_eq!(messages.next().await, Some(2));
        assert_eq!(errors.recv().await, Some("second"));
        assert_eq!(messages.next().await, None);
    }

    #[test]
    fn render_hostname_substitutes_pod_name_and_namespace() {
        assert_eq!(
            render_hostname("{pod_name}.stargate.external", "stargate-1", "ns"),
            "stargate-1.stargate.external"
        );
    }

    #[test]
    fn render_hostname_with_namespace_placeholder() {
        assert_eq!(
            render_hostname(
                "{pod_name}.{namespace}.stargate.external",
                "stargate-1",
                "prod"
            ),
            "stargate-1.prod.stargate.external"
        );
    }

    macro_rules! extract_pod_tests {
        ($($name:ident: ($hostname:expr, $template:expr, $namespace:expr) => $expected:expr;)+) => {
            $(
                #[test]
                fn $name() {
                    assert_eq!(
                        extract_pod_from_hostname($hostname, $template, $namespace).as_deref(),
                        $expected
                    );
                }
            )+
        };
    }

    extract_pod_tests! {
        extract_pod_matches_template:
            ("stargate-1.stargate.external", "{pod_name}.stargate.external", "")
            => Some("stargate-1");
        extract_pod_returns_none_for_non_matching_host:
            ("stargate-1.other.domain", "{pod_name}.stargate.external", "") => None;
        extract_pod_returns_none_for_empty_pod:
            (".stargate.external", "{pod_name}.stargate.external", "") => None;
        extract_pod_with_namespace_in_template:
            ("stargate-1.prod.stargate.external",
             "{pod_name}.{namespace}.stargate.external", "prod") => Some("stargate-1");
        extract_pod_rejects_template_without_placeholder:
            ("stargate-1.stargate.external", "static.stargate.external", "") => None;
    }

    fn make_resolver() -> HeadlessDnsResolver {
        HeadlessDnsResolver {
            self_pod_name: "stargate-0".to_string(),
            advertised_hostname_template: "{pod_name}.stargate.external".to_string(),
            namespace: String::new(),
            headless_dns_suffix: "stargate-headless.prod.svc.cluster.local".to_string(),
        }
    }

    #[test]
    fn resolve_peer_returns_local_for_self() {
        assert_resolution("stargate-0.stargate.external", PeerResolution::Local);
    }

    #[test]
    fn resolve_peer_returns_not_peer_for_non_matching_host() {
        assert_resolution("something.other.domain", PeerResolution::NotPeer);
    }

    #[test]
    fn resolve_peer_returns_headless_dns_addr_and_original_hostname_for_peer() {
        assert_resolution(
            "stargate-1.stargate.external",
            PeerResolution::Peer(PeerTarget {
                dial_addr: "stargate-1.stargate-headless.prod.svc.cluster.local:50072".to_string(),
                server_name: "stargate-1.stargate.external".to_string(),
            }),
        );
    }

    #[test]
    fn resolve_peer_treats_matching_non_self_host_as_peer_dns_name() {
        assert_resolution(
            "stargate-2.stargate.external",
            PeerResolution::Peer(PeerTarget {
                dial_addr: "stargate-2.stargate-headless.prod.svc.cluster.local:50072".to_string(),
                server_name: "stargate-2.stargate.external".to_string(),
            }),
        );
    }

    fn assert_resolution(host: &str, expected: PeerResolution) {
        assert_eq!(make_resolver().resolve_peer(host, 50072), expected);
    }

    #[test]
    fn resolve_peer_returns_not_peer_for_wrong_namespace() {
        let resolver = HeadlessDnsResolver {
            self_pod_name: "stargate-0".to_string(),
            advertised_hostname_template: "{pod_name}.{namespace}.stargate.external".to_string(),
            namespace: "prod".to_string(),
            headless_dns_suffix: "stargate-headless.prod.svc.cluster.local".to_string(),
        };
        assert_eq!(
            resolver.resolve_peer("stargate-1.staging.stargate.external", 50072),
            PeerResolution::NotPeer
        );
    }

    macro_rules! host_tests {
        ($($name:ident: $addr:literal => $expected:literal;)+) => {
            $(
                #[test]
                fn $name() {
                    let authority: http::uri::Authority = $addr.parse().unwrap();
                    assert_eq!(authority.host(), $expected);
                }
            )+
        };
    }

    host_tests! {
        ipv6_bracketed_host_extraction: "[::1]:50072" => "[::1]";
        ipv4_host_extraction: "10.0.0.1:50072" => "10.0.0.1";
        hostname_host_extraction: "pod-a.stargate.external:50072" => "pod-a.stargate.external";
    }

    fn test_server_config() -> quinn::ServerConfig {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
        let cert_chain: Vec<rustls::pki_types::CertificateDer<'static>> =
            rustls_pemfile::certs(&mut &*cert_pem)
                .collect::<std::result::Result<_, _>>()
                .unwrap();
        let key = rustls_pemfile::private_key(&mut &*key_pem)
            .unwrap()
            .unwrap();
        let tls_config = rustls::ServerConfig::builder()
            .with_no_client_auth()
            .with_single_cert(cert_chain, key)
            .unwrap();
        quinn::ServerConfig::with_crypto(Arc::new(
            quinn::crypto::rustls::QuicServerConfig::try_from(tls_config).unwrap(),
        ))
    }

    fn ipv4_only_relay_endpoints() -> RelayEndpoints {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        build_relay_endpoints_with_factory(
            RelayEndpointConfig::default(),
            stargate_tls::build_insecure_quic_client_config().unwrap(),
            |addr| {
                if addr.is_ipv6() {
                    Err(std::io::Error::new(
                        std::io::ErrorKind::Unsupported,
                        "IPv6 disabled for test",
                    ))
                } else {
                    Endpoint::client(addr)
                }
            },
        )
        .expect("relay endpoints should allow IPv4-only nodes")
    }

    fn assert_relay_endpoint_family(endpoints: &RelayEndpoints, target: &str, expect_ipv6: bool) {
        let target = target.parse().unwrap();
        let local = endpoints
            .endpoint_for_addr(&target)
            .expect("relay endpoint")
            .local_addr()
            .expect("relay endpoint address");
        assert_eq!(local.is_ipv6(), expect_ipv6);
    }

    #[test]
    fn default_relay_endpoint_config_keeps_idle_tunnels_alive() {
        assert_eq!(
            RelayEndpointConfig::default(),
            RelayEndpointConfig {
                max_idle_timeout: Duration::from_secs(300),
                keep_alive_interval: Some(Duration::from_secs(10)),
            }
        );
    }

    #[test]
    fn relay_transport_config_uses_idle_timeout_and_keep_alive() {
        let transport = build_relay_transport_config(RelayEndpointConfig {
            max_idle_timeout: Duration::from_secs(120),
            keep_alive_interval: Some(Duration::from_secs(7)),
        })
        .expect("relay transport config should build");
        let debug = format!("{transport:?}");

        assert!(debug.contains("max_idle_timeout: Some(120000)"));
        assert!(debug.contains("keep_alive_interval: Some(7s)"));
    }

    #[tokio::test]
    async fn relay_endpoints_select_ipv4_and_ipv6_local_endpoint() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let relay_client_config = stargate_tls::build_insecure_quic_client_config().unwrap();
        let endpoints = build_relay_endpoints(RelayEndpointConfig::default(), relay_client_config)
            .expect("relay endpoints");
        assert_relay_endpoint_family(&endpoints, "127.0.0.1:50072", false);
        if endpoints.endpoint_v6.is_ok() {
            assert_relay_endpoint_family(&endpoints, "[::1]:50072", true);
        }
    }

    #[tokio::test]
    async fn relay_endpoints_allow_ipv4_when_ipv6_bind_fails() {
        let endpoints = ipv4_only_relay_endpoints();

        let ipv6_target: SocketAddr = "[::1]:50072".parse().unwrap();

        assert_relay_endpoint_family(&endpoints, "127.0.0.1:50072", false);
        assert_eq!(
            endpoints
                .endpoint_for_addr(&ipv6_target)
                .expect_err("ipv6 target should fail when no ipv6 endpoint is available")
                .to_string(),
            "IPv6 relay endpoint unavailable: IPv6 disabled for test"
        );
    }

    #[tokio::test]
    async fn relay_endpoint_selection_skips_ipv6_when_ipv6_bind_fails() {
        let endpoints = ipv4_only_relay_endpoints();

        let selected = endpoints
            .endpoint_for_resolved_addrs([
                "[::1]:50072".parse().unwrap(),
                "127.0.0.1:50072".parse().unwrap(),
            ])
            .expect("ipv4 target should be selected");

        assert_eq!(selected.0, "127.0.0.1:50072".parse().unwrap());
        assert!(selected.1.local_addr().unwrap().is_ipv4());
    }

    #[tokio::test]
    async fn peer_quic_connect_is_bounded_by_connect_timeout() {
        let error = await_peer_connect(
            std::future::pending::<std::result::Result<(), std::io::Error>>(),
            Duration::from_millis(10),
        )
        .await
        .expect_err("pending peer connect should time out");

        assert_eq!(error.to_string(), "peer QUIC connect timed out after 10ms");
    }

    #[tokio::test]
    async fn quic_relay_forwards_unidirectional_streams() {
        let peer_server =
            Endpoint::server(test_server_config(), "127.0.0.1:0".parse().unwrap()).unwrap();
        let peer_addr = peer_server.local_addr().unwrap();
        let relay_server =
            Endpoint::server(test_server_config(), "127.0.0.1:0".parse().unwrap()).unwrap();
        let relay_addr = relay_server.local_addr().unwrap();
        let relay_client_config = stargate_tls::build_insecure_quic_client_config().unwrap();
        let relay_endpoints =
            build_relay_endpoints(RelayEndpointConfig::default(), relay_client_config)
                .expect("relay endpoints");

        let (received_tx, received_rx) = tokio::sync::oneshot::channel();
        let peer_task = tokio::spawn(async move {
            let incoming = peer_server.accept().await.expect("peer should accept");
            let connection = incoming.await.expect("peer connection should complete");
            let mut recv = connection
                .accept_uni()
                .await
                .expect("peer should accept relayed uni stream");
            let body = recv.read_to_end(1024).await.expect("read uni stream");
            received_tx.send(body).expect("send received bytes");
            connection.close(0u32.into(), b"test complete");
        });

        let peer = PeerTarget {
            dial_addr: peer_addr.to_string(),
            server_name: "stargate".to_string(),
        };
        let relay_task = tokio::spawn(async move {
            let incoming = relay_server.accept().await.expect("relay should accept");
            let connection = incoming.await.expect("relay connection should complete");
            forward_quic_connection(connection, &peer, &relay_endpoints, Duration::from_secs(5))
                .await
                .expect("relay should forward connection");
        });

        let mut client_endpoint = Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        client_endpoint
            .set_default_client_config(stargate_tls::build_insecure_quic_client_config().unwrap());
        let client_connection = client_endpoint
            .connect(relay_addr, "stargate")
            .unwrap()
            .await
            .unwrap();
        let mut send = client_connection.open_uni().await.unwrap();
        send.write_all(b"hello over uni").await.unwrap();
        send.finish().unwrap();

        let received = tokio::time::timeout(Duration::from_secs(5), received_rx)
            .await
            .expect("relayed unidirectional stream should arrive")
            .expect("peer task should send received bytes");
        assert_eq!(received, b"hello over uni");

        client_connection.close(0u32.into(), b"test complete");
        peer_task.await.unwrap();
        relay_task.abort();
    }
}
