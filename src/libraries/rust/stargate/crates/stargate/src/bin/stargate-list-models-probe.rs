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

use std::future::Future;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use clap::Parser;
use stargate_proto::pb::ListModelsRequest;
use stargate_proto::pb::stargate_model_discovery_client::StargateModelDiscoveryClient;

#[derive(Parser, Debug)]
#[command(name = "stargate-list-models-probe")]
struct Args {
    #[arg(long, value_name = "ADDR")]
    addr: String,

    #[arg(long, value_name = "KEY")]
    routing_key: Option<String>,

    #[arg(long = "model-id", value_name = "MODEL")]
    model_ids: Vec<String>,

    #[arg(long = "expect", value_name = "MODEL")]
    expected_model_ids: Vec<String>,

    #[arg(long, default_value_t = 30, value_name = "N")]
    attempts: u32,

    #[arg(long, default_value_t = 1000, value_name = "MS")]
    interval_ms: u64,
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = Args::parse();
    run_probe_with(&args, |endpoint, request| async move {
        list_models(&endpoint, request).await
    })
    .await
}

async fn run_probe_with<F, Fut>(args: &Args, mut list_models_fn: F) -> Result<()>
where
    F: FnMut(String, ListModelsRequest) -> Fut,
    Fut: Future<Output = Result<Vec<String>>>,
{
    let endpoint = endpoint_from_addr(&args.addr);
    let expected = sorted_expected_model_ids(args);

    let mut last_error = None;
    for attempt in 1..=args.attempts {
        match list_models_fn(endpoint.clone(), list_models_request(args)).await {
            Ok(mut actual) => {
                actual.sort();
                if actual == expected {
                    println!("ListModels returned expected models: {actual:?}");
                    return Ok(());
                }
                last_error = Some(format!(
                    "attempt {attempt}/{} returned {actual:?}; expected {expected:?}",
                    args.attempts
                ));
            }
            Err(error) => {
                last_error = Some(format!(
                    "attempt {attempt}/{} failed: {error:#}",
                    args.attempts
                ));
            }
        }

        if attempt < args.attempts {
            tokio::time::sleep(Duration::from_millis(args.interval_ms)).await;
        }
    }

    bail!(
        "ListModels did not return expected models from {endpoint}: {}",
        last_error.unwrap_or_else(|| "no attempts ran".to_string())
    )
}

async fn list_models(endpoint: &str, request: ListModelsRequest) -> Result<Vec<String>> {
    let mut client = StargateModelDiscoveryClient::connect(endpoint.to_string())
        .await
        .with_context(|| format!("connect to {endpoint}"))?;
    let response = client
        .list_models(request)
        .await
        .context("call ListModels")?
        .into_inner();

    Ok(response.model_ids)
}

fn endpoint_from_addr(addr: &str) -> String {
    if addr.starts_with("http://") || addr.starts_with("https://") {
        addr.to_string()
    } else {
        format!("http://{addr}")
    }
}

fn sorted_expected_model_ids(args: &Args) -> Vec<String> {
    let mut expected = args.expected_model_ids.clone();
    expected.sort();
    expected
}

fn list_models_request(args: &Args) -> ListModelsRequest {
    ListModelsRequest {
        routing_key: args.routing_key.clone(),
        model_ids: args.model_ids.clone(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    use stargate_proto::pb::ListModelsResponse;
    use stargate_proto::pb::stargate_model_discovery_server::{
        StargateModelDiscovery, StargateModelDiscoveryServer,
    };
    use tokio::net::TcpListener;
    use tonic::{Request, Response, Status};

    #[derive(Clone)]
    struct TestDiscovery {
        model_ids: Vec<String>,
    }

    #[tonic::async_trait]
    impl StargateModelDiscovery for TestDiscovery {
        async fn list_models(
            &self,
            request: Request<ListModelsRequest>,
        ) -> std::result::Result<Response<ListModelsResponse>, Status> {
            let request = request.into_inner();
            assert_eq!(request.routing_key, Some("tenant-a".to_string()));
            assert_eq!(request.model_ids, vec!["model-a".to_string()]);

            Ok(Response::new(ListModelsResponse {
                model_ids: self.model_ids.clone(),
            }))
        }
    }

    fn args_with_addr(addr: &str) -> Args {
        Args {
            addr: addr.to_string(),
            routing_key: Some("tenant-a".to_string()),
            model_ids: vec!["model-b".to_string(), "model-a".to_string()],
            expected_model_ids: vec!["model-z".to_string(), "model-a".to_string()],
            attempts: 3,
            interval_ms: 25,
        }
    }

    async fn spawn_list_models_server(model_ids: Vec<String>) -> String {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("test server should bind to a loopback port");
        let addr = listener
            .local_addr()
            .expect("bound listener should expose its local address");
        let incoming = async_stream::stream! {
            loop {
                match listener.accept().await {
                    Ok((stream, _peer_addr)) => yield Ok::<_, std::io::Error>(stream),
                    Err(error) => {
                        yield Err(error);
                        break;
                    }
                }
            }
        };

        tokio::spawn(async move {
            tonic::transport::Server::builder()
                .add_service(StargateModelDiscoveryServer::new(TestDiscovery {
                    model_ids,
                }))
                .serve_with_incoming(incoming)
                .await
                .expect("test discovery server should run");
        });

        format!("http://{addr}")
    }

    #[test]
    fn endpoint_from_addr_preserves_existing_scheme() {
        assert_eq!(
            endpoint_from_addr("https://stargate.example:50071"),
            "https://stargate.example:50071"
        );
    }

    #[test]
    fn endpoint_from_addr_adds_http_scheme_for_host_port() {
        assert_eq!(
            endpoint_from_addr("127.0.0.1:50071"),
            "http://127.0.0.1:50071"
        );
    }

    #[test]
    fn sorted_expected_model_ids_is_deterministic() {
        assert_eq!(
            sorted_expected_model_ids(&args_with_addr("127.0.0.1:50071")),
            vec!["model-a".to_string(), "model-z".to_string()]
        );
    }

    #[test]
    fn list_models_request_preserves_filters() {
        let request = list_models_request(&args_with_addr("127.0.0.1:50071"));

        assert_eq!(request.routing_key, Some("tenant-a".to_string()));
        assert_eq!(
            request.model_ids,
            vec!["model-b".to_string(), "model-a".to_string()]
        );
    }

    #[tokio::test]
    async fn list_models_returns_model_ids_from_discovery_response() {
        let endpoint =
            spawn_list_models_server(vec!["model-z".to_string(), "model-a".to_string()]).await;

        let models = list_models(
            &endpoint,
            ListModelsRequest {
                routing_key: Some("tenant-a".to_string()),
                model_ids: vec!["model-a".to_string()],
            },
        )
        .await
        .expect("ListModels call should succeed");

        assert_eq!(models, vec!["model-z".to_string(), "model-a".to_string()]);
    }

    #[tokio::test]
    async fn probe_loop_accepts_expected_models_in_any_order() {
        let mut args = args_with_addr("127.0.0.1:50071");
        args.expected_model_ids = vec!["model-a".to_string(), "model-z".to_string()];
        args.attempts = 1;
        let mut calls = 0;

        run_probe_with(&args, |endpoint, request| {
            calls += 1;
            assert_eq!(endpoint, "http://127.0.0.1:50071");
            assert_eq!(request.routing_key, Some("tenant-a".to_string()));
            std::future::ready(Ok(vec!["model-z".to_string(), "model-a".to_string()]))
        })
        .await
        .expect("expected models should pass");

        assert_eq!(calls, 1);
    }

    #[tokio::test]
    async fn probe_loop_reports_last_mismatched_attempt() {
        let mut args = args_with_addr("127.0.0.1:50071");
        args.expected_model_ids = vec!["model-a".to_string()];
        args.attempts = 2;
        args.interval_ms = 0;
        let mut calls = 0;

        let err = run_probe_with(&args, |_endpoint, _request| {
            calls += 1;
            let models = if calls == 1 {
                vec!["model-b".to_string()]
            } else {
                vec!["model-c".to_string()]
            };
            std::future::ready(Ok(models))
        })
        .await
        .expect_err("mismatched models should fail");

        let message = err.to_string();
        assert!(
            message.contains("attempt 2/2 returned [\"model-c\"]; expected [\"model-a\"]"),
            "unexpected error: {message}"
        );
        assert_eq!(calls, 2);
    }
}
