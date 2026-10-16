/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

use super::bucket::{bucket_for_function, NodeBucketManager};
use crate::metrics;
use crate::models::{ActiveFunction, ActiveFunctionDetails};
use std::collections::{HashMap, HashSet};
use std::sync::RwLock;
use std::time::{Duration, Instant};
use uuid::Uuid;

struct Entry {
    function: ActiveFunction,
    expires_at: Instant,
}

#[derive(Default)]
struct State {
    entries: HashMap<(Uuid, Uuid), Entry>,
    initialized: bool,
}

/// Per-node discovery state. Only positive observations renew an entry; a
/// partial or failed metrics query never implies that a function went away.
pub struct LocalFunctionRegistry {
    state: RwLock<State>,
    retention: Duration,
}

impl LocalFunctionRegistry {
    pub fn new(retention: Duration) -> Self {
        Self {
            state: RwLock::new(State::default()),
            retention,
        }
    }

    pub fn merge(&self, discovered: Vec<ActiveFunctionDetails>, buckets: &NodeBucketManager) {
        let owned: HashSet<_> = buckets.get_my_buckets().into_iter().collect();
        let now = Instant::now();
        let expires_at = now + self.retention;
        let mut state = self.state.write().unwrap();
        state.entries.retain(|(id, version), entry| {
            owned.contains(&bucket_for_function(*id, *version)) && entry.expires_at > now
        });
        let candidates = discovered.len();
        let mut accepted = 0;
        for function in discovered {
            if !owned.contains(&bucket_for_function(
                function.function_id,
                function.function_version_id,
            )) {
                continue;
            }
            accepted += 1;
            let key = (function.function_id, function.function_version_id);
            let nca_id = function.nca_id.unwrap_or_default();
            let nca_id = if nca_id.is_empty() {
                state
                    .entries
                    .get(&key)
                    .map(|entry| entry.function.nca_id.clone())
                    .unwrap_or_default()
            } else {
                nca_id
            };
            state.entries.insert(
                key,
                Entry {
                    function: ActiveFunction {
                        function_id: key.0,
                        function_version_id: key.1,
                        nca_id,
                    },
                    expires_at,
                },
            );
        }
        state.initialized = true;
        metrics::record_local_discovery(candidates, accepted, state.entries.len());
    }

    pub fn functions_in_bucket(
        &self,
        bucket: usize,
        buckets: &NodeBucketManager,
    ) -> Vec<ActiveFunction> {
        let owned: HashSet<_> = buckets.get_my_buckets().into_iter().collect();
        if !owned.contains(&bucket) {
            return Vec::new();
        }
        let mut state = self.state.write().unwrap();
        if !state.initialized {
            return Vec::new();
        }
        let now = Instant::now();
        state.entries.retain(|(id, version), entry| {
            entry.expires_at > now && owned.contains(&bucket_for_function(*id, *version))
        });
        metrics::record_local_registry_size(state.entries.len());
        state
            .entries
            .iter()
            .filter(|((id, version), _)| bucket_for_function(*id, *version) == bucket)
            .map(|(_, entry)| entry.function.clone())
            .collect()
    }

    pub fn retain_positive(&self, function: &ActiveFunction) {
        if let Some(entry) = self
            .state
            .write()
            .unwrap()
            .entries
            .get_mut(&(function.function_id, function.function_version_id))
        {
            entry.expires_at = Instant::now() + self.retention;
        }
    }

    pub fn is_initialized(&self) -> bool {
        self.state.read().unwrap().initialized
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::work::bucket::{bucket_for_function, BUCKET_COUNT};

    #[tokio::test]
    async fn tracks_only_owned_functions_and_keeps_them_on_empty_discovery() {
        let owned = ActiveFunctionDetails::new(Uuid::new_v4(), Uuid::new_v4(), "owner".into());
        let bucket = bucket_for_function(owned.function_id, owned.function_version_id);
        let other = (0..1000)
            .map(|_| ActiveFunctionDetails::new(Uuid::new_v4(), Uuid::new_v4(), "other".into()))
            .find(|candidate| {
                bucket_for_function(candidate.function_id, candidate.function_version_id) != bucket
            })
            .unwrap();
        let buckets = NodeBucketManager::for_test(vec![bucket]);
        let registry = LocalFunctionRegistry::new(Duration::from_secs(60));
        assert!(!registry.is_initialized());
        registry.merge(vec![owned.clone(), other], &buckets);
        registry.merge(vec![], &buckets);
        assert!(registry.is_initialized());
        let functions = registry.functions_in_bucket(bucket, &buckets);
        assert_eq!(functions.len(), 1);
        assert_eq!(functions[0].function_id, owned.function_id);
        assert_eq!(functions[0].nca_id, "owner");
        let mut without_account = owned;
        without_account.nca_id = None;
        registry.merge(vec![without_account], &buckets);
        assert_eq!(
            registry.functions_in_bucket(bucket, &buckets)[0].nca_id,
            "owner"
        );
        assert!(registry
            .functions_in_bucket((bucket + 1) % BUCKET_COUNT, &buckets)
            .is_empty());
    }

    #[tokio::test]
    async fn entries_expire_but_positive_decisions_renew_them() {
        let details = ActiveFunctionDetails::new(Uuid::new_v4(), Uuid::new_v4(), "owner".into());
        let bucket = bucket_for_function(details.function_id, details.function_version_id);
        let buckets = NodeBucketManager::for_test(vec![bucket]);
        let registry = LocalFunctionRegistry::new(Duration::from_secs(30));
        registry.merge(vec![details], &buckets);
        let function = registry
            .functions_in_bucket(bucket, &buckets)
            .pop()
            .unwrap();
        registry
            .state
            .write()
            .unwrap()
            .entries
            .get_mut(&(function.function_id, function.function_version_id))
            .unwrap()
            .expires_at = Instant::now() - Duration::from_secs(1);
        registry.retain_positive(&function);
        assert_eq!(registry.functions_in_bucket(bucket, &buckets).len(), 1);
        registry
            .state
            .write()
            .unwrap()
            .entries
            .get_mut(&(function.function_id, function.function_version_id))
            .unwrap()
            .expires_at = Instant::now() - Duration::from_secs(1);
        assert!(registry.functions_in_bucket(bucket, &buckets).is_empty());
    }

    #[tokio::test]
    async fn ownership_change_discards_lost_bucket_and_discovers_new_bucket() {
        let old = ActiveFunctionDetails::new(Uuid::new_v4(), Uuid::new_v4(), "old".into());
        let old_bucket = bucket_for_function(old.function_id, old.function_version_id);
        let new = (0..1000)
            .map(|_| ActiveFunctionDetails::new(Uuid::new_v4(), Uuid::new_v4(), "new".into()))
            .find(|candidate| {
                bucket_for_function(candidate.function_id, candidate.function_version_id)
                    != old_bucket
            })
            .unwrap();
        let new_bucket = bucket_for_function(new.function_id, new.function_version_id);
        let buckets = NodeBucketManager::for_test(vec![old_bucket]);
        let registry = LocalFunctionRegistry::new(Duration::from_secs(30));
        registry.merge(vec![old.clone(), new.clone()], &buckets);
        buckets.assign_for_test(vec![new_bucket]);
        assert!(registry
            .functions_in_bucket(old_bucket, &buckets)
            .is_empty());
        registry.merge(vec![old, new.clone()], &buckets);
        assert_eq!(
            registry.functions_in_bucket(new_bucket, &buckets)[0].function_id,
            new.function_id
        );
    }
}
