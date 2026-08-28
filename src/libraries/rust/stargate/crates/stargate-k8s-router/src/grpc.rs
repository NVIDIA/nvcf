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
use std::time::Duration;

use futures::Stream;
use stargate_forwarding::{HostnameMatcher, forward_stream_messages, render_hostname};
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_control_plane_server::{
    StargateControlPlane, StargateControlPlaneServer,
};
use stargate_proto::pb::{
    InferenceServerAck, InferenceServerRegistration, StargateInfo, WatchStargatesRequest,
    WatchStargatesResponse,
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
    pub advertised_grpc_port: u16,
    pub grpc_pylon_dial_addr: String,
    pub target_namespace: String,
    pub connect_timeout: Duration,
    pub watch_heartbeat_interval: Duration,
}

#[derive(Clone)]
pub struct RouterControlPlane {
    connect_timeout: Duration,
    advertised_hostname_template: String,
    advertised_grpc_port: u16,
    grpc_pylon_dial_addr: String,
    target_namespace: String,
    watch_heartbeat_interval: Duration,
    hostname_matcher: Option<HostnameMatcher>,
    targets: watch::Receiver<TargetSnapshot>,
}

impl RouterControlPlane {
    pub fn new(config: GrpcRouterConfig, targets: watch::Receiver<TargetSnapshot>) -> Self {
        let hostname_matcher = HostnameMatcher::new(
            &config.advertised_hostname_template,
            &config.target_namespace,
        );
        Self {
            connect_timeout: config.connect_timeout,
            advertised_hostname_template: config.advertised_hostname_template,
            advertised_grpc_port: config.advertised_grpc_port,
            grpc_pylon_dial_addr: config.grpc_pylon_dial_addr,
            target_namespace: config.target_namespace,
            watch_heartbeat_interval: config.watch_heartbeat_interval,
            hostname_matcher,
            targets,
        }
    }

    fn watch_stargates_stream(&self) -> WatchStargatesStream {
        let mut targets = self.targets.clone();
        let advertised_hostname_template = self.advertised_hostname_template.clone();
        let advertised_grpc_port = self.advertised_grpc_port;
        let grpc_pylon_dial_addr = self.grpc_pylon_dial_addr.clone();
        let target_namespace = self.target_namespace.clone();
        let watch_heartbeat_interval = self.watch_heartbeat_interval;
        Box::pin(async_stream::try_stream! {
            let mut heartbeat = tokio::time::interval(watch_heartbeat_interval);
            heartbeat.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
            let mut last_snapshot = None;
            loop {
                let snapshot = targets.borrow_and_update().clone();
                if snapshot.is_initialized() {
                    last_snapshot = Some(snapshot.clone());
                    heartbeat.reset();
                    yield watch_response_from_snapshot(
                        &snapshot,
                        &advertised_hostname_template,
                        advertised_grpc_port,
                        &grpc_pylon_dial_addr,
                        &target_namespace,
                    );
                }
                loop {
                    tokio::select! {
                        changed = targets.changed() => {
                            if changed.is_err() {
                                return;
                            }
                            break;
                        }
                        _ = heartbeat.tick(), if last_snapshot.is_some() => {
                            let snapshot = last_snapshot
                                .as_ref()
                                .expect("heartbeat branch requires an initialized snapshot");
                            heartbeat.reset();
                            yield watch_response_from_snapshot(
                                snapshot,
                                &advertised_hostname_template,
                                advertised_grpc_port,
                                &grpc_pylon_dial_addr,
                                &target_namespace,
                            );
                        }
                    }
                }
            }
        })
    }

    fn target_for_registration<'a, T>(
        &self,
        request: &Request<T>,
        snapshot: &'a TargetSnapshot,
    ) -> Result<&'a PodTarget, Status> {
        let authority = request
            .extensions()
            .get::<http::uri::Authority>()
            .map(|authority| authority.host())
            .ok_or_else(|| {
                Status::invalid_argument("registration requires a target stargate authority")
            })?;

        let pod_name = self
            .hostname_matcher
            .as_ref()
            .and_then(|matcher| matcher.extract_pod(authority))
            .ok_or_else(|| {
                Status::invalid_argument(format!(
                    "registration authority {authority} does not match advertised stargate hostname template"
                ))
            })?;

        snapshot
            .target_for_pod_ref(pod_name)
            .ok_or_else(|| Status::unavailable(format!("target stargate {pod_name} is not ready")))
    }

    async fn connect_target_addr(
        &self,
        grpc_addr: &str,
    ) -> Result<StargateControlPlaneClient<tonic::transport::Channel>, Status> {
        let channel = tonic::transport::Channel::from_shared(format!("http://{grpc_addr}"))
            .map_err(|e| Status::internal(format!("invalid stargate target address: {e}")))?;
        let channel = tokio::time::timeout(self.connect_timeout, channel.connect())
            .await
            .map_err(|_| Status::unavailable("timed out connecting to stargate target"))?
            .map_err(|e| {
                Status::unavailable(format!("failed to connect to stargate target: {e}"))
            })?;
        Ok(StargateControlPlaneClient::new(channel))
    }
}

#[tonic::async_trait]
impl StargateControlPlane for RouterControlPlane {
    type WatchStargatesStream = WatchStargatesStream;
    type RegisterInferenceServerStream = RegisterInferenceServerStream;

    async fn watch_stargates(
        &self,
        _request: Request<WatchStargatesRequest>,
    ) -> Result<Response<Self::WatchStargatesStream>, Status> {
        Ok(Response::new(self.watch_stargates_stream()))
    }

    async fn register_inference_server(
        &self,
        request: Request<tonic::Streaming<InferenceServerRegistration>>,
    ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
        let target = {
            let snapshot = self.targets.borrow();
            GrpcTarget::from(self.target_for_registration(&request, &snapshot)?)
        };
        info!(
            target_pod = %target.pod_name,
            target_addr = %target.grpc_addr,
            "forwarding RegisterInferenceServer to stargate target"
        );

        let (metadata, extensions, inbound) = request.into_parts();
        let (inbound, mut stream_error_rx) = forward_stream_messages(inbound, |error| {
            warn!(%error, "registration stream read error, forwarding stream error");
        });
        let forwarded = Request::from_parts(metadata, extensions, inbound);
        let mut peer_client = self.connect_target_addr(&target.grpc_addr).await?;
        let resp = peer_client.register_inference_server(forwarded).await?;
        let (metadata, mut inner, extensions) = resp.into_parts();
        let stream = async_stream::stream! {
            loop {
                tokio::select! {
                    Some(error) = stream_error_rx.recv() => {
                        yield Err(error);
                        break;
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
        Ok(Response::from_parts(metadata, Box::pin(stream), extensions))
    }
}

fn watch_response_from_snapshot(
    snapshot: &TargetSnapshot,
    advertised_hostname_template: &str,
    advertised_grpc_port: u16,
    grpc_pylon_dial_addr: &str,
    target_namespace: &str,
) -> WatchStargatesResponse {
    let stargates = snapshot
        .ready_targets()
        .unwrap_or_default()
        .iter()
        .map(|target| {
            let hostname = render_hostname(
                advertised_hostname_template,
                &target.pod_name,
                target_namespace,
            );
            StargateInfo {
                stargate_id: target.pod_name.clone(),
                advertise_addr: format!("{hostname}:{advertised_grpc_port}"),
                http_advertise_addr: String::new(),
                grpc_pylon_dial_addr: grpc_pylon_dial_addr.to_string(),
            }
        })
        .collect();
    WatchStargatesResponse {
        stargates,
        watch_stargate_urls: Vec::new(),
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
    Server::builder()
        .layer(MapRequestLayer::new(|mut req: http::Request<_>| {
            if let Some(authority) = req.uri().authority().cloned() {
                req.extensions_mut().insert(authority);
            }
            req
        }))
        .add_service(StargateControlPlaneServer::new(service))
        .serve_with_incoming_shutdown(incoming, async move {
            shutdown.cancelled().await;
        })
        .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::hint::black_box;
    use std::net::SocketAddr;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::{Arc, Mutex};
    use std::time::Instant;

    use crate::perf_tests::assert_twenty_percent_faster;
    use futures::StreamExt;
    use hyper_util::rt::TokioIo;
    use stargate_proto::REGISTRATION_HEARTBEAT_MS_METADATA;
    use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
    use stargate_proto::pb::stargate_control_plane_server::StargateControlPlaneServer;
    use stargate_proto::pb::{InferenceServerAck, StargateInfo};
    use tokio_stream::wrappers::TcpListenerStream;

    use super::*;

    type MetadataRecords = Arc<Mutex<Vec<(Option<String>, Option<String>)>>>;

    #[derive(Clone, Default)]
    struct Recorder {
        watch_hits: Arc<AtomicUsize>,
        register_hits: Arc<AtomicUsize>,
        metadata: MetadataRecords,
        registration_errors: Arc<Mutex<Vec<(tonic::Code, String)>>>,
    }

    #[derive(Clone)]
    struct FakeStargate {
        stargate_id: String,
        recorder: Recorder,
    }

    struct RunningServer {
        addr: SocketAddr,
        task: tokio::task::JoinHandle<()>,
    }

    impl Drop for RunningServer {
        fn drop(&mut self) {
            self.task.abort();
        }
    }

    impl RunningServer {
        fn client(
            &self,
            authority_host: &str,
        ) -> StargateControlPlaneClient<tonic::transport::Channel> {
            let actual_addr = self.addr;
            let connector = tower::service_fn(move |_uri: http::Uri| async move {
                let stream = tokio::net::TcpStream::connect(actual_addr).await?;
                Ok::<_, std::io::Error>(TokioIo::new(stream))
            });
            let channel =
                tonic::transport::Endpoint::from_shared(format!("http://{authority_host}:50071"))
                    .expect("authority endpoint")
                    .connect_with_connector_lazy(connector);
            StargateControlPlaneClient::new(channel)
        }
    }

    fn metadata_value<T>(request: &Request<T>, key: &'static str) -> Option<String> {
        request
            .metadata()
            .get(key)
            .and_then(|value| value.to_str().ok())
            .map(str::to_string)
    }

    async fn first_message<T>(response: Response<tonic::Streaming<T>>) -> T {
        response
            .into_inner()
            .message()
            .await
            .expect("stream read should succeed")
            .expect("stream should yield a message")
    }

    async fn watch_once(
        client: &mut StargateControlPlaneClient<tonic::transport::Channel>,
    ) -> Response<tonic::Streaming<WatchStargatesResponse>> {
        client
            .watch_stargates(WatchStargatesRequest {})
            .await
            .expect("watch should route")
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
            let stream: WatchStargatesStream = Box::pin(futures::stream::iter([Ok(response)]));
            let mut response = Response::new(stream);
            response
                .metadata_mut()
                .insert("x-upstream", "watch".parse().expect("valid metadata"));
            Ok(response)
        }

        async fn register_inference_server(
            &self,
            request: Request<tonic::Streaming<InferenceServerRegistration>>,
        ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
            self.recorder.register_hits.fetch_add(1, Ordering::Relaxed);
            let auth = metadata_value(&request, "authorization");
            let heartbeat = metadata_value(&request, REGISTRATION_HEARTBEAT_MS_METADATA);
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
            let stream: RegisterInferenceServerStream = Box::pin(stream);
            let mut response = Response::new(stream);
            response.metadata_mut().insert(
                "x-upstream",
                "registration".parse().expect("valid metadata"),
            );
            Ok(response)
        }
    }

    async fn start_fake_stargate(stargate_id: &str, recorder: Recorder) -> RunningServer {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind fake stargate");
        let addr = listener.local_addr().expect("fake stargate local addr");
        let service = FakeStargate {
            stargate_id: stargate_id.to_string(),
            recorder,
        };
        let handle = tokio::spawn(async move {
            Server::builder()
                .add_service(StargateControlPlaneServer::new(service))
                .serve_with_incoming(TcpListenerStream::new(listener))
                .await
                .expect("fake stargate failed");
        });
        RunningServer { addr, task: handle }
    }

    async fn start_router(snapshot: TargetSnapshot) -> RunningServer {
        start_router_with_config(snapshot, router_config()).await
    }

    async fn start_router_with_config(
        snapshot: TargetSnapshot,
        config: GrpcRouterConfig,
    ) -> RunningServer {
        let (_tx, rx) = watch::channel(snapshot);
        start_router_with_receiver(rx, config).await
    }

    async fn start_router_with_receiver(
        rx: watch::Receiver<TargetSnapshot>,
        config: GrpcRouterConfig,
    ) -> RunningServer {
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind router");
        let addr = listener.local_addr().expect("router local addr");
        let shutdown = CancellationToken::new();
        let handle = tokio::spawn({
            let shutdown = shutdown.clone();
            async move {
                serve_grpc_router(listener, config, rx, shutdown)
                    .await
                    .expect("router failed");
            }
        });
        RunningServer { addr, task: handle }
    }

    fn snapshot(targets: &[(&str, SocketAddr)]) -> TargetSnapshot {
        TargetSnapshot::initialized(targets.iter().map(|(pod, addr)| PodTarget {
            pod_name: (*pod).to_string(),
            grpc_addr: addr.to_string(),
            quic_addr: "127.0.0.1:50072".to_string(),
        }))
    }

    fn router_config() -> GrpcRouterConfig {
        GrpcRouterConfig {
            advertised_hostname_template: "{pod_name}.stargate.external".to_string(),
            advertised_grpc_port: 50071,
            grpc_pylon_dial_addr: "https://stargate-router.external:443".to_string(),
            target_namespace: String::new(),
            connect_timeout: Duration::from_secs(2),
            watch_heartbeat_interval: Duration::from_secs(5),
        }
    }

    fn synthetic_snapshot(count: usize) -> TargetSnapshot {
        TargetSnapshot::initialized((0..count).map(|index| {
            let pod_name = format!("stargate-{index}");
            PodTarget {
                pod_name,
                grpc_addr: format!("10.0.0.{index}:50071"),
                quic_addr: format!("10.0.0.{index}:50072"),
            }
        }))
    }

    fn request_with_authority(host: &str) -> Request<()> {
        let authority: http::uri::Authority = format!("{host}:50071")
            .parse()
            .expect("test authority should parse");
        let mut request = Request::new(());
        request.extensions_mut().insert(authority);
        request
    }

    fn registration() -> InferenceServerRegistration {
        InferenceServerRegistration {
            inference_server_id: "backend-1".to_string(),
            inference_server_url: "http://127.0.0.1:8080".to_string(),
            models: Default::default(),
            reverse_tunnel: false,
            cluster_id: String::new(),
        }
    }

    #[test]
    #[ignore = "performance benchmark; run with --ignored --nocapture"]
    fn bench_grpc_registration_target_by_authority() {
        const BASELINE_NS_PER_OP: f64 = 276.71;

        let (_tx, rx) = watch::channel(synthetic_snapshot(128));
        let router = RouterControlPlane::new(router_config(), rx);
        let request = request_with_authority("stargate-64.stargate.external");
        let iterations = 1_000_000usize;
        let started = Instant::now();
        let mut checksum = 0usize;

        for _ in 0..iterations {
            let snapshot = router.targets.borrow();
            match black_box(&router)
                .target_for_registration(black_box(&request), black_box(&snapshot))
            {
                Ok(target) => {
                    checksum = checksum.wrapping_add(target.grpc_addr.len());
                }
                Err(error) => {
                    panic!("unexpected target resolution failure: {error}");
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
    async fn watch_stargates_builds_one_canonical_identity_per_ready_target() {
        let recorder_a = Recorder::default();
        let recorder_b = Recorder::default();
        let fake_a = start_fake_stargate("stargate-0", recorder_a.clone()).await;
        let fake_b = start_fake_stargate("stargate-1", recorder_b.clone()).await;
        let router = start_router(snapshot(&[
            ("stargate-0", fake_a.addr),
            ("stargate-1", fake_b.addr),
        ]))
        .await;

        let mut client = router.client("stargate.stargate-local.svc.cluster.local");
        let response = watch_once(&mut client).await;
        let first = first_message(response).await;

        assert_eq!(
            first
                .stargates
                .iter()
                .map(|stargate| (
                    stargate.stargate_id.as_str(),
                    stargate.advertise_addr.as_str()
                ))
                .collect::<Vec<_>>(),
            vec![
                ("stargate-0", "stargate-0.stargate.external:50071"),
                ("stargate-1", "stargate-1.stargate.external:50071"),
            ]
        );
        assert!(first.stargates.iter().all(|stargate| {
            stargate.grpc_pylon_dial_addr == "https://stargate-router.external:443"
        }));
        assert_eq!(recorder_a.watch_hits.load(Ordering::Relaxed), 0);
        assert_eq!(recorder_b.watch_hits.load(Ordering::Relaxed), 0);
    }

    #[tokio::test]
    async fn watch_stargates_stream_replaces_removed_targets() {
        let recorder_a = Recorder::default();
        let recorder_b = Recorder::default();
        let fake_a = start_fake_stargate("stargate-0", recorder_a.clone()).await;
        let fake_b = start_fake_stargate("stargate-1", recorder_b.clone()).await;
        let (targets_tx, targets_rx) = watch::channel(snapshot(&[
            ("stargate-0", fake_a.addr),
            ("stargate-1", fake_b.addr),
        ]));
        let router = start_router_with_receiver(targets_rx, router_config()).await;

        let mut client = router.client("stargate.stargate-local.svc.cluster.local");
        let response = watch_once(&mut client).await;
        let mut stream = response.into_inner();
        let first = stream
            .message()
            .await
            .expect("first snapshot read should succeed")
            .expect("first snapshot should be published");
        assert_eq!(first.stargates.len(), 2);

        targets_tx
            .send(snapshot(&[("stargate-1", fake_b.addr)]))
            .expect("updated snapshot should publish");
        let replacement = stream
            .message()
            .await
            .expect("replacement snapshot read should succeed")
            .expect("replacement snapshot should be published");

        assert_eq!(replacement.stargates.len(), 1);
        assert_eq!(replacement.stargates[0].stargate_id, "stargate-1");
        assert_eq!(recorder_a.watch_hits.load(Ordering::Relaxed), 0);
        assert_eq!(recorder_b.watch_hits.load(Ordering::Relaxed), 0);
    }

    #[tokio::test]
    async fn watch_stargates_heartbeats_an_unchanged_snapshot() {
        let (targets_tx, targets_rx) = watch::channel(synthetic_snapshot(1));
        let mut config = router_config();
        config.watch_heartbeat_interval = Duration::from_millis(25);
        let router = start_router_with_receiver(targets_rx, config).await;

        let mut client = router.client("stargate.stargate-local.svc.cluster.local");
        let mut stream = watch_once(&mut client).await.into_inner();
        let first = stream
            .message()
            .await
            .expect("initial snapshot read should succeed")
            .expect("initial snapshot should be published");
        let heartbeat = tokio::time::timeout(Duration::from_millis(250), stream.message())
            .await
            .expect("unchanged snapshot heartbeat should arrive")
            .expect("heartbeat read should succeed")
            .expect("heartbeat should contain a snapshot");

        assert_eq!(heartbeat, first);
        drop(targets_tx);
    }

    #[tokio::test]
    async fn registration_authority_matches_namespace_hostname_template() {
        let recorder = Recorder::default();
        let fake = start_fake_stargate("stargate-1", recorder.clone()).await;
        let router = start_router_with_config(
            snapshot(&[("stargate-1", fake.addr)]),
            GrpcRouterConfig {
                advertised_hostname_template: "{pod_name}.{namespace}.stargate.external"
                    .to_string(),
                advertised_grpc_port: 50071,
                grpc_pylon_dial_addr: "https://stargate-router.external:443".to_string(),
                target_namespace: "prod".to_string(),
                connect_timeout: Duration::from_secs(2),
                watch_heartbeat_interval: Duration::from_secs(5),
            },
        )
        .await;

        let mut client = router.client("stargate-1.prod.stargate.external");
        let response = client
            .register_inference_server(Request::new(tokio_stream::iter([registration()])))
            .await
            .expect("registration should route");
        let ack = first_message(response).await;

        assert_eq!(ack.reverse_tunnel_target, "stargate-1");
        assert_eq!(recorder.register_hits.load(Ordering::Relaxed), 1);
    }

    #[tokio::test]
    async fn registration_with_target_authority_forwards_stream_and_metadata() {
        let recorder_b = Recorder::default();
        let fake_b = start_fake_stargate("stargate-1", recorder_b.clone()).await;
        let router = start_router(snapshot(&[("stargate-1", fake_b.addr)])).await;

        let mut client = router.client("stargate-1.stargate.external");
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
        assert_eq!(
            response.metadata().get("x-upstream").unwrap(),
            "registration"
        );
        let ack = first_message(response).await;

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
    }

    #[tokio::test]
    async fn registration_rejects_service_authority_without_target_pod() {
        let recorder_a = Recorder::default();
        let fake_a = start_fake_stargate("stargate-0", recorder_a.clone()).await;
        let router = start_router(snapshot(&[("stargate-0", fake_a.addr)])).await;

        let mut client = router.client("stargate.stargate-local.svc.cluster.local");
        let error = client
            .register_inference_server(Request::new(tokio_stream::iter([registration()])))
            .await
            .expect_err("service authority should be rejected");

        assert_eq!(error.code(), tonic::Code::InvalidArgument);
        assert_eq!(recorder_a.register_hits.load(Ordering::Relaxed), 0);
    }

    #[tokio::test]
    async fn registration_returns_unavailable_for_unready_target_pod() {
        let router = start_router(TargetSnapshot::initialized([])).await;

        let mut client = router.client("stargate-9.stargate.external");
        let error = client
            .register_inference_server(Request::new(tokio_stream::iter([registration()])))
            .await
            .expect_err("missing target should be unavailable");

        assert_eq!(error.code(), tonic::Code::Unavailable);
    }

    #[tokio::test]
    async fn watch_stargates_publishes_initialized_empty_snapshot() {
        let router = start_router(TargetSnapshot::initialized([])).await;

        let mut client = router.client("stargate-9.stargate.external");
        let response = client
            .watch_stargates(WatchStargatesRequest {})
            .await
            .expect("initialized empty snapshot should be routable");
        let snapshot = first_message(response).await;

        assert!(snapshot.stargates.is_empty());
    }
}
