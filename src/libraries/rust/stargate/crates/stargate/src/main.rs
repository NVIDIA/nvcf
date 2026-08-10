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
use std::net::SocketAddr;

use anyhow::{Context, Result};
use stargate::registration::{
    DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT, DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
};
use stargate_protocol::{BackendConnectivity, TunnelTransportProtocol};
use stargate_runtime::wait_for_termination_signal;
use tracing::{error, info, warn};

#[path = "main/startup.rs"]
mod startup;

use startup::runtime_from_args;

mod built_info {
    include!(concat!(env!("OUT_DIR"), "/built.rs"));
}

const DEFAULT_PROXY_MAX_REPLAY_BODY_BYTES: usize = 64 * 1024 * 1024;

fn parse_nonzero_millis(value: &str) -> std::result::Result<u64, String> {
    let millis = value
        .parse::<u64>()
        .map_err(|err| format!("invalid millisecond value: {err}"))?;
    (millis > 0)
        .then_some(millis)
        .ok_or_else(|| "value must be greater than 0".to_string())
}

fn parse_nonzero_usize(value: &str) -> std::result::Result<usize, String> {
    let count = value
        .parse::<usize>()
        .map_err(|err| format!("invalid count: {err}"))?;
    (count > 0)
        .then_some(count)
        .ok_or_else(|| "value must be greater than 0".to_string())
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
    /// Self gRPC address published by non-Kubernetes discovery and used as the port source for Kubernetes advertised hostnames.
    #[arg(long, value_name = "ADDR")]
    advertise_addr: SocketAddr,
    /// DNS name used for Stargate peer discovery. In Kubernetes this should be the headless Service so EndpointSlice readiness controls peer visibility and development-only relay targets.
    #[arg(long, value_name = "DNS_NAME")]
    stargate_discovery_dns_name: String,
    /// Additional recursive WatchStargates seeds for remote regions. Pylons register only to concrete `stargates` entries returned by watch snapshots. Repeatable.
    #[arg(
        long,
        env = "STARGATE_REMOTE_WATCH_URLS",
        value_delimiter = ',',
        value_name = "URL"
    )]
    remote_stargate_url: Vec<String>,
    /// Optional TCP load-balancer dial address for pylons; per-pod addresses remain the advertised gRPC authority/SNI identity.
    #[arg(long, value_name = "ADDR")]
    grpc_pylon_dial_addr: Option<String>,
    /// Backend hostname template supporting `{pod_name}` and `{namespace}`; its rendered host is the pylon gRPC authority and reverse QUIC SNI.
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
    /// Enable development-only peer relaying; requires Kubernetes identity and DNS discovery. Production must use `stargate-k8s-router` or a supported load balancer.
    #[arg(long, default_value_t = false)]
    enable_dev_peer_forwarding: bool,
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
    /// Timeout for establishing outbound direct QUIC connections and development-only peer relays.
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
    #[arg(long, env = "OTEL_EXPORTER_OTLP_ENDPOINT", value_name = "ENDPOINT")]
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
    /// Tunnel connection direction: direct makes Stargate dial pylon; reverse makes pylon dial Stargate.
    #[arg(long, default_value_t = BackendConnectivity::Direct, value_name = "MODE")]
    backend_connectivity: BackendConnectivity,
    /// Local UDP socket for reverse QUIC tunnel connections from pylons.
    #[arg(long, value_name = "ADDR")]
    reverse_tunnel_listen_addr: Option<String>,
    /// Optional UDP load-balancer dial address for pylons; the per-pod reverse target remains the QUIC SNI identity.
    #[arg(long, value_name = "ADDR")]
    reverse_tunnel_pylon_dial_addr: Option<String>,
    /// Timeout waiting for a reverse tunnel connection after registration.
    #[arg(long, default_value_t = 10000, value_name = "MS")]
    reverse_tunnel_connect_timeout_ms: u64,
    /// Tunnel protocol used for proxied request streams; must match pylon.
    #[arg(long, default_value_t = TunnelTransportProtocol::RawQuic, value_name = "PROTOCOL")]
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
    /// OAuth2 host for minting worker-auth tokens at `<host>/token` with the secrets-file id/secret instead of a static bearer token.
    #[arg(long, env = "OAUTH2_PROVIDER_HOST", value_name = "URL")]
    oauth2_provider_host: Option<String>,
}

#[tokio::main]
async fn main() -> Result<()> {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

    run(clap::Parser::parse()).await
}

/// Resolves the OTLP tracing access token, read only when tracing is enabled.
/// Missing/empty key yields `None` (caller warns after the subscriber exists);
/// an unreadable or malformed secrets file is a hard error.
async fn resolve_otel_access_token(
    tracing_enabled: bool,
    secrets_path: Option<&str>,
) -> Result<Option<String>> {
    if !tracing_enabled {
        return Ok(None);
    }
    let Some(path) = secrets_path else {
        return Ok(None);
    };
    let bytes = tokio::fs::read(path)
        .await
        .with_context(|| format!("failed to read secrets file '{path}' for tracingAccessToken"))?;
    let secrets: serde_json::Value = serde_json::from_slice(&bytes)
        .with_context(|| format!("secrets file '{path}' is not valid JSON"))?;
    match secrets.get("tracingAccessToken") {
        None => Ok(None),
        Some(value) => {
            let token = value
                .as_str()
                .context("tracingAccessToken in secrets file is not a string")?
                .trim();
            if token.is_empty() {
                Ok(None)
            } else {
                Ok(Some(token.to_owned()))
            }
        }
    }
}

async fn run(args: Args) -> Result<()> {
    let tracing_enabled = args.otel_endpoint.is_some();
    let otel_access_token =
        resolve_otel_access_token(tracing_enabled, args.secrets_path.as_deref()).await?;
    let _telemetry_guard = stargate::telemetry::init_telemetry(
        args.otel_endpoint.as_deref(),
        &args.otel_service_name,
        otel_access_token.as_deref(),
    )?;
    // Warn after init_telemetry so the subscriber captures it.
    if tracing_enabled && otel_access_token.is_none() {
        warn!("no tracingAccessToken; OTLP trace export is unauthenticated");
    }
    log_startup(&args);

    let startup = runtime_from_args(args).await?;
    let handle = startup.runtime.start().await?;

    let shutdown_error = match wait_for_runtime_shutdown_trigger(
        wait_for_termination_signal(),
        handle.wait_for_critical_failure(),
    )
    .await
    {
        RuntimeShutdownTrigger::Signal(Ok(first_signal)) => {
            info!(
                signal = first_signal,
                "received termination signal, beginning graceful shutdown"
            );
            None
        }
        RuntimeShutdownTrigger::Signal(Err(error)) => {
            let error =
                anyhow::Error::new(error).context("failed to receive stargate termination signal");
            error!(error = %error, "termination signal handler failed, beginning graceful shutdown");
            Some(error)
        }
        RuntimeShutdownTrigger::Critical(failure) => {
            error!(error = %failure, "critical stargate task failed, beginning graceful shutdown");
            Some(failure.into())
        }
    };
    handle.begin_shutdown();

    tokio::select! {
        completed = handle.wait_for_shutdown(startup.shutdown_drain_timeout) => {
            if completed {
                info!("graceful shutdown complete");
            } else {
                info!(timeout_ms = startup.shutdown_drain_timeout.as_millis(), "graceful shutdown timed out; forcing exit");
                std::process::exit(1);
            }
        }
        second_signal = wait_for_termination_signal() => {
            match second_signal {
                Ok(signal) => info!(signal, "received second termination signal; forcing immediate exit"),
                Err(error) => error!(error = %error, "termination signal handler failed during shutdown; forcing immediate exit"),
            }
            std::process::exit(1);
        }
    };

    if let Some(error) = shutdown_error {
        info!("stargate shutdown complete after process failure");
        Err(error)
    } else {
        info!("stargate stopped cleanly");
        Ok(())
    }
}

fn log_startup(args: &Args) {
    info!(
        version = built_info::PKG_VERSION,
        commit_short_sha = built_info::GIT_COMMIT_HASH_SHORT.unwrap_or("unknown"),
        config = ?args,
        "starting stargate"
    );
}

enum RuntimeShutdownTrigger<Signal, Failure> {
    Signal(Signal),
    Critical(Failure),
}

async fn wait_for_runtime_shutdown_trigger<SignalOutput, FailureOutput>(
    signal: impl Future<Output = SignalOutput>,
    failure: impl Future<Output = FailureOutput>,
) -> RuntimeShutdownTrigger<SignalOutput, FailureOutput> {
    tokio::select! {
        signal = signal => RuntimeShutdownTrigger::Signal(signal),
        failure = failure => RuntimeShutdownTrigger::Critical(failure),
    }
}

#[cfg(test)]
mod tests {
    use std::io::{self, Write};
    use std::path::PathBuf;
    use std::process::Command;
    use std::sync::Arc;
    use std::time::Duration;

    use parking_lot::Mutex;
    use stargate::proxy::{ProxyRetryConfig, ProxyTransportConfig};
    use stargate_tls::ServerTlsIdentity;

    use super::startup::{
        DiscoveryAndForwarding, WorkerAuthStartup, bind_reverse_tunnel_from_args,
        make_discovery_with_resolver_and_addresses, make_resolver, proxy_retry_config_from_args,
        proxy_transport_config_from_args, runtime_config_from_args, worker_auth_startup_from_args,
    };
    use super::*;

    #[derive(Clone)]
    struct TestLogWriter(Arc<Mutex<Vec<u8>>>);

    impl Write for TestLogWriter {
        fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
            self.0.lock().extend_from_slice(bytes);
            Ok(bytes.len())
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    fn capture_logs<T>(level: tracing::Level, operation: impl FnOnce() -> T) -> (T, String) {
        let output = Arc::new(Mutex::new(Vec::new()));
        let subscriber = tracing_subscriber::fmt()
            .with_ansi(false)
            .without_time()
            .with_target(false)
            .with_max_level(level)
            .with_writer({
                let output = Arc::clone(&output);
                move || TestLogWriter(Arc::clone(&output))
            })
            .finish();
        let result = tracing::subscriber::with_default(subscriber, operation);
        let logs = String::from_utf8(output.lock().clone()).expect("logs should be UTF-8");
        (result, logs)
    }

    fn try_parse_argv<'a>(
        extra: impl IntoIterator<Item = &'a str>,
    ) -> std::result::Result<Args, clap::Error> {
        const REQUIRED_ARGS: [&str; 7] = [
            "stargate",
            "--stargate-id",
            "sg-test",
            "--advertise-addr",
            "127.0.0.1:50071",
            "--stargate-discovery-dns-name",
            "stargate.local",
        ];
        <Args as clap::Parser>::try_parse_from(REQUIRED_ARGS.into_iter().chain(extra))
    }

    fn try_parse_args(extra: &str) -> std::result::Result<Args, clap::Error> {
        try_parse_argv(extra.split_ascii_whitespace())
    }

    fn parse_args(extra: &str) -> Args {
        try_parse_args(extra).expect("args should parse")
    }

    fn assert_parse_error(args: &str, expected: &str) {
        let error = try_parse_args(args).expect_err("args should be rejected");
        assert_error_contains(&error, expected);
    }

    fn assert_error_contains(error: &impl std::fmt::Display, expected: &str) {
        assert!(
            error.to_string().contains(expected),
            "unexpected error: {error}"
        );
    }

    fn proxy_transport(args: &Args) -> ProxyTransportConfig {
        proxy_transport_config_from_args(args).expect("proxy transport config should parse")
    }

    fn retry_values(retry: &ProxyRetryConfig) -> (u32, u32, usize, bool, Option<&str>) {
        (
            retry.max_connect_retries,
            retry.max_request_retries,
            retry.max_replay_body_bytes,
            retry.require_pylon_retry_signal,
            retry
                .request_retry_budget_ms_header
                .as_ref()
                .map(|name| name.as_str()),
        )
    }

    #[tokio::test]
    async fn runtime_config_owns_default_runtime_dependencies() {
        let args = parse_args("");
        let config = runtime_config_from_args(&args, proxy_transport(&args))
            .expect("runtime config should parse");
        assert!(config.forwarding.is_none());
        assert_eq!(
            config
                .authenticator
                .authenticate(None)
                .await
                .expect("default authenticator should accept anonymous workers")
                .routing_key,
            None
        );
    }

    #[test]
    fn proxy_transport_config_groups_every_quic_setting() {
        let args = parse_args(
            "--quic-connect-timeout-ms 123 --quic-request-timeout-ms 456 \
             --direct-quic-connections 3 --tunnel-protocol http3 --quic-insecure",
        );
        let config = proxy_transport(&args);
        assert_eq!(config.quic.connect_timeout, Duration::from_millis(123));
        assert_eq!(config.quic.request_timeout, Duration::from_millis(456));
        assert_eq!(config.quic.direct_quic_connections, 3);
        assert_eq!(config.quic.tunnel_protocol, TunnelTransportProtocol::Http3);
        assert!(config.quic.quic_insecure);
    }

    fn make_discovery_with_resolver(
        args: &Args,
        make_resolver: impl FnOnce(Duration) -> Result<hickory_resolver::TokioAsyncResolver>,
    ) -> Result<DiscoveryAndForwarding> {
        let http_listen_addr = args.http_listen_addr.parse()?;
        make_discovery_with_resolver_and_addresses(
            args,
            args.advertise_addr,
            http_listen_addr,
            make_resolver,
        )
    }

    fn worker_auth_startup(
        secrets_json_path: Option<&str>,
        oauth2_provider_host: Option<&str>,
    ) -> WorkerAuthStartup {
        worker_auth_startup_from_args(
            Some("http://auth.example.test".to_owned()),
            Some("/var/run/secrets/auth.json".to_owned()),
            secrets_json_path.map(str::to_owned),
            oauth2_provider_host.map(str::to_owned),
        )
        .expect("worker auth args should be valid")
        .expect("auth startup should exist")
    }

    async fn runtime_startup_error(extra: &str) -> anyhow::Error {
        runtime_from_args(parse_args(extra))
            .await
            .err()
            .expect("runtime construction should fail")
    }

    #[test]
    fn startup_log_includes_build_identity_and_complete_args() {
        let args = parse_args(
            "--proxy-max-connect-retries 7 \
             --worker-auth-endpoint http://worker-auth.example.test:50051 \
             --secrets-path /var/run/secrets/worker-auth.json \
             --secrets-json-path auth.token",
        );
        let (_, output) = capture_logs(tracing::Level::INFO, || log_startup(&args));
        let expected_commit = built_info::GIT_COMMIT_HASH_SHORT.unwrap_or("unknown");
        for expected in [
            format!("version=\"{}\"", built_info::PKG_VERSION),
            format!("commit_short_sha=\"{expected_commit}\""),
            format!("config={args:?}"),
        ] {
            assert!(output.contains(&expected), "startup log: {output}");
        }
    }

    #[test]
    fn build_identity_matches_checked_out_commit() {
        let output = Command::new("git")
            .args(["rev-parse", "HEAD"])
            .output()
            .expect("git should be available when running repository tests");
        assert!(
            output.status.success(),
            "git rev-parse failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        let stdout = String::from_utf8(output.stdout).expect("git commit should be UTF-8");
        let expected_commit = stdout.trim();
        assert_eq!(
            built_info::GIT_COMMIT_HASH,
            Some(expected_commit),
            "generated build identity should match the checked-out commit"
        );
    }

    #[test]
    fn dns_poll_ms_zero_is_rejected() {
        assert_parse_error("--dns-poll-ms 0", "greater than 0");
    }

    #[test]
    fn registration_update_idle_timeout_default_matches_runtime_default() {
        let args = parse_args("");
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
        assert_eq!(parse_args("").direct_quic_connections, 1);
        assert_eq!(
            parse_args("--direct-quic-connections 4").direct_quic_connections,
            4
        );
    }

    #[test]
    fn direct_quic_connections_zero_is_rejected() {
        assert_parse_error("--direct-quic-connections 0", "greater than 0");
    }

    #[test]
    fn dev_peer_forwarding_defaults_to_false_and_requires_explicit_opt_in() {
        assert!(!parse_args("").enable_dev_peer_forwarding);
        assert!(parse_args("--enable-dev-peer-forwarding").enable_dev_peer_forwarding);
    }

    #[test]
    fn model_discovery_listen_addr_default_and_override_parse() {
        assert_eq!(parse_args("").model_discovery_listen_addr, "0.0.0.0:50073");
        let overridden = parse_args("--model-discovery-listen-addr 127.0.0.1:50173");
        assert_eq!(overridden.model_discovery_listen_addr, "127.0.0.1:50173");
    }

    #[test]
    fn observability_names_default_and_override_parse() {
        let defaults = parse_args("");
        assert_eq!(
            defaults.otel_service_name,
            stargate::telemetry::DEFAULT_SERVICE_NAME
        );
        assert_eq!(defaults.metrics_prefix, stargate::metrics::DEFAULT_PREFIX);
        let overridden = parse_args(
            "--otel-service-name llm-request-router --metrics-prefix llm_request_router_",
        );
        assert_eq!(overridden.otel_service_name, "llm-request-router");
        assert_eq!(overridden.metrics_prefix, "llm_request_router_");
    }

    #[tokio::test]
    async fn runtime_shutdown_trigger_reports_signal() {
        let trigger = wait_for_runtime_shutdown_trigger(
            async { "SIGTERM" },
            std::future::pending::<&'static str>(),
        )
        .await;
        assert!(matches!(trigger, RuntimeShutdownTrigger::Signal("SIGTERM")));
    }

    #[tokio::test]
    async fn runtime_shutdown_trigger_reports_critical_failure() {
        let trigger =
            wait_for_runtime_shutdown_trigger(std::future::pending::<&'static str>(), async {
                "HTTP proxy server"
            })
            .await;
        assert!(matches!(
            trigger,
            RuntimeShutdownTrigger::Critical("HTTP proxy server")
        ));
    }

    #[test]
    fn registration_update_idle_timeout_cli_override_is_applied() {
        let args = parse_args(
            "--registration-update-idle-timeout-ms 120000 \
             --registration-update-max-idle-timeout-ms 600000",
        );
        assert_eq!(args.registration_update_idle_timeout_ms, 120_000);
        assert_eq!(args.registration_update_max_idle_timeout_ms, 600_000);
    }

    #[test]
    fn registration_update_idle_timeout_zero_disables_enforcement() {
        let args = parse_args(
            "--registration-update-idle-timeout-ms 0 \
             --registration-update-max-idle-timeout-ms 0",
        );
        assert_eq!(args.registration_update_idle_timeout_ms, 0);
        assert_eq!(args.registration_update_max_idle_timeout_ms, 0);
    }

    #[test]
    fn tunnel_protocol_cli_defaults_to_raw_quic() {
        assert_eq!(
            parse_args("").tunnel_protocol,
            TunnelTransportProtocol::RawQuic
        );
    }

    #[tokio::test]
    async fn direct_connectivity_rejects_reverse_listener_configuration() {
        let error = runtime_startup_error(
            "--backend-connectivity direct --reverse-tunnel-listen-addr 127.0.0.1:0",
        )
        .await;
        assert_error_contains(
            &error,
            "--reverse-tunnel-listen-addr requires --backend-connectivity=reverse",
        );
    }

    #[tokio::test]
    async fn reverse_connectivity_requires_a_reverse_listener() {
        let error = runtime_startup_error("--backend-connectivity reverse").await;
        assert_error_contains(
            &error,
            "--backend-connectivity=reverse requires --reverse-tunnel-listen-addr",
        );
    }

    #[test]
    fn reverse_tunnel_pylon_dial_addr_cli_is_optional_and_parseable() {
        let defaults = parse_args("");
        assert_eq!(defaults.reverse_tunnel_pylon_dial_addr, None);
        assert_eq!(defaults.grpc_pylon_dial_addr, None);
        let args = parse_args(
            "--grpc-pylon-dial-addr stargate-grpc-lb.stargate.svc.cluster.local:443 \
             --reverse-tunnel-listen-addr 0.0.0.0:50072 \
             --reverse-tunnel-pylon-dial-addr stargate-quic-lb.stargate.svc.cluster.local:50072",
        );
        assert_eq!(
            args.reverse_tunnel_pylon_dial_addr.as_deref(),
            Some("stargate-quic-lb.stargate.svc.cluster.local:50072")
        );
        assert_eq!(
            args.grpc_pylon_dial_addr.as_deref(),
            Some("stargate-grpc-lb.stargate.svc.cluster.local:443")
        );
    }
    fn test_resolver(_: Duration) -> Result<hickory_resolver::TokioAsyncResolver> {
        Ok(hickory_resolver::TokioAsyncResolver::tokio(
            Default::default(),
            Default::default(),
        ))
    }

    #[test]
    fn proxy_retry_cli_defaults_match_runtime_defaults() {
        let args = parse_args("");
        let retry = proxy_retry_config_from_args(&args).expect("retry config should parse");
        assert_eq!(
            retry_values(&retry),
            retry_values(&ProxyRetryConfig::default())
        );
    }

    #[test]
    fn proxy_retry_cli_overrides_are_applied() {
        let args = parse_args(
            "--proxy-max-connect-retries 7 --proxy-max-request-retries 9 \
             --proxy-max-replay-body-bytes 12345 \
             --proxy-require-pylon-retry-signal=false \
             --proxy-retry-budget-header x-test-budget-ms",
        );
        let retry = proxy_retry_config_from_args(&args).expect("retry config should parse");
        assert_eq!(
            retry_values(&retry),
            (7, 9, 12345, false, Some("x-test-budget-ms"))
        );
    }

    #[test]
    fn empty_proxy_retry_budget_header_disables_budget_header() {
        let args = try_parse_argv(["--proxy-retry-budget-header", ""])
            .expect("empty retry budget header should parse");
        let retry = proxy_retry_config_from_args(&args).expect("retry config should parse");
        assert_eq!(retry.request_retry_budget_ms_header, None);
    }

    #[test]
    fn direct_quic_tls_trust_cert_does_not_require_server_key() {
        let cert = tempfile::NamedTempFile::new().expect("cert file should be creatable");
        let path = cert.path().to_str().expect("cert path should be UTF-8");
        let args = try_parse_argv(["--tls-cert-path", path]).expect("cert path should parse");
        assert_eq!(
            proxy_transport(&args).quic.server_tls_identity,
            ServerTlsIdentity::SelfSigned
        );
    }

    #[test]
    fn reverse_listener_tls_cert_still_requires_server_key() {
        let cert = tempfile::NamedTempFile::new().expect("cert file should be creatable");
        let path = cert.path().to_str().expect("cert path should be UTF-8");
        let args = try_parse_argv([
            "--reverse-tunnel-listen-addr",
            "127.0.0.1:0",
            "--tls-cert-path",
            path,
        ])
        .expect("reverse listener arguments should parse");
        let err = proxy_transport_config_from_args(&args)
            .expect_err("reverse listener server TLS still needs a complete PEM pair");
        assert_error_contains(&err, "TLS key PEM is required");
    }

    #[test]
    fn main_binds_reverse_tunnel_config_before_runtime_start() {
        let args = parse_args(
            "--reverse-tunnel-listen-addr 127.0.0.1:0 \
             --advertised-hostname-template {pod_name}.stargate-headless.{namespace}.svc.cluster.local \
             --pod-name stargate-3 --pod-namespace inference \
             --reverse-tunnel-pylon-dial-addr stargate-quic-lb.inference.svc.cluster.local:443 \
             --reverse-tunnel-connect-timeout-ms 4321",
        );
        let config = bind_reverse_tunnel_from_args(&args)
            .expect("main should bind reverse tunnel config")
            .expect("listener should enable reverse tunnel config");
        let listen_addr = config.listen_addr();
        assert_ne!(listen_addr.port(), 0, "the OS must select a concrete port");
        assert!(
            std::net::UdpSocket::bind(listen_addr).is_err(),
            "the bound reverse tunnel config must retain its socket"
        );
        assert_eq!(
            config.advertised_host,
            "stargate-3.stargate-headless.inference.svc.cluster.local"
        );
        assert_eq!(
            config.pylon_dial_addr.as_deref(),
            Some("stargate-quic-lb.inference.svc.cluster.local:443")
        );
        assert_eq!(config.connect_timeout, Duration::from_millis(4321));
    }

    #[test]
    fn worker_auth_json_token_provider_uses_configured_key_path() {
        let (endpoint, token_provider) = worker_auth_startup(Some("nested.authToken"), None);
        assert_eq!(endpoint, "http://auth.example.test");
        let Some(stargate_auth::AuthTokenProvider::JsonFile { path, key }) = token_provider else {
            panic!("secrets path should create a JSON-file token provider");
        };
        assert_eq!(path, PathBuf::from("/var/run/secrets/auth.json"));
        assert_eq!(key, vec!["nested".to_string(), "authToken".to_string()]);
    }

    #[test]
    fn worker_auth_json_token_provider_uses_default_key_path() {
        let (_, token_provider) = worker_auth_startup(None, None);
        let Some(stargate_auth::AuthTokenProvider::JsonFile { key, .. }) = token_provider else {
            panic!("secrets path should create a JSON-file token provider");
        };
        assert_eq!(key, vec!["authToken".to_string()]);
    }

    #[test]
    fn worker_auth_uses_client_credentials_when_oauth_host_set() {
        let (_, token_provider) = worker_auth_startup(None, Some("https://oauth.example.test"));
        assert!(matches!(
            token_provider,
            Some(stargate_auth::AuthTokenProvider::ClientCredentials(_))
        ));
    }

    #[test]
    fn worker_auth_errors_when_oauth_host_set_without_secrets() {
        let result = worker_auth_startup_from_args(
            Some("http://auth.example.test".to_string()),
            None,
            None,
            Some("https://oauth.example.test".to_string()),
        );
        assert!(result.is_err());
    }

    #[test]
    fn worker_auth_absent_without_endpoint() {
        let startup = worker_auth_startup_from_args(None, None, None, None)
            .expect("worker auth args should be valid");
        assert!(startup.is_none());
    }

    #[test]
    fn self_only_discovery_uses_proxy_http_port() {
        let args = parse_args("--disable-dns-discovery --http-listen-addr 127.0.0.1:18000");
        let (discovery, forwarding) = make_discovery_with_resolver(&args, make_resolver)
            .expect("self-only discovery should build without DNS");
        assert!(forwarding.is_none());
        let initial = discovery.initial_stargates();
        assert_eq!(initial.len(), 1);
        assert_eq!(initial[0].stargate_id, "sg-test");
        assert_eq!(initial[0].advertise_addr, "127.0.0.1:50071");
        assert_eq!(initial[0].http_advertise_addr, "127.0.0.1:18000");
    }

    #[test]
    fn kubernetes_identity_without_dev_peer_forwarding_has_no_forwarding_resolver() {
        let args = parse_args("--pod-name stargate-0 --pod-namespace inference");
        let (_, forwarding) = make_discovery_with_resolver(&args, test_resolver)
            .expect("headless DNS discovery should build");
        assert!(forwarding.is_none());
    }

    #[test]
    fn explicit_dev_peer_forwarding_attaches_a_forwarding_resolver_and_logs_warning() {
        let args = parse_args(
            "--pod-name stargate-0 --pod-namespace inference --enable-dev-peer-forwarding",
        );
        let (startup, logs) = capture_logs(tracing::Level::WARN, || {
            make_discovery_with_resolver(&args, test_resolver)
        });
        let (_, forwarding) = startup.expect("development peer forwarding should build");
        assert!(forwarding.is_some());
        assert_eq!(
            logs.matches(
                "development-only peer forwarding is enabled; it must not run in production"
            )
            .count(),
            1,
            "expected exactly one development-only warning: {logs}"
        );
        for field in ["development_only=true", "stargate_id=\"sg-test\""] {
            assert!(
                logs.contains(field),
                "warning should include {field}: {logs}"
            );
        }
    }

    #[tokio::test]
    async fn dev_peer_forwarding_without_pod_identity_is_rejected_before_runtime_construction() {
        let error = runtime_startup_error("--enable-dev-peer-forwarding").await;
        assert_error_contains(&error, "requires both --pod-name and --pod-namespace");
    }

    #[tokio::test]
    async fn dev_peer_forwarding_with_disabled_dns_discovery_is_rejected() {
        let error = runtime_startup_error(
            "--enable-dev-peer-forwarding --pod-name stargate-0 \
             --pod-namespace inference --disable-dns-discovery",
        )
        .await;
        assert_error_contains(&error, "cannot be combined with --disable-dns-discovery");
    }

    #[tokio::test]
    async fn runtime_startup_returns_process_owned_shutdown_timeout() {
        let startup = runtime_from_args(parse_args(
            "--disable-dns-discovery --listen-addr 127.0.0.1:0 \
             --model-discovery-listen-addr 127.0.0.1:0 --http-listen-addr 127.0.0.1:0 \
             --metrics-port 0 --shutdown-drain-timeout-ms 1234",
        ))
        .await
        .expect("runtime startup should build without DNS when discovery is disabled");
        assert_eq!(startup.shutdown_drain_timeout, Duration::from_millis(1234));
    }

    #[tokio::test]
    async fn occupied_metrics_port_fails_before_runtime_construction() {
        let blocker =
            std::net::TcpListener::bind("127.0.0.1:0").expect("metrics blocker should bind");
        let metrics_port = blocker
            .local_addr()
            .expect("metrics blocker should have an address")
            .port();
        let error = runtime_startup_error(&format!(
            "--disable-dns-discovery --listen-addr 127.0.0.1:0 \
             --model-discovery-listen-addr 127.0.0.1:0 --http-listen-addr 127.0.0.1:0 \
             --metrics-port {metrics_port}"
        ))
        .await;
        assert_error_contains(&error, "metrics");
    }

    fn write_secrets(contents: &str) -> tempfile::NamedTempFile {
        let mut file = tempfile::NamedTempFile::new().expect("create temp secrets file");
        file.write_all(contents.as_bytes()).expect("write secrets");
        file.flush().expect("flush secrets");
        file
    }

    #[tokio::test]
    async fn otel_access_token_none_when_tracing_disabled() {
        let file = write_secrets(r#"{"tracingAccessToken":"tok"}"#);
        let token = resolve_otel_access_token(false, file.path().to_str())
            .await
            .expect("resolve should succeed");
        assert_eq!(token, None);
    }

    #[tokio::test]
    async fn otel_access_token_none_when_no_secrets_path() {
        let token = resolve_otel_access_token(true, None)
            .await
            .expect("resolve should succeed");
        assert_eq!(token, None);
    }

    #[tokio::test]
    async fn otel_access_token_reads_and_trims_value() {
        let file = write_secrets(r#"{"nvcfApiToken":"x","tracingAccessToken":"  tok-123  "}"#);
        let token = resolve_otel_access_token(true, file.path().to_str())
            .await
            .expect("resolve should succeed");
        assert_eq!(token.as_deref(), Some("tok-123"));
    }

    #[tokio::test]
    async fn otel_access_token_absent_key_is_allowed() {
        let file = write_secrets(r#"{"nvcfApiToken":"x"}"#);
        let token = resolve_otel_access_token(true, file.path().to_str())
            .await
            .expect("missing tracingAccessToken must not error");
        assert_eq!(token, None);
    }

    #[tokio::test]
    async fn otel_access_token_empty_value_is_allowed() {
        let file = write_secrets(r#"{"tracingAccessToken":"   "}"#);
        let token = resolve_otel_access_token(true, file.path().to_str())
            .await
            .expect("empty tracingAccessToken must not error");
        assert_eq!(token, None);
    }

    #[tokio::test]
    async fn otel_access_token_non_string_value_fails() {
        let file = write_secrets(r#"{"tracingAccessToken":42}"#);
        let error = resolve_otel_access_token(true, file.path().to_str())
            .await
            .expect_err("non-string tracingAccessToken must fail");
        assert!(error.to_string().contains("not a string"), "{error:#}");
    }

    #[tokio::test]
    async fn otel_access_token_invalid_json_fails() {
        let file = write_secrets("not json");
        let error = resolve_otel_access_token(true, file.path().to_str())
            .await
            .expect_err("invalid JSON secrets file must fail");
        assert!(error.to_string().contains("not valid JSON"), "{error:#}");
    }

    #[tokio::test]
    async fn otel_access_token_unreadable_file_fails() {
        let error = resolve_otel_access_token(true, Some("/nonexistent/secrets.json"))
            .await
            .expect_err("unreadable secrets file must fail");
        assert!(error.to_string().contains("failed to read"), "{error:#}");
    }
}
