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

//! Translation from the Dynamo KV DC Relay contract (`dynamo.kvrelay.v1`) into
//! the stats collector's Relay updates.
//!
//! Load windows identify pools by `ProducerIdentity`; this joins them to the
//! catalog by its stable `pool_id` alone. The remaining producer fields name a
//! KV-index generation, which changes neither usage nor load, so a producer
//! restart does not orphan a pool's load. Window entries for pools absent from
//! the latest catalog are dropped.
//!
//! Dynamo keys models by `(namespace, canonical_model_id)`, but Pylon advertises
//! model names. A model served from several namespaces is reported as the sum of
//! its per-namespace load, so a rollout across namespaces neither hides capacity
//! nor reads as lower load.

use std::collections::{BTreeMap, HashMap, HashSet};

use stargate_proto::dynamo_kvrelay as proto;

use super::relay_update::{
    RelayKvUpdate, RelayLoadUpdate, RelayModelGauges, RelayModelKv, RelayModelLoad,
};

const POOL_IDENTITY_VERSION: u32 = 1;
const POOL_DIGEST_BYTES: usize = 16;

/// The latest `WatchKvPoolCatalog` snapshot.
#[derive(Debug, Default)]
pub(super) struct KvPoolCatalog {
    /// `None` quarantines a pool whose identity, roles, query semantics, or
    /// registrations Pylon does not understand: models it serves are reported
    /// without usage rather than summed without it.
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
    max_concurrency: Option<u64>,
    complete: bool,
}

/// One namespace's view of a model.
struct NamespaceView {
    ready: bool,
    /// Whether the namespace has frontends, which carry all of its traffic.
    has_frontends: bool,
    load: Option<RelayModelGauges>,
}

#[derive(Clone, Copy)]
struct CounterBaseline {
    relay_incarnation: u64,
    /// The window's Relay clock. Totals grow as the Relay receives frontend
    /// reports, so rates are measured on that clock, not the reports'.
    observed_at_unix_ms: u64,
    output_tokens_total: Option<u64>,
}

/// Dynamo's serving-load key: `(namespace, canonical_model_id)`.
type ModelKey = (String, String);

/// Turns serving-load windows into [`RelayLoadUpdate`]s. It outlives sessions:
/// its counter baselines are fenced by `relay_incarnation`.
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

/// KV usage per advertised model name. A model is present only when every pool
/// carrying its KV reports full rank coverage of a known capacity.
pub(super) fn kv_update(
    catalog: &KvPoolCatalog,
    update: proto::KvPoolLoadUpdate,
) -> anyhow::Result<RelayKvUpdate> {
    // Usage in tokens of each pool whose aggregate is authoritative.
    let mut usage = HashMap::<proto::KvPoolId, Option<(u64, u64)>>::new();
    for entry in update.pools {
        let pool_id = entry
            .producer
            .and_then(|producer| producer.pool_id)
            .ok_or_else(|| anyhow::anyhow!("KV load pool ID is missing"))?;
        anyhow::ensure!(!usage.contains_key(&pool_id), "duplicate KV load pool");
        let Some(pool) = catalog.serving_pool(&pool_id) else {
            continue;
        };
        // Incomplete rank coverage and unknown (zero) capacity are not zero load.
        let authoritative = entry.kv_expected_ranks > 0
            && entry.kv_observed_ranks == entry.kv_expected_ranks
            && entry.total_kv_blocks > 0
            && entry.kv_used_blocks <= entry.total_kv_blocks;
        let block_size = u64::from(pool.kv_block_size);
        let tokens = authoritative
            .then(|| {
                Some((
                    entry.total_kv_blocks.checked_mul(block_size)?,
                    entry.kv_used_blocks.checked_mul(block_size)?,
                ))
            })
            .flatten();
        usage.insert(pool_id, tokens);
    }

    let mut models = BTreeMap::new();
    for (canonical, pool_ids) in &catalog.models {
        let Some(pools) = pool_ids
            .iter()
            .map(|pool_id| Some((pool_id, catalog.serving_pool(pool_id)?)))
            .collect::<Option<Vec<_>>>()
        else {
            continue;
        };
        let role = serving_role(pools.iter().map(|(_, pool)| pool.roles.as_slice()));
        let totals = pools
            .iter()
            .filter(|(_, pool)| pool.roles.contains(&role))
            .try_fold((0_u64, 0_u64), |(capacity, used), (pool_id, _)| {
                let (pool_capacity, pool_used) = (*usage.get(*pool_id)?)?;
                Some((
                    capacity.checked_add(pool_capacity)?,
                    used.checked_add(pool_used)?,
                ))
            });
        let Some((capacity_tokens, used_tokens)) = totals.filter(|(capacity, _)| *capacity > 0)
        else {
            continue;
        };
        for name in catalog.names_of(canonical) {
            models.insert(
                name,
                RelayModelKv {
                    capacity_tokens,
                    used_tokens,
                },
            );
        }
    }
    Ok(RelayKvUpdate {
        observed_at_unix_ms: update.observed_ms,
        models,
    })
}

impl RelayLoadTranslator {
    pub(super) fn translate(
        &mut self,
        catalog: &KvPoolCatalog,
        update: proto::ServingLoadUpdate,
    ) -> anyhow::Result<RelayLoadUpdate> {
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
            let load_pool = LoadPool {
                max_concurrency: pool.deployment.unwrap_or_default().max_concurrency,
                complete: pool.load.is_some_and(|load| data_complete(load.status)),
            };
            anyhow::ensure!(
                pools.insert(pool_id, load_pool).is_none(),
                "duplicate serving-load pool"
            );
        }

        let mut seen_models = HashSet::new();
        let mut next_counters = HashMap::new();
        let mut views = BTreeMap::<String, Vec<NamespaceView>>::new();
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
            let deployment = model.deployment.unwrap_or_default();
            let load = model.load.unwrap_or_default();
            let mut gauges = complete_gauges(&load, &deployment);
            if let Some(gauges) = &mut gauges {
                gauges.max_engine_concurrency =
                    max_engine_concurrency(catalog, &pools, &deployment.serving_pools);
                let current = CounterBaseline {
                    relay_incarnation,
                    observed_at_unix_ms: update.observed_ms,
                    output_tokens_total: load.tokens.and_then(|tokens| tokens.output_tokens_total),
                };
                if let Some(previous) = self.counters.get(&key) {
                    gauges.output_tps = output_tps(*previous, current);
                }
                next_counters.insert(key.clone(), current);
            }
            let (_, canonical) = key;
            views.entry(canonical).or_default().push(NamespaceView {
                // DataStatus says only whether every expected frontend
                // reported; whether the model is served is the Relay's
                // readiness view, whatever the coverage.
                ready: deployment.ready_frontends > 0 && !deployment.serving_pools.is_empty(),
                has_frontends: deployment.expected_frontends > 0,
                load: gauges,
            });
        }
        self.counters = next_counters;

        let mut models = BTreeMap::new();
        for (canonical, views) in views {
            let model = RelayModelLoad {
                ready: views.iter().any(|view| view.ready),
                // A namespace without frontends carries no Pylon traffic, so
                // it neither adds load nor makes the other namespaces' sum
                // unknown. Any other namespace without a complete view does.
                load: views
                    .into_iter()
                    .filter(|view| view.has_frontends)
                    .map(|view| view.load)
                    .reduce(|total, view| add_gauges(total?, view?))
                    .flatten(),
            };
            for name in catalog.names_of(&canonical) {
                models.insert(name, model.clone());
            }
        }
        Ok(RelayLoadUpdate {
            observed_at_unix_ms: update.observed_ms,
            models,
        })
    }
}

/// The model's gauges when its load view is complete: the Relay heard from
/// every expected frontend and reported every gauge. The rate and concurrency
/// are left for the caller.
fn complete_gauges(
    load: &proto::LoadView,
    deployment: &proto::ModelDeploymentStatus,
) -> Option<RelayModelGauges> {
    let complete = data_complete(load.status)
        && deployment.expected_frontends > 0
        && deployment.observed_frontends == deployment.expected_frontends
        && load.source_observed_ms > 0;
    if !complete {
        return None;
    }
    let requests = load.requests.as_ref()?;
    let tokens = load.tokens.as_ref()?;
    let awaiting = requests.requests_awaiting_first_token?;
    let generating = requests.requests_generating?;
    Some(RelayModelGauges {
        output_tps: None,
        queue_size: awaiting,
        queued_input_size: tokens.awaiting_first_token_input_tokens?,
        num_running_queries: awaiting.checked_add(generating)?,
        total_query_input_size: tokens.inflight_input_tokens?,
        input_processing_queries: awaiting,
        output_generation_queries: generating,
        max_engine_concurrency: None,
    })
}

/// The scheduler concurrency of the pools that carry the model's decode load,
/// known only when every serving pool is in the catalog and reported a
/// complete view.
fn max_engine_concurrency(
    catalog: &KvPoolCatalog,
    pools: &HashMap<proto::KvPoolId, LoadPool>,
    serving_pools: &[proto::KvPoolId],
) -> Option<u64> {
    let serving = serving_pools
        .iter()
        .map(|pool_id| Some((catalog.serving_pool(pool_id)?, pools.get(pool_id)?)))
        .collect::<Option<Vec<_>>>()?;
    let role = serving_role(serving.iter().map(|(pool, _)| pool.roles.as_slice()));
    let selected = serving
        .into_iter()
        .filter(|(pool, _)| pool.roles.contains(&role))
        .map(|(_, load)| load)
        .collect::<Vec<_>>();
    if selected.is_empty() || !selected.iter().all(|pool| pool.complete) {
        return None;
    }
    selected.iter().try_fold(0_u64, |total, pool| {
        total.checked_add(pool.max_concurrency?)
    })
}

/// Sums two namespaces' gauges. The rate and concurrency stay known only when
/// known in both.
fn add_gauges(total: RelayModelGauges, view: RelayModelGauges) -> Option<RelayModelGauges> {
    fn add(total: Option<u64>, view: Option<u64>) -> Option<u64> {
        total?.checked_add(view?)
    }
    Some(RelayModelGauges {
        output_tps: total.output_tps.zip(view.output_tps).map(|(a, b)| a + b),
        queue_size: total.queue_size.checked_add(view.queue_size)?,
        queued_input_size: total
            .queued_input_size
            .checked_add(view.queued_input_size)?,
        num_running_queries: total
            .num_running_queries
            .checked_add(view.num_running_queries)?,
        total_query_input_size: total
            .total_query_input_size
            .checked_add(view.total_query_input_size)?,
        input_processing_queries: total
            .input_processing_queries
            .checked_add(view.input_processing_queries)?,
        output_generation_queries: total
            .output_generation_queries
            .checked_add(view.output_generation_queries)?,
        max_engine_concurrency: add(total.max_engine_concurrency, view.max_engine_concurrency),
    })
}

/// The output token rate between two windows of one Relay incarnation. A new
/// incarnation restarts its totals from zero, so it skips one sample; so does
/// a counter that went backwards.
fn output_tps(previous: CounterBaseline, current: CounterBaseline) -> Option<f64> {
    if previous.relay_incarnation != current.relay_incarnation {
        return None;
    }
    let elapsed_ms = current
        .observed_at_unix_ms
        .checked_sub(previous.observed_at_unix_ms)
        .filter(|elapsed_ms| *elapsed_ms > 0)?;
    let tokens = current
        .output_tokens_total?
        .checked_sub(previous.output_tokens_total?)?;
    Some(tokens as f64 / (elapsed_ms as f64 / 1_000.0))
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

    fn with_output_total(
        mut model: proto::ModelServingLoad,
        output_tokens_total: u64,
    ) -> proto::ModelServingLoad {
        let tokens = model.load.as_mut().unwrap().tokens.as_mut().unwrap();
        tokens.output_tokens_total = Some(output_tokens_total);
        model
    }

    fn load_window(
        relay: proto::RelayIdentity,
        observed_ms: u64,
        pools: Vec<proto::PoolServingLoad>,
        models: Vec<proto::ModelServingLoad>,
    ) -> proto::ServingLoadUpdate {
        proto::ServingLoadUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(relay),
            window_sequence: 1,
            observed_ms,
            window_ms: 1_000,
            pools,
            models,
            contract_marker: proto::CONTRACT_MARKER,
        }
    }

    /// Translates two windows a second apart, from zero totals, so the second
    /// carries rates.
    fn translate_with_rates(
        catalog: &KvPoolCatalog,
        pools: impl Fn() -> Vec<proto::PoolServingLoad>,
        models: impl Fn() -> Vec<proto::ModelServingLoad>,
    ) -> RelayLoadUpdate {
        let mut translator = RelayLoadTranslator::default();
        let baseline = models()
            .into_iter()
            .map(|model| with_output_total(model, 0))
            .collect();
        translator
            .translate(catalog, load_window(RELAY, 4_000, pools(), baseline))
            .unwrap();
        translator
            .translate(catalog, load_window(RELAY, 5_000, pools(), models()))
            .unwrap()
    }

    fn gauges(update: &RelayLoadUpdate) -> Vec<&RelayModelGauges> {
        update
            .models
            .values()
            .map(|model| model.load.as_ref().expect("load should be complete"))
            .collect()
    }

    #[test]
    fn kv_usage_prefers_aggregated_pools_and_scales_blocks_to_tokens() {
        let catalog = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            descriptor(2, &[proto::WorkerRole::Decode], &["model-a"]),
        ]);

        let translated = kv_update(
            &catalog,
            usage_window(vec![usage(1, 100, 40), usage(2, 1_000, 900)]),
        )
        .unwrap();

        let usage = RelayModelKv {
            capacity_tokens: 1_600,
            used_tokens: 640,
        };
        assert_eq!(translated.observed_at_unix_ms, 7);
        assert_eq!(
            translated.models,
            BTreeMap::from([
                ("model-a".to_string(), usage),
                ("model-a-alias".to_string(), usage),
            ])
        );
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

        let translated = kv_update(
            &catalog,
            usage_window(vec![
                usage(1, 1_000, 900),
                usage(2, 100, 40),
                usage(3, 10, 5),
            ]),
        )
        .unwrap();

        assert_eq!(
            translated.models.get("model-a"),
            Some(&RelayModelKv {
                capacity_tokens: 1_760,
                used_tokens: 720,
            })
        );
    }

    #[test]
    fn incomplete_kv_usage_is_absent() {
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
            let translated = kv_update(&catalog, usage_window(window)).unwrap();
            assert!(translated.models.is_empty());
        }
    }

    #[test]
    fn legacy_role_counts_as_aggregated_and_unknown_role_quarantines_its_pool() {
        let legacy = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Legacy], &["model-a"]),
            descriptor(2, &[proto::WorkerRole::Decode], &["model-a"]),
        ]);
        let window = || usage_window(vec![usage(1, 100, 40), usage(2, 1_000, 900)]);
        let translated = kv_update(&legacy, window()).unwrap();
        assert_eq!(translated.models["model-a"].capacity_tokens, 1_600);

        let mut unknown = descriptor(2, &[proto::WorkerRole::Decode], &["model-a"]);
        unknown.pool_roles = vec![99];
        let quarantined = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            unknown,
        ]);
        assert!(kv_update(&quarantined, window()).unwrap().models.is_empty());
    }

    #[test]
    fn names_assigned_to_two_models_are_omitted() {
        let mut other = descriptor(2, &[proto::WorkerRole::Aggregated], &["model-b"]);
        other.registrations[0].aliases = vec!["model-a-alias".to_string()];
        let catalog = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            other,
        ]);

        let translated = kv_update(
            &catalog,
            usage_window(vec![usage(1, 100, 40), usage(2, 100, 40)]),
        )
        .unwrap();

        assert_eq!(
            translated.models.keys().collect::<Vec<_>>(),
            ["model-a", "model-b"]
        );
    }

    #[test]
    fn complete_load_reports_gauges_for_model_and_alias() {
        let catalog = aggregated_catalog(&["model-a"]);
        let mut translator = RelayLoadTranslator::default();
        let baseline = with_output_total(model_load("model-a", vec![pool_id(1)]), 0);
        let first = translator
            .translate(
                &catalog,
                load_window(RELAY, 4_000, vec![load_pool(1)], vec![baseline]),
            )
            .unwrap();
        assert!(first.models["model-a"].ready);
        assert_eq!(gauges(&first)[0].output_tps, None);

        let translated = translator
            .translate(
                &catalog,
                load_window(
                    RELAY,
                    5_000,
                    vec![load_pool(1)],
                    vec![model_load("model-a", vec![pool_id(1)])],
                ),
            )
            .unwrap();

        let model = RelayModelLoad {
            ready: true,
            load: Some(RelayModelGauges {
                output_tps: Some(20.0),
                queue_size: 2,
                queued_input_size: 17,
                num_running_queries: 4,
                total_query_input_size: 31,
                input_processing_queries: 2,
                output_generation_queries: 2,
                max_engine_concurrency: Some(8),
            }),
        };
        assert_eq!(translated.observed_at_unix_ms, 5_000);
        assert_eq!(
            translated.models,
            BTreeMap::from([
                ("model-a".to_string(), model.clone()),
                ("model-a-alias".to_string(), model),
            ])
        );
    }

    #[test]
    fn counter_reset_skips_one_rate_sample_then_recovers() {
        let catalog = aggregated_catalog(&["model-a"]);
        let mut translator = RelayLoadTranslator::default();
        let mut translate = |observed_ms, output| {
            let model = with_output_total(model_load("model-a", vec![pool_id(1)]), output);
            translator
                .translate(
                    &catalog,
                    load_window(RELAY, observed_ms, vec![load_pool(1)], vec![model]),
                )
                .unwrap()
        };
        translate(5_000, 20);

        let reset = translate(6_000, 2);
        for gauges in gauges(&reset) {
            assert_eq!(gauges.output_tps, None);
        }

        for gauges in gauges(&translate(7_000, 7)) {
            assert_eq!(gauges.output_tps, Some(5.0));
        }
    }

    #[test]
    fn relay_restart_skips_one_rate_sample_even_when_totals_grow() {
        let catalog = aggregated_catalog(&["model-a"]);
        let mut translator = RelayLoadTranslator::default();
        let mut translate = |relay, observed_ms, output| {
            let model = with_output_total(model_load("model-a", vec![pool_id(1)]), output);
            translator
                .translate(
                    &catalog,
                    load_window(relay, observed_ms, vec![load_pool(1)], vec![model]),
                )
                .unwrap()
        };
        translate(RELAY, 5_000, 20);
        let restarted = proto::RelayIdentity {
            relay_incarnation: 3,
            ..RELAY
        };

        for gauges in gauges(&translate(restarted, 6_000, 50)) {
            assert_eq!(gauges.output_tps, None);
        }

        for gauges in gauges(&translate(restarted, 7_000, 55)) {
            assert_eq!(gauges.output_tps, Some(5.0));
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
            load_window(RELAY, 1, vec![load_pool(1)], vec![first, second]),
        );

        assert!(result.is_err());
    }

    #[test]
    fn namespaces_serving_one_model_are_summed() {
        let catalog = catalog(vec![
            descriptor(1, &[proto::WorkerRole::Aggregated], &["model-a"]),
            descriptor(2, &[proto::WorkerRole::Aggregated], &["model-a"]),
        ]);
        let pools = || vec![load_pool(1), load_pool(2)];
        let models = || {
            let mut blue = model_load("model-a", vec![pool_id(1)]);
            blue.namespace = "blue".to_string();
            let mut green = model_load("model-a", vec![pool_id(2)]);
            green.namespace = "green".to_string();
            vec![blue, green]
        };

        let translated = translate_with_rates(&catalog, pools, models);
        assert!(translated.models["model-a"].ready);
        for gauges in gauges(&translated) {
            assert_eq!(gauges.output_tps, Some(40.0));
            assert_eq!(gauges.num_running_queries, 8);
            assert_eq!(gauges.max_engine_concurrency, Some(16));
            assert_eq!(gauges.total_query_input_size, 62);
        }

        // A namespace whose frontends are partial makes the sum unknown.
        let degraded = || {
            let mut models = models();
            models[1].load.as_mut().unwrap().status = proto::DataStatus::Degraded as i32;
            models
        };
        let translated = translate_with_rates(&catalog, pools, degraded);
        assert!(translated.models["model-a"].ready);
        assert!(translated.models.values().all(|model| model.load.is_none()));

        // A namespace without frontends, such as one draining its workers,
        // adds nothing.
        let draining = || {
            let mut models = models();
            models[1].load.as_mut().unwrap().status = proto::DataStatus::Unavailable as i32;
            models[1].deployment = Some(proto::ModelDeploymentStatus {
                serving_pools: vec![pool_id(2)],
                ..Default::default()
            });
            models
        };
        let translated = translate_with_rates(&catalog, pools, draining);
        assert!(translated.models["model-a"].ready);
        for gauges in gauges(&translated) {
            assert_eq!(gauges.output_tps, Some(20.0));
            assert_eq!(gauges.num_running_queries, 4);
            assert_eq!(gauges.max_engine_concurrency, Some(8));
        }
    }

    #[test]
    fn unresolved_serving_pool_leaves_concurrency_unknown() {
        let translated = translate_with_rates(
            &aggregated_catalog(&["model-a"]),
            || vec![load_pool(1), load_pool(9)],
            || vec![model_load("model-a", vec![pool_id(1), pool_id(9)])],
        );

        assert!(translated.models["model-a"].ready);
        for gauges in gauges(&translated) {
            assert_eq!(gauges.max_engine_concurrency, None);
        }
    }

    #[test]
    fn unknown_input_gauges_clear_the_load_but_not_readiness() {
        let model = || {
            let mut model = model_load("model-a", vec![pool_id(1)]);
            let tokens = model.load.as_mut().unwrap().tokens.as_mut().unwrap();
            tokens.awaiting_first_token_input_tokens = None;
            vec![model]
        };

        let translated = translate_with_rates(
            &aggregated_catalog(&["model-a"]),
            || vec![load_pool(1)],
            model,
        );

        assert_eq!(
            translated.models["model-a"],
            RelayModelLoad {
                ready: true,
                load: None,
            }
        );
    }

    #[test]
    fn model_without_a_frontend_or_serving_pool_is_not_ready() {
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
                    1,
                    vec![load_pool(1)],
                    vec![relay_only, model_load("frontend-only", Vec::new())],
                ),
            )
            .unwrap();

        assert_eq!(
            translated.models.keys().collect::<Vec<_>>(),
            ["frontend-only", "relay-only", "relay-only-alias"]
        );
        assert!(translated.models.values().all(|model| !model.ready));
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
        assert!(kv_update(&catalog, usage_window(vec![usage(1, 1, 0), usage(1, 1, 0)])).is_err());
        let mut anonymous = load_pool(1);
        anonymous.producer = None;
        assert!(
            RelayLoadTranslator::default()
                .translate(&catalog, load_window(RELAY, 1, vec![anonymous], Vec::new()))
                .is_err()
        );
    }
}
