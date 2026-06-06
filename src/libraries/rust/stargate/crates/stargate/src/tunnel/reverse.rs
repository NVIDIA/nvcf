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

use std::collections::HashSet;
use std::future::Future;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use anyhow::{Context, Result, bail};
use axum::http::{Method, StatusCode};
use quinn::{Connection, Endpoint, EndpointConfig};
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;
use tracing::{Instrument, info, info_span, warn};

use stargate_forwarding::{self as forwarding, ForwardingResolver, PeerResolution};
use stargate_protocol::TunnelTransportProtocol;
use stargate_protocol::tunnel_contract::{
    HEADER_INFERENCE_SERVER_ID, HEADER_REVERSE_AUTH_TOKEN, WEBTRANSPORT_TUNNEL_PATH,
};

use crate::routing_state::StargateState;

use super::QuicHttpProxy;
use super::direct::{
    TunnelConnection, TunnelConnectionSet, build_client_config, build_server_config,
};
use super::http3::{H3ServerRequestStream, h3_error};

#[derive(Clone)]
pub(super) struct ReverseConnectionEvents {
    updates: watch::Sender<u64>,
}

struct ReverseConnectionEventReceiver {
    updates: watch::Receiver<u64>,
}

impl ReverseConnectionEvents {
    pub(super) fn new() -> Self {
        let (updates, _) = watch::channel(0);
        Self { updates }
    }

    fn subscribe(&self) -> ReverseConnectionEventReceiver {
        ReverseConnectionEventReceiver {
            updates: self.updates.subscribe(),
        }
    }

    pub(super) fn notify_changed(&self) {
        self.updates
            .send_modify(|version| *version = version.wrapping_add(1));
    }

    pub(super) async fn wait_until<Check, Fut>(
        &self,
        timeout: Duration,
        mut is_ready: Check,
    ) -> bool
    where
        Check: FnMut() -> Fut,
        Fut: Future<Output = bool>,
    {
        let mut connection_events = self.subscribe();
        tokio::time::timeout(timeout, async {
            loop {
                if is_ready().await {
                    return true;
                }
                if !connection_events.changed().await {
                    return false;
                }
            }
        })
        .await
        .unwrap_or(false)
    }
}

impl ReverseConnectionEventReceiver {
    async fn changed(&mut self) -> bool {
        self.updates.changed().await.is_ok()
    }
}
struct PendingReverseConnectionGuard {
    pending: Arc<Mutex<HashSet<String>>>,
    inference_server_id: String,
}

impl Drop for PendingReverseConnectionGuard {
    fn drop(&mut self) {
        lock_pending_reverse_connections(&self.pending).remove(&self.inference_server_id);
    }
}

fn lock_pending_reverse_connections(
    pending: &Mutex<HashSet<String>>,
) -> std::sync::MutexGuard<'_, HashSet<String>> {
    match pending.lock() {
        Ok(guard) => guard,
        Err(poisoned) => poisoned.into_inner(),
    }
}

impl QuicHttpProxy {
    pub async fn await_reverse_connection(
        &self,
        inference_server_id: &str,
        timeout: Duration,
    ) -> bool {
        let pool = self.pool.clone();
        let inference_server_id = inference_server_id.to_string();
        self.reverse_connection_events
            .wait_until(timeout, move || {
                let pool = pool.clone();
                let inference_server_id = inference_server_id.clone();
                async move {
                    pool.read()
                        .await
                        .get(&inference_server_id)
                        .is_some_and(TunnelConnectionSet::is_healthy)
                }
            })
            .await
    }

    pub async fn store_reverse_connection(
        &self,
        inference_server_id: &str,
        connection: Connection,
    ) -> bool {
        if self.has_healthy_connection(inference_server_id).await {
            return false;
        }
        let Some(_pending_guard) = self.try_mark_pending_reverse_connection(inference_server_id)
        else {
            return false;
        };

        let tunnel_connection = match self.build_reverse_tunnel_connection(connection).await {
            Ok(connection) => connection,
            Err(error) => {
                warn!(
                    inference_server_id = %inference_server_id,
                    error = %error,
                    "failed to initialize tunnel connection"
                );
                return false;
            }
        };
        self.store_built_reverse_connection(inference_server_id, tunnel_connection)
            .await
    }

    async fn store_built_reverse_connection(
        &self,
        inference_server_id: &str,
        tunnel_connection: TunnelConnection,
    ) -> bool {
        let mut pool = self.pool.write().await;
        if let Some(existing) = pool.get(inference_server_id)
            && existing.is_healthy()
        {
            return false;
        }
        pool.insert(
            inference_server_id.to_string(),
            TunnelConnectionSet::single(tunnel_connection),
        );
        self.reverse_connection_events.notify_changed();
        true
    }

    fn try_mark_pending_reverse_connection(
        &self,
        inference_server_id: &str,
    ) -> Option<PendingReverseConnectionGuard> {
        let mut pending = lock_pending_reverse_connections(&self.pending_reverse_connections);
        if !pending.insert(inference_server_id.to_string()) {
            return None;
        }
        Some(PendingReverseConnectionGuard {
            pending: self.pending_reverse_connections.clone(),
            inference_server_id: inference_server_id.to_string(),
        })
    }

    async fn build_reverse_tunnel_connection(
        &self,
        connection: Connection,
    ) -> Result<TunnelConnection> {
        match self.config.tunnel_protocol {
            TunnelTransportProtocol::Custom => Ok(TunnelConnection::Custom(connection)),
            TunnelTransportProtocol::Http3 => self.build_h3_client_connection(connection).await,
            TunnelTransportProtocol::WebTransport => {
                bail!("reverse WebTransport connections are established by CONNECT handshake")
            }
        }
    }

    pub async fn start_reverse_listener(
        self: &Arc<Self>,
        listen_addr: SocketAddr,
        state: Arc<StargateState>,
        shutdown: CancellationToken,
        task_tracker: TaskTracker,
        forwarding: Option<Arc<dyn ForwardingResolver>>,
        pre_bound_socket: Option<std::net::UdpSocket>,
    ) -> Result<SocketAddr> {
        let server_config = build_server_config(
            &self.config.server_tls_identity,
            self.config.tunnel_protocol,
        )?;
        let endpoint = match pre_bound_socket {
            Some(socket) => {
                socket
                    .set_nonblocking(true)
                    .context("set reverse listener socket to non-blocking")?;
                let runtime =
                    quinn::default_runtime().context("no async runtime for quinn endpoint")?;
                Endpoint::new(
                    EndpointConfig::default(),
                    Some(server_config),
                    socket,
                    runtime,
                )
                .context("create reverse listener from pre-bound socket")?
            }
            None => {
                Endpoint::server(server_config, listen_addr).context("bind reverse listener")?
            }
        };
        let bound_addr = endpoint
            .local_addr()
            .context("reverse listener local addr")?;

        let relay_client_config = build_client_config(
            self.config.tls_cert_pem.as_deref(),
            self.config.quic_insecure,
            self.config.tunnel_protocol,
        )?;
        let relay_endpoints = Arc::new(
            forwarding::build_relay_endpoints(
                forwarding::RelayEndpointConfig::default(),
                relay_client_config,
            )
            .context("build relay endpoints")?,
        );

        let proxy = self.clone();
        let listener_tasks = task_tracker.clone();
        let listener_span = info_span!("reverse_tunnel_listener", addr = %bound_addr);
        task_tracker.spawn(
            async move {
                loop {
                    tokio::select! {
                        _ = shutdown.cancelled() => break,
                        incoming = endpoint.accept() => {
                            let Some(incoming) = incoming else { break };
                            let proxy = proxy.clone();
                            let state = state.clone();
                            let forwarding = forwarding.clone();
                            let relay_endpoints = relay_endpoints.clone();
                            let port = bound_addr.port();
                            let peer_connect_timeout = proxy.config.connect_timeout;
                            let connection_tasks = listener_tasks.clone();
                            let connection_span = info_span!("reverse_tunnel_connection", port);
                            listener_tasks.spawn(async move {
                                let dispatch = ReverseDispatchContext {
                                    proxy: &proxy,
                                    state: &state,
                                    forwarding: forwarding.as_deref(),
                                    relay_endpoints: &relay_endpoints,
                                    listen_port: port,
                                    peer_connect_timeout,
                                    task_tracker: &connection_tasks,
                                };
                                if let Err(e) = dispatch_incoming(incoming, dispatch).await {
                                    warn!(error = %e, "reverse tunnel connection failed");
                                }
                            }.instrument(connection_span));
                        }
                    }
                }
                endpoint.close(0u32.into(), b"shutdown");
            }
            .instrument(listener_span),
        );

        info!(addr = %bound_addr, "reverse tunnel listener started");
        Ok(bound_addr)
    }
}

struct ReverseDispatchContext<'a> {
    proxy: &'a QuicHttpProxy,
    state: &'a StargateState,
    forwarding: Option<&'a dyn ForwardingResolver>,
    relay_endpoints: &'a forwarding::RelayEndpoints,
    listen_port: u16,
    peer_connect_timeout: Duration,
    task_tracker: &'a TaskTracker,
}

async fn dispatch_incoming(
    incoming: quinn::Incoming,
    dispatch: ReverseDispatchContext<'_>,
) -> Result<()> {
    let connection = incoming.await.context("accept reverse connection")?;

    if let Some(fwd) = dispatch.forwarding {
        let sni = connection
            .handshake_data()
            .and_then(|data| data.downcast::<quinn::crypto::rustls::HandshakeData>().ok())
            .and_then(|hd| hd.server_name);

        if let Some(sni) = sni {
            match fwd.resolve_peer(&sni, dispatch.listen_port) {
                PeerResolution::Peer(peer) => {
                    info!(
                        peer = %peer.dial_addr,
                        server_name = %peer.server_name,
                        sni = %sni,
                        "relaying QUIC connection to peer"
                    );
                    return forwarding::forward_quic_connection(
                        connection,
                        &peer,
                        dispatch.relay_endpoints,
                        dispatch.peer_connect_timeout,
                    )
                    .await;
                }
                PeerResolution::Local | PeerResolution::NotPeer => {}
            }
        }
    }

    match dispatch.proxy.config.tunnel_protocol {
        TunnelTransportProtocol::WebTransport => {
            handle_reverse_webtransport_connect(
                connection,
                dispatch.proxy,
                dispatch.state,
                dispatch.task_tracker,
            )
            .await
        }
        TunnelTransportProtocol::Custom | TunnelTransportProtocol::Http3 => {
            handle_reverse_handshake(
                connection,
                dispatch.proxy,
                dispatch.state,
                dispatch.task_tracker,
            )
            .await
        }
    }
}

async fn handle_reverse_webtransport_connect(
    connection: Connection,
    proxy: &QuicHttpProxy,
    state: &StargateState,
    task_tracker: &TaskTracker,
) -> Result<()> {
    let mut h3_connection = h3::server::builder()
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1)
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(h3_error)
        .context("create reverse WebTransport h3 server")?;
    let Some(resolver) = h3_connection
        .accept()
        .await
        .map_err(h3_error)
        .context("accept reverse WebTransport CONNECT")?
    else {
        bail!("reverse WebTransport connection closed before CONNECT");
    };
    let (request, mut stream) = resolver
        .resolve_request()
        .await
        .map_err(h3_error)
        .context("resolve reverse WebTransport CONNECT")?;

    let is_webtransport = request
        .extensions()
        .get::<h3::ext::Protocol>()
        .is_some_and(|protocol| *protocol == h3::ext::Protocol::WEB_TRANSPORT);
    if request.method() != Method::CONNECT
        || request.uri().path() != WEBTRANSPORT_TUNNEL_PATH
        || !is_webtransport
    {
        send_webtransport_connect_response(&mut stream, StatusCode::BAD_REQUEST).await?;
        bail!("invalid reverse WebTransport CONNECT request");
    }

    let Some(inference_server_id) = request
        .headers()
        .get(HEADER_INFERENCE_SERVER_ID)
        .and_then(|value| value.to_str().ok())
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
    else {
        send_webtransport_connect_response(&mut stream, StatusCode::BAD_REQUEST).await?;
        bail!("reverse WebTransport CONNECT missing {HEADER_INFERENCE_SERVER_ID}");
    };
    let auth_token = request
        .headers()
        .get(HEADER_REVERSE_AUTH_TOKEN)
        .and_then(|value| value.to_str().ok())
        .map(ToOwned::to_owned);

    let result = match proxy
        .authenticator
        .authenticate(auth_token.as_deref())
        .await
    {
        Ok(result) => result,
        Err(error) => {
            warn!(
                inference_server_id = %inference_server_id,
                error = %error,
                "reverse WebTransport authentication failed"
            );
            send_webtransport_connect_response(&mut stream, StatusCode::UNAUTHORIZED).await?;
            bail!("authentication failed for reverse WebTransport: {inference_server_id}");
        }
    };

    let Some(registration) = state.registered_reverse_tunnel(&inference_server_id).await else {
        send_webtransport_connect_response(&mut stream, StatusCode::NOT_FOUND).await?;
        bail!("unauthorized inference_server_id in reverse WebTransport: {inference_server_id}");
    };

    if result.routing_key != registration.routing_key {
        send_webtransport_connect_response(&mut stream, StatusCode::FORBIDDEN).await?;
        bail!("QUIC routing_key does not match gRPC registration: {inference_server_id}");
    }

    if proxy.has_healthy_connection(&inference_server_id).await {
        send_webtransport_connect_response(&mut stream, StatusCode::CONFLICT).await?;
        bail!("duplicate reverse WebTransport connection for: {inference_server_id}");
    }
    let Some(_pending_guard) = proxy.try_mark_pending_reverse_connection(&inference_server_id)
    else {
        send_webtransport_connect_response(&mut stream, StatusCode::CONFLICT).await?;
        bail!("pending duplicate reverse WebTransport connection for: {inference_server_id}");
    };

    let tunnel_connection = proxy
        .build_webtransport_server_connection(connection.clone(), h3_connection, stream)
        .await?;
    if !proxy
        .store_built_reverse_connection(&inference_server_id, tunnel_connection)
        .await
    {
        bail!("duplicate reverse WebTransport connection for: {inference_server_id}");
    }

    info!(inference_server_id = %inference_server_id, "reverse WebTransport tunnel established");
    let pool = proxy.pool.clone();
    let closed_id = connection.stable_id();
    let cleanup_span = info_span!(
        "reverse_webtransport_connection_cleanup",
        inference_server_id = %inference_server_id,
        stable_id = closed_id,
    );
    task_tracker.spawn(async move {
        connection.closed().await;
        let mut guard = pool.write().await;
        let is_current = guard
            .get(&inference_server_id)
            .is_some_and(|conn| conn.contains_stable_id(closed_id));
        if is_current {
            guard.remove(&inference_server_id);
            warn!(inference_server_id = %inference_server_id, "reverse WebTransport connection closed, removed from pool");
        }
    }.instrument(cleanup_span));

    Ok(())
}

async fn send_webtransport_connect_response(
    stream: &mut H3ServerRequestStream,
    status: StatusCode,
) -> Result<()> {
    let response = http::Response::builder()
        .status(status)
        .body(())
        .context("build WebTransport CONNECT rejection")?;
    stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("send WebTransport CONNECT response")?;
    stream
        .finish()
        .await
        .map_err(h3_error)
        .context("finish WebTransport CONNECT response")
}

async fn send_handshake_nack(
    send: &mut quinn::SendStream,
    reason: &str,
    delivery_timeout: Duration,
) -> Result<()> {
    let ack = stargate_protocol::HandshakeAck {
        accepted: false,
        reason: reason.to_string(),
    };
    stargate_protocol::write_handshake_ack(send, &ack)
        .await
        .context("send handshake NACK")?;
    send.finish().context("finish NACK stream")?;
    // finish() marks the stream as done but does not wait for QUIC to
    // deliver the bytes. The caller bail!s after this function returns,
    // which drops the Connection and tears down the transport before the
    // NACK reaches the client. stopped() blocks until the peer has
    // consumed the stream, ensuring the rejection reason is delivered.
    match tokio::time::timeout(delivery_timeout, send.stopped()).await {
        Ok(result) => {
            result?;
        }
        Err(_) => {
            warn!(
                timeout_ms = delivery_timeout.as_millis(),
                "timed out waiting for reverse handshake NACK delivery"
            );
        }
    }
    Ok(())
}

async fn handle_reverse_handshake(
    connection: Connection,
    proxy: &QuicHttpProxy,
    state: &StargateState,
    task_tracker: &TaskTracker,
) -> Result<()> {
    let (mut quinn_send, mut quinn_recv) = connection
        .accept_bi()
        .await
        .context("accept handshake stream")?;

    let handshake = stargate_protocol::read_handshake(&mut quinn_recv)
        .await
        .context("read handshake message")?;
    let inference_server_id = handshake.inference_server_id;

    if inference_server_id.is_empty() {
        send_handshake_nack(
            &mut quinn_send,
            "empty inference_server_id",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("empty inference_server_id in reverse handshake");
    }

    let result = match proxy
        .authenticator
        .authenticate(handshake.auth_token.as_deref())
        .await
    {
        Ok(result) => result,
        Err(e) => {
            warn!(
                inference_server_id = %inference_server_id,
                error = %e,
                "reverse handshake authentication failed"
            );
            send_handshake_nack(
                &mut quinn_send,
                "authentication failed",
                proxy.config.connect_timeout,
            )
            .await?;
            bail!("authentication failed for reverse handshake: {inference_server_id}");
        }
    };

    info!(
        inference_server_id = %inference_server_id,
        routing_key = ?result.routing_key,
        "reverse handshake authenticated"
    );

    let Some(registration) = state.registered_reverse_tunnel(&inference_server_id).await else {
        warn!(
            inference_server_id = %inference_server_id,
            "reverse handshake NACK: unauthorized inference_server_id"
        );
        send_handshake_nack(
            &mut quinn_send,
            "unauthorized inference_server_id",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("unauthorized inference_server_id in reverse handshake: {inference_server_id}");
    };

    if result.routing_key != registration.routing_key {
        warn!(
            inference_server_id = %inference_server_id,
            quic_routing_key = ?result.routing_key,
            stored_routing_key = ?registration.routing_key,
            "reverse handshake NACK: routing key mismatch"
        );
        send_handshake_nack(
            &mut quinn_send,
            "routing key mismatch",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("QUIC routing_key does not match gRPC registration: {inference_server_id}");
    }

    if !proxy
        .store_reverse_connection(&inference_server_id, connection.clone())
        .await
    {
        warn!(
            inference_server_id = %inference_server_id,
            "reverse handshake NACK: duplicate connection"
        );
        send_handshake_nack(
            &mut quinn_send,
            "duplicate connection",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("duplicate reverse tunnel connection for: {inference_server_id}");
    }
    let ack = stargate_protocol::HandshakeAck {
        accepted: true,
        reason: String::new(),
    };
    stargate_protocol::write_handshake_ack(&mut quinn_send, &ack)
        .await
        .context("send ACK")?;
    quinn_send.finish().context("finish ACK stream")?;

    info!(inference_server_id = %inference_server_id, "reverse tunnel connection established");
    let pool = proxy.pool.clone();
    let closed_id = connection.stable_id();
    let cleanup_span = info_span!(
        "reverse_tunnel_connection_cleanup",
        inference_server_id = %inference_server_id,
        stable_id = closed_id,
    );
    task_tracker.spawn(async move {
        connection.closed().await;
        let mut guard = pool.write().await;
        // Only the connection instance that installed this cleanup task may
        // remove the pool entry; newer reconnects should remain active.
        let is_current = guard
            .get(&inference_server_id)
            .is_some_and(|conn| conn.contains_stable_id(closed_id));
        if is_current {
            guard.remove(&inference_server_id);
            warn!(inference_server_id = %inference_server_id, "reverse tunnel connection closed, removed from pool");
        }
    }.instrument(cleanup_span));

    Ok(())
}
