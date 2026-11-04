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

use mocks::{nats, nats_properties};
use nvcf_invocation_service::nats::NatsService;
use uuid::Uuid;

/// Tests that creating a consumer is idempotent.
///
/// This test verifies that:
/// 1. First call to create_request_stream creates both stream and consumer
/// 2. Second call to create_request_consumer succeeds idempotently (consumer already exists)
#[tokio::test]
async fn test_consumer_creation_is_idempotent() {
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

    // First call - creates stream and consumer
    let result1 = nats_service
        .create_request_stream(function_version_id)
        .await;
    assert!(
        result1.is_ok(),
        "create_request_stream should succeed: {:?}",
        result1
    );

    // Second call - should succeed idempotently (consumer already exists)
    let result2 = nats_service
        .create_request_consumer(function_version_id)
        .await;
    assert!(
        result2.is_ok(),
        "create_request_consumer should succeed idempotently when consumer exists: {:?}",
        result2
    );

    println!("Consumer creation is idempotent - calling create_request_consumer after stream+consumer already exist succeeds");
}

/// Tests that recreating a deleted consumer succeeds.
///
/// This test verifies that:
/// 1. Create stream and consumer
/// 2. Delete the consumer
/// 3. Call create_request_stream again - should recreate the consumer successfully
#[tokio::test]
async fn test_consumer_recreation_after_deletion() {
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

    // First call - creates stream and consumer
    let result1 = nats_service
        .create_request_stream(function_version_id)
        .await;
    assert!(
        result1.is_ok(),
        "First create_request_stream should succeed: {:?}",
        result1
    );

    // Get the consumer names
    let stream_name = nats_service.request_stream_name(function_version_id);
    let consumer_name = nats_service.request_consumer_name(&stream_name);

    // Verify consumer exists
    let consumer_before = nats_service
        .jetstream()
        .get_consumer_from_stream::<async_nats::jetstream::consumer::pull::Config, _, _>(
            &consumer_name,
            &stream_name,
        )
        .await;
    assert!(
        consumer_before.is_ok(),
        "Consumer should exist before deletion: {:?}",
        consumer_before
    );

    // Delete the consumer
    println!("Deleting consumer: {}", consumer_name);
    let delete_result = nats_service
        .jetstream()
        .delete_consumer_from_stream(&consumer_name, &stream_name)
        .await;
    assert!(delete_result.is_ok(), "Should delete consumer successfully");

    // Verify consumer is deleted
    let consumer_after_delete = nats_service
        .jetstream()
        .get_consumer_from_stream::<async_nats::jetstream::consumer::pull::Config, _, _>(
            &consumer_name,
            &stream_name,
        )
        .await;
    assert!(
        consumer_after_delete.is_err(),
        "Consumer should not exist after deletion"
    );

    // Recreate - should succeed
    let result2 = nats_service
        .create_request_stream(function_version_id)
        .await;
    assert!(
        result2.is_ok(),
        "Should recreate consumer successfully after deletion: {:?}",
        result2
    );

    // Verify consumer was recreated
    let consumer_after_recreate = nats_service
        .jetstream()
        .get_consumer_from_stream::<async_nats::jetstream::consumer::pull::Config, _, _>(
            &consumer_name,
            &stream_name,
        )
        .await;
    assert!(
        consumer_after_recreate.is_ok(),
        "Consumer should exist after recreation: {:?}",
        consumer_after_recreate
    );

    println!("Consumer successfully recreated after deletion");
}
