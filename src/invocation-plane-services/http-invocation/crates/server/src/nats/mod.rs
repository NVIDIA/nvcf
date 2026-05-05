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

mod retry;

use crate::{
    metrics::{
        record_nats_error_total, record_nats_event, record_nats_jetstream_publish,
        register_nats_statistics,
    },
    nats::retry::{jitter, retry, ExponentialBackoff},
    nvcf_api::nvcf::{WorkerInvokeFunctionRequest, WorkerResultTracking},
    nvcf_api::oauth2_client::TokenProducer,
    request_id::RequestId,
    secrets::secret_provider::SecretFileWatcher,
};
use anyhow::anyhow;
use async_nats::{
    connection::State,
    jetstream::{
        self, consumer,
        context::{
            CreateStreamError, CreateStreamErrorKind, PublishAckFuture, PublishError,
            PublishErrorKind,
        },
        stream::{
            Config, ConsumerError, ConsumerErrorKind, ConsumerLimits, DirectGetErrorKind,
            DiscardPolicy, Placement, RetentionPolicy, StorageType,
        },
        Context, ErrorCode,
    },
    AuthError, Client, ConnectOptions, Event, HeaderMap, RequestErrorKind, SubscribeError,
};
use bytes::Bytes;
use futures::{FutureExt, StreamExt, TryFutureExt};
use oauth2::Scope;
use opentelemetry::propagation::Injector;
use prost::Message;
use rand;
use serde::{Deserialize, Serialize};
use std::fmt::Debug;
use std::pin::pin;
use std::sync::Arc;
use std::time::{Duration, SystemTime};
use sync_wrapper;
use tracing::{Level, Span};
use uom::si::information;
use uom::si::usize::Information;
use uuid::Uuid;

type NatsAuthCallbackFuture = std::pin::Pin<
    Box<dyn std::future::Future<Output = Result<async_nats::Auth, AuthError>> + Send + Sync>,
>;

pub struct NatsService {
    client: Client,
    jetstream: Context,
    nats_properties: NatsProperties,
}

#[derive(Deserialize, Serialize, Clone, Debug)]
pub struct NatsProperties {
    pub nats_address: String,
    pub region: String,
    #[serde(deserialize_with = "de_csv_str")]
    pub other_regions: Vec<String>,
    pub replicas: usize,
    pub max_messages: i64,
    pub retry_strategy: NatsRetryStrategy,
    pub placement_tag_key: String,
    /// Auth-callout plugin name registered on the NATS server side. Sent
    /// verbatim as the `pluginName` field in the auth-callout token payload.
    pub auth_plugin_name: String,
}

#[derive(Deserialize, Serialize, Clone, Debug)]
pub struct NatsRetryStrategy {
    pub retries: usize,
    pub initial_delay_ms: u64,
    pub backoff_factor: f64,
}

impl Default for NatsRetryStrategy {
    fn default() -> Self {
        Self {
            retries: 5,
            initial_delay_ms: 1000,
            backoff_factor: 2.0,
        }
    }
}

#[derive(thiserror::Error, Debug)]
pub enum Error {
    /// Errors from async_nats
    #[error("Subscribe error: {0}")]
    Subscribe(#[from] SubscribeError),

    #[error("Jetstream error: {0}")]
    Publish(#[from] jetstream::Error),

    /// Error from something else
    #[error("Other error: {0}")]
    Other(#[from] anyhow::Error),
}

#[derive(Debug)]
enum PublishErrorClassification {
    TreatAsSuccess,
    Terminal,
    Retryable,
}

impl From<PublishError> for Error {
    fn from(err: PublishError) -> Self {
        match err.kind() {
            PublishErrorKind::Other => {
                if let Some(src) = std::error::Error::source(&err) {
                    tracing::warn!("failed to publish request: {:?}", src);
                    if let Some(js_err) = src.downcast_ref::<jetstream::Error>() {
                        if js_err.error_code() == ErrorCode::STREAM_STORE_FAILED {
                            // Propagate the queue filled error up to the controller and not expose
                            // http status codes at this layer (see crate::nvcf_api::Error)
                            return Self::Publish(js_err.clone());
                        }
                    }
                }
                Self::Other(anyhow!(err.to_string()))
            }
            _ => Self::Other(anyhow::Error::new(err).context("Error publishing to nats")),
        }
    }
}

fn de_csv_str<'de, D>(deserializer: D) -> Result<Vec<String>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let raw: serde_json::Value = Deserialize::deserialize(deserializer)?;

    match raw {
        serde_json::Value::String(s) => {
            // If the input is a string, split by commas
            Ok(s.split(',').map(|x| x.trim().to_string()).collect())
        }
        serde_json::Value::Array(arr) => {
            // If the input is an array, ensure each element is a string
            arr.into_iter()
                .map(|v| {
                    v.as_str()
                        .map(|s| s.to_string())
                        .ok_or_else(|| serde::de::Error::custom("Expected a string in array"))
                })
                .collect()
        }
        _ => Err(serde::de::Error::custom(
            "Invalid type: expected a string or array",
        )),
    }
}

impl Default for NatsProperties {
    fn default() -> Self {
        Self {
            nats_address: "nats://localhost:4222".into(),
            region: "ncp".into(),
            other_regions: vec![],
            replicas: 1,
            max_messages: 100_000,
            retry_strategy: NatsRetryStrategy::default(),
            placement_tag_key: "aws-region".into(),
            auth_plugin_name: "".into(),
        }
    }
}

#[derive(Debug)]
pub struct ExistingInvocationDetails {
    pub worker_result_tracking: WorkerResultTracking,
    pub region: String,
}

#[derive(Debug, PartialEq, Eq)]
pub enum WorkerPollingResponse {
    NoResponse,
    AckedRequest,
}

impl NatsService {
    pub async fn new(
        nats_properties: &NatsProperties,
        oauth2_token_endpoint: &str,
        secrets_provider: Option<Arc<SecretFileWatcher>>,
        grpc_config: &crate::settings::GrpcClientConfig,
    ) -> anyhow::Result<Self> {
        tracing::info!("connecting to nats {}", nats_properties.nats_address);
        let (tx, mut rx) = tokio::sync::mpsc::channel(1);

        let auth_callback = Self::create_auth_callback(
            secrets_provider.clone(),
            oauth2_token_endpoint,
            &nats_properties.auth_plugin_name,
            grpc_config,
        )?;
        let builder = ConnectOptions::with_auth_callback(auth_callback)
            .name(format!(
                "nvcf-invocation-service-{}",
                hostname::get()?.to_str().unwrap_or_default()
            ))
            .event_callback(move |event| {
                let tx = tx.clone();
                async move {
                    record_nats_event(&event);
                    let _ = tx.send(event).await;
                }
            });
        let client = builder
            .connect(&nats_properties.nats_address)
            .await
            .inspect_err(|_err| {
                record_nats_error_total();
            })?;
        tracing::info!("connected to nats");
        let client_reconnect = client.clone();
        tokio::spawn(async move {
            while let Some(event) = rx.recv().await {
                if let Event::LameDuckMode = event {
                    let client_reconnect = client_reconnect.clone();
                    tokio::spawn(async move {
                        let sleep_millis = rand::random_range(0..5000); // 0-5s
                        tokio::time::sleep(Duration::from_millis(sleep_millis)).await;
                        if client_reconnect.flush().await.is_ok() {
                            match client_reconnect.force_reconnect().await {
                                Ok(()) => {
                                    tracing::info!("reconnected");
                                }
                                Err(e) => {
                                    tracing::error!("failed to reconnect: {}", e);
                                    record_nats_error_total();
                                }
                            }
                        }
                    });
                }
            }
        });
        register_nats_statistics(client.statistics());

        let jetstream = jetstream::new(client.clone());
        let nats_service = Self {
            client: client.clone(),
            jetstream,
            nats_properties: nats_properties.clone(),
        };
        // NATS will time out instead of returning a 404 if the stream does not exist and we do a direct get.
        // create the stream eagerly to prevent timeouts.
        nats_service
            .create_request_tracking_stream()
            .await
            .inspect_err(|_err| {
                record_nats_error_total();
            })?;
        Ok(nats_service)
    }

    fn create_auth_callback(
        secrets_provider: Option<Arc<SecretFileWatcher>>,
        oauth2_token_endpoint: &str,
        auth_plugin_name: &str,
        grpc_config: &crate::settings::GrpcClientConfig,
    ) -> anyhow::Result<impl Fn(Vec<u8>) -> NatsAuthCallbackFuture + Send + Sync> {
        let token_provider = TokenProducer::new(
            oauth2_token_endpoint,
            Scope::new("admin:nats:Worker".into()),
            secrets_provider.clone(),
            None, // fixed oauth token is not used for nats. use an nkey instead.
            grpc_config,
        )?
        .map(Arc::new);
        let auth_plugin_name = auth_plugin_name.to_string();

        Ok(move |nonce: Vec<u8>| {
            let secrets_provider = secrets_provider.clone();
            let token_provider = token_provider.clone();
            let auth_plugin_name = auth_plugin_name.clone();
            Box::pin(sync_wrapper::SyncFuture::new(async move {
                let mut auth = async_nats::Auth::new();
                if let Some(secrets_provider) = secrets_provider {
                    if let Some(nkey_seed) = secrets_provider.get_config().nats.nkey_seed {
                        tracing::debug!("using nkey for nats login");
                        // Parse the keypair and sign the nonce
                        let kp = nkeys::KeyPair::from_seed(&nkey_seed).map_err(AuthError::new)?;
                        // Sign the server's nonce
                        let signature = kp.sign(&nonce).map_err(AuthError::new)?;
                        // Return Auth with nkey public key and signature
                        auth.nkey = Some(kp.public_key());
                        auth.signature = Some(signature);
                    } else if let Some(token_provider) = token_provider {
                        tracing::debug!("using oauth for nats login");
                        auth.token = Some(NatsService::serialize_oauth_token(
                            token_provider
                                .produce_token()
                                .await
                                .map_err(AuthError::new)?,
                            &auth_plugin_name,
                        ));
                    }
                }
                Ok(auth)
            })) as NatsAuthCallbackFuture
        })
    }

    // token=b64({"account":"$account","pluginName":"$pluginName","payload":"$payload"})
    // This function takes an OAuth token string and returns a base64-url encoded JSON string
    // with the required fields for our NATS auth callout.
    fn serialize_oauth_token(token: String, plugin_name: &str) -> String {
        use base64::engine::general_purpose::URL_SAFE_NO_PAD;
        use base64::Engine;
        use serde::Serialize;

        #[derive(Serialize)]
        struct TokenPayload<'a> {
            account: &'a str,
            #[serde(rename = "pluginName")]
            plugin_name: &'a str,
            payload: &'a str,
        }

        let payload = TokenPayload {
            account: "Worker",
            plugin_name,
            payload: &token,
        };

        match serde_json::to_vec(&payload) {
            Ok(token_json) => URL_SAFE_NO_PAD.encode(token_json),
            Err(err) => {
                tracing::warn!("failed to marshal nvcf token for nats: {:?}", err);
                String::new()
            }
        }
    }

    /// Returns the jetstream context.
    /// Public for integration tests that need direct access to verify stream state.
    pub fn jetstream(&self) -> &Context {
        &self.jetstream
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    pub fn health(&self) -> anyhow::Result<()> {
        match self.client.connection_state() {
            State::Connected => Ok(()),
            _ => {
                record_nats_error_total();
                Err(anyhow::anyhow!("nats not connected"))
            }
        }
    }

    #[tracing::instrument(level = Level::DEBUG, skip(self, payload), err)]
    pub async fn request(
        &self,
        request_id: RequestId,
        function_version_id: Uuid,
        payload: WorkerInvokeFunctionRequest,
    ) -> Result<(), Error> {
        Self::validate_nats_payload(&payload)?;
        self.publish_request(
            request_id,
            function_version_id,
            payload.encode_to_vec().into(),
        )
        .await?;
        Ok(())
    }

    fn validate_nats_payload(payload: &WorkerInvokeFunctionRequest) -> anyhow::Result<()> {
        // convert request payload to protobuf nats payload
        // 1MB buffer. we accept 5MB bodies and need some space for our own metadata.
        // the nats servers are configured to accept up to 8MB.
        let six_mb = Information::new::<information::mebibyte>(6).get::<information::byte>();
        if payload.encoded_len() > six_mb {
            record_nats_error_total();
            return Err(anyhow!("payload size exceeds 6MB"));
        }
        Ok(())
    }

    // responses come back external from this function. function result indicates whether a worker picked up the polling request.
    #[tracing::instrument(level = Level::DEBUG, skip(self, payload), ret, err)]
    pub async fn polling_request(
        &self,
        request_id: RequestId,
        payload: WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<WorkerPollingResponse> {
        Self::validate_nats_payload(&payload)?;
        Ok(
            match self
                .client
                .request_with_headers(
                    format!("rq_polling.{request_id}"),
                    otel_context(),
                    payload.encode_to_vec().into(),
                )
                .await
            {
                Ok(_response) => WorkerPollingResponse::AckedRequest,
                Err(err) => match err.kind() {
                    RequestErrorKind::TimedOut | RequestErrorKind::NoResponders => {
                        record_nats_error_total();
                        WorkerPollingResponse::NoResponse
                    }
                    RequestErrorKind::Other | RequestErrorKind::InvalidSubject => {
                        record_nats_error_total();
                        return Err(err.into());
                    }
                },
            },
        )
    }

    // valid only for current region,
    // but we shouldn't be cancelling requests that don't originate with our current instance anyway
    #[tracing::instrument(level = Level::DEBUG, skip(self), err)]
    pub async fn cancel_request(
        &self,
        request_id: RequestId,
        function_version_id: Uuid,
    ) -> anyhow::Result<()> {
        let subject = self.request_subject(request_id, function_version_id);
        tracing::debug!("purging request with subject {}", subject);
        let stream = self
            .jetstream
            .get_stream_no_info(self.request_stream_name(function_version_id))
            .await
            .inspect_err(|_err| {
                record_nats_error_total();
            })?;
        stream.purge().filter(subject).await.inspect_err(|_err| {
            record_nats_error_total();
        })?;
        Ok(())
    }

    // Helper function to classify errors for retry behaviour
    fn classify_publish_error(
        err: &PublishError,
        request_id: RequestId,
    ) -> PublishErrorClassification {
        // Check if this is a jetstream error with the specific error code
        if let Some(source) = std::error::Error::source(err) {
            if let Some(js_err) = source.downcast_ref::<jetstream::Error>() {
                if js_err.error_code() == ErrorCode::STREAM_STORE_FAILED {
                    // We need to differentiate between stream full vs duplicate subject
                    // Both use the same error code, so check the description after matching the code
                    let error_message = js_err.to_string();
                    if error_message.contains("maximum messages exceeded") {
                        // we *could* retry, but since the stream is full we don't want to spam it.
                        tracing::trace!("Maximum messages exceeded on stream, terminal error");
                        return PublishErrorClassification::Terminal;
                    }
                    if error_message.contains("maximum messages per subject exceeded") {
                        tracing::debug!("Maximum messages per subject exceeded for request_id {}, treating as success (message with the same subject is already present)", request_id);
                        return PublishErrorClassification::TreatAsSuccess;
                    }
                    tracing::trace!("Stream store failed for different reason for request_id {}, retryable error", request_id);
                    return PublishErrorClassification::Retryable;
                }
            }
        }
        PublishErrorClassification::Retryable
    }

    #[tracing::instrument(level = Level::DEBUG, skip(self, payload), err)]
    async fn publish_request(
        &self,
        request_id: RequestId,
        version: Uuid,
        payload: Bytes,
    ) -> Result<(), Error> {
        // Attempt the initial publish
        let ack = self
            .publish_to_stream(request_id, version, payload.clone())
            .await;

        if let Err(err) = ack {
            // If the publish failed, see if it was "stream not found", "max messages per subject exceeded", "maximum messages exceeded", or something else
            match err.kind() {
                PublishErrorKind::StreamNotFound => {
                    tracing::warn!("Stream not found, creating stream: {version}");
                    // Create the stream, then we'll do a backoff retry below
                    // Don't keep trying to create the stream in retry if that one also gives a stream not found.
                    // A stream create success followed by a not found could mean flapping.
                    // We do not want to rapidly create streams as that is an expensive operation.
                    self.create_request_stream(version).await?;
                }
                PublishErrorKind::Other => {
                    match Self::classify_publish_error(&err, request_id) {
                        PublishErrorClassification::TreatAsSuccess => {
                            // handle duplicate publishes in case nats got the message but did not ack on a previous retry.
                            return Ok(());
                        }
                        PublishErrorClassification::Terminal => {
                            // Terminal error - don't retry
                            return Err(Error::from(err));
                        }
                        PublishErrorClassification::Retryable => {
                            tracing::warn!("Failed to publish request, retrying: {:?}", err);
                        }
                    }
                }
                _ => {
                    tracing::warn!("Failed to publish request, retrying: {:?}", err);
                }
            }
            // Now retry with exponential backoff
            let retry_strategy = ExponentialBackoff::from_millis_with_factor(
                self.nats_properties.retry_strategy.initial_delay_ms,
                self.nats_properties.retry_strategy.backoff_factor,
            )
            .map(jitter)
            .take(self.nats_properties.retry_strategy.retries.max(1));

            retry(
                || async {
                    match self
                        .publish_to_stream(request_id, version, payload.clone())
                        .await
                    {
                        Ok(()) => Ok(()),
                        Err(e) => match Self::classify_publish_error(&e, request_id) {
                            PublishErrorClassification::TreatAsSuccess => Ok(()),
                            PublishErrorClassification::Terminal => {
                                Err(retry::ClassifiedError::Terminal(e))
                            }
                            PublishErrorClassification::Retryable => {
                                Err(retry::ClassifiedError::Retryable(e))
                            }
                        },
                    }
                },
                retry_strategy,
            )
            .await?;
        }

        // If we made it here, either the initial publish succeeded or the retry succeeded
        Ok(())
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    pub async fn create_request_stream(&self, version: Uuid) -> anyhow::Result<()> {
        let six_mb = Information::new::<information::mebibyte>(6).get::<information::byte>();
        let config = Config {
            name: self.request_stream_name(version),
            subjects: vec![format!("rq.{}.{}.>", self.nats_properties.region, version)],
            max_messages_per_subject: 1, // nats may have gotten the message but not acked. set to 1 to avoid duplicate requests when we retry publishing.
            discard_new_per_subject: true, // duplicates are rejected but treated as success in publish_request
            max_messages: self.nats_properties.max_messages,
            storage: StorageType::Memory,
            retention: RetentionPolicy::WorkQueue,
            placement: Some(self.own_region_placement()),
            discard: DiscardPolicy::New,
            allow_direct: true,
            duplicate_window: Duration::from_secs(5),
            num_replicas: self.nats_properties.replicas,
            consumer_limits: Some(ConsumerLimits {
                max_ack_pending: self.nats_properties.max_messages,
                ..Default::default()
            }),
            max_message_size: six_mb as i32,
            ..Default::default()
        };
        self.create_stream_if_not_present(&config).await?;
        self.create_request_consumer(version).await?;
        Ok(())
    }

    /// Returns the stream name for a given function version.
    /// Public for integration tests that need to verify stream configuration and state.
    pub fn request_stream_name(&self, version: Uuid) -> String {
        format!("rq_{}_{}", self.nats_properties.region, version)
    }

    /// Returns the consumer name for a given stream name.
    /// Public for integration tests that need to construct consumer names.
    pub fn request_consumer_name(&self, stream_name: &str) -> String {
        format!("{stream_name}_workers")
    }

    /// Creates a consumer for the request stream.
    /// Public for integration tests that need to test consumer creation directly.
    #[tracing::instrument(level = Level::DEBUG, skip_all, fields(version = version.to_string()), err)]
    pub async fn create_request_consumer(&self, version: Uuid) -> Result<(), ConsumerError> {
        let stream_name = self.request_stream_name(version);
        let consumer_name = self.request_consumer_name(&stream_name);
        let consumer_config = consumer::pull::Config {
            durable_name: Some(consumer_name.clone()),
            ..Default::default()
        };
        match self
            .jetstream
            .create_consumer_on_stream(consumer_config, stream_name.clone())
            .await
        {
            Ok(_) => {
                tracing::debug!("created consumer {}", consumer_name);
                Ok(())
            }
            Err(err) => {
                // Check if this is a JetStream error indicating the consumer already exists
                // Error code 10148: Consumer already exists
                if matches!(err.kind(), ConsumerErrorKind::JetStream(js_err)
                    if js_err.kind() == ErrorCode::CONSUMER_ALREADY_EXISTS)
                {
                    tracing::debug!("consumer {} already exists", consumer_name);
                    return Ok(());
                }

                // Check if this is a general CONSUMER_CREATE error (error code 10012)
                // with an immutable field update message (which means consumer already exists)
                if let ConsumerErrorKind::JetStream(js_err) = err.kind() {
                    if js_err.kind() == ErrorCode::CONSUMER_CREATE {
                        // In NATS server v2.10.25, it could return 8 immutable field errors like below for general consumer creation failure.
                        //   - "max waiting can not be updated"
                        //   - "deliver policy can not be updated"
                        //   - "ack policy can not be updated"
                        // Note: We only check for "X can not be updated" pattern (immutable field errors).
                        // The "can not update X" pattern (changing pull↔push consumer types) is not
                        // treated as success and will propagate as an error.
                        let error_message = js_err.to_string();
                        if error_message.contains("can not be updated") {
                            tracing::debug!(
                                "consumer {} already exists with different immutable config, treating as success: {}",
                                consumer_name,
                                error_message
                            );
                            return Ok(());
                        }
                    }
                }

                // For other errors, propagate them
                tracing::warn!("failed to create consumer {}: {:?}", consumer_name, err);
                record_nats_error_total();
                Err(err)
            }
        }
    }

    fn own_region_placement(&self) -> Placement {
        Placement {
            tags: vec![format!(
                "{}:{}",
                self.nats_properties.placement_tag_key, self.nats_properties.region
            )],
            ..Default::default()
        }
    }

    #[tracing::instrument(level = Level::DEBUG, skip_all, fields(name = config.name), err)]
    async fn create_stream_if_not_present(&self, config: &Config) -> Result<(), CreateStreamError> {
        tracing::debug!("creating stream {}", config.name);
        match self.jetstream.create_stream(config).await {
            Ok(_) => {
                tracing::debug!("created stream {}", config.name);
                Ok(())
            }
            Err(err) => {
                if matches!(err.kind(), CreateStreamErrorKind::JetStream(js_err)
                    if js_err.kind() == ErrorCode::STREAM_NAME_EXIST)
                {
                    tracing::debug!("stream {} already exists", config.name);
                    Ok(())
                } else {
                    tracing::warn!("failed to create stream {}", config.name);
                    record_nats_error_total();
                    Err(err)
                }
            }
        }
    }

    #[tracing::instrument(level = Level::DEBUG, skip(self, payload), err)]
    async fn publish_to_stream(
        &self,
        request_id: RequestId,
        version: Uuid,
        payload: Bytes,
    ) -> Result<(), PublishError> {
        let stream_name = self.request_stream_name(version);
        let subject = self.request_subject(request_id, version);
        tracing::debug!("sending request to subject {}", subject);

        match self
            .jetstream
            .publish_with_headers(subject, otel_context(), payload)
            .await
        {
            Ok(ack) => match ack.await {
                Ok(_) => {
                    record_nats_jetstream_publish(stream_name, "Ok".to_string());
                    Ok(())
                }
                Err(e) => {
                    record_nats_error_total();
                    record_nats_jetstream_publish(stream_name, e.kind().to_string());
                    Err(e)
                }
            },
            Err(e) => {
                record_nats_error_total();
                record_nats_jetstream_publish(stream_name, e.kind().to_string());
                Err(e)
            }
        }
    }

    /// rq.${region}.${function_version}.${request_id}
    fn request_subject(&self, request_id: RequestId, version: Uuid) -> String {
        format!(
            "rq.{}.{}.{}",
            self.nats_properties.region, version, request_id
        )
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    pub async fn record_polling_request(
        &self,
        region: Option<&str>,
        request_id: RequestId,
        function_id: Uuid,
        function_version_id: Uuid,
        request_time: SystemTime,
    ) -> anyhow::Result<()> {
        let region = region.unwrap_or(&self.nats_properties.region);
        let pub_ack = self
            .publish_to_request_mapping(
                region,
                request_id,
                function_id,
                function_version_id,
                request_time,
            )
            .await?;
        match pub_ack.await {
            Err(err) if err.kind() == PublishErrorKind::StreamNotFound => {
                // create stream and try again
                self.create_request_tracking_stream().await?;
                self.publish_to_request_mapping(
                    region,
                    request_id,
                    function_id,
                    function_version_id,
                    request_time,
                )
                .await?
                .await
                .inspect_err(|_err| {
                    // track the error if it fails to wait for the actual publish acknowledgment from NATS.
                    // use inspect_err here as cargo clippy suggested
                    record_nats_error_total();
                })?;
                Ok(())
            }
            Err(err) => {
                record_nats_error_total();
                Err(err.into())
            }
            Ok(_) => Ok(()),
        }
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    async fn publish_to_request_mapping(
        &self,
        region: &str,
        request_id: RequestId,
        function_id: Uuid,
        function_version_id: Uuid,
        request_time: SystemTime,
    ) -> Result<PublishAckFuture, PublishError> {
        let payload = WorkerResultTracking {
            function_id: function_id.to_string(),
            function_version_id: function_version_id.to_string(),
            request_time: Some(request_time.into()),
        }
        .encode_to_vec()
        .into();
        let subject = format!("requestToFunctionVersion.{}.{}", region, request_id);
        self.jetstream.publish(subject, payload).await
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    pub async fn get_request_mapping(
        &self,
        request_id: RequestId,
    ) -> anyhow::Result<Option<ExistingInvocationDetails>> {
        let own_region_mapping = self
            .get_request_mapping_by_region(request_id, &self.nats_properties.region)
            .await?;
        if let Some(worker_result_tracking) = own_region_mapping {
            return Ok(Some(ExistingInvocationDetails {
                worker_result_tracking,
                region: self.nats_properties.region.clone(),
            }));
        }
        tracing::trace!("did not find request in current region");
        let other_regions_lookup =
            futures::stream::iter(self.nats_properties.other_regions.clone().into_iter())
                .flat_map_unordered(None, move |region| {
                    self.get_request_mapping_by_region(request_id, region.clone())
                        .map_ok(|worker_result_tracking| {
                            worker_result_tracking.map(|worker_result_tracking| {
                                ExistingInvocationDetails {
                                    worker_result_tracking,
                                    region,
                                }
                            })
                        })
                        .boxed()
                        .into_stream()
                })
                .filter_map(|tracking_result| async move { tracking_result.ok().flatten() });
        let other_region_mapping = pin!(other_regions_lookup).next().await;
        Ok(other_region_mapping)
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    async fn get_request_mapping_by_region(
        &self,
        request_id: RequestId,
        region: impl AsRef<str> + Debug,
    ) -> anyhow::Result<Option<WorkerResultTracking>> {
        let region = region.as_ref();
        let subject = format!("requestToFunctionVersion.{}.{}", region, request_id);
        // TODO handle creation if missing with self.create_request_tracking_stream()
        let stream = self
            .jetstream
            .get_stream_no_info(format!("requestToFunctionVersion_{}", region))
            .await
            .inspect_err(|_err| {
                record_nats_error_total();
            })?;
        let message = match stream.direct_get_last_for_subject(subject).await {
            Ok(message) => message,
            Err(err) => {
                return if err.kind() == DirectGetErrorKind::NotFound {
                    Ok(None)
                } else {
                    record_nats_error_total();
                    Err(err.into())
                }
            }
        };
        Ok(Some(WorkerResultTracking::decode(message.payload.clone())?))
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    pub async fn purge_record_of_polling_request(
        &self,
        region: &str,
        request_id: RequestId,
    ) -> anyhow::Result<()> {
        let subject = format!("requestToFunctionVersion.{}.{}", region, request_id);
        let stream = Self::request_tracking_stream_name(region);
        let stream = self
            .jetstream
            .get_stream_no_info(stream)
            .await
            .inspect_err(|_err| {
                record_nats_error_total();
            })?;
        stream.purge().filter(subject).await.inspect_err(|_err| {
            record_nats_error_total();
        })?;
        Ok(())
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    async fn create_request_tracking_stream(&self) -> anyhow::Result<()> {
        let config = Config {
            name: Self::request_tracking_stream_name(&self.nats_properties.region),
            subjects: vec![format!(
                "requestToFunctionVersion.{}.>",
                self.nats_properties.region
            )],
            max_messages: self.nats_properties.max_messages,
            storage: StorageType::Memory,
            retention: RetentionPolicy::Limits,
            placement: Some(self.own_region_placement()),
            discard: DiscardPolicy::New,
            allow_direct: true,
            duplicate_window: Duration::from_secs(5),
            num_replicas: self.nats_properties.replicas,
            max_age: Duration::from_secs(60 * 60),
            max_messages_per_subject: 1,
            ..Default::default()
        };
        self.create_stream_if_not_present(&config).await?;
        Ok(())
    }

    fn request_tracking_stream_name(region: &str) -> String {
        format!("requestToFunctionVersion_{}", region)
    }
}

fn otel_context() -> HeaderMap {
    struct NatsHeaderMapInjector<'a>(&'a mut HeaderMap);
    impl Injector for NatsHeaderMapInjector<'_> {
        fn set(&mut self, key: &str, value: String) {
            self.0.append(key, value)
        }
    }
    let mut carrier = HeaderMap::new();
    opentelemetry::global::get_text_map_propagator(|propagator| {
        use tracing_opentelemetry::OpenTelemetrySpanExt;
        let context = Span::current().context();
        propagator.inject_context(&context, &mut NatsHeaderMapInjector(&mut carrier))
    });
    carrier
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::secrets::secret_config::{
        FixedBearerSecrets, NatsSecrets, Oauth2Secrets, Secrets, TracingSecrets,
    };
    use axum::{extract::Query, response::Json, routing::post, Router};
    use std::collections::HashMap;
    use std::net::SocketAddr;
    use std::sync::Arc;
    use tempfile::NamedTempFile;
    use tokio::io::AsyncWriteExt;
    use tokio::net::TcpListener;

    #[derive(Serialize)]
    struct TokenResponse {
        access_token: String,
        token_type: String,
        expires_in: u64,
        scope: String,
    }

    async fn mock_oauth_token_handler(
        Query(params): Query<HashMap<String, String>>,
    ) -> Json<TokenResponse> {
        let empty_string = String::new();
        let _grant_type = params.get("grant_type").unwrap_or(&empty_string);
        let _client_id = params.get("client_id").unwrap_or(&empty_string);
        let _client_secret = params.get("client_secret").unwrap_or(&empty_string);
        let scope = params.get("scope").unwrap_or(&empty_string);

        Json(TokenResponse {
            access_token: "mock_access_token_12345".to_string(),
            token_type: "Bearer".to_string(),
            expires_in: 3600,
            scope: scope.clone(),
        })
    }

    async fn start_mock_oauth_server() -> (SocketAddr, tokio::task::JoinHandle<()>) {
        let app = Router::new().route("/token", post(mock_oauth_token_handler));

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server_handle = tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        // Wait for server to be ready by polling the /token endpoint
        let client = reqwest::Client::new();
        let mut retries = 0;
        loop {
            let result = client
                .post(format!("http://{}/token", addr))
                .form(&[
                    ("grant_type", "client_credentials"),
                    ("client_id", "test"),
                    ("client_secret", "test"),
                    ("scope", "test"),
                ])
                .send()
                .await;

            if result.is_ok() {
                break;
            }

            retries += 1;
            if retries > 50 {
                panic!("OAuth server failed to start after 50 retries");
            }

            tokio::time::sleep(Duration::from_millis(10)).await;
        }

        (addr, server_handle)
    }

    struct TestSecretsProvider {
        _temp_file: NamedTempFile,
        secrets_provider: Arc<SecretFileWatcher>,
    }

    impl TestSecretsProvider {
        async fn new(nkey_seed: Option<String>) -> anyhow::Result<Self> {
            let secrets = Secrets {
                oauth2: Oauth2Secrets {
                    client_id: "test_client".to_string(),
                    client_secret: "test_secret".to_string(),
                },
                nats: NatsSecrets {
                    nkey_pub: None,
                    nkey_seed,
                },
                tracing: TracingSecrets {
                    access_key: "test_access_key".to_string(),
                },
                fixed_bearer_secrets: FixedBearerSecrets {
                    nvcf_api_token: None,
                    rate_limit_token: None,
                },
            };

            let temp_file = NamedTempFile::new()?;
            let temp_path = temp_file.path().to_path_buf();

            let mut file = tokio::fs::File::create(&temp_path).await?;
            file.write_all(serde_json::to_vec(&secrets)?.as_slice())
                .await?;
            file.flush().await?;

            let secrets_provider =
                Arc::new(SecretFileWatcher::new(&temp_path, Duration::from_secs(5)).await?);

            Ok(TestSecretsProvider {
                _temp_file: temp_file,
                secrets_provider,
            })
        }

        fn get_provider(&self) -> Arc<SecretFileWatcher> {
            self.secrets_provider.clone()
        }
    }

    #[tokio::test]
    async fn test_create_auth_callback_with_no_secrets_provider() {
        let grpc_config = crate::settings::GrpcClientConfig::default();
        let callback = NatsService::create_auth_callback(
            None,
            "http://localhost:8080/token",
            "test-plugin",
            &grpc_config,
        )
        .expect("Should create callback");

        let auth_future = callback(vec![]);
        let auth = auth_future.await.expect("Should get auth");

        assert!(auth.nkey.is_none());
        assert!(auth.token.is_none());
    }

    #[tokio::test]
    async fn test_create_auth_callback_with_nkey_seed() {
        let key_pair = nkeys::KeyPair::new_user();
        let nkey_seed = key_pair.seed().expect("must generate a seed");
        let test_provider = TestSecretsProvider::new(Some(nkey_seed.clone()))
            .await
            .expect("Should create secrets provider");

        let callback = NatsService::create_auth_callback(
            Some(test_provider.get_provider()),
            "http://localhost:8080/token",
            "test-plugin",
            &crate::settings::GrpcClientConfig::default(),
        )
        .expect("Should create callback");

        let auth_future = callback(vec![]);
        let auth = auth_future.await.expect("Should get auth");

        assert_eq!(auth.nkey, Some(key_pair.public_key()));
        assert!(auth.token.is_none());
    }

    #[tokio::test]
    async fn test_create_auth_callback_with_oauth_token() {
        let (addr, _server_handle) = start_mock_oauth_server().await;
        let oauth_endpoint = format!("http://{}", addr);

        let test_provider = TestSecretsProvider::new(None)
            .await
            .expect("Should create secrets provider");

        let callback = NatsService::create_auth_callback(
            Some(test_provider.get_provider()),
            &oauth_endpoint,
            "test-plugin",
            &crate::settings::GrpcClientConfig::default(),
        )
        .expect("Should create callback");

        let auth_future = callback(vec![]);
        let auth = auth_future.await.expect("Should get auth");

        assert!(auth.nkey.is_none());
        assert!(auth.token.is_some());

        // Verify the token is our serialized OAuth token
        let token = auth.token.unwrap();
        assert!(!token.is_empty());

        // Decode and verify it contains our mock token
        use base64::engine::general_purpose::URL_SAFE_NO_PAD;
        use base64::Engine;
        let decoded = URL_SAFE_NO_PAD.decode(token).expect("Should decode");
        let json: serde_json::Value = serde_json::from_slice(&decoded).expect("Should parse JSON");

        assert_eq!(json["account"], "Worker");
        assert_eq!(json["pluginName"], "test-plugin");
        assert_eq!(json["payload"], "mock_access_token_12345");
    }

    #[tokio::test]
    async fn test_create_auth_callback_invalid_endpoint() {
        let test_provider = TestSecretsProvider::new(None)
            .await
            .expect("Should create secrets provider");

        let result = NatsService::create_auth_callback(
            Some(test_provider.get_provider()),
            "invalid-url",
            "test-plugin",
            &crate::settings::GrpcClientConfig::default(),
        );

        assert!(result.is_err());
    }

    #[test]
    fn test_serialize_oauth_token() {
        let token = "test_token_123".to_string();
        let serialized = NatsService::serialize_oauth_token(token, "test-plugin");

        assert!(!serialized.is_empty());

        // Decode and verify the structure
        use base64::engine::general_purpose::URL_SAFE_NO_PAD;
        use base64::Engine;
        let decoded = URL_SAFE_NO_PAD.decode(serialized).expect("Should decode");
        let json: serde_json::Value = serde_json::from_slice(&decoded).expect("Should parse JSON");

        assert_eq!(json["account"], "Worker");
        assert_eq!(json["pluginName"], "test-plugin");
        assert_eq!(json["payload"], "test_token_123");
    }
}
