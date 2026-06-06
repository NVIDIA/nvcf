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

use std::time::Duration;

use axum::http::{HeaderMap, StatusCode};

use stargate_protocol::TunnelTransportProtocol;
use stargate_tls::ServerTlsIdentity;

mod body;
mod direct;
mod http3;
mod reverse;
mod watcher;
mod webtransport;

pub use body::StreamingBody;
pub use direct::QuicHttpProxy;
pub use watcher::{ConnectionWatcher, EnsureConnectedResult};

#[derive(Clone, Debug)]
pub struct QuicTunnelConfig {
    pub connect_timeout: Duration,
    pub request_timeout: Duration,
    pub direct_quic_connections: usize,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub server_tls_identity: ServerTlsIdentity,
    pub quic_insecure: bool,
    pub tunnel_protocol: TunnelTransportProtocol,
}

pub struct StreamingResponse {
    pub status: StatusCode,
    pub headers: HeaderMap,
    pub body_stream: StreamingBody,
}

#[cfg(test)]
mod tests;
