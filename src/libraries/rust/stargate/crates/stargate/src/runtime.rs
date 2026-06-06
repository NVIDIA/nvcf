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
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use anyhow::{Context, Result};
use tokio::sync::watch;
use tokio_stream::wrappers::TcpListenerStream;
use tokio_util::task::TaskTracker;
use tonic::transport::Server;
use tower::util::MapRequestLayer;
use tracing::{error, info};

use tokio_util::sync::CancellationToken;

use stargate_forwarding::{ForwardingResolver, render_hostname};

use crate::auth::{OpenAuthenticator, WorkerAuthenticator};
use crate::control_plane::{RegistrationConnectionConfig, StargateService, StargateServiceConfig};
use crate::discovery::Discovery;
use crate::http_proxy::{ProxyAppState, ProxyTrafficState, ProxyTransportConfig, make_router};
use crate::load_balancer::{LoadBalancerConfig, LoadBalancerRouter};
use crate::metrics::StargateMetrics;
use crate::routing_state::StargateState;
use crate::tunnel::{QuicHttpProxy, QuicTunnelConfig};

const ACTIVE_MODELS_SNAPSHOT_INTERVAL: Duration = Duration::from_secs(1);

#[cfg(test)]
static ACTIVE_MODELS_SNAPSHOT_TASKS: std::sync::atomic::AtomicUsize =
    std::sync::atomic::AtomicUsize::new(0);

#[derive(Debug, Clone)]
pub struct StargateRuntimeConfig {
    /// Stable process/pod identity used in logs, metrics, routing snapshots, and
    /// as the default `{pod_name}` value when no Kubernetes pod name is set.
    pub stargate_id: String,

    /// Local TCP socket for the backend-facing gRPC control plane:
    /// `WatchStargates` and `RegisterInferenceServer`.
    pub grpc_listen_addr: SocketAddr,

    /// Local TCP socket for the frontend-facing `ListModels` gRPC service.
    /// This is intentionally separate from the backend control plane.
    pub model_discovery_listen_addr: SocketAddr,

    /// Local HTTP socket for OpenAI-compatible proxy traffic, health probes,
    /// and metrics scraping.
    pub http_listen_addr: SocketAddr,

    /// Self address used by discovery implementations before any hostname
    /// template is applied. Outside Kubernetes this is usually what pylons see
    /// in `WatchStargates.stargates[*].advertise_addr`; in Kubernetes the
    /// advertised hostname template normally replaces the host.
    pub advertise_addr: SocketAddr,

    /// DNS name used for Stargate peer discovery. In Kubernetes this must be
    /// the headless Service name so EndpointSlice readiness controls which pods
    /// are visible to pylons and peer forwarding.
    pub stargate_discovery_dns_name: String,

    /// Additional `WatchStargates` endpoints for remote regions. These are
    /// recursive watch seeds only; pylons register to concrete Stargate entries
    /// returned by those streams.
    pub remote_watch_stargate_urls: Vec<String>,

    /// Optional pylon dial address for backend-facing gRPC registration/watch.
    /// When set, `WatchStargates` preserves the per-pod `advertise_addr` as
    /// gRPC authority/SNI identity and sends this as `grpc_pylon_dial_addr` so
    /// pylons connect through a TCP load balancer.
    pub grpc_pylon_dial_addr: Option<String>,

    /// Template for backend-facing advertised hostnames, supporting
    /// `{pod_name}` and `{namespace}`. The rendered value is used as gRPC
    /// authority and QUIC SNI so routers can identify the selected pod.
    pub advertised_hostname_template: Option<String>,

    /// Kubernetes pod name used to render `advertised_hostname_template`.
    pub pod_name: Option<String>,

    /// Kubernetes namespace used to render `advertised_hostname_template`.
    pub pod_namespace: Option<String>,

    /// Poll cadence for DNS-based peer discovery.
    pub dns_poll_interval: Duration,

    /// Maximum interval between unchanged `WatchStargates` snapshots.
    pub watch_heartbeat_interval: Duration,

    /// Minimum idle timeout for heartbeat-aware registration streams. A zero
    /// value disables registration idle enforcement.
    pub registration_update_idle_timeout: Duration,

    /// Hard cap for heartbeat-aware registration idle timeout and fallback for
    /// streams that do not advertise heartbeat hints. A zero value disables idle enforcement.
    pub registration_update_max_idle_timeout: Duration,

    /// QUIC/TLS/tunnel-protocol and proxy retry configuration for backend
    /// request forwarding.
    pub proxy_transport: ProxyTransportConfig,

    /// Optional JSON load-balancer config path.
    pub lb_config_path: Option<String>,

    /// Prefix prepended to Prometheus metric names.
    pub metrics_prefix: String,

    /// Local UDP socket Stargate binds for pylon-initiated reverse QUIC
    /// tunnels. This is the actual listener, not necessarily the address pylons
    /// should dial from another network.
    pub reverse_tunnel_listen_addr: Option<SocketAddr>,

    /// Optional pylon dial address for reverse QUIC tunnels. When set, Stargate
    /// still sends the per-pod `reverse_tunnel_target` identity for SNI/routing,
    /// and sends this as `reverse_tunnel_pylon_dial_addr` so pylons open the UDP
    /// connection through a separate load balancer.
    pub reverse_tunnel_pylon_dial_addr: Option<String>,

    /// How long registration processing waits for the reverse tunnel connection
    /// before advertising the backend inactive for that Stargate.
    pub reverse_tunnel_connect_timeout: Duration,
}

pub struct StargateRuntime {
    config: StargateRuntimeConfig,
    discovery: Box<dyn Discovery>,
    forwarding: Option<Arc<dyn ForwardingResolver>>,
    authenticator: Arc<dyn WorkerAuthenticator>,
    /// Pre-bound listeners bypass the bind-after-allocation race where
    /// `ephemeral_addr()` releases a port that another process can steal
    /// before `start()` re-binds it. When set, `start()` uses the provided
    /// socket instead of binding to the configured address.
    grpc_listener: Option<std::net::TcpListener>,
    model_discovery_listener: Option<std::net::TcpListener>,
    http_listener: Option<std::net::TcpListener>,
    reverse_tunnel_socket: Option<std::net::UdpSocket>,
}

impl StargateRuntime {
    pub fn new(config: StargateRuntimeConfig, discovery: Box<dyn Discovery>) -> Self {
        Self {
            config,
            discovery,
            forwarding: None,
            authenticator: Arc::new(OpenAuthenticator),
            grpc_listener: None,
            model_discovery_listener: None,
            http_listener: None,
            reverse_tunnel_socket: None,
        }
    }

    pub fn with_forwarding(mut self, forwarding: Arc<dyn ForwardingResolver>) -> Self {
        self.forwarding = Some(forwarding);
        self
    }

    pub fn with_authenticator(mut self, authenticator: Arc<dyn WorkerAuthenticator>) -> Self {
        self.authenticator = authenticator;
        self
    }

    pub fn with_grpc_listener(mut self, listener: std::net::TcpListener) -> Self {
        self.grpc_listener = Some(listener);
        self
    }

    pub fn with_model_discovery_listener(mut self, listener: std::net::TcpListener) -> Self {
        self.model_discovery_listener = Some(listener);
        self
    }

    pub fn with_http_listener(mut self, listener: std::net::TcpListener) -> Self {
        self.http_listener = Some(listener);
        self
    }

    pub fn with_reverse_tunnel_socket(mut self, socket: std::net::UdpSocket) -> Self {
        self.reverse_tunnel_socket = Some(socket);
        self
    }

    pub async fn start(self) -> Result<StargateHandle> {
        let (shutdown_sender, shutdown_receiver) = watch::channel(false);
        let task_tracker = TaskTracker::new();
        let draining = Arc::new(AtomicBool::new(false));
        let shutdown_token = CancellationToken::new();
        let startup_shutdown =
            StartupShutdownGuard::new(&shutdown_sender, &shutdown_token, &task_tracker);
        let metrics = StargateMetrics::new_with_prefix(&self.config.metrics_prefix)
            .context("failed to create prometheus metrics registry")?;

        let quic_proxy = QuicHttpProxy::new(
            QuicTunnelConfig {
                connect_timeout: self.config.proxy_transport.quic_connect_timeout,
                request_timeout: self.config.proxy_transport.quic_request_timeout,
                tls_cert_pem: self.config.proxy_transport.tls_cert_pem.clone(),
                server_tls_identity: self.config.proxy_transport.server_tls_identity.clone(),
                quic_insecure: self.config.proxy_transport.quic_insecure,
                tunnel_protocol: self.config.proxy_transport.tunnel_protocol,
                direct_quic_connections: self.config.proxy_transport.direct_quic_connections,
            },
            self.authenticator.clone(),
        )
        .context("failed to initialize quic proxy")?;
        let quic_proxy = Arc::new(quic_proxy);
        let shared_state = Arc::new(StargateState::new_with_metrics(metrics.clone()));
        spawn_active_models_snapshot_loop(
            &task_tracker,
            shared_state.clone(),
            ACTIVE_MODELS_SNAPSHOT_INTERVAL,
            shutdown_token.clone(),
        );

        let reverse_tunnel_ack_addrs =
            if let Some(reverse_addr) = self.config.reverse_tunnel_listen_addr {
                let bound_reverse_addr = quic_proxy
                    .start_reverse_listener(
                        reverse_addr,
                        shared_state.clone(),
                        shutdown_token.clone(),
                        task_tracker.clone(),
                        self.forwarding.clone(),
                        self.reverse_tunnel_socket,
                    )
                    .await
                    .context("failed to start reverse tunnel listener")?;
                Some(derive_reverse_tunnel_ack_addrs(
                    &self
                        .config
                        .advertised_hostname_template
                        .clone()
                        .unwrap_or_else(|| "{pod_name}.stargate.external".to_string()),
                    self.config.pod_namespace.as_deref().unwrap_or(""),
                    self.config
                        .pod_name
                        .as_deref()
                        .unwrap_or(&self.config.stargate_id),
                    bound_reverse_addr.port(),
                    self.config.reverse_tunnel_pylon_dial_addr.as_deref(),
                ))
            } else {
                None
            };
        let registration_connection_config = RegistrationConnectionConfig {
            quic_proxy: quic_proxy.clone(),
            reverse_tunnel_connect_timeout: self.config.reverse_tunnel_connect_timeout,
            reverse_tunnel_target: reverse_tunnel_ack_addrs
                .as_ref()
                .map(|addrs| addrs.routing_target_addr.clone()),
            reverse_tunnel_pylon_dial_addr: reverse_tunnel_ack_addrs
                .map(|addrs| addrs.pylon_dial_addr),
        };

        let lb_config = match &self.config.lb_config_path {
            Some(path) => {
                let bytes = std::fs::read(path)
                    .with_context(|| format!("failed to read lb config file: {path}"))?;
                serde_json::from_slice::<LoadBalancerConfig>(&bytes)
                    .with_context(|| format!("failed to parse lb config file: {path}"))?
            }
            None => LoadBalancerConfig::default(),
        };
        let lb_router = Arc::new(
            LoadBalancerRouter::from_config(&lb_config)
                .context("failed to create load balancer router")?,
        );
        info!(
            default_lb = %lb_config.default,
            model_overrides = lb_config.models.len(),
            "load balancer config loaded"
        );

        let model_discovery_listener = match self.model_discovery_listener {
            Some(listener) => {
                listener
                    .set_nonblocking(true)
                    .context("failed to set model-discovery listener to non-blocking")?;
                tokio::net::TcpListener::from_std(listener)
                    .context("failed to convert model-discovery listener")?
            }
            None => tokio::net::TcpListener::bind(self.config.model_discovery_listen_addr)
                .await
                .context("failed to bind model-discovery listener")?,
        };
        let service = StargateService::new(StargateServiceConfig {
            stargate_id: self.config.stargate_id.clone(),
            advertise_addr: self.config.advertise_addr,
            discovery_dns_name: self.config.stargate_discovery_dns_name.clone(),
            discovery: self.discovery,
            remote_watch_stargate_urls: self.config.remote_watch_stargate_urls.clone(),
            grpc_pylon_dial_addr: self.config.grpc_pylon_dial_addr.clone(),
            discovery_poll_interval: self.config.dns_poll_interval,
            watch_heartbeat_interval: self.config.watch_heartbeat_interval,
            shutdown_token: shutdown_token.clone(),
            task_tracker: task_tracker.clone(),
            registration_update_idle_timeout: self.config.registration_update_idle_timeout,
            registration_update_max_idle_timeout: self.config.registration_update_max_idle_timeout,
            state: shared_state.clone(),
            registration_connection_config,
            forwarding: self.forwarding,
            authenticator: self.authenticator,
        });

        let proxy_router = make_router(ProxyAppState {
            state: service.state(),
            traffic: ProxyTrafficState {
                is_draining: draining.clone(),
            },
            quic_proxy: quic_proxy.clone(),
            lb_router,
            metrics: metrics.clone(),
            retry: self.config.proxy_transport.retry.clone(),
        });

        let grpc_listener = match self.grpc_listener {
            Some(listener) => {
                listener
                    .set_nonblocking(true)
                    .context("failed to set gRPC listener to non-blocking")?;
                tokio::net::TcpListener::from_std(listener)
                    .context("failed to convert gRPC listener")?
            }
            None => tokio::net::TcpListener::bind(self.config.grpc_listen_addr)
                .await
                .context("failed to bind gRPC listener")?,
        };
        let grpc_incoming = TcpListenerStream::new(grpc_listener);
        let mut grpc_shutdown = shutdown_receiver.clone();
        let grpc_service = service.clone();
        task_tracker.spawn(async move {
            let authority_layer = MapRequestLayer::new(|mut req: http::Request<_>| {
                if let Some(authority) = req.uri().authority().cloned() {
                    req.extensions_mut().insert(authority);
                }
                req
            });
            let result = Server::builder()
                .layer(authority_layer)
                .add_service(
                    stargate_proto::pb::stargate_control_plane_server::StargateControlPlaneServer::new(
                        grpc_service,
                    ),
                )
                .serve_with_incoming_shutdown(grpc_incoming, async move {
                    let _ = grpc_shutdown.changed().await;
                })
                .await;
            if let Err(error) = result {
                error!(%error, "gRPC server exited with error");
            }
        });

        let model_discovery_incoming = TcpListenerStream::new(model_discovery_listener);
        let mut model_discovery_shutdown = shutdown_receiver.clone();
        let model_discovery_service = service.clone();
        task_tracker.spawn(async move {
            let result = Server::builder()
                .add_service(
                    stargate_proto::pb::stargate_model_discovery_server::StargateModelDiscoveryServer::new(
                        model_discovery_service,
                    ),
                )
                .serve_with_incoming_shutdown(model_discovery_incoming, async move {
                    let _ = model_discovery_shutdown.changed().await;
                })
                .await;
            if let Err(error) = result {
                error!(%error, "model-discovery gRPC server exited with error");
            }
        });

        let listener = match self.http_listener {
            Some(listener) => {
                listener
                    .set_nonblocking(true)
                    .context("failed to set HTTP listener to non-blocking")?;
                tokio::net::TcpListener::from_std(listener)
                    .context("failed to convert HTTP listener")?
            }
            None => tokio::net::TcpListener::bind(self.config.http_listen_addr)
                .await
                .context("failed to bind HTTP listener")?,
        };
        let mut http_shutdown = shutdown_receiver.clone();
        task_tracker.spawn(async move {
            let result = axum::serve(
                listener,
                proxy_router.into_make_service_with_connect_info::<SocketAddr>(),
            )
            .with_graceful_shutdown(async move {
                let _ = http_shutdown.changed().await;
            })
            .await;
            if let Err(error) = result {
                error!(%error, "HTTP server exited with error");
            }
        });

        startup_shutdown.disarm();
        Ok(StargateHandle {
            shutdown_sender,
            task_tracker,
            draining,
            shutdown_token,
            metrics,
            state: service.state(),
        })
    }
}

struct StartupShutdownGuard<'a> {
    shutdown_sender: &'a watch::Sender<bool>,
    shutdown_token: &'a CancellationToken,
    task_tracker: &'a TaskTracker,
    disarmed: bool,
}

impl<'a> StartupShutdownGuard<'a> {
    fn new(
        shutdown_sender: &'a watch::Sender<bool>,
        shutdown_token: &'a CancellationToken,
        task_tracker: &'a TaskTracker,
    ) -> Self {
        Self {
            shutdown_sender,
            shutdown_token,
            task_tracker,
            disarmed: false,
        }
    }

    fn disarm(mut self) {
        self.disarmed = true;
    }
}

impl Drop for StartupShutdownGuard<'_> {
    fn drop(&mut self) {
        if self.disarmed {
            return;
        }
        self.shutdown_token.cancel();
        let _ = self.shutdown_sender.send(true);
        self.task_tracker.close();
    }
}

fn spawn_active_models_snapshot_loop(
    task_tracker: &TaskTracker,
    state: Arc<StargateState>,
    interval: Duration,
    shutdown_token: CancellationToken,
) {
    #[cfg(test)]
    ACTIVE_MODELS_SNAPSHOT_TASKS.fetch_add(1, Ordering::SeqCst);

    task_tracker.spawn(async move {
        #[cfg(test)]
        let _active_task = ActiveModelsSnapshotTaskGuard;

        run_active_models_snapshot_loop(state, interval, shutdown_token).await;
    });
}

#[cfg(test)]
struct ActiveModelsSnapshotTaskGuard;

#[cfg(test)]
impl Drop for ActiveModelsSnapshotTaskGuard {
    fn drop(&mut self) {
        ACTIVE_MODELS_SNAPSHOT_TASKS.fetch_sub(1, Ordering::SeqCst);
    }
}

async fn run_active_models_snapshot_loop(
    state: Arc<StargateState>,
    interval: Duration,
    shutdown_token: CancellationToken,
) {
    let mut ticker = tokio::time::interval(interval);
    ticker.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    loop {
        tokio::select! {
            _ = shutdown_token.cancelled() => break,
            _ = ticker.tick() => state.refresh_active_models_snapshot().await,
        }
    }
}

pub struct StargateHandle {
    shutdown_sender: watch::Sender<bool>,
    task_tracker: TaskTracker,
    draining: Arc<AtomicBool>,
    shutdown_token: CancellationToken,
    metrics: Arc<StargateMetrics>,
    state: Arc<StargateState>,
}

impl StargateHandle {
    pub fn metrics(&self) -> Arc<StargateMetrics> {
        self.metrics.clone()
    }

    pub fn state(&self) -> Arc<StargateState> {
        self.state.clone()
    }

    pub fn begin_shutdown(&self) {
        let already_draining = self.draining.swap(true, Ordering::SeqCst);
        if !already_draining {
            info!("Entering draining mode");
            self.shutdown_token.cancel();
            let _ = self.shutdown_sender.send(true);
            self.task_tracker.close();
        }
    }

    pub async fn wait_for_shutdown(&self, timeout: Duration) -> bool {
        tokio::select! {
            _ = self.task_tracker.wait() => true,
            _ = tokio::time::sleep(timeout) => false,
        }
    }
}

fn derive_reverse_tunnel_target(
    hostname_template: &str,
    namespace: &str,
    pod_name: &str,
    reverse_port: u16,
) -> String {
    let hostname = render_hostname(hostname_template, pod_name, namespace);
    format!("{hostname}:{reverse_port}")
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ReverseTunnelAckAddrs {
    routing_target_addr: String,
    pylon_dial_addr: String,
}

fn derive_reverse_tunnel_ack_addrs(
    hostname_template: &str,
    namespace: &str,
    pod_name: &str,
    reverse_port: u16,
    reverse_tunnel_pylon_dial_addr: Option<&str>,
) -> ReverseTunnelAckAddrs {
    let routing_target_addr =
        derive_reverse_tunnel_target(hostname_template, namespace, pod_name, reverse_port);
    let pylon_dial_addr = reverse_tunnel_pylon_dial_addr
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
        .unwrap_or_else(|| routing_target_addr.clone());

    ReverseTunnelAckAddrs {
        routing_target_addr,
        pylon_dial_addr,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::http_proxy::ProxyTransportConfig;
    use stargate_proto::pb::StargateInfo;
    use std::sync::atomic::AtomicUsize;
    use tokio::time::Instant as TokioInstant;

    // ACTIVE_MODELS_SNAPSHOT_TASKS is process-global in unit tests, so serialize
    // runtime tests that assert exact snapshot-task counts.
    static SNAPSHOT_TASK_COUNTER_TEST_LOCK: tokio::sync::Mutex<()> =
        tokio::sync::Mutex::const_new(());

    #[test]
    fn derive_target_uses_template() {
        let result =
            derive_reverse_tunnel_target("{pod_name}.stargate.external", "ns", "stargate-0", 50072);
        assert_eq!(result, "stargate-0.stargate.external:50072");
    }

    #[test]
    fn derive_reverse_tunnel_ack_addrs_keep_routing_target_separate_from_pylon_dial_address() {
        let addrs = derive_reverse_tunnel_ack_addrs(
            "{pod_name}.stargate-headless.{namespace}.svc.cluster.local",
            "stargate",
            "stargate-0",
            50072,
            Some("stargate-quic-lb.stargate.svc.cluster.local:50072"),
        );

        assert_eq!(
            addrs.routing_target_addr,
            "stargate-0.stargate-headless.stargate.svc.cluster.local:50072"
        );
        assert_eq!(
            addrs.pylon_dial_addr,
            "stargate-quic-lb.stargate.svc.cluster.local:50072"
        );
    }

    #[test]
    fn derive_reverse_tunnel_ack_addrs_uses_routing_target_as_default_pylon_dial_address() {
        let addrs = derive_reverse_tunnel_ack_addrs(
            "{pod_name}.stargate-headless.{namespace}.svc.cluster.local",
            "stargate",
            "stargate-0",
            50072,
            None,
        );

        assert_eq!(
            addrs.routing_target_addr,
            "stargate-0.stargate-headless.stargate.svc.cluster.local:50072"
        );
        assert_eq!(addrs.pylon_dial_addr, addrs.routing_target_addr);
    }

    struct CountingDiscovery {
        active_count: Arc<AtomicUsize>,
        self_info: StargateInfo,
    }

    impl CountingDiscovery {
        fn new(active_count: Arc<AtomicUsize>, self_info: StargateInfo) -> Self {
            active_count.fetch_add(1, Ordering::SeqCst);
            Self {
                active_count,
                self_info,
            }
        }
    }

    impl Drop for CountingDiscovery {
        fn drop(&mut self) {
            self.active_count.fetch_sub(1, Ordering::SeqCst);
        }
    }

    #[async_trait::async_trait]
    impl Discovery for CountingDiscovery {
        fn initial_stargates(&self) -> Vec<StargateInfo> {
            vec![self.self_info.clone()]
        }

        async fn discover_stargates(&self) -> Vec<StargateInfo> {
            vec![self.self_info.clone()]
        }
    }

    struct BlockingDiscovery {
        active_calls: Arc<AtomicUsize>,
        self_info: StargateInfo,
    }

    struct ActiveDiscoveryCall {
        active_calls: Arc<AtomicUsize>,
    }

    impl Drop for ActiveDiscoveryCall {
        fn drop(&mut self) {
            self.active_calls.fetch_sub(1, Ordering::SeqCst);
        }
    }

    #[async_trait::async_trait]
    impl Discovery for BlockingDiscovery {
        fn initial_stargates(&self) -> Vec<StargateInfo> {
            vec![self.self_info.clone()]
        }

        async fn discover_stargates(&self) -> Vec<StargateInfo> {
            let _active_call = ActiveDiscoveryCall {
                active_calls: self.active_calls.clone(),
            };
            self.active_calls.fetch_add(1, Ordering::SeqCst);
            std::future::pending().await
        }
    }

    #[tokio::test]
    async fn start_failure_after_service_construction_stops_startup_tasks() {
        let _snapshot_counter_guard = SNAPSHOT_TASK_COUNTER_TEST_LOCK.lock().await;
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

        let snapshot_task_baseline = active_models_snapshot_task_count();
        let active_discoveries = Arc::new(AtomicUsize::new(0));
        let grpc_blocker = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let grpc_addr = grpc_blocker.local_addr().unwrap();
        let http_addr: SocketAddr = "127.0.0.1:0".parse().unwrap();
        let discovery = CountingDiscovery::new(
            active_discoveries.clone(),
            StargateInfo {
                stargate_id: "test-startup-cleanup".to_string(),
                advertise_addr: grpc_addr.to_string(),
                http_advertise_addr: http_addr.to_string(),
                grpc_pylon_dial_addr: String::new(),
            },
        );
        let runtime = StargateRuntime::new(
            StargateRuntimeConfig {
                stargate_id: "test-startup-cleanup".to_string(),
                grpc_listen_addr: grpc_addr,
                model_discovery_listen_addr: "127.0.0.1:0".parse().unwrap(),
                http_listen_addr: http_addr,
                advertise_addr: grpc_addr,
                stargate_discovery_dns_name: "localhost".to_string(),
                remote_watch_stargate_urls: Vec::new(),
                grpc_pylon_dial_addr: None,
                advertised_hostname_template: None,
                pod_name: None,
                pod_namespace: None,
                dns_poll_interval: Duration::from_secs(60),
                watch_heartbeat_interval: Duration::from_secs(60),
                registration_update_idle_timeout:
                    crate::control_plane::DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT,
                registration_update_max_idle_timeout:
                    crate::control_plane::DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
                proxy_transport: ProxyTransportConfig {
                    quic_connect_timeout: Duration::from_secs(5),
                    quic_request_timeout: Duration::from_secs(10),
                    tls_cert_pem: None,
                    server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                    direct_quic_connections: 1,
                    retry: Default::default(),
                },
                lb_config_path: None,
                metrics_prefix: crate::metrics::DEFAULT_PREFIX.to_string(),
                reverse_tunnel_listen_addr: None,
                reverse_tunnel_pylon_dial_addr: None,
                reverse_tunnel_connect_timeout: Duration::from_secs(10),
            },
            Box::new(discovery),
        );

        let result = runtime.start().await;
        assert!(result.is_err(), "occupied gRPC port should fail startup");

        wait_for_active_models_snapshot_task_count(snapshot_task_baseline, Duration::from_secs(2))
            .await;
        wait_for_count(&active_discoveries, 0, Duration::from_secs(2)).await;
    }

    #[tokio::test]
    async fn shutdown_cancels_in_flight_discovery_poll() {
        let _snapshot_counter_guard = SNAPSHOT_TASK_COUNTER_TEST_LOCK.lock().await;
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

        let active_calls = Arc::new(AtomicUsize::new(0));
        let grpc_addr: SocketAddr = "127.0.0.1:0".parse().unwrap();
        let http_addr: SocketAddr = "127.0.0.1:0".parse().unwrap();
        let runtime = StargateRuntime::new(
            StargateRuntimeConfig {
                stargate_id: "test-discovery-cancel".to_string(),
                grpc_listen_addr: grpc_addr,
                model_discovery_listen_addr: "127.0.0.1:0".parse().unwrap(),
                http_listen_addr: http_addr,
                advertise_addr: grpc_addr,
                stargate_discovery_dns_name: "localhost".to_string(),
                remote_watch_stargate_urls: Vec::new(),
                grpc_pylon_dial_addr: None,
                advertised_hostname_template: None,
                pod_name: None,
                pod_namespace: None,
                dns_poll_interval: Duration::from_secs(60),
                watch_heartbeat_interval: Duration::from_secs(60),
                registration_update_idle_timeout:
                    crate::control_plane::DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT,
                registration_update_max_idle_timeout:
                    crate::control_plane::DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
                proxy_transport: ProxyTransportConfig {
                    quic_connect_timeout: Duration::from_secs(5),
                    quic_request_timeout: Duration::from_secs(10),
                    tls_cert_pem: None,
                    server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                    direct_quic_connections: 1,
                    retry: Default::default(),
                },
                lb_config_path: None,
                metrics_prefix: crate::metrics::DEFAULT_PREFIX.to_string(),
                reverse_tunnel_listen_addr: None,
                reverse_tunnel_pylon_dial_addr: None,
                reverse_tunnel_connect_timeout: Duration::from_secs(10),
            },
            Box::new(BlockingDiscovery {
                active_calls: active_calls.clone(),
                self_info: StargateInfo {
                    stargate_id: "test-discovery-cancel".to_string(),
                    advertise_addr: grpc_addr.to_string(),
                    http_advertise_addr: http_addr.to_string(),
                    grpc_pylon_dial_addr: String::new(),
                },
            }),
        );

        let handle = runtime.start().await.expect("stargate should start");
        wait_for_count(&active_calls, 1, Duration::from_secs(2)).await;

        handle.begin_shutdown();
        assert!(
            handle.wait_for_shutdown(Duration::from_secs(2)).await,
            "shutdown should not wait for a blocked discovery call"
        );
        wait_for_count(&active_calls, 0, Duration::from_secs(2)).await;
    }

    fn active_models_snapshot_task_count() -> usize {
        ACTIVE_MODELS_SNAPSHOT_TASKS.load(Ordering::SeqCst)
    }

    async fn wait_for_active_models_snapshot_task_count(expected: usize, timeout: Duration) {
        wait_for_count(&ACTIVE_MODELS_SNAPSHOT_TASKS, expected, timeout).await;
    }

    async fn wait_for_count(count: &AtomicUsize, expected: usize, timeout: Duration) {
        let deadline = TokioInstant::now() + timeout;
        let mut interval = tokio::time::interval(Duration::from_millis(10));
        interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        loop {
            let actual = count.load(Ordering::SeqCst);
            if actual == expected {
                return;
            }
            assert!(
                TokioInstant::now() < deadline,
                "count stayed at {actual}, expected {expected}"
            );
            interval.tick().await;
        }
    }
}
