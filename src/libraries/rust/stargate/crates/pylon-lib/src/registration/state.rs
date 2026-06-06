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

use std::collections::HashMap;
use std::sync::Arc;

use parking_lot::Mutex;
use tokio::sync::watch;

use stargate_proto::pb::{
    InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
    ModelStats,
};

use crate::bringup::{BringupModelUpdate, ModelBringupState};

#[derive(Debug, Clone, Default)]
pub struct CurrentModelStats {
    // Sticky runtime-observed mean input TPS for this backend.
    pub last_mean_input_tps: f64,
    // Token/sec output rate for streaming generation endpoints. Embeddings item
    // cardinality is observed separately and is not exported through this field.
    pub output_tps: f64,
    pub embedding_item_tps: f64,
    pub queue_size: u64,
    pub queued_input_size: u64,

    // Cluster-scoped shared-hardware/scheduler state. Stargate currently keeps
    // the latest active backend snapshot for these fields when multiple
    // backends share a cluster.
    // Same token/sec unit as `output_tps`; embeddings item rates are not folded in.
    pub max_output_tps: f64,
    pub max_embedding_item_tps: f64,
    pub kv_cache_capacity_tokens: u64,
    pub kv_cache_used_tokens: u64,
    pub kv_cache_free_tokens: u64,
    pub num_running_queries: u64,
    pub max_engine_concurrency: Option<u64>,
    pub total_query_input_size: u64,
    pub queue_time_estimate_ms_by_priority: Option<HashMap<u32, u64>>,
    pub input_processing_queries: u64,
    pub output_generation_queries: u64,
    pub stats_observed_at_unix_ms: u64,
    pub stats_capabilities: Vec<String>,
    pub stats_sources: Vec<String>,
}

#[derive(Clone)]
pub(super) struct SharedInstStateChannels {
    pub(super) status_rx: flume::Receiver<InferenceServerStatus>,
    pub(super) stats_rx: flume::Receiver<(String, CurrentModelStats)>,
    pub(super) bringup_state_rx: flume::Receiver<BringupModelUpdate>,
}

#[derive(Clone)]
pub(super) struct SharedInstState {
    status_rx: flume::Receiver<InferenceServerStatus>,
    stats_rx: flume::Receiver<(String, CurrentModelStats)>,
    bringup_state_rx: flume::Receiver<BringupModelUpdate>,
    current_status: watch::Sender<InferenceServerStatus>,
    current_status_rx: watch::Receiver<InferenceServerStatus>,
    current_stats: watch::Sender<HashMap<String, CurrentModelStats>>,
    current_stats_rx: watch::Receiver<HashMap<String, CurrentModelStats>>,
    current_bringup_state: watch::Sender<HashMap<String, ModelBringupState>>,
    current_bringup_state_rx: watch::Receiver<HashMap<String, ModelBringupState>>,
    snapshot_update_lock: Arc<Mutex<()>>,
}

impl SharedInstState {
    pub(super) fn new(
        initial_status: InferenceServerStatus,
        model_ids: &[String],
        channels: SharedInstStateChannels,
        bringup_enabled: bool,
    ) -> Self {
        let mut initial_stats = HashMap::new();
        let mut initial_bringup_state = HashMap::new();
        for model_id in model_ids {
            initial_stats.insert(model_id.clone(), CurrentModelStats::default());
            initial_bringup_state.insert(
                model_id.clone(),
                if bringup_enabled {
                    ModelBringupState::ConnectingUnavailable
                } else {
                    ModelBringupState::AdvertisingActive
                },
            );
        }
        let (current_status, current_status_rx) = watch::channel(initial_status);
        let (current_stats, current_stats_rx) = watch::channel(initial_stats);
        let (current_bringup_state, current_bringup_state_rx) =
            watch::channel(initial_bringup_state);
        Self {
            status_rx: channels.status_rx,
            stats_rx: channels.stats_rx,
            bringup_state_rx: channels.bringup_state_rx,
            current_status,
            current_status_rx,
            current_stats,
            current_stats_rx,
            current_bringup_state,
            current_bringup_state_rx,
            snapshot_update_lock: Arc::new(Mutex::new(())),
        }
    }

    pub(super) fn drain_updates(&self) -> bool {
        let _snapshot_update_guard = self.snapshot_update_lock.lock();
        let mut changed = false;

        while let Ok(new_status) = self.status_rx.try_recv() {
            let _ = self.current_status.send(new_status);
            changed = true;
        }

        let mut stats_updated = false;
        while let Ok((model_id, stats)) = self.stats_rx.try_recv() {
            self.current_stats.send_modify(|map| {
                let merged = map
                    .get(&model_id)
                    .map(|existing| merge_current_model_stats(existing, &stats))
                    .unwrap_or(stats);
                map.insert(model_id, merged);
            });
            stats_updated = true;
        }
        if stats_updated {
            changed = true;
        }

        let mut bringup_updated = false;
        while let Ok(update) = self.bringup_state_rx.try_recv() {
            self.current_bringup_state.send_modify(|map| {
                map.insert(update.model_id, update.state);
            });
            bringup_updated = true;
        }
        if bringup_updated {
            changed = true;
        }

        changed
    }

    pub(super) fn snapshot(&self) -> HashMap<String, InferenceServerModelRegistration> {
        let _snapshot_update_guard = self.snapshot_update_lock.lock();
        let status = *self.current_status_rx.borrow();
        let current = self.current_stats_rx.borrow().clone();
        let bringup_state = self.current_bringup_state_rx.borrow().clone();
        current
            .into_iter()
            .map(|(model_id, cur)| {
                let last_mean_input_tps = cur.last_mean_input_tps;
                let model_status = gated_model_status(
                    status,
                    bringup_state
                        .get(&model_id)
                        .copied()
                        .unwrap_or(ModelBringupState::ConnectingUnavailable),
                );
                let proto = InferenceServerModelRegistration {
                    stats: Some(ModelStats {
                        last_mean_input_tps,
                        output_tps: cur.output_tps,
                        max_output_tps: cur.max_output_tps,
                        queue_size: cur.queue_size,
                        queued_input_size: cur.queued_input_size,
                        kv_cache_capacity_tokens: cur.kv_cache_capacity_tokens,
                        kv_cache_used_tokens: cur.kv_cache_used_tokens,
                        kv_cache_free_tokens: cur.kv_cache_free_tokens,
                        num_running_queries: cur.num_running_queries,
                        max_engine_concurrency: cur.max_engine_concurrency.unwrap_or_default(),
                        total_query_input_size: cur.total_query_input_size,
                        queue_time_estimate_ms_by_priority: cur
                            .queue_time_estimate_ms_by_priority
                            .clone()
                            .unwrap_or_default(),
                        input_processing_queries: cur.input_processing_queries,
                        output_generation_queries: cur.output_generation_queries,
                        stats_observed_at_unix_ms: cur.stats_observed_at_unix_ms,
                        stats_capabilities: cur.stats_capabilities.clone(),
                        stats_sources: cur.stats_sources.clone(),
                    }),
                    status: model_status.into(),
                };
                (model_id, proto)
            })
            .collect()
    }
}

pub(super) fn merge_current_model_stats(
    existing: &CurrentModelStats,
    incoming: &CurrentModelStats,
) -> CurrentModelStats {
    let mut merged = incoming.clone();
    if incoming.kv_cache_capacity_tokens == 0
        && incoming.kv_cache_used_tokens == 0
        && incoming.kv_cache_free_tokens == 0
        && has_any_kv_metrics(existing)
    {
        merged.kv_cache_capacity_tokens = existing.kv_cache_capacity_tokens;
        merged.kv_cache_used_tokens = existing.kv_cache_used_tokens;
        merged.kv_cache_free_tokens = existing.kv_cache_free_tokens;
    }
    if incoming.max_engine_concurrency.is_none() && existing.max_engine_concurrency.is_some() {
        merged.max_engine_concurrency = existing.max_engine_concurrency;
    }
    if incoming.queue_time_estimate_ms_by_priority.is_none()
        && existing.queue_time_estimate_ms_by_priority.is_some()
    {
        merged.queue_time_estimate_ms_by_priority =
            existing.queue_time_estimate_ms_by_priority.clone();
    }
    if incoming.stats_observed_at_unix_ms == 0 && existing.stats_observed_at_unix_ms != 0 {
        merged.stats_observed_at_unix_ms = existing.stats_observed_at_unix_ms;
    }
    if incoming.stats_capabilities.is_empty() && !existing.stats_capabilities.is_empty() {
        merged.stats_capabilities = existing.stats_capabilities.clone();
    }
    if incoming.stats_sources.is_empty() && !existing.stats_sources.is_empty() {
        merged.stats_sources = existing.stats_sources.clone();
    }
    merged
}

fn has_any_kv_metrics(stats: &CurrentModelStats) -> bool {
    stats.kv_cache_capacity_tokens > 0
        || stats.kv_cache_used_tokens > 0
        || stats.kv_cache_free_tokens > 0
}

#[derive(Debug, Clone)]
pub(super) struct AdvertisedModelStatus {
    pub(super) model_id: String,
    pub(super) status: InferenceServerStatus,
}

pub(super) fn advertised_model_statuses(
    update: &InferenceServerRegistration,
) -> Vec<AdvertisedModelStatus> {
    update
        .models
        .iter()
        .map(|(model_id, registration)| AdvertisedModelStatus {
            model_id: model_id.clone(),
            status: InferenceServerStatus::try_from(registration.status)
                .unwrap_or(InferenceServerStatus::Unknown),
        })
        .collect()
}

pub(super) fn build_inference_server_registration(
    inference_server_id: &str,
    cluster_id: &str,
    inference_server_url: &str,
    models: &HashMap<String, InferenceServerModelRegistration>,
    reverse_tunnel: bool,
    coordinated_calibration: bool,
    reverse_connected: bool,
) -> InferenceServerRegistration {
    let models = models
        .iter()
        .map(|(model_id, model)| {
            let mut model = model.clone();
            let model_status = InferenceServerStatus::try_from(model.status)
                .unwrap_or(InferenceServerStatus::Unknown);
            model.status =
                router_advertised_status(model_status, reverse_tunnel, reverse_connected).into();
            (model_id.clone(), model)
        })
        .collect();
    InferenceServerRegistration {
        inference_server_id: inference_server_id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: inference_server_url.to_string(),
        models,
        reverse_tunnel,
        coordinated_calibration,
    }
}

pub(super) fn router_advertised_status(
    model_status: InferenceServerStatus,
    reverse_tunnel: bool,
    reverse_connected: bool,
) -> InferenceServerStatus {
    if reverse_tunnel && model_status == InferenceServerStatus::Active && !reverse_connected {
        InferenceServerStatus::Inactive
    } else {
        model_status
    }
}

pub(super) fn gated_model_status(
    base_status: InferenceServerStatus,
    bringup_state: ModelBringupState,
) -> InferenceServerStatus {
    if base_status != InferenceServerStatus::Active {
        return base_status;
    }
    match bringup_state {
        ModelBringupState::AdvertisingActive => InferenceServerStatus::Active,
        ModelBringupState::ConnectingUnavailable | ModelBringupState::Recovering => {
            InferenceServerStatus::Inactive
        }
    }
}
