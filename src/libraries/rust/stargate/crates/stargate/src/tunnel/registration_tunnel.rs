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

use std::sync::Arc;
use std::time::{Duration, Instant};

use tracing::{error, warn};

use crate::routing_state::RegistrationGeneration;

use super::direct::QuicHttpProxy;

// A previously connected direct backend gets a short recovery window so a proxy
// request can hot-path reconnect after a stale connection is observed.
const DIRECT_CONNECTION_UNAVAILABLE_GRACE: Duration = Duration::from_secs(2);

enum RegistrationTunnelState {
    Direct(DirectConnectionState),
    Reverse {
        connection: ReverseConnectionState,
        connect_timeout: Duration,
    },
}

enum DirectConnectionState {
    Initial,
    Connected,
    Recovering { unavailable_since: Instant },
    Unavailable,
}

enum ReverseConnectionState {
    EndpointUnadvertised,
    EndpointAdvertised,
}

pub struct RegistrationTunnel {
    proxy: Arc<QuicHttpProxy>,
    registration: Arc<RegistrationGeneration>,
    state: RegistrationTunnelState,
}

pub enum EnsureConnectedResult {
    Connected,
    ReverseDisconnected,
    Unavailable,
}

impl RegistrationTunnel {
    pub fn direct(proxy: Arc<QuicHttpProxy>, registration: Arc<RegistrationGeneration>) -> Self {
        assert!(
            !registration.reverse_tunnel(),
            "direct tunnel owner requires a direct registration"
        );
        Self {
            proxy,
            registration,
            state: RegistrationTunnelState::Direct(DirectConnectionState::Initial),
        }
    }

    pub fn reverse(
        proxy: Arc<QuicHttpProxy>,
        registration: Arc<RegistrationGeneration>,
        connect_timeout: Duration,
    ) -> Self {
        assert!(
            registration.reverse_tunnel(),
            "reverse tunnel owner requires a reverse registration"
        );
        Self {
            proxy,
            registration,
            state: RegistrationTunnelState::Reverse {
                connection: ReverseConnectionState::EndpointUnadvertised,
                connect_timeout,
            },
        }
    }

    pub async fn ensure_connected(&mut self) -> EnsureConnectedResult {
        if !self.registration.tunnel_connections().is_active() {
            return EnsureConnectedResult::Unavailable;
        }
        match &mut self.state {
            RegistrationTunnelState::Direct(state) => {
                ensure_direct_connected(&self.proxy, &self.registration, state).await
            }
            RegistrationTunnelState::Reverse {
                connection: connection @ ReverseConnectionState::EndpointUnadvertised,
                ..
            } => {
                *connection = ReverseConnectionState::EndpointAdvertised;
                EnsureConnectedResult::ReverseDisconnected
            }
            RegistrationTunnelState::Reverse {
                connection: ReverseConnectionState::EndpointAdvertised,
                connect_timeout,
            } => ensure_reverse_connected(&self.proxy, &self.registration, *connect_timeout).await,
        }
    }
}

impl Drop for RegistrationTunnel {
    fn drop(&mut self) {
        self.registration.tunnel_connections().retire();
    }
}

async fn ensure_direct_connected(
    proxy: &QuicHttpProxy,
    registration: &Arc<RegistrationGeneration>,
    state: &mut DirectConnectionState,
) -> EnsureConnectedResult {
    if proxy.has_healthy_connection(registration) {
        *state = DirectConnectionState::Connected;
        if proxy.connection_set_needs_replenishment(registration)
            && let Err(error) = proxy.connect_direct_registration(registration).await
        {
            warn!(
                inference_server_id = %registration.inference_server_id(),
                inference_server_url = %registration.inference_server_url(),
                error = %error,
                "failed to replenish direct tunnel connection set"
            );
        }
        return EnsureConnectedResult::Connected;
    }

    match proxy.connect_direct_registration(registration).await {
        Ok(()) => {
            *state = DirectConnectionState::Connected;
            EnsureConnectedResult::Connected
        }
        Err(error) => {
            warn!(
                inference_server_id = %registration.inference_server_id(),
                inference_server_url = %registration.inference_server_url(),
                error = %error,
                "quic preconnect failed"
            );
            state.after_failed_connect()
        }
    }
}

async fn ensure_reverse_connected(
    proxy: &QuicHttpProxy,
    registration: &Arc<RegistrationGeneration>,
    timeout: Duration,
) -> EnsureConnectedResult {
    if proxy.has_healthy_connection(registration)
        || proxy
            .await_reverse_connection(registration.clone(), timeout)
            .await
    {
        return EnsureConnectedResult::Connected;
    }

    error!(
        inference_server_id = %registration.inference_server_id(),
        timeout_secs = timeout.as_secs(),
        "reverse tunnel connection not received within timeout"
    );
    EnsureConnectedResult::ReverseDisconnected
}

impl DirectConnectionState {
    fn after_failed_connect(&mut self) -> EnsureConnectedResult {
        match self {
            Self::Connected => {
                *self = Self::Recovering {
                    unavailable_since: Instant::now(),
                };
                EnsureConnectedResult::Connected
            }
            Self::Recovering { unavailable_since }
                if unavailable_since.elapsed() < DIRECT_CONNECTION_UNAVAILABLE_GRACE =>
            {
                EnsureConnectedResult::Connected
            }
            Self::Initial | Self::Recovering { .. } | Self::Unavailable => {
                *self = Self::Unavailable;
                EnsureConnectedResult::Unavailable
            }
        }
    }
}
