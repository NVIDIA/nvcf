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

use crate::metrics;
use crate::nats::NatsService;
use crate::nvcf_api::{
    nvcf::{
        worker_invoke_function_request::{
            stateless_config::{
                connection_config::{Config, Http1ProtocolConfig},
                ConnectionConfig,
            },
            StatelessConfig,
        },
        InputAssetReference, StringKv, WorkerInvokeFunctionRequest,
    },
    AllowedFunctionInvocations, AllowedFunctionVersion, NVCFService,
};
use crate::rate_limit::{LimitResult, RateLimitService};
use crate::request_id::RequestId;
use crate::routes::{
    app_error::AppError, body_stream, http_headers::remove_hop_by_hop_headers,
    input_asset_header::InputAssetHeader, nvcf_status_header::NVCFStatusHeader, tlb::FunctionId,
};
use crate::s3::{Error, S3Service};
use crate::settings::AppConfig;
use crate::worker_streams::{DroppableBody, RequestDataStream, WorkerStreamService};
use anyhow::{anyhow, Context};
use axum::body::Body;
use axum::extract::{Extension, Path, Request, State};
use axum::http::header::ACCEPT;
use axum::http::{HeaderMap, HeaderName, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum_extra::headers::authorization::Bearer;
use axum_extra::headers::Authorization;
use axum_extra::TypedHeader;
use bytes::Bytes;
use futures::future::try_join_all;
use http::Extensions;
use problem_details::ProblemDetails;
use rand::{self, seq::IndexedRandom};
use serde_with::serde_derive::Deserialize;
use std::str::FromStr;
use std::sync::Arc;
use std::time::{Duration, SystemTime};
use tokio::select;
use tokio::task;
use tokio::time::timeout;
use tracing::{Instrument, Level, Span};
use uom::si::information;
use uom::si::usize::Information;
use uuid::Uuid;

#[derive(Clone, Deserialize)]
pub struct FunctionRouting {
    pub function_id: Uuid,
    pub function_version_id: Option<Uuid>,
}

// Define the guard that holds enough info to cancel the request on drop unless
// we call `mark_completed()`.
struct InflightRequestGuard {
    nats_service: Arc<NatsService>,
    request_id: RequestId,
    function_id: Uuid,
    function_version_id: Uuid,
    nca_id: String,
    completed: bool,
    // True once a status was returned (server-side error). Lets Drop tell a
    // server error from a client disconnect (future dropped, no status).
    status_returned: bool,
}

impl InflightRequestGuard {
    fn new(
        nats_service: Arc<NatsService>,
        request_id: RequestId,
        function_id: Uuid,
        function_version_id: Uuid,
        nca_id: String,
    ) -> Self {
        Self {
            nats_service,
            request_id,
            function_id,
            function_version_id,
            nca_id,
            completed: false,
            status_returned: false,
        }
    }

    fn mark_completed(&mut self) {
        tracing::trace!("InflightRequestGuard mark_completed called");
        self.completed = true;
    }

    // A status was returned (server-side error); keep Drop from counting it as 499.
    fn mark_status_returned(&mut self) {
        self.status_returned = true;
    }
}

impl Drop for InflightRequestGuard {
    fn drop(&mut self) {
        // If it hasn't completed normally, we assume it was dropped
        // due to the client going away or some other early-exit scenario.
        tracing::trace!(
            "InflightRequestGuard drop called, completed: {}",
            self.completed
        );
        if !self.completed {
            // No status produced => client early disconnect. Record it so it's
            // countable (server errors are already counted via
            // `AppError::into_response`).
            if !self.status_returned {
                tracing::info!(
                    request_id = %self.request_id,
                    function_id = %self.function_id,
                    function_version_id = %self.function_version_id,
                    nca_id = %self.nca_id,
                    "client disconnected before a response was produced; recording client early disconnect"
                );
                metrics::record_nvcf_application_error(
                    metrics::CLIENT_EARLY_DISCONNECT_STATUS.to_string(),
                    Some(self.function_id.to_string()),
                );
            }
            let nats_svc = self.nats_service.clone();
            let req_id = self.request_id;
            let version_id = self.function_version_id;
            // Spawn an async cancellation task so we don't block in Drop.
            task::spawn(
                async move {
                    tracing::trace!("InflightRequestGuard cancelling request.");
                    // tear down workers already running this request
                    if let Err(e) = nats_svc.publish_cancel(req_id, version_id).await {
                        tracing::warn!("Failed to publish cancel for {:?}: {:?}", req_id, e);
                    }
                    // drop the message if it hasn't been picked up yet
                    match timeout(
                        Duration::from_secs(10),
                        nats_svc.cancel_request(req_id, version_id),
                    )
                    .await
                    {
                        Ok(Err(e)) => {
                            tracing::warn!("Failed to cancel request {:?}: {:?}", req_id, e);
                        }
                        Err(e) => {
                            tracing::warn!(
                                "Timeout while cancelling request {:?}: {:?}",
                                req_id,
                                e
                            );
                        }
                        _ => {}
                    }
                }
                .in_current_span(),
            );
        }
    }
}

#[allow(clippy::too_many_arguments)]
pub async fn pexec(
    Path(function_routing): Path<FunctionRouting>,
    State(nvcf_service): State<Arc<NVCFService>>,
    State(nats_service): State<Arc<NatsService>>,
    State(s3_service): State<Arc<S3Service>>,
    State(rate_limit_service): State<Arc<RateLimitService>>,
    State(worker_stream_service): State<Arc<WorkerStreamService>>,
    State(app_config): State<Arc<AppConfig>>,
    Extension(Authorization(bearer)): Extension<Authorization<Bearer>>,
    TypedHeader(InputAssetHeader(input_assets)): TypedHeader<InputAssetHeader>,
    req: Request,
) -> Result<Response, AppError> {
    let function_id = function_routing.function_id;
    let function_version_id = function_routing.function_version_id;
    let span = Span::current();
    span.record("function_id", function_id.to_string());
    span.record(
        "function_version_id",
        function_version_id
            .map(|id| id.to_string())
            .unwrap_or_default(),
    );
    span.record("operation", "auth_invocation");
    let allowed_functions = match nvcf_service
        .auth_invocation(bearer, function_id, function_version_id, None)
        .await
    {
        Ok(allowed) => allowed,
        Err(err) => {
            span.record("error", true);
            if let Some(nca_id) = err.nca_id() {
                span.record("nca_id", nca_id);
            }
            return Err(AppError::map_nvcf_api_err(err));
        }
    };

    tracing::trace!("allowed_functions: {:#?}", allowed_functions);

    let (mut parts, request_body) = req.into_parts();
    remove_hop_by_hop_headers(&mut parts.headers);
    let stream_full_request = app_config.worker_stream_properties.stream_full_request
        || is_only_stream_full_request_opt_in(&parts.headers);
    let headers = if stream_full_request {
        &mut HeaderMap::new()
    } else {
        &mut parts.headers
    };
    // TLB is always pass through
    let is_tlb = is_tlb(&parts.extensions);
    if is_tlb {
        headers.insert(
            "nvcf-feature-disable-worker-compatibility",
            HeaderValue::from_static("true"),
        );
    };
    let headers =
        to_nvcf_request_headers(headers).context("failed to parse http request headers")?;

    tracing::trace!("headers: {:?}", headers);

    let poll_duration = poll_duration(&parts.headers, is_tlb)
        .context("failed to parse poll duration")
        .map_err(|err| {
            AppError(
                ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
                    .with_detail(err.to_string()),
            )
        })?;

    let (first_body_chunk, remaining_body) =
        maybe_buffered_request_body(&app_config, &parts.headers, request_body).await?;
    let assets = request_assets(&s3_service, &allowed_functions, &input_assets).await?;
    tracing::trace!("assets: {:?}", assets);
    let request_id = RequestId::new();
    tracing::trace!("request_id assigned: {:?}", request_id);
    span.record("request_id", format!("{:?}", request_id));
    let large_response_url = if is_tlb {
        // large response zips are not supported by TLB. for TLB, send the whole response inline.
        "".to_string()
    } else {
        s3_service
            .get_large_response_upload_url(&allowed_functions.authed_client_nca_id, request_id)
            .await?
    };
    tracing::trace!("large_response_url: {:?}", large_response_url);
    let five_mb = Information::new::<information::mebibyte>(5).get::<information::byte>();
    let start_time = SystemTime::now();
    let allowed_function_version = select_version(&allowed_functions)?;
    tracing::trace!("allowed_function_version: {:?}", allowed_function_version);
    let function_version_id = allowed_function_version.function_version_id;
    let nca_id = allowed_functions.authed_client_nca_id;
    metrics::record_invocation_start(
        function_id.to_string(),
        function_version_id.to_string(),
        nca_id.to_string(),
    );
    span.record("nca_id", nca_id.clone());
    let has_rate_limit = allowed_function_version.has_rate_limit;
    let sync_check = allowed_function_version.sync_check;
    tracing::debug!(
        has_rate_limit = has_rate_limit,
        sync_check = sync_check,
        "ratelimit settings"
    );

    if has_rate_limit
        && rate_limit_service
            .check_rate_limit(
                allowed_functions.authed_client_subject.clone(),
                nca_id.clone(),
                function_id,
                function_version_id,
                sync_check,
            )
            .await
            == LimitResult::Limited
    {
        return Err(AppError(ProblemDetails::from_status_code(
            StatusCode::TOO_MANY_REQUESTS,
        )));
    };
    let request_path = if stream_full_request {
        "".to_string()
    } else if is_tlb {
        // checking for transparent load balancer to pick path
        parts
            .uri
            .path_and_query()
            .map(|pq| pq.as_str())
            .unwrap_or_default()
            .to_string()
    } else {
        allowed_function_version.default_path.unwrap_or_default()
    };
    let request_method = if stream_full_request {
        "".into()
    } else {
        parts.method.as_str().into()
    };
    let (response_tx, response_rx) = tokio::sync::oneshot::channel();
    let response_authorization_token =
        worker_stream_service.generate_response_token(request_id, response_tx);
    let request_token = if let Some(remaining_body) = remaining_body {
        let (request_tx, request_rx) = tokio::sync::oneshot::channel();
        let request_token = worker_stream_service.generate_request_token(request_id, request_rx);
        let remaining_body = if stream_full_request {
            RequestDataStream::HttpRequest(Box::new(Request::from_parts(
                parts,
                Body::new(remaining_body),
            )))
        } else {
            RequestDataStream::Raw(Body::new(remaining_body))
        };
        request_tx
            .send(remaining_body)
            .map_err(|_| anyhow!("could not send remaining body stream to worker"))?;
        Some(request_token)
    } else {
        None
    };

    let payload = WorkerInvokeFunctionRequest {
        request_id: request_id.to_string(),
        nca_id: nca_id.clone(),
        subject: allowed_functions.authed_client_subject.clone(),
        large_response_url,
        max_direct_response_size_bytes: five_mb as u32,
        input_asset_reference: assets,
        request_time: Some(start_time.into()),
        stateful_config: None,
        request_body: Some(first_body_chunk),
        direct_response_url: app_config.regional_nvcf_api_grpc_address.clone(),
        request_headers: headers,
        request_method,
        request_path,
        stateless_config: Some(StatelessConfig {
            connection_configs: vec![ConnectionConfig {
                config: Some(Config::Http1Config(Http1ProtocolConfig {
                    proxy_uri: worker_stream_service.proxy_address().map(Into::into),
                    target_uri: worker_stream_service.self_address().into(),
                    request_authorization_token: request_token
                        .as_ref()
                        .map(|token| token.token().into()),
                    response_authorization_token: response_authorization_token.token().into(),
                })),
            }],
        }),
    };

    nats_service
        .request(request_id, function_version_id, payload)
        .await
        .map_err(AppError::map_nats_err)?;
    tracing::trace!("Message sent to NATS");

    // Now we know we have a request in flight, create the guard.
    let mut inflight_guard = InflightRequestGuard::new(
        nats_service.clone(),
        request_id,
        function_id,
        function_version_id,
        nca_id.clone(),
    );
    let response = match handle_streaming_response(
        nats_service.clone(),
        function_id,
        function_version_id,
        nca_id,
        request_id,
        poll_duration,
        start_time,
        None,
        response_rx,
    )
    .await
    {
        Ok(response) => response,
        Err(err) => {
            // Server-side error returns a status; mark it so Drop doesn't count a 499.
            inflight_guard.mark_status_returned();
            return Err(err.into());
        }
    };

    // If we got here, we are done and can mark the guard as completed so it doesn't cancel
    inflight_guard.mark_completed();

    // cancel requests if timed out on initial invocation since there will be nobody to listen for the response if it ever comes
    if response.status() == StatusCode::GATEWAY_TIMEOUT {
        // tear down workers already running this request
        if let Err(err) = nats_service
            .publish_cancel(request_id, function_version_id)
            .await
        {
            tracing::warn!("failed to publish cancel for timed out request {}", err)
        }
        // drop the message if it hasn't been picked up yet
        if let Err(err) = nats_service
            .cancel_request(request_id, function_version_id)
            .await
        {
            tracing::warn!("failed to cancel timed out request {}", err)
        }
    }

    // keep the request body reference alive at most until the response is dropped
    // it may (ideally) be consumed by the worker before the response is dropped
    let response = if let Some(request_token) = request_token {
        response
            .map(|body| DroppableBody::new(body, request_token))
            .into_response()
    } else {
        response
    };
    Ok(response)
}

async fn maybe_buffered_request_body(
    app_config: &AppConfig,
    headers: &HeaderMap,
    request_body: Body,
) -> Result<(Bytes, Option<Body>), AppError> {
    // don't read the body at all if we're streaming the full request
    if app_config.worker_stream_properties.stream_full_request
        || is_only_stream_full_request_opt_in(headers)
    {
        return Ok((Bytes::new(), Some(request_body)));
    }
    body_stream::prepare_body(request_body)
        .await
        .map_err(|err| {
            AppError(
                // we're blaming body reading errors on the client here. every error I've seen
                // relating to reading the body has been due to the client cancelling the request.
                ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
                    .with_detail(format!("failed to read request body: {}", err)),
            )
        })
}

pub(crate) fn is_only_stream_full_request_opt_in(headers: &HeaderMap) -> bool {
    headers
        .get("nvcf-feature-enable-only-stream-full-request")
        .map(|s| s.to_str().unwrap_or_default() == "true")
        .unwrap_or_default()
}

fn is_tlb(extensions: &Extensions) -> bool {
    extensions.get::<FunctionId>().is_some()
}

#[allow(clippy::too_many_arguments)]
#[tracing::instrument(
    level = Level::DEBUG,
    skip(
        nats_service,
        attach_response_rx,
        start_time
    )
)]
pub(crate) async fn handle_streaming_response(
    nats_service: Arc<NatsService>,
    function_id: Uuid,
    function_version_id: Uuid,
    nca_id: String,
    request_id: RequestId,
    poll_duration: Duration,
    start_time: SystemTime,
    region: Option<&str>,
    attach_response_rx: tokio::sync::oneshot::Receiver<http::Response<Body>>,
) -> anyhow::Result<Response> {
    // try to read from the response stream
    // the worker may be trying to send an intermediate poll response exactly at the time
    // user supplied time runs out so give it an extra 2s.
    let mut response = select! {
        biased;
        response = attach_response_rx => {
            response?
        }
        _ = tokio::time::sleep(poll_duration + Duration::from_secs(2)) => {
            gateway_timeout_response(function_id, request_id)
        }
    };

    if response.status() == StatusCode::ACCEPTED {
        nats_service
            .record_polling_request(
                region,
                request_id,
                function_id,
                function_version_id,
                start_time,
            )
            .await?;
    }

    // Add NVCF status as header attribute
    let status_header = NVCFStatusHeader::from(response.status()).as_response_header();
    response
        .headers_mut()
        .try_append(status_header.0, status_header.1)?;
    // Add nvcf-reqid and append it to the CORS access list
    let reqid_header = request_id.as_response_header();
    response
        .headers_mut()
        .try_append(&reqid_header.0, reqid_header.1)?;
    response.headers_mut().try_append(
        http::header::ACCESS_CONTROL_EXPOSE_HEADERS,
        HeaderValue::from_name(reqid_header.0),
    )?;

    let (parts, body) = response.into_parts();
    let recorded_body = DroppableBody::new(
        body,
        FnOnDrop {
            fn_on_drop: Box::new(move || {
                metrics::record_invocation_end(
                    function_id.to_string(),
                    function_version_id.to_string(),
                    nca_id.to_string(),
                    start_time,
                );
            }),
        },
    );
    Ok(Response::from_parts(parts, Body::new(recorded_body)))
}

fn gateway_timeout_response(function_id: Uuid, request_id: RequestId) -> Response {
    tracing::warn!("generating gateway timeout response for request id {request_id}");
    metrics::record_nvcf_application_error(
        StatusCode::GATEWAY_TIMEOUT.as_str().to_string(),
        Some(function_id.to_string()),
    );
    Span::current().record("app_error", "true");
    StatusCode::GATEWAY_TIMEOUT.into_response()
}

#[tracing::instrument(level = Level::TRACE, skip(s3_service))]
async fn request_assets(
    s3_service: &S3Service,
    allowed_functions: &AllowedFunctionInvocations,
    asset_ids: &[Uuid],
) -> Result<Vec<InputAssetReference>, AppError> {
    try_join_all(asset_ids.iter().map(|asset_id| {
        s3_service.get_asset_dto(&allowed_functions.authed_client_nca_id, *asset_id)
    }))
    .await
    .map_err(|err| match err {
        Error::NotFound { .. } => AppError(
            ProblemDetails::from_status_code(StatusCode::NOT_FOUND).with_detail(err.to_string()),
        ),
        Error::Other(err) => err.context("failed to get asset dtos").into(),
    })
}

pub const NVCF_POLL_SECONDS: HeaderName = HeaderName::from_static("nvcf-poll-seconds");

pub fn poll_duration(headers: &HeaderMap, is_tlb: bool) -> anyhow::Result<Duration> {
    const DEFAULT: Duration = Duration::from_secs(60);
    const DEFAULT_TEXT_STREAM: Duration = Duration::from_secs(60 * 20);
    const MAX: Duration = Duration::from_secs(60 * 60);
    const MIN: Duration = Duration::from_secs(1);
    let poll_duration = headers
        .get(NVCF_POLL_SECONDS)
        .and_then(|seconds| seconds.to_str().ok())
        .map(u64::from_str)
        .transpose()
        .context("failed to parse NVCF-POLL-SECONDS")?
        .map(Duration::from_secs)
        .unwrap_or_else(|| {
            // default to 20 minutes if text event stream, else 1 minute
            // TODO DO NOT CARRY THIS FORWARD! only use this logic for the existing exec and pexec endpoints
            // this is for backwards compatibility only
            if !is_tlb
                && headers
                    .get(ACCEPT)
                    .and_then(|accept| accept.to_str().ok())
                    .filter(|accept| *accept == mime::TEXT_EVENT_STREAM.essence_str())
                    .is_some()
            {
                DEFAULT_TEXT_STREAM
            } else {
                DEFAULT
            }
        })
        .max(MIN); // minimum of 1 second
    if poll_duration > MAX {
        // 20 minute max
        return Err(anyhow::anyhow!(
            "NVCF-POLL-SECONDS must be <= {}",
            MAX.as_secs()
        ));
    }
    Ok(poll_duration)
}

pub fn to_nvcf_request_headers(map: &HeaderMap) -> anyhow::Result<Vec<StringKv>> {
    map.iter()
        .map(|(key, value)| {
            let key = key.as_str().to_owned();
            let value = String::from_utf8(value.as_bytes().to_vec())
                .context("failed to convert header value to utf8")?;
            Ok(StringKv { key, value })
        })
        .collect()
}

fn select_version(routing: &AllowedFunctionInvocations) -> anyhow::Result<AllowedFunctionVersion> {
    let version = if routing.function_version_ids.len() == 1 {
        routing.function_version_ids[0].clone()
    } else {
        routing
            .function_version_ids
            .choose(&mut rand::rng())
            .ok_or_else(|| anyhow!("function version id must not be empty"))?
            .clone()
    };
    Ok(version)
}

struct FnOnDrop {
    fn_on_drop: Box<dyn Fn() + Send + 'static>,
}

impl Drop for FnOnDrop {
    fn drop(&mut self) {
        (self.fn_on_drop)();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use futures::StreamExt;
    use metrics_util::debugging::{DebugValue, DebuggingRecorder};
    use metrics_util::MetricKind;
    use std::time::Duration;
    use testcontainers_modules::nats::Nats;
    use testcontainers_modules::testcontainers::core::Mount;
    use testcontainers_modules::testcontainers::{runners::AsyncRunner, ContainerAsync, ImageExt};

    #[test]
    fn gateway_timeout_response_records_function_error_metric() {
        let recorder = DebuggingRecorder::new();
        let snapshotter = recorder.snapshotter();
        let function_id = Uuid::new_v4();

        ::metrics::with_local_recorder(&recorder, || {
            let response = gateway_timeout_response(function_id, RequestId::new());
            assert_eq!(response.status(), StatusCode::GATEWAY_TIMEOUT);
        });

        let metric_value = snapshotter
            .snapshot()
            .into_vec()
            .into_iter()
            .find_map(|(key, _, _, value)| {
                let matches = key.kind() == MetricKind::Counter
                    && key.key().name() == "app.invocation.error"
                    && key
                        .key()
                        .labels()
                        .any(|label| label.key() == "http_status_code" && label.value() == "504")
                    && key.key().labels().any(|label| {
                        label.key() == "function_id" && label.value() == function_id.to_string()
                    });
                matches.then_some(value)
            })
            .expect("gateway timeout response should record app.invocation.error by function_id");

        assert_eq!(metric_value, DebugValue::Counter(1));
    }

    async fn make_nats_service() -> (Arc<NatsService>, async_nats::Client, ContainerAsync<Nats>) {
        let container = Nats::default()
            .with_tag("2.10.25")
            .with_mount(
                Mount::bind_mount(
                    concat!(env!("CARGO_MANIFEST_DIR"), "/tests/mocks/nats-server.conf"),
                    "/nats-server.conf",
                )
                .with_access_mode(
                    testcontainers_modules::testcontainers::core::AccessMode::ReadOnly,
                ),
            )
            .start()
            .await
            .expect("start nats");
        let host = container.get_host().await.unwrap().to_string();
        let port = container.get_host_port_ipv4(4222).await.unwrap();
        let address = format!("nats://{}:{}", host, port);

        let nats_properties = crate::nats::NatsProperties {
            nats_address: address.clone(),
            ..Default::default()
        };
        let svc = NatsService::new(
            &nats_properties,
            "http://dummy.localhost",
            None,
            &crate::settings::GrpcClientConfig::default(),
        )
        .await
        .expect("create NatsService");

        let observer = async_nats::connect(&address).await.unwrap();
        (Arc::new(svc), observer, container)
    }

    /// Drop on an unfinished guard broadcasts cancel with the request id.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    #[ignore = "requires docker; run with cargo test -- --ignored"]
    async fn test_inflight_guard_drop_publishes_cancel() {
        let (svc, observer, _container) = make_nats_service().await;
        let request_id = RequestId::new();
        let fvid = Uuid::new_v4();
        let mut sub = observer
            .subscribe(format!("nvcf.cancel.{}", fvid))
            .await
            .unwrap();
        observer.flush().await.unwrap();

        {
            let _guard = InflightRequestGuard::new(
                svc.clone(),
                request_id,
                Uuid::new_v4(),
                fvid,
                "nca-test".to_string(),
            );
            // dropped at end of scope without mark_completed
        }

        let msg = tokio::time::timeout(Duration::from_secs(5), sub.next())
            .await
            .expect("should receive cancel within timeout")
            .expect("subscription should not close");
        assert_eq!(
            std::str::from_utf8(&msg.payload).unwrap(),
            request_id.to_string()
        );
    }

    /// A completed guard stays silent on Drop.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    #[ignore = "requires docker; run with cargo test -- --ignored"]
    async fn test_inflight_guard_drop_no_publish_when_completed() {
        let (svc, observer, _container) = make_nats_service().await;
        let request_id = RequestId::new();
        let fvid = Uuid::new_v4();
        let mut sub = observer
            .subscribe(format!("nvcf.cancel.{}", fvid))
            .await
            .unwrap();
        observer.flush().await.unwrap();

        {
            let mut guard = InflightRequestGuard::new(
                svc.clone(),
                request_id,
                Uuid::new_v4(),
                fvid,
                "nca-test".to_string(),
            );
            guard.mark_completed();
        }

        let result = tokio::time::timeout(Duration::from_millis(300), sub.next()).await;
        assert!(result.is_err(), "completed guard must not publish a cancel");
    }

    /// A client disconnect (dropped without a status ever produced) records a
    /// client early disconnect.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    #[ignore = "requires docker; run with cargo test -- --ignored"]
    async fn test_inflight_guard_drop_records_client_early_disconnect() {
        let (svc, _observer, _container) = make_nats_service().await;
        let function_id = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let recorder = DebuggingRecorder::new();
        let snapshotter = recorder.snapshotter();

        ::metrics::with_local_recorder(&recorder, || {
            let _guard = InflightRequestGuard::new(
                svc.clone(),
                RequestId::new(),
                function_id,
                fvid,
                "nca-test".to_string(),
            );
            // dropped without mark_completed / mark_status_returned => client disconnect
        });

        let value = snapshotter
            .snapshot()
            .into_vec()
            .into_iter()
            .find_map(|(key, _, _, value)| {
                let matches = key.kind() == MetricKind::Counter
                    && key.key().name() == "app.invocation.error"
                    && key.key().labels().any(|label| {
                        label.key() == "http_status_code"
                            && label.value() == metrics::CLIENT_EARLY_DISCONNECT_STATUS
                    })
                    && key.key().labels().any(|label| {
                        label.key() == "function_id" && label.value() == function_id.to_string()
                    });
                matches.then_some(value)
            })
            .expect(
                "client disconnect should record a client-early-disconnect app.invocation.error",
            );

        assert_eq!(value, DebugValue::Counter(1));
    }

    /// A server-side early exit (status was returned) must NOT be counted as a
    /// client early disconnect.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    #[ignore = "requires docker; run with cargo test -- --ignored"]
    async fn test_inflight_guard_drop_no_client_early_disconnect_on_server_error() {
        let (svc, _observer, _container) = make_nats_service().await;
        let function_id = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let recorder = DebuggingRecorder::new();
        let snapshotter = recorder.snapshotter();

        ::metrics::with_local_recorder(&recorder, || {
            let mut guard = InflightRequestGuard::new(
                svc.clone(),
                RequestId::new(),
                function_id,
                fvid,
                "nca-test".to_string(),
            );
            // a status was produced (server-side early exit)
            guard.mark_status_returned();
        });

        let found_disconnect =
            snapshotter
                .snapshot()
                .into_vec()
                .into_iter()
                .any(|(key, _, _, _)| {
                    key.key().name() == "app.invocation.error"
                        && key.key().labels().any(|label| {
                            label.key() == "http_status_code"
                                && label.value() == metrics::CLIENT_EARLY_DISCONNECT_STATUS
                        })
                });

        assert!(
            !found_disconnect,
            "server-side error must not record a client early disconnect"
        );
    }
}
