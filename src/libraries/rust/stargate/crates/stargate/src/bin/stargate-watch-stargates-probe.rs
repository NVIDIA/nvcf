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
    let endpoint = if args.addr.starts_with("http://") || args.addr.starts_with("https://") {
        args.addr.clone()
    } else {
        format!("http://{}", args.addr)
    };

    let mut last_error = None;
    for attempt in 1..=args.attempts {
        match watch_stargates_once(&endpoint).await {
            Ok(response) => match validate_snapshot(&response, &args) {
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
                Err(error) => {
                    last_error = Some(format!(
                        "attempt {attempt}/{} returned invalid snapshot: {error:#}",
                        args.attempts
                    ));
                }
            },
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
        "WatchStargates did not return expected stargates from {endpoint}: {}",
        last_error.unwrap_or_else(|| "no attempts ran".to_string())
    )
}

async fn watch_stargates_once(
    endpoint: &str,
) -> Result<stargate_proto::pb::WatchStargatesResponse> {
    let mut client = StargateControlPlaneClient::connect(endpoint.to_string())
        .await
        .with_context(|| format!("connect to {endpoint}"))?;
    let mut stream = client
        .watch_stargates(WatchStargatesRequest {})
        .await
        .context("call WatchStargates")?
        .into_inner();

    let response = tokio::time::timeout(Duration::from_secs(5), stream.message())
        .await
        .context("timed out waiting for WatchStargates snapshot")?
        .context("read WatchStargates snapshot")?
        .context("WatchStargates stream closed before first snapshot")?;

    Ok(response)
}

fn validate_snapshot(
    response: &stargate_proto::pb::WatchStargatesResponse,
    args: &Args,
) -> Result<()> {
    if let Some(expected_count) = args.expect_stargate_count
        && response.stargates.len() != expected_count
    {
        bail!(
            "expected {expected_count} stargates; got {}: {:?}",
            response.stargates.len(),
            response.stargates
        );
    }

    if let Some(expected_count) = args.expect_watch_url_count
        && response.watch_stargate_urls.len() != expected_count
    {
        bail!(
            "expected {expected_count} watch urls; got {}: {:?}",
            response.watch_stargate_urls.len(),
            response.watch_stargate_urls
        );
    }

    let ids: HashSet<_> = response
        .stargates
        .iter()
        .map(|s| s.stargate_id.as_str())
        .collect();
    for expected_id in &args.expected_ids {
        if !ids.contains(expected_id.as_str()) {
            bail!("missing stargate_id {expected_id}; got {ids:?}");
        }
    }

    let advertise_addrs: HashSet<_> = response
        .stargates
        .iter()
        .map(|s| s.advertise_addr.as_str())
        .collect();
    for expected_advertise_addr in &args.expected_advertise_addrs {
        if !advertise_addrs.contains(expected_advertise_addr.as_str()) {
            bail!("missing advertise_addr {expected_advertise_addr}; got {advertise_addrs:?}");
        }
    }

    let grpc_pylon_dial_addrs: HashSet<_> = response
        .stargates
        .iter()
        .map(|s| s.grpc_pylon_dial_addr.as_str())
        .collect();
    for expected_grpc_pylon_dial_addr in &args.expected_grpc_pylon_dial_addrs {
        if !grpc_pylon_dial_addrs.contains(expected_grpc_pylon_dial_addr.as_str()) {
            bail!(
                "missing grpc_pylon_dial_addr {expected_grpc_pylon_dial_addr}; got {grpc_pylon_dial_addrs:?}"
            );
        }
    }

    let watch_urls: HashSet<_> = response
        .watch_stargate_urls
        .iter()
        .map(String::as_str)
        .collect();
    for expected_watch_url in &args.expected_watch_urls {
        if !watch_urls.contains(expected_watch_url.as_str()) {
            bail!("missing watch url {expected_watch_url}; got {watch_urls:?}");
        }
    }

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

#[cfg(test)]
mod tests {
    use super::*;
    use stargate_proto::pb::StargateInfo;
    use stargate_proto::pb::WatchStargatesResponse;

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
