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

//! The Dynamo KV DC Relay session: subscribes to the pool catalog, KV pool
//! load, and serving load, and feeds the translated windows to the stats
//! collector.
//!
//! A session publishes nothing it cannot vouch for. When it ends, falls
//! silent, or receives something invalid, it sends `RelayLost` and the task
//! reconnects; nothing falls back to local observation.

use std::convert::Infallible;
use std::sync::Arc;
use std::time::Duration;

use futures::{Stream, StreamExt};
use stargate_proto::dynamo_kvrelay as proto;
use stargate_proto::dynamo_kvrelay::kv_event_relay_client::KvEventRelayClient;
use stargate_runtime::OwnedTask;
use tokio::time::{Instant, timeout_at};
use tokio_util::sync::CancellationToken;

use super::collector::StatsAggregatorUpdate;
use super::metrics::PylonMetrics;
use super::relay_translate::{KvPoolCatalog, RelayLoadTranslator, kv_update};

const INITIAL_RECONNECT_BACKOFF: Duration = Duration::from_millis(100);
const MAX_RECONNECT_BACKOFF: Duration = Duration::from_secs(5);
/// How long a session may take to open its streams, and then to deliver the
/// first catalog and each load stream's first window, before the windows'
/// period is known.
const FIRST_WINDOW_TIMEOUT: Duration = Duration::from_secs(30);
/// After a window, a load stream is silent once three of its periods pass
/// without another, and never sooner than this.
const MIN_SILENCE_TIMEOUT: Duration = Duration::from_secs(3);
/// The Relay's maximum message size, above tonic's 4 MiB client default.
const MAX_RELAY_MESSAGE_BYTES: usize = 8 * 1024 * 1024;
const RELAY_ERROR_REASON_TRAILER: &str = "kv-relay-error-reason";
/// The `mode` label of the shared engine-stats connection gauge.
const METRICS_MODE: &str = "dynamo_relay";

#[derive(Debug, Clone)]
pub struct DynamoRelayConfig {
    /// The Relay's gRPC endpoint.
    pub url: String,
    /// Diagnostic label the Relay logs for this consumer's streams.
    pub subscriber_id: String,
    pub metrics: Option<Arc<PylonMetrics>>,
}

impl DynamoRelayConfig {
    pub fn new(url: impl Into<String>, subscriber_id: impl Into<String>) -> Self {
        Self {
            url: url.into(),
            subscriber_id: subscriber_id.into(),
            metrics: None,
        }
    }
}

owned_task_handle!(DynamoRelayHandle);

/// Starts the Relay task. It reconnects internally and runs until shutdown,
/// or until the stats collector is gone.
pub fn start_dynamo_relay(
    config: DynamoRelayConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
) -> DynamoRelayHandle {
    let task = OwnedTask::spawn("Dynamo Relay stats", move |stop| async move {
        RelaySubscriber::new(config, stats_update_tx)
            .run(&stop)
            .await;
    });
    DynamoRelayHandle { task }
}

/// Why a Relay session ended.
#[derive(Debug, PartialEq, Eq)]
pub(super) enum SessionEnd {
    Stopped,
    /// The session's view is gone; the reason labels the reconnect metric.
    Lost(&'static str),
}

/// The state that outlives Relay sessions.
pub(super) struct RelaySubscriber {
    config: DynamoRelayConfig,
    updates: flume::Sender<StatsAggregatorUpdate>,
    translator: RelayLoadTranslator,
    /// Resets once a session publishes, so a Relay that accepts the streams
    /// and then rejects every session is retried at the maximum backoff.
    backoff: Duration,
}

impl RelaySubscriber {
    pub(super) fn new(
        config: DynamoRelayConfig,
        updates: flume::Sender<StatsAggregatorUpdate>,
    ) -> Self {
        Self {
            config,
            updates,
            translator: RelayLoadTranslator::default(),
            backoff: INITIAL_RECONNECT_BACKOFF,
        }
    }

    async fn run(mut self, stop: &CancellationToken) {
        loop {
            let reason = match self.session(stop).await {
                SessionEnd::Stopped => return,
                SessionEnd::Lost(reason) => reason,
            };
            if let Some(metrics) = &self.config.metrics {
                metrics.observe_engine_stats_stream_connected(METRICS_MODE, false);
                metrics.observe_engine_stats_reconnect(reason);
            }
            let delay = self.backoff;
            self.backoff = (self.backoff * 2).min(MAX_RECONNECT_BACKOFF);
            if stop
                .run_until_cancelled(tokio::time::sleep(delay))
                .await
                .is_none()
            {
                return;
            }
        }
    }

    async fn session(&mut self, stop: &CancellationToken) -> SessionEnd {
        // A peer that accepts the connection but never answers would hold the
        // session here, before any window deadline, so opening the streams
        // shares the first window's allowance.
        let open = tokio::time::timeout(FIRST_WINDOW_TIMEOUT, open_streams(&self.config));
        let (catalogs, kv_loads, serving_loads) = match stop.run_until_cancelled(open).await {
            None => return SessionEnd::Stopped,
            Some(Err(_)) => return silent(&self.config, "subscribe"),
            Some(Ok(Err(end))) => return end,
            Some(Ok(Ok(streams))) => streams,
        };
        if let Some(metrics) = &self.config.metrics {
            metrics.observe_engine_stats_stream_connected(METRICS_MODE, true);
        }
        self.drive(catalogs, kv_loads, serving_loads, stop).await
    }

    /// Feeds one session's streams to the collector until the session ends,
    /// then retracts what it published with `RelayLost`.
    pub(super) async fn drive<C, K, L>(
        &mut self,
        catalogs: C,
        kv_loads: K,
        serving_loads: L,
        stop: &CancellationToken,
    ) -> SessionEnd
    where
        C: Stream<Item = Result<proto::KvPoolCatalogUpdate, tonic::Status>> + Unpin,
        K: Stream<Item = Result<proto::KvPoolLoadUpdate, tonic::Status>> + Unpin,
        L: Stream<Item = Result<proto::ServingLoadUpdate, tonic::Status>> + Unpin,
    {
        let Err(end) = self
            .drive_streams(catalogs, kv_loads, serving_loads, stop)
            .await;
        if let SessionEnd::Lost(_) = end {
            let _ = send_update(&self.updates, StatsAggregatorUpdate::RelayLost, stop).await;
        }
        end
    }

    /// Windows are translated against the latest catalog, so those that
    /// arrive before it are skipped.
    async fn drive_streams<C, K, L>(
        &mut self,
        mut catalogs: C,
        mut kv_loads: K,
        mut serving_loads: L,
        stop: &CancellationToken,
    ) -> Result<Infallible, SessionEnd>
    where
        C: Stream<Item = Result<proto::KvPoolCatalogUpdate, tonic::Status>> + Unpin,
        K: Stream<Item = Result<proto::KvPoolLoadUpdate, tonic::Status>> + Unpin,
        L: Stream<Item = Result<proto::ServingLoadUpdate, tonic::Status>> + Unpin,
    {
        let mut relay_incarnation = None;
        let mut catalog = None;
        let first_window = Instant::now() + FIRST_WINDOW_TIMEOUT;
        let mut kv_deadline = first_window;
        let mut load_deadline = first_window;
        loop {
            tokio::select! {
                _ = stop.cancelled() => return Err(SessionEnd::Stopped),
                // The catalog changes rarely and may stay quiet once it has
                // arrived, but until then every window is skipped.
                () = tokio::time::sleep_until(first_window), if catalog.is_none() => {
                    return Err(silent(&self.config, "kv_pool_catalog"));
                }
                message = catalogs.next() => {
                    let update = received(&self.config, "kv_pool_catalog", message)?;
                    same_relay(
                        &mut relay_incarnation,
                        envelope_incarnation(
                            &self.config,
                            update.protocol_version,
                            update.contract_marker,
                            update.relay.as_ref(),
                        )?,
                    )?;
                    let next = KvPoolCatalog::from_proto(update)
                        .map_err(|error| self.invalid("kv_pool_catalog", error))?;
                    catalog = Some(next);
                    self.observe_event("kv_pool_catalog");
                }
                message = timeout_at(kv_deadline, kv_loads.next()) => {
                    let message = message.map_err(|_| silent(&self.config, "kv_pool_load"))?;
                    let update = received(&self.config, "kv_pool_load", message)?;
                    kv_deadline = Instant::now() + silence_timeout(update.window_ms);
                    same_relay(
                        &mut relay_incarnation,
                        envelope_incarnation(
                            &self.config,
                            update.protocol_version,
                            update.contract_marker,
                            update.relay.as_ref(),
                        )?,
                    )?;
                    if let Some(catalog) = &catalog {
                        let update = kv_update(catalog, update)
                            .map_err(|error| self.invalid("kv_pool_load", error))?;
                        self.publish(StatsAggregatorUpdate::RelayKv(update), stop).await?;
                        self.observe_event("kv_pool_load");
                    }
                }
                message = timeout_at(load_deadline, serving_loads.next()) => {
                    let message = message.map_err(|_| silent(&self.config, "serving_load"))?;
                    let update = received(&self.config, "serving_load", message)?;
                    load_deadline = Instant::now() + silence_timeout(update.window_ms);
                    same_relay(
                        &mut relay_incarnation,
                        envelope_incarnation(
                            &self.config,
                            update.protocol_version,
                            update.contract_marker,
                            update.relay.as_ref(),
                        )?,
                    )?;
                    if let Some(catalog) = &catalog {
                        let update = self
                            .translator
                            .translate(catalog, update)
                            .map_err(|error| self.invalid("serving_load", error))?;
                        self.publish(StatsAggregatorUpdate::RelayLoad(update), stop).await?;
                        self.observe_event("serving_load");
                    }
                }
            }
        }
    }

    async fn publish(
        &mut self,
        update: StatsAggregatorUpdate,
        stop: &CancellationToken,
    ) -> Result<(), SessionEnd> {
        send_update(&self.updates, update, stop).await?;
        self.backoff = INITIAL_RECONNECT_BACKOFF;
        Ok(())
    }

    fn observe_event(&self, event: &'static str) {
        if let Some(metrics) = &self.config.metrics {
            metrics.observe_engine_stats_stream_event(event);
        }
    }

    fn invalid(&self, kind: &'static str, error: anyhow::Error) -> SessionEnd {
        tracing::warn!(url = self.config.url, kind, %error, "invalid Dynamo Relay update");
        if let Some(metrics) = &self.config.metrics {
            metrics.observe_engine_stats_invalid_event(kind);
        }
        SessionEnd::Lost("invalid")
    }
}

/// Connects and subscribes to the catalog, KV pool load, and serving load.
async fn open_streams(
    config: &DynamoRelayConfig,
) -> Result<
    (
        tonic::Streaming<proto::KvPoolCatalogUpdate>,
        tonic::Streaming<proto::KvPoolLoadUpdate>,
        tonic::Streaming<proto::ServingLoadUpdate>,
    ),
    SessionEnd,
> {
    let mut client = KvEventRelayClient::connect(config.url.clone())
        .await
        .map_err(|error| {
            tracing::warn!(url = config.url, %error, "Dynamo Relay connect failed");
            SessionEnd::Lost("connect")
        })?
        .max_decoding_message_size(MAX_RELAY_MESSAGE_BYTES);
    let subscriber_id = &config.subscriber_id;
    let subscribe = async {
        let catalogs = client
            .watch_kv_pool_catalog(proto::WatchKvPoolCatalogRequest {
                subscriber_id: subscriber_id.clone(),
                contract_marker: proto::CONTRACT_MARKER,
            })
            .await?;
        let kv_loads = client
            .subscribe_kv_pool_load(proto::SubscribeKvPoolLoadRequest {
                subscriber_id: subscriber_id.clone(),
                contract_marker: proto::CONTRACT_MARKER,
            })
            .await?;
        let serving_loads = client
            .subscribe_serving_load(proto::SubscribeServingLoadRequest {
                subscriber_id: subscriber_id.clone(),
                contract_marker: proto::CONTRACT_MARKER,
            })
            .await?;
        Ok((
            catalogs.into_inner(),
            kv_loads.into_inner(),
            serving_loads.into_inner(),
        ))
    };
    subscribe
        .await
        .map_err(|status: tonic::Status| status_end(config, "subscribe", &status))
}

fn silence_timeout(window_ms: u64) -> Duration {
    Duration::from_millis(window_ms.saturating_mul(3)).max(MIN_SILENCE_TIMEOUT)
}

/// Unwraps one stream message, ending the session on EOF or error.
fn received<T>(
    config: &DynamoRelayConfig,
    stream: &'static str,
    message: Option<Result<T, tonic::Status>>,
) -> Result<T, SessionEnd> {
    match message {
        Some(Ok(message)) => Ok(message),
        Some(Err(status)) => Err(status_end(config, stream, &status)),
        None => {
            tracing::warn!(url = config.url, stream, "Dynamo Relay stream ended");
            Err(SessionEnd::Lost("stream_ended"))
        }
    }
}

fn silent(config: &DynamoRelayConfig, stream: &'static str) -> SessionEnd {
    tracing::warn!(url = config.url, stream, "Dynamo Relay stream fell silent");
    SessionEnd::Lost("silence")
}

/// A contract or request the Relay rejects cannot succeed unchanged, so it is
/// logged as an error; the session still reconnects, at the maximum backoff,
/// so a fixed Relay is picked up without restarting Pylon.
fn status_end(
    config: &DynamoRelayConfig,
    stream: &'static str,
    status: &tonic::Status,
) -> SessionEnd {
    let reason = status
        .metadata()
        .get(RELAY_ERROR_REASON_TRAILER)
        .and_then(|value| value.to_str().ok())
        .and_then(proto::RelayErrorReason::from_str_name);
    match reason {
        Some(
            proto::RelayErrorReason::ContractMismatch | proto::RelayErrorReason::InvalidRequest,
        ) => {
            tracing::error!(url = config.url, stream, %status, "Dynamo Relay rejects this Pylon's requests");
            SessionEnd::Lost("contract")
        }
        _ => {
            tracing::warn!(url = config.url, stream, %status, "Dynamo Relay stream failed");
            SessionEnd::Lost("stream_failed")
        }
    }
}

/// Validates a response envelope before any of its state is applied and
/// returns the Relay incarnation that produced it.
fn envelope_incarnation(
    config: &DynamoRelayConfig,
    protocol_version: u32,
    contract_marker: u32,
    relay: Option<&proto::RelayIdentity>,
) -> Result<u64, SessionEnd> {
    if contract_marker != proto::CONTRACT_MARKER || protocol_version != proto::PROTOCOL_VERSION {
        tracing::error!(
            url = config.url,
            "Dynamo Relay sent contract marker {contract_marker:#010x}, protocol version {protocol_version}"
        );
        return Err(SessionEnd::Lost("contract"));
    }
    match relay.map(|relay| relay.relay_incarnation) {
        Some(incarnation) if incarnation != 0 => Ok(incarnation),
        _ => Err(SessionEnd::Lost("relay_identity")),
    }
}

/// Every stream of a session must come from one Relay incarnation; a restart
/// mid-session reconnects so no state from the old incarnation survives.
fn same_relay(session: &mut Option<u64>, incarnation: u64) -> Result<(), SessionEnd> {
    match session.replace(incarnation) {
        Some(previous) if previous != incarnation => Err(SessionEnd::Lost("relay_restart")),
        _ => Ok(()),
    }
}

/// Fails with [`SessionEnd::Stopped`] once Pylon stops or the collector is gone.
async fn send_update(
    sender: &flume::Sender<StatsAggregatorUpdate>,
    update: StatsAggregatorUpdate,
    stop: &CancellationToken,
) -> Result<(), SessionEnd> {
    let sent = match sender.try_send(update) {
        Ok(()) => true,
        Err(flume::TrySendError::Full(update)) => stop
            .run_until_cancelled(sender.send_async(update))
            .await
            .is_some_and(|result| result.is_ok()),
        Err(flume::TrySendError::Disconnected(_)) => false,
    };
    sent.then_some(()).ok_or(SessionEnd::Stopped)
}

#[cfg(test)]
mod tests {
    use tokio::sync::mpsc;
    use tokio_stream::wrappers::UnboundedReceiverStream;

    use super::*;

    const RELAY: proto::RelayIdentity = proto::RelayIdentity {
        drt_instance_id: 7,
        relay_incarnation: 2,
    };

    type Feed<T> = mpsc::UnboundedSender<Result<T, tonic::Status>>;

    struct Session {
        catalogs: Feed<proto::KvPoolCatalogUpdate>,
        kv_loads: Feed<proto::KvPoolLoadUpdate>,
        serving_loads: Feed<proto::ServingLoadUpdate>,
        updates: flume::Receiver<StatsAggregatorUpdate>,
        end: tokio::task::JoinHandle<SessionEnd>,
    }

    fn start_session() -> Session {
        let (catalogs, catalog_stream) = mpsc::unbounded_channel();
        let (kv_loads, kv_stream) = mpsc::unbounded_channel();
        let (serving_loads, serving_stream) = mpsc::unbounded_channel();
        let (tx, updates) = flume::unbounded();
        let mut subscriber = RelaySubscriber::new(DynamoRelayConfig::new("", "test"), tx);
        let end = tokio::spawn(async move {
            subscriber
                .drive(
                    UnboundedReceiverStream::new(catalog_stream),
                    UnboundedReceiverStream::new(kv_stream),
                    UnboundedReceiverStream::new(serving_stream),
                    &CancellationToken::new(),
                )
                .await
        });
        Session {
            catalogs,
            kv_loads,
            serving_loads,
            updates,
            end,
        }
    }

    fn catalog() -> proto::KvPoolCatalogUpdate {
        proto::KvPoolCatalogUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            revision: 1,
            snapshot: Some(proto::KvPoolCatalogSnapshot::default()),
            contract_marker: proto::CONTRACT_MARKER,
        }
    }

    fn kv_window() -> proto::KvPoolLoadUpdate {
        proto::KvPoolLoadUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            window_sequence: 1,
            observed_ms: 1,
            window_ms: 1_000,
            pools: Vec::new(),
            contract_marker: proto::CONTRACT_MARKER,
        }
    }

    fn load_window() -> proto::ServingLoadUpdate {
        proto::ServingLoadUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            window_sequence: 1,
            observed_ms: 1,
            window_ms: 1_000,
            pools: Vec::new(),
            models: Vec::new(),
            contract_marker: proto::CONTRACT_MARKER,
        }
    }

    /// Lets the session handle everything sent to it so far.
    async fn settle() {
        tokio::time::sleep(Duration::from_millis(1)).await;
    }

    #[tokio::test(start_paused = true)]
    async fn windows_wait_for_the_catalog_and_a_lost_session_retracts_them() {
        let session = start_session();
        session.kv_loads.send(Ok(kv_window())).unwrap();
        session.serving_loads.send(Ok(load_window())).unwrap();
        settle().await;
        assert!(session.updates.is_empty());

        session.catalogs.send(Ok(catalog())).unwrap();
        settle().await;
        session.serving_loads.send(Ok(load_window())).unwrap();
        settle().await;
        let published = session.updates.drain().collect::<Vec<_>>();
        assert!(matches!(
            published[..],
            [StatsAggregatorUpdate::RelayLoad(_)]
        ));

        drop(session.kv_loads);
        assert_eq!(session.end.await.unwrap(), SessionEnd::Lost("stream_ended"));
        let retracted = session.updates.drain().collect::<Vec<_>>();
        assert!(matches!(retracted[..], [StatsAggregatorUpdate::RelayLost]));
    }

    #[tokio::test(start_paused = true)]
    async fn the_first_catalog_must_arrive_but_may_then_stay_quiet() {
        let start = Instant::now();
        let without_catalog = start_session();
        let with_catalog = start_session();
        with_catalog.catalogs.send(Ok(catalog())).unwrap();
        // Both sessions' load streams stay live throughout, for at most one
        // step past the allowance.
        for _ in 0..=FIRST_WINDOW_TIMEOUT.as_secs() {
            if without_catalog.end.is_finished() {
                break;
            }
            for session in [&without_catalog, &with_catalog] {
                let _ = session.kv_loads.send(Ok(kv_window()));
                let _ = session.serving_loads.send(Ok(load_window()));
            }
            tokio::time::sleep(Duration::from_secs(1)).await;
        }

        assert!(
            without_catalog.end.is_finished(),
            "a session whose first catalog never arrives must end"
        );
        assert!(start.elapsed() <= FIRST_WINDOW_TIMEOUT + Duration::from_secs(1));
        assert_eq!(
            without_catalog.end.await.unwrap(),
            SessionEnd::Lost("silence")
        );
        let retracted = without_catalog.updates.drain().collect::<Vec<_>>();
        assert!(matches!(retracted[..], [StatsAggregatorUpdate::RelayLost]));
        assert!(!with_catalog.end.is_finished());
    }

    #[tokio::test(start_paused = true)]
    async fn a_relay_that_never_answers_its_subscriptions_is_abandoned() {
        // Accepts connections but never answers, so the subscriptions stall.
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            let mut accepted = Vec::new();
            while let Ok(connection) = listener.accept().await {
                accepted.push(connection);
            }
        });
        let (tx, updates) = flume::unbounded();
        let mut subscriber =
            RelaySubscriber::new(DynamoRelayConfig::new(format!("http://{addr}"), "test"), tx);

        let end = tokio::time::timeout(
            FIRST_WINDOW_TIMEOUT * 2,
            subscriber.session(&CancellationToken::new()),
        )
        .await
        .expect("the session must give up on a Relay that never answers");

        assert_eq!(end, SessionEnd::Lost("silence"));
        assert!(updates.is_empty());
    }

    #[test]
    fn silence_timeout_is_three_windows_but_at_least_the_floor() {
        assert_eq!(silence_timeout(1_000), MIN_SILENCE_TIMEOUT);
        assert_eq!(silence_timeout(5_000), Duration::from_secs(15));
        assert_eq!(silence_timeout(u64::MAX), Duration::from_millis(u64::MAX));
    }

    #[test]
    fn envelope_rejects_contract_skew_before_identity() {
        let config = DynamoRelayConfig::new("", "test");
        let version = proto::PROTOCOL_VERSION;
        let marker = proto::CONTRACT_MARKER;

        assert_eq!(
            envelope_incarnation(&config, version, marker, Some(&RELAY)),
            Ok(2)
        );
        for (version, marker) in [(version, 0), (version + 1, marker)] {
            assert_eq!(
                envelope_incarnation(&config, version, marker, None),
                Err(SessionEnd::Lost("contract"))
            );
        }
        for relay in [None, Some(&proto::RelayIdentity::default())] {
            assert_eq!(
                envelope_incarnation(&config, version, marker, relay),
                Err(SessionEnd::Lost("relay_identity"))
            );
        }
    }

    #[test]
    fn relay_restart_within_a_session_reconnects() {
        let mut session = None;
        assert_eq!(same_relay(&mut session, 1), Ok(()));
        assert_eq!(same_relay(&mut session, 1), Ok(()));
        assert_eq!(
            same_relay(&mut session, 2),
            Err(SessionEnd::Lost("relay_restart"))
        );
    }

    #[test]
    fn rejected_requests_are_reported_as_contract_errors() {
        let config = DynamoRelayConfig::new("", "test");
        let status = |reason: Option<&'static str>| {
            let mut status = tonic::Status::failed_precondition("rejected");
            if let Some(reason) = reason {
                status
                    .metadata_mut()
                    .insert(RELAY_ERROR_REASON_TRAILER, reason.parse().unwrap());
            }
            status
        };

        for reason in [
            "RELAY_ERROR_REASON_CONTRACT_MISMATCH",
            "RELAY_ERROR_REASON_INVALID_REQUEST",
        ] {
            assert_eq!(
                status_end(&config, "subscribe", &status(Some(reason))),
                SessionEnd::Lost("contract")
            );
        }
        for reason in [
            None,
            Some("RELAY_ERROR_REASON_SUBSCRIBER_LAGGED"),
            Some("UNKNOWN"),
        ] {
            assert_eq!(
                status_end(&config, "subscribe", &status(reason)),
                SessionEnd::Lost("stream_failed")
            );
        }
    }
}
