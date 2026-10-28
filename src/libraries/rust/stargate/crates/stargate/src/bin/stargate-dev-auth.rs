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

use std::collections::BTreeMap;
use std::fs;
use std::net::SocketAddr;
use std::path::PathBuf;

use anyhow::{Context, Result, ensure};
use axum::http::{StatusCode as HttpStatusCode, header};
use axum::response::{IntoResponse, Response as HttpResponse};
use axum::routing::get;
use axum::{Router, extract::State};
use clap::Parser;
use prometheus::{Encoder, IntCounterVec, Opts, Registry, TextEncoder};
use serde::Deserialize;
use stargate_proto::gateway_pb::auth_llm_invoke_response::ModelSpec;
use stargate_proto::gateway_pb::llm_gateway_server::{LlmGateway, LlmGatewayServer};
use stargate_proto::gateway_pb::{
    AuthLlmInvokeRequest, AuthLlmInvokeResponse, AuthLlmWorkerRequest, AuthLlmWorkerResponse,
};
use stargate_runtime::wait_for_termination_signal;
use tonic::transport::Server;
use tonic::{Request, Response, Status};
use tracing::{info, warn};
use tracing_subscriber::EnvFilter;

const WORKER_RPC: &str = "auth_llm_worker";
const INVOCATION_RPC: &str = "auth_llm_invocation";
const OK_RESULT: &str = "ok";

#[derive(Debug, Parser)]
#[command(name = "stargate-dev-auth")]
struct Args {
    /// Static JSON configuration. The file is read once during startup.
    #[arg(long, value_name = "PATH")]
    config_path: PathBuf,

    /// TCP address for the LlmGateway gRPC server.
    #[arg(long, default_value = "0.0.0.0:50051", value_name = "ADDR")]
    grpc_listen_addr: SocketAddr,

    /// TCP address for health and Prometheus endpoints.
    #[arg(long, default_value = "0.0.0.0:8080", value_name = "ADDR")]
    http_listen_addr: SocketAddr,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Config {
    service_token: String,
    worker_token: String,
    routing_key: String,
    /// Enables the invocation RPC used by the LLM API gateway. Without it the
    /// RPC stays unimplemented.
    #[serde(default)]
    invocation: Option<InvocationConfig>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct InvocationConfig {
    client_token: String,
    nca_id: String,
    #[serde(default = "default_client_auth_subject")]
    client_auth_subject: String,
    /// Per-model endpoint allow-lists, as the function API reports them.
    #[serde(default)]
    model_specs: BTreeMap<String, ModelSpecConfig>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct ModelSpecConfig {
    #[serde(default)]
    uris: Vec<String>,
}

fn default_client_auth_subject() -> String {
    "stargate-dev-client".to_string()
}

struct DevAuth {
    config: Config,
    metrics: AuthMetrics,
}

impl DevAuth {
    fn new(config: Config) -> Result<Self> {
        for (name, value) in [
            ("serviceToken", &config.service_token),
            ("workerToken", &config.worker_token),
            ("routingKey", &config.routing_key),
        ] {
            ensure!(!value.is_empty(), "{name} must not be empty");
        }
        Ok(Self {
            config,
            metrics: AuthMetrics::new()?,
        })
    }

    fn authorize_service<T>(&self, request: &Request<T>, rpc: &'static str) -> Result<(), Status> {
        let supplied = request
            .metadata()
            .get("authorization")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "));
        if supplied == Some(self.config.service_token.as_str()) {
            return Ok(());
        }

        Err(self.reject(
            rpc,
            "service_token",
            Status::unauthenticated("missing or invalid service authorization"),
        ))
    }

    fn reject(&self, rpc: &'static str, category: &'static str, status: Status) -> Status {
        self.metrics
            .requests_total
            .with_label_values(&[rpc, category])
            .inc();
        warn!(rpc, category, code = ?status.code(), "authentication rejected");
        status
    }

    fn record_success(&self, rpc: &'static str) {
        self.metrics
            .requests_total
            .with_label_values(&[rpc, OK_RESULT])
            .inc();
    }
}

#[tonic::async_trait]
impl LlmGateway for DevAuth {
    #[tracing::instrument(name = "stargate_dev_auth.auth_llm_worker", skip_all)]
    async fn auth_llm_worker(
        &self,
        request: Request<AuthLlmWorkerRequest>,
    ) -> Result<Response<AuthLlmWorkerResponse>, Status> {
        self.authorize_service(&request, WORKER_RPC)?;

        if request.get_ref().worker_token != self.config.worker_token {
            return Err(self.reject(
                WORKER_RPC,
                "worker_token",
                Status::unauthenticated("unknown worker token"),
            ));
        }
        self.record_success(WORKER_RPC);
        Ok(Response::new(AuthLlmWorkerResponse {
            routing_key: self.config.routing_key.clone(),
        }))
    }

    #[tracing::instrument(name = "stargate_dev_auth.auth_llm_invocation", skip_all)]
    async fn auth_llm_invocation(
        &self,
        request: Request<AuthLlmInvokeRequest>,
    ) -> Result<Response<AuthLlmInvokeResponse>, Status> {
        let Some(invocation) = &self.config.invocation else {
            return Err(Status::unimplemented("invocation auth is not configured"));
        };
        self.authorize_service(&request, INVOCATION_RPC)?;

        let request = request.into_inner();
        if request.client_authorization_token != invocation.client_token {
            return Err(self.reject(
                INVOCATION_RPC,
                "client_token",
                Status::unauthenticated("unknown client token"),
            ));
        }
        self.record_success(INVOCATION_RPC);
        Ok(Response::new(AuthLlmInvokeResponse {
            routing_key: request.routing_key,
            client_auth_subject: invocation.client_auth_subject.clone(),
            auth_context: [("ncaId".to_string(), invocation.nca_id.clone())].into(),
            model_specs: invocation
                .model_specs
                .iter()
                .map(|(model, spec)| {
                    (
                        model.clone(),
                        ModelSpec {
                            uris: spec.uris.clone(),
                            ..ModelSpec::default()
                        },
                    )
                })
                .collect(),
            priority: None,
        }))
    }
}

#[derive(Clone)]
struct AuthMetrics {
    registry: Registry,
    requests_total: IntCounterVec,
}

impl AuthMetrics {
    fn new() -> Result<Self> {
        let registry = Registry::new();
        let requests_total = IntCounterVec::new(
            Opts::new(
                "nvcf_stargate_dev_auth_requests_total",
                "Authentication requests by RPC and result: ok or the rejection category.",
            ),
            &["rpc", "result"],
        )?;
        registry.register(Box::new(requests_total.clone()))?;

        for (rpc, rejection) in [
            (WORKER_RPC, "worker_token"),
            (INVOCATION_RPC, "client_token"),
        ] {
            for result in [OK_RESULT, "service_token", rejection] {
                requests_total.with_label_values(&[rpc, result]).inc_by(0);
            }
        }

        Ok(Self {
            registry,
            requests_total,
        })
    }

    fn gather(&self) -> Result<Vec<u8>> {
        let mut buffer = Vec::new();
        TextEncoder::new().encode(&self.registry.gather(), &mut buffer)?;
        Ok(buffer)
    }
}

async fn metrics(State(metrics): State<AuthMetrics>) -> HttpResponse {
    match metrics.gather() {
        Ok(body) => (
            [(header::CONTENT_TYPE, TextEncoder::new().format_type())],
            body,
        )
            .into_response(),
        Err(error) => {
            tracing::error!(%error, "encode Prometheus metrics");
            HttpStatusCode::INTERNAL_SERVER_ERROR.into_response()
        }
    }
}

async fn healthy() -> HttpStatusCode {
    HttpStatusCode::OK
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .with_target(false)
        .compact()
        .init();

    let args = Args::parse();
    let config_path = args.config_path.display();
    let config = fs::read(&args.config_path).with_context(|| format!("read {config_path}"))?;
    let config: Config =
        serde_json::from_slice(&config).with_context(|| format!("parse {config_path}"))?;
    let auth = DevAuth::new(config)?;
    let http = Router::new()
        .route("/healthz", get(healthy))
        .route("/readyz", get(healthy))
        .route("/metrics", get(metrics))
        .with_state(auth.metrics.clone());
    let http_listener = tokio::net::TcpListener::bind(args.http_listen_addr)
        .await
        .context("bind auth health server")?;

    info!(
        grpc_address = %args.grpc_listen_addr,
        http_address = %args.http_listen_addr,
        "starting development auth service"
    );
    let servers = async {
        tokio::try_join!(
            async {
                Server::builder()
                    .add_service(LlmGatewayServer::new(auth))
                    .serve(args.grpc_listen_addr)
                    .await
                    .context("serve development auth gRPC")
            },
            async {
                axum::serve(http_listener, http)
                    .await
                    .context("serve development auth HTTP")
            },
        )
    };
    // The fixture is stateless, so exit on the first signal instead of
    // draining. As PID 1 the process would otherwise ignore SIGTERM.
    tokio::select! {
        result = servers => result.map(|_| ()),
        signal = wait_for_termination_signal() => {
            let signal = signal.context("wait for termination signal")?;
            info!(signal, "received termination signal, exiting");
            Ok(())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tonic::Code;
    use tonic::metadata::MetadataValue;

    fn config(service: &str, worker: &str, routing_key: &str) -> Config {
        Config {
            service_token: service.to_string(),
            worker_token: worker.to_string(),
            routing_key: routing_key.to_string(),
            invocation: None,
        }
    }

    fn invocation_auth() -> DevAuth {
        let config: Config = serde_json::from_str(
            r#"{
                "serviceToken": "service-token",
                "workerToken": "worker-token",
                "routingKey": "mockdc-usw2-a",
                "invocation": {
                    "clientToken": "client-token",
                    "ncaId": "nca-1",
                    "modelSpecs": {"m": {"uris": ["/v1/decisions", "/v1/systemone"]}}
                }
            }"#,
        )
        .expect("invocation config should parse");
        DevAuth::new(config).expect("valid test config")
    }

    fn invoke_request(token: &str) -> Request<AuthLlmInvokeRequest> {
        Request::new(AuthLlmInvokeRequest {
            client_authorization_token: token.to_string(),
            routing_key: "function-1".to_string(),
        })
    }

    #[tokio::test]
    async fn invocation_returns_model_specs_for_the_configured_client() {
        let auth = invocation_auth();
        let mut request = invoke_request("client-token");
        authorize(&mut request);

        let response = auth
            .auth_llm_invocation(request)
            .await
            .unwrap()
            .into_inner();

        assert_eq!(response.routing_key, "function-1");
        assert_eq!(response.client_auth_subject, "stargate-dev-client");
        assert_eq!(response.auth_context["ncaId"], "nca-1");
        assert_eq!(
            response.model_specs["m"].uris,
            ["/v1/decisions", "/v1/systemone"]
        );
    }

    #[tokio::test]
    async fn invocation_rejects_missing_service_token_and_unknown_client_token() {
        let auth = invocation_auth();
        assert_eq!(
            auth.auth_llm_invocation(invoke_request("client-token"))
                .await
                .unwrap_err()
                .code(),
            Code::Unauthenticated
        );
        let mut request = invoke_request("other");
        authorize(&mut request);
        assert_eq!(
            auth.auth_llm_invocation(request).await.unwrap_err().code(),
            Code::Unauthenticated
        );
    }

    #[tokio::test]
    async fn invocation_stays_unimplemented_without_configuration() {
        let mut request = invoke_request("client-token");
        authorize(&mut request);
        assert_eq!(
            auth()
                .auth_llm_invocation(request)
                .await
                .unwrap_err()
                .code(),
            Code::Unimplemented
        );
    }

    fn auth() -> DevAuth {
        DevAuth::new(config("service-token", "worker-token", "mockdc-usw2-a"))
            .expect("valid test config")
    }

    fn authorize<T>(request: &mut Request<T>) {
        request.metadata_mut().insert(
            "authorization",
            MetadataValue::try_from("Bearer service-token").unwrap(),
        );
    }

    #[tokio::test]
    async fn worker_requires_service_and_worker_tokens() {
        let auth = auth();
        let request = Request::new(AuthLlmWorkerRequest {
            worker_token: "worker-token".to_string(),
        });
        assert_eq!(
            auth.auth_llm_worker(request).await.unwrap_err().code(),
            Code::Unauthenticated
        );

        let mut request = Request::new(AuthLlmWorkerRequest {
            worker_token: "unknown".to_string(),
        });
        authorize(&mut request);
        assert_eq!(
            auth.auth_llm_worker(request).await.unwrap_err().code(),
            Code::Unauthenticated
        );

        let mut request = Request::new(AuthLlmWorkerRequest {
            worker_token: "worker-token".to_string(),
        });
        authorize(&mut request);
        assert_eq!(
            auth.auth_llm_worker(request)
                .await
                .unwrap()
                .into_inner()
                .routing_key,
            "mockdc-usw2-a"
        );
    }

    #[test]
    fn empty_config_values_are_rejected() {
        for (config, expected) in [
            (config("", "w", "r"), "serviceToken must not be empty"),
            (config("s", "", "r"), "workerToken must not be empty"),
            (config("s", "w", ""), "routingKey must not be empty"),
        ] {
            let error = DevAuth::new(config).err().expect(expected);
            assert_eq!(error.to_string(), expected);
        }
    }

    #[test]
    fn metrics_exist_before_requests() {
        let body = String::from_utf8(auth().metrics.gather().unwrap()).unwrap();

        for series in [
            "nvcf_stargate_dev_auth_requests_total{result=\"ok\",rpc=\"auth_llm_worker\"} 0",
            "nvcf_stargate_dev_auth_requests_total{result=\"service_token\",rpc=\"auth_llm_worker\"} 0",
            "nvcf_stargate_dev_auth_requests_total{result=\"worker_token\",rpc=\"auth_llm_worker\"} 0",
            "nvcf_stargate_dev_auth_requests_total{result=\"ok\",rpc=\"auth_llm_invocation\"} 0",
            "nvcf_stargate_dev_auth_requests_total{result=\"service_token\",rpc=\"auth_llm_invocation\"} 0",
            "nvcf_stargate_dev_auth_requests_total{result=\"client_token\",rpc=\"auth_llm_invocation\"} 0",
        ] {
            assert!(body.contains(series), "missing {series}");
        }
    }
}
