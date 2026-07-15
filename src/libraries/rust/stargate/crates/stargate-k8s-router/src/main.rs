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
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use clap::{Parser, ValueEnum};
use kube::Client;
use stargate_forwarding::RelayEndpointConfig;
use stargate_k8s_router::endpoints::{TargetBuildConfig, TargetSnapshot};
use stargate_k8s_router::grpc::{GrpcRouterConfig, serve_grpc_router};
use stargate_k8s_router::health::serve_health;
use stargate_k8s_router::metrics::RouterMetrics;
use stargate_k8s_router::quic::{QuicRouterConfig, serve_quic_router};
use stargate_k8s_router::watcher::run_endpoint_slice_watcher;
use stargate_k8s_router::webtransport::{WebTransportRouterConfig, serve_webtransport_router};
use stargate_runtime::{
    CriticalTaskFailureReceiver, CriticalTaskGroup, wait_for_termination_signal,
};
use tokio::net::TcpListener;
use tokio::sync::watch;
use tracing::{debug, error, info};
use tracing_subscriber::EnvFilter;

const DEFAULT_CONNECT_TIMEOUT_MS: u64 = 5_000;
const DEFAULT_RELAY_MAX_IDLE_TIMEOUT_MS: u64 = 300_000;
const DEFAULT_RELAY_KEEP_ALIVE_MS: u64 = 10_000;

#[derive(Clone, Debug, PartialEq, ValueEnum)]
enum RouterTunnelProtocol {
    #[value(name = "raw-quic")]
    RawQuic,
    #[value(name = "webtransport")]
    WebTransport,
}

#[derive(Parser)]
#[command(name = "stargate-k8s-router")]
struct Args {
    #[arg(long, default_value = "0.0.0.0:50071", value_name = "ADDR")]
    listen_addr: SocketAddr,
    #[arg(long, default_value = "0.0.0.0:50072", value_name = "ADDR")]
    reverse_tunnel_listen_addr: SocketAddr,
    #[arg(long, default_value = "0.0.0.0:8080", value_name = "ADDR")]
    health_listen_addr: SocketAddr,
    #[arg(long, env = "POD_NAMESPACE", value_name = "NAMESPACE")]
    target_namespace: String,
    #[arg(long, default_value = "stargate-headless", value_name = "NAME")]
    target_service_name: String,
    #[arg(
        long,
        default_value = "{pod_name}.stargate.external",
        value_name = "TEMPLATE"
    )]
    advertised_hostname_template: String,
    #[arg(long, default_value = "grpc", value_name = "NAME")]
    grpc_port_name: String,
    #[arg(long, default_value = "quic", value_name = "NAME")]
    quic_port_name: String,
    #[arg(long, default_value_t = DEFAULT_CONNECT_TIMEOUT_MS, value_name = "MS")]
    connect_timeout_ms: u64,
    #[arg(long, default_value_t = DEFAULT_RELAY_MAX_IDLE_TIMEOUT_MS, value_name = "MS")]
    relay_idle_timeout_ms: u64,
    /// QUIC keepalive interval for relayed reverse tunnels; 0 disables keepalive
    #[arg(long, default_value_t = DEFAULT_RELAY_KEEP_ALIVE_MS, value_name = "MS")]
    relay_keep_alive_ms: u64,
    #[arg(long, env = "STARGATE_TLS_CERT_PATH", value_name = "PATH")]
    tls_cert_path: Option<String>,
    #[arg(long, env = "STARGATE_TLS_KEY_PATH", value_name = "PATH")]
    tls_key_path: Option<String>,
    #[arg(long, default_value_t = false, env = "STARGATE_QUIC_INSECURE")]
    quic_insecure: bool,
    /// Tunnel protocol served by this UDP listener. HTTP/3 without WebTransport is unsupported.
    #[arg(long, default_value = "raw-quic", value_name = "PROTOCOL")]
    tunnel_protocol: RouterTunnelProtocol,
    #[arg(long, env = "STARGATE_UPSTREAM_TLS_CERT_PATH", value_name = "PATH")]
    upstream_tls_cert_path: Option<String>,
}

struct RouterStartupConfig {
    listen_addr: SocketAddr,
    health_listen_addr: SocketAddr,
    grpc: GrpcRouterConfig,
    target_build_config: TargetBuildConfig,
    tunnel: RouterTunnelConfig,
}

/// One wire protocol per UDP listener, preventing Raw QUIC and WebTransport settings from mixing.
enum RouterTunnelConfig {
    RawQuic(QuicRouterConfig),
    WebTransport(WebTransportRouterConfig),
}

impl RouterStartupConfig {
    fn from_args(args: Args) -> Result<Self> {
        let relay_endpoint_config = relay_endpoint_config_from_args(&args)?;
        let tls_cert_pem = read_optional_file(args.tls_cert_path.as_deref())?;
        let tls_key_pem = read_optional_file(args.tls_key_path.as_deref())?;
        let grpc = GrpcRouterConfig {
            advertised_hostname_template: args.advertised_hostname_template.clone(),
            target_namespace: args.target_namespace.clone(),
            connect_timeout: Duration::from_millis(args.connect_timeout_ms),
        };
        let tunnel = match args.tunnel_protocol {
            RouterTunnelProtocol::RawQuic => {
                ensure!(
                    args.upstream_tls_cert_path.is_none(),
                    "--upstream-tls-cert-path is only supported with --tunnel-protocol=webtransport"
                );
                RouterTunnelConfig::RawQuic(QuicRouterConfig {
                    listen_addr: args.reverse_tunnel_listen_addr,
                    advertised_hostname_template: grpc.advertised_hostname_template.clone(),
                    target_namespace: grpc.target_namespace.clone(),
                    connect_timeout: grpc.connect_timeout,
                    relay_max_idle_timeout: relay_endpoint_config.max_idle_timeout,
                    relay_keep_alive_interval: relay_endpoint_config.keep_alive_interval,
                    tls_cert_pem,
                    tls_key_pem,
                    quic_insecure: args.quic_insecure,
                })
            }
            RouterTunnelProtocol::WebTransport => {
                RouterTunnelConfig::WebTransport(WebTransportRouterConfig {
                    listen_addr: args.reverse_tunnel_listen_addr,
                    advertised_hostname_template: grpc.advertised_hostname_template.clone(),
                    target_namespace: grpc.target_namespace.clone(),
                    connect_timeout: grpc.connect_timeout,
                    relay_max_idle_timeout: relay_endpoint_config.max_idle_timeout,
                    relay_keep_alive_interval: relay_endpoint_config.keep_alive_interval,
                    tls_cert_pem,
                    tls_key_pem,
                    upstream_tls_cert_pem: read_optional_file(
                        args.upstream_tls_cert_path.as_deref(),
                    )?,
                    quic_insecure: args.quic_insecure,
                })
            }
        };

        Ok(Self {
            listen_addr: args.listen_addr,
            health_listen_addr: args.health_listen_addr,
            grpc,
            target_build_config: TargetBuildConfig {
                service_name: args.target_service_name,
                grpc_port_name: args.grpc_port_name,
                quic_port_name: args.quic_port_name,
            },
            tunnel,
        })
    }
}

struct RouterRuntime {
    tasks: CriticalTaskGroup,
    critical_failure_rx: CriticalTaskFailureReceiver,
}

impl RouterRuntime {
    async fn start(config: RouterStartupConfig, client: Client) -> Result<Self> {
        let grpc_listener = TcpListener::bind(config.listen_addr)
            .await
            .context("failed to bind gRPC router listener")?;
        info!(addr = %grpc_listener.local_addr()?, "gRPC router listening");
        let (targets_tx, targets_rx) = watch::channel(TargetSnapshot::default());
        let metrics = Arc::new(RouterMetrics::new()?);
        let (tasks, critical_failure_rx) = CriticalTaskGroup::new("router");
        let target_namespace = config.grpc.target_namespace.clone();
        let health_listen_addr = config.health_listen_addr;

        let build_config = config.target_build_config;
        tasks.spawn_critical("EndpointSlice watcher", move |shutdown| {
            run_endpoint_slice_watcher(client, target_namespace, build_config, targets_tx, shutdown)
        });
        tasks.spawn_critical("gRPC router", {
            let targets = targets_rx.clone();
            move |shutdown| serve_grpc_router(grpc_listener, config.grpc, targets, shutdown)
        });
        let tunnel_name = match &config.tunnel {
            RouterTunnelConfig::RawQuic(_) => "Raw QUIC router",
            RouterTunnelConfig::WebTransport(_) => "WebTransport router",
        };
        let targets = targets_rx.clone();
        let tunnel_metrics = metrics.clone();
        let connection_tasks = tasks.task_tracker();
        tasks.spawn_critical(tunnel_name, move |shutdown| async move {
            match config.tunnel {
                RouterTunnelConfig::RawQuic(config) => {
                    serve_quic_router(config, targets, tunnel_metrics, shutdown, connection_tasks)
                        .await
                }
                RouterTunnelConfig::WebTransport(config) => {
                    serve_webtransport_router(
                        config,
                        targets,
                        tunnel_metrics,
                        shutdown,
                        connection_tasks,
                    )
                    .await
                }
            }
        });
        tasks.spawn_critical("health server", move |shutdown| {
            serve_health(health_listen_addr, targets_rx, metrics, shutdown)
        });

        Ok(Self {
            tasks,
            critical_failure_rx,
        })
    }

    async fn run_until_shutdown<S>(self, signal: S) -> Result<()>
    where
        S: Future<Output = std::io::Result<&'static str>>,
    {
        tokio::pin!(signal);
        let failure = tokio::select! {
            result = &mut signal => {
                result
                    .context("failed to receive router termination signal")
                    .map(|signal| {
                        info!(signal, "received shutdown signal");
                        None
                    })
            }
            failure = self.critical_failure_rx.recv_async() => {
                failure
                    .context("router critical failure channel closed")
                    .map(|failure| {
                        error!(error = %failure, "critical router task exited");
                        Some(failure)
                    })
            }
        };

        self.tasks.begin_shutdown();
        self.tasks.wait().await;
        if let Some(failure) = failure? {
            return Err(failure.into());
        }
        info!("stargate Kubernetes router stopped cleanly");
        Ok(())
    }
}

#[tokio::main]
async fn main() -> Result<()> {
    init_logging();
    install_default_crypto_provider();
    let config = RouterStartupConfig::from_args(Args::parse())?;
    run_router(config).await
}

async fn run_router(config: RouterStartupConfig) -> Result<()> {
    log_startup(&config);
    start_router(config)
        .await?
        .run_until_shutdown(wait_for_termination_signal())
        .await
}

async fn start_router(config: RouterStartupConfig) -> Result<RouterRuntime> {
    let client = Client::try_default()
        .await
        .context("failed to create Kubernetes client")?;
    RouterRuntime::start(config, client).await
}

fn install_default_crypto_provider() {
    if rustls::crypto::aws_lc_rs::default_provider()
        .install_default()
        .is_err()
    {
        debug!("rustls crypto provider was already installed");
    }
}

fn log_startup(config: &RouterStartupConfig) {
    let (
        tunnel_protocol,
        reverse_tunnel_listen_addr,
        relay_idle_timeout,
        relay_keep_alive,
        quic_insecure,
    ) = match &config.tunnel {
        RouterTunnelConfig::RawQuic(config) => (
            "raw-quic",
            config.listen_addr,
            config.relay_max_idle_timeout,
            config.relay_keep_alive_interval,
            config.quic_insecure,
        ),
        RouterTunnelConfig::WebTransport(config) => (
            "webtransport",
            config.listen_addr,
            config.relay_max_idle_timeout,
            config.relay_keep_alive_interval,
            config.quic_insecure,
        ),
    };
    info!(
        listen_addr = %config.listen_addr,
        reverse_tunnel_listen_addr = %reverse_tunnel_listen_addr,
        health_listen_addr = %config.health_listen_addr,
        target_namespace = %config.grpc.target_namespace,
        target_service_name = %config.target_build_config.service_name,
        advertised_hostname_template = %config.grpc.advertised_hostname_template,
        grpc_port_name = %config.target_build_config.grpc_port_name,
        quic_port_name = %config.target_build_config.quic_port_name,
        connect_timeout_ms = config.grpc.connect_timeout.as_millis(),
        relay_idle_timeout_ms = relay_idle_timeout.as_millis(),
        relay_keep_alive_ms = relay_keep_alive.map_or(0, |duration| duration.as_millis()),
        quic_insecure,
        tunnel_protocol = %tunnel_protocol,
        "starting stargate Kubernetes router"
    );
}

fn read_optional_file(path: Option<&str>) -> Result<Option<Vec<u8>>> {
    path.map(|path| std::fs::read(path).with_context(|| format!("failed to read {path}")))
        .transpose()
}

fn relay_endpoint_config_from_args(args: &Args) -> Result<RelayEndpointConfig> {
    if args.relay_idle_timeout_ms == 0 {
        bail!("--relay-idle-timeout-ms must be greater than 0");
    }
    if args.relay_keep_alive_ms >= args.relay_idle_timeout_ms {
        bail!("--relay-keep-alive-ms must be less than --relay-idle-timeout-ms");
    }
    Ok(RelayEndpointConfig {
        max_idle_timeout: Duration::from_millis(args.relay_idle_timeout_ms),
        keep_alive_interval: (args.relay_keep_alive_ms > 0)
            .then_some(Duration::from_millis(args.relay_keep_alive_ms)),
    })
}

fn init_logging() {
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .with_target(false)
        .compact()
        .init();
}

#[cfg(test)]
mod tests {
    use super::*;

    fn router_argv<'a>(extra: &'a [&'a str]) -> impl Iterator<Item = &'a str> {
        ["stargate-k8s-router", "--target-namespace", "prod"]
            .into_iter()
            .chain(extra.iter().copied())
    }

    fn router_args(extra: &[&str]) -> Args {
        Args::parse_from(router_argv(extra))
    }

    fn startup_config(extra: &[&str]) -> RouterStartupConfig {
        RouterStartupConfig::from_args(router_args(extra))
            .expect("startup config should derive from args")
    }

    fn relay_config(extra: &[&str]) -> Result<RelayEndpointConfig> {
        relay_endpoint_config_from_args(&router_args(extra))
    }

    fn test_file(contents: &[u8]) -> tempfile::NamedTempFile {
        let file = tempfile::NamedTempFile::new().expect("test file should be creatable");
        std::fs::write(file.path(), contents).expect("test file should be writable");
        file
    }

    fn test_file_path(file: &tempfile::NamedTempFile) -> &str {
        file.path()
            .to_str()
            .expect("test file path should be valid UTF-8")
    }

    #[test]
    fn router_cli_accepts_only_raw_quic_and_webtransport_listeners() {
        let defaults =
            Args::try_parse_from(router_argv(&[])).expect("default router arguments should parse");
        assert_eq!(defaults.tunnel_protocol, RouterTunnelProtocol::RawQuic);

        for tunnel_protocol in ["raw-quic", "webtransport"] {
            Args::try_parse_from(router_argv(&["--tunnel-protocol", tunnel_protocol]))
                .unwrap_or_else(|error| panic!("{tunnel_protocol} should parse: {error}"));
        }

        let result = Args::try_parse_from(router_argv(&["--tunnel-protocol", "http3"]));
        assert!(
            result.is_err(),
            "http3 must not select a router UDP listener"
        );
    }

    #[test]
    fn raw_quic_rejects_the_webtransport_only_upstream_trust_option() {
        let upstream_ca = test_file(b"upstream-ca-bytes");
        let args = router_args(&["--upstream-tls-cert-path", test_file_path(&upstream_ca)]);

        let error = RouterStartupConfig::from_args(args)
            .err()
            .expect("Raw QUIC must reject a WebTransport-only trust option");
        assert_eq!(
            error.to_string(),
            "--upstream-tls-cert-path is only supported with --tunnel-protocol=webtransport"
        );
    }

    #[test]
    fn startup_config_derives_runtime_configs_from_args() {
        let cert = test_file(b"cert-bytes");
        let key = test_file(b"key-bytes");
        let upstream_ca = test_file(b"upstream-ca-bytes");
        let config = startup_config(&[
            "--listen-addr",
            "127.0.0.1:41071",
            "--reverse-tunnel-listen-addr",
            "127.0.0.1:41072",
            "--health-listen-addr",
            "127.0.0.1:41800",
            "--target-service-name",
            "stargate-ready",
            "--advertised-hostname-template",
            "{pod_name}.{namespace}.example",
            "--grpc-port-name",
            "grpc-control",
            "--quic-port-name",
            "quic-tunnel",
            "--connect-timeout-ms",
            "2500",
            "--relay-idle-timeout-ms",
            "20000",
            "--relay-keep-alive-ms",
            "5000",
            "--tls-cert-path",
            test_file_path(&cert),
            "--tls-key-path",
            test_file_path(&key),
            "--quic-insecure",
        ]);

        assert_eq!(config.listen_addr, "127.0.0.1:41071".parse().unwrap());
        assert_eq!(
            config.health_listen_addr,
            "127.0.0.1:41800".parse().unwrap()
        );
        assert_eq!(config.grpc.target_namespace, "prod");
        assert_eq!(
            config.grpc.advertised_hostname_template,
            "{pod_name}.{namespace}.example"
        );
        assert_eq!(config.grpc.connect_timeout, Duration::from_millis(2500));
        assert_eq!(
            config.target_build_config,
            TargetBuildConfig {
                service_name: "stargate-ready".to_string(),
                grpc_port_name: "grpc-control".to_string(),
                quic_port_name: "quic-tunnel".to_string(),
            }
        );

        let RouterTunnelConfig::RawQuic(quic_config) = &config.tunnel else {
            panic!("default startup configuration must construct only the Raw QUIC runtime");
        };
        assert_eq!(quic_config.listen_addr, "127.0.0.1:41072".parse().unwrap());
        assert_eq!(
            quic_config.advertised_hostname_template,
            "{pod_name}.{namespace}.example"
        );
        assert_eq!(quic_config.target_namespace, "prod");
        assert_eq!(quic_config.connect_timeout, Duration::from_millis(2500));
        assert_eq!(
            quic_config.relay_max_idle_timeout,
            Duration::from_millis(20000)
        );
        assert_eq!(
            quic_config.relay_keep_alive_interval,
            Some(Duration::from_millis(5000))
        );
        assert_eq!(
            quic_config.tls_cert_pem.as_deref(),
            Some(&b"cert-bytes"[..])
        );
        assert_eq!(quic_config.tls_key_pem.as_deref(), Some(&b"key-bytes"[..]));
        assert!(quic_config.quic_insecure);

        let webtransport_config = startup_config(&[
            "--tunnel-protocol=webtransport",
            "--upstream-tls-cert-path",
            test_file_path(&upstream_ca),
            "--quic-insecure",
        ]);
        let RouterTunnelConfig::WebTransport(webtransport_tunnel) = &webtransport_config.tunnel
        else {
            panic!(
                "WebTransport startup configuration must construct only the WebTransport runtime"
            );
        };
        assert_eq!(
            webtransport_tunnel.upstream_tls_cert_pem.as_deref(),
            Some(&b"upstream-ca-bytes"[..])
        );
        assert_eq!(
            webtransport_tunnel.listen_addr,
            "0.0.0.0:50072".parse().unwrap()
        );
        assert_eq!(
            webtransport_tunnel.relay_max_idle_timeout,
            Duration::from_millis(DEFAULT_RELAY_MAX_IDLE_TIMEOUT_MS)
        );
        assert_eq!(
            webtransport_tunnel.relay_keep_alive_interval,
            Some(Duration::from_millis(DEFAULT_RELAY_KEEP_ALIVE_MS))
        );
        assert!(webtransport_tunnel.quic_insecure);
        log_startup(&webtransport_config);
    }

    #[test]
    fn startup_config_derives_absent_tls_and_logs_disabled_keepalive() {
        let config = startup_config(&["--relay-keep-alive-ms", "0"]);

        let RouterTunnelConfig::RawQuic(tunnel) = &config.tunnel else {
            panic!("default startup configuration must use Raw QUIC");
        };
        assert_eq!(tunnel.tls_cert_pem, None);
        assert_eq!(tunnel.tls_key_pem, None);
        assert_eq!(tunnel.relay_keep_alive_interval, None);
        log_startup(&config);
    }

    #[tokio::test]
    async fn router_runtime_turns_critical_exit_into_error() {
        let (tasks, critical_failure_rx) = CriticalTaskGroup::new("router");
        tasks.spawn_critical("QUIC router", |_shutdown| async {
            anyhow::bail!("bind failed")
        });
        let runtime = RouterRuntime {
            tasks,
            critical_failure_rx,
        };

        let error = runtime
            .run_until_shutdown(std::future::pending())
            .await
            .expect_err("critical task exits should fail the process");

        assert!(error.to_string().contains("QUIC router"));
        assert!(error.to_string().contains("bind failed"));
    }

    #[tokio::test]
    async fn router_runtime_signal_error_still_stops_critical_tasks() {
        let (stopped_tx, stopped_rx) = tokio::sync::oneshot::channel();
        let (tasks, critical_failure_rx) = CriticalTaskGroup::new("router");
        tasks.spawn_critical("stoppable root", |shutdown| async move {
            shutdown.cancelled().await;
            let _ = stopped_tx.send(());
            Ok(())
        });
        let runtime = RouterRuntime {
            tasks,
            critical_failure_rx,
        };

        let error = runtime
            .run_until_shutdown(std::future::ready(Err(std::io::Error::other(
                "signal setup failed",
            ))))
            .await
            .expect_err("signal failure should fail the process after shutdown");

        assert!(format!("{error:#}").contains("signal setup failed"));
        stopped_rx
            .await
            .expect("signal failure should cancel and join critical tasks");
    }

    #[tokio::test]
    async fn router_runtime_starts_and_stops_all_roots() -> Result<()> {
        install_default_crypto_provider();
        let config = startup_config(&[
            "--listen-addr=127.0.0.1:0",
            "--reverse-tunnel-listen-addr=127.0.0.1:0",
            "--health-listen-addr=127.0.0.1:0",
            "--quic-insecure",
        ]);
        let client = Client::try_from(kube::Config::new("http://127.0.0.1:9".parse()?))?;

        RouterRuntime::start(config, client)
            .await?
            .run_until_shutdown(std::future::ready(Ok("test")))
            .await
    }

    #[test]
    fn relay_endpoint_config_uses_long_idle_defaults() {
        let config = relay_config(&[]).expect("default relay endpoint config should be valid");

        assert_eq!(
            config,
            RelayEndpointConfig {
                max_idle_timeout: Duration::from_secs(300),
                keep_alive_interval: Some(Duration::from_secs(10)),
            }
        );
    }

    #[test]
    fn relay_endpoint_config_allows_disabling_keep_alive() {
        let config = relay_config(&["--relay-keep-alive-ms", "0"])
            .expect("relay endpoint config should allow no keepalive");

        assert_eq!(config.keep_alive_interval, None);
    }

    #[test]
    fn relay_endpoint_config_rejects_disabled_idle_timeout() {
        let error = relay_config(&["--relay-idle-timeout-ms", "0"]).unwrap_err();
        assert_eq!(
            error.to_string(),
            "--relay-idle-timeout-ms must be greater than 0"
        );
    }

    #[test]
    fn relay_endpoint_config_rejects_keep_alive_at_idle_timeout() {
        let error = relay_config(&[
            "--relay-idle-timeout-ms",
            "10000",
            "--relay-keep-alive-ms",
            "10000",
        ])
        .unwrap_err();
        assert_eq!(
            error.to_string(),
            "--relay-keep-alive-ms must be less than --relay-idle-timeout-ms"
        );
    }
}
