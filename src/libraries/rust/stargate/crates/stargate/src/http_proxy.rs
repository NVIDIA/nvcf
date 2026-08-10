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
use std::time::Instant;

use axum::Router;
use axum::body::Body;
use axum::extract::{Request, State};
use axum::http::StatusCode;
use axum::response::Response;
use axum::routing::{get, post};
use tokio_util::sync::CancellationToken;
use tracing::{Instrument, Span};

use crate::load_balancer::LoadBalancerRouter;
use crate::metrics::StargateMetrics;
use crate::routing_state::StargateState;
use crate::tunnel::{QuicHttpProxy, QuicTunnelConfig};

mod attempt;
mod diagnostics;
mod request;
mod retry;
mod routing;
mod run;
mod trace;
mod upstream;
pub(crate) use diagnostics::DebugConfig;
use diagnostics::{debug_state, http_list_models};
use request::{
    parse_proxy_request_inputs, reject_invalid_routing_algorithm,
    validate_load_balancer_request_requirements,
};
pub use retry::ProxyRetryConfig;
use retry::{ReplayableRequestBody, retry_budget_deadline};
use run::{PreparedProxyRequest, ProxyRequestRun};
use trace::{RequestTraceFields, proxy_openai_request_span, record_request_to_span};
use upstream::prepare_forwarded_headers;

const HEADER_ROUTING_METHOD: &str = "x-routing-method";
const HEADER_MAX_WAIT_MS: &str = "x-max-wait-ms";
const HEADER_REQUEST_SLO_MS: &str = "x-request-slo-ms";
const HEADER_CACHE_AFFINITY_KEY: &str = "x-cache-affinity-key";
const HEADER_STARGATE_ERROR_CODE: &str = "x-stargate-error-code";

#[derive(Clone, Copy)]
struct OpenAiProxyEndpoint {
    path: &'static str,
    name: &'static str,
}

impl OpenAiProxyEndpoint {
    const CHAT_COMPLETIONS: Self = Self {
        path: "/v1/chat/completions",
        name: "chat_completions",
    };
    const RESPONSES: Self = Self {
        path: "/v1/responses",
        name: "responses",
    };
    const EMBEDDINGS: Self = Self {
        path: "/v1/embeddings",
        name: "embeddings",
    };
}

#[derive(Clone, Debug)]
pub struct ProxyTransportConfig {
    pub quic: QuicTunnelConfig,
    pub retry: ProxyRetryConfig,
}

#[derive(Clone)]
pub struct ProxyTrafficState {
    pub shutdown: CancellationToken,
}

#[derive(Clone)]
pub struct ProxyAppState {
    pub state: Arc<StargateState>,
    pub quic_proxy: Arc<QuicHttpProxy>,
    pub traffic: ProxyTrafficState,
    pub lb_router: Arc<LoadBalancerRouter>,
    pub metrics: Arc<StargateMetrics>,
    pub retry: ProxyRetryConfig,
    pub debug_config: DebugConfig,
}

pub fn make_router(app: ProxyAppState) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/readyz", get(readyz))
        .route("/v1/models", get(http_list_models))
        .route("/debug/state", get(debug_state))
        .route(
            OpenAiProxyEndpoint::CHAT_COMPLETIONS.path,
            post(|State(app), req| {
                proxy_openai_request(app, req, OpenAiProxyEndpoint::CHAT_COMPLETIONS)
            }),
        )
        .route(
            OpenAiProxyEndpoint::RESPONSES.path,
            post(|State(app), req| proxy_openai_request(app, req, OpenAiProxyEndpoint::RESPONSES)),
        )
        .route(
            OpenAiProxyEndpoint::EMBEDDINGS.path,
            post(|State(app), req| proxy_openai_request(app, req, OpenAiProxyEndpoint::EMBEDDINGS)),
        )
        .with_state(app)
}

async fn proxy_openai_request(
    app: ProxyAppState,
    req: Request,
    endpoint: OpenAiProxyEndpoint,
) -> Result<Response<Body>, StatusCode> {
    let request_start = Instant::now();
    let (parts, body) = req.into_parts();
    let span = proxy_openai_request_span(&parts.headers);
    async move {
        let request = prepare_proxy_request(&app, parts, body, endpoint, request_start)?;
        ProxyRequestRun::new(&app, request).execute().await
    }
    .instrument(span)
    .await
}

fn prepare_proxy_request(
    app: &ProxyAppState,
    parts: axum::http::request::Parts,
    body: Body,
    endpoint: OpenAiProxyEndpoint,
    request_start: Instant,
) -> Result<PreparedProxyRequest, StatusCode> {
    let request_path = parts.uri.path();
    let path_and_query = parts
        .uri
        .path_and_query()
        .map(|pq| pq.to_string())
        .unwrap_or_else(|| endpoint.path.to_string());
    let request_inputs = parse_proxy_request_inputs(&parts.headers)?;
    let target = &request_inputs.target;
    let model_id = target.model_id.as_str();

    Span::current().record("request.endpoint", endpoint.name);
    record_request_to_span(
        &Span::current(),
        RequestTraceFields {
            routing_key: target.routing_key.as_deref(),
            model_id,
            request_path,
            input_tokens: request_inputs.input_tokens,
            priority: request_inputs.priority,
            max_wait_ms: request_inputs.max_wait_ms,
            request_slo_ms: request_inputs.request_slo_ms,
            cache_affinity_key_present: request_inputs.cache_affinity_key.is_some(),
        },
    );

    let lb_resolution = app
        .lb_router
        .resolve_algorithm_override(model_id, request_inputs.routing_algorithm_override.as_ref())
        .map_err(|error| reject_invalid_routing_algorithm(target, &error))?;
    validate_load_balancer_request_requirements(lb_resolution.config(), &request_inputs)?;
    let retry_deadline = retry_budget_deadline(&parts.headers, &app.retry, request_start)?;
    let replay_body =
        ReplayableRequestBody::new(&parts.headers, body, app.retry.max_replay_body_bytes)?;

    Ok(PreparedProxyRequest {
        request_inputs,
        lb_resolution,
        endpoint_name: endpoint.name,
        method: parts.method,
        path_and_query,
        forwarded_headers: prepare_forwarded_headers(&parts.headers),
        retry_deadline,
        request_start,
        replay_body,
    })
}

async fn healthz() -> StatusCode {
    StatusCode::OK
}

async fn readyz(State(app): State<ProxyAppState>) -> StatusCode {
    if app.traffic.shutdown.is_cancelled() {
        return StatusCode::SERVICE_UNAVAILABLE;
    }

    StatusCode::OK
}

#[cfg(test)]
mod test_support {
    use axum::extract::State;
    use axum::http::StatusCode;
    use std::sync::Arc;
    use std::time::Duration;
    use tokio_util::sync::CancellationToken;

    use crate::auth::OpenAuthenticator;
    use crate::load_balancer::{LoadBalancerConfig, LoadBalancerRouter};
    use crate::metrics::StargateMetrics;
    use crate::routing_state::StargateState;
    use crate::tunnel::{QuicHttpProxy, QuicTunnelConfig};

    use super::{DebugConfig, ProxyAppState, ProxyRetryConfig, ProxyTrafficState, readyz};

    pub(super) fn test_proxy_app_state() -> ProxyAppState {
        test_proxy_app_state_with_lb_config(LoadBalancerConfig::default())
    }

    pub(super) fn test_proxy_app_state_with_lb_config(
        lb_config: LoadBalancerConfig,
    ) -> ProxyAppState {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let metrics = StargateMetrics::new().expect("metrics should initialize");
        ProxyAppState {
            state: Arc::new(StargateState::new()),
            quic_proxy: Arc::new(
                QuicHttpProxy::new(
                    QuicTunnelConfig {
                        connect_timeout: Duration::from_millis(10),
                        request_timeout: Duration::from_millis(10),
                        direct_quic_connections: 1,
                        tls_cert_pem: None,
                        server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                        quic_insecure: true,
                        tunnel_protocol: Default::default(),
                    },
                    Arc::new(OpenAuthenticator),
                )
                .expect("quic proxy should initialize"),
            ),
            traffic: ProxyTrafficState {
                shutdown: CancellationToken::new(),
            },
            lb_router: Arc::new(
                LoadBalancerRouter::from_config(&lb_config)
                    .expect("load balancer should initialize"),
            ),
            metrics,
            retry: ProxyRetryConfig::default(),
            debug_config: DebugConfig::default(),
        }
    }

    #[tokio::test]
    async fn readyz_derives_draining_from_runtime_shutdown_token() {
        let app = test_proxy_app_state();

        assert_eq!(readyz(State(app.clone())).await, StatusCode::OK);

        app.traffic.shutdown.cancel();

        assert_eq!(readyz(State(app)).await, StatusCode::SERVICE_UNAVAILABLE);
    }
}
