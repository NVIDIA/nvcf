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
use std::io::Write;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use clap::Parser;
use stargate_proto::pb::stargate_model_discovery_client::StargateModelDiscoveryClient;
use stargate_proto::pb::{ListModelsEntry, ListModelsRequest, ListModelsResponse};

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
    run_probe_with(
        &args,
        |endpoint, request| async move { list_models(&endpoint, request).await },
        &mut std::io::stdout(),
    )
    .await
}

/// Polls `ListModels` until its `model_ids` equal the expected set, then
/// writes the matched ids followed by one line per inference-server entry.
async fn run_probe_with<F, Fut>(
    args: &Args,
    mut list_models_fn: F,
    out: &mut impl Write,
) -> Result<()>
where
    F: FnMut(String, ListModelsRequest) -> Fut,
    Fut: Future<Output = Result<ListModelsResponse>>,
{
    let endpoint = endpoint_from_addr(&args.addr);
    let mut expected = args.expected_model_ids.clone();
    expected.sort();

    let mut last_error = "no attempts ran".to_string();
    for attempt in 1..=args.attempts {
        last_error = match list_models_fn(endpoint.clone(), list_models_request(args)).await {
            Ok(response) => {
                let mut actual = response.model_ids;
                actual.sort();
                if actual == expected {
                    writeln!(out, "ListModels returned expected models: {actual:?}")
                        .context("write probe output")?;
                    for entry in &response.entries {
                        writeln!(out, "{}", format_entry(entry)).context("write probe output")?;
                    }
                    return Ok(());
                }
                format!(
                    "attempt {attempt}/{} returned {actual:?}; expected {expected:?}",
                    args.attempts
                )
            }
            Err(error) => format!("attempt {attempt}/{} failed: {error:#}", args.attempts),
        };

        if attempt < args.attempts {
            tokio::time::sleep(Duration::from_millis(args.interval_ms)).await;
        }
    }

    bail!("ListModels did not return expected models from {endpoint}: {last_error}")
}

async fn list_models(endpoint: &str, request: ListModelsRequest) -> Result<ListModelsResponse> {
    let mut client = StargateModelDiscoveryClient::connect(endpoint.to_string())
        .await
        .with_context(|| format!("connect to {endpoint}"))?;
    Ok(client
        .list_models(request)
        .await
        .context("call ListModels")?
        .into_inner())
}

/// Formats one entry on one line with debug-quoted strings.
fn format_entry(entry: &ListModelsEntry) -> String {
    format!(
        "entry model_id={:?} inference_server_id={:?} cluster_id={:?}",
        entry.model_id, entry.inference_server_id, entry.cluster_id
    )
}

fn endpoint_from_addr(addr: &str) -> String {
    if addr.starts_with("http://") || addr.starts_with("https://") {
        addr.to_string()
    } else {
        format!("http://{addr}")
    }
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

    use stargate_proto::pb::stargate_model_discovery_server::{
        StargateModelDiscovery, StargateModelDiscoveryServer,
    };
    use tokio::net::TcpListener;
    use tonic::{Request, Response, Status};

    #[derive(Clone)]
    struct TestDiscovery {
        response: ListModelsResponse,
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

            Ok(Response::new(self.response.clone()))
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

    fn entry(model_id: &str, server_id: &str) -> ListModelsEntry {
        ListModelsEntry {
            model_id: model_id.to_string(),
            cluster_id: format!("{server_id}-cluster"),
            inference_server_id: server_id.to_string(),
        }
    }

    fn response(model_ids: &[&str], entries: Vec<ListModelsEntry>) -> ListModelsResponse {
        ListModelsResponse {
            model_ids: model_ids.iter().map(|id| id.to_string()).collect(),
            entries,
        }
    }

    async fn spawn_list_models_server(response: ListModelsResponse) -> String {
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
                    response,
                }))
                .serve_with_incoming(incoming)
                .await
                .expect("test discovery server should run");
        });

        format!("http://{addr}")
    }

    #[test]
    fn endpoint_from_addr_normalizes_only_missing_schemes() {
        assert_eq!(
            endpoint_from_addr("https://stargate.example:50071"),
            "https://stargate.example:50071"
        );
        assert_eq!(
            endpoint_from_addr("127.0.0.1:50071"),
            "http://127.0.0.1:50071"
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

    #[test]
    fn format_entry_quotes_fields() {
        assert_eq!(
            format_entry(&entry("model a", "pylon-1")),
            "entry model_id=\"model a\" inference_server_id=\"pylon-1\" \
             cluster_id=\"pylon-1-cluster\""
        );
    }

    #[tokio::test]
    async fn list_models_returns_discovery_response_with_entries() {
        let expected = response(&["model-z", "model-a"], vec![entry("model-a", "pylon-1")]);
        let endpoint = spawn_list_models_server(expected.clone()).await;

        let actual = list_models(
            &endpoint,
            ListModelsRequest {
                routing_key: Some("tenant-a".to_string()),
                model_ids: vec!["model-a".to_string()],
            },
        )
        .await
        .expect("ListModels call should succeed");

        assert_eq!(actual, expected);
    }

    #[tokio::test]
    async fn probe_loop_accepts_expected_models_in_any_order_and_prints_entries() {
        let mut args = args_with_addr("127.0.0.1:50071");
        args.expected_model_ids = vec!["model-a".to_string(), "model-z".to_string()];
        args.attempts = 1;
        let mut calls = 0;
        let mut out = Vec::new();

        run_probe_with(
            &args,
            |endpoint, request| {
                calls += 1;
                assert_eq!(endpoint, "http://127.0.0.1:50071");
                assert_eq!(request.routing_key, Some("tenant-a".to_string()));
                std::future::ready(Ok(response(
                    &["model-z", "model-a"],
                    vec![entry("model-a", "pylon-1"), entry("model-z", "pylon-2")],
                )))
            },
            &mut out,
        )
        .await
        .expect("expected models should pass");

        assert_eq!(calls, 1);
        assert_eq!(
            String::from_utf8(out).expect("probe output should be UTF-8"),
            "ListModels returned expected models: [\"model-a\", \"model-z\"]\n\
             entry model_id=\"model-a\" inference_server_id=\"pylon-1\" \
             cluster_id=\"pylon-1-cluster\"\n\
             entry model_id=\"model-z\" inference_server_id=\"pylon-2\" \
             cluster_id=\"pylon-2-cluster\"\n"
        );
    }

    #[tokio::test]
    async fn probe_loop_reports_last_mismatched_attempt() {
        let mut args = args_with_addr("127.0.0.1:50071");
        args.expected_model_ids = vec!["model-a".to_string()];
        args.attempts = 2;
        args.interval_ms = 0;
        let mut calls = 0;
        let mut out = Vec::new();

        let err = run_probe_with(
            &args,
            |_endpoint, _request| {
                calls += 1;
                let model_id = if calls == 1 { "model-b" } else { "model-c" };
                std::future::ready(Ok(response(&[model_id], vec![entry(model_id, "pylon-1")])))
            },
            &mut out,
        )
        .await
        .expect_err("mismatched models should fail");

        let message = err.to_string();
        assert!(
            message.contains("attempt 2/2 returned [\"model-c\"]; expected [\"model-a\"]"),
            "unexpected error: {message}"
        );
        assert_eq!(calls, 2);
        assert!(out.is_empty(), "a failed probe should print nothing");

        args.attempts = 0;
        let error = run_probe_with(
            &args,
            |_, _| std::future::ready(Ok(ListModelsResponse::default())),
            &mut out,
        )
        .await
        .expect_err("zero attempts should fail");
        assert!(error.to_string().ends_with("no attempts ran"));
    }
}
