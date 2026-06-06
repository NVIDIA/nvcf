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

use std::collections::{BTreeMap, BTreeSet};
use std::net::IpAddr;
use std::net::SocketAddr;
use std::pin::Pin;
use std::sync::Arc;
use std::time::{Duration, Instant};

use futures::{Stream, StreamExt, stream};
use tokio::sync::{mpsc, watch};
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;
use tonic::{Request, Response, Status};
use tracing::{debug, info, warn};
use url::Url;

use stargate_forwarding::{ForwardingResolver, PeerResolution, PeerTarget};

use crate::auth::WorkerAuthenticator;
use crate::discovery::Discovery;
use crate::routing_state::{RegistrationIdentity, RunningRegistration, StargateState};
use crate::tunnel::{ConnectionWatcher, EnsureConnectedResult, QuicHttpProxy};

use stargate_proto::REGISTRATION_HEARTBEAT_MS_METADATA;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_control_plane_server::StargateControlPlane;
use stargate_proto::pb::stargate_model_discovery_server::StargateModelDiscovery;
use stargate_proto::pb::{
    CalibrationState, InferenceServerAck, InferenceServerRegistration, ListModelsRequest,
    ListModelsResponse, ModelCalibrationDirective, StargateInfo, SubmitClusterCalibrationRequest,
    SubmitClusterCalibrationResponse, WatchStargatesRequest, WatchStargatesResponse,
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
}

#[derive(Clone)]
pub struct RegistrationConnectionConfig {
    pub quic_proxy: Arc<QuicHttpProxy>,
    pub reverse_tunnel_connect_timeout: Duration,
    pub reverse_tunnel_target: Option<String>,
    pub reverse_tunnel_pylon_dial_addr: Option<String>,
}

pub struct StargateServiceConfig {
    pub stargate_id: String,
    pub advertise_addr: SocketAddr,
    pub discovery_dns_name: String,
    pub discovery: Box<dyn Discovery>,
    pub remote_watch_stargate_urls: Vec<String>,
    pub grpc_pylon_dial_addr: Option<String>,
    pub discovery_poll_interval: Duration,
    pub watch_heartbeat_interval: Duration,
    pub shutdown_token: CancellationToken,
    pub task_tracker: TaskTracker,
    pub registration_update_idle_timeout: Duration,
    pub registration_update_max_idle_timeout: Duration,
    pub state: Arc<StargateState>,
    pub registration_connection_config: RegistrationConnectionConfig,
    pub forwarding: Option<Arc<dyn ForwardingResolver>>,
    pub authenticator: Arc<dyn WorkerAuthenticator>,
}

impl StargateService {
    pub fn new(config: StargateServiceConfig) -> Self {
        let discovery = config.discovery;
        let local_watch_endpoint_keys =
            local_watch_endpoint_keys(config.advertise_addr, &config.discovery_dns_name);
        let remote_watch_stargate_urls = normalize_remote_watch_urls(
            config.remote_watch_stargate_urls,
            &local_watch_endpoint_keys,
        );
        let grpc_pylon_dial_addr = config.grpc_pylon_dial_addr;
        let discovery_poll_interval = config.discovery_poll_interval;
        let watch_heartbeat_interval = config.watch_heartbeat_interval;
        let shutdown_token = config.shutdown_token;
        let task_tracker = config.task_tracker;
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

        let (watch_stargates_tx, watch_stargates_rx) =
            watch::channel(WatchStargatesResponse::default());

        task_tracker.spawn(async move {
            let mut known = WatchStargatesResponse::default();
            let mut last_emit: Option<Instant> = None;
            let mut poll = tokio::time::interval(discovery_poll_interval);
            poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

            loop {
                tokio::select! {
                    _ = shutdown_token.cancelled() => break,
                    _ = poll.tick() => {}
                }

                let event = tokio::select! {
                    _ = shutdown_token.cancelled() => break,
                    stargates = discovery.discover_stargates() => {
                        build_watch_stargates_response(
                            stargates,
                            &remote_watch_stargate_urls,
                            grpc_pylon_dial_addr.as_deref(),
                        )
                    },
                };

                let changed = event != known;
                let heartbeat_due = last_emit
                    .map(|ts| ts.elapsed() >= watch_heartbeat_interval)
                    .unwrap_or(true);

                if changed || heartbeat_due {
                    let _ = watch_stargates_tx.send(event.clone());
                    last_emit = Some(Instant::now());
                    debug!(
                        stargate_count = event.stargates.len(),
                        remote_watch_url_count = event.watch_stargate_urls.len(),
                        changed,
                        heartbeat_due,
                        "published stargate snapshot"
                    );
                }

                known = event;
            }
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
        }
    }

    fn peer_grpc_target<T>(&self, request: &Request<T>) -> PeerGrpcTarget {
        let Some(fwd) = self.forwarding.as_ref() else {
            return PeerGrpcTarget::Local;
        };
        let Some(authority) = request.extensions().get::<http::uri::Authority>() else {
            return PeerGrpcTarget::Local;
        };
        let host = authority.host();
        match fwd.resolve_peer(host, self.advertise_addr.port()) {
            PeerResolution::Peer(target) => PeerGrpcTarget::Peer(target),
            PeerResolution::Local | PeerResolution::NotPeer => PeerGrpcTarget::Local,
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

fn build_watch_stargates_response(
    stargates: Vec<StargateInfo>,
    remote_watch_stargate_urls: &[String],
    grpc_pylon_dial_addr: Option<&str>,
) -> WatchStargatesResponse {
    let mut stargates = normalize_stargates(stargates);
    apply_grpc_pylon_dial_addr(&mut stargates, grpc_pylon_dial_addr);
    WatchStargatesResponse {
        stargates,
        watch_stargate_urls: remote_watch_stargate_urls.to_vec(),
    }
}

fn apply_grpc_pylon_dial_addr(stargates: &mut [StargateInfo], grpc_pylon_dial_addr: Option<&str>) {
    let Some(grpc_pylon_dial_addr) = grpc_pylon_dial_addr
        .map(str::trim)
        .filter(|addr| !addr.is_empty())
    else {
        return;
    };
    for stargate in stargates {
        stargate.grpc_pylon_dial_addr = grpc_pylon_dial_addr.to_string();
    }
}

fn normalize_stargates(stargates: Vec<StargateInfo>) -> Vec<StargateInfo> {
    let mut by_advertise_addr: BTreeMap<String, StargateInfo> = BTreeMap::new();
    for stargate in stargates {
        let entry = by_advertise_addr
            .entry(stargate.advertise_addr.clone())
            .or_insert_with(|| stargate.clone());
        if entry.stargate_id.is_empty() || !stargate.stargate_id.is_empty() {
            *entry = stargate;
        }
    }

    let mut deduped: BTreeMap<String, StargateInfo> = BTreeMap::new();
    for stargate in by_advertise_addr.into_values() {
        let key = if !stargate.stargate_id.is_empty() {
            stargate.stargate_id.clone()
        } else {
            stargate.advertise_addr.clone()
        };
        deduped.insert(key, stargate);
    }
    deduped.into_values().collect()
}

fn normalize_remote_watch_urls(
    urls: Vec<String>,
    excluded_endpoint_keys: &BTreeSet<String>,
) -> Vec<String> {
    let mut deduped: BTreeMap<String, String> = BTreeMap::new();
    for raw_url in urls {
        let url = raw_url.trim().to_string();
        if url.is_empty() {
            continue;
        }
        let key = watch_endpoint_key(&url).unwrap_or_else(|| url.clone());
        if excluded_endpoint_keys.contains(&key) {
            continue;
        }
        deduped.entry(key).or_insert(url);
    }
    deduped.into_values().collect()
}

fn local_watch_endpoint_keys(
    advertise_addr: SocketAddr,
    discovery_dns_name: &str,
) -> BTreeSet<String> {
    let discovery_dns_name = discovery_dns_name.trim();
    let mut endpoints = vec![
        advertise_addr.to_string(),
        format!("{discovery_dns_name}:{}", advertise_addr.port()),
    ];
    let cluster_service_dns_name = discovery_dns_name.replace("-headless.", ".");
    if cluster_service_dns_name != discovery_dns_name {
        endpoints.push(format!(
            "{cluster_service_dns_name}:{}",
            advertise_addr.port()
        ));
    }
    endpoints
        .into_iter()
        .filter_map(|endpoint| watch_endpoint_key(&endpoint))
        .collect()
}

fn watch_endpoint_key(endpoint: &str) -> Option<String> {
    let endpoint = endpoint.trim();
    if endpoint.is_empty() {
        return None;
    }
    let url = if endpoint.starts_with("http://") || endpoint.starts_with("https://") {
        endpoint.to_string()
    } else {
        format!("http://{endpoint}")
    };
    let parsed = Url::parse(&url).ok()?;
    let host = parsed.host_str()?;
    let port = parsed.port_or_known_default()?;
    Some(format!("{host}:{port}"))
}

fn watch_stargates_stream_from_receiver(
    mut rx: watch::Receiver<WatchStargatesResponse>,
) -> impl Stream<Item = Result<WatchStargatesResponse, Status>> + Send + 'static {
    let initial = rx.borrow_and_update().clone();
    let pending_initial = watch_response_has_entries(&initial).then_some(initial);
    stream::unfold((rx, pending_initial), |(mut rx, pending)| async move {
        if let Some(message) = pending {
            return Some((Ok(message), (rx, None)));
        }
        match rx.changed().await {
            Ok(()) => {
                let message = rx.borrow_and_update().clone();
                Some((Ok(message), (rx, None)))
            }
            Err(_) => None,
        }
    })
}

fn watch_response_has_entries(response: &WatchStargatesResponse) -> bool {
    !response.stargates.is_empty() || !response.watch_stargate_urls.is_empty()
}

fn registration_message_stream<S>(
    inbound: S,
) -> (
    impl Stream<Item = InferenceServerRegistration>,
    RegistrationStreamErrorRx,
)
where
    S: Stream<Item = Result<InferenceServerRegistration, Status>> + Send + 'static,
{
    let (stream_error_tx, stream_error_rx) = mpsc::channel(1);
    let messages = inbound.filter_map(move |result| {
        let stream_error_tx = stream_error_tx.clone();
        async move {
            match result {
                Ok(message) => Some(message),
                Err(error) => {
                    warn!(
                        error = %error,
                        "forwarded registration stream read error, forwarding stream error"
                    );
                    let _ = stream_error_tx.send(error).await;
                    None
                }
            }
        }
    });
    (messages, stream_error_rx)
}

async fn pending_registration_stream_error(
    stream_error_rx: &mut RegistrationStreamErrorRx,
) -> Option<Status> {
    match stream_error_rx.try_recv() {
        Ok(error) => Some(error),
        Err(mpsc::error::TryRecvError::Empty) => stream_error_rx.recv().await,
        Err(mpsc::error::TryRecvError::Disconnected) => None,
    }
}

enum PeerGrpcTarget {
    Local,
    Peer(PeerTarget),
}

type WatchStargatesStream =
    Pin<Box<dyn Stream<Item = Result<WatchStargatesResponse, Status>> + Send + 'static>>;
type RegisterInferenceServerStream =
    Pin<Box<dyn Stream<Item = Result<InferenceServerAck, Status>> + Send + 'static>>;
type RegistrationStreamErrorRx = mpsc::Receiver<Status>;

const HEALTH_CHECK_INTERVAL: Duration = Duration::from_secs(1);
const HEALTH_CHECK_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(2);
pub const DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT: Duration = Duration::from_secs(60);
pub const DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT: Duration = Duration::from_secs(300);

struct StreamRegistration {
    running: RunningRegistration,
    reverse_tunnel_watcher: Option<ConnectionWatcher>,
    health_check: Option<HealthCheckHandle>,
}

enum RegistrationStreamState {
    Start,
    Running(Box<StreamRegistration>),
}

enum ApplyUpdateOutcome {
    Ack(InferenceServerAck),
    Skip,
}

struct HealthCheckHandle {
    stop: CancellationToken,
    task: Option<JoinHandle<()>>,
    rx: watch::Receiver<HealthCheckStatus>,
}

impl HealthCheckHandle {
    async fn shutdown(mut self) {
        self.stop.cancel();
        if let Some(task) = self.task.take() {
            await_health_check_task(task, HEALTH_CHECK_SHUTDOWN_TIMEOUT).await;
        }
    }
}

impl Drop for HealthCheckHandle {
    fn drop(&mut self) {
        self.stop.cancel();
        if let Some(task) = self.task.take() {
            // A registration processor can be aborted before cleanup; abort the
            // owned health-check task before dropping its join handle.
            task.abort();
        }
    }
}

struct AbortOnDropHealthCheckTask {
    task: Option<JoinHandle<()>>,
}

impl AbortOnDropHealthCheckTask {
    fn new(task: JoinHandle<()>) -> Self {
        Self { task: Some(task) }
    }

    fn abort(&self) {
        if let Some(task) = &self.task {
            task.abort();
        }
    }

    async fn join(&mut self) -> Result<(), tokio::task::JoinError> {
        self.task
            .as_mut()
            .expect("health-check task should not be disarmed before join")
            .await
    }

    fn disarm(&mut self) {
        let _completed = self.task.take();
    }
}

impl Drop for AbortOnDropHealthCheckTask {
    fn drop(&mut self) {
        if let Some(task) = &self.task {
            // A parent cleanup future can be cancelled while waiting; abort
            // before dropping the join handle so the task is not detached.
            task.abort();
        }
    }
}

async fn await_health_check_task(task: JoinHandle<()>, timeout: Duration) {
    let mut task = AbortOnDropHealthCheckTask::new(task);
    match tokio::time::timeout(timeout, task.join()).await {
        Ok(result) => {
            task.disarm();
            finish_health_check_task(result);
        }
        Err(_) => {
            warn!(
                timeout_ms = timeout.as_millis(),
                "health-check task did not stop before shutdown timeout"
            );
            // Cooperative shutdown missed the timeout; abort is the final fallback.
            task.abort();
            let result = task.join().await;
            task.disarm();
            finish_health_check_task(result);
        }
    }
}

fn finish_health_check_task(result: Result<(), tokio::task::JoinError>) {
    match result {
        Ok(()) => {}
        Err(error) if error.is_cancelled() => {}
        Err(error) if error.is_panic() => std::panic::resume_unwind(error.into_panic()),
        Err(error) => warn!(error = %error, "health-check task join failed"),
    }
}

#[derive(Clone, Debug)]
enum HealthCheckStatus {
    Pending,
    Ready(Duration),
}

#[tonic::async_trait]
impl StargateControlPlane for StargateService {
    type WatchStargatesStream = WatchStargatesStream;
    type RegisterInferenceServerStream = RegisterInferenceServerStream;

    async fn watch_stargates(
        &self,
        request: Request<WatchStargatesRequest>,
    ) -> Result<Response<Self::WatchStargatesStream>, Status> {
        match self.peer_grpc_target(&request) {
            PeerGrpcTarget::Peer(peer) => {
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
            PeerGrpcTarget::Local => {}
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
        match self.peer_grpc_target(&request) {
            PeerGrpcTarget::Peer(peer) => {
                info!(
                    peer = %peer.dial_addr,
                    server_name = %peer.server_name,
                    "forwarding register_inference_server to peer"
                );
                let mut peer_client = Self::connect_peer(&peer.dial_addr).await?;
                let metadata = request.metadata().clone();
                let (inbound, mut stream_error_rx) =
                    registration_message_stream(request.into_inner());
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
                                        if let Some(error) =
                                            pending_registration_stream_error(&mut stream_error_rx).await
                                        {
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
            PeerGrpcTarget::Local => {}
        }

        let token = request
            .metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.strip_prefix("Bearer "));
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

        tokio::spawn(async move {
            process_registration_stream(
                request.into_inner(),
                state,
                registration_connection_config,
                tx,
                auth_result,
                idle_timeout,
            )
            .await;
            info!("register inference servers stream closed");
        });

        let out = stream::unfold(rx, |rx| async move {
            match rx.recv_async().await {
                Ok(item) => Some((item, rx)),
                Err(_) => None,
            }
        });

        Ok(Response::new(Box::pin(out)))
    }

    async fn submit_cluster_calibration(
        &self,
        request: Request<SubmitClusterCalibrationRequest>,
    ) -> Result<Response<SubmitClusterCalibrationResponse>, Status> {
        match self.peer_grpc_target(&request) {
            PeerGrpcTarget::Peer(peer) => {
                info!(
                    peer = %peer.dial_addr,
                    server_name = %peer.server_name,
                    "forwarding submit_cluster_calibration to peer"
                );
                let mut peer_client = Self::connect_peer(&peer.dial_addr).await?;
                let metadata = request.metadata().clone();
                let mut forwarded = Request::new(request.into_inner());
                *forwarded.metadata_mut() = metadata;
                return peer_client.submit_cluster_calibration(forwarded).await;
            }
            PeerGrpcTarget::Local => {}
        }

        let token = request
            .metadata()
            .get("authorization")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "));
        let auth_result = self
            .authenticator
            .authenticate(token)
            .await
            .map_err(|error| {
                warn!(error = %error, "gRPC calibration submission authentication failed");
                Status::unauthenticated("authentication failed")
            })?;

        self.state
            .submit_cluster_calibration(auth_result.routing_key, request.get_ref())
            .await?;

        Ok(Response::new(SubmitClusterCalibrationResponse {
            state: CalibrationState::Complete as i32,
        }))
    }
}

#[tonic::async_trait]
impl StargateModelDiscovery for StargateService {
    async fn list_models(
        &self,
        request: Request<ListModelsRequest>,
    ) -> Result<Response<ListModelsResponse>, Status> {
        let requested = normalize_list_models_request(request.into_inner())
            .map_err(Status::invalid_argument)?;
        let model_id_filter_count = requested.model_ids.len();
        let model_ids = self
            .state
            .list_active_models(requested.routing_key.as_deref(), &requested.model_ids);

        debug!(
            routing_key = ?requested.routing_key,
            model_id_filter_count,
            return_all_models = model_id_filter_count == 0,
            returned_model_count = model_ids.len(),
            "list_models completed"
        );

        Ok(Response::new(ListModelsResponse { model_ids }))
    }
}

async fn process_registration_stream(
    stream: impl Stream<Item = Result<InferenceServerRegistration, Status>> + Unpin,
    state: Arc<StargateState>,
    registration_connection_config: RegistrationConnectionConfig,
    tx: flume::Sender<Result<InferenceServerAck, Status>>,
    auth_result: crate::auth::AuthResult,
    idle_timeout: Option<Duration>,
) {
    process_registration_stream_with_state(
        stream,
        state,
        registration_connection_config,
        tx,
        auth_result,
        idle_timeout,
        RegistrationStreamState::Start,
    )
    .await;
}

async fn process_registration_stream_with_state(
    mut stream: impl Stream<Item = Result<InferenceServerRegistration, Status>> + Unpin,
    state: Arc<StargateState>,
    registration_connection_config: RegistrationConnectionConfig,
    tx: flume::Sender<Result<InferenceServerAck, Status>>,
    auth_result: crate::auth::AuthResult,
    idle_timeout: Option<Duration>,
    mut stream_state: RegistrationStreamState,
) {
    loop {
        let next = if let Some(idle_timeout) = idle_timeout {
            match tokio::time::timeout(idle_timeout, stream.next()).await {
                Ok(Some(next)) => next,
                Ok(None) => break,
                Err(_elapsed) => {
                    warn!(
                        idle_timeout_ms = idle_timeout.as_millis(),
                        "registration stream idle timeout; closing registration"
                    );
                    break;
                }
            }
        } else {
            match stream.next().await {
                Some(next) => next,
                None => break,
            }
        };

        let update = match next {
            Ok(update) => update,
            Err(error) => {
                warn!(error = %error, "inference servers stream read failed or closed");
                break;
            }
        };

        debug!(
            inference_server_id = %update.inference_server_id,
            model_ids = ?update.models.keys().collect::<Vec<_>>(),
            "received inference servers update"
        );

        if matches!(stream_state, RegistrationStreamState::Start) {
            match start_registration_stream(
                &update,
                &state,
                &registration_connection_config,
                auth_result.routing_key.as_deref(),
            )
            .await
            {
                Ok(running) => {
                    stream_state = RegistrationStreamState::Running(Box::new(running));
                }
                Err(status) => {
                    let _ = tx.send_async(Err(status)).await;
                    break;
                }
            }
        }

        let outcome = match &mut stream_state {
            // Structurally unreachable: the Start check above either
            // transitions to Running or breaks out of the loop.
            RegistrationStreamState::Start => {
                let _ = tx
                    .send_async(Err(Status::internal(
                        "registration stream still in Start after initialization",
                    )))
                    .await;
                break;
            }
            RegistrationStreamState::Running(running) => {
                if let Some(status) = validate_running_update(&running.running.identity, &update) {
                    let _ = tx.send_async(Err(status)).await;
                    break;
                }
                apply_registration_update(
                    running.as_mut(),
                    &state,
                    &update,
                    &registration_connection_config,
                )
                .await
            }
        };

        match outcome {
            Ok(ApplyUpdateOutcome::Ack(ack)) => {
                if tx.send_async(Ok(ack)).await.is_err() {
                    break;
                }
            }
            Ok(ApplyUpdateOutcome::Skip) => {}
            Err(status) => {
                let _ = tx.send_async(Err(status)).await;
                break;
            }
        }
    }

    cleanup_registration_stream(stream_state, &state).await;
}

fn negotiated_registration_update_idle_timeout(
    metadata: &tonic::metadata::MetadataMap,
    configured_idle_timeout: Duration,
    configured_max_idle_timeout: Duration,
) -> Option<Duration> {
    if configured_idle_timeout.is_zero() || configured_max_idle_timeout.is_zero() {
        return None;
    }
    let Some(heartbeat_ms) = metadata.get(REGISTRATION_HEARTBEAT_MS_METADATA) else {
        return Some(configured_max_idle_timeout);
    };
    let Ok(heartbeat_ms) = heartbeat_ms.to_str() else {
        warn!(
            "{REGISTRATION_HEARTBEAT_MS_METADATA} must be ascii milliseconds; using configured registration max idle timeout"
        );
        return Some(configured_max_idle_timeout);
    };
    let Ok(heartbeat_ms) = heartbeat_ms.parse::<u64>() else {
        warn!(
            "{REGISTRATION_HEARTBEAT_MS_METADATA} must be integer milliseconds; using configured registration max idle timeout"
        );
        return Some(configured_max_idle_timeout);
    };
    // Untrusted heartbeat metadata must not overflow timeout math; cap through saturation before
    // applying the configured maximum.
    let negotiated_timeout = heartbeat_ms.saturating_mul(3);
    Some(
        Duration::from_millis(negotiated_timeout)
            .max(configured_idle_timeout)
            .min(configured_max_idle_timeout),
    )
}

async fn start_registration_stream(
    update: &InferenceServerRegistration,
    state: &Arc<StargateState>,
    registration_connection_config: &RegistrationConnectionConfig,
    routing_key: Option<&str>,
) -> Result<StreamRegistration, Status> {
    if update.inference_server_id.is_empty() {
        warn!("inference_server_id is empty; denying registration");
        return Err(Status::invalid_argument("inference_server_id is empty"));
    }

    if update.inference_server_url.is_empty() {
        warn!(
            inference_server_id = %update.inference_server_id,
            "inference_server_url is empty; denying registration"
        );
        return Err(Status::invalid_argument("inference_server_url is empty"));
    }

    let url_validation = if update.reverse_tunnel {
        validate_reverse_tunnel_inference_server_url(&update.inference_server_url)
    } else {
        validate_inference_server_url(&update.inference_server_url)
    };
    if let Err(error) = url_validation {
        warn!(
            inference_server_id = %update.inference_server_id,
            inference_server_url = %update.inference_server_url,
            reverse_tunnel = update.reverse_tunnel,
            error = %error,
            "invalid inference_server_url; denying registration"
        );
        return Err(Status::invalid_argument(format!(
            "invalid inference_server_url: {error}"
        )));
    }

    if update.reverse_tunnel
        && registration_connection_config
            .reverse_tunnel_target
            .is_none()
    {
        warn!(
            inference_server_id = %update.inference_server_id,
            "reverse tunnel flag set but no reverse tunnel config; denying registration"
        );
        return Err(Status::invalid_argument(
            "reverse tunnel flag set but no reverse tunnel config",
        ));
    }

    let identity = RegistrationIdentity {
        inference_server_id: update.inference_server_id.clone(),
        cluster_id: effective_cluster_id(update),
        inference_server_url: update.inference_server_url.clone(),
        routing_key: routing_key.map(ToOwned::to_owned),
        reverse_tunnel: update.reverse_tunnel,
        coordinated_calibration: update.coordinated_calibration,
    };

    let running = state.begin_registration(&identity).await?;

    let reverse_tunnel_watcher = Some(ConnectionWatcher::new(
        registration_connection_config.quic_proxy.clone(),
        registration_connection_config.reverse_tunnel_connect_timeout,
    ));

    Ok(StreamRegistration {
        running,
        reverse_tunnel_watcher,
        health_check: None,
    })
}

fn validate_running_update(
    identity: &RegistrationIdentity,
    update: &InferenceServerRegistration,
) -> Option<Status> {
    if update.reverse_tunnel != identity.reverse_tunnel {
        warn!(
            inference_server_id = %update.inference_server_id,
            reverse_tunnel = %update.reverse_tunnel,
            "reverse tunnel flag changed; denying registration"
        );
        return Some(Status::invalid_argument("reverse tunnel flag changed"));
    }
    if update.inference_server_url != identity.inference_server_url {
        warn!(
            inference_server_id = %update.inference_server_id,
            inference_server_url = %update.inference_server_url,
            "inference_server_url changed; denying registration"
        );
        return Some(Status::invalid_argument("inference_server_url changed"));
    }
    if update.inference_server_id != identity.inference_server_id {
        warn!(
            inference_server_id = %update.inference_server_id,
            "inference_server_id changed; denying registration"
        );
        return Some(Status::invalid_argument("inference_server_id changed"));
    }
    if effective_cluster_id(update) != identity.cluster_id {
        warn!(
            inference_server_id = %update.inference_server_id,
            cluster_id = %update.cluster_id,
            "cluster_id changed; denying registration"
        );
        return Some(Status::invalid_argument("cluster_id changed"));
    }
    if update.coordinated_calibration != identity.coordinated_calibration {
        warn!(
            inference_server_id = %update.inference_server_id,
            coordinated_calibration = update.coordinated_calibration,
            "coordinated_calibration changed; denying registration"
        );
        return Some(Status::invalid_argument("coordinated_calibration changed"));
    }
    None
}

fn effective_cluster_id(update: &InferenceServerRegistration) -> String {
    if update.cluster_id.is_empty() {
        update.inference_server_id.clone()
    } else {
        update.cluster_id.clone()
    }
}

async fn apply_registration_update(
    running: &mut StreamRegistration,
    state: &Arc<StargateState>,
    update: &InferenceServerRegistration,
    registration_connection_config: &RegistrationConnectionConfig,
) -> Result<ApplyUpdateOutcome, Status> {
    let reverse_connected = if let Some(watcher) = &mut running.reverse_tunnel_watcher {
        match watcher
            .ensure_connected(
                &running.running.identity.inference_server_id,
                &running.running.identity.inference_server_url,
                running.running.identity.reverse_tunnel,
            )
            .await
        {
            EnsureConnectedResult::Connected => true,
            EnsureConnectedResult::ReverseDisconnected => false,
            EnsureConnectedResult::Unavailable => {
                // Keep the no-ack retry behavior while clearing any stale route
                // that depended on the now-unavailable direct connection.
                state
                    .apply_registration_update(&mut running.running, update, false, None)
                    .await;
                return Ok(ApplyUpdateOutcome::Skip);
            }
        }
    } else {
        true
    };
    ensure_health_check_started(running, registration_connection_config);

    let rtt = if let Some(handle) = &mut running.health_check {
        let status = {
            let current = handle.rx.borrow_and_update();
            current.clone()
        };
        match status {
            HealthCheckStatus::Ready(rtt) => Some(rtt),
            HealthCheckStatus::Pending => registration_connection_config
                .quic_proxy
                .health_check_rtt(&running.running.identity.inference_server_id)
                .await
                .ok(),
        }
    } else {
        None
    };

    let model_calibration_directives = state
        .apply_registration_update(&mut running.running, update, reverse_connected, rtt)
        .await;

    Ok(ApplyUpdateOutcome::Ack(build_registration_ack(
        registration_connection_config,
        model_calibration_directives,
    )))
}

fn build_registration_ack(
    registration_connection_config: &RegistrationConnectionConfig,
    model_calibration_directives: Vec<ModelCalibrationDirective>,
) -> InferenceServerAck {
    InferenceServerAck {
        reverse_tunnel_target: registration_connection_config
            .reverse_tunnel_target
            .clone()
            .unwrap_or_default(),
        reverse_tunnel_pylon_dial_addr: registration_connection_config
            .reverse_tunnel_pylon_dial_addr
            .clone()
            .unwrap_or_default(),
        model_calibration_directives,
    }
}

async fn cleanup_registration_stream(
    stream_state: RegistrationStreamState,
    state: &Arc<StargateState>,
) {
    if let RegistrationStreamState::Running(running) = stream_state {
        if let Some(health_check) = running.health_check {
            health_check.shutdown().await;
        }
        state
            .end_registration(&running.running.identity.inference_server_id)
            .await;
    } else {
        debug!("register inference servers stream exited after empty stream");
    }
}

fn validate_inference_server_url(url: &str) -> Result<(), anyhow::Error> {
    use anyhow::Context;
    let parsed = Url::parse(url).context("inference_server_url must be a valid URL")?;
    if parsed.scheme() != "quic" {
        anyhow::bail!("inference_server_url scheme must be quic");
    }
    if parsed.host_str().is_none() {
        anyhow::bail!("inference_server_url must include host");
    }
    if parsed
        .host_str()
        .and_then(|host| host.parse::<IpAddr>().ok())
        .is_none()
    {
        anyhow::bail!("inference_server_url host must be an IP address");
    }
    if parsed.port_or_known_default().is_none() {
        anyhow::bail!("inference_server_url must include port");
    }
    Ok(())
}

fn validate_reverse_tunnel_inference_server_url(url: &str) -> Result<(), anyhow::Error> {
    use anyhow::Context;
    let parsed = Url::parse(url).context("inference_server_url must be a valid URL")?;
    if !matches!(parsed.scheme(), "http" | "https") {
        anyhow::bail!("inference_server_url scheme must be http or https");
    }
    if parsed.host_str().is_none() {
        anyhow::bail!("inference_server_url must include host");
    }
    if parsed.port_or_known_default().is_none() {
        anyhow::bail!("inference_server_url must include port");
    }
    Ok(())
}

#[derive(Debug)]
struct NormalizedListModelsRequest {
    routing_key: Option<String>,
    model_ids: Vec<String>,
}

fn normalize_list_models_request(
    request: ListModelsRequest,
) -> Result<NormalizedListModelsRequest, &'static str> {
    let ListModelsRequest {
        routing_key,
        model_ids,
    } = request;
    let routing_key = routing_key
        .as_deref()
        .map(str::trim)
        .filter(|routing_key| !routing_key.is_empty())
        .map(ToOwned::to_owned);

    let mut normalized_model_ids = Vec::with_capacity(model_ids.len());
    for model_id in model_ids {
        let model_id = model_id.trim();
        if model_id.is_empty() {
            return Err("model_ids must not contain empty values");
        }
        normalized_model_ids.push(model_id.to_string());
    }

    Ok(NormalizedListModelsRequest {
        routing_key,
        model_ids: normalized_model_ids,
    })
}

fn ensure_health_check_started(
    running: &mut StreamRegistration,
    registration_connection_config: &RegistrationConnectionConfig,
) {
    if running.health_check.is_some() {
        return;
    }

    let inference_server_id = running.running.identity.inference_server_id.clone();
    let quic_proxy = registration_connection_config.quic_proxy.clone();
    let stop = CancellationToken::new();
    let task_stop = stop.clone();
    let (tx, rx) = watch::channel(HealthCheckStatus::Pending);
    let task = tokio::spawn(async move {
        loop {
            let status = tokio::select! {
                _ = task_stop.cancelled() => break,
                status = sample_health_check_status(&quic_proxy, &inference_server_id) => status,
            };
            let _ = tx.send_replace(status);
            tokio::select! {
                _ = task_stop.cancelled() => break,
                _ = tokio::time::sleep(HEALTH_CHECK_INTERVAL) => {}
            }
        }
    });

    running.health_check = Some(HealthCheckHandle {
        stop,
        task: Some(task),
        rx,
    });
}

async fn sample_health_check_status(
    quic_proxy: &QuicHttpProxy,
    inference_server_id: &str,
) -> HealthCheckStatus {
    if quic_proxy.has_healthy_connection(inference_server_id).await {
        match quic_proxy.health_check_rtt(inference_server_id).await {
            Ok(rtt) => HealthCheckStatus::Ready(rtt),
            Err(error) => {
                warn!(
                    inference_server_id = %inference_server_id,
                    error = %error,
                    "health check failed"
                );
                HealthCheckStatus::Pending
            }
        }
    } else {
        HealthCheckStatus::Pending
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    use crate::auth::{AuthResult, OpenAuthenticator};
    use crate::routing_state::RegistrationIdentity;
    use crate::tunnel::QuicTunnelConfig;
    use stargate_proto::pb::{
        InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
        ModelStats, StargateInfo,
    };

    #[test]
    fn validate_inference_server_url_rejects_http() {
        let result = validate_inference_server_url("http://10.0.0.1:8080");
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains("scheme must be quic"), "got: {msg}");
    }

    #[test]
    fn validate_inference_server_url_rejects_missing_port() {
        let result = validate_inference_server_url("quic://10.0.0.1");
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains("must include port"), "got: {msg}");
    }

    #[test]
    fn validate_inference_server_url_accepts_ip_host() {
        validate_inference_server_url("quic://10.0.0.1:8080")
            .expect("direct quic URL with IP host and port should be valid");
    }

    #[test]
    fn validate_inference_server_url_rejects_hostname_host() {
        let result = validate_inference_server_url("quic://backend.default.svc:8080");
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains("host must be an IP address"), "got: {msg}");
    }

    #[test]
    fn validate_inference_server_url_rejects_garbage() {
        let result = validate_inference_server_url("not a url at all");
        assert!(result.is_err());
    }

    #[test]
    fn normalize_list_models_request_trims_model_filters() {
        let request = normalize_list_models_request(ListModelsRequest {
            routing_key: None,
            model_ids: vec![" model-a ".to_string(), "model-b".to_string()],
        })
        .expect("valid filters should normalize");

        assert_eq!(request.model_ids, vec!["model-a", "model-b"]);
        assert_eq!(request.routing_key, None);
    }

    #[test]
    fn normalize_list_models_request_trims_routing_key() {
        let request = normalize_list_models_request(ListModelsRequest {
            routing_key: Some(" rk-a ".to_string()),
            model_ids: Vec::new(),
        })
        .expect("valid routing key should normalize");

        assert_eq!(request.routing_key.as_deref(), Some("rk-a"));
    }

    #[test]
    fn normalize_list_models_request_treats_blank_routing_key_as_none() {
        let request = normalize_list_models_request(ListModelsRequest {
            routing_key: Some(" ".to_string()),
            model_ids: Vec::new(),
        })
        .expect("blank routing key should normalize to unscoped");

        assert_eq!(request.routing_key, None);
    }

    #[test]
    fn normalize_list_models_request_allows_empty_filter() {
        let request = normalize_list_models_request(ListModelsRequest {
            routing_key: None,
            model_ids: Vec::new(),
        })
        .expect("empty model filter should request all models");

        assert!(request.model_ids.is_empty());
    }

    #[test]
    fn normalize_list_models_request_rejects_blank_model_filter() {
        let error = normalize_list_models_request(ListModelsRequest {
            routing_key: None,
            model_ids: vec![" ".to_string()],
        })
        .expect_err("blank model filter should be rejected");

        assert_eq!(error, "model_ids must not contain empty values");
    }

    #[tokio::test]
    async fn registration_message_stream_reports_inbound_stream_errors() {
        let (messages, mut errors) = registration_message_stream(futures::stream::iter([Err(
            Status::cancelled("client cancelled registration stream"),
        )]));
        futures::pin_mut!(messages);

        assert!(
            messages.next().await.is_none(),
            "stream errors must not be converted into registration messages"
        );
        let error = errors
            .recv()
            .await
            .expect("inbound stream error should be retained for response termination");
        assert_eq!(error.code(), tonic::Code::Cancelled);
        assert_eq!(error.message(), "client cancelled registration stream");
    }

    #[tokio::test]
    async fn pending_registration_stream_error_returns_buffered_error() {
        let (tx, mut rx) = mpsc::channel(1);
        tx.send(Status::cancelled("buffered registration stream error"))
            .await
            .expect("receiver should be alive");

        let error = pending_registration_stream_error(&mut rx)
            .await
            .expect("buffered stream error should be returned");

        assert_eq!(error.code(), tonic::Code::Cancelled);
        assert_eq!(error.message(), "buffered registration stream error");
    }

    #[tokio::test]
    async fn pending_registration_stream_error_waits_for_delayed_error() {
        let (tx, mut rx) = mpsc::channel(1);
        tokio::spawn(async move {
            tokio::task::yield_now().await;
            tx.send(Status::internal("delayed registration stream error"))
                .await
                .expect("receiver should be alive");
        });

        let error = tokio::time::timeout(
            Duration::from_secs(1),
            pending_registration_stream_error(&mut rx),
        )
        .await
        .expect("pending stream error should arrive")
        .expect("delayed stream error should be returned");

        assert_eq!(error.code(), tonic::Code::Internal);
        assert_eq!(error.message(), "delayed registration stream error");
    }

    #[tokio::test]
    async fn pending_registration_stream_error_returns_none_after_disconnect() {
        let (tx, mut rx) = mpsc::channel(1);
        drop(tx);

        let error = pending_registration_stream_error(&mut rx).await;

        assert!(error.is_none());
    }

    fn make_identity() -> RegistrationIdentity {
        RegistrationIdentity {
            inference_server_id: "server-1".to_string(),
            cluster_id: "server-1".to_string(),
            inference_server_url: "quic://10.0.0.1:8080".to_string(),
            routing_key: None,
            reverse_tunnel: false,
            coordinated_calibration: false,
        }
    }

    fn make_update(id: &str, url: &str, reverse_tunnel: bool) -> InferenceServerRegistration {
        InferenceServerRegistration {
            inference_server_id: id.to_string(),
            cluster_id: String::new(),
            inference_server_url: url.to_string(),
            reverse_tunnel,
            models: Default::default(),
            coordinated_calibration: false,
        }
    }

    fn test_registration_connection_config() -> RegistrationConnectionConfig {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        RegistrationConnectionConfig {
            quic_proxy: Arc::new(
                QuicHttpProxy::new(
                    QuicTunnelConfig {
                        connect_timeout: Duration::from_millis(10),
                        request_timeout: Duration::from_millis(10),
                        direct_quic_connections: 1,
                        tls_cert_pem: None,
                        server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                        quic_insecure: true,
                        tunnel_protocol: Default::default(),
                    },
                    Arc::new(OpenAuthenticator),
                )
                .expect("quic proxy should initialize"),
            ),
            reverse_tunnel_connect_timeout: Duration::from_millis(10),
            reverse_tunnel_target: None,
            reverse_tunnel_pylon_dial_addr: None,
        }
    }

    fn test_reverse_tunnel_registration_connection_config() -> RegistrationConnectionConfig {
        RegistrationConnectionConfig {
            reverse_tunnel_target: Some("127.0.0.1:50071".to_string()),
            ..test_registration_connection_config()
        }
    }

    #[tokio::test]
    async fn registration_ack_includes_reverse_tunnel_target_and_pylon_dial_addr() {
        let config = RegistrationConnectionConfig {
            reverse_tunnel_target: Some(
                "stargate-0.stargate-headless.stargate.svc.cluster.local:50072".to_string(),
            ),
            reverse_tunnel_pylon_dial_addr: Some(
                "stargate-quic-lb.stargate.svc.cluster.local:50072".to_string(),
            ),
            ..test_registration_connection_config()
        };

        let ack = build_registration_ack(&config, Vec::new());

        assert_eq!(
            ack.reverse_tunnel_target,
            "stargate-0.stargate-headless.stargate.svc.cluster.local:50072"
        );
        assert_eq!(
            ack.reverse_tunnel_pylon_dial_addr,
            "stargate-quic-lb.stargate.svc.cluster.local:50072"
        );
    }

    #[tokio::test]
    async fn start_registration_stream_rejects_reverse_tunnel_empty_url() {
        let state = Arc::new(StargateState::default());
        let update = make_update("server-1", "", true);

        let status = match start_registration_stream(
            &update,
            &state,
            &test_reverse_tunnel_registration_connection_config(),
            None,
        )
        .await
        {
            Ok(running) => {
                state
                    .end_registration(&running.running.identity.inference_server_id)
                    .await;
                panic!("reverse-tunnel registration with empty URL should be rejected");
            }
            Err(status) => status,
        };

        assert_eq!(status.code(), tonic::Code::InvalidArgument);
        assert!(
            status.message().contains("inference_server_url is empty"),
            "got: {status}"
        );
    }

    #[tokio::test]
    async fn start_registration_stream_rejects_reverse_tunnel_non_http_url() {
        let state = Arc::new(StargateState::default());
        let update = make_update("server-1", "quic://10.0.0.1:8080", true);

        let status = match start_registration_stream(
            &update,
            &state,
            &test_reverse_tunnel_registration_connection_config(),
            None,
        )
        .await
        {
            Ok(running) => {
                state
                    .end_registration(&running.running.identity.inference_server_id)
                    .await;
                panic!("reverse-tunnel registration with non-HTTP URL should be rejected");
            }
            Err(status) => status,
        };

        assert_eq!(status.code(), tonic::Code::InvalidArgument);
        assert!(
            status
                .message()
                .contains("inference_server_url scheme must be http or https"),
            "got: {status}"
        );
    }

    #[test]
    fn watch_stargates_response_sorts_and_dedupes_local_and_remote_entries() {
        let remote_watch_urls = normalize_remote_watch_urls(
            vec![
                "remote-b.stargate:50071".to_string(),
                "remote-a.stargate:50071".to_string(),
                "remote-b.stargate:50071".to_string(),
            ],
            &BTreeSet::new(),
        );
        let response = build_watch_stargates_response(
            vec![
                StargateInfo {
                    stargate_id: "stargate-1".to_string(),
                    advertise_addr: "10.0.0.2:50071".to_string(),
                    http_advertise_addr: "10.0.0.2:8000".to_string(),
                    grpc_pylon_dial_addr: String::new(),
                },
                StargateInfo {
                    stargate_id: "stargate-0".to_string(),
                    advertise_addr: "10.0.0.1:50071".to_string(),
                    http_advertise_addr: "10.0.0.1:8000".to_string(),
                    grpc_pylon_dial_addr: String::new(),
                },
                StargateInfo {
                    stargate_id: "stargate-1".to_string(),
                    advertise_addr: "10.0.0.2:50071".to_string(),
                    http_advertise_addr: "10.0.0.2:8000".to_string(),
                    grpc_pylon_dial_addr: String::new(),
                },
            ],
            &remote_watch_urls,
            None,
        );

        let ids: Vec<&str> = response
            .stargates
            .iter()
            .map(|info| info.stargate_id.as_str())
            .collect();
        assert_eq!(ids, vec!["stargate-0", "stargate-1"]);
        assert_eq!(
            response.watch_stargate_urls,
            vec!["remote-a.stargate:50071", "remote-b.stargate:50071"]
        );
    }

    #[test]
    fn watch_stargates_response_dedupes_empty_id_by_advertise_addr() {
        let response = build_watch_stargates_response(
            vec![
                StargateInfo {
                    stargate_id: String::new(),
                    advertise_addr: "10.0.0.1:50071".to_string(),
                    http_advertise_addr: "10.0.0.1:8000".to_string(),
                    grpc_pylon_dial_addr: String::new(),
                },
                StargateInfo {
                    stargate_id: "stargate-0".to_string(),
                    advertise_addr: "10.0.0.1:50071".to_string(),
                    http_advertise_addr: "10.0.0.1:8000".to_string(),
                    grpc_pylon_dial_addr: String::new(),
                },
            ],
            &[],
            None,
        );

        assert_eq!(response.stargates.len(), 1);
        assert_eq!(response.stargates[0].stargate_id, "stargate-0");
        assert_eq!(response.stargates[0].advertise_addr, "10.0.0.1:50071");
    }

    #[test]
    fn watch_stargates_response_applies_configured_grpc_pylon_dial_addr() {
        let response = build_watch_stargates_response(
            vec![StargateInfo {
                stargate_id: "stargate-0".to_string(),
                advertise_addr: "stargate-0.region-a:50071".to_string(),
                http_advertise_addr: String::new(),
                grpc_pylon_dial_addr: String::new(),
            }],
            &[],
            Some(" stargate-grpc-lb.region-a:443 "),
        );

        assert_eq!(response.stargates.len(), 1);
        assert_eq!(
            response.stargates[0].advertise_addr,
            "stargate-0.region-a:50071"
        );
        assert_eq!(
            response.stargates[0].grpc_pylon_dial_addr,
            "stargate-grpc-lb.region-a:443"
        );
    }

    #[test]
    fn remote_watch_urls_are_normalized_and_filter_self_endpoints() {
        let excluded = local_watch_endpoint_keys(
            "10.0.0.1:50071".parse().unwrap(),
            "stargate-headless.ns.svc.cluster.local",
        );
        let urls = normalize_remote_watch_urls(
            vec![
                " remote-b:50071 ".to_string(),
                "remote-a:50071".to_string(),
                "remote-b:50071".to_string(),
                String::new(),
                "10.0.0.1:50071".to_string(),
                "http://10.0.0.1:50071".to_string(),
                "stargate-headless.ns.svc.cluster.local:50071".to_string(),
                "stargate.ns.svc.cluster.local:50071".to_string(),
            ],
            &excluded,
        );

        assert_eq!(urls, vec!["remote-a:50071", "remote-b:50071"]);
    }

    #[test]
    fn watch_response_has_entries_only_when_local_or_remote_targets_exist() {
        assert!(!watch_response_has_entries(
            &WatchStargatesResponse::default()
        ));
        assert!(watch_response_has_entries(&WatchStargatesResponse {
            stargates: vec![StargateInfo {
                stargate_id: "stargate-0".to_string(),
                advertise_addr: "10.0.0.1:50071".to_string(),
                http_advertise_addr: "10.0.0.1:8000".to_string(),
                grpc_pylon_dial_addr: String::new(),
            }],
            watch_stargate_urls: Vec::new(),
        }));
        assert!(watch_response_has_entries(&WatchStargatesResponse {
            stargates: Vec::new(),
            watch_stargate_urls: vec!["remote-a:50071".to_string()],
        }));
    }

    #[tokio::test]
    async fn watch_stargates_stream_marks_initial_snapshot_seen() {
        let (tx, rx) = watch::channel(WatchStargatesResponse::default());
        let first = WatchStargatesResponse {
            stargates: vec![StargateInfo {
                stargate_id: "stargate-0".to_string(),
                advertise_addr: "10.0.0.1:50071".to_string(),
                http_advertise_addr: "10.0.0.1:8000".to_string(),
                grpc_pylon_dial_addr: String::new(),
            }],
            watch_stargate_urls: Vec::new(),
        };
        tx.send(first.clone()).expect("receiver should be alive");
        let mut out = Box::pin(watch_stargates_stream_from_receiver(rx));

        assert_eq!(out.next().await.unwrap().unwrap(), first);

        let waker = futures::task::noop_waker_ref();
        let mut cx = std::task::Context::from_waker(waker);
        assert!(matches!(
            out.as_mut().poll_next(&mut cx),
            std::task::Poll::Pending
        ));

        let second = WatchStargatesResponse {
            stargates: vec![StargateInfo {
                stargate_id: "stargate-1".to_string(),
                advertise_addr: "10.0.0.2:50071".to_string(),
                http_advertise_addr: "10.0.0.2:8000".to_string(),
                grpc_pylon_dial_addr: String::new(),
            }],
            watch_stargate_urls: Vec::new(),
        };
        tx.send(second.clone()).expect("receiver should be alive");

        assert_eq!(out.next().await.unwrap().unwrap(), second);
    }

    #[tokio::test]
    async fn registration_stream_idle_timeout_removes_routable_model() {
        let state = Arc::new(StargateState::default());
        let identity = make_identity();
        let mut running = state.begin_registration(&identity).await.unwrap();
        let update = InferenceServerRegistration {
            inference_server_id: identity.inference_server_id.clone(),
            cluster_id: String::new(),
            inference_server_url: identity.inference_server_url.clone(),
            reverse_tunnel: false,
            coordinated_calibration: false,
            models: HashMap::from([(
                "model-idle".to_string(),
                InferenceServerModelRegistration {
                    stats: Some(ModelStats::default()),
                    status: InferenceServerStatus::Active as i32,
                },
            )]),
        };
        state
            .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
            .await;

        let target = crate::routing_state::RoutingTargetKey {
            routing_key: None,
            model_id: "model-idle".to_string(),
        };
        assert_eq!(state.candidates_for_target(&target).await.len(), 1);

        let stream = futures::stream::pending::<Result<InferenceServerRegistration, Status>>();
        let (tx, _rx) = flume::bounded(1);
        tokio::time::timeout(
            Duration::from_secs(2),
            process_registration_stream_with_state(
                stream,
                state.clone(),
                test_registration_connection_config(),
                tx,
                AuthResult { routing_key: None },
                Some(Duration::from_millis(1)),
                RegistrationStreamState::Running(Box::new(StreamRegistration {
                    running,
                    reverse_tunnel_watcher: None,
                    health_check: None,
                })),
            ),
        )
        .await
        .expect("registration processor should exit after idle timeout");

        assert!(state.candidates_for_target(&target).await.is_empty());
    }

    #[tokio::test]
    async fn registration_stream_skips_update_when_direct_connection_unavailable() {
        let state = Arc::new(StargateState::default());
        let update = InferenceServerRegistration {
            inference_server_id: "unavailable-direct".to_string(),
            cluster_id: String::new(),
            inference_server_url: "quic://127.0.0.1:1".to_string(),
            reverse_tunnel: false,
            coordinated_calibration: false,
            models: HashMap::from([(
                "model-unavailable".to_string(),
                InferenceServerModelRegistration {
                    stats: Some(ModelStats::default()),
                    status: InferenceServerStatus::Active as i32,
                },
            )]),
        };
        let target = crate::routing_state::RoutingTargetKey {
            routing_key: None,
            model_id: "model-unavailable".to_string(),
        };
        let stream = futures::stream::iter([Ok(update)]);
        let (tx, rx) = flume::bounded(1);

        process_registration_stream_with_state(
            stream,
            state.clone(),
            test_registration_connection_config(),
            tx,
            AuthResult { routing_key: None },
            None,
            RegistrationStreamState::Start,
        )
        .await;

        assert!(matches!(
            rx.try_recv(),
            Err(flume::TryRecvError::Disconnected)
        ));
        assert!(state.candidates_for_target(&target).await.is_empty());
    }

    #[tokio::test]
    async fn unavailable_direct_update_removes_existing_route_before_stream_cleanup() {
        let state = Arc::new(StargateState::default());
        let identity = RegistrationIdentity {
            inference_server_id: "lost-direct".to_string(),
            cluster_id: "lost-direct".to_string(),
            inference_server_url: "quic://127.0.0.1:1".to_string(),
            routing_key: None,
            reverse_tunnel: false,
            coordinated_calibration: false,
        };
        let mut running = state.begin_registration(&identity).await.unwrap();
        let update = InferenceServerRegistration {
            inference_server_id: identity.inference_server_id.clone(),
            cluster_id: identity.cluster_id.clone(),
            inference_server_url: identity.inference_server_url.clone(),
            reverse_tunnel: false,
            coordinated_calibration: false,
            models: HashMap::from([(
                "model-lost-direct".to_string(),
                InferenceServerModelRegistration {
                    stats: Some(ModelStats::default()),
                    status: InferenceServerStatus::Active as i32,
                },
            )]),
        };
        let target = crate::routing_state::RoutingTargetKey {
            routing_key: None,
            model_id: "model-lost-direct".to_string(),
        };
        state
            .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
            .await;
        assert_eq!(state.candidates_for_target(&target).await.len(), 1);

        let config = test_registration_connection_config();
        let watcher = ConnectionWatcher::new(
            config.quic_proxy.clone(),
            config.reverse_tunnel_connect_timeout,
        );
        let stream = futures::stream::iter([Ok(update)]).chain(futures::stream::pending());
        let (tx, rx) = flume::bounded(1);
        let task = tokio::spawn(process_registration_stream_with_state(
            stream,
            state.clone(),
            config,
            tx,
            AuthResult { routing_key: None },
            None,
            RegistrationStreamState::Running(Box::new(StreamRegistration {
                running,
                reverse_tunnel_watcher: Some(watcher),
                health_check: None,
            })),
        ));

        tokio::time::timeout(Duration::from_secs(1), async {
            loop {
                if state.candidates_for_target(&target).await.is_empty() {
                    break;
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("unavailable direct connection should clear stale route");
        assert!(matches!(rx.try_recv(), Err(flume::TryRecvError::Empty)));

        task.abort();
        let _ = task.await;
    }

    #[tokio::test]
    async fn health_check_shutdown_cancels_interval_wait() {
        let state = Arc::new(StargateState::default());
        let identity = make_identity();
        let running = state
            .begin_registration(&identity)
            .await
            .expect("test registration should start");
        let mut registration = StreamRegistration {
            running,
            reverse_tunnel_watcher: None,
            health_check: None,
        };
        ensure_health_check_started(&mut registration, &test_registration_connection_config());

        let mut health_check = registration
            .health_check
            .take()
            .expect("health check should start");
        tokio::time::timeout(Duration::from_secs(1), health_check.rx.changed())
            .await
            .expect("health check should publish initial pending status")
            .expect("health check sender should remain open");

        tokio::time::timeout(Duration::from_millis(200), health_check.shutdown())
            .await
            .expect("health check shutdown should not wait for the full interval");
    }

    #[tokio::test]
    async fn registration_stream_with_disabled_idle_timeout_preserves_pending_stream() {
        let state = Arc::new(StargateState::default());
        let identity = make_identity();
        let mut running = state.begin_registration(&identity).await.unwrap();
        let update = InferenceServerRegistration {
            inference_server_id: identity.inference_server_id.clone(),
            cluster_id: String::new(),
            inference_server_url: identity.inference_server_url.clone(),
            reverse_tunnel: false,
            coordinated_calibration: false,
            models: HashMap::from([(
                "model-idle-timeout".to_string(),
                InferenceServerModelRegistration {
                    stats: Some(ModelStats::default()),
                    status: InferenceServerStatus::Active as i32,
                },
            )]),
        };
        state
            .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
            .await;

        let target = crate::routing_state::RoutingTargetKey {
            routing_key: None,
            model_id: "model-idle-timeout".to_string(),
        };
        assert_eq!(state.candidates_for_target(&target).await.len(), 1);

        let (polled_tx, polled_rx) = tokio::sync::oneshot::channel();
        let mut polled_tx = Some(polled_tx);
        let stream = futures::stream::poll_fn(move |_cx| {
            if let Some(polled_tx) = polled_tx.take() {
                let _ = polled_tx.send(());
            }
            std::task::Poll::<Option<Result<InferenceServerRegistration, Status>>>::Pending
        });
        let (tx, _rx) = flume::bounded(1);
        let processor = tokio::spawn(process_registration_stream_with_state(
            stream,
            state.clone(),
            test_registration_connection_config(),
            tx,
            AuthResult { routing_key: None },
            None,
            RegistrationStreamState::Running(Box::new(StreamRegistration {
                running,
                reverse_tunnel_watcher: None,
                health_check: None,
            })),
        ));
        tokio::time::timeout(Duration::from_secs(1), polled_rx)
            .await
            .expect("registration processor should poll the stream")
            .expect("poll marker sender should be alive");

        assert!(!processor.is_finished());
        assert_eq!(state.candidates_for_target(&target).await.len(), 1);

        processor.abort();
        let _ = processor.await;
    }

    #[test]
    fn registration_idle_timeout_is_negotiated_from_heartbeat_metadata() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(
            REGISTRATION_HEARTBEAT_MS_METADATA,
            "120000".parse().unwrap(),
        );

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::from_secs(600),
        );

        assert_eq!(timeout, Some(Duration::from_secs(360)));
    }

    #[test]
    fn registration_idle_timeout_uses_configured_floor() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(REGISTRATION_HEARTBEAT_MS_METADATA, "1000".parse().unwrap());

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::from_secs(600),
        );

        assert_eq!(timeout, Some(Duration::from_secs(60)));
    }

    #[test]
    fn registration_idle_timeout_zero_heartbeat_uses_configured_floor() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(REGISTRATION_HEARTBEAT_MS_METADATA, "0".parse().unwrap());

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::from_secs(300),
        );

        assert_eq!(timeout, Some(Duration::from_secs(60)));
    }

    #[test]
    fn registration_idle_timeout_uses_configured_cap_for_large_heartbeat() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(
            REGISTRATION_HEARTBEAT_MS_METADATA,
            "120000".parse().unwrap(),
        );

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::from_secs(300),
        );

        assert_eq!(timeout, Some(Duration::from_secs(300)));
    }

    #[test]
    fn registration_idle_timeout_uses_configured_cap_without_heartbeat_metadata() {
        let metadata = tonic::metadata::MetadataMap::new();

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::from_secs(300),
        );

        assert_eq!(timeout, Some(Duration::from_secs(300)));
    }

    #[test]
    fn registration_idle_timeout_honors_configured_cap_below_floor() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(REGISTRATION_HEARTBEAT_MS_METADATA, "1000".parse().unwrap());

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::from_secs(10),
        );

        assert_eq!(timeout, Some(Duration::from_secs(10)));

        let timeout = negotiated_registration_update_idle_timeout(
            &tonic::metadata::MetadataMap::new(),
            Duration::from_secs(60),
            Duration::from_secs(10),
        );

        assert_eq!(timeout, Some(Duration::from_secs(10)));
    }

    #[test]
    fn registration_idle_timeout_can_be_disabled() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(REGISTRATION_HEARTBEAT_MS_METADATA, "1000".parse().unwrap());

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::ZERO,
            Duration::from_secs(300),
        );

        assert_eq!(timeout, None);
    }

    #[test]
    fn registration_idle_timeout_max_zero_disables_enforcement() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(REGISTRATION_HEARTBEAT_MS_METADATA, "1000".parse().unwrap());

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::ZERO,
        );

        assert_eq!(timeout, None);
    }

    #[test]
    fn malformed_registration_heartbeat_metadata_uses_configured_cap() {
        let mut metadata = tonic::metadata::MetadataMap::new();
        metadata.insert(
            REGISTRATION_HEARTBEAT_MS_METADATA,
            "not-a-number".parse().unwrap(),
        );

        let timeout = negotiated_registration_update_idle_timeout(
            &metadata,
            Duration::from_secs(60),
            Duration::from_secs(300),
        );

        assert_eq!(timeout, Some(Duration::from_secs(300)));
    }

    #[test]
    fn validate_running_update_rejects_changed_url() {
        let identity = make_identity();
        let update = make_update("server-1", "quic://10.0.0.2:9090", false);
        let status = validate_running_update(&identity, &update);
        assert!(status.is_some());
        assert_eq!(status.unwrap().code(), tonic::Code::InvalidArgument);
    }

    #[test]
    fn validate_running_update_rejects_changed_id() {
        let identity = make_identity();
        let update = make_update("server-2", "quic://10.0.0.1:8080", false);
        let status = validate_running_update(&identity, &update);
        assert!(status.is_some());
        assert_eq!(status.unwrap().code(), tonic::Code::InvalidArgument);
    }

    #[test]
    fn validate_running_update_rejects_toggled_reverse_tunnel() {
        let identity = make_identity();
        let update = make_update("server-1", "quic://10.0.0.1:8080", true);
        let status = validate_running_update(&identity, &update);
        assert!(status.is_some());
        assert_eq!(status.unwrap().code(), tonic::Code::InvalidArgument);
    }
}
