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

use bytes::Bytes;
use mocks::{nats, nats_properties};
use nvcf_invocation_service::{
    nats::{Error, NatsService},
    nvcf_api::nvcf::WorkerInvokeFunctionRequest,
    request_id::RequestId,
};
use uuid::Uuid;

fn create_test_payload() -> WorkerInvokeFunctionRequest {
    WorkerInvokeFunctionRequest {
        request_body: Some(Bytes::from("test payload")),
        ..Default::default()
    }
}

/// Tests the max_messages_per_subject behavior of the NATS request function.
///
/// This test verifies that:
/// 1. The stream is configured with max_messages_per_subject = 1 and discard_new_per_subject = true
/// 2. First request with a request_id succeeds
/// 3. Second request with the same request_id succeeds (error is treated as success in publish_request)
/// 4. Requests with different request_ids succeed (different subjects)
/// 5. The stream configuration matches expectations
#[tokio::test]
async fn test_request_max_messages_per_subject_behavior() {
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

    // Test data
    let function_version_id = Uuid::new_v4();
    let request_id_1 = RequestId::new();
    let request_id_2 = RequestId::new();
    let payload = create_test_payload();

    // First request should succeed
    let result1 = nats_service
        .request(request_id_1, function_version_id, payload.clone())
        .await;
    assert!(
        result1.is_ok(),
        "First request should succeed: {:?}",
        result1
    );

    // Second request with the same request_id should now succeed due to the new error handling
    // (same subject, but with discard_new_per_subject=true and the new error handling,
    // "maximum messages per subject exceeded" is treated as success)
    let result2 = nats_service
        .request(request_id_1, function_version_id, payload.clone())
        .await;
    assert!(
        result2.is_ok(),
        "Second request with same ID should succeed due to new error handling: {:?}",
        result2
    );

    // Third request with different request_id should also succeed (different subject)
    let result3 = nats_service
        .request(request_id_2, function_version_id, payload.clone())
        .await;
    assert!(
        result3.is_ok(),
        "Request with different ID should succeed: {:?}",
        result3
    );

    // Verify the stream was created with correct configuration
    let stream_name = nats_service.request_stream_name(function_version_id);
    let mut stream = nats_service
        .jetstream()
        .get_stream(&stream_name)
        .await
        .expect("Should get stream info");

    let stream_info = stream.info().await.expect("Should get stream info");
    assert_eq!(
        stream_info.config.max_messages_per_subject, 1i64,
        "Stream should be configured with max_messages_per_subject = 1"
    );
    assert!(
        stream_info.config.discard_new_per_subject,
        "Stream should be configured with discard_new_per_subject = true"
    );

    // Verify that the stream has exactly 2 messages (one per unique subject)
    // With discard_new_per_subject=true, the second message to request_id_1 was rejected
    assert_eq!(
        stream_info.state.messages, 2,
        "Stream should have exactly 2 messages (one per unique request_id/subject)"
    );

    println!(
        "Stream has {} messages with discard_new_per_subject=true",
        stream_info.state.messages
    );
}

#[tokio::test]
async fn test_request_stream_full_vs_duplicate_subject_behavior() {
    let nats_container = nats().await;
    let nats_properties = nats_properties(&nats_container, Some(2)).await.unwrap(); // Small limit to test stream full

    let nats_service = NatsService::new(
        &nats_properties,
        "http://dummy.localhost",
        None,
        &nvcf_invocation_service::settings::GrpcClientConfig::default(),
    )
    .await
    .expect("Should create NATS service");

    // Test data
    let function_version_id = Uuid::new_v4();
    let request_id_1 = RequestId::new();
    let request_id_2 = RequestId::new();
    let request_id_3 = RequestId::new();
    let payload = create_test_payload();

    // First request should succeed
    let result1 = nats_service
        .request(request_id_1, function_version_id, payload.clone())
        .await;
    assert!(
        result1.is_ok(),
        "First request should succeed: {:?}",
        result1
    );

    // Second request with different subject should succeed
    let result2 = nats_service
        .request(request_id_2, function_version_id, payload.clone())
        .await;
    assert!(
        result2.is_ok(),
        "Second request with different subject should succeed: {:?}",
        result2
    );

    // Now the stream should be at max_messages (2), so a third request with new subject should fail with stream full
    let result3 = nats_service
        .request(request_id_3, function_version_id, payload.clone())
        .await;
    println!(
        "Third request result (should fail due to stream full): {:?}",
        result3
    );
    assert!(
        result3.is_err(),
        "Third request should fail due to stream being full"
    );

    // Verify it's the correct error (stream full, not per-subject limit)
    if let Err(Error::Publish(js_err)) = &result3 {
        let error_message = js_err.to_string();
        assert!(
            error_message.contains("maximum messages exceeded")
                && !error_message.contains("per subject"),
            "Should be stream full error, not per-subject error"
        );
    }

    // But a duplicate request to an existing subject should succeed (treated as success)
    let result4 = nats_service
        .request(request_id_1, function_version_id, payload.clone())
        .await;
    println!(
        "Fourth request result (duplicate subject, should succeed): {:?}",
        result4
    );
    assert!(
        result4.is_ok(),
        "Duplicate request should succeed (treated as success): {:?}",
        result4
    );

    // Check stream state
    let stream_name = nats_service.request_stream_name(function_version_id);
    let mut stream = nats_service
        .jetstream()
        .get_stream(&stream_name)
        .await
        .expect("Should get stream");
    let stream_info = stream.info().await.expect("Should get stream info");
    println!(
        "Stream state - messages: {}, max_messages: {}",
        stream_info.state.messages, stream_info.config.max_messages
    );

    println!("Stream full test completed - differentiating stream full vs duplicate subject");
}
