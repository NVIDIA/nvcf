// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//! Translation of Dynamo frontend KV snapshots into Pylon's aggregate model state.

use std::collections::HashSet;

use stargate_proto::dynamo_frontend_stats as proto;

use super::aggregator::{KvCacheStatsEnvelope, KvCacheStatsSnapshot};

pub(super) fn kv_snapshot_from_proto(
    snapshot: proto::KvStatsSnapshot,
) -> anyhow::Result<KvCacheStatsEnvelope> {
    let mut identities = HashSet::new();
    let models = snapshot
        .models
        .into_iter()
        .map(|model| -> anyhow::Result<_> {
            anyhow::ensure!(!model.model.trim().is_empty(), "KV stats model is empty");
            anyhow::ensure!(
                identities.insert(model.model.clone()),
                "duplicate KV stats identity {}",
                model.model
            );
            for alias in &model.aliases {
                anyhow::ensure!(!alias.trim().is_empty(), "KV stats alias is empty");
                anyhow::ensure!(
                    identities.insert(alias.clone()),
                    "duplicate KV stats identity {alias}"
                );
            }

            let complete = snapshot.observed_at_unix_ms > 0
                && model.routing_cache.as_ref().is_some_and(|routing| {
                    routing.capacity_tokens > 0
                        && routing.used_tokens.checked_add(routing.free_tokens)
                            == Some(routing.capacity_tokens)
                });
            let (capacity, used, free) = model
                .routing_cache
                .map(|routing| {
                    (
                        routing.capacity_tokens,
                        routing.used_tokens,
                        routing.free_tokens,
                    )
                })
                .unwrap_or_default();
            Ok(KvCacheStatsSnapshot {
                model: model.model,
                aliases: model.aliases,
                kv_cache_capacity_tokens: capacity,
                kv_cache_used_tokens: used,
                kv_cache_free_tokens: free,
                source_observed_at_unix_ms: snapshot.observed_at_unix_ms,
                complete,
            })
        })
        .collect::<anyhow::Result<Vec<_>>>()?;
    Ok(KvCacheStatsEnvelope { models })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn snapshot(capacity: u64, used: u64, free: u64) -> proto::KvStatsSnapshot {
        proto::KvStatsSnapshot {
            snapshot_id: 1,
            observed_at_unix_ms: 10,
            models: vec![proto::ModelKvStats {
                model: "model-a".to_string(),
                aliases: vec!["alias-a".to_string()],
                routing_cache: Some(proto::RoutingCacheStats {
                    role: proto::WorkerRole::Aggregated as i32,
                    capacity_tokens: capacity,
                    used_tokens: used,
                    free_tokens: free,
                }),
                pools: Vec::new(),
            }],
        }
    }

    #[test]
    fn converts_complete_routing_cache_stats() {
        let envelope = kv_snapshot_from_proto(snapshot(100, 40, 60)).unwrap();
        let model = &envelope.models[0];
        assert!(model.complete);
        assert_eq!(model.kv_cache_capacity_tokens, 100);
        assert_eq!(model.kv_cache_used_tokens, 40);
        assert_eq!(model.kv_cache_free_tokens, 60);
    }

    #[test]
    fn marks_inconsistent_totals_incomplete() {
        let envelope = kv_snapshot_from_proto(snapshot(100, 40, 50)).unwrap();
        assert!(!envelope.models[0].complete);
    }

    #[test]
    fn rejects_duplicate_model_identity() {
        let mut value = snapshot(100, 40, 60);
        value.models.push(proto::ModelKvStats {
            model: "alias-a".to_string(),
            aliases: Vec::new(),
            routing_cache: None,
            pools: Vec::new(),
        });
        assert!(kv_snapshot_from_proto(value).is_err());
    }
}
