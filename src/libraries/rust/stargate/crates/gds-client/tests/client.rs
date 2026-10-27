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

//! Typed calls, retries and raw watch streams over an in-memory channel.

use std::collections::HashMap;

use gds_client::error::{reason, status_with_reason};
use gds_client::v1::{
    EventType, ListClustersRequest, ListPoolsRequest, ListServiceImportsRequest, Location, Pool,
    ServiceImport, UpdatePoolRequest, WatchPoolsRequest, WatchServiceImportsRequest,
};
use gds_client::{Error, GdsClient};
use tonic::{Code, Status};

use crate::support::fake_gds::FakeGds;
use crate::support::fast_policy;

fn pool(id: &str, cluster: &str) -> Pool {
    Pool {
        pool_id: id.to_string(),
        name: id.to_string(),
        location: Some(Location {
            cluster: cluster.to_string(),
            ..Location::default()
        }),
        ..Pool::default()
    }
}

#[tokio::test]
async fn unavailable_is_retried_up_to_max_attempts() {
    let fake = FakeGds::new("r");
    let (channel, _server) = fake.in_memory_channel();
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());
    fake.upsert_pool(pool("p1", "c1"));

    fake.fail_next("ListPools", Status::unavailable("down"));
    fake.fail_next("ListPools", Status::deadline_exceeded("slow"));
    let response = client
        .list_pools(ListPoolsRequest::default())
        .await
        .expect("list after retries");
    assert_eq!(response.pools.len(), 1);
    assert_eq!(fake.calls("ListPools"), 3);

    for _ in 0..4 {
        fake.fail_next("ListPools", Status::unavailable("down"));
    }
    let error = client
        .list_pools(ListPoolsRequest::default())
        .await
        .expect_err("all attempts fail");
    assert_eq!(error.code(), Some(Code::Unavailable));
    assert_eq!(fake.calls("ListPools"), 3 + 4);
}

#[tokio::test]
async fn other_errors_are_not_retried_and_keep_details() {
    let fake = FakeGds::new("r");
    let (channel, _server) = fake.in_memory_channel();
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());

    fake.fail_next("ListPools", Status::invalid_argument("bad filter"));
    let error = client
        .list_pools(ListPoolsRequest::default())
        .await
        .expect_err("invalid argument");
    assert_eq!(error.code(), Some(Code::InvalidArgument));
    assert_eq!(fake.calls("ListPools"), 1);

    fake.fail_next(
        "UpdatePool",
        status_with_reason(
            Code::FailedPrecondition,
            "stale",
            reason::STALE_GENERATION,
            HashMap::from([(
                reason::METADATA_CURRENT_GENERATION.to_string(),
                "7".to_string(),
            )]),
        ),
    );
    let error = client
        .update_pool(UpdatePoolRequest::default())
        .await
        .expect_err("stale generation");
    let details = error.details().expect("error info");
    assert_eq!(details.reason, reason::STALE_GENERATION);
    assert_eq!(details.current_generation(), Some(7));
    assert!(matches!(error, Error::Rpc(_)));
}

#[tokio::test]
async fn watch_stream_follows_the_watch_contract() {
    let fake = FakeGds::new("r");
    let (channel, _server) = fake.in_memory_channel();
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());
    fake.upsert_pool(pool("p1", "c1"));
    fake.upsert_pool(pool("p2", "c2"));

    let mut stream = client
        .watch_pools(WatchPoolsRequest {
            cluster: "c1".to_string(),
            ..WatchPoolsRequest::default()
        })
        .await
        .expect("watch");
    let mut types = Vec::new();
    let revision = loop {
        let event = stream.message().await.expect("event").expect("open");
        types.push(event.r#type());
        if event.r#type() == EventType::SnapshotEnd {
            break event.revision;
        }
    };
    assert_eq!(
        types,
        [
            EventType::SnapshotStart,
            EventType::Upsert,
            EventType::SnapshotEnd
        ]
    );
    assert!(!revision.is_empty());

    fake.upsert_pool(pool("p3", "c1"));
    fake.upsert_pool(pool("p4", "c2"));
    fake.remove_pool("p1");
    let event = stream.message().await.expect("event").expect("open");
    assert_eq!(event.r#type(), EventType::Upsert);
    assert_eq!(event.pool.expect("pool").pool_id, "p3");
    let event = stream.message().await.expect("event").expect("open");
    assert_eq!(event.r#type(), EventType::Delete);
    assert_eq!(event.pool.expect("pool").pool_id, "p1");

    // Resuming from the snapshot revision replays the deltas only.
    let mut resumed = client
        .watch_pools(WatchPoolsRequest {
            cluster: "c1".to_string(),
            resume_revision: revision,
            ..WatchPoolsRequest::default()
        })
        .await
        .expect("resume");
    let event = resumed.message().await.expect("event").expect("open");
    assert_eq!(event.r#type(), EventType::Upsert);
    assert_eq!(event.pool.expect("pool").pool_id, "p3");
    assert_eq!(fake.resumes_served(), 1);

    fake.disconnect_watches();
    let error = stream.message().await.expect_err("cut");
    assert_eq!(error.code(), Some(Code::Unavailable));
}

#[tokio::test]
async fn clusters_and_service_imports() {
    let fake = FakeGds::new("r");
    let (channel, _server) = fake.in_memory_channel();
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());
    fake.upsert_cluster(gds_client::v1::Cluster {
        clusterset: "cs".to_string(),
        cluster: "c1".to_string(),
        ..Default::default()
    });
    fake.upsert_import(ServiceImport {
        namespace: "llm".to_string(),
        name: "router".to_string(),
        ..ServiceImport::default()
    });

    let clusters = client
        .list_clusters(ListClustersRequest::default())
        .await
        .expect("clusters");
    assert_eq!(clusters.clusters.len(), 1);
    let imports = client
        .list_service_imports(ListServiceImportsRequest::default())
        .await
        .expect("imports");
    assert_eq!(imports.service_imports.len(), 1);
    let mut stream = client
        .watch_service_imports(WatchServiceImportsRequest::default())
        .await
        .expect("watch imports");
    let first = stream.message().await.expect("event").expect("open");
    assert_eq!(first.r#type(), EventType::SnapshotStart);
    let info = client.server_info().await.expect("server info");
    assert_eq!(info.region, "r");
}
