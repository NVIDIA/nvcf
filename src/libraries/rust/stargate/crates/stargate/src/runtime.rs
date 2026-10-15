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

mod server_tasks;

use std::net::{SocketAddr, TcpListener, UdpSocket};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, ensure};
use tracing::info;

use stargate_forwarding::ForwardingResolver;
use stargate_protocol::BackendConnectivity;
pub use stargate_runtime::CriticalTaskFailure;
use stargate_runtime::CriticalTaskGroup;

use crate::auth::WorkerAuthenticator;
use crate::control_plane::{
    RegistrationConnectionConfig, ReverseTunnelRegistrationConfig, StargateService,
    StargateServiceConfig,
};
use crate::discovery::Discovery;
use crate::http_proxy::{
    DebugConfig, ProxyAppState, ProxyTrafficState, ProxyTransportConfig, make_router,
};
use crate::load_balancer::{LoadBalancerConfig, LoadBalancerRouter};
use crate::metrics::StargateMetrics;
use crate::routing_state::StargateState;
use crate::tunnel::QuicHttpProxy;
use server_tasks::{
    into_tokio_tcp_listener, spawn_control_plane_grpc_server, spawn_http_proxy_server,
    spawn_model_discovery_grpc_server,
};

pub struct StargateRuntimeConfig {
    /// Stable process/pod identity used in logs, metrics, and routing snapshots.
    pub stargate_id: String,
    /// Local TCP socket for backend-facing `WatchStargates` and `RegisterInferenceServer`.
    pub grpc_listen_addr: SocketAddr,
    /// Local TCP socket for frontend-facing `ListModels`, separate from the backend control plane.
    pub model_discovery_listen_addr: SocketAddr,
    /// Local HTTP socket for OpenAI-compatible proxy traffic and health probes.
    pub http_listen_addr: SocketAddr,
    /// Optional local TCP socket for Prometheus metrics.
    pub metrics_listen_addr: Option<SocketAddr>,
    /// Discovery address before hostname rendering; outside Kubernetes this is usually `WatchStargates.stargates[*].advertise_addr`.
    pub advertise_addr: SocketAddr,
    /// Peer-discovery DNS name; in Kubernetes this must be the headless Service controlling ready endpoint visibility.
    pub stargate_discovery_dns_name: String,
    /// Remote-region recursive watch seeds; pylons register to returned Stargates, not these URLs.
    pub remote_watch_stargate_urls: Vec<String>,
    /// Optional pylon TCP load-balancer address; per-pod `advertise_addr` remains the authority/SNI identity.
    pub grpc_pylon_dial_addr: Option<String>,
    /// Poll cadence for DNS-based peer discovery.
    pub dns_poll_interval: Duration,
    /// Maximum interval between unchanged `WatchStargates` snapshots.
    pub watch_heartbeat_interval: Duration,
    /// Minimum heartbeat-aware registration idle timeout; zero disables enforcement.
    pub registration_update_idle_timeout: Duration,
    /// Registration idle-time hard cap and fallback without heartbeat hints; zero disables enforcement.
    pub registration_update_max_idle_timeout: Duration,
    /// QUIC, TLS, tunnel-protocol, and retry configuration for backend request forwarding.
    pub proxy_transport: ProxyTransportConfig,
    /// Optional JSON load-balancer config path.
    pub lb_config_path: Option<String>,
    /// Prefix prepended to Prometheus metric names.
    pub metrics_prefix: String,
    /// Optional resolver for the development-only peer relay.
    pub forwarding: Option<Arc<dyn ForwardingResolver>>,
    /// Authenticates worker registrations and tunneled requests.
    pub authenticator: Arc<dyn WorkerAuthenticator>,
}

pub struct ReverseTunnelConfig {
    socket: UdpSocket,
    /// Already-rendered hostname used as reverse QUIC SNI and routing identity.
    pub advertised_host: String,
    /// Optional externally reachable UDP load-balancer address for pylons.
    pub pylon_dial_addr: Option<String>,
    /// How long registration waits for a reverse connection after advertising.
    pub connect_timeout: Duration,
}

/// Process-lifetime TCP listeners, bound before [`StargateRuntime`] construction and consumed without rebinding during `start()`.
pub struct BoundStargateListeners {
    grpc_listener: TcpListener,
    model_discovery_listener: TcpListener,
    http_listener: TcpListener,
    metrics_listener: Option<TcpListener>,
}

impl BoundStargateListeners {
    pub fn bind(config: &mut StargateRuntimeConfig) -> Result<Self> {
        let configured_grpc_port = config.grpc_listen_addr.port();
        let grpc_listener = bind_tcp_listener(&mut config.grpc_listen_addr, "gRPC")?;
        if config.advertise_addr.port() == configured_grpc_port {
            config
                .advertise_addr
                .set_port(config.grpc_listen_addr.port());
        }

        let model_discovery_listener =
            bind_tcp_listener(&mut config.model_discovery_listen_addr, "model-discovery")?;
        let http_listener = bind_tcp_listener(&mut config.http_listen_addr, "HTTP")?;
        let metrics_listener = config
            .metrics_listen_addr
            .as_mut()
            .map(|address| bind_tcp_listener(address, "metrics"))
            .transpose()?;

        Ok(Self {
            grpc_listener,
            model_discovery_listener,
            http_listener,
            metrics_listener,
        })
    }

    /// Adopts external listeners after verifying every configured address matches its handle.
    pub fn from_prebound(
        config: &StargateRuntimeConfig,
        grpc_listener: TcpListener,
        model_discovery_listener: TcpListener,
        http_listener: TcpListener,
        metrics_listener: Option<TcpListener>,
    ) -> Result<Self> {
        for (listener, configured_addr, name) in [
            (&grpc_listener, config.grpc_listen_addr, "gRPC"),
            (
                &model_discovery_listener,
                config.model_discovery_listen_addr,
                "model-discovery",
            ),
            (&http_listener, config.http_listen_addr, "HTTP"),
        ] {
            validate_bound_tcp_listener(listener, configured_addr, name)?;
        }

        ensure!(
            config.metrics_listen_addr.is_some() == metrics_listener.is_some(),
            "pre-bound metrics listener state must match metrics configuration"
        );
        if let Some((configured_addr, listener)) =
            config.metrics_listen_addr.zip(metrics_listener.as_ref())
        {
            validate_bound_tcp_listener(listener, configured_addr, "metrics")?;
        }

        Ok(Self {
            grpc_listener,
            model_discovery_listener,
            http_listener,
            metrics_listener,
        })
    }

    pub fn grpc_addr(&self) -> SocketAddr {
        bound_tcp_addr(&self.grpc_listener, "gRPC")
    }

    pub fn model_discovery_addr(&self) -> SocketAddr {
        bound_tcp_addr(&self.model_discovery_listener, "model-discovery")
    }

    pub fn http_addr(&self) -> SocketAddr {
        bound_tcp_addr(&self.http_listener, "HTTP")
    }

    pub fn metrics_addr(&self) -> Option<SocketAddr> {
        self.metrics_listener
            .as_ref()
            .map(|listener| bound_tcp_addr(listener, "metrics"))
    }
}

fn bind_tcp_listener(address: &mut SocketAddr, name: &'static str) -> Result<TcpListener> {
    let listener =
        TcpListener::bind(*address).with_context(|| format!("failed to bind {name} listener"))?;
    *address = listener_addr(&listener, name)?;
    Ok(listener)
}

fn bound_tcp_addr(listener: &TcpListener, name: &'static str) -> SocketAddr {
    listener_addr(listener, name).expect("bound listener must retain its local address")
}

fn listener_addr(listener: &TcpListener, name: &'static str) -> Result<SocketAddr> {
    listener
        .local_addr()
        .with_context(|| format!("failed to read {name} listener address"))
}

fn validate_bound_tcp_listener(
    listener: &TcpListener,
    configured_addr: SocketAddr,
    name: &'static str,
) -> Result<()> {
    let bound_addr = listener_addr(listener, name)?;
    ensure!(
        bound_addr == configured_addr,
        "supplied {name} listener address {bound_addr} does not match configured address {configured_addr}"
    );
    Ok(())
}

/// Owns process-lifetime services, bound listeners, and coordinated critical-task shutdown.
pub struct StargateRuntime {
    config: StargateRuntimeConfig,
    discovery: Box<dyn Discovery>,
    listeners: BoundStargateListeners,
    reverse_tunnel: Option<ReverseTunnelConfig>,
}

impl StargateRuntime {
    pub fn new(
        config: StargateRuntimeConfig,
        discovery: Box<dyn Discovery>,
        listeners: BoundStargateListeners,
        reverse_tunnel: Option<ReverseTunnelConfig>,
    ) -> Self {
        Self {
            config,
            discovery,
            listeners,
            reverse_tunnel,
        }
    }

    pub async fn start(self) -> Result<StargateHandle> {
        let grpc_listen_addr = self.listeners.grpc_addr();
        let model_discovery_listen_addr = self.listeners.model_discovery_addr();
        let http_listen_addr = self.listeners.http_addr();
        let metrics_listen_addr = self.listeners.metrics_addr();
        let BoundStargateListeners {
            grpc_listener,
            model_discovery_listener,
            http_listener,
            metrics_listener,
        } = self.listeners;

        let metrics = StargateMetrics::new_with_prefix(&self.config.metrics_prefix)
            .context("failed to create prometheus metrics registry")?;
        let metrics_listener = metrics_listener
            .map(|listener| into_tokio_tcp_listener(listener, "metrics"))
            .transpose()?;
        let grpc_listener = into_tokio_tcp_listener(grpc_listener, "gRPC")?;
        let model_discovery_listener =
            into_tokio_tcp_listener(model_discovery_listener, "model-discovery")?;
        let http_listener = into_tokio_tcp_listener(http_listener, "HTTP")?;

        let quic_proxy = QuicHttpProxy::new(
            self.config.proxy_transport.quic.clone(),
            self.config.authenticator.clone(),
        )
        .context("failed to initialize quic proxy")?;
        let quic_proxy = Arc::new(quic_proxy);
        let shared_state = Arc::new(StargateState::new_with_metrics(metrics.clone()));

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

        let (tasks, critical_failure_rx) = CriticalTaskGroup::new("stargate");
        let (backend_connectivity, reverse_tunnel) = match self.reverse_tunnel {
            Some(reverse_tunnel) => {
                let registration_config = reverse_tunnel.registration_config();
                quic_proxy
                    .start_reverse_listener(
                        shared_state.clone(),
                        tasks.clone(),
                        self.config.forwarding.clone(),
                        reverse_tunnel.socket,
                    )
                    .await
                    .inspect_err(|_| tasks.begin_shutdown())
                    .context("failed to start reverse tunnel listener")?;
                (BackendConnectivity::Reverse, Some(registration_config))
            }
            None => (BackendConnectivity::Direct, None),
        };
        let registration_connection_config = RegistrationConnectionConfig {
            quic_proxy: quic_proxy.clone(),
            reverse_tunnel,
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
            tasks: tasks.clone(),
            registration_update_idle_timeout: self.config.registration_update_idle_timeout,
            registration_update_max_idle_timeout: self.config.registration_update_max_idle_timeout,
            state: shared_state.clone(),
            registration_connection_config,
            forwarding: self.config.forwarding,
            authenticator: self.config.authenticator,
        });

        let proxy_router = make_router(ProxyAppState {
            state: service.state(),
            traffic: ProxyTrafficState {
                shutdown: tasks.shutdown_signal(),
            },
            quic_proxy,
            lb_router,
            metrics: metrics.clone(),
            retry: self.config.proxy_transport.retry.clone(),
            debug_config: DebugConfig {
                stargate_id: self.config.stargate_id.clone(),
                grpc_listen_addr: grpc_listen_addr.to_string(),
                model_discovery_listen_addr: model_discovery_listen_addr.to_string(),
                http_listen_addr: http_listen_addr.to_string(),
                metrics_listen_addr: metrics_listen_addr.map(|addr| addr.to_string()),
                advertise_addr: self.config.advertise_addr.to_string(),
                stargate_discovery_dns_name: self.config.stargate_discovery_dns_name.clone(),
                tunnel_protocol: self.config.proxy_transport.quic.tunnel_protocol.to_string(),
                backend_connectivity,
                direct_quic_connections: self.config.proxy_transport.quic.direct_quic_connections,
            },
        });

        if let Some(metrics_listener) = metrics_listener {
            let metrics_registry = metrics.registry();
            tasks.spawn_critical("metrics server", move |stop| {
                crate::metrics::start_metrics_server(metrics_listener, metrics_registry, stop)
            });
        }
        spawn_control_plane_grpc_server(&tasks, grpc_listener, service.clone());

        spawn_model_discovery_grpc_server(&tasks, model_discovery_listener, service);

        spawn_http_proxy_server(&tasks, http_listener, proxy_router);

        Ok(StargateHandle {
            tasks,
            critical_failure_rx,
            metrics,
            state: shared_state,
        })
    }
}

pub struct StargateHandle {
    tasks: CriticalTaskGroup,
    critical_failure_rx: flume::Receiver<CriticalTaskFailure>,
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
        if !self.tasks.is_stopping() {
            info!("Entering draining mode");
            self.tasks.begin_shutdown();
        }
    }

    pub async fn wait_for_critical_failure(&self) -> CriticalTaskFailure {
        self.critical_failure_rx
            .recv_async()
            .await
            .expect("runtime task group outlives its critical-failure receiver")
    }

    pub async fn wait_for_shutdown(&self, timeout: Duration) -> bool {
        tokio::select! {
            _ = self.tasks.wait() => true,
            _ = tokio::time::sleep(timeout) => false,
        }
    }
}

impl Drop for StargateHandle {
    fn drop(&mut self) {
        self.tasks.begin_shutdown();
    }
}

impl ReverseTunnelConfig {
    pub fn bind(
        listen_addr: SocketAddr,
        advertised_host: String,
        pylon_dial_addr: Option<String>,
        connect_timeout: Duration,
    ) -> Result<Self> {
        let socket =
            UdpSocket::bind(listen_addr).context("failed to bind reverse tunnel listener")?;
        Ok(Self::from_bound_socket(
            socket,
            advertised_host,
            pylon_dial_addr,
            connect_timeout,
        ))
    }

    /// Adopts a UDP listener already bound by an external owner.
    pub fn from_bound_socket(
        socket: UdpSocket,
        advertised_host: String,
        pylon_dial_addr: Option<String>,
        connect_timeout: Duration,
    ) -> Self {
        Self {
            socket,
            advertised_host,
            pylon_dial_addr,
            connect_timeout,
        }
    }

    pub fn listen_addr(&self) -> SocketAddr {
        self.socket
            .local_addr()
            .expect("bound reverse tunnel listener must retain its local address")
    }

    fn registration_config(&self) -> ReverseTunnelRegistrationConfig {
        let bound_port = self.listen_addr().port();
        let target = format!("{}:{bound_port}", self.advertised_host);
        let pylon_dial_addr = self
            .pylon_dial_addr
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .unwrap_or(&target)
            .to_owned();
        ReverseTunnelRegistrationConfig {
            target,
            pylon_dial_addr,
            connect_timeout: self.connect_timeout,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::http_proxy::ProxyTransportConfig;
    use stargate_proto::pb::StargateInfo;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use tokio::sync::oneshot;
    use tokio::time::Instant as TokioInstant;

    fn test_runtime_config(stargate_id: &str) -> StargateRuntimeConfig {
        StargateRuntimeConfig {
            stargate_id: stargate_id.to_string(),
            grpc_listen_addr: "127.0.0.1:0".parse().unwrap(),
            model_discovery_listen_addr: "127.0.0.1:0".parse().unwrap(),
            http_listen_addr: "127.0.0.1:0".parse().unwrap(),
            metrics_listen_addr: None,
            advertise_addr: "127.0.0.1:0".parse().unwrap(),
            stargate_discovery_dns_name: "localhost".to_string(),
            remote_watch_stargate_urls: Vec::new(),
            grpc_pylon_dial_addr: None,
            dns_poll_interval: Duration::from_secs(60),
            watch_heartbeat_interval: Duration::from_secs(60),
            registration_update_idle_timeout:
                crate::control_plane::DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT,
            registration_update_max_idle_timeout:
                crate::control_plane::DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
            proxy_transport: ProxyTransportConfig {
                quic: crate::tunnel::QuicTunnelConfig {
                    connect_timeout: Duration::from_secs(5),
                    request_timeout: Duration::from_secs(10),
                    tls_cert_pem: None,
                    server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                    direct_quic_connections: 1,
                },
                retry: Default::default(),
            },
            lb_config_path: None,
            metrics_prefix: crate::metrics::DEFAULT_PREFIX.to_string(),
            forwarding: None,
            authenticator: Arc::new(crate::auth::OpenAuthenticator),
        }
    }

    fn test_handle(
        tasks: CriticalTaskGroup,
        critical_failure_rx: flume::Receiver<CriticalTaskFailure>,
    ) -> StargateHandle {
        StargateHandle {
            tasks,
            critical_failure_rx,
            metrics: StargateMetrics::new().expect("metrics should initialize"),
            state: Arc::new(StargateState::new()),
        }
    }

    #[test]
    fn reverse_tunnel_config_owns_its_bound_socket() {
        let config = ReverseTunnelConfig::bind(
            "127.0.0.1:0".parse().unwrap(),
            "localhost".to_string(),
            None,
            Duration::from_secs(10),
        )
        .expect("reverse tunnel listener should bind");

        let bound_addr = config.listen_addr();
        assert_ne!(bound_addr.port(), 0, "the OS must select a concrete port");
        assert!(
            std::net::UdpSocket::bind(bound_addr).is_err(),
            "the configuration must retain its reverse listener socket"
        );
    }

    #[test]
    fn bound_listeners_hold_the_effective_server_addresses() {
        let mut config = test_runtime_config("test-bound-listeners");
        config.metrics_listen_addr = Some("127.0.0.1:0".parse().unwrap());

        let listeners = BoundStargateListeners::bind(&mut config)
            .expect("binding the test listener set should succeed");

        assert_eq!(config.grpc_listen_addr, listeners.grpc_addr());
        assert_eq!(
            config.model_discovery_listen_addr,
            listeners.model_discovery_addr()
        );
        assert_eq!(config.http_listen_addr, listeners.http_addr());
        assert_eq!(config.metrics_listen_addr, listeners.metrics_addr());
        assert_eq!(config.advertise_addr.port(), config.grpc_listen_addr.port());

        for address in [
            listeners.grpc_addr(),
            listeners.model_discovery_addr(),
            listeners.http_addr(),
            listeners.metrics_addr().expect("metrics are enabled"),
        ] {
            assert_ne!(address.port(), 0, "the OS must select a concrete port");
            assert!(
                std::net::TcpListener::bind(address).is_err(),
                "the bound listener must keep {address} reserved"
            );
        }
    }

    #[test]
    fn reverse_tunnel_registration_config_keeps_target_separate_from_pylon_dial_address() {
        let reverse_tunnel = ReverseTunnelConfig::bind(
            "127.0.0.1:0".parse().unwrap(),
            "stargate-0.stargate-headless.stargate.svc.cluster.local".to_string(),
            Some("stargate-quic-lb.stargate.svc.cluster.local:50072".to_string()),
            Duration::from_secs(10),
        )
        .expect("reverse tunnel listener should bind");
        let port = reverse_tunnel.listen_addr().port();
        let config = reverse_tunnel.registration_config();

        assert_eq!(
            config.target,
            format!("stargate-0.stargate-headless.stargate.svc.cluster.local:{port}")
        );
        assert_eq!(
            config.pylon_dial_addr,
            "stargate-quic-lb.stargate.svc.cluster.local:50072"
        );
        assert_eq!(config.connect_timeout, Duration::from_secs(10));
    }

    #[test]
    fn reverse_tunnel_registration_config_uses_target_as_default_pylon_dial_address() {
        let reverse_tunnel = ReverseTunnelConfig::bind(
            "127.0.0.1:0".parse().unwrap(),
            "stargate-0.stargate-headless.stargate.svc.cluster.local".to_string(),
            Some("   ".to_string()),
            Duration::from_secs(10),
        )
        .expect("reverse tunnel listener should bind");
        let port = reverse_tunnel.listen_addr().port();
        let config = reverse_tunnel.registration_config();

        assert_eq!(
            config.target,
            format!("stargate-0.stargate-headless.stargate.svc.cluster.local:{port}")
        );
        assert_eq!(config.pylon_dial_addr, config.target);
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

    #[test]
    fn listener_binding_failure_happens_before_runtime_construction() {
        let grpc_blocker = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let grpc_addr = grpc_blocker.local_addr().unwrap();
        let mut config = test_runtime_config("test-listener-binding");
        config.grpc_listen_addr = grpc_addr;
        config.advertise_addr = grpc_addr;

        let error = match BoundStargateListeners::bind(&mut config) {
            Ok(_) => panic!("occupied gRPC port must fail listener binding"),
            Err(error) => error,
        };

        assert!(error.to_string().contains("gRPC"));
    }

    #[tokio::test]
    async fn shutdown_cancels_in_flight_discovery_poll() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

        let active_calls = Arc::new(AtomicUsize::new(0));
        let mut config = test_runtime_config("test-discovery-cancel");
        let listeners =
            BoundStargateListeners::bind(&mut config).expect("test listeners should bind");
        let grpc_addr = config.grpc_listen_addr;
        let http_addr = config.http_listen_addr;
        let runtime = StargateRuntime::new(
            config,
            Box::new(BlockingDiscovery {
                active_calls: active_calls.clone(),
                self_info: StargateInfo {
                    stargate_id: "test-discovery-cancel".to_string(),
                    advertise_addr: grpc_addr.to_string(),
                    http_advertise_addr: http_addr.to_string(),
                    grpc_pylon_dial_addr: String::new(),
                },
            }),
            listeners,
            None,
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

    #[tokio::test]
    async fn handle_surfaces_critical_root_failure_and_stops_runtime() {
        let (tasks, critical_failure_rx) = CriticalTaskGroup::new("stargate");
        let handle = test_handle(tasks.clone(), critical_failure_rx);
        tasks.spawn_critical("test critical root", |_| async { Ok(()) });

        let failure =
            tokio::time::timeout(Duration::from_secs(1), handle.wait_for_critical_failure())
                .await
                .expect("critical root failure should reach the handle");

        assert_eq!(failure.task_name(), "test critical root");
        assert_eq!(failure.detail(), "exited unexpectedly");
        assert!(
            handle.wait_for_shutdown(Duration::from_secs(1)).await,
            "critical root failure should stop the runtime tree"
        );
    }

    #[tokio::test]
    async fn dropping_handle_cancels_owned_runtime_work() {
        let (tasks, critical_failure_rx) = CriticalTaskGroup::new("stargate");
        let stop = tasks.shutdown_signal();
        let (stopped_tx, stopped_rx) = oneshot::channel();
        tasks.task_tracker().spawn(async move {
            stop.cancelled().await;
            let _ = stopped_tx.send(());
        });
        let handle = test_handle(tasks, critical_failure_rx);

        // Dropping the runtime owner is the behavior under test.
        drop(handle);

        tokio::time::timeout(Duration::from_secs(1), stopped_rx)
            .await
            .expect("dropping the handle should cancel owned runtime work")
            .expect("owned runtime work should publish completion");
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
