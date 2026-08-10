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

#![allow(dead_code)]

use anyhow::anyhow;
use async_nats::jetstream::consumer::PullConsumer;
use async_nats::jetstream::Context;
use async_nats::{Client, ConnectOptions};
use async_trait::async_trait;
use axum::body::{Body, HttpBody};
use axum::http::header::CONTENT_TYPE;
use axum::Router;
use bytes::Bytes;
use futures::{try_join, AsyncBufReadExt, Stream, StreamExt, TryStreamExt};
use http::header::{AUTHORIZATION, CONTENT_LENGTH, LOCATION};
use http::StatusCode;
use http_body_util::BodyExt;
use httparse::Status;
use nvcf_invocation_service::worker_streams::new_from_buf_and_body_data_stream;
use nvcf_invocation_service::{
    nats::{NatsProperties, NatsService},
    nvcf_api::nvcf::{
        worker_invoke_function_request::stateless_config::connection_config::Config, StringKv,
        WorkerInvokeFunctionRequest,
    },
    request_id::RequestId,
};
use prost::Message;
use std::collections::HashMap;
use std::time::Duration;
use tokio::{pin, select, sync::mpsc::unbounded_channel};
use tokio_stream::wrappers::ReceiverStream;
use tower::Service;
use uuid::Uuid;

pub struct WorkerProperties {
    pub function_id: Uuid,
    pub function_version_id: Uuid,
    pub instance_id: String,
}

#[async_trait]
pub trait WorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()>;
}

pub enum PublishMode {
    // a router is the fully built NVCF-IS application. we can directly call it without doing a real network request.
    Attach(Router<()>),
    AttachMultiRegion(HashMap<String, Router<()>>),
}

pub struct Worker {
    pub client: Client,
    pub jetstream: Context,
    pub nats_properties: NatsProperties,
    pub worker_properties: WorkerProperties,
    work_handler: Box<dyn WorkHandler + Send + Sync>,
    publish_mode: PublishMode,
}

async fn test_provision_streams_on_worker_connect(
    nats: &NatsService,
    version: Uuid,
) -> anyhow::Result<()> {
    nats.create_request_stream(version).await?;
    Ok(())
}

impl Worker {
    pub async fn new(
        nats_properties: NatsProperties,
        worker_properties: WorkerProperties,
        work_handler: Box<dyn WorkHandler + Send + Sync>,
        publish_mode: PublishMode,
    ) -> anyhow::Result<Self> {
        let nats_service = NatsService::new(
            &nats_properties,
            "http://dummy.localhost",
            None,
            &nvcf_invocation_service::settings::GrpcClientConfig::default(),
        )
        .await?;
        test_provision_streams_on_worker_connect(
            &nats_service,
            worker_properties.function_version_id,
        )
        .await?;

        let client = ConnectOptions::new()
            .connect(&nats_properties.nats_address)
            .await?;
        let jetstream = async_nats::jetstream::new(client.clone());
        let worker = Self {
            client,
            jetstream,
            nats_properties,
            worker_properties,
            work_handler,
            publish_mode,
        };
        Ok(worker)
    }

    pub async fn consume_work(&self) -> anyhow::Result<()> {
        let stream_name = format!(
            "rq_{}_{}",
            self.nats_properties.region, self.worker_properties.function_version_id
        );
        let consumer_name = format!("{stream_name}_workers");
        let consumer: PullConsumer = self
            .jetstream
            .get_consumer_from_stream(consumer_name, stream_name)
            .await?;
        let mut message_stream = consumer.messages().await?;
        while let Some(Ok(message)) = message_stream.next().await {
            tracing::debug!("worker got message {:?}", message);
            let request = WorkerInvokeFunctionRequest::decode(message.payload.clone())?;
            Self::ensure_no_auth_header(&request)?;
            self.work_handler.handle(self, &request).await?;
            message
                .ack()
                .await
                .map_err(|e| anyhow!("failed to ack message: {:?}", e))?;
            tracing::debug!("worker handled request {}", request.request_id);
        }
        Ok(())
    }

    pub fn into_background_task(self) -> DroppableBackgroundWorker {
        let handle = tokio::spawn(async move {
            self.consume_work()
                .await
                .expect("worker should not fail to consume work")
        });
        tracing::info!("mock worker started");
        DroppableBackgroundWorker { handle }
    }

    fn ensure_no_auth_header(request: &WorkerInvokeFunctionRequest) -> anyhow::Result<()> {
        for header in &request.request_headers {
            if header.key.to_lowercase() == AUTHORIZATION.as_str() {
                return Err(anyhow::anyhow!(
                    "it is illegal to pass through the authorization header"
                ));
            }
        }
        Ok(())
    }

    async fn listen_for_polling_requests(
        &self,
        request_id: RequestId,
    ) -> anyhow::Result<impl Stream<Item = anyhow::Result<WorkerInvokeFunctionRequest>>> {
        let subscriber = self
            .client
            .subscribe(format!("rq_polling.{request_id}"))
            .await?;
        let client = self.client.clone();
        Ok(subscriber.then(move |message| {
            let client = client.clone();
            async move {
                tracing::debug!("worker got polling message {:?}", message);
                let request = WorkerInvokeFunctionRequest::decode(message.payload.clone())?;
                Self::ensure_no_auth_header(&request)?;
                client
                    .publish(
                        message.reply.ok_or_else(|| anyhow!("no reply subject"))?,
                        Bytes::default(),
                    )
                    .await?;
                Ok(request)
            }
        }))
    }

    async fn publish_response(
        &self,
        request: &WorkerInvokeFunctionRequest,
        response: http::Response<Body>,
    ) -> anyhow::Result<()> {
        let Config::Http1Config(attach_config) = request
            .stateless_config
            .as_ref()
            .and_then(|sc| sc.connection_configs.first())
            .and_then(|cc| cc.config.as_ref())
            .unwrap();
        let header = Self::format_http_response_header(&response);
        let mut router = match &self.publish_mode {
            PublishMode::Attach(router) => router.clone(),
            PublishMode::AttachMultiRegion(routers) => {
                let router = routers.get(&attach_config.target_uri).unwrap();
                router.clone()
            }
        };
        let request = http::Request::builder()
            .method(http::Method::POST)
            .uri("/v2/nvcf/worker/request-attach")
            .header(
                AUTHORIZATION,
                format!("Bearer {}", attach_config.response_authorization_token),
            )
            .header(CONTENT_TYPE, "application/octet-stream+h1")
            .body(new_from_buf_and_body_data_stream(
                header,
                response.into_body().into_data_stream(),
            )?)?;
        tracing::debug!("publishing response");
        let response = router.call(request).await?;
        if response.status() != StatusCode::OK {
            return Err(anyhow::anyhow!("failed to publish response"));
        }
        tracing::debug!("publish successful");
        Ok(())
    }

    async fn get_request_body(
        &self,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<AttachedRequest> {
        let Config::Http1Config(attach_config) = request
            .stateless_config
            .as_ref()
            .and_then(|sc| sc.connection_configs.first())
            .and_then(|cc| cc.config.as_ref())
            .unwrap();
        if attach_config.request_authorization_token.is_none() {
            return Ok(AttachedRequest::BodyOnly(
                request.request_body.clone().unwrap_or_default().into(),
            ));
        }
        let mut router = match &self.publish_mode {
            PublishMode::Attach(router) => router.clone(),
            PublishMode::AttachMultiRegion(routers) => {
                let router = routers.get(&attach_config.target_uri).unwrap();
                router.clone()
            }
        };
        let get_body_request = http::Request::builder()
            .method(http::Method::GET)
            .uri("/v2/nvcf/worker/request-attach")
            .header(
                AUTHORIZATION,
                format!("Bearer {}", attach_config.request_authorization_token()),
            )
            .header(http::header::ACCEPT, "application/octet-stream")
            .body(Body::empty())?;
        let (get_body_response, body) = router.call(get_body_request).await?.into_parts();
        assert_eq!(get_body_response.status, StatusCode::OK);
        let size_hint_exact = body.size_hint().exact().map(|x| x.to_string());
        let content_length = get_body_response
            .headers
            .get(CONTENT_LENGTH)
            .map(|x| x.to_str().unwrap().to_string());
        assert_eq!(size_hint_exact, content_length);
        match get_body_response.headers.get(CONTENT_TYPE) {
            Some(content_type)
                if content_type == http::HeaderValue::from_static("application/octet-stream") =>
            {
                Ok(AttachedRequest::BodyOnly(
                    new_from_buf_and_body_data_stream(
                        request.request_body.clone().unwrap_or_default(),
                        body.into_data_stream(),
                    )?,
                ))
            }
            Some(content_type)
                if content_type
                    == http::HeaderValue::from_static("application/octet-stream+h1") =>
            {
                let mut body_stream = body.into_data_stream();
                // parse and strip headers first
                let mut buffer = Vec::new();
                let mut request_builder = http::Request::builder();
                while let Some(chunk) = body_stream.try_next().await? {
                    buffer.extend_from_slice(&chunk);
                    let mut headers = [httparse::EMPTY_HEADER; 100];
                    let mut request = httparse::Request::new(&mut headers);
                    if let Status::Complete(headers_len) = request.parse(&buffer)? {
                        request_builder = request_builder
                            .method(request.method.unwrap_or_default())
                            .uri(request.path.unwrap_or_default());
                        for header in request.headers {
                            request_builder = request_builder.header(
                                header.name.to_string(),
                                String::from_utf8(header.value.to_vec())?,
                            );
                        }
                        buffer = buffer.split_off(headers_len);
                        break;
                    }
                }
                let body_stream =
                    new_from_buf_and_body_data_stream(Bytes::from(buffer), body_stream)?;
                let request = request_builder.body(body_stream)?;
                Ok(AttachedRequest::FullRequest(Box::new(request)))
            }
            _ => Err(anyhow::anyhow!("unexpected content type")),
        }
    }

    /// Formats an HTTP response header into a byte buffer.
    /// This creates the status line and headers portion of an HTTP response.
    fn format_http_response_header<T>(response: &http::Response<T>) -> Bytes {
        let mut buffer = Vec::new();

        // Write status line
        let status = response.status();
        let status_line = format!(
            "HTTP/1.1 {} {}\r\n",
            status.as_u16(),
            status.canonical_reason().unwrap_or("")
        );
        buffer.extend_from_slice(status_line.as_bytes());

        // Write headers
        for (name, value) in response.headers() {
            let header_line = format!("{}: {}\r\n", name.as_str(), value.to_str().unwrap_or(""));
            buffer.extend_from_slice(header_line.as_bytes());
        }

        // End of headers
        buffer.extend_from_slice(b"\r\n");

        Bytes::from(buffer)
    }
}

enum AttachedRequest {
    BodyOnly(Body),
    FullRequest(Box<http::Request<Body>>),
}

pub struct DroppableBackgroundWorker {
    handle: tokio::task::JoinHandle<()>,
}

impl Drop for DroppableBackgroundWorker {
    fn drop(&mut self) {
        self.handle.abort()
    }
}

pub struct DefaultWorkHandler {}

#[async_trait]
impl WorkHandler for DefaultWorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let body = match worker.get_request_body(request).await? {
            AttachedRequest::BodyOnly(body) => body,
            AttachedRequest::FullRequest(request) => request.into_body(),
        };
        assert!(body.size_hint().exact().is_some());
        let body_len = body.size_hint().exact().unwrap();
        let body = body.collect().await?.to_bytes();
        assert_eq!(body.len(), body_len as usize);
        let response = http::Response::builder()
            .status(StatusCode::OK)
            .header(CONTENT_TYPE, "text/plain")
            .body(Bytes::from_static(b"a response").into())?;
        worker.publish_response(request, response).await?;
        Ok(())
    }
}

pub struct CustomHeadersWorkHandler {
    headers: http::HeaderMap,
}

#[async_trait]
impl WorkHandler for CustomHeadersWorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let mut builder = http::Response::builder()
            .status(StatusCode::OK)
            .header(CONTENT_TYPE, "text/event-stream");
        for header in &self.headers {
            builder = builder.header(header.0.clone(), header.1.clone());
        }
        let response = builder.body(Bytes::from_static(b"a response").into())?;
        tracing::debug!("publishing response: {:?}", response);
        worker.publish_response(request, response).await?;
        Ok(())
    }
}

impl CustomHeadersWorkHandler {
    pub fn new(headers: http::HeaderMap) -> Self {
        Self { headers }
    }
}

pub struct LargeResponseWorkHandler {}

#[async_trait]
impl WorkHandler for LargeResponseWorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let upload_response = reqwest::Client::new()
            .put(&request.large_response_url)
            .header(CONTENT_TYPE, "application/zip")
            .body("fake-zip")
            .send()
            .await?;
        if !upload_response.status().is_success() {
            return Err(anyhow!("bad upload status"));
        }
        let response = http::Response::builder()
            .status(StatusCode::FOUND)
            .header(LOCATION, request.large_response_url.clone())
            .body(Bytes::from_static(b"a response").into())?;
        worker.publish_response(request, response).await?;
        Ok(())
    }
}

pub struct SseWorkHandler {}

#[async_trait]
impl WorkHandler for SseWorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let (tx, rx) = tokio::sync::mpsc::channel::<Result<Bytes, std::io::Error>>(1);
        let response = http::Response::builder()
            .status(StatusCode::OK)
            .header(CONTENT_TYPE, "text/event-stream")
            .body(Body::from_stream(ReceiverStream::from(rx)))?;
        try_join! {
            async move {
                for _ in 0..100 {
                    tx.send(Ok(Bytes::from_static(
                        b"event:an event\ndata:some data\n\n",
                    )))
                    .await?;
                }
                Ok(())
            },
            worker.publish_response(request, response)
        }?;

        Ok(())
    }
}

pub struct SleepWorkHandler<T> {
    pub sleep_time: Duration,
    pub delegate: T,
}

#[async_trait]
impl<T> WorkHandler for SleepWorkHandler<T>
where
    T: WorkHandler + Send + Sync,
{
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        tokio::time::sleep(self.sleep_time).await;
        self.delegate.handle(worker, request).await
    }
}

pub struct EchoWorkHandler {}

#[async_trait]
impl WorkHandler for EchoWorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let mut builder = http::Response::builder().status(StatusCode::OK);
        for header in &request.request_headers {
            builder = builder.header(header.key.clone(), header.value.clone());
        }
        let response = builder.body(request.request_body.clone().unwrap_or_default().into())?;
        worker.publish_response(request, response).await?;
        Ok(())
    }
}

type WorkFn =
    Box<dyn Fn(&Worker, &WorkerInvokeFunctionRequest) -> anyhow::Result<()> + Send + Sync>;
pub struct FnWorkHandler(pub WorkFn);

#[async_trait]
impl WorkHandler for FnWorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        self.0(worker, request)
    }
}

pub struct PollAwareHandler {
    pub sleep_time: Duration,
    pub enable_echo_request: bool,
}

#[async_trait]
impl WorkHandler for PollAwareHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let (tx, mut rx) = unbounded_channel();
        let poll_time = Self::poll_time(request)?;
        tx.send((poll_time, request.clone()))?;
        let listen_for_polling_requests = async move {
            let polling_requests = worker
                .listen_for_polling_requests(request.request_id.parse::<Uuid>()?.into())
                .await?;
            pin!(polling_requests);
            while let Some(polling_request) = polling_requests.next().await {
                tracing::debug!("got polling request");
                let polling_request = polling_request?;
                let poll_time = Self::poll_time(&polling_request)?;
                tx.send((poll_time, polling_request))?;
            }
            Ok::<_, anyhow::Error>(())
        };

        // capture the request body and headers for potential echo response
        let response_body = if self.enable_echo_request {
            request.request_body.clone().unwrap_or_default()
        } else {
            "a response".into()
        };
        let response_headers = if self.enable_echo_request {
            request.request_headers.clone()
        } else {
            vec![StringKv {
                key: CONTENT_TYPE.to_string(),
                value: "text/plain".into(),
            }]
        };

        let send_responses = async move {
            let finished = tokio::time::sleep(self.sleep_time);
            pin!(finished);
            while let Some((poll_time, polling_request)) = rx.recv().await {
                select! {
                    biased;
                    () = &mut finished => {
                        tracing::debug!("producing finished response");
                        let mut builder = http::Response::builder().status(StatusCode::OK);
                        for header in &response_headers {
                            builder = builder.header(header.key.clone(), header.value.clone());
                        }
                        let response = builder.body(response_body.into())?;
                        worker.publish_response(&polling_request, response).await?;
                        return Ok(());
                    }
                    () = tokio::time::sleep((poll_time - Duration::from_secs(1)).max(Duration::from_secs(0))) => {
                        tracing::debug!("producing polling response");
                        let response = http::Response::builder()
                            .status(StatusCode::ACCEPTED)
                            .body(Body::empty())?;
                        worker.publish_response(&polling_request, response).await?;
                    }
                }
            }
            Ok::<_, anyhow::Error>(())
        };

        select! {
            // listen_for_polling_requests is only used to drive the tx channel for send_responses.
            // we don't need any result out of it.
            _ = listen_for_polling_requests => {}
            res = send_responses => {
                res?;
            }
        }
        Ok(())
    }
}

impl PollAwareHandler {
    fn poll_time(request: &WorkerInvokeFunctionRequest) -> anyhow::Result<Duration> {
        let poll_seconds = request
            .request_headers
            .iter()
            .find(|kv| kv.key.eq_ignore_ascii_case("nvcf-poll-seconds"))
            .map(|kv| kv.value.clone())
            .unwrap_or("60".to_string());
        let poll_seconds = poll_seconds.parse::<u64>()?;
        let poll_time = Duration::from_secs(poll_seconds);
        Ok(poll_time)
    }
}

pub struct ReturnRequestHandler {}

#[derive(serde::Serialize, serde::Deserialize)]
pub struct JsonHttpRequest {
    pub method: String,
    pub path: String,
    pub body: Option<String>,
}

#[async_trait]
impl WorkHandler for ReturnRequestHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let body_stream = match worker.get_request_body(request).await? {
            AttachedRequest::BodyOnly(body) => body,
            AttachedRequest::FullRequest(request) => request.into_body(),
        };
        let body = body_stream.collect().await?.to_bytes();
        let body = if body.is_empty() {
            None
        } else {
            Some(String::from_utf8(body.to_vec())?)
        };
        let json_http_request = JsonHttpRequest {
            method: request.request_method.clone(),
            path: request.request_path.clone(),
            body,
        };
        let body = serde_json::to_vec(&json_http_request)?;
        let response = http::Response::builder()
            .status(StatusCode::OK)
            .header(CONTENT_TYPE, "application/json")
            .body(body.into())?;
        worker.publish_response(request, response).await?;
        Ok(())
    }
}

pub struct FailedWorkHandler {}

#[async_trait]
impl WorkHandler for FailedWorkHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let response = http::Response::builder()
            .status(StatusCode::INTERNAL_SERVER_ERROR)
            .header(CONTENT_TYPE, "text/plain")
            .body(Bytes::from_static(b"a response").into())?;
        worker.publish_response(request, response).await?;
        Ok(())
    }
}

pub struct PingPongAttachHandler {}

#[async_trait]
impl WorkHandler for PingPongAttachHandler {
    async fn handle(
        &self,
        worker: &Worker,
        request: &WorkerInvokeFunctionRequest,
    ) -> anyhow::Result<()> {
        let body = match worker.get_request_body(request).await? {
            AttachedRequest::BodyOnly(body) => body,
            AttachedRequest::FullRequest(request) => request.into_body(),
        };

        let mut request_lines = body
            .into_data_stream()
            .map(|b| b.map_err(std::io::Error::other))
            .into_async_read()
            .lines();

        let (tx, rx) = tokio::sync::mpsc::channel(1);

        let publish_response_future = worker.publish_response(
            request,
            http::Response::builder()
                .status(StatusCode::OK)
                .body(Body::from_stream(ReceiverStream::from(rx)))?,
        );

        try_join!(publish_response_future, async move {
            tracing::info!("worker sending ping");
            tx.send(Ok::<Bytes, std::io::Error>(Bytes::from_static(b"ping\n")))
                .await?;
            let mut seq = 1;
            while let Some(request_line) = request_lines.try_next().await? {
                if request_line != "pong" {
                    return Err(anyhow::anyhow!("bad request"));
                }
                tracing::info!("worker got pong");
                if seq > 10 {
                    break;
                }
                tracing::info!("worker sending ping");
                tx.send(Ok::<Bytes, std::io::Error>(Bytes::from_static(b"ping\n")))
                    .await?;
                seq += 1;
            }
            Ok::<(), anyhow::Error>(())
        })?;
        Ok(())
    }
}
