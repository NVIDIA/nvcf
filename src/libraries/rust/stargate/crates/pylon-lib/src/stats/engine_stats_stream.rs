// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::convert::Infallible;
use std::fmt;
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use stargate_proto::dynamo_kvrelay as proto;
use stargate_proto::dynamo_kvrelay::kv_event_relay_client::KvEventRelayClient;
use stargate_runtime::OwnedTask;
use tokio::time::{Instant, error::Elapsed, timeout_at};
use tokio_util::sync::CancellationToken;

use super::collector::StatsAggregatorUpdate;
use super::kv_stats::{KvPoolCatalog, RelayLoadTranslator, kv_usage_from_proto};
use super::metrics::PylonMetrics;
use crate::PylonRuntimeState;

const DEFAULT_INITIAL_RECONNECT_BACKOFF: Duration = Duration::from_millis(100);
const DEFAULT_MAX_RECONNECT_BACKOFF: Duration = Duration::from_secs(5);
/// Load windows arrive about once per second; the catalog only on change.
const RELAY_SILENCE_TIMEOUT: Duration = Duration::from_secs(3);
/// The Relay's maximum message size, above tonic's 4 MiB client default.
const MAX_RELAY_MESSAGE_BYTES: usize = 8 * 1024 * 1024;
const RELAY_ERROR_REASON_TRAILER: &str = "kv-relay-error-reason";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EngineStatsStreamMode {
    Required,
    Off,
}

impl EngineStatsStreamMode {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Required => "required",
            Self::Off => "off",
        }
    }
}

impl fmt::Display for EngineStatsStreamMode {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

impl FromStr for EngineStatsStreamMode {
    type Err = ParseEngineStatsStreamModeError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "required" => Ok(Self::Required),
            "off" => Ok(Self::Off),
            _ => Err(ParseEngineStatsStreamModeError),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
#[error("expected one of required, off")]
pub struct ParseEngineStatsStreamModeError;

#[derive(Debug, Clone)]
pub struct EngineStatsStreamConfig {
    pub endpoint: String,
    /// Diagnostic label the Relay logs for this consumer's streams.
    pub subscriber_id: String,
    pub mode: EngineStatsStreamMode,
    pub initial_reconnect_backoff: Duration,
    pub max_reconnect_backoff: Duration,
    pub metrics: Option<Arc<PylonMetrics>>,
    pub runtime_state: Option<PylonRuntimeState>,
}

impl EngineStatsStreamConfig {
    pub fn new(relay_endpoint: &str, mode: EngineStatsStreamMode) -> Self {
        Self {
            endpoint: relay_endpoint.trim_end_matches('/').to_string(),
            subscriber_id: "pylon".to_string(),
            mode,
            initial_reconnect_backoff: DEFAULT_INITIAL_RECONNECT_BACKOFF,
            max_reconnect_backoff: DEFAULT_MAX_RECONNECT_BACKOFF,
            metrics: None,
            runtime_state: None,
        }
    }
}

impl Default for EngineStatsStreamConfig {
    fn default() -> Self {
        Self::new("http://127.0.0.1:50051", EngineStatsStreamMode::Required)
    }
}

owned_task_handle!(EngineStatsStreamHandle);

pub fn start_engine_stats_stream(
    config: EngineStatsStreamConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
) -> Option<EngineStatsStreamHandle> {
    if config.mode == EngineStatsStreamMode::Off {
        return None;
    }
    if let Some(runtime_state) = &config.runtime_state {
        runtime_state.require_relay_load(true);
        runtime_state.mark_relay_load_unavailable();
    }
    let task = OwnedTask::spawn("Dynamo Relay stats streams", move |stop| {
        run_engine_stats_stream(config, stats_update_tx, stop)
    });
    Some(EngineStatsStreamHandle { task })
}

/// Why a Relay session ended.
#[derive(Debug, PartialEq, Eq)]
enum SessionEnd {
    Stopped,
    Reconnect(&'static str),
    /// Contract skew or a request the Relay rejects: retrying unchanged
    /// cannot succeed, so Pylon stops instead.
    Incompatible(String),
}

async fn run_engine_stats_stream(
    config: EngineStatsStreamConfig,
    updates: flume::Sender<StatsAggregatorUpdate>,
    stop: CancellationToken,
) {
    let mut backoff = config.initial_reconnect_backoff;
    // Outlives sessions: its counter baselines are fenced by Relay incarnation.
    let mut translator = RelayLoadTranslator::default();
    loop {
        let Err(end) = run_session(&config, &updates, &mut translator, &mut backoff, &stop).await;
        let reconnect = match end {
            SessionEnd::Stopped => return,
            SessionEnd::Reconnect(reason) => Some(reason),
            SessionEnd::Incompatible(error) => {
                tracing::error!(
                    endpoint = config.endpoint,
                    %error,
                    "Dynamo Relay contract is incompatible; stopping Relay stats"
                );
                if let Some(metrics) = &config.metrics {
                    metrics.observe_engine_stats_invalid_event("incompatible_contract");
                }
                None
            }
        };
        observe_connected(&config, false);
        clear_load(&config, &updates, &stop).await;
        clear_kv(&updates, &stop).await;
        let Some(reason) = reconnect else {
            return;
        };
        reconnect_delay(&config, &stop, &mut backoff, reason).await;
    }
}

/// Streams the pool catalog, KV pool load, and serving load over one
/// connection. Both loads are translated against the latest catalog, so
/// windows that arrive before it are skipped.
async fn run_session(
    config: &EngineStatsStreamConfig,
    updates: &flume::Sender<StatsAggregatorUpdate>,
    translator: &mut RelayLoadTranslator,
    backoff: &mut Duration,
    stop: &CancellationToken,
) -> Result<Infallible, SessionEnd> {
    let connect = KvEventRelayClient::connect(config.endpoint.clone());
    let mut client = stop
        .run_until_cancelled(connect)
        .await
        .ok_or(SessionEnd::Stopped)?
        .map_err(|error| {
            tracing::warn!(endpoint = config.endpoint, %error, "Dynamo Relay connect failed");
            SessionEnd::Reconnect("connect")
        })?
        .max_decoding_message_size(MAX_RELAY_MESSAGE_BYTES);
    let subscribe = async {
        let subscriber_id = &config.subscriber_id;
        let catalogs = client
            .watch_kv_pool_catalog(proto::WatchKvPoolCatalogRequest {
                subscriber_id: subscriber_id.clone(),
                contract_marker: proto::CONTRACT_MARKER,
            })
            .await?;
        let pool_loads = client
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
            pool_loads.into_inner(),
            serving_loads.into_inner(),
        ))
    };
    let (mut catalogs, mut pool_loads, mut serving_loads) = stop
        .run_until_cancelled(subscribe)
        .await
        .ok_or(SessionEnd::Stopped)?
        .map_err(|status| status_end(config, "subscribe", &status))?;
    observe_connected(config, true);
    *backoff = config.initial_reconnect_backoff;

    let mut relay_incarnation = None;
    let mut catalog = None;
    let mut pool_load_deadline = Instant::now() + RELAY_SILENCE_TIMEOUT;
    let mut serving_load_deadline = pool_load_deadline;
    loop {
        tokio::select! {
            _ = stop.cancelled() => return Err(SessionEnd::Stopped),
            message = catalogs.message() => {
                let update = received(config, "catalog", Ok(message))?;
                same_relay(
                    &mut relay_incarnation,
                    envelope_incarnation(
                        update.protocol_version,
                        update.contract_marker,
                        update.relay.as_ref(),
                    )?,
                )?;
                match KvPoolCatalog::from_proto(update) {
                    Ok(next) => {
                        catalog = Some(next);
                        observe_event(config, "kv_pool_catalog");
                    }
                    Err(error) => {
                        invalid_event(config, "kv_pool_catalog", error);
                        catalog = None;
                        clear_load(config, updates, stop).await;
                        clear_kv(updates, stop).await;
                    }
                }
            }
            message = timeout_at(pool_load_deadline, pool_loads.message()) => {
                let update = received(config, "kv_pool_load", message)?;
                pool_load_deadline = Instant::now() + RELAY_SILENCE_TIMEOUT;
                same_relay(
                    &mut relay_incarnation,
                    envelope_incarnation(
                        update.protocol_version,
                        update.contract_marker,
                        update.relay.as_ref(),
                    )?,
                )?;
                if let Some(catalog) = &catalog {
                    match kv_usage_from_proto(catalog, update) {
                        Ok(snapshot) => {
                            send_update(updates, StatsAggregatorUpdate::KvCache(snapshot), stop)
                                .await?;
                            observe_event(config, "kv_pool_load");
                        }
                        Err(error) => {
                            invalid_event(config, "kv_pool_load", error);
                            clear_kv(updates, stop).await;
                        }
                    }
                }
            }
            message = timeout_at(serving_load_deadline, serving_loads.message()) => {
                let update = received(config, "serving_load", message)?;
                serving_load_deadline = Instant::now() + RELAY_SILENCE_TIMEOUT;
                same_relay(
                    &mut relay_incarnation,
                    envelope_incarnation(
                        update.protocol_version,
                        update.contract_marker,
                        update.relay.as_ref(),
                    )?,
                )?;
                if let Some(catalog) = &catalog {
                    match translator.translate(catalog, update) {
                        Ok(translation) => {
                            if let Some(runtime_state) = &config.runtime_state {
                                runtime_state.replace_relay_models(translation.relay_models);
                            }
                            send_update(
                                updates,
                                StatsAggregatorUpdate::RelayLoad(translation.stats),
                                stop,
                            )
                            .await?;
                            observe_event(config, "serving_load");
                        }
                        Err(error) => {
                            invalid_event(config, "serving_load", error);
                            clear_load(config, updates, stop).await;
                        }
                    }
                }
            }
        }
    }
}

/// Unwraps one stream message, ending the session on EOF, error, or silence.
fn received<T>(
    config: &EngineStatsStreamConfig,
    stream: &'static str,
    message: Result<Result<Option<T>, tonic::Status>, Elapsed>,
) -> Result<T, SessionEnd> {
    match message {
        Ok(Ok(Some(message))) => Ok(message),
        Ok(Ok(None)) => {
            tracing::warn!(
                endpoint = config.endpoint,
                stream,
                "Dynamo Relay stream ended"
            );
            Err(SessionEnd::Reconnect(stream))
        }
        Ok(Err(status)) => Err(status_end(config, stream, &status)),
        Err(_) => {
            tracing::warn!(
                endpoint = config.endpoint,
                stream,
                "Dynamo Relay stream became stale"
            );
            Err(SessionEnd::Reconnect(stream))
        }
    }
}

fn status_end(
    config: &EngineStatsStreamConfig,
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
        ) => SessionEnd::Incompatible(format!("{stream}: {status}")),
        _ => {
            tracing::warn!(endpoint = config.endpoint, stream, %status, "Dynamo Relay stream failed");
            SessionEnd::Reconnect(stream)
        }
    }
}

/// Validates a response envelope before any of its state is applied and
/// returns the Relay incarnation that produced it.
fn envelope_incarnation(
    protocol_version: u32,
    contract_marker: u32,
    relay: Option<&proto::RelayIdentity>,
) -> Result<u64, SessionEnd> {
    if contract_marker != proto::CONTRACT_MARKER || protocol_version != proto::PROTOCOL_VERSION {
        return Err(SessionEnd::Incompatible(format!(
            "Relay sent contract marker {contract_marker:#010x}, protocol version {protocol_version}"
        )));
    }
    match relay.map(|relay| relay.relay_incarnation) {
        Some(incarnation) if incarnation != 0 => Ok(incarnation),
        _ => Err(SessionEnd::Reconnect("relay_identity")),
    }
}

/// Every stream of a session must come from one Relay incarnation; a restart
/// mid-session reconnects so no state from the old incarnation survives.
fn same_relay(session: &mut Option<u64>, incarnation: u64) -> Result<(), SessionEnd> {
    match session.replace(incarnation) {
        Some(previous) if previous != incarnation => Err(SessionEnd::Reconnect("relay_restart")),
        _ => Ok(()),
    }
}

async fn clear_load(
    config: &EngineStatsStreamConfig,
    updates: &flume::Sender<StatsAggregatorUpdate>,
    stop: &CancellationToken,
) {
    if let Some(runtime_state) = &config.runtime_state {
        runtime_state.mark_relay_load_unavailable();
    }
    let _ = send_update(
        updates,
        StatsAggregatorUpdate::RelayLoad(Default::default()),
        stop,
    )
    .await;
}

async fn clear_kv(updates: &flume::Sender<StatsAggregatorUpdate>, stop: &CancellationToken) {
    let _ = send_update(
        updates,
        StatsAggregatorUpdate::KvCache(Default::default()),
        stop,
    )
    .await;
}

async fn reconnect_delay(
    config: &EngineStatsStreamConfig,
    stop: &CancellationToken,
    backoff: &mut Duration,
    reason: &'static str,
) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_reconnect(reason);
    }
    let delay = *backoff;
    *backoff = (*backoff * 2).min(config.max_reconnect_backoff);
    let _ = stop.run_until_cancelled(tokio::time::sleep(delay)).await;
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

fn observe_connected(config: &EngineStatsStreamConfig, connected: bool) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_stream_connected(config.mode.as_str(), connected);
    }
}

fn observe_event(config: &EngineStatsStreamConfig, event: &'static str) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_stream_event(event);
    }
}

fn invalid_event(config: &EngineStatsStreamConfig, kind: &'static str, error: anyhow::Error) {
    tracing::warn!(endpoint = config.endpoint, %error, "invalid Dynamo Relay stats update");
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_invalid_event(kind);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_stream_modes() {
        assert_eq!("required".parse(), Ok(EngineStatsStreamMode::Required));
        assert_eq!("off".parse(), Ok(EngineStatsStreamMode::Off));
        assert!("auto".parse::<EngineStatsStreamMode>().is_err());
        assert!("other".parse::<EngineStatsStreamMode>().is_err());
    }

    #[test]
    fn envelope_rejects_contract_skew_before_identity() {
        let relay = proto::RelayIdentity {
            drt_instance_id: 0,
            relay_incarnation: 9,
        };
        let version = proto::PROTOCOL_VERSION;
        let marker = proto::CONTRACT_MARKER;

        assert_eq!(envelope_incarnation(version, marker, Some(&relay)), Ok(9));
        assert!(matches!(
            envelope_incarnation(version, 0, None),
            Err(SessionEnd::Incompatible(_))
        ));
        assert!(matches!(
            envelope_incarnation(version + 1, marker, Some(&relay)),
            Err(SessionEnd::Incompatible(_))
        ));
    }

    #[test]
    fn rejects_missing_or_zero_relay_incarnation() {
        let version = proto::PROTOCOL_VERSION;
        let marker = proto::CONTRACT_MARKER;
        for relay in [None, Some(&proto::RelayIdentity::default())] {
            assert_eq!(
                envelope_incarnation(version, marker, relay),
                Err(SessionEnd::Reconnect("relay_identity"))
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
            Err(SessionEnd::Reconnect("relay_restart"))
        );
    }

    #[test]
    fn only_contract_and_request_errors_stop_the_stream() {
        let config = EngineStatsStreamConfig::default();
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
            assert!(matches!(
                status_end(&config, "catalog", &status(Some(reason))),
                SessionEnd::Incompatible(_)
            ));
        }
        for reason in [
            None,
            Some("RELAY_ERROR_REASON_SUBSCRIBER_LAGGED"),
            Some("UNKNOWN"),
        ] {
            assert_eq!(
                status_end(&config, "catalog", &status(reason)),
                SessionEnd::Reconnect("catalog")
            );
        }
    }
}
