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

use std::collections::HashSet;
use std::fmt::Debug;
use std::future::Future;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use clap::Parser;
use stargate_proto::pb::WatchStargatesRequest;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;

#[derive(Parser, Debug)]
#[command(name = "stargate-watch-stargates-probe")]
struct Args {
    #[arg(long, value_name = "ADDR")]
    addr: String,
    #[arg(long = "expect-id", value_name = "ID")]
    expected_ids: Vec<String>,
    #[arg(long = "expect-advertise-addr", value_name = "ADDR")]
    expected_advertise_addrs: Vec<String>,
    #[arg(long = "expect-grpc-pylon-dial-addr", value_name = "ADDR")]
    expected_grpc_pylon_dial_addrs: Vec<String>,
    #[arg(long = "expect-watch-url", value_name = "URL")]
    expected_watch_urls: Vec<String>,
    #[arg(long, value_name = "N")]
    expect_stargate_count: Option<usize>,
    #[arg(long = "expect-watch-url-count", value_name = "N")]
    expect_watch_url_count: Option<usize>,
    #[arg(long = "reject-advertise-prefix", value_name = "PREFIX")]
    rejected_advertise_prefixes: Vec<String>,
    #[arg(long, default_value_t = false)]
    expect_empty_http_advertise: bool,
    #[arg(long, default_value_t = 30, value_name = "N")]
    attempts: u32,
    #[arg(long, default_value_t = 1000, value_name = "MS")]
    interval_ms: u64,
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = Args::parse();
    run_probe_with(&args, |endpoint, request| async move {
        watch_stargates_once(&endpoint, request).await
    })
    .await
}

async fn run_probe_with<F, Fut>(args: &Args, mut watch_stargates_fn: F) -> Result<()>
where
    F: FnMut(String, WatchStargatesRequest) -> Fut,
    Fut: Future<Output = Result<stargate_proto::pb::WatchStargatesResponse>>,
{
    let endpoint = endpoint_from_addr(&args.addr);

    let mut last_error = "no attempts ran".to_string();
    for attempt in 1..=args.attempts {
        last_error = match watch_stargates_fn(endpoint.clone(), WatchStargatesRequest {}).await {
            Ok(response) => match validate_snapshot(&response, args) {
                Ok(()) => {
                    let ids: Vec<_> = response
                        .stargates
                        .iter()
                        .map(|s| s.stargate_id.as_str())
                        .collect();
                    println!(
                        "WatchStargates returned expected stargates: {ids:?}; watch urls: {:?}",
                        response.watch_stargate_urls
                    );
                    return Ok(());
                }
                Err(error) => format!(
                    "attempt {attempt}/{} returned invalid snapshot: {error:#}",
                    args.attempts
                ),
            },
            Err(error) => format!("attempt {attempt}/{} failed: {error:#}", args.attempts),
        };

        if attempt < args.attempts {
            tokio::time::sleep(Duration::from_millis(args.interval_ms)).await;
        }
    }

    bail!("WatchStargates did not return expected stargates from {endpoint}: {last_error}")
}

fn endpoint_from_addr(addr: &str) -> String {
    if addr.starts_with("http://") || addr.starts_with("https://") {
        addr.to_string()
    } else {
        format!("http://{addr}")
    }
}

async fn watch_stargates_once(
    endpoint: &str,
    request: WatchStargatesRequest,
) -> Result<stargate_proto::pb::WatchStargatesResponse> {
    let mut client = StargateControlPlaneClient::connect(endpoint.to_string())
        .await
        .with_context(|| format!("connect to {endpoint}"))?;
    let mut stream = client
        .watch_stargates(request)
        .await
        .context("call WatchStargates")?
        .into_inner();

    tokio::time::timeout(Duration::from_secs(5), stream.message())
        .await
        .context("timed out waiting for WatchStargates snapshot")?
        .context("read WatchStargates snapshot")?
        .context("WatchStargates stream closed before first snapshot")
}

fn validate_snapshot(
    response: &stargate_proto::pb::WatchStargatesResponse,
    args: &Args,
) -> Result<()> {
    require_count(&response.stargates, args.expect_stargate_count, "stargates")?;
    require_count(
        &response.watch_stargate_urls,
        args.expect_watch_url_count,
        "watch urls",
    )?;
    require_expected(
        response.stargates.iter().map(|s| s.stargate_id.as_str()),
        &args.expected_ids,
        "stargate_id",
    )?;
    require_expected(
        response.stargates.iter().map(|s| s.advertise_addr.as_str()),
        &args.expected_advertise_addrs,
        "advertise_addr",
    )?;
    require_expected(
        response
            .stargates
            .iter()
            .map(|s| s.grpc_pylon_dial_addr.as_str()),
        &args.expected_grpc_pylon_dial_addrs,
        "grpc_pylon_dial_addr",
    )?;
    require_expected(
        response.watch_stargate_urls.iter().map(String::as_str),
        &args.expected_watch_urls,
        "watch url",
    )?;

    for stargate in &response.stargates {
        for rejected_prefix in &args.rejected_advertise_prefixes {
            if stargate.advertise_addr.starts_with(rejected_prefix) {
                bail!(
                    "advertise_addr {} starts with rejected prefix {rejected_prefix}",
                    stargate.advertise_addr
                );
            }
        }

        if args.expect_empty_http_advertise && !stargate.http_advertise_addr.is_empty() {
            bail!(
                "stargate_id {} reported non-empty http_advertise_addr {}",
                stargate.stargate_id,
                stargate.http_advertise_addr
            );
        }
    }

    Ok(())
}

fn require_count<T: Debug>(items: &[T], expected: Option<usize>, label: &str) -> Result<()> {
    match expected {
        Some(expected) if items.len() != expected => bail!(
            "expected {expected} {label}; got {}: {items:?}",
            items.len()
        ),
        _ => Ok(()),
    }
}

fn require_expected<'a>(
    actual: impl IntoIterator<Item = &'a str>,
    expected: &[String],
    label: &str,
) -> Result<()> {
    let actual: HashSet<_> = actual.into_iter().collect();
    if let Some(expected) = expected
        .iter()
        .find(|expected| !actual.contains(expected.as_str()))
    {
        bail!("missing {label} {expected}; got {actual:?}");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::pin::Pin;

    use futures::Stream;
    use stargate_proto::pb::stargate_control_plane_server::{
        StargateControlPlane, StargateControlPlaneServer,
    };
    use stargate_proto::pb::{
        InferenceServerAck, InferenceServerRegistration, StargateInfo, WatchStargatesResponse,
    };
    use tokio::net::TcpListener;
    use tonic::{Request, Response, Status};

    type WatchStargatesStream =
        Pin<Box<dyn Stream<Item = Result<WatchStargatesResponse, Status>> + Send + 'static>>;
    type RegisterInferenceServerStream =
        Pin<Box<dyn Stream<Item = Result<InferenceServerAck, Status>> + Send + 'static>>;

    #[derive(Clone)]
    struct TestControlPlane {
        response: WatchStargatesResponse,
    }

    #[tonic::async_trait]
    impl StargateControlPlane for TestControlPlane {
        type WatchStargatesStream = WatchStargatesStream;
        type RegisterInferenceServerStream = RegisterInferenceServerStream;

        async fn watch_stargates(
            &self,
            _request: Request<WatchStargatesRequest>,
        ) -> Result<Response<Self::WatchStargatesStream>, Status> {
            let response = self.response.clone();
            Ok(Response::new(Box::pin(futures::stream::iter([Ok(
                response,
            )]))))
        }

        async fn register_inference_server(
            &self,
            _request: Request<tonic::Streaming<InferenceServerRegistration>>,
        ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
            Err(Status::unimplemented("not needed by watch probe tests"))
        }
    }

    fn args() -> Args {
        Args {
            addr: "127.0.0.1:50071".to_string(),
            expected_ids: Vec::new(),
            expected_advertise_addrs: Vec::new(),
            expected_grpc_pylon_dial_addrs: Vec::new(),
            expected_watch_urls: Vec::new(),
            expect_stargate_count: None,
            expect_watch_url_count: None,
            rejected_advertise_prefixes: Vec::new(),
            expect_empty_http_advertise: false,
            attempts: 1,
            interval_ms: 1,
        }
    }

    fn response_with_stargate(stargate_id: &str) -> WatchStargatesResponse {
        WatchStargatesResponse {
            stargates: vec![StargateInfo {
                stargate_id: stargate_id.to_string(),
                advertise_addr: format!("{stargate_id}.stargate.external:50071"),
                http_advertise_addr: String::new(),
                grpc_pylon_dial_addr: String::new(),
            }],
            watch_stargate_urls: Vec::new(),
        }
    }

    async fn spawn_watch_server(response: WatchStargatesResponse) -> String {
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
                .add_service(StargateControlPlaneServer::new(TestControlPlane {
                    response,
                }))
                .serve_with_incoming(incoming)
                .await
                .expect("test WatchStargates server should run");
        });

        format!("http://{addr}")
    }

    #[tokio::test]
    async fn probe_loop_accepts_valid_snapshot() {
        let mut args = args();
        args.expected_ids = vec!["stargate-0".to_string()];
        args.attempts = 1;
        let mut calls = 0;

        run_probe_with(&args, |endpoint, _request| {
            calls += 1;
            assert_eq!(endpoint, "http://127.0.0.1:50071");
            std::future::ready(Ok(response_with_stargate("stargate-0")))
        })
        .await
        .expect("expected snapshot should pass");

        assert_eq!(calls, 1);
    }

    #[tokio::test]
    async fn probe_loop_reports_last_invalid_snapshot() {
        let mut args = args();
        args.expected_ids = vec!["stargate-a".to_string()];
        args.attempts = 2;
        args.interval_ms = 0;
        let mut calls = 0;

        let error = run_probe_with(&args, |_endpoint, _request| {
            calls += 1;
            let stargate_id = if calls == 1 {
                "stargate-b"
            } else {
                "stargate-c"
            };
            std::future::ready(Ok(response_with_stargate(stargate_id)))
        })
        .await
        .expect_err("invalid snapshots should fail");

        let message = error.to_string();
        assert!(
            message.contains("attempt 2/2 returned invalid snapshot"),
            "unexpected error: {message}"
        );
        assert!(
            message.contains("missing stargate_id stargate-a"),
            "unexpected error: {message}"
        );
        assert_eq!(calls, 2);

        args.attempts = 0;
        let error = run_probe_with(&args, |_, _| {
            std::future::ready(Ok(response_with_stargate("unused")))
        })
        .await
        .expect_err("zero attempts should fail");
        assert!(error.to_string().ends_with("no attempts ran"));
    }

    #[tokio::test]
    async fn watch_stargates_once_reads_first_snapshot() {
        let endpoint = spawn_watch_server(response_with_stargate("stargate-0")).await;

        let response = watch_stargates_once(&endpoint, WatchStargatesRequest {})
            .await
            .expect("watch call should read first snapshot");

        assert_eq!(response.stargates[0].stargate_id, "stargate-0");
    }

    #[test]
    fn validate_snapshot_checks_remote_watch_urls_separately_from_stargates() {
        let mut args = args();
        args.expected_ids = vec!["stargate-0".to_string(), "stargate-1".to_string()];
        args.expected_advertise_addrs = vec![
            "stargate-0.stargate-headless.region-a.svc.cluster.local:50071".to_string(),
            "stargate-1.stargate-headless.region-a.svc.cluster.local:50071".to_string(),
        ];
        args.expected_grpc_pylon_dial_addrs =
            vec!["stargate-grpc-lb.region-a.svc.cluster.local:443".to_string()];
        args.expected_watch_urls = vec!["stargate.region-b.svc.cluster.local:50071".to_string()];
        args.expect_stargate_count = Some(2);
        args.expect_watch_url_count = Some(1);
        args.rejected_advertise_prefixes = vec!["stargate.region-b".to_string()];
        args.expect_empty_http_advertise = true;

        validate_snapshot(
            &WatchStargatesResponse {
                stargates: vec![
                    StargateInfo {
                        stargate_id: "stargate-1".to_string(),
                        advertise_addr:
                            "stargate-1.stargate-headless.region-a.svc.cluster.local:50071"
                                .to_string(),
                        http_advertise_addr: String::new(),
                        grpc_pylon_dial_addr: "stargate-grpc-lb.region-a.svc.cluster.local:443"
                            .to_string(),
                    },
                    StargateInfo {
                        stargate_id: "stargate-0".to_string(),
                        advertise_addr:
                            "stargate-0.stargate-headless.region-a.svc.cluster.local:50071"
                                .to_string(),
                        http_advertise_addr: String::new(),
                        grpc_pylon_dial_addr: "stargate-grpc-lb.region-a.svc.cluster.local:443"
                            .to_string(),
                    },
                ],
                watch_stargate_urls: vec!["stargate.region-b.svc.cluster.local:50071".to_string()],
            },
            &args,
        )
        .expect("snapshot should satisfy remote watch url expectations");
    }

    #[test]
    fn validate_snapshot_rejects_remote_watch_url_in_stargates() {
        let mut args = args();
        args.expected_watch_urls = vec!["stargate.region-b.svc.cluster.local:50071".to_string()];
        args.expect_stargate_count = Some(1);
        args.expect_watch_url_count = Some(1);
        args.rejected_advertise_prefixes = vec!["stargate.region-b".to_string()];

        let error = validate_snapshot(
            &WatchStargatesResponse {
                stargates: vec![StargateInfo {
                    stargate_id: "remote-service".to_string(),
                    advertise_addr: "stargate.region-b.svc.cluster.local:50071".to_string(),
                    http_advertise_addr: String::new(),
                    grpc_pylon_dial_addr: String::new(),
                }],
                watch_stargate_urls: vec!["stargate.region-b.svc.cluster.local:50071".to_string()],
            },
            &args,
        )
        .expect_err("remote watch service must not be accepted as a stargate target");

        assert!(
            error
                .to_string()
                .contains("starts with rejected prefix stargate.region-b"),
            "unexpected error: {error:#}"
        );
    }
}
