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
use std::time::Duration;

use anyhow::{Context, Result, anyhow, bail};
use clap::Parser;
use kube::Client;
use stargate_forwarding::RelayEndpointConfig;
use stargate_k8s_router::endpoints::{TargetBuildConfig, TargetSnapshot};
use stargate_k8s_router::grpc::{GrpcRouterConfig, serve_grpc_router};
use stargate_k8s_router::health::serve_health;
use stargate_k8s_router::metrics::RouterMetrics;
use stargate_k8s_router::quic::{QuicRouterConfig, serve_quic_router};
use stargate_k8s_router::watcher::run_endpoint_slice_watcher;
use tokio::net::TcpListener;
use tokio::sync::mpsc;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;
use tracing::{debug, error, info};
use tracing_subscriber::EnvFilter;

const DEFAULT_CONNECT_TIMEOUT_MS: u64 = 5_000;
const DEFAULT_RELAY_MAX_IDLE_TIMEOUT_MS: u64 = 300_000;
const DEFAULT_RELAY_KEEP_ALIVE_MS: u64 = 10_000;

#[derive(Parser, Debug)]
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
}

#[tokio::main]
async fn main() -> Result<()> {
    init_logging();
    if rustls::crypto::aws_lc_rs::default_provider()
        .install_default()
        .is_err()
    {
        debug!("rustls crypto provider was already installed");
    }
    let args = Args::parse();
    let tls_cert_pem = args.tls_cert_path.as_ref().map(std::fs::read).transpose()?;
    let tls_key_pem = args.tls_key_path.as_ref().map(std::fs::read).transpose()?;
    let connect_timeout = Duration::from_millis(args.connect_timeout_ms);
    let relay_endpoint_config = relay_endpoint_config_from_args(&args)?;

    info!(
        listen_addr = %args.listen_addr,
        reverse_tunnel_listen_addr = %args.reverse_tunnel_listen_addr,
        health_listen_addr = %args.health_listen_addr,
        target_namespace = %args.target_namespace,
        target_service_name = %args.target_service_name,
        advertised_hostname_template = %args.advertised_hostname_template,
        grpc_port_name = %args.grpc_port_name,
        quic_port_name = %args.quic_port_name,
        connect_timeout_ms = args.connect_timeout_ms,
        relay_idle_timeout_ms = args.relay_idle_timeout_ms,
        relay_keep_alive_ms = args.relay_keep_alive_ms,
        quic_insecure = args.quic_insecure,
        "starting stargate Kubernetes router"
    );

    let client = Client::try_default()
        .await
        .context("failed to create Kubernetes client")?;
    let (targets_tx, targets_rx) = watch::channel(TargetSnapshot::default());
    let metrics = std::sync::Arc::new(RouterMetrics::new()?);
    let shutdown = CancellationToken::new();
    let tracker = TaskTracker::new();
    let (critical_task_tx, critical_task_rx) = mpsc::unbounded_channel();

    let grpc_listener = TcpListener::bind(args.listen_addr)
        .await
        .context("failed to bind gRPC router listener")?;
    info!(addr = %grpc_listener.local_addr()?, "gRPC router listening");

    tracker.spawn({
        let shutdown = shutdown.child_token();
        let target_namespace = args.target_namespace.clone();
        let build_config = TargetBuildConfig {
            service_name: args.target_service_name.clone(),
            grpc_port_name: args.grpc_port_name.clone(),
            quic_port_name: args.quic_port_name.clone(),
        };
        let critical_task_tx = critical_task_tx.clone();
        async move {
            let result = run_endpoint_slice_watcher(
                client,
                target_namespace,
                build_config,
                targets_tx,
                shutdown.clone(),
            )
            .await;
            report_critical_task_exit("EndpointSlice watcher", result, &shutdown, critical_task_tx);
        }
    });

    tracker.spawn({
        let shutdown = shutdown.child_token();
        let targets = targets_rx.clone();
        let critical_task_tx = critical_task_tx.clone();
        let config = GrpcRouterConfig {
            advertised_hostname_template: args.advertised_hostname_template.clone(),
            target_namespace: args.target_namespace.clone(),
            connect_timeout,
        };
        async move {
            let result = serve_grpc_router(grpc_listener, config, targets, shutdown.clone()).await;
            report_critical_task_exit("gRPC router", result, &shutdown, critical_task_tx);
        }
    });

    tracker.spawn({
        let shutdown = shutdown.child_token();
        let targets = targets_rx.clone();
        let metrics = metrics.clone();
        let critical_task_tx = critical_task_tx.clone();
        let config = QuicRouterConfig {
            listen_addr: args.reverse_tunnel_listen_addr,
            advertised_hostname_template: args.advertised_hostname_template.clone(),
            target_namespace: args.target_namespace.clone(),
            connect_timeout,
            relay_max_idle_timeout: relay_endpoint_config.max_idle_timeout,
            relay_keep_alive_interval: relay_endpoint_config.keep_alive_interval,
            tls_cert_pem,
            tls_key_pem,
            quic_insecure: args.quic_insecure,
        };
        async move {
            let result = serve_quic_router(config, targets, metrics, shutdown.clone()).await;
            report_critical_task_exit("QUIC router", result, &shutdown, critical_task_tx);
        }
    });

    tracker.spawn({
        let shutdown = shutdown.child_token();
        let critical_task_tx = critical_task_tx.clone();
        let metrics = metrics.clone();
        async move {
            let result = serve_health(
                args.health_listen_addr,
                targets_rx,
                metrics,
                shutdown.clone(),
            )
            .await;
            report_critical_task_exit("health server", result, &shutdown, critical_task_tx);
        }
    });
    // Close the parent sender so wait_for_shutdown_reason can observe channel
    // closure if every critical task exits before a signal arrives.
    drop(critical_task_tx);

    let shutdown_reason =
        wait_for_shutdown_reason(wait_for_termination_signal(), critical_task_rx).await;
    match &shutdown_reason {
        ShutdownReason::Signal(signal) => {
            info!(signal, "received shutdown signal");
        }
        ShutdownReason::CriticalTaskExit(message) => {
            error!(%message, "critical router task exited");
        }
    }
    shutdown.cancel();
    tracker.close();
    tracker.wait().await;
    if let ShutdownReason::CriticalTaskExit(message) = shutdown_reason {
        return Err(anyhow!(message));
    }
    info!("stargate Kubernetes router stopped cleanly");
    Ok(())
}

fn relay_endpoint_config_from_args(args: &Args) -> Result<RelayEndpointConfig> {
    if args.relay_idle_timeout_ms == 0 {
        bail!("--relay-idle-timeout-ms must be greater than 0");
    }
    let keep_alive_interval = if args.relay_keep_alive_ms == 0 {
        None
    } else {
        if args.relay_keep_alive_ms >= args.relay_idle_timeout_ms {
            bail!("--relay-keep-alive-ms must be less than --relay-idle-timeout-ms");
        }
        Some(Duration::from_millis(args.relay_keep_alive_ms))
    };
    Ok(RelayEndpointConfig {
        max_idle_timeout: Duration::from_millis(args.relay_idle_timeout_ms),
        keep_alive_interval,
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

#[derive(Debug, PartialEq, Eq)]
enum ShutdownReason {
    Signal(&'static str),
    CriticalTaskExit(String),
}

fn report_critical_task_exit(
    task_name: &'static str,
    result: Result<()>,
    shutdown: &CancellationToken,
    critical_task_tx: mpsc::UnboundedSender<String>,
) {
    if shutdown.is_cancelled() {
        return;
    }
    let message = match result {
        Ok(()) => format!("{task_name} exited unexpectedly"),
        Err(error) => format!("{task_name} exited with error: {error:#}"),
    };
    let _ = critical_task_tx.send(message);
}

async fn wait_for_shutdown_reason<S>(
    signal: S,
    mut critical_task_rx: mpsc::UnboundedReceiver<String>,
) -> ShutdownReason
where
    S: Future<Output = &'static str>,
{
    tokio::select! {
        signal = signal => ShutdownReason::Signal(signal),
        message = critical_task_rx.recv() => {
            ShutdownReason::CriticalTaskExit(
                message.unwrap_or_else(|| "all critical router tasks exited".to_string()),
            )
        }
    }
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

    #[test]
    fn relay_endpoint_config_uses_long_idle_defaults() {
        let args = Args::parse_from(["stargate-k8s-router", "--target-namespace", "prod"]);

        let config = relay_endpoint_config_from_args(&args)
            .expect("default relay endpoint config should be valid");

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
        let args = Args::parse_from([
            "stargate-k8s-router",
            "--target-namespace",
            "prod",
            "--relay-keep-alive-ms",
            "0",
        ]);

        let config = relay_endpoint_config_from_args(&args)
            .expect("relay endpoint config should allow no keepalive");

        assert_eq!(config.keep_alive_interval, None);
    }

    #[test]
    fn relay_endpoint_config_rejects_disabled_idle_timeout() {
        let args = Args::parse_from([
            "stargate-k8s-router",
            "--target-namespace",
            "prod",
            "--relay-idle-timeout-ms",
            "0",
        ]);

        let error = relay_endpoint_config_from_args(&args)
            .expect_err("relay idle timeout should be required");

        assert!(
            error
                .to_string()
                .contains("--relay-idle-timeout-ms must be greater than 0")
        );
    }

    #[test]
    fn relay_endpoint_config_rejects_keep_alive_at_idle_timeout() {
        let args = Args::parse_from([
            "stargate-k8s-router",
            "--target-namespace",
            "prod",
            "--relay-idle-timeout-ms",
            "10000",
            "--relay-keep-alive-ms",
            "10000",
        ]);

        let error = relay_endpoint_config_from_args(&args)
            .expect_err("keepalive must be lower than idle timeout");

        assert!(
            error
                .to_string()
                .contains("--relay-keep-alive-ms must be less than --relay-idle-timeout-ms")
        );
    }

    #[tokio::test]
    async fn wait_for_shutdown_reason_reports_critical_task_exit() {
        let (tx, rx) = mpsc::unbounded_channel();
        tx.send("QUIC router exited with error: bind failed".to_string())
            .expect("critical task channel should be open");

        let reason = wait_for_shutdown_reason(std::future::pending(), rx).await;

        assert_eq!(
            reason,
            ShutdownReason::CriticalTaskExit(
                "QUIC router exited with error: bind failed".to_string()
            )
        );
    }
}
