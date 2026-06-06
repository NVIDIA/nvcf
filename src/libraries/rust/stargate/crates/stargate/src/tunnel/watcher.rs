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

use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use tracing::{error, warn};

use super::direct::QuicHttpProxy;

// Previously connected direct backends get a short recovery window so a proxy
// request can hot-path reconnect after a stale connection is observed.
const DIRECT_CONNECTION_UNAVAILABLE_GRACE: Duration = Duration::from_secs(2);

#[derive(Clone)]
pub(super) enum ConnState {
    Connecting { url: String },
    Connected { url: String },
}

pub struct ConnectionWatcher {
    proxy: Arc<QuicHttpProxy>,
    pub(super) states: HashMap<String, ConnState>,
    direct_unavailable_since: HashMap<String, Instant>,
    reverse_tunnel_connect_timeout: Duration,
}

pub enum EnsureConnectedResult {
    Connected,
    ReverseDisconnected,
    Unavailable,
}

impl ConnectionWatcher {
    pub fn new(proxy: Arc<QuicHttpProxy>, reverse_tunnel_connect_timeout: Duration) -> Self {
        Self {
            proxy,
            states: HashMap::new(),
            direct_unavailable_since: HashMap::new(),
            reverse_tunnel_connect_timeout,
        }
    }

    pub async fn ensure_connected(
        &mut self,
        inference_server_id: &str,
        inference_server_url: &str,
        reverse_tunnel: bool,
    ) -> EnsureConnectedResult {
        match self.states.get(inference_server_id).cloned() {
            None => {
                self.mark_connecting(inference_server_id, inference_server_url);
                if reverse_tunnel {
                    EnsureConnectedResult::ReverseDisconnected
                } else {
                    self.connect_for_transport(
                        inference_server_id,
                        inference_server_url,
                        reverse_tunnel,
                    )
                    .await
                }
            }
            Some(ConnState::Connected { url }) => {
                if url != inference_server_url {
                    self.handle_url_changed(
                        inference_server_id,
                        inference_server_url,
                        reverse_tunnel,
                    )
                    .await
                } else {
                    self.handle_connected_same_url(
                        inference_server_id,
                        inference_server_url,
                        reverse_tunnel,
                    )
                    .await
                }
            }
            Some(ConnState::Connecting { url }) => {
                if reverse_tunnel && self.proxy.has_healthy_connection(inference_server_id).await {
                    self.mark_connected(inference_server_id, &url);
                    return EnsureConnectedResult::Connected;
                }
                self.connect_for_transport(
                    inference_server_id,
                    inference_server_url,
                    reverse_tunnel,
                )
                .await
            }
        }
    }

    fn mark_connecting(&mut self, inference_server_id: &str, inference_server_url: &str) {
        self.direct_unavailable_since.remove(inference_server_id);
        self.states.insert(
            inference_server_id.to_string(),
            ConnState::Connecting {
                url: inference_server_url.to_string(),
            },
        );
    }

    fn mark_connected(&mut self, inference_server_id: &str, inference_server_url: &str) {
        self.direct_unavailable_since.remove(inference_server_id);
        self.states.insert(
            inference_server_id.to_string(),
            ConnState::Connected {
                url: inference_server_url.to_string(),
            },
        );
    }

    async fn connect_for_transport(
        &mut self,
        inference_server_id: &str,
        inference_server_url: &str,
        reverse_tunnel: bool,
    ) -> EnsureConnectedResult {
        if reverse_tunnel {
            self.try_reverse_connect(inference_server_id, inference_server_url)
                .await
        } else {
            self.try_preconnect(inference_server_id, inference_server_url)
                .await
        }
    }

    async fn handle_connected_same_url(
        &mut self,
        inference_server_id: &str,
        inference_server_url: &str,
        reverse_tunnel: bool,
    ) -> EnsureConnectedResult {
        if self.proxy.has_healthy_connection(inference_server_id).await {
            self.direct_unavailable_since.remove(inference_server_id);
            if reverse_tunnel {
                EnsureConnectedResult::Connected
            } else {
                self.try_replenish_direct_connection_set(inference_server_id, inference_server_url)
                    .await
            }
        } else {
            warn!(inference_server_id = %inference_server_id, "connection lost, reconnecting");
            if reverse_tunnel {
                self.mark_connecting(inference_server_id, inference_server_url);
                return self
                    .connect_for_transport(
                        inference_server_id,
                        inference_server_url,
                        reverse_tunnel,
                    )
                    .await;
            }

            match self
                .try_preconnect(inference_server_id, inference_server_url)
                .await
            {
                EnsureConnectedResult::Connected => EnsureConnectedResult::Connected,
                EnsureConnectedResult::Unavailable | EnsureConnectedResult::ReverseDisconnected => {
                    let unavailable_for = self.direct_unavailable_duration(inference_server_id);
                    if unavailable_for < DIRECT_CONNECTION_UNAVAILABLE_GRACE {
                        return EnsureConnectedResult::Connected;
                    }
                    self.mark_connecting(inference_server_id, inference_server_url);
                    EnsureConnectedResult::Unavailable
                }
            }
        }
    }

    async fn handle_url_changed(
        &mut self,
        inference_server_id: &str,
        inference_server_url: &str,
        reverse_tunnel: bool,
    ) -> EnsureConnectedResult {
        // Evict the stale connection so await_reverse_connection doesn't match
        // the old entry and short-circuit.
        self.proxy.pool.write().await.remove(inference_server_id);
        self.direct_unavailable_since.remove(inference_server_id);
        self.mark_connecting(inference_server_id, inference_server_url);
        self.connect_for_transport(inference_server_id, inference_server_url, reverse_tunnel)
            .await
    }

    fn direct_unavailable_duration(&mut self, inference_server_id: &str) -> Duration {
        let unavailable_since = self
            .direct_unavailable_since
            .entry(inference_server_id.to_string())
            .or_insert_with(Instant::now);
        unavailable_since.elapsed()
    }

    async fn try_preconnect(
        &mut self,
        inference_server_id: &str,
        inference_server_url: &str,
    ) -> EnsureConnectedResult {
        match self
            .proxy
            .preconnect(inference_server_id, inference_server_url)
            .await
        {
            Ok(()) => {
                self.mark_connected(inference_server_id, inference_server_url);
                EnsureConnectedResult::Connected
            }
            Err(error) => {
                warn!(
                    inference_server_id = %inference_server_id,
                    inference_server_url = %inference_server_url,
                    error = %error,
                    "quic preconnect failed"
                );
                EnsureConnectedResult::Unavailable
            }
        }
    }

    async fn try_replenish_direct_connection_set(
        &mut self,
        inference_server_id: &str,
        inference_server_url: &str,
    ) -> EnsureConnectedResult {
        if !self
            .proxy
            .connection_set_needs_replenishment(inference_server_id)
            .await
        {
            return EnsureConnectedResult::Connected;
        }

        match self
            .try_preconnect(inference_server_id, inference_server_url)
            .await
        {
            EnsureConnectedResult::Connected => EnsureConnectedResult::Connected,
            EnsureConnectedResult::Unavailable | EnsureConnectedResult::ReverseDisconnected => {
                // A failed replenish attempt should not demote a backend that
                // still has at least one usable direct connection; the next
                // update will retry.
                EnsureConnectedResult::Connected
            }
        }
    }

    async fn try_reverse_connect(
        &mut self,
        inference_server_id: &str,
        inference_server_url: &str,
    ) -> EnsureConnectedResult {
        if self
            .proxy
            .await_reverse_connection(inference_server_id, self.reverse_tunnel_connect_timeout)
            .await
        {
            self.mark_connected(inference_server_id, inference_server_url);
            EnsureConnectedResult::Connected
        } else {
            error!(
                inference_server_id = %inference_server_id,
                timeout_secs = self.reverse_tunnel_connect_timeout.as_secs(),
                "reverse tunnel connection not received within timeout"
            );
            EnsureConnectedResult::ReverseDisconnected
        }
    }
}
