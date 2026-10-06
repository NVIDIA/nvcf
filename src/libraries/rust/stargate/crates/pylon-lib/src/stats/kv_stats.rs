// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//! Translation from Dynamo's Relay-owned contract into Pylon model state.

use std::collections::{BTreeMap, HashMap, HashSet};

use stargate_proto::dynamo_pool_relay as proto;

use super::aggregator::{
    KvCacheStatsEnvelope, KvCacheStatsSnapshot, RelayLoadStatsEnvelope, RelayLoadStatsSnapshot,
};

#[derive(Clone, Debug, Hash, PartialEq, Eq)]
struct PoolKey {
    cache_semantics: [u8; 16],
    cache_source: i32,
    routing_scope: [u8; 16],
    routing_source: i32,
    locality_id: u64,
}

#[derive(Clone)]
struct UsagePool {
    roles: Vec<proto::WorkerRole>,
    identities: Vec<String>,
    capacity_tokens: Option<u64>,
    used_tokens: Option<u64>,
    source_observed_at_unix_ms: u64,
    complete: bool,
}

#[derive(Clone)]
struct LoadPool {
    roles: Vec<proto::WorkerRole>,
    live_workers: Option<u64>,
    max_concurrency: Option<u64>,
    complete: bool,
}

pub(super) struct RelayLoadTranslation {
    pub(super) stats: RelayLoadStatsEnvelope,
    pub(super) relay_models: BTreeMap<String, bool>,
}

#[derive(Clone, Copy)]
struct CounterBaseline {
    relay_incarnation: u64,
    observed_at_unix_ms: u64,
    input_tokens_total: Option<u64>,
    output_tokens_total: u64,
}

#[derive(Default)]
pub(super) struct RelayLoadTranslator {
    counters: HashMap<String, CounterBaseline>,
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
        let roles = worker_roles(&pool.roles)?;
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
            roles,
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
            let role = serving_role(indexes.iter().map(|index| pools[*index].roles.as_slice()));
            let selected = indexes
                .into_iter()
                .filter(|index| pools[*index].roles.contains(&role))
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

impl RelayLoadTranslator {
    pub(super) fn translate(
        &mut self,
        snapshot: proto::LoadSnapshot,
    ) -> anyhow::Result<RelayLoadTranslation> {
        let relay_incarnation = snapshot
            .metadata
            .as_ref()
            .ok_or_else(|| anyhow::anyhow!("load snapshot metadata is missing"))?
            .relay_incarnation;
        let mut pools = HashMap::<PoolKey, LoadPool>::new();
        for pool in snapshot.pools {
            let key = pool_key(pool.pool)?;
            let deployment = pool.deployment.unwrap_or_default();
            let load_pool = LoadPool {
                roles: worker_roles(&deployment.roles)?,
                live_workers: deployment.live_workers,
                max_concurrency: deployment.max_concurrency,
                complete: pool.load.is_some_and(|load| data_complete(load.status)),
            };
            anyhow::ensure!(
                pools.insert(key, load_pool).is_none(),
                "duplicate load pool"
            );
        }

        let mut identity_owner = HashMap::<String, String>::new();
        let mut seen_models = HashSet::new();
        let mut next_counters = HashMap::new();
        let mut relay_models = BTreeMap::new();
        let mut models = Vec::new();
        for model in snapshot.models {
            let registration = model
                .model
                .as_ref()
                .ok_or_else(|| anyhow::anyhow!("load model registration is missing"))?;
            let identities =
                registration_identities(std::slice::from_ref(registration), &mut identity_owner)?;
            let canonical_model = registration.model.trim();
            anyhow::ensure!(
                seen_models.insert(canonical_model.to_string()),
                "duplicate load model"
            );
            let proto::ModelDeploymentStatus {
                expected_frontends,
                observed_frontends,
                ready_frontends,
                serving_pools,
            } = model.deployment.unwrap_or_default();
            let load = model.load.unwrap_or_default();
            let tokens = load.tokens.unwrap_or_default();
            let serving_pools = serving_pools
                .into_iter()
                .map(|pool| pool_key(Some(pool)))
                .collect::<anyhow::Result<HashSet<_>>>()?;
            anyhow::ensure!(
                serving_pools.iter().all(|pool| pools.contains_key(pool)),
                "load model references an unknown serving pool"
            );
            let serving = serving_pools
                .iter()
                .filter_map(|key| pools.get(key))
                .collect::<Vec<_>>();
            let role = serving_role(serving.iter().map(|pool| pool.roles.as_slice()));
            let selected = serving
                .into_iter()
                .filter(|pool| pool.roles.contains(&role))
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
            let awaiting_first_token = load
                .requests
                .as_ref()
                .and_then(|requests| requests.requests_awaiting_first_token);
            let generating = load
                .requests
                .as_ref()
                .and_then(|requests| requests.requests_generating);
            let num_running_queries = awaiting_first_token
                .zip(generating)
                .and_then(|(awaiting, generating)| awaiting.checked_add(generating));
            let load_complete = data_complete(load.status)
                && expected_frontends > 0
                && observed_frontends == expected_frontends
                && load.source_observed_at_unix_ms > 0
                && num_running_queries.is_some();
            let rates = tokens
                .output_tokens_total
                .filter(|_| load_complete)
                .map(|output_tokens_total| CounterBaseline {
                    relay_incarnation,
                    observed_at_unix_ms: load.source_observed_at_unix_ms,
                    input_tokens_total: tokens.input_tokens_total,
                    output_tokens_total,
                })
                .map(|current| {
                    let rates = self
                        .counters
                        .get(canonical_model)
                        .and_then(|previous| counter_rates(*previous, current));
                    (current, rates)
                });
            if let Some((current, _)) = rates {
                next_counters.insert(canonical_model.to_string(), current);
            }
            let (input_tps, output_tps) = rates
                .and_then(|(_, rates)| rates)
                .map_or((None, None), |rates| rates);
            let stats_complete = load_complete && output_tps.is_some();
            let active =
                load_complete && !serving_pools.is_empty() && ready_frontends > 0 && scheduler_live;
            for identity in &identities {
                relay_models.insert(identity.clone(), active);
                models.push(RelayLoadStatsSnapshot {
                    model: identity.clone(),
                    input_tps: stats_complete.then_some(input_tps).flatten(),
                    output_tps: output_tps.unwrap_or_default(),
                    queue_size: awaiting_first_token.unwrap_or_default(),
                    queued_input_size: load_complete
                        .then_some(tokens.awaiting_first_token_input_tokens)
                        .flatten(),
                    num_running_queries: num_running_queries.unwrap_or_default(),
                    max_engine_concurrency,
                    total_query_input_size: load_complete
                        .then_some(tokens.inflight_input_tokens)
                        .flatten(),
                    input_processing_queries: awaiting_first_token.unwrap_or_default(),
                    output_generation_queries: generating.unwrap_or_default(),
                    source_observed_at_unix_ms: load.source_observed_at_unix_ms,
                    complete: stats_complete,
                });
            }
        }
        self.counters = next_counters;
        Ok(RelayLoadTranslation {
            stats: RelayLoadStatsEnvelope { models },
            relay_models,
        })
    }
}

fn counter_rates(
    previous: CounterBaseline,
    current: CounterBaseline,
) -> Option<(Option<f64>, Option<f64>)> {
    if previous.relay_incarnation != current.relay_incarnation {
        return None;
    }
    let elapsed_ms = current
        .observed_at_unix_ms
        .checked_sub(previous.observed_at_unix_ms)
        .filter(|elapsed| *elapsed > 0)?;
    let elapsed_seconds = elapsed_ms as f64 / 1_000.0;
    let input_tps = previous
        .input_tokens_total
        .zip(current.input_tokens_total)
        .and_then(|(previous, current)| current.checked_sub(previous))
        .map(|tokens| tokens as f64 / elapsed_seconds);
    let output_tps = current
        .output_tokens_total
        .checked_sub(previous.output_tokens_total)
        .map(|tokens| tokens as f64 / elapsed_seconds);
    Some((input_tps, output_tps))
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
        locality_id: pool.locality_id,
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

fn worker_roles(values: &[i32]) -> anyhow::Result<Vec<proto::WorkerRole>> {
    anyhow::ensure!(!values.is_empty(), "pool has no worker roles");
    values
        .iter()
        .map(|value| match proto::WorkerRole::try_from(*value) {
            Ok(
                role @ (proto::WorkerRole::Aggregated
                | proto::WorkerRole::Prefill
                | proto::WorkerRole::Decode
                | proto::WorkerRole::Encode),
            ) => Ok(role),
            _ => anyhow::bail!("invalid or unspecified worker role"),
        })
        .collect()
}

/// The role whose pools carry a model's KV capacity and decode load: aggregated
/// when any serving pool has aggregated workers, otherwise decode. Callers then
/// select every pool whose role set contains it, so a mixed prefill/decode pool
/// counts as decode and prefill- or encode-only pools never count.
fn serving_role<'a>(
    mut pool_roles: impl Iterator<Item = &'a [proto::WorkerRole]>,
) -> proto::WorkerRole {
    if pool_roles.any(|roles| roles.contains(&proto::WorkerRole::Aggregated)) {
        proto::WorkerRole::Aggregated
    } else {
        proto::WorkerRole::Decode
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
            relay_incarnation: 2,
            emitted_at_unix_ms: 99,
        }
    }

    fn pool(seed: u8) -> proto::PoolIdentity {
        proto::PoolIdentity {
            cache_semantics_digest: vec![seed; 16],
            cache_semantics_source: proto::IdentitySource::Explicit as i32,
            routing_scope_digest: vec![seed.wrapping_add(1); 16],
            routing_scope_source: proto::IdentitySource::DefaultDerived as i32,
            locality_id: u64::from(seed),
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

    fn load_pool(identity: proto::PoolIdentity) -> proto::PoolView {
        proto::PoolView {
            pool: Some(identity),
            load: Some(proto::LoadView {
                requests: None,
                tokens: Some(proto::TokenLoadStats {
                    active_prefill_tokens: Some(5),
                    active_decode_blocks: Some(6),
                    ..Default::default()
                }),
                status: proto::DataStatus::Complete as i32,
                source_observed_at_unix_ms: 4,
            }),
            deployment: Some(proto::PoolDeploymentStatus {
                roles: vec![proto::WorkerRole::Aggregated as i32],
                live_workers: Some(2),
                max_concurrency: Some(8),
            }),
        }
    }

    fn model_load(model: &str, serving_pools: Vec<proto::PoolIdentity>) -> proto::ModelView {
        proto::ModelView {
            model: Some(registration(model)),
            load: Some(proto::LoadView {
                requests: Some(proto::RequestLifecycleStats {
                    requests_started_total: 4,
                    requests_completed_total: 3,
                    requests_failed_total: 0,
                    requests_cancelled_total: 0,
                    requests_awaiting_first_token: Some(2),
                    requests_generating: Some(2),
                }),
                tokens: Some(proto::TokenLoadStats {
                    awaiting_first_token_input_tokens: Some(17),
                    inflight_input_tokens: Some(31),
                    input_tokens_total: Some(40),
                    output_tokens_total: Some(20),
                    ..Default::default()
                }),
                status: proto::DataStatus::Complete as i32,
                source_observed_at_unix_ms: 5_000,
            }),
            deployment: Some(proto::ModelDeploymentStatus {
                expected_frontends: 1,
                observed_frontends: 1,
                ready_frontends: 1,
                serving_pools,
            }),
        }
    }

    /// Sets the model's cumulative token counters and their observation time.
    fn with_totals(
        mut model: proto::ModelView,
        observed_at_unix_ms: u64,
        input_tokens_total: u64,
        output_tokens_total: u64,
    ) -> proto::ModelView {
        let load = model.load.as_mut().unwrap();
        load.source_observed_at_unix_ms = observed_at_unix_ms;
        let tokens = load.tokens.as_mut().unwrap();
        tokens.input_tokens_total = Some(input_tokens_total);
        tokens.output_tokens_total = Some(output_tokens_total);
        model
    }

    fn usage_pool(
        identity: proto::PoolIdentity,
        roles: &[proto::WorkerRole],
        capacity_blocks: u64,
        used_blocks: u64,
    ) -> proto::PoolKvUsage {
        proto::PoolKvUsage {
            pool: Some(identity),
            models: vec![registration("model-a")],
            roles: roles.iter().map(|role| *role as i32).collect(),
            block_size_tokens: 16,
            expected_ranks: 1,
            observed_ranks: 1,
            capacity_blocks: Some(capacity_blocks),
            used_blocks: Some(used_blocks),
            status: proto::DataStatus::Complete as i32,
            source_observed_at_unix_ms: 7,
        }
    }

    #[test]
    fn kv_usage_prefers_aggregated_pools_and_scales_blocks_to_tokens() {
        let mut decode = usage_pool(pool(2), &[proto::WorkerRole::Decode], 1_000, 900);
        decode.source_observed_at_unix_ms = 8;
        let snapshot = proto::KvUsageSnapshot {
            metadata: Some(metadata()),
            pools: vec![
                usage_pool(pool(1), &[proto::WorkerRole::Aggregated], 100, 40),
                decode,
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
    fn kv_usage_counts_mixed_role_pools_by_their_decode_role() {
        let snapshot = proto::KvUsageSnapshot {
            metadata: Some(metadata()),
            pools: vec![
                usage_pool(pool(1), &[proto::WorkerRole::Prefill], 1_000, 900),
                usage_pool(
                    pool(2),
                    &[proto::WorkerRole::Prefill, proto::WorkerRole::Decode],
                    100,
                    40,
                ),
                usage_pool(pool(3), &[proto::WorkerRole::Decode], 10, 5),
            ],
        };

        let translated = kv_snapshot_from_proto(snapshot).unwrap();
        for model in translated.models {
            assert_eq!(model.kv_cache_capacity_tokens, 1_760);
            assert_eq!(model.kv_cache_used_tokens, 720);
            assert!(model.complete);
        }
    }

    #[test]
    fn complete_load_activates_model_and_alias() {
        let identity = pool(1);
        let mut translator = RelayLoadTranslator::default();
        let baseline = with_totals(model_load("model-a", vec![identity.clone()]), 4_000, 0, 0);
        let first = translator
            .translate(proto::LoadSnapshot {
                metadata: Some(metadata()),
                pools: vec![load_pool(identity.clone())],
                models: vec![baseline],
            })
            .unwrap();
        assert_eq!(first.relay_models.get("model-a"), Some(&true));
        assert!(first.stats.models.iter().all(|model| !model.complete));
        let snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            pools: vec![load_pool(identity.clone())],
            models: vec![model_load("model-a", vec![identity])],
        };

        let translated = translator.translate(snapshot).unwrap();
        assert_eq!(translated.relay_models.get("model-a"), Some(&true));
        assert_eq!(translated.relay_models.get("model-a-alias"), Some(&true));
        assert_eq!(translated.stats.models.len(), 2);
        for model in translated.stats.models {
            assert_eq!(model.input_tps, Some(40.0));
            assert_eq!(model.output_tps, 20.0);
            assert_eq!(model.queue_size, 2);
            assert_eq!(model.queued_input_size, Some(17));
            assert_eq!(model.num_running_queries, 4);
            assert_eq!(model.max_engine_concurrency, Some(8));
            assert_eq!(model.total_query_input_size, Some(31));
            assert_eq!(model.input_processing_queries, 2);
            assert_eq!(model.output_generation_queries, 2);
            assert_eq!(model.source_observed_at_unix_ms, 5_000);
            assert!(model.complete);
        }
    }

    #[test]
    fn counter_reset_skips_one_rate_sample_then_recovers() {
        let identity = pool(1);
        let mut translator = RelayLoadTranslator::default();
        translator
            .translate(proto::LoadSnapshot {
                metadata: Some(metadata()),
                pools: vec![load_pool(identity.clone())],
                models: vec![model_load("model-a", vec![identity.clone()])],
            })
            .unwrap();

        let reset = with_totals(model_load("model-a", vec![identity.clone()]), 6_000, 4, 2);
        let reset = translator
            .translate(proto::LoadSnapshot {
                metadata: Some(metadata()),
                pools: vec![load_pool(identity.clone())],
                models: vec![reset],
            })
            .unwrap();
        assert_eq!(reset.relay_models.get("model-a"), Some(&true));
        assert!(reset.stats.models.iter().all(|model| !model.complete));

        let recovered = with_totals(model_load("model-a", vec![identity.clone()]), 7_000, 14, 7);
        let recovered = translator
            .translate(proto::LoadSnapshot {
                metadata: Some(metadata()),
                pools: vec![load_pool(identity)],
                models: vec![recovered],
            })
            .unwrap();
        for model in recovered.stats.models {
            assert!(model.complete);
            assert_eq!(model.input_tps, Some(10.0));
            assert_eq!(model.output_tps, 5.0);
        }
    }

    #[test]
    fn relay_restart_skips_one_rate_sample_even_when_totals_grow() {
        let identity = pool(1);
        let mut translator = RelayLoadTranslator::default();
        translator
            .translate(proto::LoadSnapshot {
                metadata: Some(metadata()),
                pools: vec![load_pool(identity.clone())],
                models: vec![model_load("model-a", vec![identity.clone()])],
            })
            .unwrap();
        let restarted = proto::RelayMessageMetadata {
            relay_incarnation: 3,
            ..metadata()
        };

        let first = translator
            .translate(proto::LoadSnapshot {
                metadata: Some(restarted),
                pools: vec![load_pool(identity.clone())],
                models: vec![with_totals(
                    model_load("model-a", vec![identity.clone()]),
                    6_000,
                    100,
                    50,
                )],
            })
            .unwrap();
        assert_eq!(first.relay_models.get("model-a"), Some(&true));
        assert!(first.stats.models.iter().all(|model| !model.complete));

        let second = translator
            .translate(proto::LoadSnapshot {
                metadata: Some(restarted),
                pools: vec![load_pool(identity.clone())],
                models: vec![with_totals(
                    model_load("model-a", vec![identity]),
                    7_000,
                    110,
                    55,
                )],
            })
            .unwrap();
        for model in second.stats.models {
            assert!(model.complete);
            assert_eq!(model.input_tps, Some(10.0));
            assert_eq!(model.output_tps, 5.0);
        }
    }

    #[test]
    fn duplicate_load_model_rejects_even_an_incomplete_snapshot() {
        let identity = pool(1);
        let mut first = model_load("model-a", vec![identity.clone()]);
        let load = first.load.as_mut().unwrap();
        load.status = proto::DataStatus::Unavailable as i32;
        load.tokens.as_mut().unwrap().output_tokens_total = None;
        let mut second = first.clone();
        second.deployment.as_mut().unwrap().serving_pools.clear();

        let result = RelayLoadTranslator::default().translate(proto::LoadSnapshot {
            metadata: Some(metadata()),
            pools: vec![load_pool(identity)],
            models: vec![first, second],
        });

        assert!(result.is_err());
    }

    #[test]
    fn unknown_exact_input_gauges_do_not_deactivate_the_model() {
        let identity = pool(1);
        let mut translator = RelayLoadTranslator::default();
        let baseline = with_totals(model_load("model-a", vec![identity.clone()]), 4_000, 0, 0);
        translator
            .translate(proto::LoadSnapshot {
                metadata: Some(metadata()),
                pools: vec![load_pool(identity.clone())],
                models: vec![baseline],
            })
            .unwrap();
        let mut model = model_load("model-a", vec![identity.clone()]);
        let tokens = model.load.as_mut().unwrap().tokens.as_mut().unwrap();
        tokens.awaiting_first_token_input_tokens = None;
        tokens.inflight_input_tokens = None;
        let snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            pools: vec![load_pool(identity)],
            models: vec![model],
        };

        let translated = translator.translate(snapshot).unwrap();

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
        let relay_only = proto::ModelView {
            model: Some(registration("relay-only")),
            load: Some(proto::LoadView {
                status: proto::DataStatus::Unavailable as i32,
                ..Default::default()
            }),
            deployment: Some(proto::ModelDeploymentStatus {
                expected_frontends: 1,
                serving_pools: vec![identity.clone()],
                ..Default::default()
            }),
        };

        let snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            pools: vec![load_pool(identity)],
            models: vec![relay_only, model_load("frontend-only", Vec::new())],
        };
        let translated = RelayLoadTranslator::default().translate(snapshot).unwrap();

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
            pools: vec![usage_pool(
                malformed,
                &[proto::WorkerRole::Aggregated],
                1,
                0,
            )],
        };
        assert!(kv_snapshot_from_proto(malformed_snapshot).is_err());

        let load_snapshot = proto::LoadSnapshot {
            metadata: Some(metadata()),
            pools: Vec::new(),
            models: vec![model_load("model-a", vec![pool(9)])],
        };
        assert!(
            RelayLoadTranslator::default()
                .translate(load_snapshot)
                .is_err()
        );
    }
}
