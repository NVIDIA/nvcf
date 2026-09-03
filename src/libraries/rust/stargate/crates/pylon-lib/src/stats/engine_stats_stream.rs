// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::fmt;
use std::str::FromStr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use stargate_proto::dynamo_kv_dc_relay as proto;
use stargate_runtime::OwnedTask;
use tokio_util::sync::CancellationToken;

use super::collector::StatsAggregatorUpdate;
use super::kv_stats::{RelayLoadTranslator, kv_snapshot_from_proto};
use super::metrics::PylonMetrics;
use crate::PylonRuntimeState;

const DEFAULT_INITIAL_RECONNECT_BACKOFF: Duration = Duration::from_millis(100);
const DEFAULT_MAX_RECONNECT_BACKOFF: Duration = Duration::from_secs(5);
const RELAY_SILENCE_TIMEOUT: Duration = Duration::from_secs(3);

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

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct RelayIdentity {
    drt_instance_id: u64,
    relay_incarnation: u64,
}

#[derive(Default)]
struct RelayEpoch {
    load: Option<RelayIdentity>,
}

async fn run_engine_stats_stream(
    config: EngineStatsStreamConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
    stop: CancellationToken,
) {
    let epoch = Arc::new(Mutex::new(RelayEpoch::default()));
    tokio::join!(
        run_load_stream(
            config.clone(),
            stats_update_tx.clone(),
            epoch.clone(),
            stop.clone(),
        ),
        run_kv_stream(config, stats_update_tx, epoch, stop),
    );
}

async fn run_load_stream(
    config: EngineStatsStreamConfig,
    updates: flume::Sender<StatsAggregatorUpdate>,
    epoch: Arc<Mutex<RelayEpoch>>,
    stop: CancellationToken,
) {
    let mut backoff = config.initial_reconnect_backoff;
    let mut last_identity = None;
    let mut translator = RelayLoadTranslator::default();
    loop {
        if stop.is_cancelled() {
            return;
        }
        mark_load_unavailable(&config, &updates, &epoch, &stop).await;
        let connect = proto::kv_dc_relay_client::KvDcRelayClient::connect(config.endpoint.clone());
        let mut client = match stop.run_until_cancelled(connect).await {
            None => return,
            Some(Ok(client)) => client,
            Some(Err(error)) => {
                tracing::warn!(endpoint = config.endpoint, %error, "Dynamo Relay load connect failed");
                reconnect_delay(&config, &stop, &mut backoff, "load_connect").await;
                continue;
            }
        };
        let response = match stop.run_until_cancelled(client.watch_load(())).await {
            None => return,
            Some(Ok(response)) => response,
            Some(Err(status)) => {
                tracing::warn!(endpoint = config.endpoint, %status, "Dynamo Relay load stream failed");
                reconnect_delay(&config, &stop, &mut backoff, "load_watch").await;
                continue;
            }
        };

        observe_connected(&config, true);
        backoff = config.initial_reconnect_backoff;
        let mut stream = response.into_inner();
        loop {
            let message = tokio::select! {
                _ = stop.cancelled() => return,
                message = tokio::time::timeout(RELAY_SILENCE_TIMEOUT, stream.message()) => message,
            };
            let snapshot = match message {
                Ok(Ok(Some(snapshot))) => snapshot,
                Ok(Ok(None)) => break,
                Ok(Err(status)) => {
                    tracing::warn!(endpoint = config.endpoint, %status, "Dynamo Relay load stream disconnected");
                    break;
                }
                Err(_) => {
                    tracing::warn!(
                        endpoint = config.endpoint,
                        "Dynamo Relay load stream became stale"
                    );
                    break;
                }
            };
            let identity = match metadata_identity(snapshot.metadata.as_ref()) {
                Ok(identity) => identity,
                Err(error) => {
                    invalid_event(&config, "load_metadata", error);
                    mark_load_unavailable(&config, &updates, &epoch, &stop).await;
                    continue;
                }
            };
            let identity_changed = {
                let mut epoch = epoch.lock().expect("Relay epoch mutex poisoned");
                let changed = epoch.load.is_some_and(|current| current != identity);
                epoch.load = Some(identity);
                changed
            };
            if last_identity.is_some_and(|current| current != identity) {
                translator = RelayLoadTranslator::default();
            }
            last_identity = Some(identity);
            if identity_changed {
                send_update(
                    &updates,
                    StatsAggregatorUpdate::KvCache(Default::default()),
                    &stop,
                )
                .await;
            }
            match translator.translate(snapshot) {
                Ok(translation) => {
                    if let Some(runtime_state) = &config.runtime_state {
                        runtime_state.replace_relay_models(translation.relay_models);
                    }
                    if !send_update(
                        &updates,
                        StatsAggregatorUpdate::RelayLoad(translation.stats),
                        &stop,
                    )
                    .await
                    {
                        return;
                    }
                    observe_event(&config, "load_snapshot");
                }
                Err(error) => {
                    invalid_event(&config, "load_snapshot", error);
                    mark_load_unavailable(&config, &updates, &epoch, &stop).await;
                }
            }
        }
        observe_connected(&config, false);
        mark_load_unavailable(&config, &updates, &epoch, &stop).await;
        reconnect_delay(&config, &stop, &mut backoff, "load_eof").await;
    }
}

async fn run_kv_stream(
    config: EngineStatsStreamConfig,
    updates: flume::Sender<StatsAggregatorUpdate>,
    epoch: Arc<Mutex<RelayEpoch>>,
    stop: CancellationToken,
) {
    let mut backoff = config.initial_reconnect_backoff;
    loop {
        if stop.is_cancelled() {
            return;
        }
        send_update(
            &updates,
            StatsAggregatorUpdate::KvCache(Default::default()),
            &stop,
        )
        .await;
        let connect = proto::kv_dc_relay_client::KvDcRelayClient::connect(config.endpoint.clone());
        let mut client = match stop.run_until_cancelled(connect).await {
            None => return,
            Some(Ok(client)) => client,
            Some(Err(error)) => {
                tracing::warn!(endpoint = config.endpoint, %error, "Dynamo Relay KV-usage connect failed");
                reconnect_delay(&config, &stop, &mut backoff, "kv_connect").await;
                continue;
            }
        };
        let response = match stop.run_until_cancelled(client.watch_kv_usage(())).await {
            None => return,
            Some(Ok(response)) => response,
            Some(Err(status)) => {
                tracing::warn!(endpoint = config.endpoint, %status, "Dynamo Relay KV-usage stream failed");
                reconnect_delay(&config, &stop, &mut backoff, "kv_watch").await;
                continue;
            }
        };
        backoff = config.initial_reconnect_backoff;
        let mut stream = response.into_inner();
        loop {
            let message = tokio::select! {
                _ = stop.cancelled() => return,
                message = tokio::time::timeout(RELAY_SILENCE_TIMEOUT, stream.message()) => message,
            };
            let snapshot = match message {
                Ok(Ok(Some(snapshot))) => snapshot,
                Ok(Ok(None)) => break,
                Ok(Err(status)) => {
                    tracing::warn!(endpoint = config.endpoint, %status, "Dynamo Relay KV-usage stream disconnected");
                    break;
                }
                Err(_) => {
                    tracing::warn!(
                        endpoint = config.endpoint,
                        "Dynamo Relay KV-usage stream became stale"
                    );
                    break;
                }
            };
            let identity = match metadata_identity(snapshot.metadata.as_ref()) {
                Ok(identity) => identity,
                Err(error) => {
                    invalid_event(&config, "kv_metadata", error);
                    clear_kv(&updates, &stop).await;
                    continue;
                }
            };
            if epoch.lock().expect("Relay epoch mutex poisoned").load != Some(identity) {
                clear_kv(&updates, &stop).await;
                continue;
            }
            match kv_snapshot_from_proto(snapshot) {
                Ok(snapshot) => {
                    if !send_update(&updates, StatsAggregatorUpdate::KvCache(snapshot), &stop).await
                    {
                        return;
                    }
                    observe_event(&config, "kv_usage_snapshot");
                }
                Err(error) => {
                    invalid_event(&config, "kv_usage_snapshot", error);
                    clear_kv(&updates, &stop).await;
                }
            }
        }
        clear_kv(&updates, &stop).await;
        reconnect_delay(&config, &stop, &mut backoff, "kv_eof").await;
    }
}

async fn mark_load_unavailable(
    config: &EngineStatsStreamConfig,
    updates: &flume::Sender<StatsAggregatorUpdate>,
    epoch: &Mutex<RelayEpoch>,
    stop: &CancellationToken,
) {
    epoch.lock().expect("Relay epoch mutex poisoned").load = None;
    if let Some(runtime_state) = &config.runtime_state {
        runtime_state.mark_relay_load_unavailable();
    }
    send_update(
        updates,
        StatsAggregatorUpdate::RelayLoad(Default::default()),
        stop,
    )
    .await;
    clear_kv(updates, stop).await;
}

async fn clear_kv(updates: &flume::Sender<StatsAggregatorUpdate>, stop: &CancellationToken) {
    send_update(
        updates,
        StatsAggregatorUpdate::KvCache(Default::default()),
        stop,
    )
    .await;
}

fn metadata_identity(
    metadata: Option<&proto::RelayMessageMetadata>,
) -> anyhow::Result<RelayIdentity> {
    let metadata = metadata.ok_or_else(|| anyhow::anyhow!("Relay metadata is missing"))?;
    anyhow::ensure!(metadata.drt_instance_id != 0, "DRT instance ID is zero");
    anyhow::ensure!(metadata.relay_incarnation != 0, "Relay incarnation is zero");
    Ok(RelayIdentity {
        drt_instance_id: metadata.drt_instance_id,
        relay_incarnation: metadata.relay_incarnation,
    })
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

async fn send_update(
    sender: &flume::Sender<StatsAggregatorUpdate>,
    update: StatsAggregatorUpdate,
    stop: &CancellationToken,
) -> bool {
    match sender.try_send(update) {
        Ok(()) => true,
        Err(flume::TrySendError::Full(update)) => stop
            .run_until_cancelled(sender.send_async(update))
            .await
            .is_some_and(|result| result.is_ok()),
        Err(flume::TrySendError::Disconnected(_)) => false,
    }
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
    fn rejects_missing_or_zero_relay_incarnation() {
        assert!(metadata_identity(None).is_err());
        assert!(metadata_identity(Some(&proto::RelayMessageMetadata::default())).is_err());
    }
}
