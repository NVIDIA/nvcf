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

mod non_jitter_cache;

use crate::metrics::record_aws_request_status;
use crate::nvcf_api::nvcf::InputAssetReference;
use crate::request_id::RequestId;
use crate::s3::non_jitter_cache::NonJitterTTLIdentityCache;
use anyhow::Context;
use aws_config::{BehaviorVersion, Region, SdkConfig};
use aws_sdk_s3::config::{Credentials, SharedCredentialsProvider};
use aws_sdk_s3::operation::head_object::HeadObjectError;
use aws_sdk_s3::presigning::PresigningConfig;
use aws_smithy_runtime_api::box_error::BoxError;
use aws_smithy_runtime_api::client::interceptors::{
    context::{BeforeTransmitInterceptorContextRef, FinalizerInterceptorContextRef},
    Intercept,
};
use aws_smithy_runtime_api::client::runtime_components::RuntimeComponents;
use aws_smithy_types::config_bag::{ConfigBag, Layer, Storable, StoreReplace};
use http::StatusCode;
use http::Uri;
use serde_with::serde_derive::{Deserialize, Serialize};
use std::time::Duration;
use uuid::Uuid;

pub struct S3Service {
    config: S3Properties,
    s3_client: aws_sdk_s3::Client,
}

#[derive(Deserialize, Serialize, Clone, Debug)]
pub struct S3Properties {
    pub provisioned_region: String,
    pub results_bucket: String,
    pub assets_bucket: String,
    pub endpoint_override: Option<String>,
    pub enable_large_response_url: bool,
}

impl Default for S3Properties {
    fn default() -> Self {
        Self {
            provisioned_region: "us-east-1".into(),
            results_bucket: "results-bucket".into(),
            assets_bucket: "assets-bucket".into(),
            endpoint_override: None,
            enable_large_response_url: true,
        }
    }
}

// the presigned urls given must be valid for at least 30 minutes,
// since workers will not attempt to refresh credentials before then.
// extra 4 minutes for buffer.
const PRESIGNED_URL_EXPIRY: Duration = Duration::from_secs(34 * 60);

#[derive(thiserror::Error, Debug)]
pub enum Error {
    /// Error from the underlying s3 response
    #[error("NotFound error: failed to find asset {asset_id}")]
    NotFound {
        #[source]
        source: aws_sdk_s3::types::error::NotFound,
        asset_id: Uuid,
    },
    /// There was an error from something else
    #[error("Other error: {0}")]
    Other(#[from] anyhow::Error),
}

// Define a storable for the host
#[derive(Debug)]
struct HostDataType {
    host_data: String,
}

impl Storable for HostDataType {
    type Storer = StoreReplace<Self>;
}

// Our custom metrics interceptor
#[derive(Debug)]
struct MetricsInterceptor;
const UNKNOWN_VALUE: &str = "unknown"; // unknown value when we can't read the host or the status code

impl Intercept for MetricsInterceptor {
    fn name(&self) -> &'static str {
        "MetricsInterceptor"
    }

    // Hook to capture the host before the request is signed
    fn read_before_signing(
        &self,
        context: &BeforeTransmitInterceptorContextRef<'_>,
        _runtime_components: &RuntimeComponents,
        cfg: &mut ConfigBag,
    ) -> Result<(), BoxError> {
        let request = context.request();
        let uri_str = request.uri();
        let host = match uri_str.parse::<Uri>() {
            Ok(uri) => uri.host().unwrap_or(UNKNOWN_VALUE).to_string(),
            Err(_) => UNKNOWN_VALUE.to_string(),
        };

        // Store the host in the ConfigBag
        let mut layer = Layer::new("host");
        layer.store_put(HostDataType {
            host_data: host.to_string(),
        });
        cfg.push_layer(layer);

        Ok(())
    }

    fn read_after_execution(
        &self,
        context: &FinalizerInterceptorContextRef<'_>,
        _runtime_components: &RuntimeComponents,
        cfg: &mut ConfigBag,
    ) -> Result<(), BoxError> {
        // Extract the host from ConfigBag
        let host = cfg
            .load::<HostDataType>()
            .map(|host_data| host_data.host_data.clone())
            .unwrap_or_else(|| UNKNOWN_VALUE.to_string());

        let maybe_http_resp = context.response();
        let maybe_out_or_err = context.output_or_error();

        // If there's a real HTTP response, then record its status code
        if let Some(http_response) = maybe_http_resp {
            record_aws_request_status(host, http_response.status().into());
            return Ok(());
        }

        // Otherwise, rely on output_or_error()
        match maybe_out_or_err {
            Some(Ok(_operation_output)) => {
                // if there is output, then it's a success
                // there is no direct status code in the output but HEAD calls usually return 200 for success
                record_aws_request_status(host, StatusCode::OK);
            }
            Some(Err(_orchestrator_err)) => {
                // otherwise, it's an error
                // record server internal error in metrics as there is no direct mapping from aws error to http status code
                record_aws_request_status(host, StatusCode::INTERNAL_SERVER_ERROR);
            }
            None => {
                // => No response, no output, no error => short-circuit or cached
                // Skip recording metrics
            }
        }

        Ok(())
    }
}

impl S3Service {
    pub async fn new(s3_properties: &S3Properties) -> anyhow::Result<Self> {
        let test = s3_properties.endpoint_override.is_some();
        let credentials_provider = if test {
            SharedCredentialsProvider::new(Credentials::for_tests())
        } else {
            aws_config::load_from_env()
                .await
                .credentials_provider()
                .context("Failed to load credentials")?
        };
        let sdk_config = if let Some(endpoint_override) = &s3_properties.endpoint_override {
            SdkConfig::builder()
                .behavior_version(BehaviorVersion::latest())
                .endpoint_url(endpoint_override.clone())
        } else {
            aws_config::load_from_env().await.into_builder()
        }
        .region(Some(Region::new(s3_properties.provisioned_region.clone())))
        .credentials_provider(credentials_provider)
        .identity_cache(
            // keep a buffer for the max time that the credentials must be valid for signing any given request
            NonJitterTTLIdentityCache::new(PRESIGNED_URL_EXPIRY),
        )
        .build();
        // A custom endpoint cannot be combined with S3 Accelerate
        let config = if s3_properties.endpoint_override.is_none() {
            aws_sdk_s3::Config::new(&sdk_config)
                .to_builder()
                .accelerate(true)
        } else {
            aws_sdk_s3::Config::new(&sdk_config)
                .to_builder()
                .force_path_style(true) // localstack
        }
        .interceptor(MetricsInterceptor)
        .build();
        let s3_client = aws_sdk_s3::Client::from_conf(config);
        Ok(S3Service {
            config: s3_properties.clone(),
            s3_client,
        })
    }

    #[tracing::instrument(level = tracing::Level::DEBUG, skip(self), err)]
    pub async fn get_asset_dto(
        &self,
        nca_id: &str,
        asset_id: Uuid,
    ) -> Result<InputAssetReference, Error> {
        let key = format!("{nca_id}/{asset_id}");
        let head = self
            .s3_client
            .head_object()
            .bucket(&self.config.assets_bucket)
            .key(&key)
            .send()
            .await
            .map_err(|err| {
                if let Some(HeadObjectError::NotFound(err)) = err.as_service_error() {
                    Error::NotFound {
                        source: err.clone(),
                        asset_id,
                    }
                } else {
                    anyhow::anyhow!("failed to HEAD asset {}: {}", asset_id, err).into()
                }
            })?;
        let url = self
            .presigned_url_get(&self.config.assets_bucket, &key)
            .await
            .with_context(|| format!("failed to sign presigned URL for asset {}", key))?;
        Ok(InputAssetReference {
            asset_id: asset_id.to_string(),
            reference: url,
            content_type: head
                .content_type
                .ok_or_else(|| anyhow::anyhow!("asset missing content type"))?,
        })
    }

    #[tracing::instrument(level = tracing::Level::DEBUG, skip(self), err)]
    pub async fn get_large_response_upload_url(
        &self,
        nca_id: &str,
        request_id: RequestId,
    ) -> anyhow::Result<String> {
        if !self.config.enable_large_response_url {
            return Ok("".into());
        }
        let key = format!("{nca_id}/{request_id}");
        self.presigned_url_put(&self.config.results_bucket, &key, "application/zip")
            .await
            .with_context(|| {
                format!(
                    "failed to sign presigned response upload URL for request ID {}",
                    request_id
                )
            })
    }

    #[tracing::instrument(level = tracing::Level::DEBUG, skip(self), err)]
    pub async fn get_large_response_download_url(
        &self,
        nca_id: &str,
        request_id: RequestId,
    ) -> anyhow::Result<String> {
        let key = format!("{nca_id}/{request_id}");
        self.presigned_url_get(&self.config.results_bucket, &key)
            .await
            .with_context(|| {
                format!(
                    "failed to sign presigned response download URL for request ID {}",
                    request_id
                )
            })
    }

    async fn presigned_url_get(&self, bucket: &str, key: &str) -> anyhow::Result<String> {
        let request = self
            .s3_client
            .get_object()
            .bucket(bucket)
            .key(key)
            .presigned(PresigningConfig::expires_in(PRESIGNED_URL_EXPIRY)?)
            .await
            .context("failed to get presigned download url")?;
        Ok(request.uri().to_string())
    }

    async fn presigned_url_put(
        &self,
        bucket: &str,
        key: &str,
        content_type: &str,
    ) -> anyhow::Result<String> {
        let request = self
            .s3_client
            .put_object()
            .bucket(bucket)
            .key(key)
            .content_type(content_type)
            .presigned(PresigningConfig::expires_in(PRESIGNED_URL_EXPIRY)?)
            .await
            .context("failed to get presigned upload url")?;
        Ok(request.uri().to_string())
    }
}
