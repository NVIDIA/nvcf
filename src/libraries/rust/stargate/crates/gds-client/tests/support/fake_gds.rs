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

//! An in-process fake GDS: the pool, router, cluster, service import and
//! server info services over a shared in-memory state, with watches fed by a
//! broadcast channel, a replay history for resume, and injectable failures.
//!
//! Serve it on a TCP port ([`FakeGds::serve_tcp`]) for failover tests or
//! over an in-memory duplex transport ([`FakeGds::in_memory_channel`]) for
//! tests on a paused clock, where socket I/O would let virtual time jump
//! past RPC deadlines.

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::net::SocketAddr;
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use futures::Stream;
use gds_client::error::{reason, status_with_reason};
use gds_client::v1::cluster_service_server::{ClusterService, ClusterServiceServer};
use gds_client::v1::pool_service_server::{PoolService, PoolServiceServer};
use gds_client::v1::server_info_service_server::{ServerInfoService, ServerInfoServiceServer};
use gds_client::v1::service_import_service_server::{
    ServiceImportService, ServiceImportServiceServer,
};
use gds_client::v1::{
    Cluster, DeregisterPoolRequest, DeregisterPoolResponse, EventType, GetPoolRequest,
    GetPoolResponse, GetServerInfoRequest, GetServerInfoResponse, HeartbeatPoolRequest,
    HeartbeatPoolResponse, Lease, LeaseState, ListClustersRequest, ListClustersResponse,
    ListPoolsRequest, ListPoolsResponse, ListRoutersRequest, ListRoutersResponse,
    ListServiceImportsRequest, ListServiceImportsResponse, Pool, PoolState, RegisterPoolRequest,
    RegisterPoolResponse, Router, ServerEndpoint, ServiceImport, UpdatePoolRequest,
    UpdatePoolResponse, WatchPoolsRequest, WatchPoolsResponse, WatchRoutersRequest,
    WatchRoutersResponse, WatchServiceImportsRequest, WatchServiceImportsResponse,
};
use tokio::net::TcpListener;
use tokio::sync::{broadcast, mpsc};
use tokio::task::JoinHandle;
use tokio_stream::wrappers::ReceiverStream;
use tokio_util::sync::CancellationToken;
use tonic::transport::server::TcpIncoming;
use tonic::transport::{Channel, Endpoint, Server};
use tonic::{Code, Request, Response, Status};

type BoxStream<T> = std::pin::Pin<Box<dyn Stream<Item = Result<T, Status>> + Send + 'static>>;

/// A change fanned out to watchers.
#[derive(Debug, Clone)]
enum Change {
    Pool(EventType, Pool),
    Router(EventType, Router),
    Import(EventType, ServiceImport),
}

#[derive(Debug, Clone)]
struct Recorded {
    seq: u64,
    change: Change,
}

#[derive(Default)]
struct FakeState {
    view_epoch: u64,
    seq: u64,
    history: Vec<Recorded>,
    pools: BTreeMap<String, Pool>,
    routers: BTreeMap<String, Router>,
    imports: BTreeMap<String, ServiceImport>,
    clusters: BTreeMap<String, Cluster>,
    calls: HashMap<String, usize>,
    fail_next: HashMap<String, VecDeque<Status>>,
    unavailable: bool,
    heartbeats: Vec<HeartbeatPoolRequest>,
    updates: Vec<UpdatePoolRequest>,
    registrations: Vec<RegisterPoolRequest>,
    snapshots_sent: usize,
    resumes_served: usize,
    next_pool: u64,
    next_epoch: u64,
    server_endpoints: Vec<ServerEndpoint>,
}

impl FakeState {
    fn revision(&self) -> String {
        format!("{}:{}", self.view_epoch, self.seq)
    }

    fn record(&mut self, change: Change, events: &broadcast::Sender<Recorded>) -> String {
        self.seq += 1;
        let recorded = Recorded {
            seq: self.seq,
            change,
        };
        self.history.push(recorded.clone());
        let _ = events.send(recorded);
        self.revision()
    }

    /// The deltas after `revision` when it is resumable on this view.
    fn replay_after(&self, revision: &str) -> Option<Vec<Recorded>> {
        let (epoch, seq) = revision.split_once(':')?;
        let epoch: u64 = epoch.parse().ok()?;
        let seq: u64 = seq.parse().ok()?;
        if epoch != self.view_epoch || seq > self.seq {
            return None;
        }
        Some(
            self.history
                .iter()
                .filter(|recorded| recorded.seq > seq)
                .cloned()
                .collect(),
        )
    }
}

struct Inner {
    region: String,
    state: Mutex<FakeState>,
    events: broadcast::Sender<Recorded>,
    /// Cancelled by [`FakeGds::disconnect_watches`]; replaced afterwards.
    watch_cut: Mutex<CancellationToken>,
    bookmark_interval: Mutex<Option<Duration>>,
}

/// The fake. Clones share state.
#[derive(Clone)]
pub struct FakeGds {
    inner: Arc<Inner>,
}

impl FakeGds {
    pub fn new(region: &str) -> Self {
        let (events, _) = broadcast::channel(1024);
        Self {
            inner: Arc::new(Inner {
                region: region.to_string(),
                state: Mutex::new(FakeState {
                    view_epoch: 1,
                    next_epoch: 1,
                    ..FakeState::default()
                }),
                events,
                watch_cut: Mutex::new(CancellationToken::new()),
                bookmark_interval: Mutex::new(None),
            }),
        }
    }

    fn state(&self) -> MutexGuard<'_, FakeState> {
        self.inner.state.lock().unwrap()
    }

    // ---- setup and inspection ----

    pub fn set_bookmark_interval(&self, interval: Option<Duration>) {
        *self.inner.bookmark_interval.lock().unwrap() = interval;
    }

    pub fn set_server_endpoints(&self, endpoints: Vec<ServerEndpoint>) {
        self.state().server_endpoints = endpoints;
    }

    /// Fails the next call of `method` (the gRPC method name) with `status`.
    pub fn fail_next(&self, method: &str, status: Status) {
        self.state()
            .fail_next
            .entry(method.to_string())
            .or_default()
            .push_back(status);
    }

    /// Fails every call with UNAVAILABLE while set.
    pub fn set_unavailable(&self, unavailable: bool) {
        self.state().unavailable = unavailable;
    }

    pub fn calls(&self, method: &str) -> usize {
        self.state().calls.get(method).copied().unwrap_or(0)
    }

    pub fn heartbeats(&self) -> Vec<HeartbeatPoolRequest> {
        self.state().heartbeats.clone()
    }

    pub fn updates(&self) -> Vec<UpdatePoolRequest> {
        self.state().updates.clone()
    }

    pub fn registrations(&self) -> Vec<RegisterPoolRequest> {
        self.state().registrations.clone()
    }

    pub fn snapshots_sent(&self) -> usize {
        self.state().snapshots_sent
    }

    pub fn resumes_served(&self) -> usize {
        self.state().resumes_served
    }

    pub fn pool(&self, pool_id: &str) -> Option<Pool> {
        self.state().pools.get(pool_id).cloned()
    }

    /// Adds or replaces a pool and notifies watchers.
    pub fn upsert_pool(&self, pool: Pool) {
        let mut state = self.state();
        state.pools.insert(pool.pool_id.clone(), pool.clone());
        state.record(Change::Pool(EventType::Upsert, pool), &self.inner.events);
    }

    /// Removes a pool and notifies watchers with a DELETE carrying GONE.
    pub fn remove_pool(&self, pool_id: &str) {
        let mut state = self.state();
        if let Some(mut pool) = state.pools.remove(pool_id) {
            pool.state = PoolState::Gone as i32;
            state.record(Change::Pool(EventType::Delete, pool), &self.inner.events);
        }
    }

    /// Removes a pool without notifying watchers, as if the event was lost.
    pub fn remove_pool_silently(&self, pool_id: &str) {
        self.state().pools.remove(pool_id);
    }

    /// Sets the stored generation of a pool, for STALE_GENERATION tests.
    pub fn set_pool_generation(&self, pool_id: &str, generation: u64) {
        if let Some(pool) = self.state().pools.get_mut(pool_id) {
            pool.generation = generation;
        }
    }

    /// Mints a new lease epoch for a pool, as a competing registration would.
    pub fn bump_pool_epoch(&self, pool_id: &str) {
        let mut state = self.state();
        state.next_epoch += 1;
        let epoch = state.next_epoch;
        if let Some(lease) = state
            .pools
            .get_mut(pool_id)
            .and_then(|pool| pool.lease.as_mut())
        {
            lease.epoch = epoch;
        }
    }

    pub fn upsert_router(&self, router: Router) {
        let mut state = self.state();
        state.routers.insert(router_key(&router), router.clone());
        state.record(
            Change::Router(EventType::Upsert, router),
            &self.inner.events,
        );
    }

    pub fn remove_router(&self, cluster: &str, namespace: &str, name: &str) {
        let mut state = self.state();
        if let Some(router) = state
            .routers
            .remove(&format!("{cluster}/{namespace}/{name}"))
        {
            state.record(
                Change::Router(EventType::Delete, router),
                &self.inner.events,
            );
        }
    }

    pub fn upsert_import(&self, import: ServiceImport) {
        let mut state = self.state();
        state.imports.insert(
            format!("{}/{}", import.namespace, import.name),
            import.clone(),
        );
        state.record(
            Change::Import(EventType::Upsert, import),
            &self.inner.events,
        );
    }

    pub fn upsert_cluster(&self, cluster: Cluster) {
        self.state()
            .clusters
            .insert(cluster.cluster.clone(), cluster);
    }

    /// Starts a new view epoch, as a restarted replica would: earlier
    /// revisions are no longer resumable and watchers get a fresh snapshot.
    pub fn new_view_epoch(&self) {
        let mut state = self.state();
        state.view_epoch += 1;
        state.history.clear();
    }

    /// Ends every open watch stream with UNAVAILABLE.
    pub fn disconnect_watches(&self) {
        let mut cut = self.inner.watch_cut.lock().unwrap();
        cut.cancel();
        *cut = CancellationToken::new();
    }

    // ---- serving ----

    /// Serves on an ephemeral localhost port.
    pub async fn serve_tcp(&self) -> RunningServer {
        self.serve_tcp_on("127.0.0.1:0".parse().unwrap())
            .await
            .expect("bind fake GDS")
    }

    /// Serves on `addr`, for example the port of a server stopped earlier.
    pub async fn serve_tcp_on(&self, addr: SocketAddr) -> std::io::Result<RunningServer> {
        let listener = TcpListener::bind(addr).await?;
        let addr = listener.local_addr()?;
        let stop = CancellationToken::new();
        let incoming = TcpIncoming::from(listener).with_nodelay(Some(true));
        let handle = tokio::spawn(
            self.router(stop.clone())
                .serve_with_incoming_shutdown(incoming, stop.clone().cancelled_owned()),
        );
        Ok(RunningServer {
            addr: Some(addr),
            stop,
            handle: Some(handle),
        })
    }

    /// A channel to the fake over in-memory duplex streams. Each
    /// (re)connection of the channel opens a new duplex pair.
    pub fn in_memory_channel(&self) -> (Channel, RunningServer) {
        let (conn_tx, conn_rx) =
            mpsc::channel::<Result<tokio::io::DuplexStream, std::io::Error>>(16);
        let stop = CancellationToken::new();
        let handle = tokio::spawn(self.router(stop.clone()).serve_with_incoming_shutdown(
            ReceiverStream::new(conn_rx),
            stop.clone().cancelled_owned(),
        ));
        let channel = Endpoint::from_static("http://fake.gds.invalid").connect_with_connector_lazy(
            tower::service_fn(move |_: http::Uri| {
                let conn_tx = conn_tx.clone();
                async move {
                    let (client, server) = tokio::io::duplex(1 << 20);
                    conn_tx
                        .send(Ok(server))
                        .await
                        .map_err(|_| std::io::Error::other("fake GDS stopped"))?;
                    Ok::<_, std::io::Error>(hyper_util::rt::TokioIo::new(client))
                }
            }),
        );
        (
            channel,
            RunningServer {
                addr: None,
                stop,
                handle: Some(handle),
            },
        )
    }

    fn router(&self, stop: CancellationToken) -> tonic::transport::server::Router {
        let service = FakeService {
            fake: self.clone(),
            stop,
        };
        Server::builder()
            .add_service(PoolServiceServer::new(service.clone()))
            .add_service(ClusterServiceServer::new(service.clone()))
            .add_service(ServiceImportServiceServer::new(service.clone()))
            .add_service(ServerInfoServiceServer::new(service))
    }

    /// Counts a call and returns the injected failure, if any.
    fn begin(&self, method: &str) -> Result<(), Status> {
        let mut state = self.state();
        *state.calls.entry(method.to_string()).or_default() += 1;
        if state.unavailable {
            return Err(Status::unavailable("fake GDS unavailable"));
        }
        match state
            .fail_next
            .get_mut(method)
            .and_then(VecDeque::pop_front)
        {
            Some(status) => Err(status),
            None => Ok(()),
        }
    }
}

fn router_key(router: &Router) -> String {
    format!("{}/{}/{}", router.cluster, router.namespace, router.name)
}

/// A running fake server.
pub struct RunningServer {
    addr: Option<SocketAddr>,
    stop: CancellationToken,
    handle: Option<JoinHandle<Result<(), tonic::transport::Error>>>,
}

impl RunningServer {
    pub fn addr(&self) -> SocketAddr {
        self.addr.expect("TCP server")
    }

    pub fn url(&self) -> String {
        format!("http://{}", self.addr())
    }

    /// Stops the server: open watches end with UNAVAILABLE, connections
    /// close.
    pub async fn shutdown(mut self) {
        self.stop.cancel();
        if let Some(mut handle) = self.handle.take()
            && tokio::time::timeout(Duration::from_secs(2), &mut handle)
                .await
                .is_err()
        {
            handle.abort();
            let _ = handle.await;
        }
    }
}

impl Drop for RunningServer {
    fn drop(&mut self) {
        self.stop.cancel();
        if let Some(handle) = &self.handle {
            handle.abort();
        }
    }
}

#[derive(Clone)]
struct FakeService {
    fake: FakeGds,
    stop: CancellationToken,
}

impl FakeService {
    /// Streams a watch: the replay or snapshot taken under the state lock,
    /// then live changes, bookmarks, and UNAVAILABLE when the server stops or
    /// watches are cut.
    fn watch<T: Send + 'static>(
        &self,
        method: &str,
        resume_revision: &str,
        snapshot: impl FnOnce(&FakeState) -> Vec<Change>,
        select: impl Fn(&Change) -> bool + Send + 'static,
        encode: impl Fn(EventType, Option<Change>, String) -> T + Send + 'static,
    ) -> Result<Response<BoxStream<T>>, Status> {
        self.fake.begin(method)?;
        let (tx, rx) = mpsc::channel::<Result<T, Status>>(1024);
        let mut events;
        let mut initial: Vec<T> = Vec::new();
        {
            let mut state = self.fake.state();
            events = self.fake.inner.events.subscribe();
            match state.replay_after(resume_revision) {
                Some(replay) => {
                    state.resumes_served += 1;
                    for recorded in replay
                        .into_iter()
                        .filter(|recorded| select(&recorded.change))
                    {
                        let revision = format!("{}:{}", state.view_epoch, recorded.seq);
                        let (event_type, change) = split(recorded.change);
                        initial.push(encode(event_type, Some(change), revision));
                    }
                }
                None => {
                    state.snapshots_sent += 1;
                    initial.push(encode(EventType::SnapshotStart, None, String::new()));
                    for change in snapshot(&state) {
                        let (_, change) = split(change);
                        initial.push(encode(EventType::Upsert, Some(change), String::new()));
                    }
                    initial.push(encode(EventType::SnapshotEnd, None, state.revision()));
                }
            }
        }
        let fake = self.fake.clone();
        let stop = self.stop.clone();
        let cut = self.fake.inner.watch_cut.lock().unwrap().clone();
        let bookmark_interval = *self.fake.inner.bookmark_interval.lock().unwrap();
        tokio::spawn(async move {
            for message in initial {
                if tx.send(Ok(message)).await.is_err() {
                    return;
                }
            }
            let mut bookmark = bookmark_interval.map(tokio::time::interval);
            if let Some(bookmark) = &mut bookmark {
                bookmark.tick().await;
            }
            loop {
                tokio::select! {
                    () = stop.cancelled() => {
                        let _ = tx.send(Err(Status::unavailable("fake GDS shutting down"))).await;
                        return;
                    }
                    () = cut.cancelled() => {
                        let _ = tx.send(Err(Status::unavailable("fake GDS watch cut"))).await;
                        return;
                    }
                    () = tx.closed() => return,
                    _ = async {
                        match &mut bookmark {
                            Some(bookmark) => bookmark.tick().await,
                            None => std::future::pending().await,
                        }
                    } => {
                        let revision = fake.state().revision();
                        if tx.send(Ok(encode(EventType::Bookmark, None, revision))).await.is_err() {
                            return;
                        }
                    }
                    received = events.recv() => match received {
                        Ok(recorded) => {
                            if !select(&recorded.change) {
                                continue;
                            }
                            let revision = format!("{}:{}", fake.state().view_epoch, recorded.seq);
                            let (event_type, change) = split(recorded.change);
                            if tx.send(Ok(encode(event_type, Some(change), revision))).await.is_err() {
                                return;
                            }
                        }
                        Err(broadcast::error::RecvError::Lagged(_)) => {
                            let _ = tx.send(Err(Status::unavailable("fake GDS watcher lagged"))).await;
                            return;
                        }
                        Err(broadcast::error::RecvError::Closed) => return,
                    },
                }
            }
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(rx))))
    }
}

fn split(change: Change) -> (EventType, Change) {
    let event_type = match &change {
        Change::Pool(event_type, _)
        | Change::Router(event_type, _)
        | Change::Import(event_type, _) => *event_type,
    };
    (event_type, change)
}

fn not_found(what: &str) -> Status {
    Status::not_found(format!("{what} not found"))
}

#[tonic::async_trait]
impl PoolService for FakeService {
    async fn register_pool(
        &self,
        request: Request<RegisterPoolRequest>,
    ) -> Result<Response<RegisterPoolResponse>, Status> {
        self.fake.begin("RegisterPool")?;
        let request = request.into_inner();
        let mut state = self.fake.state();
        state.registrations.push(request.clone());
        let mut pool = request.pool.unwrap_or_default();
        let cluster = pool
            .location
            .as_ref()
            .map(|location| location.cluster.clone())
            .unwrap_or_default();
        if pool.name.is_empty() || cluster.is_empty() {
            return Err(Status::invalid_argument(
                "name and location.cluster are required",
            ));
        }
        let existing = state
            .pools
            .values()
            .find(|existing| {
                existing.name == pool.name
                    && existing
                        .location
                        .as_ref()
                        .map(|location| location.cluster.as_str())
                        == Some(cluster.as_str())
            })
            .cloned();
        if let Some(existing) = &existing {
            if !pool.pool_id.is_empty() && pool.pool_id != existing.pool_id {
                return Err(status_with_reason(
                    Code::AlreadyExists,
                    "pool name taken",
                    reason::POOL_NAME_TAKEN,
                    HashMap::new(),
                ));
            }
            pool.pool_id = existing.pool_id.clone();
        } else if pool.pool_id.is_empty() {
            state.next_pool += 1;
            pool.pool_id = format!("pool-{}", state.next_pool);
        }
        state.next_epoch += 1;
        let ttl = request.lease_ttl.unwrap_or(prost_types_duration(60));
        pool.lease = Some(Lease {
            epoch: state.next_epoch,
            ttl: Some(ttl),
            state: LeaseState::Active as i32,
            ..Lease::default()
        });
        pool.clusterset = request.clusterset;
        pool.generation = request.generation;
        pool.state = derived_state(&pool);
        state.pools.insert(pool.pool_id.clone(), pool.clone());
        state.record(
            Change::Pool(EventType::Upsert, pool.clone()),
            &self.fake.inner.events,
        );
        Ok(Response::new(RegisterPoolResponse {
            pool: Some(pool),
            server_time: None,
        }))
    }

    async fn update_pool(
        &self,
        request: Request<UpdatePoolRequest>,
    ) -> Result<Response<UpdatePoolResponse>, Status> {
        self.fake.begin("UpdatePool")?;
        let request = request.into_inner();
        let mut state = self.fake.state();
        state.updates.push(request.clone());
        let Some(stored) = state.pools.get(&request.pool_id).cloned() else {
            return Err(not_found("pool"));
        };
        if request.generation <= stored.generation {
            return Err(status_with_reason(
                Code::FailedPrecondition,
                "stale generation",
                reason::STALE_GENERATION,
                HashMap::from([(
                    reason::METADATA_CURRENT_GENERATION.to_string(),
                    stored.generation.to_string(),
                )]),
            ));
        }
        let update = request.pool.unwrap_or_default();
        let mut pool = stored;
        let paths = request
            .update_mask
            .map(|mask| mask.paths)
            .unwrap_or_default();
        let all = paths.is_empty();
        let has = |path: &str| all || paths.iter().any(|p| p == path);
        if has("worker_count") {
            pool.worker_count = update.worker_count;
        }
        if has("ready_worker_count") {
            pool.ready_worker_count = update.ready_worker_count;
        }
        if has("desired_state") {
            pool.desired_state = update.desired_state;
        }
        if has("models") {
            pool.models = update.models;
        }
        if has("labels") {
            pool.labels = update.labels;
        }
        if has("hardware_class") {
            pool.hardware_class = update.hardware_class;
        }
        if has("frontends") {
            pool.frontends = update.frontends;
        }
        pool.generation = request.generation;
        pool.state = derived_state(&pool);
        state.pools.insert(pool.pool_id.clone(), pool.clone());
        state.record(
            Change::Pool(EventType::Upsert, pool.clone()),
            &self.fake.inner.events,
        );
        Ok(Response::new(UpdatePoolResponse { pool: Some(pool) }))
    }

    async fn heartbeat_pool(
        &self,
        request: Request<HeartbeatPoolRequest>,
    ) -> Result<Response<HeartbeatPoolResponse>, Status> {
        self.fake.begin("HeartbeatPool")?;
        let request = request.into_inner();
        let mut state = self.fake.state();
        state.heartbeats.push(request.clone());
        let Some(pool) = state.pools.get_mut(&request.pool_id) else {
            return Err(not_found("pool"));
        };
        let epoch = pool.lease.as_ref().map_or(0, |lease| lease.epoch);
        if epoch != request.epoch {
            return Err(status_with_reason(
                Code::Aborted,
                "lease epoch mismatch",
                reason::LEASE_EPOCH_MISMATCH,
                HashMap::new(),
            ));
        }
        if let Some(worker_count) = request.worker_count {
            pool.worker_count = worker_count;
        }
        if let Some(ready_worker_count) = request.ready_worker_count {
            pool.ready_worker_count = ready_worker_count;
        }
        if request.desired_state != PoolState::Unspecified as i32 {
            pool.desired_state = request.desired_state;
        }
        pool.state = derived_state(pool);
        let response = HeartbeatPoolResponse {
            lease: pool.lease,
            state: pool.state,
            server_time: None,
        };
        Ok(Response::new(response))
    }

    async fn deregister_pool(
        &self,
        request: Request<DeregisterPoolRequest>,
    ) -> Result<Response<DeregisterPoolResponse>, Status> {
        self.fake.begin("DeregisterPool")?;
        let request = request.into_inner();
        self.fake.remove_pool(&request.pool_id);
        Ok(Response::new(DeregisterPoolResponse {}))
    }

    async fn get_pool(
        &self,
        request: Request<GetPoolRequest>,
    ) -> Result<Response<GetPoolResponse>, Status> {
        self.fake.begin("GetPool")?;
        let request = request.into_inner();
        let pool = self
            .fake
            .pool(&request.pool_id)
            .ok_or_else(|| not_found("pool"))?;
        Ok(Response::new(GetPoolResponse { pool: Some(pool) }))
    }

    async fn list_pools(
        &self,
        request: Request<ListPoolsRequest>,
    ) -> Result<Response<ListPoolsResponse>, Status> {
        self.fake.begin("ListPools")?;
        let request = request.into_inner();
        let state = self.fake.state();
        let pools = state
            .pools
            .values()
            .filter(|pool| pool_matches(pool, &request.cluster, &request.model))
            .cloned()
            .collect();
        Ok(Response::new(ListPoolsResponse {
            pools,
            revision: state.revision(),
        }))
    }

    async fn watch_pools(
        &self,
        request: Request<WatchPoolsRequest>,
    ) -> Result<Response<BoxStream<WatchPoolsResponse>>, Status> {
        let request = request.into_inner();
        let (cluster, model) = (request.cluster.clone(), request.model.clone());
        let (cluster_filter, model_filter) = (cluster.clone(), model.clone());
        self.watch(
            "WatchPools",
            &request.resume_revision,
            move |state| {
                state
                    .pools
                    .values()
                    .filter(|pool| pool_matches(pool, &cluster, &model))
                    .map(|pool| Change::Pool(EventType::Upsert, pool.clone()))
                    .collect()
            },
            move |change| {
                matches!(change, Change::Pool(_, pool) if pool_matches(pool, &cluster_filter, &model_filter))
            },
            |event_type, change, revision| WatchPoolsResponse {
                r#type: event_type as i32,
                pool: match change {
                    Some(Change::Pool(_, pool)) => Some(pool),
                    _ => None,
                },
                revision,
            },
        )
    }

    async fn list_routers(
        &self,
        request: Request<ListRoutersRequest>,
    ) -> Result<Response<ListRoutersResponse>, Status> {
        self.fake.begin("ListRouters")?;
        let request = request.into_inner();
        let state = self.fake.state();
        let routers = state
            .routers
            .values()
            .filter(|router| request.cluster.is_empty() || router.cluster == request.cluster)
            .cloned()
            .collect();
        Ok(Response::new(ListRoutersResponse {
            routers,
            revision: state.revision(),
        }))
    }

    async fn watch_routers(
        &self,
        request: Request<WatchRoutersRequest>,
    ) -> Result<Response<BoxStream<WatchRoutersResponse>>, Status> {
        let request = request.into_inner();
        let cluster = request.cluster.clone();
        let cluster_filter = cluster.clone();
        self.watch(
            "WatchRouters",
            &request.resume_revision,
            move |state| {
                state
                    .routers
                    .values()
                    .filter(|router| cluster.is_empty() || router.cluster == cluster)
                    .map(|router| Change::Router(EventType::Upsert, router.clone()))
                    .collect()
            },
            move |change| {
                matches!(change, Change::Router(_, router) if cluster_filter.is_empty() || router.cluster == cluster_filter)
            },
            |event_type, change, revision| WatchRoutersResponse {
                r#type: event_type as i32,
                router: match change {
                    Some(Change::Router(_, router)) => Some(router),
                    _ => None,
                },
                revision,
            },
        )
    }
}

#[tonic::async_trait]
impl ClusterService for FakeService {
    async fn list_clusters(
        &self,
        _request: Request<ListClustersRequest>,
    ) -> Result<Response<ListClustersResponse>, Status> {
        self.fake.begin("ListClusters")?;
        let state = self.fake.state();
        Ok(Response::new(ListClustersResponse {
            clusters: state.clusters.values().cloned().collect(),
            revision: state.revision(),
        }))
    }
}

#[tonic::async_trait]
impl ServiceImportService for FakeService {
    async fn list_service_imports(
        &self,
        request: Request<ListServiceImportsRequest>,
    ) -> Result<Response<ListServiceImportsResponse>, Status> {
        self.fake.begin("ListServiceImports")?;
        let request = request.into_inner();
        let state = self.fake.state();
        Ok(Response::new(ListServiceImportsResponse {
            service_imports: state
                .imports
                .values()
                .filter(|import| {
                    request.namespace.is_empty() || import.namespace == request.namespace
                })
                .cloned()
                .collect(),
            revision: state.revision(),
        }))
    }

    async fn watch_service_imports(
        &self,
        request: Request<WatchServiceImportsRequest>,
    ) -> Result<Response<BoxStream<WatchServiceImportsResponse>>, Status> {
        let request = request.into_inner();
        self.watch(
            "WatchServiceImports",
            &request.resume_revision,
            |state| {
                state
                    .imports
                    .values()
                    .map(|import| Change::Import(EventType::Upsert, import.clone()))
                    .collect()
            },
            |change| matches!(change, Change::Import(..)),
            |event_type, change, revision| WatchServiceImportsResponse {
                r#type: event_type as i32,
                service_import: match change {
                    Some(Change::Import(_, import)) => Some(import),
                    _ => None,
                },
                revision,
            },
        )
    }
}

#[tonic::async_trait]
impl ServerInfoService for FakeService {
    async fn get_server_info(
        &self,
        _request: Request<GetServerInfoRequest>,
    ) -> Result<Response<GetServerInfoResponse>, Status> {
        self.fake.begin("GetServerInfo")?;
        let state = self.fake.state();
        Ok(Response::new(GetServerInfoResponse {
            region: self.fake.inner.region.clone(),
            version: "fake".to_string(),
            view_epoch: state.view_epoch.to_string(),
            endpoints: state.server_endpoints.clone(),
            server_time: None,
        }))
    }
}

fn pool_matches(pool: &Pool, cluster: &str, model: &str) -> bool {
    let cluster_ok = cluster.is_empty()
        || pool
            .location
            .as_ref()
            .is_some_and(|location| location.cluster == cluster);
    let model_ok = model.is_empty()
        || pool.models.iter().any(|served| {
            served.model_id == model || served.aliases.iter().any(|alias| alias == model)
        });
    cluster_ok && model_ok
}

/// API pools are live while their lease is active, which the fake assumes.
fn derived_state(pool: &Pool) -> i32 {
    if pool.desired_state == PoolState::Draining as i32 {
        PoolState::Draining as i32
    } else {
        PoolState::Active as i32
    }
}

fn prost_types_duration(seconds: i64) -> gds_client::prost_types::Duration {
    gds_client::prost_types::Duration { seconds, nanos: 0 }
}
