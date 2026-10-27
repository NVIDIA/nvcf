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

//! Endpoint failover.
//!
//! [`ChannelManager`] owns the one gRPC channel a client uses at a time. It
//! connects to the configured URLs in order, builds a fresh tonic `Endpoint`
//! for every connection attempt so DNS is resolved again (Route53 failover
//! applies on the next reconnect), moves to the next URL when a call reports
//! the current one failed, appends the GDS endpoints advertised by
//! `GetServerInfo` after the configured ones, and periodically tries the
//! primary again while it runs on another endpoint.

use std::sync::{Arc, Mutex, MutexGuard, Weak};

use tokio::sync::watch;
use tonic::transport::{Channel, Endpoint};
use tracing::{debug, info, warn};

use crate::endpoints::{ConnectPolicy, EndpointSet, GdsUrl, TlsOptions};
use crate::error::Error;
use crate::metrics::MetricsHandle;
use crate::proto::v1::server_info_service_client::ServerInfoServiceClient;
use crate::proto::v1::{GetServerInfoRequest, ServerEndpoint};

/// Largest response message the client accepts. The server limits messages
/// to 4 MiB; the margin covers encoding overhead.
pub(crate) const MAX_DECODING_MESSAGE_BYTES: usize = 8 * 1024 * 1024;

/// A connected channel and the endpoint it reaches.
#[derive(Debug, Clone)]
pub struct Connection {
    channel: Channel,
    url: Arc<str>,
    generation: u64,
    primary: bool,
}

impl Connection {
    /// The gRPC channel. Clones share the underlying HTTP/2 connection.
    pub fn channel(&self) -> &Channel {
        &self.channel
    }

    /// The URL of the endpoint, or `injected` for a channel passed to
    /// [`ChannelManager::from_channel`].
    pub fn url(&self) -> &str {
        &self.url
    }

    /// Increases with every new connection; pass the connection to
    /// [`ChannelManager::mark_failed`] so a late failure report cannot tear
    /// down a newer connection.
    pub fn generation(&self) -> u64 {
        self.generation
    }

    /// Whether the endpoint is the first configured URL.
    pub fn is_primary(&self) -> bool {
        self.primary
    }
}

/// The connection state published by [`ChannelManager::subscribe_connection`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ConnectionState {
    /// No connection was attempted yet.
    Idle,
    /// A connection to `url` is in use.
    Connected {
        url: String,
        generation: u64,
        primary: bool,
    },
    /// The last connection failed or every endpoint was unreachable; the
    /// next call reconnects.
    Disconnected {
        last_url: Option<String>,
        error: Option<String>,
    },
}

impl ConnectionState {
    /// The generation of a connected state.
    pub fn generation(&self) -> Option<u64> {
        match self {
            Self::Connected { generation, .. } => Some(*generation),
            _ => None,
        }
    }

    pub fn is_connected(&self) -> bool {
        matches!(self, Self::Connected { .. })
    }
}

/// Ordered failover across GDS endpoints. Cheap to clone; clones share the
/// connection.
#[derive(Clone)]
pub struct ChannelManager {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for ChannelManager {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ChannelManager")
            .field("candidates", &self.candidates())
            .field("current_url", &self.current_url())
            .finish()
    }
}

struct Inner {
    configured: Vec<GdsUrl>,
    tls: TlsOptions,
    policy: ConnectPolicy,
    injected: bool,
    state: Mutex<State>,
    connect_lock: tokio::sync::Mutex<()>,
    connection_tx: watch::Sender<ConnectionState>,
    metrics: MetricsHandle,
}

struct State {
    fallbacks: Vec<GdsUrl>,
    current: Option<Connection>,
    /// The endpoint that failed last; the next connection pass starts after
    /// it.
    failed_url: Option<String>,
    /// URL of the previous connection, to count endpoint switches.
    last_url: Option<String>,
    generation: u64,
    /// Incremented after every connection pass that found no endpoint.
    failed_passes: u64,
    last_pass_error: Option<(usize, String)>,
    probe_running: bool,
}

impl ChannelManager {
    /// A manager for the configured endpoints. Nothing connects until the
    /// first call.
    pub fn new(endpoints: EndpointSet, tls: TlsOptions, policy: ConnectPolicy) -> Self {
        Self::build(endpoints.urls().to_vec(), tls, policy, None)
    }

    /// A manager around a channel the host created, for example one with a
    /// custom connector or an in-process transport. There is no failover;
    /// the channel's own reconnect logic applies.
    pub fn from_channel(channel: Channel, policy: ConnectPolicy) -> Self {
        Self::build(Vec::new(), TlsOptions::default(), policy, Some(channel))
    }

    fn build(
        configured: Vec<GdsUrl>,
        tls: TlsOptions,
        policy: ConnectPolicy,
        injected: Option<Channel>,
    ) -> Self {
        let injected_flag = injected.is_some();
        let current = injected.map(|channel| Connection {
            channel,
            url: Arc::from("injected"),
            generation: 1,
            primary: true,
        });
        let initial_state = match &current {
            Some(connection) => ConnectionState::Connected {
                url: connection.url().to_string(),
                generation: connection.generation,
                primary: true,
            },
            None => ConnectionState::Idle,
        };
        let (connection_tx, _) = watch::channel(initial_state);
        Self {
            inner: Arc::new(Inner {
                configured,
                tls,
                policy,
                injected: injected_flag,
                state: Mutex::new(State {
                    fallbacks: Vec::new(),
                    generation: current
                        .as_ref()
                        .map_or(0, |connection| connection.generation),
                    current,
                    failed_url: None,
                    last_url: None,
                    failed_passes: 0,
                    last_pass_error: None,
                    probe_running: false,
                }),
                connect_lock: tokio::sync::Mutex::new(()),
                connection_tx,
                metrics: MetricsHandle::default(),
            }),
        }
    }

    pub(crate) fn metrics(&self) -> &MetricsHandle {
        &self.inner.metrics
    }

    /// The connect policy.
    pub fn policy(&self) -> &ConnectPolicy {
        &self.inner.policy
    }

    /// The current connection, connecting first when there is none. A
    /// connection pass tries every candidate once, starting after the
    /// endpoint that failed last.
    pub async fn channel(&self) -> Result<Connection, Error> {
        let failed_passes = {
            let state = self.inner.lock_state();
            if let Some(connection) = &state.current {
                return Ok(connection.clone());
            }
            state.failed_passes
        };
        let _connecting = self.inner.connect_lock.lock().await;
        {
            let state = self.inner.lock_state();
            if let Some(connection) = &state.current {
                return Ok(connection.clone());
            }
            // Another caller finished a pass while this one waited; report
            // its result instead of trying every endpoint again.
            if state.failed_passes != failed_passes
                && let Some((attempted, last_error)) = &state.last_pass_error
            {
                return Err(Error::AllEndpointsFailed {
                    attempted: *attempted,
                    last_error: last_error.clone(),
                });
            }
        }
        self.inner.connect_pass().await
    }

    /// Reports that a call on `connection` failed in a way another endpoint
    /// may not (UNAVAILABLE, DEADLINE_EXCEEDED, a broken stream). The next
    /// call connects to the next candidate. Reports about an older
    /// connection are ignored.
    pub fn mark_failed(&self, connection: &Connection) {
        if self.inner.injected {
            return;
        }
        let mut state = self.inner.lock_state();
        let Some(current) = &state.current else {
            return;
        };
        if current.generation != connection.generation {
            return;
        }
        warn!(
            endpoint = %connection.url(),
            generation = connection.generation,
            "GDS endpoint failed; switching to the next endpoint"
        );
        state.failed_url = Some(connection.url().to_string());
        state.current = None;
        drop(state);
        self.inner.metrics.connected(false);
        self.inner
            .connection_tx
            .send_replace(ConnectionState::Disconnected {
                last_url: Some(connection.url().to_string()),
                error: None,
            });
    }

    /// The URL of the current connection.
    pub fn current_url(&self) -> Option<String> {
        self.inner
            .lock_state()
            .current
            .as_ref()
            .map(|connection| connection.url().to_string())
    }

    /// Connection state changes: connected (with a new generation after each
    /// reconnect or return to the primary) and disconnected.
    pub fn subscribe_connection(&self) -> watch::Receiver<ConnectionState> {
        self.inner.connection_tx.subscribe()
    }

    /// Every candidate URL in try order: the configured URLs, then the
    /// endpoints GDS advertised that are not configured.
    pub fn candidates(&self) -> Vec<String> {
        let state = self.inner.lock_state();
        self.inner
            .configured
            .iter()
            .chain(state.fallbacks.iter())
            .map(|url| url.as_str().to_string())
            .collect()
    }

    /// Replaces the advertised fallback endpoints with the `grpc_url` values
    /// of a `GetServerInfo` response. URLs that are configured already, that
    /// do not parse, or that use plain http while it is not allowed are
    /// skipped.
    pub fn set_advertised_endpoints(&self, endpoints: &[ServerEndpoint]) {
        if self.inner.injected {
            return;
        }
        let mut fallbacks: Vec<GdsUrl> = Vec::new();
        for endpoint in endpoints {
            if endpoint.grpc_url.trim().is_empty() {
                continue;
            }
            match GdsUrl::parse(&endpoint.grpc_url, self.inner.policy.allow_insecure_http) {
                Ok(url) => {
                    if !self.inner.configured.contains(&url) && !fallbacks.contains(&url) {
                        fallbacks.push(url);
                    }
                }
                Err(error) => {
                    debug!(
                        region = %endpoint.region,
                        %error,
                        "ignoring advertised GDS endpoint"
                    );
                }
            }
        }
        let mut state = self.inner.lock_state();
        if state.fallbacks != fallbacks {
            debug!(
                fallbacks = ?fallbacks.iter().map(GdsUrl::as_str).collect::<Vec<_>>(),
                "updated advertised GDS endpoints"
            );
            state.fallbacks = fallbacks;
        }
    }
}

impl Inner {
    fn lock_state(&self) -> MutexGuard<'_, State> {
        self.state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    /// Tries every candidate once. The caller holds `connect_lock`.
    async fn connect_pass(self: &Arc<Self>) -> Result<Connection, Error> {
        if self.injected {
            // An injected channel is never dropped.
            if let Some(connection) = &self.lock_state().current {
                return Ok(connection.clone());
            }
        }
        let (candidates, start) = {
            let state = self.lock_state();
            let candidates: Vec<(usize, GdsUrl)> = self
                .configured
                .iter()
                .chain(state.fallbacks.iter())
                .cloned()
                .enumerate()
                .collect();
            let start = state
                .failed_url
                .as_deref()
                .and_then(|failed| {
                    candidates
                        .iter()
                        .position(|(_, url)| url.as_str() == failed)
                })
                .map_or(0, |position| position + 1);
            (candidates, start)
        };
        if candidates.is_empty() {
            return Err(Error::NoEndpoints);
        }
        let mut last_error = String::new();
        for offset in 0..candidates.len() {
            let (index, url) = &candidates[(start + offset) % candidates.len()];
            match self.connect_one(url).await {
                Ok(channel) => return Ok(self.install(url, *index == 0, channel, "error")),
                Err(error) => {
                    warn!(endpoint = %url, %error, "GDS connection attempt failed");
                    last_error = error.to_string();
                }
            }
        }
        let mut state = self.lock_state();
        state.failed_passes += 1;
        state.last_pass_error = Some((candidates.len(), last_error.clone()));
        state.failed_url = None;
        drop(state);
        self.connection_tx
            .send_replace(ConnectionState::Disconnected {
                last_url: None,
                error: Some(last_error.clone()),
            });
        Err(Error::AllEndpointsFailed {
            attempted: candidates.len(),
            last_error,
        })
    }

    /// Opens a connection to one endpoint with a freshly built tonic
    /// `Endpoint`, so the host name is resolved again.
    async fn connect_one(&self, url: &GdsUrl) -> Result<Channel, Error> {
        let result = self.try_connect(url).await;
        self.metrics.connection_attempt(result.is_ok());
        result
    }

    async fn try_connect(&self, url: &GdsUrl) -> Result<Channel, Error> {
        let policy = &self.policy;
        let connect_error = |source| Error::Connect {
            url: url.as_str().to_string(),
            source,
        };
        let mut endpoint = Endpoint::from_shared(url.as_str().to_string())
            .map_err(connect_error)?
            .connect_timeout(policy.connect_timeout)
            .tcp_nodelay(true)
            .http2_keep_alive_interval(policy.keepalive_interval)
            .keep_alive_timeout(policy.keepalive_timeout)
            .keep_alive_while_idle(true);
        if let Some(user_agent) = &policy.user_agent {
            endpoint = endpoint
                .user_agent(user_agent.clone())
                .map_err(connect_error)?;
        }
        if url.is_tls() {
            endpoint = endpoint
                .tls_config(self.tls.client_config())
                .map_err(|error| Error::Tls(error.to_string()))?;
        }
        match tokio::time::timeout(policy.connect_timeout, endpoint.connect()).await {
            Ok(result) => result.map_err(connect_error),
            Err(_) => Err(Error::ConnectTimeout {
                url: url.as_str().to_string(),
                timeout: policy.connect_timeout,
            }),
        }
    }

    fn install(
        self: &Arc<Self>,
        url: &GdsUrl,
        primary: bool,
        channel: Channel,
        switch_reason: &str,
    ) -> Connection {
        let mut state = self.lock_state();
        state.generation += 1;
        let connection = Connection {
            channel,
            url: Arc::from(url.as_str()),
            generation: state.generation,
            primary,
        };
        let switched = state
            .last_url
            .as_deref()
            .is_some_and(|last| last != url.as_str());
        state.current = Some(connection.clone());
        state.failed_url = None;
        state.last_url = Some(url.as_str().to_string());
        let start_probe = !primary && !state.probe_running;
        if start_probe {
            state.probe_running = true;
        }
        drop(state);

        if switched {
            self.metrics.failover(switch_reason);
        }
        self.metrics.connected(true);
        info!(
            endpoint = %url,
            generation = connection.generation,
            primary,
            "connected to GDS"
        );
        self.connection_tx.send_replace(ConnectionState::Connected {
            url: url.as_str().to_string(),
            generation: connection.generation,
            primary,
        });
        self.spawn_server_info(connection.clone());
        if start_probe {
            self.spawn_primary_probe();
        }
        connection
    }

    /// Fetches the advertised endpoints of the new connection in the
    /// background.
    fn spawn_server_info(self: &Arc<Self>, connection: Connection) {
        let weak = Arc::downgrade(self);
        let timeout = self.policy.rpc_timeout;
        tokio::spawn(async move {
            let mut client = ServerInfoServiceClient::new(connection.channel.clone())
                .max_decoding_message_size(MAX_DECODING_MESSAGE_BYTES);
            let response =
                tokio::time::timeout(timeout, client.get_server_info(GetServerInfoRequest {}))
                    .await;
            let Some(inner) = weak.upgrade() else {
                return;
            };
            match response {
                Ok(Ok(response)) => {
                    let response = response.into_inner();
                    debug!(
                        endpoint = %connection.url(),
                        region = %response.region,
                        version = %response.version,
                        "GDS server info"
                    );
                    ChannelManager { inner }.set_advertised_endpoints(&response.endpoints);
                }
                Ok(Err(status)) => debug!(
                    endpoint = %connection.url(),
                    code = ?status.code(),
                    "GetServerInfo failed; keeping the configured endpoints only"
                ),
                Err(_) => debug!(
                    endpoint = %connection.url(),
                    "GetServerInfo timed out; keeping the configured endpoints only"
                ),
            }
        });
    }

    /// After the client moved off the primary, tries the primary every
    /// `primary_retry_interval` and switches back once it connects.
    fn spawn_primary_probe(self: &Arc<Self>) {
        let weak: Weak<Self> = Arc::downgrade(self);
        let interval = self.policy.primary_retry_interval;
        tokio::spawn(async move {
            loop {
                tokio::time::sleep(interval).await;
                let Some(inner) = weak.upgrade() else {
                    return;
                };
                if !inner.off_primary() {
                    inner.lock_state().probe_running = false;
                    return;
                }
                let Some(primary) = inner.configured.first().cloned() else {
                    inner.lock_state().probe_running = false;
                    return;
                };
                match inner.connect_one(&primary).await {
                    Ok(channel) => {
                        let _connecting = inner.connect_lock.lock().await;
                        if !inner.off_primary() {
                            inner.lock_state().probe_running = false;
                            return;
                        }
                        inner.lock_state().probe_running = false;
                        info!(endpoint = %primary, "GDS primary reachable again; returning to it");
                        inner.install(&primary, true, channel, "return_to_primary");
                        return;
                    }
                    Err(error) => {
                        debug!(endpoint = %primary, %error, "GDS primary still unreachable");
                    }
                }
            }
        });
    }

    /// Whether the client is on a non-primary endpoint or has lost its
    /// connection.
    fn off_primary(&self) -> bool {
        self.lock_state()
            .current
            .as_ref()
            .is_none_or(|connection| !connection.primary)
    }
}
