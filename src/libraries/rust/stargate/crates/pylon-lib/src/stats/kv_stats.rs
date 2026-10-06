// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//! Translation from the Dynamo KV DC Relay contract (`dynamo.kvrelay.v1`) into
//! Pylon model state.
//!
//! Load windows identify pools by `ProducerIdentity`; Pylon joins them to the
//! catalog by its stable `pool_id` alone. The remaining producer fields name a
//! KV-index generation, which changes neither usage nor load, so a producer
//! restart does not orphan a pool's load. Window entries for pools absent from
//! the latest catalog are dropped.
//!
//! Dynamo keys models by `(namespace, canonical_model_id)`, but Pylon advertises
//! model names. A model served from several namespaces is reported as the sum of
//! its per-namespace views, complete only when every view is complete, so a
//! rollout across namespaces neither hides capacity nor reads as lower load.

use std::collections::{BTreeMap, HashMap, HashSet};

use stargate_proto::dynamo_kvrelay as proto;

use super::aggregator::{
    KvCacheStatsEnvelope, KvCacheStatsSnapshot, RelayLoadStatsEnvelope, RelayLoadStatsSnapshot,
};

const POOL_IDENTITY_VERSION: u32 = 1;
const POOL_DIGEST_BYTES: usize = 16;

/// The latest complete `WatchKvPoolCatalog` snapshot.
#[derive(Debug, Default)]
pub(super) struct KvPoolCatalog {
    /// `None` quarantines a pool whose identity, roles, query semantics, or
    /// registrations Pylon does not understand: models it serves are reported
    /// incomplete rather than summed without it.
    pools: HashMap<proto::KvPoolId, Option<ServingPool>>,
    /// Canonical model ID to the pools registering it, across namespaces.
    models: BTreeMap<String, Vec<proto::KvPoolId>>,
    /// Advertised name (canonical ID or alias) to its canonical model ID;
    /// `None` when pools assign the name to different models, which omits it.
    names: BTreeMap<String, Option<String>>,
}

#[derive(Debug)]
struct ServingPool {
    roles: Vec<proto::WorkerRole>,
    kv_block_size: u32,
}

struct LoadPool {
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

/// Dynamo's serving-load key: `(namespace, canonical_model_id)`.
type ModelKey = (String, String);

#[derive(Default)]
pub(super) struct RelayLoadTranslator {
    counters: HashMap<ModelKey, CounterBaseline>,
}

impl KvPoolCatalog {
    pub(super) fn from_proto(update: proto::KvPoolCatalogUpdate) -> anyhow::Result<Self> {
        let snapshot = update
            .snapshot
            .ok_or_else(|| anyhow::anyhow!("pool catalog snapshot is missing"))?;
        let mut catalog = Self::default();
        for descriptor in snapshot.pools {
            let pool_id = descriptor
                .producer
                .and_then(|producer| producer.pool_id)
                .ok_or_else(|| anyhow::anyhow!("catalog pool ID is missing"))?;
            let mut registrations_valid = true;
            for registration in &descriptor.registrations {
                let canonical = &registration.canonical_model_id;
                if !valid_name(canonical) {
                    registrations_valid = false;
                    continue;
                }
                let pools = catalog.models.entry(canonical.clone()).or_default();
                if !pools.contains(&pool_id) {
                    pools.push(pool_id.clone());
                }
                for name in std::iter::once(canonical).chain(&registration.aliases) {
                    if valid_name(name) {
                        catalog.index_name(name, canonical);
                    } else {
                        registrations_valid = false;
                    }
                }
            }
            let serving = (registrations_valid && supported_pool_id(&pool_id))
                .then(|| {
                    Some(ServingPool {
                        roles: pool_roles(&descriptor.pool_roles)?,
                        kv_block_size: descriptor
                            .query_semantics
                            .map(|semantics| semantics.kv_block_size)
                            .filter(|size| *size > 0)?,
                    })
                })
                .flatten();
            if serving.is_none() {
                tracing::warn!(
                    ?pool_id,
                    "quarantining unsupported Dynamo Relay catalog pool"
                );
            }
            anyhow::ensure!(
                catalog.pools.insert(pool_id, serving).is_none(),
                "duplicate catalog pool"
            );
        }
        Ok(catalog)
    }

    fn index_name(&mut self, name: &str, canonical: &str) {
        match self.names.get_mut(name) {
            None => {
                self.names
                    .insert(name.to_string(), Some(canonical.to_string()));
            }
            Some(owner) if owner.as_deref() != Some(canonical) => *owner = None,
            Some(_) => {}
        }
    }

    /// Names Pylon advertises for a canonical model: the ID and its aliases,
    /// minus any name the catalog also assigns to another model.
    fn names_of(&self, canonical: &str) -> Vec<String> {
        if !self.names.contains_key(canonical) {
            return vec![canonical.to_string()];
        }
        self.names
            .iter()
            .filter(|(_, owner)| owner.as_deref() == Some(canonical))
            .map(|(name, _)| name.clone())
            .collect()
    }

    /// The pool's serving description, or `None` when it is absent or quarantined.
    fn serving_pool(&self, pool_id: &proto::KvPoolId) -> Option<&ServingPool> {
        self.pools.get(pool_id)?.as_ref()
    }
}

pub(super) fn kv_usage_from_proto(
    catalog: &KvPoolCatalog,
    update: proto::KvPoolLoadUpdate,
) -> anyhow::Result<KvCacheStatsEnvelope> {
    // Usage in tokens for pools whose aggregate is authoritative; `None` otherwise.
    let mut usage = HashMap::<proto::KvPoolId, Option<(u64, u64)>>::new();
    for entry in update.pools {
        let pool_id = entry
            .producer
            .and_then(|producer| producer.pool_id)
            .ok_or_else(|| anyhow::anyhow!("KV load pool ID is missing"))?;
        anyhow::ensure!(!usage.contains_key(&pool_id), "duplicate KV load pool");
        let Some(pool) = catalog.pools.get(&pool_id) else {
            continue;
        };
        // Incomplete rank coverage and unknown (zero) capacity are not zero load.
        let authoritative = entry.kv_expected_ranks > 0
            && entry.kv_observed_ranks == entry.kv_expected_ranks
            && entry.total_kv_blocks > 0
            && entry.kv_used_blocks <= entry.total_kv_blocks;
        let tokens = pool.as_ref().filter(|_| authoritative).and_then(|pool| {
            let block_size = u64::from(pool.kv_block_size);
            Some((
                entry.total_kv_blocks.checked_mul(block_size)?,
                entry.kv_used_blocks.checked_mul(block_size)?,
            ))
        });
        usage.insert(pool_id, tokens);
    }

    let mut models = Vec::new();
    for (canonical, pool_ids) in &catalog.models {
        let pools = pool_ids
            .iter()
            .map(|pool_id| Some((catalog.serving_pool(pool_id)?, pool_id)))
            .collect::<Option<Vec<_>>>();
        let totals = pools.and_then(|pools| {
            let role = serving_role(pools.iter().map(|(pool, _)| pool.roles.as_slice()));
            let selected = pools
                .iter()
                .filter(|(pool, _)| pool.roles.contains(&role))
                .collect::<Vec<_>>();
            if selected.is_empty() {
                return None;
            }
            selected
                .into_iter()
                .try_fold((0_u64, 0_u64), |(capacity, used), (_, pool_id)| {
                    let (pool_capacity, pool_used) = (*usage.get(*pool_id)?)?;
                    Some((
                        capacity.checked_add(pool_capacity)?,
                        used.checked_add(pool_used)?,
                    ))
                })
        });
        let (capacity, used) = totals.unwrap_or_default();
        for name in catalog.names_of(canonical) {
            models.push(KvCacheStatsSnapshot {
                model: name,
                aliases: Vec::new(),
                kv_cache_capacity_tokens: capacity,
                kv_cache_used_tokens: used,
                kv_cache_free_tokens: capacity - used,
                source_observed_at_unix_ms: update.observed_ms,
                complete: totals.is_some(),
            });
        }
    }
    Ok(KvCacheStatsEnvelope { models })
}

impl RelayLoadTranslator {
    pub(super) fn translate(
        &mut self,
        catalog: &KvPoolCatalog,
        update: proto::ServingLoadUpdate,
    ) -> anyhow::Result<RelayLoadTranslation> {
        let relay_incarnation = update
            .relay
            .ok_or_else(|| anyhow::anyhow!("serving-load Relay identity is missing"))?
            .relay_incarnation;
        let mut pools = HashMap::<proto::KvPoolId, LoadPool>::new();
        for pool in update.pools {
            let pool_id = pool
                .producer
                .and_then(|producer| producer.pool_id)
                .ok_or_else(|| anyhow::anyhow!("serving-load pool ID is missing"))?;
            let deployment = pool.deployment.unwrap_or_default();
            let load_pool = LoadPool {
                live_workers: deployment.live_workers,
                max_concurrency: deployment.max_concurrency,
                complete: pool.load.is_some_and(|load| data_complete(load.status)),
            };
            anyhow::ensure!(
                pools.insert(pool_id, load_pool).is_none(),
                "duplicate serving-load pool"
            );
        }

        let mut seen_models = HashSet::new();
        let mut next_counters = HashMap::new();
        let mut by_canonical = BTreeMap::<String, (RelayLoadStatsSnapshot, bool)>::new();
        for model in update.models {
            anyhow::ensure!(
                valid_name(&model.canonical_model_id),
                "serving-load canonical model ID is invalid"
            );
            let key = (model.namespace, model.canonical_model_id);
            anyhow::ensure!(
                seen_models.insert(key.clone()),
                "duplicate serving-load model"
            );
            let proto::ModelDeploymentStatus {
                expected_frontends,
                observed_frontends,
                ready_frontends,
                serving_pools,
            } = model.deployment.unwrap_or_default();
            let load = model.load.unwrap_or_default();
            let tokens = load.tokens.unwrap_or_default();
            let serving = serving_pools
                .iter()
                .filter_map(|pool_id| Some((catalog.serving_pool(pool_id)?, pools.get(pool_id)?)))
                .collect::<Vec<_>>();
            let all_pools_known = serving.len() == serving_pools.len();
            let role = serving_role(serving.iter().map(|(pool, _)| pool.roles.as_slice()));
            let selected = serving
                .into_iter()
                .filter(|(pool, _)| pool.roles.contains(&role))
                .map(|(_, load)| load)
                .collect::<Vec<_>>();
            let scheduler_live = selected
                .iter()
                .any(|pool| pool.complete && pool.live_workers.is_some_and(|workers| workers > 0));
            let max_engine_concurrency = (all_pools_known
                && !selected.is_empty()
                && selected.iter().all(|pool| pool.complete))
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
                && load.source_observed_ms > 0
                && num_running_queries.is_some();
            let rates = tokens
                .output_tokens_total
                .filter(|_| load_complete)
                .map(|output_tokens_total| CounterBaseline {
                    relay_incarnation,
                    observed_at_unix_ms: load.source_observed_ms,
                    input_tokens_total: tokens.input_tokens_total,
                    output_tokens_total,
                })
                .map(|current| {
                    let rates = self
                        .counters
                        .get(&key)
                        .and_then(|previous| counter_rates(*previous, current));
                    (current, rates)
                });
            if let Some((current, _)) = rates {
                next_counters.insert(key.clone(), current);
            }
            let (input_tps, output_tps) = rates
                .and_then(|(_, rates)| rates)
                .map_or((None, None), |rates| rates);
            let stats_complete = load_complete && output_tps.is_some();
            let active =
                load_complete && !serving_pools.is_empty() && ready_frontends > 0 && scheduler_live;
            let stats = RelayLoadStatsSnapshot {
                model: String::new(),
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
                source_observed_at_unix_ms: load.source_observed_ms,
                complete: stats_complete,
            };
            let (_, canonical) = key;
            match by_canonical.get_mut(&canonical) {
                Some((total, total_active)) => {
                    add_namespace_load(total, &stats);
                    *total_active |= active;
                }
                None => {
                    by_canonical.insert(canonical, (stats, active));
                }
            }
        }
        self.counters = next_counters;

        let mut relay_models = BTreeMap::new();
        let mut models = Vec::new();
        for (canonical, (stats, active)) in by_canonical {
            for name in catalog.names_of(&canonical) {
                relay_models.insert(name.clone(), active);
                models.push(RelayLoadStatsSnapshot {
                    model: name,
                    ..stats.clone()
                });
            }
        }
        Ok(RelayLoadTranslation {
            stats: RelayLoadStatsEnvelope { models },
            relay_models,
        })
    }
}

/// Adds one namespace's view of a model to the total of its other namespaces.
/// Optional values stay known only when known in every view.
fn add_namespace_load(total: &mut RelayLoadStatsSnapshot, view: &RelayLoadStatsSnapshot) {
    fn add(total: Option<u64>, view: Option<u64>) -> Option<u64> {
        total?.checked_add(view?)
    }
    total.complete &= view.complete;
    total.input_tps = total.input_tps.zip(view.input_tps).map(|(a, b)| a + b);
    total.output_tps += view.output_tps;
    total.queue_size = total.queue_size.saturating_add(view.queue_size);
    total.queued_input_size = add(total.queued_input_size, view.queued_input_size);
    total.num_running_queries = total
        .num_running_queries
        .saturating_add(view.num_running_queries);
    total.max_engine_concurrency = add(total.max_engine_concurrency, view.max_engine_concurrency);
    total.total_query_input_size = add(total.total_query_input_size, view.total_query_input_size);
    total.input_processing_queries = total
        .input_processing_queries
        .saturating_add(view.input_processing_queries);
    total.output_generation_queries = total
        .output_generation_queries
        .saturating_add(view.output_generation_queries);
    total.source_observed_at_unix_ms = total
        .source_observed_at_unix_ms
        .min(view.source_observed_at_unix_ms);
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

fn supported_pool_id(pool_id: &proto::KvPoolId) -> bool {
    let supported_digest = |digest: Option<&proto::DigestIdentity>| {
        digest.is_some_and(|digest| {
            digest.digest.len() == POOL_DIGEST_BYTES
                && matches!(
                    proto::IdentitySource::try_from(digest.source),
                    Ok(proto::IdentitySource::DefaultDerived | proto::IdentitySource::Explicit)
                )
        })
    };
    pool_id.identity_version == POOL_IDENTITY_VERSION
        && pool_id.indexer_domain.as_ref().is_some_and(|domain| {
            supported_digest(domain.cache_semantics.as_ref())
                && supported_digest(domain.routing_scope.as_ref())
        })
}

/// Declared pool roles, or `None` when empty or any role is unknown. `LEGACY`
/// marks a worker card without a role, which Dynamo serves as an aggregated
/// worker, so it counts as `AGGREGATED`.
fn pool_roles(values: &[i32]) -> Option<Vec<proto::WorkerRole>> {
    if values.is_empty() {
        return None;
    }
    values
        .iter()
        .map(|value| match proto::WorkerRole::try_from(*value).ok()? {
            proto::WorkerRole::Legacy => Some(proto::WorkerRole::Aggregated),
            proto::WorkerRole::Unspecified => None,
            role => Some(role),
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

fn valid_name(name: &str) -> bool {
    !name.is_empty() && name.trim() == name
}

#[cfg(test)]
mod tests {
    use super::*;

    const RELAY: proto::RelayIdentity = proto::RelayIdentity {
        drt_instance_id: 7,
        relay_incarnation: 2,
    };

    fn pool_id(seed: u8) -> proto::KvPoolId {
        let digest = |seed: u8, source: proto::IdentitySource| proto::DigestIdentity {
            digest: vec![seed; 16],
            source: source as i32,
        };
        proto::KvPoolId {
            identity_version: POOL_IDENTITY_VERSION,
            indexer_domain: Some(proto::IndexerDomainId {
                cache_semantics: Some(digest(seed, proto::IdentitySource::Explicit)),
                routing_scope: Some(digest(
                    seed.wrapping_add(1),
                    proto::IdentitySource::DefaultDerived,
                )),
            }),
            dc_id: u64::from(seed),
        }
    }

    fn producer(seed: u8) -> proto::ProducerIdentity {
        proto::ProducerIdentity {
            pool_id: Some(pool_id(seed)),
            producer_incarnation: 1,
            layout_generation: 1,
            ckf_format: None,
        }
    }

    fn registration(model: &str) -> proto::ModelRegistration {
        proto::ModelRegistration {
            canonical_model_id: model.to_string(),
            target: Some(proto::ModelTarget {
                target: Some(proto::model_target::Target::Base(proto::BaseModelTarget {
                    base_model: model.to_string(),
                })),
            }),
            aliases: vec![format!("{model}-alias")],
        }
    }

    fn descriptor(
        seed: u8,
        roles: &[proto::WorkerRole],
        models: &[&str],
    ) -> proto::KvPoolDescriptor {
        proto::KvPoolDescriptor {
            producer: Some(producer(seed)),
            serving_endpoint: None,
            registrations: models.iter().map(|model| registration(model)).collect(),
            query_semantics: Some(proto::KvQuerySemantics {
                kv_block_size: 16,
                hash_format: proto::KvQueryHashFormat::DynamoStandardV1 as i32,
            }),
            pool_roles: roles.iter().map(|role| *role as i32).collect(),
        }
    }

    fn catalog(pools: Vec<proto::KvPoolDescriptor>) -> KvPoolCatalog {
        KvPoolCatalog::from_proto(proto::KvPoolCatalogUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            revision: 1,
            snapshot: Some(proto::KvPoolCatalogSnapshot { pools }),
            contract_marker: proto::CONTRACT_MARKER,
        })
        .unwrap()
    }

    fn aggregated_catalog(models: &[&str]) -> KvPoolCatalog {
        catalog(vec![descriptor(
            1,
            &[proto::WorkerRole::Aggregated],
            models,
        )])
    }

    fn usage(seed: u8, total_kv_blocks: u64, kv_used_blocks: u64) -> proto::KvPoolLoadEntry {
        proto::KvPoolLoadEntry {
            producer: Some(producer(seed)),
            kv_used_blocks,
            total_kv_blocks,
            kv_observed_ranks: 1,
            kv_expected_ranks: 1,
        }
    }

    fn usage_window(pools: Vec<proto::KvPoolLoadEntry>) -> proto::KvPoolLoadUpdate {
        proto::KvPoolLoadUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            window_sequence: 1,
            observed_ms: 7,
            window_ms: 1_000,
            pools,
            contract_marker: proto::CONTRACT_MARKER,
        }
    }

    fn load_pool(seed: u8) -> proto::PoolServingLoad {
        proto::PoolServingLoad {
            producer: Some(producer(seed)),
            load: Some(proto::LoadView {
                requests: None,
                tokens: Some(proto::TokenLoadStats {
                    active_prefill_tokens: Some(5),
                    active_decode_blocks: Some(6),
                    ..Default::default()
                }),
                status: proto::DataStatus::Complete as i32,
                source_observed_ms: 4,
            }),
            deployment: Some(proto::PoolDeploymentStatus {
                live_workers: Some(2),
                max_concurrency: Some(8),
            }),
        }
    }

    fn model_load(model: &str, serving_pools: Vec<proto::KvPoolId>) -> proto::ModelServingLoad {
        proto::ModelServingLoad {
            namespace: "dynamo".to_string(),
            canonical_model_id: model.to_string(),
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
                source_observed_ms: 5_000,
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
        mut model: proto::ModelServingLoad,
        observed_ms: u64,
        input_tokens_total: u64,
        output_tokens_total: u64,
    ) -> proto::ModelServingLoad {
        let load = model.load.as_mut().unwrap();
        load.source_observed_ms = observed_ms;
        let tokens = load.tokens.as_mut().unwrap();
        tokens.input_tokens_total = Some(input_tokens_total);
        tokens.output_tokens_total = Some(output_tokens_total);
        model
    }

    fn load_window(
        relay: proto::RelayIdentity,
        pools: Vec<proto::PoolServingLoad>,
        models: Vec<proto::ModelServingLoad>,
    ) -> proto::ServingLoadUpdate {
        proto::ServingLoadUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(relay),
            window_sequence: 1,
            observed_ms: 1,
            window_ms: 1_000,
            pools,
            models,
            contract_marker: proto::CONTRACT_MARKER,
        }
    }

    /// Translates two windows a second apart so the second carries rates.
    fn translate_with_rates(
        catalog: &KvPoolCatalog,
        pools: impl Fn() -> Vec<proto::PoolServingLoad>,
        models: impl Fn() -> Vec<proto::ModelServingLoad>,
    ) -> RelayLoadTranslation {
        let mut translator = RelayLoadTranslator::default();
        let baseline = models()
            .into_iter()
            .map(|model| with_totals(model, 4_000, 0, 0))
            .collect();
        translator
            .translate(catalog, load_window(RELAY, pools(), baseline))
            .unwrap();
        translator
            .translate(catalog, load_window(RELAY, pools(), models()))
            .unwrap()
    }

    #[test]
    fn kv_usage_prefers_aggregated_pools_and_scales_blocks_to_tokens() {
        let catalog = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            descriptor(2, &[proto::WorkerRole::Decode], &["model-a"]),
        ]);

        let translated = kv_usage_from_proto(
            &catalog,
            usage_window(vec![usage(1, 100, 40), usage(2, 1_000, 900)]),
        )
        .unwrap();

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
        let catalog = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Prefill], &["model-a"]),
            descriptor(
                2,
                &[proto::WorkerRole::Prefill, proto::WorkerRole::Decode],
                &["model-a"],
            ),
            descriptor(3, &[proto::WorkerRole::Decode], &["model-a"]),
        ]);

        let translated = kv_usage_from_proto(
            &catalog,
            usage_window(vec![
                usage(1, 1_000, 900),
                usage(2, 100, 40),
                usage(3, 10, 5),
            ]),
        )
        .unwrap();

        for model in translated.models {
            assert_eq!(model.kv_cache_capacity_tokens, 1_760);
            assert_eq!(model.kv_cache_used_tokens, 720);
            assert!(model.complete);
        }
    }

    #[test]
    fn kv_usage_without_full_rank_coverage_capacity_or_window_entry_is_incomplete() {
        let catalog = aggregated_catalog(&["model-a"]);
        let mut partial = usage(1, 100, 40);
        partial.kv_observed_ranks = 0;

        for window in [
            vec![partial],
            vec![usage(1, 0, 0)],
            Vec::new(),
            // A pool absent from the catalog is dropped, not attributed.
            vec![usage(9, 100, 40)],
        ] {
            let translated = kv_usage_from_proto(&catalog, usage_window(window)).unwrap();
            assert_eq!(translated.models.len(), 2);
            for model in translated.models {
                assert!(!model.complete);
                assert_eq!(model.kv_cache_capacity_tokens, 0);
            }
        }
    }

    #[test]
    fn legacy_role_counts_as_aggregated_and_unknown_role_quarantines_its_pool() {
        let legacy = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Legacy], &["model-a"]),
            descriptor(2, &[proto::WorkerRole::Decode], &["model-a"]),
        ]);
        let window = || usage_window(vec![usage(1, 100, 40), usage(2, 1_000, 900)]);
        for model in kv_usage_from_proto(&legacy, window()).unwrap().models {
            assert!(model.complete);
            assert_eq!(model.kv_cache_capacity_tokens, 1_600);
        }

        let mut unknown = descriptor(2, &[proto::WorkerRole::Decode], &["model-a"]);
        unknown.pool_roles = vec![99];
        let quarantined = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            unknown,
        ]);
        for model in kv_usage_from_proto(&quarantined, window()).unwrap().models {
            assert!(!model.complete);
        }
    }

    #[test]
    fn names_assigned_to_two_models_are_omitted() {
        let mut other = descriptor(2, &[proto::WorkerRole::Aggregated], &["model-b"]);
        other.registrations[0].aliases = vec!["model-a-alias".to_string()];
        let catalog = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            other,
        ]);

        let names = kv_usage_from_proto(&catalog, usage_window(Vec::new()))
            .unwrap()
            .models
            .into_iter()
            .map(|model| model.model)
            .collect::<Vec<_>>();

        assert_eq!(names, ["model-a", "model-b"]);
    }

    #[test]
    fn complete_load_activates_model_and_alias() {
        let catalog = aggregated_catalog(&["model-a"]);
        let mut translator = RelayLoadTranslator::default();
        let baseline = with_totals(model_load("model-a", vec![pool_id(1)]), 4_000, 0, 0);
        let first = translator
            .translate(
                &catalog,
                load_window(RELAY, vec![load_pool(1)], vec![baseline]),
            )
            .unwrap();
        assert_eq!(first.relay_models.get("model-a"), Some(&true));
        assert!(first.stats.models.iter().all(|model| !model.complete));

        let translated = translator
            .translate(
                &catalog,
                load_window(
                    RELAY,
                    vec![load_pool(1)],
                    vec![model_load("model-a", vec![pool_id(1)])],
                ),
            )
            .unwrap();

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
        let catalog = aggregated_catalog(&["model-a"]);
        let mut translator = RelayLoadTranslator::default();
        let mut translate = |observed_ms, input, output| {
            let model = with_totals(
                model_load("model-a", vec![pool_id(1)]),
                observed_ms,
                input,
                output,
            );
            translator
                .translate(
                    &catalog,
                    load_window(RELAY, vec![load_pool(1)], vec![model]),
                )
                .unwrap()
        };
        translate(5_000, 40, 20);

        let reset = translate(6_000, 4, 2);
        assert_eq!(reset.relay_models.get("model-a"), Some(&true));
        assert!(reset.stats.models.iter().all(|model| !model.complete));

        for model in translate(7_000, 14, 7).stats.models {
            assert!(model.complete);
            assert_eq!(model.input_tps, Some(10.0));
            assert_eq!(model.output_tps, 5.0);
        }
    }

    #[test]
    fn relay_restart_skips_one_rate_sample_even_when_totals_grow() {
        let catalog = aggregated_catalog(&["model-a"]);
        let mut translator = RelayLoadTranslator::default();
        let mut translate = |relay, observed_ms, input, output| {
            let model = with_totals(
                model_load("model-a", vec![pool_id(1)]),
                observed_ms,
                input,
                output,
            );
            translator
                .translate(
                    &catalog,
                    load_window(relay, vec![load_pool(1)], vec![model]),
                )
                .unwrap()
        };
        translate(RELAY, 5_000, 40, 20);
        let restarted = proto::RelayIdentity {
            relay_incarnation: 3,
            ..RELAY
        };

        let first = translate(restarted, 6_000, 100, 50);
        assert_eq!(first.relay_models.get("model-a"), Some(&true));
        assert!(first.stats.models.iter().all(|model| !model.complete));

        for model in translate(restarted, 7_000, 110, 55).stats.models {
            assert!(model.complete);
            assert_eq!(model.input_tps, Some(10.0));
            assert_eq!(model.output_tps, 5.0);
        }
    }

    #[test]
    fn duplicate_load_model_rejects_even_an_incomplete_window() {
        let mut first = model_load("model-a", vec![pool_id(1)]);
        first.load.as_mut().unwrap().status = proto::DataStatus::Unavailable as i32;
        let mut second = first.clone();
        second.deployment.as_mut().unwrap().serving_pools.clear();

        let result = RelayLoadTranslator::default().translate(
            &aggregated_catalog(&["model-a"]),
            load_window(RELAY, vec![load_pool(1)], vec![first, second]),
        );

        assert!(result.is_err());
    }

    #[test]
    fn namespaces_serving_one_model_are_summed() {
        let catalog = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            descriptor(2, &[proto::WorkerRole::Aggregated], &["model-a"]),
        ]);
        let models = || {
            let mut blue = model_load("model-a", vec![pool_id(1)]);
            blue.namespace = "blue".to_string();
            let mut green = model_load("model-a", vec![pool_id(2)]);
            green.namespace = "green".to_string();
            vec![blue, green]
        };

        let translated =
            translate_with_rates(&catalog, || vec![load_pool(1), load_pool(2)], models);

        assert_eq!(translated.relay_models.get("model-a"), Some(&true));
        for model in translated.stats.models {
            assert!(model.complete);
            assert_eq!(model.input_tps, Some(80.0));
            assert_eq!(model.output_tps, 40.0);
            assert_eq!(model.num_running_queries, 8);
            assert_eq!(model.max_engine_concurrency, Some(16));
            assert_eq!(model.total_query_input_size, Some(62));
        }

        let degraded = || {
            let mut models = models();
            models[1].load.as_mut().unwrap().status = proto::DataStatus::Degraded as i32;
            models
        };
        let translated =
            translate_with_rates(&catalog, || vec![load_pool(1), load_pool(2)], degraded);
        assert_eq!(translated.relay_models.get("model-a"), Some(&true));
        assert!(translated.stats.models.iter().all(|model| !model.complete));
    }

    #[test]
    fn unresolved_serving_pool_leaves_concurrency_unknown() {
        let translated = translate_with_rates(
            &aggregated_catalog(&["model-a"]),
            || vec![load_pool(1), load_pool(9)],
            || vec![model_load("model-a", vec![pool_id(1), pool_id(9)])],
        );

        assert_eq!(translated.relay_models.get("model-a"), Some(&true));
        for model in translated.stats.models {
            assert!(model.complete);
            assert_eq!(model.max_engine_concurrency, None);
        }
    }

    #[test]
    fn unknown_exact_input_gauges_do_not_deactivate_the_model() {
        let model = || {
            let mut model = model_load("model-a", vec![pool_id(1)]);
            let tokens = model.load.as_mut().unwrap().tokens.as_mut().unwrap();
            tokens.awaiting_first_token_input_tokens = None;
            tokens.inflight_input_tokens = None;
            vec![model]
        };

        let translated = translate_with_rates(
            &aggregated_catalog(&["model-a"]),
            || vec![load_pool(1)],
            model,
        );

        assert_eq!(translated.relay_models.get("model-a"), Some(&true));
        for stats in translated.stats.models {
            assert!(stats.complete);
            assert_eq!(stats.queued_input_size, None);
            assert_eq!(stats.total_query_input_size, None);
        }
    }

    #[test]
    fn model_without_a_frontend_or_serving_pool_remains_advertised_inactive() {
        let relay_only = proto::ModelServingLoad {
            namespace: "dynamo".to_string(),
            canonical_model_id: "relay-only".to_string(),
            load: Some(proto::LoadView {
                status: proto::DataStatus::Unavailable as i32,
                ..Default::default()
            }),
            deployment: Some(proto::ModelDeploymentStatus {
                expected_frontends: 1,
                serving_pools: vec![pool_id(1)],
                ..Default::default()
            }),
        };

        let translated = RelayLoadTranslator::default()
            .translate(
                &aggregated_catalog(&["relay-only"]),
                load_window(
                    RELAY,
                    vec![load_pool(1)],
                    vec![relay_only, model_load("frontend-only", Vec::new())],
                ),
            )
            .unwrap();

        assert_eq!(translated.relay_models.get("relay-only"), Some(&false));
        assert_eq!(
            translated.relay_models.get("relay-only-alias"),
            Some(&false)
        );
        assert_eq!(translated.relay_models.get("frontend-only"), Some(&false));
    }

    #[test]
    fn unidentifiable_entries_reject_the_update() {
        let mut anonymous = descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]);
        anonymous.producer = None;
        assert!(
            KvPoolCatalog::from_proto(proto::KvPoolCatalogUpdate {
                snapshot: Some(proto::KvPoolCatalogSnapshot {
                    pools: vec![anonymous],
                }),
                ..Default::default()
            })
            .is_err()
        );

        let catalog = aggregated_catalog(&["model-a"]);
        assert!(
            kv_usage_from_proto(&catalog, usage_window(vec![usage(1, 1, 0), usage(1, 1, 0)]))
                .is_err()
        );
        let mut anonymous = load_pool(1);
        anonymous.producer = None;
        assert!(
            RelayLoadTranslator::default()
                .translate(&catalog, load_window(RELAY, vec![anonymous], Vec::new()))
                .is_err()
        );
    }
}
