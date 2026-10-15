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
use std::net::SocketAddr;
use std::str::FromStr;

use anyhow::{Context, Result, ensure};
use clap::Parser;
use stargate_proto::gateway_pb::llm_gateway_server::{LlmGateway, LlmGatewayServer};
use stargate_proto::gateway_pb::{AuthLlmWorkerRequest, AuthLlmWorkerResponse};
use tonic::transport::Server;
use tonic::{Code, Request, Response, Status};

#[derive(Clone, Debug, PartialEq, Eq)]
struct WorkerMapping {
    token: String,
    routing_key: String,
}

impl FromStr for WorkerMapping {
    type Err = anyhow::Error;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        let (token, routing_key) = value
            .split_once('=')
            .context("worker mapping must use TOKEN=ROUTING_KEY")?;
        ensure!(!token.is_empty(), "worker token must not be empty");
        Ok(Self {
            token: token.to_string(),
            routing_key: routing_key.to_string(),
        })
    }
}

#[derive(Debug, Parser)]
struct Args {
    /// TCP address for the LlmGateway gRPC fixture.
    #[arg(long, default_value = "0.0.0.0:50051", value_name = "ADDR")]
    listen_addr: SocketAddr,

    /// Worker token to routing-key mapping. Repeat for each worker.
    #[arg(long = "worker", value_name = "TOKEN=ROUTING_KEY", required = true)]
    workers: Vec<WorkerMapping>,

    /// Optional bearer token Stargate must send to this gateway.
    #[arg(long, value_name = "TOKEN")]
    gateway_auth_token: Option<String>,
}

#[derive(Clone, Debug)]
struct WorkerAuthGateway {
    routing_keys: HashMap<String, String>,
    gateway_auth_token: Option<String>,
}

impl WorkerAuthGateway {
    fn new(
        workers: impl IntoIterator<Item = WorkerMapping>,
        gateway_auth_token: Option<String>,
    ) -> Result<Self> {
        let mut routing_keys = HashMap::new();
        for WorkerMapping { token, routing_key } in workers {
            ensure!(
                routing_keys.insert(token.clone(), routing_key).is_none(),
                "duplicate worker token {token}"
            );
        }
        Ok(Self {
            routing_keys,
            gateway_auth_token,
        })
    }
}

#[tonic::async_trait]
impl LlmGateway for WorkerAuthGateway {
    async fn auth_llm_worker(
        &self,
        request: Request<AuthLlmWorkerRequest>,
    ) -> Result<Response<AuthLlmWorkerResponse>, Status> {
        if !self.gateway_auth_token.as_deref().is_none_or(|expected| {
            request
                .metadata()
                .get("authorization")
                .and_then(|value| value.to_str().ok())
                .and_then(|value| value.strip_prefix("Bearer "))
                == Some(expected)
        }) {
            return Err(Status::unauthenticated(
                "missing or invalid gateway authorization",
            ));
        }
        let routing_key = self
            .routing_keys
            .get(&request.get_ref().worker_token)
            .ok_or_else(|| Status::new(Code::Unauthenticated, "unknown worker token"))?;
        Ok(Response::new(AuthLlmWorkerResponse {
            routing_key: routing_key.clone(),
        }))
    }
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = Args::parse();
    let gateway = WorkerAuthGateway::new(args.workers, args.gateway_auth_token)?;
    Server::builder()
        .add_service(LlmGatewayServer::new(gateway))
        .serve(args.listen_addr)
        .await
        .context("serve worker-auth gateway")
}

#[cfg(test)]
mod tests {
    use super::*;
    use tonic::metadata::MetadataValue;

    fn gateway(mappings: &[(&str, &str)], gateway_auth_token: Option<&str>) -> WorkerAuthGateway {
        WorkerAuthGateway::new(
            mappings.iter().map(|(token, routing_key)| WorkerMapping {
                token: (*token).to_string(),
                routing_key: (*routing_key).to_string(),
            }),
            gateway_auth_token.map(str::to_string),
        )
        .expect("fixture configuration should be valid")
    }

    fn request(worker_token: &str) -> Request<AuthLlmWorkerRequest> {
        Request::new(AuthLlmWorkerRequest {
            worker_token: worker_token.to_string(),
        })
    }

    #[test]
    fn cli_parses_repeated_worker_mappings_and_empty_routing_key() {
        let args = Args::try_parse_from([
            "worker-auth-gateway",
            "--listen-addr=127.0.0.1:50080",
            "--worker=token-a=tenant-a",
            "--worker=unscoped=",
            "--gateway-auth-token=gateway-token",
        ])
        .expect("fixture CLI should parse");

        assert_eq!(args.listen_addr, "127.0.0.1:50080".parse().unwrap());
        assert_eq!(
            args.workers,
            vec![
                WorkerMapping {
                    token: "token-a".to_string(),
                    routing_key: "tenant-a".to_string(),
                },
                WorkerMapping {
                    token: "unscoped".to_string(),
                    routing_key: String::new(),
                },
            ]
        );
        assert_eq!(args.gateway_auth_token.as_deref(), Some("gateway-token"));
    }

    #[tokio::test]
    async fn mapped_worker_token_returns_routing_key() {
        let gateway = gateway(&[("token-a", "tenant-a")], None);

        let response = gateway
            .auth_llm_worker(request("token-a"))
            .await
            .expect("mapped worker should authenticate")
            .into_inner();

        assert_eq!(response.routing_key, "tenant-a");
    }

    #[tokio::test]
    async fn unknown_worker_token_is_rejected() {
        let gateway = gateway(&[("token-a", "tenant-a")], None);

        let error = gateway
            .auth_llm_worker(request("unknown"))
            .await
            .expect_err("unknown worker should be rejected");

        assert_eq!(error.code(), Code::Unauthenticated);
    }

    #[tokio::test]
    async fn configured_empty_routing_key_is_returned() {
        let gateway = gateway(&[("unscoped", "")], None);

        let response = gateway
            .auth_llm_worker(request("unscoped"))
            .await
            .expect("configured unscoped worker should authenticate")
            .into_inner();

        assert_eq!(response.routing_key, "");
    }

    #[tokio::test]
    async fn gateway_bearer_authorization_is_optional_but_enforced_when_configured() {
        let gateway = gateway(&[("token-a", "tenant-a")], Some("gateway-token"));

        let error = gateway
            .auth_llm_worker(request("token-a"))
            .await
            .expect_err("missing gateway bearer should be rejected");
        assert_eq!(error.code(), Code::Unauthenticated);

        let mut authorized = request("token-a");
        authorized.metadata_mut().insert(
            "authorization",
            MetadataValue::try_from("Bearer gateway-token").unwrap(),
        );
        let response = gateway
            .auth_llm_worker(authorized)
            .await
            .expect("correct gateway bearer should authenticate")
            .into_inner();
        assert_eq!(response.routing_key, "tenant-a");
    }

    #[test]
    fn duplicate_worker_tokens_are_rejected() {
        let error = WorkerAuthGateway::new(
            [
                WorkerMapping {
                    token: "token-a".to_string(),
                    routing_key: "tenant-a".to_string(),
                },
                WorkerMapping {
                    token: "token-a".to_string(),
                    routing_key: "tenant-b".to_string(),
                },
            ],
            None,
        )
        .expect_err("duplicate worker tokens should be rejected");

        assert!(error.to_string().contains("duplicate worker token token-a"));
    }
}
