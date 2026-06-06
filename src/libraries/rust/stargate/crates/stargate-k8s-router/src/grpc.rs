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

use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use futures::{Stream, StreamExt};
use stargate_forwarding::HostnameMatcher;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_control_plane_server::{
    StargateControlPlane, StargateControlPlaneServer,
};
use stargate_proto::pb::{
    InferenceServerAck, InferenceServerRegistration, SubmitClusterCalibrationRequest,
    SubmitClusterCalibrationResponse, WatchStargatesRequest, WatchStargatesResponse,
};
use tokio::net::TcpListener;
use tokio::sync::watch;
use tokio_stream::wrappers::TcpListenerStream;
use tokio_util::sync::CancellationToken;
use tonic::transport::Server;
use tonic::{Request, Response, Status};
use tower::util::MapRequestLayer;
use tracing::{info, warn};

use crate::endpoints::{PodTarget, TargetSnapshot};

type WatchStargatesStream =
    Pin<Box<dyn Stream<Item = Result<WatchStargatesResponse, Status>> + Send + 'static>>;
type RegisterInferenceServerStream =
    Pin<Box<dyn Stream<Item = Result<InferenceServerAck, Status>> + Send + 'static>>;
type RegistrationStreamErrorRx = tokio::sync::mpsc::Receiver<Status>;

enum WatchTarget<'a> {
    Ready(&'a PodTarget),
    Unavailable(String),
}

enum RegistrationTarget<'a> {
    Ready(&'a PodTarget),
    InvalidAuthority(String),
    Unavailable(String),
}

struct GrpcTarget {
    pod_name: String,
    grpc_addr: String,
}

impl From<&PodTarget> for GrpcTarget {
    fn from(target: &PodTarget) -> Self {
        Self {
            pod_name: target.pod_name.clone(),
            grpc_addr: target.grpc_addr.clone(),
        }
    }
}

#[derive(Clone, Debug)]
pub struct GrpcRouterConfig {
    pub advertised_hostname_template: String,
    pub target_namespace: String,
    pub connect_timeout: Duration,
}

#[derive(Clone)]
pub struct RouterControlPlane {
    config: Arc<GrpcRouterConfig>,
    hostname_matcher: Option<HostnameMatcher>,
    targets: watch::Receiver<TargetSnapshot>,
    round_robin: Arc<AtomicUsize>,
}

impl RouterControlPlane {
    pub fn new(config: GrpcRouterConfig, targets: watch::Receiver<TargetSnapshot>) -> Self {
        let hostname_matcher = HostnameMatcher::new(
            &config.advertised_hostname_template,
            &config.target_namespace,
        );
        Self {
            config: Arc::new(config),
            hostname_matcher,
            targets,
            round_robin: Arc::new(AtomicUsize::new(0)),
        }
    }

    fn pod_from_authority<'a, T>(&self, request: &'a Request<T>) -> Option<&'a str> {
        let host = request
            .extensions()
            .get::<http::uri::Authority>()
            .map(|authority| authority.host())?;
        self.hostname_matcher.as_ref()?.extract_pod(host)
    }

    fn target_for_watch<'a, T>(
        &self,
        request: &Request<T>,
        snapshot: &'a TargetSnapshot,
    ) -> WatchTarget<'a> {
        if let Some(pod_name) = self.pod_from_authority(request) {
            return match snapshot.target_for_pod_ref(pod_name) {
                Some(target) => WatchTarget::Ready(target),
                None => {
                    WatchTarget::Unavailable(format!("target stargate {pod_name} is not ready"))
                }
            };
        }

        let offset = self.round_robin.fetch_add(1, Ordering::Relaxed);
        match snapshot.first_ready_ref(offset) {
            Some(target) => WatchTarget::Ready(target),
            None => WatchTarget::Unavailable("no ready stargate targets".to_string()),
        }
    }

    fn target_for_registration<'a, T>(
        &self,
        request: &Request<T>,
        snapshot: &'a TargetSnapshot,
    ) -> RegistrationTarget<'a> {
        let Some(authority) = request
            .extensions()
            .get::<http::uri::Authority>()
            .map(|authority| authority.host())
        else {
            return RegistrationTarget::InvalidAuthority(
                "registration requires a target stargate authority".to_string(),
            );
        };

        let Some(pod_name) = self
            .hostname_matcher
            .as_ref()
            .and_then(|matcher| matcher.extract_pod(authority))
        else {
            return RegistrationTarget::InvalidAuthority(format!(
                "registration authority {authority} does not match advertised stargate hostname template"
            ));
        };

        match snapshot.target_for_pod_ref(pod_name) {
            Some(target) => RegistrationTarget::Ready(target),
            None => {
                RegistrationTarget::Unavailable(format!("target stargate {pod_name} is not ready"))
            }
        }
    }

    async fn connect_target_addr(
        &self,
        grpc_addr: &str,
    ) -> Result<StargateControlPlaneClient<tonic::transport::Channel>, Status> {
        let endpoint = format!("http://{grpc_addr}");
        let channel = tonic::transport::Channel::from_shared(endpoint)
            .map_err(|e| Status::internal(format!("invalid stargate target address: {e}")))?;
        let channel = tokio::time::timeout(self.config.connect_timeout, channel.connect())
            .await
            .map_err(|_| Status::unavailable("timed out connecting to stargate target"))?
            .map_err(|e| {
                Status::unavailable(format!("failed to connect to stargate target: {e}"))
            })?;
        Ok(StargateControlPlaneClient::new(channel))
    }
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
    let (stream_error_tx, stream_error_rx) = tokio::sync::mpsc::channel(1);
    let messages = inbound.filter_map(move |result| {
        let stream_error_tx = stream_error_tx.clone();
        async move {
            match result {
                Ok(message) => Some(message),
                Err(error) => {
                    warn!(%error, "registration stream read error, forwarding stream error");
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
        Err(tokio::sync::mpsc::error::TryRecvError::Empty) => stream_error_rx.recv().await,
        Err(tokio::sync::mpsc::error::TryRecvError::Disconnected) => None,
    }
}

#[tonic::async_trait]
impl StargateControlPlane for RouterControlPlane {
    type WatchStargatesStream = WatchStargatesStream;
    type RegisterInferenceServerStream = RegisterInferenceServerStream;

    async fn watch_stargates(
        &self,
        request: Request<WatchStargatesRequest>,
    ) -> Result<Response<Self::WatchStargatesStream>, Status> {
        let target = {
            let snapshot = self.targets.borrow();
            match self.target_for_watch(&request, &snapshot) {
                WatchTarget::Ready(target) => GrpcTarget::from(target),
                WatchTarget::Unavailable(message) => return Err(Status::unavailable(message)),
            }
        };
        info!(
            target_pod = %target.pod_name,
            target_addr = %target.grpc_addr,
            "forwarding WatchStargates to stargate target"
        );

        let metadata = request.metadata().clone();
        let mut peer_client = self.connect_target_addr(&target.grpc_addr).await?;
        let mut forwarded = Request::new(request.into_inner());
        *forwarded.metadata_mut() = metadata;
        let resp = peer_client.watch_stargates(forwarded).await?;
        let mut inner = resp.into_inner();
        let stream = async_stream::stream! {
            let _client = peer_client;
            while let Some(msg) = inner.message().await.transpose() {
                yield msg;
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }

    async fn register_inference_server(
        &self,
        request: Request<tonic::Streaming<InferenceServerRegistration>>,
    ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
        let target = {
            let snapshot = self.targets.borrow();
            match self.target_for_registration(&request, &snapshot) {
                RegistrationTarget::Ready(target) => GrpcTarget::from(target),
                RegistrationTarget::InvalidAuthority(message) => {
                    return Err(Status::invalid_argument(message));
                }
                RegistrationTarget::Unavailable(message) => {
                    return Err(Status::unavailable(message));
                }
            }
        };
        info!(
            target_pod = %target.pod_name,
            target_addr = %target.grpc_addr,
            "forwarding RegisterInferenceServer to stargate target"
        );

        let metadata = request.metadata().clone();
        let (inbound, mut stream_error_rx) = registration_message_stream(request.into_inner());
        let mut forwarded = Request::new(inbound);
        *forwarded.metadata_mut() = metadata;
        let mut peer_client = self.connect_target_addr(&target.grpc_addr).await?;
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
        Ok(Response::new(Box::pin(stream)))
    }

    async fn submit_cluster_calibration(
        &self,
        request: Request<SubmitClusterCalibrationRequest>,
    ) -> Result<Response<SubmitClusterCalibrationResponse>, Status> {
        let target = {
            let snapshot = self.targets.borrow();
            match self.target_for_registration(&request, &snapshot) {
                RegistrationTarget::Ready(target) => GrpcTarget::from(target),
                RegistrationTarget::InvalidAuthority(message) => {
                    return Err(Status::invalid_argument(message));
                }
                RegistrationTarget::Unavailable(message) => {
                    return Err(Status::unavailable(message));
                }
            }
        };
        info!(
            target_pod = %target.pod_name,
            target_addr = %target.grpc_addr,
            "forwarding SubmitClusterCalibration to stargate target"
        );

        let metadata = request.metadata().clone();
        let mut peer_client = self.connect_target_addr(&target.grpc_addr).await?;
        let mut forwarded = Request::new(request.into_inner());
        *forwarded.metadata_mut() = metadata;
        peer_client.submit_cluster_calibration(forwarded).await
    }
}

pub async fn serve_grpc_router(
    listener: TcpListener,
    config: GrpcRouterConfig,
    targets: watch::Receiver<TargetSnapshot>,
    shutdown: CancellationToken,
) -> anyhow::Result<()> {
    let incoming = TcpListenerStream::new(listener);
    let service = RouterControlPlane::new(config, targets);
    let authority_layer = MapRequestLayer::new(|mut req: http::Request<_>| {
        if let Some(authority) = req.uri().authority().cloned() {
            req.extensions_mut().insert(authority);
        }
        req
    });
    Server::builder()
        .layer(authority_layer)
        .add_service(StargateControlPlaneServer::new(service))
        .serve_with_incoming_shutdown(incoming, async move {
            shutdown.cancelled().await;
        })
        .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::hint::black_box;
    use std::net::SocketAddr;
    use std::sync::Mutex;
    use std::time::Instant;

    use crate::perf_tests::assert_twenty_percent_faster;
    use hyper_util::rt::TokioIo;
    use stargate_proto::REGISTRATION_HEARTBEAT_MS_METADATA;
    use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
    use stargate_proto::pb::stargate_control_plane_server::StargateControlPlaneServer;
    use stargate_proto::pb::{
        CalibrationState, InferenceServerAck, StargateInfo, SubmitClusterCalibrationRequest,
        SubmitClusterCalibrationResponse,
    };
    use tokio_stream::wrappers::TcpListenerStream;

    use super::*;

    type MetadataRecords = Arc<Mutex<Vec<(Option<String>, Option<String>)>>>;

    #[derive(Clone, Default)]
    struct Recorder {
        watch_hits: Arc<AtomicUsize>,
        register_hits: Arc<AtomicUsize>,
        submit_hits: Arc<AtomicUsize>,
        metadata: MetadataRecords,
        submissions: Arc<Mutex<Vec<SubmitClusterCalibrationRequest>>>,
        registration_errors: Arc<Mutex<Vec<(tonic::Code, String)>>>,
    }

    #[derive(Clone)]
    struct FakeStargate {
        stargate_id: String,
        recorder: Recorder,
    }

    #[tonic::async_trait]
    impl StargateControlPlane for FakeStargate {
        type WatchStargatesStream = WatchStargatesStream;
        type RegisterInferenceServerStream = RegisterInferenceServerStream;

        async fn watch_stargates(
            &self,
            _request: Request<WatchStargatesRequest>,
        ) -> Result<Response<Self::WatchStargatesStream>, Status> {
            self.recorder.watch_hits.fetch_add(1, Ordering::Relaxed);
            let response = WatchStargatesResponse {
                stargates: vec![StargateInfo {
                    stargate_id: self.stargate_id.clone(),
                    advertise_addr: format!("{}.stargate.external:50071", self.stargate_id),
                    http_advertise_addr: String::new(),
                    grpc_pylon_dial_addr: String::new(),
                }],
                watch_stargate_urls: vec![],
            };
            Ok(Response::new(Box::pin(futures::stream::iter([Ok(
                response,
            )]))))
        }

        async fn register_inference_server(
            &self,
            request: Request<tonic::Streaming<InferenceServerRegistration>>,
        ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
            self.recorder.register_hits.fetch_add(1, Ordering::Relaxed);
            let auth = request
                .metadata()
                .get("authorization")
                .and_then(|value| value.to_str().ok())
                .map(str::to_string);
            let heartbeat = request
                .metadata()
                .get(REGISTRATION_HEARTBEAT_MS_METADATA)
                .and_then(|value| value.to_str().ok())
                .map(str::to_string);
            self.recorder
                .metadata
                .lock()
                .expect("metadata lock poisoned")
                .push((auth, heartbeat));

            let stargate_id = self.stargate_id.clone();
            let recorder = self.recorder.clone();
            let mut inbound = request.into_inner();
            let stream = async_stream::stream! {
                if let Some(message) = inbound.next().await {
                    match message {
                        Ok(_registration) => {
                            yield Ok(InferenceServerAck {
                                reverse_tunnel_target: stargate_id,
                                reverse_tunnel_pylon_dial_addr: String::new(),
                                model_calibration_directives: vec![],
                            });
                        }
                        Err(error) => {
                            recorder
                                .registration_errors
                                .lock()
                                .expect("registration errors lock poisoned")
                                .push((error.code(), error.message().to_string()));
                            yield Err(error);
                        }
                    }
                }
            };
            Ok(Response::new(Box::pin(stream)))
        }

        async fn submit_cluster_calibration(
            &self,
            request: Request<SubmitClusterCalibrationRequest>,
        ) -> Result<Response<SubmitClusterCalibrationResponse>, Status> {
            self.recorder.submit_hits.fetch_add(1, Ordering::Relaxed);
            let auth = request
                .metadata()
                .get("authorization")
                .and_then(|value| value.to_str().ok())
                .map(str::to_string);
            self.recorder
                .metadata
                .lock()
                .expect("metadata lock poisoned")
                .push((auth, None));
            self.recorder
                .submissions
                .lock()
                .expect("submissions lock poisoned")
                .push(request.into_inner());
            Ok(Response::new(SubmitClusterCalibrationResponse {
                state: CalibrationState::Complete as i32,
            }))
        }
    }

    async fn start_fake_stargate(
        stargate_id: &str,
        recorder: Recorder,
    ) -> (SocketAddr, tokio::task::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind fake stargate");
        let addr = listener.local_addr().expect("fake stargate local addr");
        let service = FakeStargate {
            stargate_id: stargate_id.to_string(),
            recorder,
        };
        let handle = tokio::spawn(async move {
            let result = Server::builder()
                .add_service(StargateControlPlaneServer::new(service))
                .serve_with_incoming(TcpListenerStream::new(listener))
                .await;
            if let Err(error) = result {
                panic!("fake stargate failed: {error}");
            }
        });
        (addr, handle)
    }

    async fn start_router(
        snapshot: TargetSnapshot,
    ) -> (SocketAddr, CancellationToken, tokio::task::JoinHandle<()>) {
        let (_tx, rx) = watch::channel(snapshot);
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind router");
        let addr = listener.local_addr().expect("router local addr");
        let shutdown = CancellationToken::new();
        let handle = tokio::spawn({
            let shutdown = shutdown.clone();
            async move {
                serve_grpc_router(
                    listener,
                    GrpcRouterConfig {
                        advertised_hostname_template: "{pod_name}.stargate.external".to_string(),
                        target_namespace: String::new(),
                        connect_timeout: Duration::from_secs(2),
                    },
                    rx,
                    shutdown,
                )
                .await
                .expect("router failed");
            }
        });
        (addr, shutdown, handle)
    }

    fn snapshot(targets: &[(&str, SocketAddr)]) -> TargetSnapshot {
        TargetSnapshot::initialized(
            targets
                .iter()
                .map(|(pod, addr)| {
                    (
                        (*pod).to_string(),
                        PodTarget {
                            pod_name: (*pod).to_string(),
                            grpc_addr: addr.to_string(),
                            quic_addr: "127.0.0.1:50072".to_string(),
                        },
                    )
                })
                .collect::<BTreeMap<_, _>>(),
        )
    }

    fn synthetic_snapshot(count: usize) -> TargetSnapshot {
        TargetSnapshot::initialized(
            (0..count)
                .map(|index| {
                    let pod_name = format!("stargate-{index}");
                    (
                        pod_name.clone(),
                        PodTarget {
                            pod_name,
                            grpc_addr: format!("10.0.0.{index}:50071"),
                            quic_addr: format!("10.0.0.{index}:50072"),
                        },
                    )
                })
                .collect::<BTreeMap<_, _>>(),
        )
    }

    fn request_with_authority(host: &str) -> Request<()> {
        let authority: http::uri::Authority = format!("{host}:50071")
            .parse()
            .expect("test authority should parse");
        let mut request = Request::new(());
        request.extensions_mut().insert(authority);
        request
    }

    async fn connect_with_authority(
        actual_addr: SocketAddr,
        authority_host: &str,
        authority_port: u16,
    ) -> StargateControlPlaneClient<tonic::transport::Channel> {
        let connector = tower::service_fn(move |_uri: http::Uri| async move {
            let stream = tokio::net::TcpStream::connect(actual_addr).await?;
            Ok::<_, std::io::Error>(TokioIo::new(stream))
        });
        let channel = tonic::transport::Endpoint::from_shared(format!(
            "http://{authority_host}:{authority_port}"
        ))
        .expect("authority endpoint")
        .connect_with_connector_lazy(connector);
        StargateControlPlaneClient::new(channel)
    }

    fn registration() -> InferenceServerRegistration {
        InferenceServerRegistration {
            inference_server_id: "backend-1".to_string(),
            inference_server_url: "http://127.0.0.1:8080".to_string(),
            models: Default::default(),
            reverse_tunnel: false,
            cluster_id: String::new(),
            coordinated_calibration: false,
        }
    }

    fn calibration_submission() -> SubmitClusterCalibrationRequest {
        SubmitClusterCalibrationRequest {
            inference_server_id: "backend-1".to_string(),
            cluster_id: "cluster-1".to_string(),
            model_id: "model-1".to_string(),
            assignment_token: calibration_assignment(1),
            measured_last_mean_input_tps: 123.0,
        }
    }

    fn calibration_assignment(id: u32) -> String {
        format!("assignment-{id}")
    }

    #[test]
    #[ignore = "performance benchmark; run with --ignored --nocapture"]
    fn bench_grpc_registration_target_by_authority() {
        const BASELINE_NS_PER_OP: f64 = 276.71;

        let (_tx, rx) = watch::channel(synthetic_snapshot(128));
        let router = RouterControlPlane::new(
            GrpcRouterConfig {
                advertised_hostname_template: "{pod_name}.stargate.external".to_string(),
                target_namespace: String::new(),
                connect_timeout: Duration::from_secs(2),
            },
            rx,
        );
        let request = request_with_authority("stargate-64.stargate.external");
        let iterations = 1_000_000usize;
        let started = Instant::now();
        let mut checksum = 0usize;

        for _ in 0..iterations {
            let snapshot = router.targets.borrow();
            match black_box(&router)
                .target_for_registration(black_box(&request), black_box(&snapshot))
            {
                RegistrationTarget::Ready(target) => {
                    checksum = checksum.wrapping_add(target.grpc_addr.len());
                }
                RegistrationTarget::InvalidAuthority(message)
                | RegistrationTarget::Unavailable(message) => {
                    panic!("unexpected target resolution failure: {message}");
                }
            }
        }

        let elapsed = started.elapsed();
        let ns_per_op = elapsed.as_nanos() as f64 / iterations as f64;
        eprintln!(
            "bench_grpc_registration_target_by_authority: iterations={iterations} elapsed={elapsed:?} ns_per_op={ns_per_op:.2} checksum={checksum}"
        );
        assert!(checksum > 0);
        assert_twenty_percent_faster(
            "bench_grpc_registration_target_by_authority",
            BASELINE_NS_PER_OP,
            ns_per_op,
        );
    }

    #[tokio::test]
    async fn watch_stargates_with_target_authority_routes_to_that_target() {
        let recorder_a = Recorder::default();
        let recorder_b = Recorder::default();
        let (addr_a, fake_a) = start_fake_stargate("stargate-0", recorder_a.clone()).await;
        let (addr_b, fake_b) = start_fake_stargate("stargate-1", recorder_b.clone()).await;
        let (router_addr, shutdown, router) =
            start_router(snapshot(&[("stargate-0", addr_a), ("stargate-1", addr_b)])).await;

        let mut client =
            connect_with_authority(router_addr, "stargate-1.stargate.external", 50071).await;
        let response = client
            .watch_stargates(WatchStargatesRequest {})
            .await
            .expect("watch should route");
        let first = response
            .into_inner()
            .message()
            .await
            .expect("stream read should succeed")
            .expect("stream should yield response");

        assert_eq!(first.stargates[0].stargate_id, "stargate-1");
        assert_eq!(recorder_a.watch_hits.load(Ordering::Relaxed), 0);
        assert_eq!(recorder_b.watch_hits.load(Ordering::Relaxed), 1);

        shutdown.cancel();
        router.abort();
        fake_a.abort();
        fake_b.abort();
    }

    #[tokio::test]
    async fn watch_stargates_without_target_authority_uses_ready_round_robin_target() {
        let recorder_a = Recorder::default();
        let (addr_a, fake_a) = start_fake_stargate("stargate-0", recorder_a.clone()).await;
        let (router_addr, shutdown, router) =
            start_router(snapshot(&[("stargate-0", addr_a)])).await;

        let mut client = connect_with_authority(
            router_addr,
            "stargate.stargate-local.svc.cluster.local",
            50071,
        )
        .await;
        let response = client
            .watch_stargates(WatchStargatesRequest {})
            .await
            .expect("watch should route");
        let first = response
            .into_inner()
            .message()
            .await
            .expect("stream read should succeed")
            .expect("stream should yield response");

        assert_eq!(first.stargates[0].stargate_id, "stargate-0");
        assert_eq!(recorder_a.watch_hits.load(Ordering::Relaxed), 1);

        shutdown.cancel();
        router.abort();
        fake_a.abort();
    }

    #[tokio::test]
    async fn watch_stargates_without_target_authority_round_robins_ready_targets() {
        let recorder_a = Recorder::default();
        let recorder_b = Recorder::default();
        let (addr_a, fake_a) = start_fake_stargate("stargate-0", recorder_a.clone()).await;
        let (addr_b, fake_b) = start_fake_stargate("stargate-1", recorder_b.clone()).await;
        let (router_addr, shutdown, router) =
            start_router(snapshot(&[("stargate-0", addr_a), ("stargate-1", addr_b)])).await;

        let mut client = connect_with_authority(
            router_addr,
            "stargate.stargate-local.svc.cluster.local",
            50071,
        )
        .await;
        for _ in 0..2 {
            let response = client
                .watch_stargates(WatchStargatesRequest {})
                .await
                .expect("watch should route");
            let _ = response
                .into_inner()
                .message()
                .await
                .expect("stream read should succeed")
                .expect("stream should yield response");
        }

        assert_eq!(recorder_a.watch_hits.load(Ordering::Relaxed), 1);
        assert_eq!(recorder_b.watch_hits.load(Ordering::Relaxed), 1);

        shutdown.cancel();
        router.abort();
        fake_a.abort();
        fake_b.abort();
    }

    #[tokio::test]
    async fn registration_authority_matches_namespace_hostname_template() {
        let recorder = Recorder::default();
        let (addr, fake) = start_fake_stargate("stargate-1", recorder.clone()).await;
        let (_tx, rx) = watch::channel(snapshot(&[("stargate-1", addr)]));
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind router");
        let router_addr = listener.local_addr().expect("router local addr");
        let shutdown = CancellationToken::new();
        let router = tokio::spawn({
            let shutdown = shutdown.clone();
            async move {
                serve_grpc_router(
                    listener,
                    GrpcRouterConfig {
                        advertised_hostname_template: "{pod_name}.{namespace}.stargate.external"
                            .to_string(),
                        target_namespace: "prod".to_string(),
                        connect_timeout: Duration::from_secs(2),
                    },
                    rx,
                    shutdown,
                )
                .await
                .expect("router failed");
            }
        });

        let mut client =
            connect_with_authority(router_addr, "stargate-1.prod.stargate.external", 50071).await;
        let response = client
            .register_inference_server(Request::new(tokio_stream::iter([registration()])))
            .await
            .expect("registration should route");
        let ack = response
            .into_inner()
            .message()
            .await
            .expect("stream read should succeed")
            .expect("stream should yield ack");

        assert_eq!(ack.reverse_tunnel_target, "stargate-1");
        assert_eq!(recorder.register_hits.load(Ordering::Relaxed), 1);

        shutdown.cancel();
        router.abort();
        fake.abort();
    }

    #[tokio::test]
    async fn registration_with_target_authority_forwards_stream_and_metadata() {
        let recorder_b = Recorder::default();
        let (addr_b, fake_b) = start_fake_stargate("stargate-1", recorder_b.clone()).await;
        let (router_addr, shutdown, router) =
            start_router(snapshot(&[("stargate-1", addr_b)])).await;

        let mut client =
            connect_with_authority(router_addr, "stargate-1.stargate.external", 50071).await;
        let mut request = Request::new(tokio_stream::iter([registration()]));
        request.metadata_mut().insert(
            "authorization",
            "Bearer token".parse().expect("valid metadata"),
        );
        request.metadata_mut().insert(
            REGISTRATION_HEARTBEAT_MS_METADATA,
            "1000".parse().expect("valid metadata"),
        );

        let response = client
            .register_inference_server(request)
            .await
            .expect("registration should route");
        let ack = response
            .into_inner()
            .message()
            .await
            .expect("stream read should succeed")
            .expect("stream should yield ack");

        assert_eq!(ack.reverse_tunnel_target, "stargate-1");
        assert_eq!(recorder_b.register_hits.load(Ordering::Relaxed), 1);
        assert_eq!(
            recorder_b
                .metadata
                .lock()
                .expect("metadata lock poisoned")
                .as_slice(),
            &[(Some("Bearer token".to_string()), Some("1000".to_string()))]
        );

        shutdown.cancel();
        router.abort();
        fake_b.abort();
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
    async fn calibration_submission_with_target_authority_forwards_request_and_metadata() {
        let recorder_b = Recorder::default();
        let (addr_b, fake_b) = start_fake_stargate("stargate-1", recorder_b.clone()).await;
        let (router_addr, shutdown, router) =
            start_router(snapshot(&[("stargate-1", addr_b)])).await;

        let mut client =
            connect_with_authority(router_addr, "stargate-1.stargate.external", 50071).await;
        let mut request = Request::new(calibration_submission());
        request.metadata_mut().insert(
            "authorization",
            "Bearer calibration-token".parse().expect("valid metadata"),
        );
        let response = client
            .submit_cluster_calibration(request)
            .await
            .expect("calibration submission should route")
            .into_inner();

        assert_eq!(response.state, CalibrationState::Complete as i32);
        assert_eq!(recorder_b.submit_hits.load(Ordering::Relaxed), 1);
        assert_eq!(
            recorder_b
                .metadata
                .lock()
                .expect("metadata lock poisoned")
                .as_slice(),
            &[(Some("Bearer calibration-token".to_string()), None)]
        );
        assert_eq!(
            recorder_b
                .submissions
                .lock()
                .expect("submissions lock poisoned")
                .as_slice(),
            &[calibration_submission()]
        );

        shutdown.cancel();
        router.abort();
        fake_b.abort();
    }

    #[tokio::test]
    async fn registration_rejects_service_authority_without_target_pod() {
        let recorder_a = Recorder::default();
        let (addr_a, fake_a) = start_fake_stargate("stargate-0", recorder_a.clone()).await;
        let (router_addr, shutdown, router) =
            start_router(snapshot(&[("stargate-0", addr_a)])).await;

        let mut client = connect_with_authority(
            router_addr,
            "stargate.stargate-local.svc.cluster.local",
            50071,
        )
        .await;
        let error = client
            .register_inference_server(Request::new(tokio_stream::iter([registration()])))
            .await
            .expect_err("service authority should be rejected");

        assert_eq!(error.code(), tonic::Code::InvalidArgument);
        assert_eq!(recorder_a.register_hits.load(Ordering::Relaxed), 0);

        shutdown.cancel();
        router.abort();
        fake_a.abort();
    }

    #[tokio::test]
    async fn registration_returns_unavailable_for_unready_target_pod() {
        let (router_addr, shutdown, router) =
            start_router(TargetSnapshot::initialized(BTreeMap::new())).await;

        let mut client =
            connect_with_authority(router_addr, "stargate-9.stargate.external", 50071).await;
        let error = client
            .register_inference_server(Request::new(tokio_stream::iter([registration()])))
            .await
            .expect_err("missing target should be unavailable");

        assert_eq!(error.code(), tonic::Code::Unavailable);

        shutdown.cancel();
        router.abort();
    }

    #[tokio::test]
    async fn watch_stargates_returns_unavailable_for_unready_target_pod() {
        let (router_addr, shutdown, router) =
            start_router(TargetSnapshot::initialized(BTreeMap::new())).await;

        let mut client =
            connect_with_authority(router_addr, "stargate-9.stargate.external", 50071).await;
        let error = client
            .watch_stargates(WatchStargatesRequest {})
            .await
            .expect_err("missing target should be unavailable");

        assert_eq!(error.code(), tonic::Code::Unavailable);

        shutdown.cancel();
        router.abort();
    }
}
