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

//! The list-then-watch engine behind [`crate::cache::PoolCache`] and
//! [`crate::routers::RouterCache`].
//!
//! Each cache lists the objects, opens a watch resuming from the list
//! revision, applies the watch events, and every TTL closes the watch and
//! lists again (anti-entropy). A broken watch is reopened with the last
//! revision seen, with a jittered backoff. The cache is stale when it has not
//! heard from GDS (a list, any event or a bookmark) for `stale_after`.

use std::collections::BTreeMap;
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use tokio::sync::{Notify, watch};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::client::{GdsClient, WatchStream};
use crate::error::Error;
use crate::proto::v1::EventType;
use crate::task::OwnedTask;

/// One decoded watch event.
#[derive(Debug)]
pub(crate) struct WatchEvent<O> {
    pub(crate) event_type: Option<EventType>,
    pub(crate) object: Option<O>,
    pub(crate) revision: String,
}

/// Metadata published with every cache state.
#[derive(Debug, Clone, Default)]
pub(crate) struct StateMeta {
    pub(crate) revision: String,
    pub(crate) fetched_at: Option<Instant>,
    pub(crate) synced: bool,
    pub(crate) stale: bool,
}

/// What one cache watches.
pub(crate) trait CacheKind: Send + Sync + 'static {
    type Object: Send + Sync + 'static;
    type Filter: Clone + Send + Sync + 'static;
    type Event: Send + 'static;
    type State: Send + Sync + 'static;

    /// The metrics label and log field of the kind.
    const NAME: &'static str;

    fn key(object: &Self::Object) -> String;

    fn list(
        client: &GdsClient,
        filter: &Self::Filter,
    ) -> impl Future<Output = Result<(Vec<Self::Object>, String), Error>> + Send;

    fn watch(
        client: &GdsClient,
        filter: &Self::Filter,
        resume_revision: String,
    ) -> impl Future<Output = Result<WatchStream<Self::Event>, Error>> + Send;

    fn event(event: Self::Event) -> WatchEvent<Self::Object>;

    fn state(objects: &BTreeMap<String, Arc<Self::Object>>, meta: &StateMeta) -> Self::State;

    fn is_synced(state: &Self::State) -> bool;
}

/// Timing of a cache.
#[derive(Debug, Clone, Copy)]
pub(crate) struct EngineConfig {
    pub(crate) ttl: Duration,
    pub(crate) watch: bool,
    pub(crate) stale_after: Duration,
}

/// The running cache task and its published state.
pub(crate) struct CacheHandle<K: CacheKind> {
    rx: watch::Receiver<Arc<K::State>>,
    refresh: Arc<Notify>,
    _task: OwnedTask,
}

impl<K: CacheKind> CacheHandle<K> {
    pub(crate) fn spawn(
        client: GdsClient,
        filter: K::Filter,
        config: EngineConfig,
        stop: &CancellationToken,
    ) -> Self {
        let initial = Arc::new(K::state(&BTreeMap::new(), &StateMeta::default()));
        let (tx, rx) = watch::channel(initial);
        let refresh = Arc::new(Notify::new());
        let task = OwnedTask::spawn(stop, {
            let refresh = refresh.clone();
            move |stop| {
                Engine::<K> {
                    client,
                    filter,
                    config,
                    tx,
                    refresh,
                    stop,
                    objects: BTreeMap::new(),
                    meta: StateMeta::default(),
                    last_contact: Instant::now(),
                    failures: 0,
                }
                .run()
            }
        });
        Self {
            rx,
            refresh,
            _task: task,
        }
    }

    pub(crate) fn subscribe(&self) -> watch::Receiver<Arc<K::State>> {
        self.rx.clone()
    }

    pub(crate) fn snapshot(&self) -> Arc<K::State> {
        self.rx.borrow().clone()
    }

    pub(crate) async fn wait_synced(&self) -> Result<Arc<K::State>, Error> {
        let mut rx = self.rx.clone();
        let state = rx
            .wait_for(|state| K::is_synced(state))
            .await
            .map_err(|_| Error::Shutdown)?;
        Ok(state.clone())
    }

    pub(crate) fn refresh_now(&self) {
        self.refresh.notify_one();
    }
}

struct Engine<K: CacheKind> {
    client: GdsClient,
    filter: K::Filter,
    config: EngineConfig,
    tx: watch::Sender<Arc<K::State>>,
    refresh: Arc<Notify>,
    stop: CancellationToken,
    objects: BTreeMap<String, Arc<K::Object>>,
    meta: StateMeta,
    /// When GDS was last heard from.
    last_contact: Instant,
    failures: u32,
}

/// Why the engine left a wait.
enum Wake {
    Elapsed,
    Refresh,
    Stop,
}

impl<K: CacheKind> Engine<K> {
    async fn run(mut self) {
        let mut next_list = Instant::now();
        loop {
            if self.stop.is_cancelled() {
                return;
            }
            if Instant::now() >= next_list {
                let client = self.client.clone();
                let filter = self.filter.clone();
                let Some(listed) = self.until_done(K::list(&client, &filter)).await else {
                    return;
                };
                match listed {
                    Ok((objects, revision)) => {
                        self.apply_list(objects, revision);
                        next_list = Instant::now() + self.config.ttl;
                    }
                    Err(error) => {
                        warn!(cache = K::NAME, %error, "GDS list failed; retrying");
                        if let Wake::Stop = self.backoff(None).await {
                            return;
                        }
                        continue;
                    }
                }
            }
            if !self.config.watch {
                match self.wait_until(next_list).await {
                    Wake::Stop => return,
                    Wake::Refresh => next_list = Instant::now(),
                    Wake::Elapsed => {}
                }
                continue;
            }
            match self.watch_until(next_list).await {
                Wake::Stop => return,
                Wake::Refresh => next_list = Instant::now(),
                Wake::Elapsed => {}
            }
        }
    }

    /// Opens a watch and applies its events until `deadline`, a refresh, a
    /// stop, or a broken stream (followed by a backoff).
    async fn watch_until(&mut self, deadline: Instant) -> Wake {
        let client = self.client.clone();
        let filter = self.filter.clone();
        let opening = K::watch(&client, &filter, self.meta.revision.clone());
        let Some(opened) = self.until_done(opening).await else {
            return Wake::Stop;
        };
        let mut stream = match opened {
            Ok(stream) => stream,
            Err(error) => {
                warn!(cache = K::NAME, %error, "GDS watch failed to open; retrying");
                return self.backoff(Some(deadline)).await;
            }
        };
        let generation = stream.connection().generation();
        let mut connection = self.client.subscribe_connection();
        let mut snapshot: Option<BTreeMap<String, Arc<K::Object>>> = None;
        loop {
            let stale_at = self.last_contact + self.config.stale_after;
            tokio::select! {
                biased;
                () = self.stop.cancelled() => return Wake::Stop,
                () = self.refresh.notified() => return Wake::Refresh,
                () = tokio::time::sleep_until(deadline) => return Wake::Elapsed,
                changed = connection.changed() => {
                    let moved = changed.is_err()
                        || connection
                            .borrow_and_update()
                            .generation()
                            .is_some_and(|current| current != generation);
                    if moved {
                        debug!(cache = K::NAME, "GDS connection changed; reopening the watch");
                        return Wake::Elapsed;
                    }
                }
                () = tokio::time::sleep_until(stale_at), if !self.meta.stale => {
                    self.set_stale(true);
                }
                message = stream.message() => match message {
                    Ok(Some(event)) => {
                        self.failures = 0;
                        self.last_contact = Instant::now();
                        self.apply_event(K::event(event), &mut snapshot);
                    }
                    Ok(None) => {
                        debug!(cache = K::NAME, "GDS watch closed by the server; reopening");
                        return self.backoff(Some(deadline)).await;
                    }
                    Err(error) => {
                        warn!(cache = K::NAME, %error, "GDS watch failed; reopening");
                        return self.backoff(Some(deadline)).await;
                    }
                },
            }
        }
    }

    /// Runs `call` to completion while keeping the stale flag current.
    /// Returns `None` when the cache is stopped first.
    async fn until_done<T>(&mut self, call: impl Future<Output = T>) -> Option<T> {
        tokio::pin!(call);
        loop {
            let stale_at = self.last_contact + self.config.stale_after;
            tokio::select! {
                biased;
                () = self.stop.cancelled() => return None,
                result = &mut call => return Some(result),
                () = tokio::time::sleep_until(stale_at), if !self.meta.stale => {
                    self.set_stale(true);
                }
            }
        }
    }

    fn apply_list(&mut self, objects: Vec<K::Object>, revision: String) {
        self.objects = objects
            .into_iter()
            .map(|object| (K::key(&object), Arc::new(object)))
            .collect();
        let now = Instant::now();
        self.meta.revision = revision;
        self.meta.fetched_at = Some(now);
        self.meta.synced = true;
        self.meta.stale = false;
        self.last_contact = now;
        self.failures = 0;
        self.client.metrics().resync(K::NAME, "list");
        debug!(
            cache = K::NAME,
            objects = self.objects.len(),
            "GDS list applied"
        );
        self.publish();
    }

    /// Applies one event per the watch contract. Objects of a snapshot are
    /// collected aside and replace the cache at `SNAPSHOT_END`, so readers
    /// never see a partial snapshot.
    fn apply_event(
        &mut self,
        event: WatchEvent<K::Object>,
        snapshot: &mut Option<BTreeMap<String, Arc<K::Object>>>,
    ) {
        let metrics = self.client.metrics().clone();
        let was_stale = self.meta.stale;
        self.meta.stale = false;
        let changed = match event.event_type {
            Some(EventType::SnapshotStart) => {
                metrics.watch_event(K::NAME, "snapshot_start");
                *snapshot = Some(BTreeMap::new());
                false
            }
            Some(EventType::Upsert) => {
                metrics.watch_event(K::NAME, "upsert");
                let Some(object) = event.object else {
                    debug!(cache = K::NAME, "ignoring UPSERT without an object");
                    return self.publish_if(was_stale);
                };
                let key = K::key(&object);
                match snapshot {
                    Some(pending) => {
                        pending.insert(key, Arc::new(object));
                        false
                    }
                    None => {
                        self.objects.insert(key, Arc::new(object));
                        self.set_revision(event.revision);
                        true
                    }
                }
            }
            Some(EventType::Delete) => {
                metrics.watch_event(K::NAME, "delete");
                let Some(object) = event.object else {
                    debug!(cache = K::NAME, "ignoring DELETE without an object");
                    return self.publish_if(was_stale);
                };
                let key = K::key(&object);
                match snapshot {
                    Some(pending) => {
                        pending.remove(&key);
                        false
                    }
                    None => {
                        self.objects.remove(&key);
                        self.set_revision(event.revision);
                        true
                    }
                }
            }
            Some(EventType::SnapshotEnd) => {
                metrics.watch_event(K::NAME, "snapshot_end");
                if let Some(pending) = snapshot.take() {
                    self.objects = pending;
                }
                self.set_revision(event.revision);
                self.meta.synced = true;
                self.meta.fetched_at = Some(Instant::now());
                metrics.resync(K::NAME, "snapshot");
                info!(
                    cache = K::NAME,
                    objects = self.objects.len(),
                    "GDS watch snapshot applied"
                );
                true
            }
            Some(EventType::Bookmark) => {
                metrics.watch_event(K::NAME, "bookmark");
                self.set_revision(event.revision);
                false
            }
            Some(EventType::Unspecified) | None => {
                debug!(cache = K::NAME, "ignoring watch event of unknown type");
                false
            }
        };
        self.publish_if(changed || was_stale);
    }

    fn set_revision(&mut self, revision: String) {
        if !revision.is_empty() {
            self.meta.revision = revision;
        }
    }

    fn set_stale(&mut self, stale: bool) {
        if self.meta.stale != stale {
            if stale {
                warn!(
                    cache = K::NAME,
                    stale_after = ?self.config.stale_after,
                    "GDS cache is stale"
                );
            }
            self.meta.stale = stale;
            self.publish();
        }
    }

    fn publish_if(&self, changed: bool) {
        if changed {
            self.publish();
        }
    }

    fn publish(&self) {
        self.client
            .metrics()
            .cache(K::NAME, self.objects.len(), self.meta.stale);
        self.tx
            .send_replace(Arc::new(K::state(&self.objects, &self.meta)));
    }

    /// Waits a jittered backoff after a failure, never past `deadline`, and
    /// marks the cache stale when the wait crosses the stale point.
    async fn backoff(&mut self, deadline: Option<Instant>) -> Wake {
        let delay = self.client.policy().backoff.delay(self.failures);
        self.failures = self.failures.saturating_add(1);
        let mut until = Instant::now() + delay;
        if let Some(deadline) = deadline {
            until = until.min(deadline);
        }
        self.wait_until(until).await
    }

    async fn wait_until(&mut self, until: Instant) -> Wake {
        loop {
            let stale_at = self.last_contact + self.config.stale_after;
            tokio::select! {
                biased;
                () = self.stop.cancelled() => return Wake::Stop,
                () = self.refresh.notified() => return Wake::Refresh,
                () = tokio::time::sleep_until(until) => return Wake::Elapsed,
                () = tokio::time::sleep_until(stale_at), if !self.meta.stale => {
                    self.set_stale(true);
                }
            }
        }
    }
}
