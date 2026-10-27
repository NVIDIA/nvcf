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

//! Typed GDS calls with retries and failover.

use std::future::Future;
use std::pin::Pin;
use std::task::{Context, Poll};

use futures::Stream;
use tokio::sync::watch;
use tonic::transport::Channel;
use tonic::{Code, Status};
use tracing::debug;

use crate::channel::{ChannelManager, Connection, ConnectionState, MAX_DECODING_MESSAGE_BYTES};
use crate::endpoints::{ConnectPolicy, EndpointSet, TlsOptions};
use crate::error::{Error, is_retryable_code};
use crate::proto::v1::cluster_service_client::ClusterServiceClient;
use crate::proto::v1::pool_service_client::PoolServiceClient;
use crate::proto::v1::server_info_service_client::ServerInfoServiceClient;
use crate::proto::v1::service_import_service_client::ServiceImportServiceClient;
use crate::proto::v1::{
    DeregisterPoolRequest, DeregisterPoolResponse, GetClusterRequest, GetClusterResponse,
    GetPoolRequest, GetPoolResponse, GetServerInfoRequest, GetServerInfoResponse,
    GetServiceImportRequest, GetServiceImportResponse, HeartbeatPoolRequest, HeartbeatPoolResponse,
    ListClustersRequest, ListClustersResponse, ListPoolsRequest, ListPoolsResponse,
    ListRoutersRequest, ListRoutersResponse, ListServiceImportsRequest, ListServiceImportsResponse,
    RegisterPoolRequest, RegisterPoolResponse, UpdatePoolRequest, UpdatePoolResponse,
    WatchPoolsRequest, WatchPoolsResponse, WatchRoutersRequest, WatchRoutersResponse,
    WatchServiceImportsRequest, WatchServiceImportsResponse,
};

/// Configuration of a [`GdsClient`].
#[derive(Debug, Clone, Default)]
pub struct GdsClientConfig {
    /// GDS gRPC URLs in failover order. The first is the primary, normally
    /// the global name; the rest are optional per-region fallbacks.
    pub urls: Vec<String>,
    pub tls: TlsOptions,
    pub policy: ConnectPolicy,
}

impl GdsClientConfig {
    pub fn new<I, S>(urls: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        Self {
            urls: urls.into_iter().map(Into::into).collect(),
            ..Self::default()
        }
    }
}

/// A GDS client. Cheap to clone; clones share the connection.
///
/// Unary calls run up to `policy.max_attempts` times. An attempt that fails
/// with UNAVAILABLE or DEADLINE_EXCEEDED, or that cannot connect, marks the
/// endpoint failed so the next attempt uses the next one, then waits a
/// jittered backoff. Watch calls retry only the opening of the stream; the
/// caches in [`crate::cache`] and [`crate::routers`] reconnect broken
/// streams.
#[derive(Debug, Clone)]
pub struct GdsClient {
    channels: ChannelManager,
}

impl GdsClient {
    /// A client for the configured URLs. It connects on the first call.
    pub fn new(config: GdsClientConfig) -> Result<Self, Error> {
        let endpoints = EndpointSet::parse(&config.urls, config.policy.allow_insecure_http)?;
        Ok(Self {
            channels: ChannelManager::new(endpoints, config.tls, config.policy),
        })
    }

    /// A client over a channel the host built. Retries apply; failover does
    /// not.
    pub fn from_channel(channel: Channel) -> Self {
        Self::from_channel_with_policy(channel, ConnectPolicy::default())
    }

    /// [`GdsClient::from_channel`] with an explicit policy for timeouts and
    /// retries.
    pub fn from_channel_with_policy(channel: Channel, policy: ConnectPolicy) -> Self {
        Self {
            channels: ChannelManager::from_channel(channel, policy),
        }
    }

    /// Records `nvcf_gds_client_*` metrics for this client and its clones.
    #[cfg(feature = "metrics")]
    pub fn with_metrics(self, metrics: std::sync::Arc<crate::metrics::GdsClientMetrics>) -> Self {
        self.channels.metrics().install(metrics);
        self
    }

    /// The endpoint failover state.
    pub fn channels(&self) -> &ChannelManager {
        &self.channels
    }

    pub fn policy(&self) -> &ConnectPolicy {
        self.channels.policy()
    }

    pub fn subscribe_connection(&self) -> watch::Receiver<ConnectionState> {
        self.channels.subscribe_connection()
    }

    pub(crate) fn metrics(&self) -> &crate::metrics::MetricsHandle {
        self.channels.metrics()
    }

    // ---- PoolService ----

    pub async fn register_pool(
        &self,
        request: RegisterPoolRequest,
    ) -> Result<RegisterPoolResponse, Error> {
        self.unary("RegisterPool", request, |channel, request| async move {
            pool_client(channel).register_pool(request).await
        })
        .await
    }

    pub async fn update_pool(
        &self,
        request: UpdatePoolRequest,
    ) -> Result<UpdatePoolResponse, Error> {
        self.unary("UpdatePool", request, |channel, request| async move {
            pool_client(channel).update_pool(request).await
        })
        .await
    }

    pub async fn heartbeat_pool(
        &self,
        request: HeartbeatPoolRequest,
    ) -> Result<HeartbeatPoolResponse, Error> {
        self.unary("HeartbeatPool", request, |channel, request| async move {
            pool_client(channel).heartbeat_pool(request).await
        })
        .await
    }

    pub async fn deregister_pool(
        &self,
        request: DeregisterPoolRequest,
    ) -> Result<DeregisterPoolResponse, Error> {
        self.unary("DeregisterPool", request, |channel, request| async move {
            pool_client(channel).deregister_pool(request).await
        })
        .await
    }

    pub async fn get_pool(&self, request: GetPoolRequest) -> Result<GetPoolResponse, Error> {
        self.unary("GetPool", request, |channel, request| async move {
            pool_client(channel).get_pool(request).await
        })
        .await
    }

    pub async fn list_pools(&self, request: ListPoolsRequest) -> Result<ListPoolsResponse, Error> {
        self.unary("ListPools", request, |channel, request| async move {
            pool_client(channel).list_pools(request).await
        })
        .await
    }

    /// Opens a pool watch. See [`WatchStream`].
    pub async fn watch_pools(
        &self,
        request: WatchPoolsRequest,
    ) -> Result<WatchStream<WatchPoolsResponse>, Error> {
        self.open_watch("WatchPools", request, |channel, request| async move {
            pool_client(channel).watch_pools(request).await
        })
        .await
    }

    pub async fn list_routers(
        &self,
        request: ListRoutersRequest,
    ) -> Result<ListRoutersResponse, Error> {
        self.unary("ListRouters", request, |channel, request| async move {
            pool_client(channel).list_routers(request).await
        })
        .await
    }

    /// Opens a router watch. See [`WatchStream`].
    pub async fn watch_routers(
        &self,
        request: WatchRoutersRequest,
    ) -> Result<WatchStream<WatchRoutersResponse>, Error> {
        self.open_watch("WatchRouters", request, |channel, request| async move {
            pool_client(channel).watch_routers(request).await
        })
        .await
    }

    // ---- ClusterService (read side) ----

    pub async fn get_cluster(
        &self,
        request: GetClusterRequest,
    ) -> Result<GetClusterResponse, Error> {
        self.unary("GetCluster", request, |channel, request| async move {
            cluster_client(channel).get_cluster(request).await
        })
        .await
    }

    pub async fn list_clusters(
        &self,
        request: ListClustersRequest,
    ) -> Result<ListClustersResponse, Error> {
        self.unary("ListClusters", request, |channel, request| async move {
            cluster_client(channel).list_clusters(request).await
        })
        .await
    }

    // ---- ServiceImportService ----

    pub async fn get_service_import(
        &self,
        request: GetServiceImportRequest,
    ) -> Result<GetServiceImportResponse, Error> {
        self.unary("GetServiceImport", request, |channel, request| async move {
            import_client(channel).get_service_import(request).await
        })
        .await
    }

    pub async fn list_service_imports(
        &self,
        request: ListServiceImportsRequest,
    ) -> Result<ListServiceImportsResponse, Error> {
        self.unary("ListServiceImports", request, |channel, request| async move {
            import_client(channel).list_service_imports(request).await
        })
        .await
    }

    /// Opens a service import watch. See [`WatchStream`].
    pub async fn watch_service_imports(
        &self,
        request: WatchServiceImportsRequest,
    ) -> Result<WatchStream<WatchServiceImportsResponse>, Error> {
        self.open_watch(
            "WatchServiceImports",
            request,
            |channel, request| async move {
                import_client(channel).watch_service_imports(request).await
            },
        )
        .await
    }

    // ---- ServerInfoService ----

    pub async fn server_info(&self) -> Result<GetServerInfoResponse, Error> {
        self.unary(
            "GetServerInfo",
            GetServerInfoRequest {},
            |channel, request| async move {
                ServerInfoServiceClient::new(channel)
                    .max_decoding_message_size(MAX_DECODING_MESSAGE_BYTES)
                    .get_server_info(request)
                    .await
            },
        )
        .await
    }

    /// Runs a unary call with per-attempt deadlines, retries and failover.
    async fn unary<Req, Resp, F, Fut>(
        &self,
        method: &'static str,
        request: Req,
        call: F,
    ) -> Result<Resp, Error>
    where
        Req: Clone,
        F: Fn(Channel, tonic::Request<Req>) -> Fut,
        Fut: Future<Output = Result<tonic::Response<Resp>, Status>>,
    {
        let policy = self.channels.policy();
        let max_attempts = policy.max_attempts.max(1);
        let rpc_timeout = policy.rpc_timeout;
        let metrics = self.channels.metrics();
        let mut attempt = 0;
        loop {
            let error = match self.channels.channel().await {
                Ok(connection) => {
                    let mut grpc_request = tonic::Request::new(request.clone());
                    grpc_request.set_timeout(rpc_timeout);
                    let started = tokio::time::Instant::now();
                    let result = tokio::time::timeout(
                        rpc_timeout,
                        call(connection.channel().clone(), grpc_request),
                    )
                    .await
                    .unwrap_or_else(|_| {
                        Err(Status::deadline_exceeded(format!(
                            "{method} did not complete within {rpc_timeout:?}"
                        )))
                    });
                    let code = result.as_ref().map_or_else(Status::code, |_| Code::Ok);
                    metrics.rpc(method, code, Some(started.elapsed()));
                    match result {
                        Ok(response) => return Ok(response.into_inner()),
                        Err(status) if is_retryable_code(status.code()) => {
                            self.channels.mark_failed(&connection);
                            Error::Rpc(status)
                        }
                        Err(status) => return Err(Error::Rpc(status)),
                    }
                }
                Err(error) if error.is_retryable() => error,
                Err(error) => return Err(error),
            };
            attempt += 1;
            if attempt >= max_attempts {
                return Err(error);
            }
            metrics.retry(method);
            let delay = policy.backoff.delay(attempt - 1);
            debug!(method, attempt, ?delay, %error, "retrying GDS call");
            tokio::time::sleep(delay).await;
        }
    }

    /// Opens a server stream, retrying the open like a unary call. Only the
    /// wait for the response headers has a deadline; the stream itself has
    /// none.
    async fn open_watch<Req, Resp, F, Fut>(
        &self,
        method: &'static str,
        request: Req,
        call: F,
    ) -> Result<WatchStream<Resp>, Error>
    where
        Req: Clone,
        F: Fn(Channel, tonic::Request<Req>) -> Fut,
        Fut: Future<Output = Result<tonic::Response<tonic::Streaming<Resp>>, Status>>,
    {
        let policy = self.channels.policy();
        let max_attempts = policy.max_attempts.max(1);
        let rpc_timeout = policy.rpc_timeout;
        let metrics = self.channels.metrics();
        let mut attempt = 0;
        loop {
            let error = match self.channels.channel().await {
                Ok(connection) => {
                    let result = tokio::time::timeout(
                        rpc_timeout,
                        call(
                            connection.channel().clone(),
                            tonic::Request::new(request.clone()),
                        ),
                    )
                    .await
                    .unwrap_or_else(|_| {
                        Err(Status::deadline_exceeded(format!(
                            "{method} did not start within {rpc_timeout:?}"
                        )))
                    });
                    match result {
                        Ok(response) => {
                            metrics.rpc(method, Code::Ok, None);
                            return Ok(WatchStream {
                                inner: response.into_inner(),
                                channels: self.channels.clone(),
                                connection,
                                method,
                            });
                        }
                        Err(status) => {
                            metrics.rpc(method, status.code(), None);
                            if !is_retryable_code(status.code()) {
                                return Err(Error::Rpc(status));
                            }
                            self.channels.mark_failed(&connection);
                            Error::Rpc(status)
                        }
                    }
                }
                Err(error) if error.is_retryable() => error,
                Err(error) => return Err(error),
            };
            attempt += 1;
            if attempt >= max_attempts {
                return Err(error);
            }
            metrics.retry(method);
            let delay = policy.backoff.delay(attempt - 1);
            debug!(method, attempt, ?delay, %error, "retrying GDS watch");
            tokio::time::sleep(delay).await;
        }
    }
}

fn pool_client(channel: Channel) -> PoolServiceClient<Channel> {
    PoolServiceClient::new(channel).max_decoding_message_size(MAX_DECODING_MESSAGE_BYTES)
}

fn cluster_client(channel: Channel) -> ClusterServiceClient<Channel> {
    ClusterServiceClient::new(channel).max_decoding_message_size(MAX_DECODING_MESSAGE_BYTES)
}

fn import_client(channel: Channel) -> ServiceImportServiceClient<Channel> {
    ServiceImportServiceClient::new(channel).max_decoding_message_size(MAX_DECODING_MESSAGE_BYTES)
}

/// An open GDS watch stream. Events follow the watch contract:
/// `SNAPSHOT_START`, one `UPSERT` per object, `SNAPSHOT_END` with a revision,
/// then `UPSERT` and `DELETE` deltas and a `BOOKMARK` every few seconds.
/// When the stream breaks (UNAVAILABLE, DEADLINE_EXCEEDED, or UNKNOWN for a
/// connection lost mid-stream) the endpoint is marked failed so a reopened
/// watch uses the next one. Reopen with the last revision seen as
/// `resume_revision`; the server replays the missed deltas or sends a fresh
/// snapshot.
#[derive(Debug)]
pub struct WatchStream<T> {
    inner: tonic::Streaming<T>,
    channels: ChannelManager,
    connection: Connection,
    method: &'static str,
}

impl<T> WatchStream<T> {
    /// The next event, `Ok(None)` when the server closed the stream.
    pub async fn message(&mut self) -> Result<Option<T>, Error> {
        let result = self.inner.message().await;
        self.observe(result)
    }

    /// The connection the stream runs on.
    pub fn connection(&self) -> &Connection {
        &self.connection
    }

    fn observe(&self, result: Result<Option<T>, Status>) -> Result<Option<T>, Error> {
        match result {
            Ok(Some(message)) => Ok(Some(message)),
            Ok(None) => {
                debug!(method = self.method, endpoint = %self.connection.url(), "GDS watch ended");
                Ok(None)
            }
            Err(status) => {
                if is_retryable_code(status.code()) || status.code() == Code::Unknown {
                    self.channels.mark_failed(&self.connection);
                }
                Err(Error::Rpc(status))
            }
        }
    }
}

impl<T> Stream for WatchStream<T> {
    type Item = Result<T, Error>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let this = self.get_mut();
        match Pin::new(&mut this.inner).poll_next(cx) {
            Poll::Pending => Poll::Pending,
            Poll::Ready(None) => {
                let _ = this.observe(Ok(None));
                Poll::Ready(None)
            }
            Poll::Ready(Some(Ok(message))) => Poll::Ready(Some(Ok(message))),
            Poll::Ready(Some(Err(status))) => Poll::Ready(this.observe(Err(status)).err().map(Err)),
        }
    }
}
