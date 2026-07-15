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

use std::collections::BTreeSet;
use std::fmt;
use std::str::FromStr;
use std::time::Duration;

use reqwest::StatusCode;
use serde::Deserialize;
use url::Url;

use crate::upstream_url::upstream_endpoint;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ModelDiscoveryProvider {
    Dynamo,
}

impl fmt::Display for ModelDiscoveryProvider {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

impl ModelDiscoveryProvider {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Dynamo => "dynamo",
        }
    }
}

impl FromStr for ModelDiscoveryProvider {
    type Err = ParseModelDiscoveryProviderError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "dynamo" => Ok(Self::Dynamo),
            _ => Err(ParseModelDiscoveryProviderError),
        }
    }
}

#[derive(Debug, Clone, Copy, thiserror::Error)]
#[error("model discovery provider must be one of: dynamo")]
pub struct ParseModelDiscoveryProviderError;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ModelDiscoveryConfig {
    pub provider: ModelDiscoveryProvider,
    pub poll_interval: Duration,
    pub request_timeout: Duration,
}

#[derive(Debug, thiserror::Error)]
pub enum ModelDiscoveryError {
    #[error("invalid upstream URL: {0}")]
    InvalidUpstreamUrl(#[from] url::ParseError),
    #[error("Dynamo model discovery request failed: {0}")]
    Request(#[from] reqwest::Error),
    #[error("Dynamo model discovery returned HTTP {0}")]
    HttpStatus(StatusCode),
    #[error("Dynamo returned an empty model id")]
    EmptyModelId,
}

#[derive(Deserialize)]
struct DynamoModelsResponse {
    data: Vec<DynamoModel>,
}

#[derive(Deserialize)]
struct DynamoModel {
    id: String,
}

pub(crate) async fn discover_model_ids(
    client: &reqwest::Client,
    upstream_base_url: &str,
    provider: ModelDiscoveryProvider,
    request_timeout: Duration,
) -> Result<BTreeSet<String>, ModelDiscoveryError> {
    match provider {
        ModelDiscoveryProvider::Dynamo => {
            discover_dynamo_model_ids(client, upstream_base_url, request_timeout).await
        }
    }
}

async fn discover_dynamo_model_ids(
    client: &reqwest::Client,
    upstream_base_url: &str,
    request_timeout: Duration,
) -> Result<BTreeSet<String>, ModelDiscoveryError> {
    let url = Url::parse(&upstream_endpoint(upstream_base_url, "/v1/models"))?;
    let response = client.get(url).timeout(request_timeout).send().await?;
    if !response.status().is_success() {
        return Err(ModelDiscoveryError::HttpStatus(response.status()));
    }
    let response = response.json::<DynamoModelsResponse>().await?;
    response
        .data
        .into_iter()
        .map(|model| {
            let model_id = model.id.trim();
            if model_id.is_empty() {
                Err(ModelDiscoveryError::EmptyModelId)
            } else {
                Ok(model_id.to_owned())
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeSet;
    use std::time::Duration;

    use axum::{Json, Router, http::StatusCode, routing::get};
    use serde_json::json;

    use super::{ModelDiscoveryProvider, discover_model_ids};
    use crate::test_support::TestHttpServer;

    async fn spawn(handler: Router) -> TestHttpServer {
        TestHttpServer::spawn(handler).await
    }

    #[tokio::test]
    async fn dynamo_models_are_sorted_and_deduplicated() {
        let base_url = spawn(Router::new().route(
            "/v1/models",
            get(|| async {
                Json(json!({
                    "data": [
                        {"id": "model-b"},
                        {"id": "model-a"},
                        {"id": "model-b"}
                    ]
                }))
            }),
        ))
        .await;

        let models = discover_model_ids(
            &reqwest::Client::new(),
            base_url.as_str(),
            ModelDiscoveryProvider::Dynamo,
            Duration::from_secs(1),
        )
        .await
        .expect("valid model list should be discovered");

        assert_eq!(
            models,
            BTreeSet::from(["model-a".to_string(), "model-b".to_string()])
        );
    }

    #[tokio::test]
    async fn empty_dynamo_model_list_is_authoritative() {
        let base_url =
            spawn(Router::new().route("/v1/models", get(|| async { Json(json!({"data": []})) })))
                .await;

        let models = discover_model_ids(
            &reqwest::Client::new(),
            base_url.as_str(),
            ModelDiscoveryProvider::Dynamo,
            Duration::from_secs(1),
        )
        .await
        .expect("empty model list should be valid");

        assert!(models.is_empty());
    }

    #[tokio::test]
    async fn empty_model_id_rejects_the_whole_poll() {
        let base_url = spawn(Router::new().route(
            "/v1/models",
            get(|| async { Json(json!({"data": [{"id": "model-a"}, {"id": "  "}]})) }),
        ))
        .await;

        let error = discover_model_ids(
            &reqwest::Client::new(),
            base_url.as_str(),
            ModelDiscoveryProvider::Dynamo,
            Duration::from_secs(1),
        )
        .await
        .expect_err("empty model ID must invalidate the poll");

        assert_eq!(error.to_string(), "Dynamo returned an empty model id");
    }

    #[tokio::test]
    async fn non_success_response_rejects_the_poll() {
        let base_url = spawn(Router::new().route(
            "/v1/models",
            get(|| async { StatusCode::SERVICE_UNAVAILABLE }),
        ))
        .await;

        let error = discover_model_ids(
            &reqwest::Client::new(),
            base_url.as_str(),
            ModelDiscoveryProvider::Dynamo,
            Duration::from_secs(1),
        )
        .await
        .expect_err("non-success response must invalidate the poll");

        assert_eq!(
            error.to_string(),
            "Dynamo model discovery returned HTTP 503 Service Unavailable"
        );
    }

    #[tokio::test]
    async fn malformed_model_list_rejects_the_poll() {
        let base_url = spawn(Router::new().route(
            "/v1/models",
            get(|| async { Json(json!({"models": ["model-a"]})) }),
        ))
        .await;

        discover_model_ids(
            &reqwest::Client::new(),
            base_url.as_str(),
            ModelDiscoveryProvider::Dynamo,
            Duration::from_secs(1),
        )
        .await
        .expect_err("missing data array must invalidate the poll");
    }

    #[tokio::test]
    async fn model_discovery_request_timeout_rejects_the_poll() {
        let base_url = spawn(Router::new().route(
            "/v1/models",
            get(|| async { std::future::pending::<Json<serde_json::Value>>().await }),
        ))
        .await;

        let error = discover_model_ids(
            &reqwest::Client::new(),
            base_url.as_str(),
            ModelDiscoveryProvider::Dynamo,
            Duration::from_millis(5),
        )
        .await
        .expect_err("blocked discovery request must honor its timeout");

        assert!(matches!(error, super::ModelDiscoveryError::Request(error) if error.is_timeout()));
    }
}
