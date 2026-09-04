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

use std::collections::{HashMap, HashSet};
use std::fs;
use std::net::SocketAddr;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, ensure};
use axum::http::{StatusCode as HttpStatusCode, header};
use axum::response::{IntoResponse, Response as HttpResponse};
use axum::routing::get;
use axum::{Router, extract::State};
use clap::Parser;
use prometheus::{
    Encoder, HistogramOpts, HistogramVec, IntCounterVec, Opts, Registry, TextEncoder,
};
use serde::Deserialize;
use stargate_proto::gateway_pb::llm_gateway_server::{LlmGateway, LlmGatewayServer};
use stargate_proto::gateway_pb::{
    AuthLlmInvokeRequest, AuthLlmInvokeResponse, AuthLlmWorkerRequest, AuthLlmWorkerResponse,
};
use tonic::transport::Server;
use tonic::{Code, Request, Response, Status};
use tracing::{info, warn};
use tracing_subscriber::EnvFilter;

const INVOCATION_RPC: &str = "auth_llm_invocation";
const WORKER_RPC: &str = "auth_llm_worker";
const CLIENT_SUBJECT: &str = "stargate-dev-client";
const NCA_ID: &str = "stargate-dev";

fn status_label(code: Code) -> &'static str {
    match code {
        Code::Ok => "ok",
        Code::Unauthenticated => "unauthenticated",
        Code::PermissionDenied => "permission_denied",
        _ => "other",
    }
}

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

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Config {
    service_token: String,
    client_token: String,
    workers: Vec<WorkerConfig>,
}

impl Config {
    fn read(path: &Path) -> Result<Self> {
        let bytes = fs::read(path).with_context(|| format!("read {}", path.display()))?;
        serde_json::from_slice(&bytes).with_context(|| format!("parse {}", path.display()))
    }
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct WorkerConfig {
    token: String,
    routing_key: String,
}

#[derive(Clone)]
struct DevAuth {
    service_token: String,
    client_token: String,
    worker_routing_keys: HashMap<String, String>,
    routing_keys: HashSet<String>,
    metrics: AuthMetrics,
}

impl DevAuth {
    fn new(config: Config) -> Result<Self> {
        ensure!(
            !config.service_token.is_empty(),
            "serviceToken must not be empty"
        );
        ensure!(
            !config.client_token.is_empty(),
            "clientToken must not be empty"
        );
        ensure!(!config.workers.is_empty(), "workers must not be empty");

        let mut worker_routing_keys = HashMap::with_capacity(config.workers.len());
        let mut routing_keys = HashSet::with_capacity(config.workers.len());
        for worker in config.workers {
            ensure!(!worker.token.is_empty(), "worker token must not be empty");
            ensure!(
                !worker.routing_key.is_empty(),
                "worker routingKey must not be empty"
            );
            ensure!(
                worker_routing_keys
                    .insert(worker.token, worker.routing_key.clone())
                    .is_none(),
                "worker tokens must be unique"
            );
            ensure!(
                routing_keys.insert(worker.routing_key),
                "worker routing keys must be unique"
            );
        }

        Ok(Self {
            service_token: config.service_token,
            client_token: config.client_token,
            worker_routing_keys,
            routing_keys,
            metrics: AuthMetrics::new()?,
        })
    }

    fn authorize_service<T>(&self, request: &Request<T>, rpc: &'static str) -> Result<(), Status> {
        let supplied = request
            .metadata()
            .get("authorization")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "));
        if supplied == Some(self.service_token.as_str()) {
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
            .with_label_values(&[rpc, status_label(status.code())])
            .inc();
        self.metrics
            .errors_total
            .with_label_values(&[rpc, category])
            .inc();
        warn!(
            rpc,
            category,
            code = status_label(status.code()),
            "authentication rejected"
        );
        status
    }

    fn record_success(&self, rpc: &'static str) {
        self.metrics
            .requests_total
            .with_label_values(&[rpc, status_label(Code::Ok)])
            .inc();
    }
}

#[tonic::async_trait]
impl LlmGateway for DevAuth {
    #[tracing::instrument(name = "stargate_dev_auth.auth_llm_invocation", skip_all)]
    async fn auth_llm_invocation(
        &self,
        request: Request<AuthLlmInvokeRequest>,
    ) -> Result<Response<AuthLlmInvokeResponse>, Status> {
        let _timer = self
            .metrics
            .request_duration_seconds
            .with_label_values(&[INVOCATION_RPC])
            .start_timer();
        self.authorize_service(&request, INVOCATION_RPC)?;

        let request = request.into_inner();
        if request.client_authorization_token != self.client_token {
            return Err(self.reject(
                INVOCATION_RPC,
                "client_token",
                Status::unauthenticated("invalid client authorization"),
            ));
        }
        if !self.routing_keys.contains(&request.routing_key) {
            return Err(self.reject(
                INVOCATION_RPC,
                "routing_key",
                Status::permission_denied("routing key is not configured"),
            ));
        }

        self.record_success(INVOCATION_RPC);
        Ok(Response::new(AuthLlmInvokeResponse {
            routing_key: request.routing_key,
            client_auth_subject: CLIENT_SUBJECT.to_string(),
            auth_context: HashMap::from([("ncaId".to_string(), NCA_ID.to_string())]),
            model_specs: HashMap::new(),
            priority: None,
        }))
    }

    #[tracing::instrument(name = "stargate_dev_auth.auth_llm_worker", skip_all)]
    async fn auth_llm_worker(
        &self,
        request: Request<AuthLlmWorkerRequest>,
    ) -> Result<Response<AuthLlmWorkerResponse>, Status> {
        let _timer = self
            .metrics
            .request_duration_seconds
            .with_label_values(&[WORKER_RPC])
            .start_timer();
        self.authorize_service(&request, WORKER_RPC)?;

        let routing_key = self
            .worker_routing_keys
            .get(&request.get_ref().worker_token)
            .ok_or_else(|| {
                self.reject(
                    WORKER_RPC,
                    "worker_token",
                    Status::unauthenticated("unknown worker token"),
                )
            })?;
        self.record_success(WORKER_RPC);
        Ok(Response::new(AuthLlmWorkerResponse {
            routing_key: routing_key.clone(),
        }))
    }
}

#[derive(Clone)]
struct AuthMetrics {
    registry: Registry,
    requests_total: IntCounterVec,
    errors_total: IntCounterVec,
    request_duration_seconds: HistogramVec,
}

impl AuthMetrics {
    fn new() -> Result<Self> {
        let registry = Registry::new();
        let requests_total = IntCounterVec::new(
            Opts::new(
                "nvcf_stargate_dev_auth_requests_total",
                "Authentication requests by RPC and gRPC status.",
            ),
            &["rpc", "status"],
        )?;
        let errors_total = IntCounterVec::new(
            Opts::new(
                "nvcf_stargate_dev_auth_errors_total",
                "Authentication errors by RPC and bounded category.",
            ),
            &["rpc", "category"],
        )?;
        let request_duration_seconds = HistogramVec::new(
            HistogramOpts::new(
                "nvcf_stargate_dev_auth_request_duration_seconds",
                "Authentication request duration by RPC.",
            ),
            &["rpc"],
        )?;
        registry.register(Box::new(requests_total.clone()))?;
        registry.register(Box::new(errors_total.clone()))?;
        registry.register(Box::new(request_duration_seconds.clone()))?;

        for (rpc, status) in [
            (INVOCATION_RPC, Code::Ok),
            (INVOCATION_RPC, Code::Unauthenticated),
            (INVOCATION_RPC, Code::PermissionDenied),
            (WORKER_RPC, Code::Ok),
            (WORKER_RPC, Code::Unauthenticated),
        ] {
            requests_total
                .with_label_values(&[rpc, status_label(status)])
                .inc_by(0);
        }
        for (rpc, category) in [
            (INVOCATION_RPC, "service_token"),
            (INVOCATION_RPC, "client_token"),
            (INVOCATION_RPC, "routing_key"),
            (WORKER_RPC, "service_token"),
            (WORKER_RPC, "worker_token"),
        ] {
            errors_total.with_label_values(&[rpc, category]).inc_by(0);
        }
        for rpc in [INVOCATION_RPC, WORKER_RPC] {
            let _ = request_duration_seconds.with_label_values(&[rpc]);
        }

        Ok(Self {
            registry,
            requests_total,
            errors_total,
            request_duration_seconds,
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
    let auth = DevAuth::new(Config::read(&args.config_path)?)?;
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
    )?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tonic::metadata::MetadataValue;

    fn auth() -> DevAuth {
        DevAuth::new(Config {
            service_token: "service-token".to_string(),
            client_token: "client-token".to_string(),
            workers: vec![WorkerConfig {
                token: "worker-token".to_string(),
                routing_key: "mockdc-usw2-a".to_string(),
            }],
        })
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

    #[tokio::test]
    async fn invocation_requires_configured_client_and_routing_key() {
        let auth = auth();
        let mut request = Request::new(AuthLlmInvokeRequest {
            client_authorization_token: "wrong".to_string(),
            routing_key: "mockdc-usw2-a".to_string(),
        });
        authorize(&mut request);
        assert_eq!(
            auth.auth_llm_invocation(request).await.unwrap_err().code(),
            Code::Unauthenticated
        );

        let mut request = Request::new(AuthLlmInvokeRequest {
            client_authorization_token: "client-token".to_string(),
            routing_key: "unknown".to_string(),
        });
        authorize(&mut request);
        assert_eq!(
            auth.auth_llm_invocation(request).await.unwrap_err().code(),
            Code::PermissionDenied
        );

        let mut request = Request::new(AuthLlmInvokeRequest {
            client_authorization_token: "client-token".to_string(),
            routing_key: "mockdc-usw2-a".to_string(),
        });
        authorize(&mut request);
        let response = auth
            .auth_llm_invocation(request)
            .await
            .unwrap()
            .into_inner();
        assert_eq!(response.routing_key, "mockdc-usw2-a");
        assert_eq!(response.auth_context.get("ncaId").unwrap(), NCA_ID);
    }

    #[test]
    fn duplicate_routing_keys_are_rejected() {
        let error = DevAuth::new(Config {
            service_token: "service-token".to_string(),
            client_token: "client-token".to_string(),
            workers: vec![
                WorkerConfig {
                    token: "worker-a".to_string(),
                    routing_key: "mockdc".to_string(),
                },
                WorkerConfig {
                    token: "worker-b".to_string(),
                    routing_key: "mockdc".to_string(),
                },
            ],
        })
        .err()
        .expect("duplicate routing key must fail");

        assert_eq!(error.to_string(), "worker routing keys must be unique");
    }

    #[test]
    fn metrics_exist_before_requests() {
        let body = String::from_utf8(auth().metrics.gather().unwrap()).unwrap();

        assert!(body.contains(
            "nvcf_stargate_dev_auth_requests_total{rpc=\"auth_llm_worker\",status=\"ok\"} 0"
        ));
        assert!(body.contains(
            "nvcf_stargate_dev_auth_errors_total{category=\"routing_key\",rpc=\"auth_llm_invocation\"} 0"
        ));
    }
}
