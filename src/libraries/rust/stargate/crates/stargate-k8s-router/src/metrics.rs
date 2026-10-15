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

use anyhow::Result;
use prometheus::{Encoder, IntCounterVec, Opts, Registry, TextEncoder};

#[derive(Clone)]
pub struct RouterMetrics {
    registry: Registry,
    quic_connections_total: IntCounterVec,
    webtransport_sessions_total: IntCounterVec,
}

impl RouterMetrics {
    pub fn new() -> Result<Self> {
        let registry = Registry::new();
        let quic_connections_total = IntCounterVec::new(
            Opts::new(
                "stargate_k8s_router_quic_connections_total",
                "QUIC reverse-tunnel router connections by outcome.",
            ),
            &["outcome"],
        )?;
        registry.register(Box::new(quic_connections_total.clone()))?;
        let webtransport_sessions_total = IntCounterVec::new(
            Opts::new(
                "stargate_k8s_router_webtransport_sessions_total",
                "WebTransport reverse-tunnel router sessions by outcome.",
            ),
            &["outcome"],
        )?;
        registry.register(Box::new(webtransport_sessions_total.clone()))?;

        Ok(Self {
            registry,
            quic_connections_total,
            webtransport_sessions_total,
        })
    }

    pub fn observe_quic_connection(&self, outcome: &str) {
        self.quic_connections_total
            .with_label_values(&[outcome])
            .inc();
    }

    pub fn observe_webtransport_session(&self, outcome: &str) {
        self.webtransport_sessions_total
            .with_label_values(&[outcome])
            .inc();
    }

    pub fn gather(&self) -> Result<String> {
        let encoder = TextEncoder::new();
        let mut buffer = Vec::new();
        encoder.encode(&self.registry.gather(), &mut buffer)?;
        String::from_utf8(buffer).map_err(Into::into)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn metrics_exports_quic_connection_outcomes() {
        let metrics = RouterMetrics::new().expect("metrics should initialize");
        metrics.observe_quic_connection("accepted");
        metrics.observe_quic_connection("accepted");
        metrics.observe_quic_connection("unknown_sni");

        let body = metrics.gather().expect("metrics should encode");

        assert!(
            body.contains(r#"stargate_k8s_router_quic_connections_total{outcome="accepted"} 2"#)
        );
        assert!(
            body.contains(r#"stargate_k8s_router_quic_connections_total{outcome="unknown_sni"} 1"#)
        );
    }

    #[test]
    fn metrics_exports_webtransport_session_outcomes() {
        let metrics = RouterMetrics::new().expect("metrics should initialize");
        metrics.observe_webtransport_session("accepted");
        metrics.observe_webtransport_session("completed");

        let body = metrics.gather().expect("metrics should encode");

        assert!(
            body.contains(
                r#"stargate_k8s_router_webtransport_sessions_total{outcome="accepted"} 1"#
            )
        );
        assert!(
            body.contains(
                r#"stargate_k8s_router_webtransport_sessions_total{outcome="completed"} 1"#
            )
        );
    }
}
