// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//! Translation from Dynamo's Relay-owned contract into Pylon model state.

use std::collections::{BTreeMap, HashMap, HashSet};

use stargate_proto::dynamo_kv_dc_relay as proto;

use super::aggregator::{
    KvCacheStatsEnvelope, KvCacheStatsSnapshot, RelayLoadStatsEnvelope, RelayLoadStatsSnapshot,
};

#[derive(Clone, Debug, Hash, PartialEq, Eq)]
struct PoolKey {
    cache_semantics: [u8; 16],
    cache_source: i32,
    routing_scope: [u8; 16],
    routing_source: i32,
    dc_id: u64,
}

#[derive(Clone)]
struct UsagePool {
    role: proto::WorkerRole,
    identities: Vec<String>,
    capacity_tokens: Option<u64>,
    used_tokens: Option<u64>,
    source_observed_at_unix_ms: u64,
    complete: bool,
}

#[derive(Clone)]
struct LoadPool {
    role: proto::WorkerRole,
    live_workers: Option<u64>,
    max_concurrency: Option<u64>,
    complete: bool,
}

pub(super) struct RelayLoadTranslation {
    pub(super) stats: RelayLoadStatsEnvelope,
    pub(super) relay_models: BTreeMap<String, bool>,
}

pub(super) fn kv_snapshot_from_proto(
    snapshot: proto::KvUsageSnapshot,
) -> anyhow::Result<KvCacheStatsEnvelope> {
    snapshot
        .metadata
        .as_ref()
        .ok_or_else(|| anyhow::anyhow!("KV usage snapshot metadata is missing"))?;
    let mut pools = Vec::with_capacity(snapshot.pools.len());
    let mut pool_ids = HashSet::new();
    let mut identity_owner = HashMap::<String, String>::new();
    for pool in snapshot.pools {
        let key = pool_key(pool.pool)?;
        anyhow::ensure!(pool_ids.insert(key.clone()), "duplicate KV usage pool");
        let role = worker_role(pool.role)?;
        let identities = registration_identities(&pool.models, &mut identity_owner)?;
        let complete = data_complete(pool.status)
            && pool.expected_ranks > 0
            && pool.observed_ranks == pool.expected_ranks
            && pool.block_size_tokens > 0;
        let (capacity_tokens, used_tokens) = match (pool.capacity_blocks, pool.used_blocks) {
            (Some(capacity), Some(used)) if used <= capacity => (
                capacity.checked_mul(u64::from(pool.block_size_tokens)),
                used.checked_mul(u64::from(pool.block_size_tokens)),
            ),
            _ => (None, None),
        };
        pools.push(UsagePool {
            role,
            identities,
            capacity_tokens,
            used_tokens,
            source_observed_at_unix_ms: pool.source_observed_at_unix_ms,
            complete: complete
                && pool.source_observed_at_unix_ms > 0
                && capacity_tokens.is_some()
                && used_tokens.is_some(),
        });
    }

    let mut by_identity = BTreeMap::<String, Vec<usize>>::new();
    for (index, pool) in pools.iter().enumerate() {
        for identity in &pool.identities {
            let indexes = by_identity.entry(identity.clone()).or_default();
            if !indexes.contains(&index) {
                indexes.push(index);
            }
        }
    }

    let models = by_identity
        .into_iter()
        .map(|(model, indexes)| {
            let role = if indexes
                .iter()
                .any(|index| pools[*index].role == proto::WorkerRole::Aggregated)
            {
                proto::WorkerRole::Aggregated
            } else {
                proto::WorkerRole::Decode
            };
            let selected = indexes
                .into_iter()
                .filter(|index| pools[*index].role == role)
                .collect::<Vec<_>>();
            let complete =
                !selected.is_empty() && selected.iter().all(|index| pools[*index].complete);
            let totals = complete
                .then(|| {
                    selected
                        .iter()
                        .try_fold((0_u64, 0_u64), |(capacity, used), index| {
                            Some((
                                capacity.checked_add(pools[*index].capacity_tokens?)?,
                                used.checked_add(pools[*index].used_tokens?)?,
                            ))
                        })
                })
                .flatten();
            let (capacity, used, free) = totals
                .and_then(|(capacity, used)| Some((capacity, used, capacity.checked_sub(used)?)))
                .unwrap_or_default();
            KvCacheStatsSnapshot {
                model,
                aliases: Vec::new(),
                kv_cache_capacity_tokens: capacity,
                kv_cache_used_tokens: used,
                kv_cache_free_tokens: free,
                source_observed_at_unix_ms: selected
                    .iter()
                    .map(|index| pools[*index].source_observed_at_unix_ms)
                    .filter(|timestamp| *timestamp > 0)
                    .min()
                    .unwrap_or_default(),
                complete: complete && totals.is_some(),
            }
        })
        .collect();
    Ok(KvCacheStatsEnvelope { models })
}

pub(super) fn load_snapshot_from_proto(
    snapshot: proto::LoadSnapshot,
) -> anyhow::Result<RelayLoadTranslation> {
    anyhow::ensure!(snapshot.window_ms > 0, "load snapshot window is zero");
    snapshot
        .metadata
        .as_ref()
        .ok_or_else(|| anyhow::anyhow!("load snapshot metadata is missing"))?;
    let window_seconds = f64::from(snapshot.window_ms) / 1_000.0;
    let mut pools = HashMap::<PoolKey, LoadPool>::new();
    for pool in snapshot.pools {
        let key = pool_key(pool.pool)?;
        let role = worker_role(pool.role)?;
        anyhow::ensure!(
            pools
                .insert(
                    key,
                    LoadPool {
                        role,
                        live_workers: pool.live_workers,
                        max_concurrency: pool.max_concurrency,
                        complete: data_complete(pool.scheduler_status),
                    },
                )
                .is_none(),
            "duplicate load pool"
        );
    }

    let mut identity_owner = HashMap::<String, String>::new();
    let mut relay_models = BTreeMap::new();
    let mut models = Vec::new();
    for model in snapshot.models {
        let registration = model
            .model
            .as_ref()
            .ok_or_else(|| anyhow::anyhow!("load model registration is missing"))?;
        let identities =
            registration_identities(std::slice::from_ref(registration), &mut identity_owner)?;
        let serving_pools = model
            .serving_pools
            .into_iter()
            .map(|pool| pool_key(Some(pool)))
            .collect::<anyhow::Result<HashSet<_>>>()?;
        anyhow::ensure!(
            serving_pools.iter().all(|pool| pools.contains_key(pool)),
            "load model references an unknown serving pool"
        );
        let selected_role = if serving_pools.iter().any(|pool| {
            pools
                .get(pool)
                .is_some_and(|pool| pool.role == proto::WorkerRole::Aggregated)
        }) {
            proto::WorkerRole::Aggregated
        } else {
            proto::WorkerRole::Decode
        };
        let selected = serving_pools
            .iter()
            .filter_map(|key| pools.get(key))
            .filter(|pool| pool.role == selected_role)
            .collect::<Vec<_>>();
        let scheduler_live = selected
            .iter()
            .any(|pool| pool.complete && pool.live_workers.is_some_and(|workers| workers > 0));
        let max_engine_concurrency = (!selected.is_empty()
            && selected
                .iter()
                .all(|pool| pool.complete && pool.max_concurrency.is_some()))
        .then(|| {
            selected.iter().try_fold(0_u64, |total, pool| {
                total.checked_add(pool.max_concurrency?)
            })
        })
        .flatten();
        let required = (
            model.ready_frontends,
            model.pending_first_output_requests,
            model.input_processing_requests,
            model.output_generation_requests,
        );
        let complete = data_complete(model.status)
            && model.expected_frontends > 0
            && model.observed_frontends == model.expected_frontends
            && model.source_observed_at_unix_ms > 0
            && matches!(required, (Some(_), Some(_), Some(_), Some(_)));
        let (ready_frontends, queue_size, input_processing_queries, output_generation_queries) =
            required;
        let num_running_queries = input_processing_queries
            .zip(output_generation_queries)
            .and_then(|(input, output)| input.checked_add(output));
        let complete = complete && num_running_queries.is_some();
        let active = complete
            && !serving_pools.is_empty()
            && ready_frontends.is_some_and(|ready| ready > 0)
            && scheduler_live;
        for identity in &identities {
            relay_models.insert(identity.clone(), active);
            models.push(RelayLoadStatsSnapshot {
                model: identity.clone(),
                input_tps: if complete {
                    model
                        .input_tokens
                        .map(|tokens| tokens as f64 / window_seconds)
                } else {
                    None
                },
                output_tps: if complete {
                    model.output_tokens as f64 / window_seconds
                } else {
                    0.0
                },
                queue_size: queue_size.unwrap_or_default(),
                queued_input_size: complete
                    .then_some(model.pending_first_output_input_tokens)
                    .flatten(),
                num_running_queries: num_running_queries.unwrap_or_default(),
                max_engine_concurrency,
                total_query_input_size: complete.then_some(model.live_input_tokens).flatten(),
                input_processing_queries: input_processing_queries.unwrap_or_default(),
                output_generation_queries: output_generation_queries.unwrap_or_default(),
                source_observed_at_unix_ms: model.source_observed_at_unix_ms,
                complete,
            });
        }
    }
    Ok(RelayLoadTranslation {
        stats: RelayLoadStatsEnvelope { models },
        relay_models,
    })
}

fn pool_key(pool: Option<proto::PoolIdentity>) -> anyhow::Result<PoolKey> {
    let pool = pool.ok_or_else(|| anyhow::anyhow!("pool identity is missing"))?;
    let cache_semantics: [u8; 16] = pool
        .cache_semantics_digest
        .try_into()
        .map_err(|_| anyhow::anyhow!("cache-semantics digest must contain 16 bytes"))?;
    let routing_scope: [u8; 16] = pool
        .routing_scope_digest
        .try_into()
        .map_err(|_| anyhow::anyhow!("routing-scope digest must contain 16 bytes"))?;
    identity_source(pool.cache_semantics_source)?;
    identity_source(pool.routing_scope_source)?;
    Ok(PoolKey {
        cache_semantics,
        cache_source: pool.cache_semantics_source,
        routing_scope,
        routing_source: pool.routing_scope_source,
        dc_id: pool.dc_id,
    })
}

fn identity_source(value: i32) -> anyhow::Result<proto::IdentitySource> {
    let source = proto::IdentitySource::try_from(value)
        .map_err(|_| anyhow::anyhow!("invalid pool identity source"))?;
    anyhow::ensure!(
        matches!(
            source,
            proto::IdentitySource::DefaultDerived | proto::IdentitySource::Explicit
        ),
        "unspecified pool identity source"
    );
    Ok(source)
}

fn worker_role(value: i32) -> anyhow::Result<proto::WorkerRole> {
    match proto::WorkerRole::try_from(value) {
        Ok(
            role @ (proto::WorkerRole::Aggregated
            | proto::WorkerRole::Prefill
            | proto::WorkerRole::Decode
            | proto::WorkerRole::Encode),
        ) => Ok(role),
        _ => anyhow::bail!("invalid or unspecified worker role"),
    }
}

fn data_complete(value: i32) -> bool {
    proto::DataStatus::try_from(value).ok() == Some(proto::DataStatus::Complete)
}

fn registration_identities(
    registrations: &[proto::ModelRegistration],
    owners: &mut HashMap<String, String>,
) -> anyhow::Result<Vec<String>> {
    let mut identities = Vec::new();
    for registration in registrations {
        let model = registration.model.trim();
        anyhow::ensure!(!model.is_empty(), "model registration is empty");
        anyhow::ensure!(
            !registration.base_model.trim().is_empty(),
            "base model registration is empty"
        );
        let mut registration_identities = Vec::with_capacity(registration.aliases.len() + 1);
        registration_identities.push(model.to_string());
        for alias in &registration.aliases {
            anyhow::ensure!(!alias.trim().is_empty(), "model alias is empty");
            if alias != model && !registration_identities.contains(alias) {
                registration_identities.push(alias.clone());
            }
        }
        for identity in registration_identities {
            if let Some(owner) = owners.insert(identity.clone(), model.to_string()) {
                anyhow::ensure!(
                    owner == model,
                    "model identity {identity} is owned by both {owner} and {model}"
                );
            }
            if !identities.contains(&identity) {
                identities.push(identity);
            }
        }
    }
    Ok(identities)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn metadata() -> proto::RelayMessageMetadata {
        proto::RelayMessageMetadata {
            drt_instance_id: 1,
            relay_incarnation: 2,
            observed_at_unix_ms: 99,
        }
    }

    fn pool(seed: u8) -> proto::PoolIdentity {
        proto::PoolIdentity {
            cache_semantics_digest: vec![seed; 16],
            cache_semantics_source: proto::IdentitySource::Explicit as i32,
            routing_scope_digest: vec![seed.wrapping_add(1); 16],
            routing_scope_source: proto::IdentitySource::DefaultDerived as i32,
            dc_id: u64::from(seed),
        }
    }

    fn registration(model: &str) -> proto::ModelRegistration {
        proto::ModelRegistration {
            model: model.to_string(),
            base_model: model.to_string(),
            adapter: None,
            aliases: vec![format!("{model}-alias")],
        }
    }

    fn load_pool(identity: proto::PoolIdentity) -> proto::PoolLoad {
        proto::PoolLoad {
            pool: Some(identity),
            role: proto::WorkerRole::Aggregated as i32,
            live_workers: Some(2),
            active_prefill_tokens: Some(5),
            active_decode_blocks: Some(6),
            max_concurrency: Some(8),
            scheduler_status: proto::DataStatus::Complete as i32,
            scheduler_observed_at_unix_ms: 4,
        }
    }

    fn model_load(model: &str, serving_pools: Vec<proto::PoolIdentity>) -> proto::ModelLoad {
        proto::ModelLoad {
            model: Some(registration(model)),
            ready_frontends: Some(1),
            pending_first_output_requests: Some(2),
            pending_first_output_input_tokens: Some(17),
            live_input_tokens: Some(31),
            input_processing_requests: Some(1),
            output_generation_requests: Some(2),
            serving_pools,
            requests_started: 4,
            requests_completed: 3,
            requests_failed: 0,
            requests_cancelled: 0,
            input_tokens: Some(40),
            output_tokens: 20,
            status: proto::DataStatus::Complete as i32,
            expected_frontends: 1,
            observed_frontends: 1,
            source_observed_at_unix_ms: 5,
        }
    }

    #[test]
    fn kv_usage_prefers_aggregated_pools_and_scales_blocks_to_tokens() {
        let aggregated = pool(1);
        let decode = pool(2);
        let snapshot = proto::KvUsageSnapshot {
            metadata: Some(metadata()),
            pools: vec![
                proto::PoolKvUsage {
                    pool: Some(aggregated),
                    models: vec![registration("model-a")],
                    role: proto::WorkerRole::Aggregated as i32,
                    block_size_tokens: 16,
                    expected_ranks: 2,
                    observed_ranks: 2,
                    capacity_blocks: Some(100),
                    used_blocks: Some(40),
                    status: proto::DataStatus::Complete as i32,
                    source_observed_at_unix_ms: 7,
                },
                proto::PoolKvUsage {
                    pool: Some(decode),
                    models: vec![registration("model-a")],
                    role: proto::WorkerRole::Decode as i32,
                    block_size_tokens: 16,
                    expected_ranks: 1,
                    observed_ranks: 1,
                    capacity_blocks: Some(1_000),
                    used_blocks: Some(900),
                    status: proto::DataStatus::Complete as i32,
                    source_observed_at_unix_ms: 8,
                },
            ],
        };

        let translated = kv_snapshot_from_proto(snapshot).unwrap();
        assert_eq!(translated.models.len(), 2);
        for model in translated.models {
            assert!(matches!(model.model.as_str(), "model-a" | "model-a-alias"));
            assert_eq!(model.kv_cache_capacity_tokens, 1_600);
            assert_eq!(model.kv_cache_used_tokens, 640);
            assert_eq!(model.kv_cache_free_tokens, 960);
            assert_eq!(model.source_observed_at_unix_ms, 7);
            assert!(model.complete);
        }
    }

    #[test]
    fn complete_load_activates_model_and_alias() {
        let identity = pool(1);
        let snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            window_ms: 1_000,
            pools: vec![load_pool(identity.clone())],
            models: vec![model_load("model-a", vec![identity])],
        };

        let translated = load_snapshot_from_proto(snapshot).unwrap();
        assert_eq!(translated.relay_models.get("model-a"), Some(&true));
        assert_eq!(translated.relay_models.get("model-a-alias"), Some(&true));
        assert_eq!(translated.stats.models.len(), 2);
        for model in translated.stats.models {
            assert_eq!(model.input_tps, Some(40.0));
            assert_eq!(model.output_tps, 20.0);
            assert_eq!(model.queue_size, 2);
            assert_eq!(model.queued_input_size, Some(17));
            assert_eq!(model.num_running_queries, 3);
            assert_eq!(model.max_engine_concurrency, Some(8));
            assert_eq!(model.total_query_input_size, Some(31));
            assert_eq!(model.input_processing_queries, 1);
            assert_eq!(model.output_generation_queries, 2);
            assert_eq!(model.source_observed_at_unix_ms, 5);
            assert!(model.complete);
        }
    }

    #[test]
    fn unknown_exact_input_gauges_do_not_deactivate_the_model() {
        let identity = pool(1);
        let mut model = model_load("model-a", vec![identity.clone()]);
        model.pending_first_output_input_tokens = None;
        model.live_input_tokens = None;
        let snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            window_ms: 1_000,
            pools: vec![load_pool(identity)],
            models: vec![model],
        };

        let translated = load_snapshot_from_proto(snapshot).unwrap();

        assert_eq!(translated.relay_models.get("model-a"), Some(&true));
        for stats in translated.stats.models {
            assert!(stats.complete);
            assert_eq!(stats.queued_input_size, None);
            assert_eq!(stats.total_query_input_size, None);
        }
    }

    #[test]
    fn model_without_a_frontend_or_serving_pool_remains_advertised_inactive() {
        let identity = pool(1);
        let mut relay_only = model_load("relay-only", vec![identity.clone()]);
        relay_only.ready_frontends = None;
        relay_only.pending_first_output_requests = None;
        relay_only.pending_first_output_input_tokens = None;
        relay_only.live_input_tokens = None;
        relay_only.input_processing_requests = None;
        relay_only.output_generation_requests = None;
        relay_only.status = proto::DataStatus::Unavailable as i32;
        relay_only.expected_frontends = 1;
        relay_only.observed_frontends = 0;

        let snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            window_ms: 1_000,
            pools: vec![load_pool(identity)],
            models: vec![relay_only, model_load("frontend-only", Vec::new())],
        };
        let translated = load_snapshot_from_proto(snapshot).unwrap();

        assert_eq!(translated.relay_models.get("relay-only"), Some(&false));
        assert_eq!(
            translated.relay_models.get("relay-only-alias"),
            Some(&false)
        );
        assert_eq!(translated.relay_models.get("frontend-only"), Some(&false));
        assert_eq!(
            translated.relay_models.get("frontend-only-alias"),
            Some(&false)
        );
    }

    #[test]
    fn malformed_or_unknown_pool_identity_rejects_the_snapshot() {
        let mut malformed = pool(1);
        malformed.cache_semantics_digest.pop();
        let malformed_snapshot = proto::KvUsageSnapshot {
            metadata: Some(metadata()),
            pools: vec![proto::PoolKvUsage {
                pool: Some(malformed),
                models: vec![registration("model-a")],
                role: proto::WorkerRole::Aggregated as i32,
                block_size_tokens: 1,
                expected_ranks: 1,
                observed_ranks: 1,
                capacity_blocks: Some(1),
                used_blocks: Some(0),
                status: proto::DataStatus::Complete as i32,
                source_observed_at_unix_ms: 1,
            }],
        };
        assert!(kv_snapshot_from_proto(malformed_snapshot).is_err());

        let load_snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            window_ms: 1_000,
            pools: Vec::new(),
            models: vec![model_load("model-a", vec![pool(9)])],
        };
        assert!(load_snapshot_from_proto(load_snapshot).is_err());
    }
}
