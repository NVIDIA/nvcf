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
use std::time::Duration;

use anyhow::{Context, Result};
use stargate::discovery::{
    Discovery, DnsDiscovery, HeadlessDnsDiscovery, HeadlessDnsDiscoveryConfig, SelfOnlyDiscovery,
};
use stargate::proxy::{ProxyRetryConfig, ProxyTransportConfig};
use stargate::registration::{
    DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT, DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
};
use stargate::runtime::{StargateRuntime, StargateRuntimeConfig};
use stargate_forwarding::{ForwardingResolver, HeadlessDnsResolver};
use stargate_protocol::TunnelTransportProtocol;
use stargate_tls::ServerTlsIdentity;
use tracing::info;

const DEFAULT_PROXY_MAX_REPLAY_BODY_BYTES: usize = 64 * 1024 * 1024;

fn parse_nonzero_millis(value: &str) -> std::result::Result<u64, String> {
    let millis = value
        .parse::<u64>()
        .map_err(|err| format!("invalid millisecond value: {err}"))?;

    if millis == 0 {
        return Err("value must be greater than 0".to_string());
    }

    Ok(millis)
}

fn parse_nonzero_usize(value: &str) -> std::result::Result<usize, String> {
    let count = value
        .parse::<usize>()
        .map_err(|err| format!("invalid count: {err}"))?;

    if count == 0 {
        return Err("value must be greater than 0".to_string());
    }

    Ok(count)
}

#[derive(clap::Parser, Debug)]
#[command(name = "stargate")]
struct Args {
    /// Stable Stargate process or pod identity.
    #[arg(long, value_name = "ID")]
    stargate_id: String,

    /// Local TCP socket for backend-facing WatchStargates and registration.
    #[arg(long, default_value = "0.0.0.0:50071", value_name = "ADDR")]
    listen_addr: String,

    /// Local TCP socket for frontend-facing model discovery (`ListModels`).
    #[arg(long, default_value = "0.0.0.0:50073", value_name = "ADDR")]
    model_discovery_listen_addr: String,

    /// Local HTTP socket for proxy traffic, health probes, and metrics.
    #[arg(long, default_value = "0.0.0.0:8000", value_name = "ADDR")]
    http_listen_addr: String,

    /// Self gRPC address published by non-Kubernetes discovery and used as the
    /// port source for Kubernetes advertised hostnames.
    #[arg(long, value_name = "ADDR")]
    advertise_addr: SocketAddr,

    /// DNS name used for Stargate peer discovery.
    ///
    /// In Kubernetes this should be the headless Service so EndpointSlice
    /// readiness controls peer visibility and forwarding targets.
    #[arg(long, value_name = "DNS_NAME")]
    stargate_discovery_dns_name: String,

    /// Additional WatchStargates endpoints for remote regions. Repeatable.
    ///
    /// These are recursive watch seeds, not registration targets. Pylons only
    /// register to concrete `stargates` entries returned by watch snapshots.
    #[arg(
        long,
        env = "STARGATE_REMOTE_WATCH_URLS",
        value_delimiter = ',',
        value_name = "URL"
    )]
    remote_stargate_url: Vec<String>,

    /// Optional pylon dial address for backend-facing gRPC registration/watch.
    ///
    /// Stargate still advertises per-pod addresses as gRPC authority/SNI
    /// identity, and sends this address separately so pylons can connect
    /// through a TCP load balancer.
    #[arg(long, value_name = "ADDR")]
    grpc_pylon_dial_addr: Option<String>,

    /// Backend-facing advertised hostname template.
    ///
    /// Supports `{pod_name}` and `{namespace}`. In Kubernetes this rendered host
    /// becomes the pylon gRPC authority and reverse QUIC SNI so routers can
    /// select the intended Stargate pod.
    #[arg(long, value_name = "TEMPLATE")]
    advertised_hostname_template: Option<String>,

    /// Pod name used to render `--advertised-hostname-template`.
    #[arg(long, env = "POD_NAME", value_name = "NAME")]
    pod_name: Option<String>,

    /// Pod namespace used to render `--advertised-hostname-template`.
    #[arg(long, env = "POD_NAMESPACE", value_name = "NAMESPACE")]
    pod_namespace: Option<String>,

    /// Publish only this Stargate in WatchStargates instead of DNS-discovered peers.
    #[arg(long, default_value_t = false)]
    disable_dns_discovery: bool,

    /// Interval for refreshing DNS-discovered Stargate peers.
    #[arg(long, default_value_t = 1000, value_parser = parse_nonzero_millis, value_name = "MS")]
    dns_poll_ms: u64,

    /// Maximum resolver cache TTL used by Stargate DNS discovery.
    #[arg(long, default_value_t = 1000, value_name = "MS")]
    dns_resolver_ttl_ms: u64,

    /// Maximum interval between unchanged WatchStargates snapshots.
    #[arg(long, default_value_t = 5000, value_name = "MS")]
    watch_heartbeat_ms: u64,

    /// Minimum idle timeout for heartbeat-aware registration streams; 0 disables all enforcement
    #[arg(
        long,
        default_value_t = DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT.as_millis() as u64,
        env = "STARGATE_REGISTRATION_UPDATE_IDLE_TIMEOUT_MS",
        value_name = "MS"
    )]
    registration_update_idle_timeout_ms: u64,

    /// Maximum idle timeout for heartbeat-aware registration streams; 0 disables all enforcement
    #[arg(
        long,
        default_value_t = DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT.as_millis() as u64,
        env = "STARGATE_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT_MS",
        value_name = "MS"
    )]
    registration_update_max_idle_timeout_ms: u64,

    /// Grace period for shutdown tasks after Stargate starts draining.
    #[arg(long, default_value_t = 30000, value_name = "MS")]
    shutdown_drain_timeout_ms: u64,

    /// Timeout for establishing outbound direct QUIC connections and peer relays.
    #[arg(long, default_value_t = 2000, value_name = "MS")]
    quic_connect_timeout_ms: u64,

    /// Timeout for each proxied request over an established QUIC tunnel.
    #[arg(long, default_value_t = 30000, value_name = "MS")]
    quic_request_timeout_ms: u64,

    /// Number of direct QUIC connections opened per backend.
    #[arg(
        long,
        default_value_t = 1,
        env = "STARGATE_DIRECT_QUIC_CONNECTIONS",
        value_parser = parse_nonzero_usize,
        value_name = "N"
    )]
    direct_quic_connections: usize,

    /// Maximum direct QUIC reconnect attempts on the proxy hot path
    #[arg(
        long,
        default_value_t = 2,
        env = "STARGATE_PROXY_MAX_CONNECT_RETRIES",
        value_name = "N"
    )]
    proxy_max_connect_retries: u32,

    /// Maximum retries for explicit retryable upstream responses
    #[arg(
        long,
        default_value_t = 2,
        env = "STARGATE_PROXY_MAX_REQUEST_RETRIES",
        value_name = "N"
    )]
    proxy_max_request_retries: u32,

    /// Maximum request body bytes buffered for proxy retry replay
    #[arg(
        long,
        default_value_t = DEFAULT_PROXY_MAX_REPLAY_BODY_BYTES,
        env = "STARGATE_PROXY_MAX_REPLAY_BODY_BYTES",
        value_name = "BYTES"
    )]
    proxy_max_replay_body_bytes: usize,

    /// Require pylon's explicit retry signal before retrying upstream status responses
    #[arg(
        long,
        action = clap::ArgAction::Set,
        default_value_t = true,
        env = "STARGATE_PROXY_REQUIRE_PYLON_RETRY_SIGNAL"
    )]
    proxy_require_pylon_retry_signal: bool,

    /// Request header carrying the retry budget in milliseconds; empty disables budget headers
    #[arg(
        long,
        default_value = "x-stargate-max-wait-ms",
        env = "STARGATE_PROXY_RETRY_BUDGET_HEADER",
        value_name = "HEADER"
    )]
    proxy_retry_budget_header: String,

    /// TLS certificate PEM for QUIC listeners. Generates self-signed if omitted.
    #[arg(long, env = "STARGATE_TLS_CERT_PATH", value_name = "PATH")]
    tls_cert_path: Option<String>,

    /// TLS private key PEM for QUIC listeners. Generates self-signed if omitted.
    #[arg(long, env = "STARGATE_TLS_KEY_PATH", value_name = "PATH")]
    tls_key_path: Option<String>,

    /// Skip QUIC TLS certificate verification for outbound connections and relays.
    #[arg(long, default_value_t = false, env = "STARGATE_QUIC_INSECURE")]
    quic_insecure: bool,

    /// Path to load balancer config JSON file (uses power-of-two default if omitted)
    #[arg(long, value_name = "PATH")]
    lb_config_path: Option<String>,

    /// OTLP/gRPC trace export endpoint. Tracing export is disabled if omitted.
    #[arg(long, value_name = "ENDPOINT")]
    otel_endpoint: Option<String>,

    /// OpenTelemetry service.name resource and tracer name.
    #[arg(long, default_value = stargate::telemetry::DEFAULT_SERVICE_NAME, value_name = "NAME")]
    otel_service_name: String,

    /// Port for Prometheus metrics HTTP server
    #[arg(long, default_value_t = 9090, value_name = "PORT")]
    metrics_port: u16,

    /// Prefix prepended to all Prometheus metric names.
    #[arg(long, default_value = stargate::metrics::DEFAULT_PREFIX, value_name = "PREFIX")]
    metrics_prefix: String,

    /// Local UDP socket for reverse QUIC tunnel connections from pylons.
    #[arg(long, value_name = "ADDR")]
    reverse_tunnel_listen_addr: Option<String>,

    /// Optional pylon dial address for reverse QUIC tunnels.
    ///
    /// Stargate still sends the per-pod reverse tunnel target as QUIC SNI
    /// identity, and sends this address separately so pylons can connect through
    /// a UDP load balancer.
    #[arg(long, value_name = "ADDR")]
    reverse_tunnel_pylon_dial_addr: Option<String>,

    /// Timeout waiting for a reverse tunnel connection after registration.
    #[arg(long, default_value_t = 10000, value_name = "MS")]
    reverse_tunnel_connect_timeout_ms: u64,

    /// Tunnel protocol used for proxied request streams; must match pylon.
    #[arg(long, default_value_t = TunnelTransportProtocol::Custom, value_name = "PROTOCOL")]
    tunnel_protocol: TunnelTransportProtocol,

    /// gRPC endpoint for worker authentication (e.g. http://llm-gateway:50051)
    #[arg(long, value_name = "URL")]
    worker_auth_endpoint: Option<String>,

    /// JSON secrets file path for worker-auth bearer tokens.
    #[arg(long, env = "SECRETS_PATH", value_name = "PATH")]
    secrets_path: Option<String>,

    /// Dot-separated JSON path to the auth token inside the secrets file.
    #[arg(long, env = "SECRETS_JSON_PATH", value_name = "PATH")]
    secrets_json_path: Option<String>,
}

struct DiscoveryAndForwarding {
    discovery: Box<dyn Discovery>,
    forwarding: Option<std::sync::Arc<dyn ForwardingResolver>>,
}

async fn make_discovery(args: &Args) -> Result<DiscoveryAndForwarding> {
    let http_listen_addr: SocketAddr = args.http_listen_addr.parse()?;
    let http_port = http_listen_addr.port();

    if args.disable_dns_discovery {
        return Ok(DiscoveryAndForwarding {
            discovery: Box::new(SelfOnlyDiscovery::new(
                args.advertise_addr,
                args.stargate_id.clone(),
                http_port,
            )) as Box<dyn Discovery>,
            forwarding: None,
        });
    }

    if let (Some(pod_name), Some(pod_namespace)) = (&args.pod_name, &args.pod_namespace) {
        let dns_resolver_ttl = Duration::from_millis(args.dns_resolver_ttl_ms);
        let resolver = make_resolver(dns_resolver_ttl)?;
        let template = args
            .advertised_hostname_template
            .clone()
            .unwrap_or_else(|| "{pod_name}.stargate.external".to_string());
        let discovery = Box::new(HeadlessDnsDiscovery::new(HeadlessDnsDiscoveryConfig {
            self_pod_name: pod_name.clone(),
            pod_namespace: pod_namespace.clone(),
            advertised_hostname_template: template.clone(),
            discovery_dns_name: args.stargate_discovery_dns_name.clone(),
            resolver,
            grpc_port: args.advertise_addr.port(),
        })) as Box<dyn Discovery>;
        let forwarding = std::sync::Arc::new(HeadlessDnsResolver {
            self_pod_name: pod_name.clone(),
            advertised_hostname_template: template,
            namespace: pod_namespace.clone(),
            headless_dns_suffix: args.stargate_discovery_dns_name.clone(),
        }) as std::sync::Arc<dyn ForwardingResolver>;
        Ok(DiscoveryAndForwarding {
            discovery,
            forwarding: Some(forwarding),
        })
    } else {
        let dns_resolver_ttl = Duration::from_millis(args.dns_resolver_ttl_ms);
        let resolver = make_resolver(dns_resolver_ttl)?;
        Ok(DiscoveryAndForwarding {
            discovery: Box::new(DnsDiscovery::new(
                args.advertise_addr,
                args.stargate_id.clone(),
                args.stargate_discovery_dns_name.clone(),
                resolver,
                http_port,
            )) as Box<dyn Discovery>,
            forwarding: None,
        })
    }
}

fn proxy_retry_config_from_args(args: &Args) -> Result<ProxyRetryConfig> {
    let request_retry_budget_ms_header = match args.proxy_retry_budget_header.trim() {
        "" => None,
        header => Some(
            http::HeaderName::from_bytes(header.as_bytes())
                .with_context(|| format!("invalid proxy retry budget header: {header}"))?,
        ),
    };
    Ok(ProxyRetryConfig {
        max_connect_retries: args.proxy_max_connect_retries,
        max_request_retries: args.proxy_max_request_retries,
        max_replay_body_bytes: args.proxy_max_replay_body_bytes,
        require_pylon_retry_signal: args.proxy_require_pylon_retry_signal,
        request_retry_budget_ms_header,
        ..ProxyRetryConfig::default()
    })
}

fn make_resolver(ttl: Duration) -> Result<hickory_resolver::TokioAsyncResolver> {
    let (config, mut options) = hickory_resolver::system_conf::read_system_conf()
        .context("failed to read system resolver config")?;
    options.timeout = Duration::from_secs(1);
    options.attempts = 1;
    options.negative_max_ttl = Some(Duration::from_secs(0));
    options.positive_max_ttl = Some(ttl);
    Ok(hickory_resolver::TokioAsyncResolver::tokio(config, options))
}

#[tokio::main]
async fn main() -> Result<()> {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

    let args = <Args as clap::Parser>::parse();
    run(args).await
}

async fn run(args: Args) -> Result<()> {
    let _telemetry_guard = stargate::telemetry::init_telemetry(
        args.otel_endpoint.as_deref(),
        &args.otel_service_name,
    )?;
    let listen_addr: SocketAddr = args.listen_addr.parse()?;
    let model_discovery_listen_addr: SocketAddr = args.model_discovery_listen_addr.parse()?;
    let http_listen_addr: SocketAddr = args.http_listen_addr.parse()?;
    info!(
        stargate_id = %args.stargate_id,
        listen_addr = %args.listen_addr,
        model_discovery_listen_addr = %args.model_discovery_listen_addr,
        http_listen_addr = %args.http_listen_addr,
        advertise_addr = %args.advertise_addr,
        discovery_dns_name = %args.stargate_discovery_dns_name,
        remote_stargate_urls = ?args.remote_stargate_url,
        grpc_pylon_dial_addr = ?args.grpc_pylon_dial_addr,
        advertised_hostname_template = ?args.advertised_hostname_template,
        disable_dns_discovery = args.disable_dns_discovery,
        dns_poll_ms = args.dns_poll_ms,
        dns_resolver_ttl_ms = args.dns_resolver_ttl_ms,
        watch_heartbeat_ms = args.watch_heartbeat_ms,
        registration_update_idle_timeout_ms = args.registration_update_idle_timeout_ms,
        registration_update_max_idle_timeout_ms = args.registration_update_max_idle_timeout_ms,
        shutdown_drain_timeout_ms = args.shutdown_drain_timeout_ms,
        quic_connect_timeout_ms = args.quic_connect_timeout_ms,
        quic_request_timeout_ms = args.quic_request_timeout_ms,
        direct_quic_connections = args.direct_quic_connections,
        otel_service_name = %args.otel_service_name,
        metrics_prefix = %args.metrics_prefix,
        reverse_tunnel_pylon_dial_addr = ?args.reverse_tunnel_pylon_dial_addr,
        "starting stargate"
    );

    let tls_cert_pem = args.tls_cert_path.as_ref().map(std::fs::read).transpose()?;
    let tls_key_pem = args.tls_key_path.as_ref().map(std::fs::read).transpose()?;
    let reverse_tunnel_listen_addr: Option<SocketAddr> = args
        .reverse_tunnel_listen_addr
        .as_deref()
        .map(|s| s.parse())
        .transpose()?;
    let server_tls_identity = server_tls_identity_for_reverse_listener(
        reverse_tunnel_listen_addr.is_some(),
        tls_cert_pem.clone(),
        tls_key_pem,
    )?;

    let DiscoveryAndForwarding {
        discovery,
        forwarding,
    } = make_discovery(&args).await?;
    let proxy_retry_config = proxy_retry_config_from_args(&args)?;

    let mut runtime = StargateRuntime::new(
        StargateRuntimeConfig {
            stargate_id: args.stargate_id,
            grpc_listen_addr: listen_addr,
            model_discovery_listen_addr,
            http_listen_addr,
            advertise_addr: args.advertise_addr,
            stargate_discovery_dns_name: args.stargate_discovery_dns_name,
            remote_watch_stargate_urls: args.remote_stargate_url,
            grpc_pylon_dial_addr: args.grpc_pylon_dial_addr,
            advertised_hostname_template: args.advertised_hostname_template,
            pod_name: args.pod_name,
            pod_namespace: args.pod_namespace,
            dns_poll_interval: std::time::Duration::from_millis(args.dns_poll_ms),
            watch_heartbeat_interval: std::time::Duration::from_millis(args.watch_heartbeat_ms),
            registration_update_idle_timeout: std::time::Duration::from_millis(
                args.registration_update_idle_timeout_ms,
            ),
            registration_update_max_idle_timeout: std::time::Duration::from_millis(
                args.registration_update_max_idle_timeout_ms,
            ),
            proxy_transport: ProxyTransportConfig {
                quic_connect_timeout: std::time::Duration::from_millis(
                    args.quic_connect_timeout_ms,
                ),
                quic_request_timeout: std::time::Duration::from_millis(
                    args.quic_request_timeout_ms,
                ),
                tls_cert_pem,
                server_tls_identity,
                quic_insecure: args.quic_insecure,
                tunnel_protocol: args.tunnel_protocol,
                direct_quic_connections: args.direct_quic_connections,
                retry: proxy_retry_config,
            },
            lb_config_path: args.lb_config_path,
            metrics_prefix: args.metrics_prefix,
            reverse_tunnel_listen_addr,
            reverse_tunnel_pylon_dial_addr: args.reverse_tunnel_pylon_dial_addr,
            reverse_tunnel_connect_timeout: std::time::Duration::from_millis(
                args.reverse_tunnel_connect_timeout_ms,
            ),
        },
        discovery,
    );
    if let Some(fwd) = forwarding {
        runtime = runtime.with_forwarding(fwd);
    }
    if let Some(auth_endpoint) = args.worker_auth_endpoint {
        let token_provider = args.secrets_path.map(|p| {
            // parse a JSON path
            let key: Vec<String> = args
                .secrets_json_path
                .unwrap_or_else(|| "authToken".to_string())
                .split('.')
                .map(String::from)
                .collect();
            stargate_auth::AuthTokenProvider::JsonFile {
                path: std::path::PathBuf::from(p),
                key,
            }
        });
        let authenticator =
            stargate::auth::GrpcWorkerAuthenticator::connect(&auth_endpoint, token_provider)
                .await
                .context("failed to connect to worker auth endpoint")?;
        runtime = runtime.with_authenticator(std::sync::Arc::new(authenticator));
    }

    let handle = runtime.start().await?;

    let metrics_addr: SocketAddr = format!("0.0.0.0:{}", args.metrics_port).parse()?;
    let metrics_registry = handle.metrics().registry();
    tokio::spawn(async move {
        if let Err(e) =
            stargate::metrics::start_metrics_server(metrics_addr, metrics_registry).await
        {
            tracing::error!(error = %e, "metrics server failed");
        }
    });

    let first_signal = wait_for_termination_signal().await;
    info!(
        signal = first_signal,
        "received termination signal, beginning graceful shutdown"
    );
    handle.begin_shutdown();

    let drain_timeout = std::time::Duration::from_millis(args.shutdown_drain_timeout_ms);
    tokio::select! {
        completed = handle.wait_for_shutdown(drain_timeout) => {
            if completed {
                info!("graceful shutdown complete");
            } else {
                info!(timeout_ms = args.shutdown_drain_timeout_ms, "graceful shutdown timed out; forcing exit");
                std::process::exit(1);
            }
        }
        second_signal = wait_for_termination_signal() => {
            info!(signal = second_signal, "received second termination signal; forcing immediate exit");
            std::process::exit(1);
        }
    };

    info!("stargate stopped cleanly");
    Ok(())
}

fn server_tls_identity_for_reverse_listener(
    reverse_tunnel_enabled: bool,
    cert_pem: Option<Vec<u8>>,
    key_pem: Option<Vec<u8>>,
) -> Result<ServerTlsIdentity, anyhow::Error> {
    if reverse_tunnel_enabled {
        return ServerTlsIdentity::from_optional_pem(cert_pem, key_pem);
    }
    Ok(ServerTlsIdentity::SelfSigned)
}

async fn wait_for_termination_signal() -> &'static str {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        let mut sigterm =
            signal(SignalKind::terminate()).expect("failed to install SIGTERM handler");
        tokio::select! {
            _ = tokio::signal::ctrl_c() => "SIGINT",
            _ = sigterm.recv() => "SIGTERM",
        }
    }

    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
        "CTRL_C"
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn try_parse_args(extra: &[&str]) -> std::result::Result<Args, clap::Error> {
        let mut args = vec![
            "stargate",
            "--stargate-id",
            "sg-test",
            "--advertise-addr",
            "127.0.0.1:50071",
            "--stargate-discovery-dns-name",
            "stargate.local",
        ];
        args.extend_from_slice(extra);
        <Args as clap::Parser>::try_parse_from(args)
    }

    fn parse_args(extra: &[&str]) -> Args {
        try_parse_args(extra).expect("args should parse")
    }

    #[test]
    fn dns_poll_ms_zero_is_rejected() {
        let error =
            try_parse_args(&["--dns-poll-ms", "0"]).expect_err("zero dns poll should be rejected");

        assert!(
            error.to_string().contains("greater than 0"),
            "unexpected clap error: {error}"
        );
    }

    #[test]
    fn proxy_retry_cli_defaults_match_runtime_defaults() {
        let args = parse_args(&[]);
        let retry = proxy_retry_config_from_args(&args).expect("retry config should parse");
        let defaults = ProxyRetryConfig::default();

        assert_eq!(retry.max_connect_retries, defaults.max_connect_retries);
        assert_eq!(retry.max_request_retries, defaults.max_request_retries);
        assert_eq!(retry.max_replay_body_bytes, defaults.max_replay_body_bytes);
        assert_eq!(
            retry.require_pylon_retry_signal,
            defaults.require_pylon_retry_signal
        );
        assert_eq!(
            retry.request_retry_budget_ms_header,
            defaults.request_retry_budget_ms_header
        );
    }

    #[test]
    fn registration_update_idle_timeout_default_matches_runtime_default() {
        let args = parse_args(&[]);

        assert_eq!(
            Duration::from_millis(args.registration_update_idle_timeout_ms),
            DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT
        );
        assert_eq!(
            Duration::from_millis(args.registration_update_max_idle_timeout_ms),
            DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT
        );
    }

    #[test]
    fn direct_quic_connections_default_and_override_parse() {
        let defaults = parse_args(&[]);
        assert_eq!(defaults.direct_quic_connections, 1);

        let overridden = parse_args(&["--direct-quic-connections", "4"]);
        assert_eq!(overridden.direct_quic_connections, 4);
    }

    #[test]
    fn direct_quic_connections_zero_is_rejected() {
        let error = try_parse_args(&["--direct-quic-connections", "0"])
            .expect_err("zero direct QUIC connections should be rejected");

        assert!(
            error.to_string().contains("greater than 0"),
            "unexpected clap error: {error}"
        );
    }

    #[test]
    fn model_discovery_listen_addr_default_and_override_parse() {
        let defaults = parse_args(&[]);
        assert_eq!(defaults.model_discovery_listen_addr, "0.0.0.0:50073");

        let overridden = parse_args(&["--model-discovery-listen-addr", "127.0.0.1:50173"]);
        assert_eq!(overridden.model_discovery_listen_addr, "127.0.0.1:50173");
    }

    #[test]
    fn observability_names_default_and_override_parse() {
        let defaults = parse_args(&[]);
        assert_eq!(
            defaults.otel_service_name,
            stargate::telemetry::DEFAULT_SERVICE_NAME
        );
        assert_eq!(defaults.metrics_prefix, stargate::metrics::DEFAULT_PREFIX);

        let overridden = parse_args(&[
            "--otel-service-name",
            "llm-request-router",
            "--metrics-prefix",
            "llm_request_router_",
        ]);
        assert_eq!(overridden.otel_service_name, "llm-request-router");
        assert_eq!(overridden.metrics_prefix, "llm_request_router_");
    }

    #[test]
    fn otel_endpoint_help_matches_grpc_exporter_transport() {
        let mut command = <Args as clap::CommandFactory>::command();
        let mut help = Vec::new();
        command
            .write_long_help(&mut help)
            .expect("help should render");
        let help = std::str::from_utf8(&help).expect("help should be UTF-8");

        assert!(help.contains("OTLP/gRPC trace export endpoint"));
        assert!(!help.contains("OTLP/HTTP/protobuf trace export endpoint"));
    }

    #[test]
    fn direct_quic_tls_trust_cert_does_not_require_server_key() {
        let identity =
            server_tls_identity_for_reverse_listener(false, Some(b"cert".to_vec()), None)
                .expect("cert-only direct trust should not require a server key");

        assert_eq!(identity, ServerTlsIdentity::SelfSigned);
    }

    #[test]
    fn reverse_listener_tls_cert_still_requires_server_key() {
        let err = server_tls_identity_for_reverse_listener(true, Some(b"cert".to_vec()), None)
            .expect_err("reverse listener server TLS still needs a complete PEM pair");

        assert!(
            err.to_string().contains("TLS key PEM is required"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn registration_update_idle_timeout_cli_override_is_applied() {
        let args = parse_args(&[
            "--registration-update-idle-timeout-ms",
            "120000",
            "--registration-update-max-idle-timeout-ms",
            "600000",
        ]);

        assert_eq!(args.registration_update_idle_timeout_ms, 120_000);
        assert_eq!(args.registration_update_max_idle_timeout_ms, 600_000);
    }

    #[test]
    fn registration_update_idle_timeout_zero_disables_enforcement() {
        let args = parse_args(&[
            "--registration-update-idle-timeout-ms",
            "0",
            "--registration-update-max-idle-timeout-ms",
            "0",
        ]);

        assert_eq!(args.registration_update_idle_timeout_ms, 0);
        assert_eq!(args.registration_update_max_idle_timeout_ms, 0);
    }

    #[test]
    fn proxy_retry_cli_overrides_are_applied() {
        let args = parse_args(&[
            "--proxy-max-connect-retries",
            "7",
            "--proxy-max-request-retries",
            "9",
            "--proxy-max-replay-body-bytes",
            "12345",
            "--proxy-require-pylon-retry-signal=false",
            "--proxy-retry-budget-header",
            "x-test-budget-ms",
        ]);
        let retry = proxy_retry_config_from_args(&args).expect("retry config should parse");

        assert_eq!(retry.max_connect_retries, 7);
        assert_eq!(retry.max_request_retries, 9);
        assert_eq!(retry.max_replay_body_bytes, 12345);
        assert!(!retry.require_pylon_retry_signal);
        assert_eq!(
            retry.request_retry_budget_ms_header,
            Some(http::HeaderName::from_static("x-test-budget-ms"))
        );
    }

    #[test]
    fn empty_proxy_retry_budget_header_disables_budget_header() {
        let args = parse_args(&["--proxy-retry-budget-header", ""]);
        let retry = proxy_retry_config_from_args(&args).expect("retry config should parse");

        assert_eq!(retry.request_retry_budget_ms_header, None);
    }

    #[test]
    fn tunnel_protocol_cli_defaults_to_custom() {
        let args = parse_args(&[]);

        assert_eq!(args.tunnel_protocol, TunnelTransportProtocol::Custom);
    }

    #[test]
    fn tunnel_protocol_cli_accepts_http3() {
        let args = parse_args(&["--tunnel-protocol", "http3"]);

        assert_eq!(args.tunnel_protocol, TunnelTransportProtocol::Http3);
    }

    #[test]
    fn tunnel_protocol_cli_accepts_webtransport() {
        let args = parse_args(&["--tunnel-protocol", "webtransport"]);

        assert_eq!(args.tunnel_protocol, TunnelTransportProtocol::WebTransport);
    }

    #[test]
    fn reverse_tunnel_pylon_dial_addr_cli_is_optional_and_parseable() {
        let defaults = parse_args(&[]);
        assert_eq!(defaults.reverse_tunnel_pylon_dial_addr, None);
        assert_eq!(defaults.grpc_pylon_dial_addr, None);

        let args = parse_args(&[
            "--grpc-pylon-dial-addr",
            "stargate-grpc-lb.stargate.svc.cluster.local:443",
            "--reverse-tunnel-listen-addr",
            "0.0.0.0:50072",
            "--reverse-tunnel-pylon-dial-addr",
            "stargate-quic-lb.stargate.svc.cluster.local:50072",
        ]);

        assert_eq!(
            args.reverse_tunnel_pylon_dial_addr.as_deref(),
            Some("stargate-quic-lb.stargate.svc.cluster.local:50072")
        );
        assert_eq!(
            args.grpc_pylon_dial_addr.as_deref(),
            Some("stargate-grpc-lb.stargate.svc.cluster.local:443")
        );
    }
}
