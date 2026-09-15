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

use std::net::SocketAddr;
use std::pin::Pin;
use std::sync::Arc;
use std::time::Duration;

use futures::Stream;
use tokio::sync::watch;
use tonic::{Request, Response, Status};
use tracing::{debug, info, warn};

use stargate_forwarding::{
    ForwardingResolver, PeerResolution, PeerTarget, forward_stream_messages,
};

use crate::auth::WorkerAuthenticator;
use crate::discovery::Discovery;
use crate::routing_state::StargateState;
use stargate_runtime::CriticalTaskGroup;

use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_control_plane_server::StargateControlPlane;
use stargate_proto::pb::stargate_model_discovery_server::StargateModelDiscovery;
use stargate_proto::pb::{
    InferenceServerAck, InferenceServerRegistration, ListModelsRequest, ListModelsResponse,
    WatchStargatesRequest, WatchStargatesResponse,
};

mod registration;
mod watch_stargates;

pub use self::registration::{
    DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT, DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
};
pub(crate) use self::registration::{
    RegistrationConnectionConfig, ReverseTunnelRegistrationConfig,
};
use self::registration::{
    negotiated_registration_update_idle_timeout, process_registration_stream,
};
use self::watch_stargates::{
    WatchStargatesPublisherConfig, spawn_watch_stargates_publisher,
    watch_stargates_stream_from_receiver,
};

#[derive(Clone)]
pub struct StargateService {
    stargate_id: String,
    advertise_addr: SocketAddr,
    discovery_dns_name: String,
    watch_stargates_rx: watch::Receiver<WatchStargatesResponse>,
    state: Arc<StargateState>,
    registration_connection_config: RegistrationConnectionConfig,
    registration_update_idle_timeout: Duration,
    registration_update_max_idle_timeout: Duration,
    forwarding: Option<Arc<dyn ForwardingResolver>>,
    authenticator: Arc<dyn WorkerAuthenticator>,
    tasks: CriticalTaskGroup,
}

pub(crate) struct StargateServiceConfig {
    pub(crate) stargate_id: String,
    pub(crate) advertise_addr: SocketAddr,
    pub(crate) discovery_dns_name: String,
    pub(crate) discovery: Box<dyn Discovery>,
    pub(crate) remote_watch_stargate_urls: Vec<String>,
    pub(crate) grpc_pylon_dial_addr: Option<String>,
    pub(crate) discovery_poll_interval: Duration,
    pub(crate) watch_heartbeat_interval: Duration,
    pub(crate) tasks: CriticalTaskGroup,
    pub(crate) registration_update_idle_timeout: Duration,
    pub(crate) registration_update_max_idle_timeout: Duration,
    pub(crate) state: Arc<StargateState>,
    pub(crate) registration_connection_config: RegistrationConnectionConfig,
    pub(crate) forwarding: Option<Arc<dyn ForwardingResolver>>,
    pub(crate) authenticator: Arc<dyn WorkerAuthenticator>,
}

impl StargateService {
    pub(crate) fn new(config: StargateServiceConfig) -> Self {
        if !config.registration_update_idle_timeout.is_zero()
            && !config.registration_update_max_idle_timeout.is_zero()
            && config.registration_update_max_idle_timeout < config.registration_update_idle_timeout
        {
            warn!(
                registration_update_idle_timeout_ms =
                    config.registration_update_idle_timeout.as_millis(),
                registration_update_max_idle_timeout_ms =
                    config.registration_update_max_idle_timeout.as_millis(),
                "registration update max idle timeout is below the idle-timeout floor; max cap wins"
            );
        }

        let tasks = config.tasks.clone();
        let watch_stargates_rx = spawn_watch_stargates_publisher(WatchStargatesPublisherConfig {
            advertise_addr: config.advertise_addr,
            discovery_dns_name: config.discovery_dns_name.clone(),
            discovery: config.discovery,
            remote_watch_stargate_urls: config.remote_watch_stargate_urls,
            grpc_pylon_dial_addr: config.grpc_pylon_dial_addr,
            discovery_poll_interval: config.discovery_poll_interval,
            watch_heartbeat_interval: config.watch_heartbeat_interval,
            tasks: config.tasks,
        });

        Self {
            stargate_id: config.stargate_id,
            advertise_addr: config.advertise_addr,
            discovery_dns_name: config.discovery_dns_name,
            watch_stargates_rx,
            state: config.state,
            registration_connection_config: config.registration_connection_config,
            registration_update_idle_timeout: config.registration_update_idle_timeout,
            registration_update_max_idle_timeout: config.registration_update_max_idle_timeout,
            forwarding: config.forwarding,
            authenticator: config.authenticator,
            tasks,
        }
    }

    fn peer_grpc_target<T>(&self, request: &Request<T>) -> Option<PeerTarget> {
        let fwd = self.forwarding.as_ref()?;
        let authority = request.extensions().get::<http::uri::Authority>()?;
        let host = authority.host();
        match fwd.resolve_peer(host, self.advertise_addr.port()) {
            PeerResolution::Peer(target) => Some(target),
            PeerResolution::Local | PeerResolution::NotPeer => None,
        }
    }

    async fn connect_peer(
        addr: &str,
    ) -> Result<StargateControlPlaneClient<tonic::transport::Channel>, Status> {
        let endpoint = if addr.starts_with("http") {
            addr.to_string()
        } else {
            format!("http://{addr}")
        };
        let channel = tonic::transport::Channel::from_shared(endpoint)
            .map_err(|e| Status::internal(format!("invalid peer address: {e}")))?
            .connect()
            .await
            .map_err(|e| Status::unavailable(format!("failed to connect to peer: {e}")))?;
        Ok(StargateControlPlaneClient::new(channel))
    }

    pub fn state(&self) -> Arc<StargateState> {
        self.state.clone()
    }
}

fn bearer_token(metadata: &tonic::metadata::MetadataMap) -> Option<&str> {
    metadata
        .get("authorization")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
}

type ResponseStream<T> = Pin<Box<dyn Stream<Item = Result<T, Status>> + Send + 'static>>;

#[tonic::async_trait]
impl StargateControlPlane for StargateService {
    type WatchStargatesStream = ResponseStream<WatchStargatesResponse>;
    type RegisterInferenceServerStream = ResponseStream<InferenceServerAck>;

    async fn watch_stargates(
        &self,
        request: Request<WatchStargatesRequest>,
    ) -> Result<Response<Self::WatchStargatesStream>, Status> {
        if let Some(peer) = self.peer_grpc_target(&request) {
            info!(
                peer = %peer.dial_addr,
                server_name = %peer.server_name,
                "forwarding watch_stargates to peer"
            );
            let mut peer_client = Self::connect_peer(&peer.dial_addr).await?;
            let resp = peer_client
                .watch_stargates(WatchStargatesRequest {})
                .await?;
            let mut inner = resp.into_inner();
            let stream = async_stream::stream! {
                let _client = peer_client;
                while let Some(msg) = inner.message().await.transpose() {
                    yield msg;
                }
            };
            return Ok(Response::new(Box::pin(stream)));
        }

        info!(
            stargate_id = %self.stargate_id,
            advertise_addr = %self.advertise_addr,
            dns_name = %self.discovery_dns_name,
            "watch stargates stream opened"
        );

        let out = watch_stargates_stream_from_receiver(self.watch_stargates_rx.clone());

        Ok(Response::new(Box::pin(out)))
    }

    async fn register_inference_server(
        &self,
        request: Request<tonic::Streaming<InferenceServerRegistration>>,
    ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
        if let Some(peer) = self.peer_grpc_target(&request) {
            info!(
                peer = %peer.dial_addr,
                server_name = %peer.server_name,
                "forwarding register_inference_server to peer"
            );
            let mut peer_client = Self::connect_peer(&peer.dial_addr).await?;
            let metadata = request.metadata().clone();
            let (inbound, mut stream_error_rx) =
                forward_stream_messages(request.into_inner(), |error| {
                    warn!(
                        error = %error,
                        "forwarded registration stream read error, forwarding stream error"
                    );
                });
            let mut forwarded = Request::new(inbound);
            *forwarded.metadata_mut() = metadata;
            let resp = peer_client.register_inference_server(forwarded).await?;
            let mut inner = resp.into_inner();
            let stream = async_stream::stream! {
                let _client = peer_client;
                let mut stream_error_rx_open = true;
                loop {
                    tokio::select! {
                        error = stream_error_rx.recv(), if stream_error_rx_open => {
                            match error {
                                Some(error) => {
                                    yield Err(error);
                                    break;
                                }
                                None => stream_error_rx_open = false,
                            }
                        }
                        message = inner.message() => {
                            match message {
                                Ok(Some(message)) => yield Ok(message),
                                Ok(None) => {
                                    if let Some(error) = stream_error_rx.recv().await {
                                        yield Err(error);
                                    }
                                    break;
                                }
                                Err(error) => {
                                    yield Err(error);
                                    break;
                                }
                            }
                        }
                    }
                }
            };
            return Ok(Response::new(Box::pin(stream)));
        }

        let token = bearer_token(request.metadata());
        let auth_result = self.authenticator.authenticate(token).await.map_err(|e| {
            warn!(error = %e, "gRPC registration authentication failed");
            Status::unauthenticated("authentication failed")
        })?;

        info!(
            stargate_id = %self.stargate_id,
            "register inference servers stream opened"
        );

        let (tx, rx) = flume::bounded::<Result<InferenceServerAck, Status>>(16);
        let state = self.state.clone();
        let registration_connection_config = self.registration_connection_config.clone();
        let idle_timeout = negotiated_registration_update_idle_timeout(
            request.metadata(),
            self.registration_update_idle_timeout,
            self.registration_update_max_idle_timeout,
        );
        let stop = self.tasks.shutdown_signal();

        self.tasks.task_tracker().spawn(async move {
            process_registration_stream(
                request.into_inner(),
                state,
                registration_connection_config,
                tx,
                auth_result,
                idle_timeout,
                stop,
            )
            .await;
            info!("register inference servers stream closed");
        });

        Ok(Response::new(Box::pin(rx.into_stream())))
    }
}

#[tonic::async_trait]
impl StargateModelDiscovery for StargateService {
    async fn list_models(
        &self,
        request: Request<ListModelsRequest>,
    ) -> Result<Response<ListModelsResponse>, Status> {
        list_models_for_state(&self.state, request.into_inner())
            .await
            .map(Response::new)
            .map_err(Status::invalid_argument)
    }
}

pub(crate) async fn list_models_for_state(
    state: &StargateState,
    request: ListModelsRequest,
) -> Result<ListModelsResponse, &'static str> {
    let requested = normalize_list_models_request(request)?;
    let model_id_filter_count = requested.model_ids.len();
    let model_ids = state
        .list_active_models(requested.routing_key.as_deref(), &requested.model_ids)
        .await;

    debug!(
        routing_key = ?requested.routing_key,
        model_id_filter_count,
        return_all_models = model_id_filter_count == 0,
        returned_model_count = model_ids.len(),
        "list_models completed"
    );

    Ok(ListModelsResponse { model_ids })
}

fn normalize_list_models_request(
    mut request: ListModelsRequest,
) -> Result<ListModelsRequest, &'static str> {
    request.routing_key = request
        .routing_key
        .as_deref()
        .map(str::trim)
        .filter(|routing_key| !routing_key.is_empty())
        .map(ToOwned::to_owned);

    for model_id in &mut request.model_ids {
        *model_id = model_id.trim().to_owned();
        if model_id.is_empty() {
            return Err("model_ids must not contain empty values");
        }
    }

    Ok(request)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn list_models_request(routing_key: Option<&str>, model_ids: &[&str]) -> ListModelsRequest {
        ListModelsRequest {
            routing_key: routing_key.map(str::to_owned),
            model_ids: model_ids.iter().map(|id| (*id).to_owned()).collect(),
        }
    }

    #[test]
    fn normalize_list_models_request_preserves_boundary_policies() {
        let cases = [
            (
                list_models_request(None, &[" model-a ", "model-b"]),
                list_models_request(None, &["model-a", "model-b"]),
            ),
            (
                list_models_request(Some(" rk-a "), &[]),
                list_models_request(Some("rk-a"), &[]),
            ),
            (
                list_models_request(Some(" "), &[]),
                list_models_request(None, &[]),
            ),
            (ListModelsRequest::default(), ListModelsRequest::default()),
        ];

        for (request, expected) in cases {
            assert_eq!(
                normalize_list_models_request(request).expect("case should normalize"),
                expected
            );
        }
    }

    #[test]
    fn normalize_list_models_request_rejects_blank_model_filter() {
        let error = normalize_list_models_request(list_models_request(None, &[" "]))
            .expect_err("blank model filter should be rejected");

        assert_eq!(error, "model_ids must not contain empty values");
    }
}
