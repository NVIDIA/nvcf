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

#![allow(dead_code)] // this module is used across multiple test binaries. not all functions are used in all test binaries.

use aws_sdk_s3::config::{BehaviorVersion, Credentials, Region, SharedCredentialsProvider};
use futures::join;
use localstack::LocalStack;
use nvcf_api_mock::{ApiClient, ApiMock, ApiMockServer, FunctionMetadata};
use nvcf_invocation_service::telemetry::{
    initialize_tracing, settings::TracingSettings, TracingGuard,
};
use nvcf_invocation_service::worker_streams::WorkerStreamProperties;
use nvcf_invocation_service::{nats::NatsProperties, s3::S3Properties, settings::AppConfig};
use std::sync::OnceLock;
use testcontainers_modules::nats::Nats;
use testcontainers_modules::testcontainers::core::AccessMode::ReadOnly;
use testcontainers_modules::testcontainers::core::Mount;
use testcontainers_modules::testcontainers::runners::AsyncRunner;
use testcontainers_modules::testcontainers::{ContainerAsync, ImageExt, TestcontainersError};
use uuid::{uuid, Uuid};

mod localstack;
pub mod nvcf_api_mock;
pub mod nvcf_worker_mock;
mod otel_collector;
pub mod rate_limit_mock;

pub const FUNCTION_ID: Uuid = uuid!("874e38f8-00a6-4403-8d4d-6998fe89e77f");
pub const FUNCTION_ID_2_RATELIMIT_SYNC: Uuid = uuid!("c294c1c9-4702-4355-988d-2ff46cbfd675");
pub const FUNCTION_ID_3_RATELIMIT_ASYNC: Uuid = uuid!("a1373a48-0806-4b9c-811d-03e040cf3481");
pub const VERSION_ID_1: Uuid = uuid!("26597542-1782-4a18-aa02-504ba0598202");
pub const VERSION_ID_2: Uuid = uuid!("49331123-201a-407d-9cbc-7bb328fd0295");
pub const VERSION_ID_3: Uuid = uuid!("4117e32b-1b96-4be2-8cd2-9da4047daa05");
pub const VERSION_ID_4: Uuid = uuid!("2bf046f7-37c1-40b8-b2c2-8f8d260f7645");
pub const LOCALSTACK_REGION: &str = "us-east-1";
pub const ASSETS_BUCKET: &str = "assets-bucket";
pub const RESULTS_BUCKET: &str = "results-bucket";
pub const API_KEY: &str = "test-token";
pub const NCA_ID: &str = "test-nca-id";
#[allow(unused)]
pub const INSTANCE_ID: &str = "local-worker-instance-id";

pub async fn fixtures() -> (
    ContainerAsync<LocalStack>,
    ContainerAsync<Nats>,
    ApiMockServer,
    AppConfig,
) {
    setup_tracing_no_export("mock_server".to_string());
    let (localstack, nats, nvcf) = join!(localstack_(), nats(), mock_nvcf_api());
    let nats_properties = nats_properties(&nats, None).await.unwrap();
    let config = AppConfig::new(
        &nvcf.address(),
        nats_properties.clone(),
        localstack_s3_config(&localstack).await.unwrap(),
        Some(nvcf.properties()),
        WorkerStreamProperties {
            self_address: Some("TEST_SERVER_ADDRESS".into()),
            ..Default::default()
        },
    );
    (localstack, nats, nvcf, config)
}

async fn localstack_() -> ContainerAsync<LocalStack> {
    tracing::info!("starting localstack");
    let localstack = LocalStack::default()
        .with_tag("3.0")
        .with_env_var("SERVICES", "s3")
        .with_env_var("SKIP_SSL_CERT_DOWNLOAD", "1")
        .with_env_var("DISABLE_EVENTS", "1")
        .with_env_var("AWS_DEFAULT_REGION", LOCALSTACK_REGION)
        .start()
        .await
        .expect("start localstack container");
    tracing::info!("creating localstack buckets");
    let s3_client = localstack_s3_client(&localstack).await.unwrap();
    futures::try_join!(
        s3_client.create_bucket().bucket(RESULTS_BUCKET).send(),
        s3_client.create_bucket().bucket(ASSETS_BUCKET).send()
    )
    .unwrap();
    tracing::info!("started localstack");
    localstack
}

pub async fn nats() -> ContainerAsync<Nats> {
    tracing::info!("starting nats");
    let ret = Nats::default()
        .with_tag("2.10.25")
        .with_mount(
            Mount::bind_mount(
                concat!(env!("CARGO_MANIFEST_DIR"), "/tests/mocks/nats-server.conf"),
                "/nats-server.conf",
            )
            .with_access_mode(ReadOnly),
        )
        .start()
        .await
        .expect("start nats container");
    tracing::info!("started nats");
    ret
}

async fn mock_nvcf_api() -> ApiMockServer {
    ApiMock {
        functions: [
            (
                FUNCTION_ID,
                FunctionMetadata {
                    functions: vec![VERSION_ID_1, VERSION_ID_2],
                    has_rate_limit: false,
                    sync_check: false,
                },
            ),
            // FUNCTION_ID_2_RATELIMIT_SYNC has rate limit and sync check
            (
                FUNCTION_ID_2_RATELIMIT_SYNC,
                FunctionMetadata {
                    functions: vec![VERSION_ID_3],
                    has_rate_limit: true,
                    sync_check: true,
                },
            ),
            // FUNCTION_ID_3_RATELIMIT_ASYNC has rate limit and async check
            (
                FUNCTION_ID_3_RATELIMIT_ASYNC,
                FunctionMetadata {
                    functions: vec![VERSION_ID_4],
                    has_rate_limit: true,
                    sync_check: false,
                },
            ),
        ]
        .into_iter()
        .collect(),
        clients: [(
            API_KEY.to_string(),
            ApiClient {
                subject: "test-subject".into(),
                nca_id: NCA_ID.into(),
            },
        )]
        .into_iter()
        .collect(),
    }
    .into_server()
    .await
}

fn setup_tracing_no_export(project_name: String) -> &'static TracingGuard {
    static INIT: OnceLock<TracingGuard> = OnceLock::new();
    INIT.get_or_init(|| {
        let module_name = module_path!();
        initialize_tracing(&project_name, &TracingSettings::default(), None,
                           format!("{module_name}=debug,server=trace,nvcf_invocation_service=trace,axum::rejection=trace,otel::tracing=trace,otel=debug,async_nats=debug,info"))
    })
}

pub async fn localstack_s3_config(
    localstack: &ContainerAsync<LocalStack>,
) -> anyhow::Result<S3Properties> {
    let localstack_port = localstack.get_host_port_ipv4(4566).await?;
    Ok(S3Properties {
        provisioned_region: LOCALSTACK_REGION.into(),
        results_bucket: RESULTS_BUCKET.into(),
        assets_bucket: ASSETS_BUCKET.into(),
        endpoint_override: Some(format!("http://localhost:{localstack_port}")),
        enable_large_response_url: true,
    })
}

pub async fn localstack_s3_client(
    localstack: &ContainerAsync<LocalStack>,
) -> anyhow::Result<aws_sdk_s3::Client> {
    let localstack_port = localstack.get_host_port_ipv4(4566).await?;
    let s3_client = aws_sdk_s3::Client::from_conf(
        aws_sdk_s3::Config::new(
            &aws_config::SdkConfig::builder()
                .behavior_version(BehaviorVersion::latest())
                .region(Some(Region::new(LOCALSTACK_REGION)))
                .endpoint_url(format!("http://localhost:{}", localstack_port))
                .credentials_provider(SharedCredentialsProvider::new(Credentials::for_tests()))
                .build(),
        )
        .to_builder()
        .force_path_style(true)
        .build(),
    );
    Ok(s3_client)
}

pub async fn nats_properties(
    nats: &ContainerAsync<Nats>,
    max_messages: Option<i64>,
) -> Result<NatsProperties, TestcontainersError> {
    let nats_port = nats.get_host_port_ipv4(4222).await?;
    let nats_host = nats.get_host().await?;
    let mut nats_properties = NatsProperties {
        nats_address: format!("nats://{}:{}", nats_host, nats_port),
        max_messages: max_messages.unwrap_or(NatsProperties::default().max_messages),
        ..Default::default()
    };
    // Reduce the delay to make the tests run faster
    nats_properties.retry_strategy.initial_delay_ms = 10;
    Ok(nats_properties)
}
