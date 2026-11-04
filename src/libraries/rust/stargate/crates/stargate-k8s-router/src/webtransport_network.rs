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
use std::net::SocketAddr;

use anyhow::{Context, Result, anyhow, bail};

pub(super) async fn resolve_upstream_addrs(upstream_addr: &str) -> Result<Vec<SocketAddr>> {
    let resolved_addrs: Vec<_> = tokio::net::lookup_host(upstream_addr)
        .await
        .with_context(|| format!("resolve upstream address {upstream_addr}"))?
        .collect();
    upstream_dial_candidates(resolved_addrs)
        .with_context(|| format!("resolve upstream address {upstream_addr}"))
}

pub(super) fn upstream_dial_candidates(resolved_addrs: Vec<SocketAddr>) -> Result<Vec<SocketAddr>> {
    let candidates = stargate_tls::ordered_dial_candidates(resolved_addrs);
    if candidates.is_empty() {
        return Err(anyhow!("no resolved upstream address"));
    }
    Ok(candidates)
}

pub(super) async fn connect_first_upstream_candidate<T, Connect, ConnectFuture>(
    candidates: &[SocketAddr],
    mut connect: Connect,
) -> Result<(SocketAddr, T)>
where
    Connect: FnMut(SocketAddr) -> ConnectFuture,
    ConnectFuture: Future<Output = Result<T>>,
{
    let mut failures = Vec::new();
    for candidate in candidates {
        match connect(*candidate).await {
            Ok(connection) => return Ok((*candidate, connection)),
            Err(error) => failures.push(format!("{candidate}: {error:#}")),
        }
    }

    bail!(
        "failed to connect to every resolved upstream address: {}",
        failures.join("; ")
    )
}

#[cfg(test)]
mod tests {
    use std::net::SocketAddr;
    use std::sync::{Arc, Mutex};

    use super::*;

    #[test]
    fn upstream_dial_candidates_keep_ipv6_after_ipv4_preference() {
        let ipv6: SocketAddr = "[fd00::1]:50072"
            .parse()
            .expect("IPv6 address should parse");
        let ipv4: SocketAddr = "10.0.0.4:50072".parse().expect("IPv4 address should parse");

        assert_eq!(
            upstream_dial_candidates(vec![ipv6, ipv4]).unwrap(),
            vec![ipv4, ipv6]
        );
    }

    #[test]
    fn upstream_dial_candidates_reject_empty_resolution() {
        let error = upstream_dial_candidates(Vec::new())
            .expect_err("an empty DNS result cannot provide an upstream QUIC target");

        assert_eq!(error.to_string(), "no resolved upstream address");
    }

    #[tokio::test]
    async fn connect_first_upstream_candidate_retries_later_addresses() {
        let first: SocketAddr = "127.0.0.1:50072".parse().unwrap();
        let second: SocketAddr = "127.0.0.1:50073".parse().unwrap();
        let attempts = Arc::new(Mutex::new(Vec::new()));
        let attempts_for_connect = attempts.clone();

        let (connected_to, value) =
            connect_first_upstream_candidate(&[first, second], move |candidate| {
                let attempts = attempts_for_connect.clone();
                async move {
                    attempts
                        .lock()
                        .expect("attempts lock should not be poisoned")
                        .push(candidate);
                    if candidate == first {
                        Err(anyhow!("first candidate rejected"))
                    } else {
                        Ok("connected")
                    }
                }
            })
            .await
            .expect("later candidate should be attempted after the first failure");

        assert_eq!(connected_to, second);
        assert_eq!(value, "connected");
        assert_eq!(
            attempts
                .lock()
                .expect("attempts lock should not be poisoned")
                .as_slice(),
            &[first, second]
        );
    }

    #[tokio::test]
    async fn connect_first_upstream_candidate_reports_every_failed_address() {
        let first: SocketAddr = "127.0.0.1:50072".parse().unwrap();
        let second: SocketAddr = "127.0.0.1:50073".parse().unwrap();

        let error = connect_first_upstream_candidate(&[first, second], |candidate| async move {
            Err::<(), _>(anyhow!("candidate {candidate} rejected"))
        })
        .await
        .expect_err("all failed candidates must surface an aggregate connection error");
        let message = format!("{error:#}");

        assert!(message.contains("failed to connect to every resolved upstream address"));
        assert!(message.contains(&format!("{first}: candidate {first} rejected")));
        assert!(message.contains(&format!("{second}: candidate {second} rejected")));
    }
}
