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

use anyhow::Result;
use prometheus::{Encoder, IntCounterVec, IntGaugeVec, Opts, Registry, TextEncoder};
use stargate_tls::{SERVER_IDENTITY_MATERIAL, TlsIdentityStatus, TlsReloadOutcome};

const QUIC_CONNECTION_OUTCOMES: &[&str] = &[
    "accepted",
    "completed",
    "missing_sni",
    "relay_error",
    "target_unavailable",
    "unknown_sni",
];
const WEBTRANSPORT_SESSION_OUTCOMES: &[&str] = &[
    "accepted",
    "completed",
    "invalid_connect",
    "missing_sni",
    "relay_error",
    "target_unavailable",
    "unknown_sni",
    "upstream_connect_error",
    "upstream_rejected",
];

#[derive(Clone)]
pub struct RouterMetrics {
    registry: Registry,
    quic_connections_total: IntCounterVec,
    webtransport_sessions_total: IntCounterVec,
    tls_reloads_total: IntCounterVec,
    tls_certificate_expiry_seconds: IntGaugeVec,
    tls_identity: Arc<TlsIdentityStatus>,
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
        let tls_reloads_total = IntCounterVec::new(
            Opts::new(
                "stargate_k8s_router_tls_reloads_total",
                "TLS material reload attempts by material type and result.",
            ),
            &["material_type", "result"],
        )?;
        registry.register(Box::new(tls_reloads_total.clone()))?;
        let tls_certificate_expiry_seconds = IntGaugeVec::new(
            Opts::new(
                "stargate_k8s_router_tls_certificate_expiry_seconds",
                "Unix timestamp when the active TLS certificate expires.",
            ),
            &["material_type"],
        )?;
        registry.register(Box::new(tls_certificate_expiry_seconds.clone()))?;

        for outcome in TlsReloadOutcome::ALL {
            tls_reloads_total
                .with_label_values(&[SERVER_IDENTITY_MATERIAL, outcome.as_str()])
                .inc_by(0);
        }

        for outcome in QUIC_CONNECTION_OUTCOMES {
            let _ = quic_connections_total.with_label_values(&[outcome]);
        }
        for outcome in WEBTRANSPORT_SESSION_OUTCOMES {
            let _ = webtransport_sessions_total.with_label_values(&[outcome]);
        }

        Ok(Self {
            registry,
            quic_connections_total,
            webtransport_sessions_total,
            tls_reloads_total,
            tls_certificate_expiry_seconds,
            tls_identity: TlsIdentityStatus::new(),
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

    pub fn observe_server_identity_reload(&self, outcome: TlsReloadOutcome) {
        self.tls_reloads_total
            .with_label_values(&[SERVER_IDENTITY_MATERIAL, outcome.as_str()])
            .inc();
    }

    /// Returns the expiry state the TLS reload task publishes to.
    pub fn tls_identity(&self) -> Arc<TlsIdentityStatus> {
        self.tls_identity.clone()
    }

    /// Republishes the active expiry to the gauge from the shared status.
    ///
    /// The reload task publishes to the status before it reports an outcome, so
    /// calling this from the outcome hook keeps the gauge and readiness aligned.
    /// A component serving a generated identity has no expiry, so it publishes
    /// no series rather than a placeholder timestamp.
    pub fn refresh_tls_certificate_expiry(&self) {
        if let Some(not_after) = self.tls_identity.active_expiry_unix_seconds() {
            self.tls_certificate_expiry_seconds
                .with_label_values(&[SERVER_IDENTITY_MATERIAL])
                .set(not_after);
        }
    }

    pub fn tls_identity_is_ready(&self) -> bool {
        self.tls_identity.is_ready()
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
    fn metrics_export_known_outcomes_before_traffic() {
        let metrics = RouterMetrics::new().expect("metrics should initialize");

        let body = metrics.gather().expect("metrics should encode");

        for &outcome in QUIC_CONNECTION_OUTCOMES {
            assert!(
                body.contains(&format!(
                    r#"stargate_k8s_router_quic_connections_total{{outcome="{outcome}"}} 0"#
                )),
                "missing zero-valued QUIC series for {outcome}"
            );
        }
        for &outcome in WEBTRANSPORT_SESSION_OUTCOMES {
            assert!(
                body.contains(&format!(
                    r#"stargate_k8s_router_webtransport_sessions_total{{outcome="{outcome}"}} 0"#
                )),
                "missing zero-valued WebTransport series for {outcome}"
            );
        }
    }

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
