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

//! Endpoint failover against real localhost servers.

use std::time::Duration;

use gds_client::v1::ServerEndpoint;
use gds_client::{ConnectionState, Error, GdsClient, GdsClientConfig};

use crate::support::fake_gds::FakeGds;
use crate::support::{fast_policy, wait_until};

fn client(urls: &[String]) -> GdsClient {
    GdsClient::new(GdsClientConfig {
        urls: urls.to_vec(),
        policy: fast_policy(),
        ..GdsClientConfig::default()
    })
    .expect("client")
}

/// A localhost URL with nothing listening on it.
async fn dead_url() -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind");
    let url = format!("http://{}", listener.local_addr().expect("addr"));
    drop(listener);
    url
}

#[tokio::test]
async fn second_server_takes_over_and_primary_returns() {
    let primary = FakeGds::new("region-a");
    let secondary = FakeGds::new("region-b");
    let primary_server = primary.serve_tcp().await;
    let primary_addr = primary_server.addr();
    let secondary_server = secondary.serve_tcp().await;
    let client = client(&[primary_server.url(), secondary_server.url()]);
    let mut connection = client.subscribe_connection();

    let info = client.server_info().await.expect("server info");
    assert_eq!(info.region, "region-a");
    assert_eq!(client.channels().current_url(), Some(primary_server.url()));

    primary_server.shutdown().await;
    let info = client
        .server_info()
        .await
        .expect("server info after failover");
    assert_eq!(info.region, "region-b");
    assert_eq!(
        client.channels().current_url(),
        Some(secondary_server.url())
    );
    assert!(matches!(
        &*connection.borrow_and_update(),
        ConnectionState::Connected { primary: false, .. }
    ));

    // The primary comes back on the same port; the probe moves the client
    // back after primary_retry_interval.
    let primary_server = primary
        .serve_tcp_on(primary_addr)
        .await
        .expect("restart primary");
    let primary_url = primary_server.url();
    assert!(
        wait_until(Duration::from_secs(10), || {
            client.channels().current_url().as_deref() == Some(primary_url.as_str())
        })
        .await,
        "client did not return to the primary"
    );
    assert!(matches!(
        &*connection.borrow_and_update(),
        ConnectionState::Connected { primary: true, .. }
    ));
    let info = client.server_info().await.expect("server info on primary");
    assert_eq!(info.region, "region-a");
    secondary_server.shutdown().await;
    primary_server.shutdown().await;
}

#[tokio::test]
async fn unreachable_primary_falls_through_to_the_next_url() {
    let secondary = FakeGds::new("region-b");
    let secondary_server = secondary.serve_tcp().await;
    let client = client(&[dead_url().await, secondary_server.url()]);

    let info = client.server_info().await.expect("server info");
    assert_eq!(info.region, "region-b");
    secondary_server.shutdown().await;
}

#[tokio::test]
async fn all_endpoints_down_is_an_error() {
    let client = client(&[dead_url().await, dead_url().await]);
    let error = client.server_info().await.expect_err("no server");
    assert!(
        matches!(error, Error::AllEndpointsFailed { attempted: 2, .. }),
        "{error}"
    );
    assert!(matches!(
        &*client.subscribe_connection().borrow(),
        ConnectionState::Disconnected { .. }
    ));
}

#[tokio::test]
async fn advertised_endpoints_become_fallbacks() {
    let primary = FakeGds::new("region-a");
    let other = FakeGds::new("region-b");
    let primary_server = primary.serve_tcp().await;
    let other_server = other.serve_tcp().await;
    primary.set_server_endpoints(vec![
        ServerEndpoint {
            region: "region-a".to_string(),
            grpc_url: primary_server.url(),
            primary: true,
            ..ServerEndpoint::default()
        },
        ServerEndpoint {
            region: "region-b".to_string(),
            grpc_url: other_server.url(),
            ..ServerEndpoint::default()
        },
        ServerEndpoint {
            region: "broken".to_string(),
            grpc_url: "not a url at all".to_string(),
            ..ServerEndpoint::default()
        },
    ]);
    let client = client(&[primary_server.url()]);

    client.server_info().await.expect("server info");
    let other_url = other_server.url();
    assert!(
        wait_until(Duration::from_secs(5), || {
            client.channels().candidates() == [primary_server.url(), other_url.clone()]
        })
        .await,
        "advertised endpoint not merged: {:?}",
        client.channels().candidates()
    );

    primary_server.shutdown().await;
    let info = client
        .server_info()
        .await
        .expect("server info via fallback");
    assert_eq!(info.region, "region-b");
    other_server.shutdown().await;
}

#[tokio::test]
async fn reconnect_builds_a_new_connection_by_name() {
    // "localhost" goes through the resolver on every connection attempt, so
    // a server restarted on the same port is found again by name.
    let fake = FakeGds::new("region-a");
    let server = fake.serve_tcp().await;
    let port = server.addr().port();
    let client = client(&[format!("http://localhost:{port}")]);
    client.server_info().await.expect("first call");
    let generation = client
        .subscribe_connection()
        .borrow()
        .generation()
        .expect("connected");

    server.shutdown().await;
    client
        .server_info()
        .await
        .expect_err("no server while it is down");
    let server = fake
        .serve_tcp_on(format!("127.0.0.1:{port}").parse().expect("addr"))
        .await
        .expect("restart");
    let info = client.server_info().await.expect("call after restart");
    assert_eq!(info.region, "region-a");
    let new_generation = client
        .subscribe_connection()
        .borrow()
        .generation()
        .expect("connected");
    assert!(
        new_generation > generation,
        "{new_generation} <= {generation}"
    );
    server.shutdown().await;
}
