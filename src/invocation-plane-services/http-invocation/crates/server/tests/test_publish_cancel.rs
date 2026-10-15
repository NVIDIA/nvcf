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

mod mocks;

use futures::StreamExt;
use mocks::{nats, nats_properties};
use nvcf_invocation_service::{nats::NatsService, request_id::RequestId};
use std::time::Duration;
use uuid::Uuid;

/// Subscribers on nvcf.cancel.<fvid> see the request id; other fvids don't.
#[tokio::test]
async fn test_publish_cancel_sends_to_correct_subject_with_request_id_payload() {
    let nats_container = nats().await;
    let nats_properties = nats_properties(&nats_container, None).await.unwrap();

    let nats_service = NatsService::new(
        &nats_properties,
        "http://dummy.localhost",
        None,
        &nvcf_invocation_service::settings::GrpcClientConfig::default(),
    )
    .await
    .expect("Should create NATS service");

    let function_version_id = Uuid::new_v4();
    let request_id = RequestId::new();
    let expected_subject = format!("nvcf.cancel.{}", function_version_id);

    // separate connection so we observe over the wire, not in-process
    let subscriber_client = async_nats::connect(&nats_properties.nats_address)
        .await
        .expect("subscriber should connect");
    let mut sub = subscriber_client
        .subscribe(expected_subject.clone())
        .await
        .expect("subscribe should succeed");
    subscriber_client
        .flush()
        .await
        .expect("flush subscribe interest");

    nats_service
        .publish_cancel(request_id, function_version_id)
        .await
        .expect("publish_cancel should succeed");

    let msg = tokio::time::timeout(Duration::from_secs(5), sub.next())
        .await
        .expect("should receive within timeout")
        .expect("subscription should not close");

    assert_eq!(msg.subject.as_str(), expected_subject);
    assert_eq!(
        std::str::from_utf8(&msg.payload).expect("payload is utf8"),
        request_id.to_string(),
    );

    // scoping check: another fvid's subscriber must stay quiet
    let other_fvid = Uuid::new_v4();
    let other_subject = format!("nvcf.cancel.{}", other_fvid);
    let mut other_sub = subscriber_client
        .subscribe(other_subject)
        .await
        .expect("second subscribe should succeed");
    subscriber_client.flush().await.unwrap();

    nats_service
        .publish_cancel(request_id, function_version_id)
        .await
        .expect("second publish_cancel should succeed");

    // small wait is enough — same-process publish would have arrived already
    let result = tokio::time::timeout(Duration::from_millis(200), other_sub.next()).await;
    assert!(
        result.is_err(),
        "wrong-fvid subscriber should not receive cancel"
    );
}
