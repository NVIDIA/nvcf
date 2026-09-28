// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::sync::Arc;
use std::time::Duration;

use moka::ops::compute::{CompResult, Op};
use moka::sync::Cache;

use crate::routing_state::{RoutingTargetKey, StargateState};

use super::expression::RejectionError;
use super::target_state::LoadBalancerDefinition;

struct DynamicConfigEntry {
    expression: String,
    definition: LoadBalancerDefinition,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Outcome {
    Hit,
    Build,
    Rebuild,
}

impl Outcome {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::Hit => "hit",
            Self::Build => "build",
            Self::Rebuild => "rebuild",
        }
    }
}

pub(crate) const DYNAMIC_CONFIG_IDLE_EXPIRY: Duration = Duration::from_secs(15 * 60);

pub(crate) struct DynamicConfigCache {
    entries: Cache<RoutingTargetKey, Arc<DynamicConfigEntry>>,
}

impl DynamicConfigCache {
    pub(crate) fn new(state: Arc<StargateState>, idle: Duration) -> Self {
        Self {
            entries: Cache::builder()
                .time_to_idle(idle)
                .eviction_listener(
                    move |target: Arc<RoutingTargetKey>, entry: Arc<DynamicConfigEntry>, _| {
                        entry.definition.retire();
                        state.forget_load_balancer_instance(&target, &entry.definition);
                    },
                )
                .build(),
        }
    }

    pub(crate) fn resolve(
        &self,
        target: &RoutingTargetKey,
        header: &str,
        build: impl FnOnce() -> Result<LoadBalancerDefinition, RejectionError>,
    ) -> Result<(LoadBalancerDefinition, Outcome), RejectionError> {
        // Hits skip the per-key compute lock so they never wait behind a rebuild.
        if let Some(entry) = self.entries.get(target)
            && entry.expression == header
        {
            return Ok((entry.definition.clone(), Outcome::Hit));
        }
        let result = self
            .entries
            .entry_by_ref(target)
            .and_try_compute_with(|current| {
                if current
                    .as_ref()
                    .is_some_and(|entry| entry.value().expression == header)
                {
                    return Ok(Op::Nop);
                }
                Ok(Op::Put(Arc::new(DynamicConfigEntry {
                    expression: header.to_owned(),
                    definition: build()?,
                })))
            })?;
        let (entry, outcome) = match result {
            CompResult::Unchanged(entry) => (entry, Outcome::Hit),
            CompResult::Inserted(entry) => (entry, Outcome::Build),
            CompResult::ReplacedWith(entry) => (entry, Outcome::Rebuild),
            // The closure returns Nop only for an existing entry and never returns Remove.
            CompResult::StillNone(_) | CompResult::Removed(_) => {
                unreachable!("dynamic config compute returned no entry")
            }
        };
        Ok((entry.into_value().definition.clone(), outcome))
    }

    #[cfg(test)]
    fn run_pending_tasks(&self) {
        self.entries.run_pending_tasks();
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::Barrier;
    use std::sync::atomic::{AtomicUsize, Ordering};

    use stargate_proto::pb::{
        InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
        ModelStats,
    };

    use super::*;
    use crate::load_balancer::expression::RoutingExpression;
    use crate::load_balancer::{LoadBalancerConfig, LoadBalancerRouter, LoadBalancerTargetState};
    use crate::routing_state::{RegistrationIdentity, RoutingTargetSnapshot};

    const EXPRESSION: &str = "round-robin;require_input_tokens=false";

    fn compile(header: &str) -> Result<LoadBalancerDefinition, RejectionError> {
        let router =
            LoadBalancerRouter::from_config(&LoadBalancerConfig::permissive_default()).unwrap();
        RoutingExpression::parse(header)?.compile(&router, "model")
    }

    async fn register_target(
        state: &StargateState,
        target: &RoutingTargetKey,
    ) -> RoutingTargetSnapshot {
        let running = state
            .begin_registration(&RegistrationIdentity {
                inference_server_id: "backend".to_owned(),
                cluster_id: "cluster".to_owned(),
                inference_server_url: "quic://127.0.0.1:5000".to_owned(),
                routing_key: target.routing_key.clone(),
                reverse_tunnel: false,
            })
            .unwrap();
        state
            .apply_registration_update(
                &running,
                &InferenceServerRegistration {
                    inference_server_id: "backend".to_owned(),
                    cluster_id: "cluster".to_owned(),
                    inference_server_url: "quic://127.0.0.1:5000".to_owned(),
                    models: HashMap::from([(
                        target.model_id.clone(),
                        InferenceServerModelRegistration {
                            stats: Some(ModelStats::default()),
                            status: InferenceServerStatus::Active as i32,
                        },
                    )]),
                    reverse_tunnel: false,
                },
                false,
                Some(Duration::from_millis(1)),
            )
            .await;
        state.routing_target_snapshot(target).await.unwrap()
    }

    #[tokio::test]
    async fn test_resolve_same_bytes_reuses_definition_and_instance() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache = DynamicConfigCache::new(state, Duration::from_secs(60));
        let (first, outcome) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        assert_eq!(outcome, Outcome::Build);
        let first_instance = snapshot.load_balancers().load_balancer(&first);
        let (second, outcome) = cache
            .resolve(&target, EXPRESSION, || panic!("hit rebuilt"))
            .unwrap();
        assert_eq!(outcome, Outcome::Hit);
        assert_eq!(first, second);
        assert!(Arc::ptr_eq(
            &first_instance,
            &snapshot.load_balancers().load_balancer(&second)
        ));
        assert_eq!(snapshot.load_balancers().instance_count(), 1);
    }

    #[tokio::test]
    async fn test_resolve_changed_bytes_forgets_only_superseded_instance() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache = DynamicConfigCache::new(state, Duration::from_secs(60));
        let static_definition = compile(EXPRESSION).unwrap();
        let static_instance = snapshot.load_balancers().load_balancer(&static_definition);
        let (first, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        let first_instance = snapshot.load_balancers().load_balancer(&first);
        assert_eq!(snapshot.load_balancers().instance_count(), 2);

        // Equivalent values with different bytes must still replace the entry.
        let changed = "ROUND_ROBIN; require_input_tokens=false";
        let (second, outcome) = cache
            .resolve(&target, changed, || compile(changed))
            .unwrap();
        assert_eq!(outcome, Outcome::Rebuild);
        assert_ne!(first, second);
        assert_eq!(snapshot.load_balancers().instance_count(), 1);
        let second_instance = snapshot.load_balancers().load_balancer(&second);
        assert!(!Arc::ptr_eq(&first_instance, &second_instance));
        assert!(Arc::ptr_eq(
            &static_instance,
            &snapshot.load_balancers().load_balancer(&static_definition)
        ));

        // A request paused after resolution must not reinsert an evicted definition.
        let _in_flight = snapshot.load_balancers().load_balancer(&first);
        assert_eq!(snapshot.load_balancers().instance_count(), 2);
        assert!(!snapshot.load_balancers().contains(&first));
    }

    #[tokio::test]
    async fn test_forget_instance_removes_only_the_named_definition() {
        let state = StargateState::new();
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let kept = compile(EXPRESSION).unwrap();
        let forgotten = compile(EXPRESSION).unwrap();
        let never_used = compile(EXPRESSION).unwrap();
        let kept_instance = snapshot.load_balancers().load_balancer(&kept);
        snapshot.load_balancers().load_balancer(&forgotten);

        state.forget_load_balancer_instance(&RoutingTargetKey::new(None, "absent"), &kept);
        state.forget_load_balancer_instance(&target, &never_used);
        assert_eq!(snapshot.load_balancers().instance_count(), 2);

        state.forget_load_balancer_instance(&target, &forgotten);
        assert_eq!(snapshot.load_balancers().instance_count(), 1);
        assert!(!snapshot.load_balancers().contains(&forgotten));
        assert!(Arc::ptr_eq(
            &kept_instance,
            &snapshot.load_balancers().load_balancer(&kept)
        ));
    }

    #[test]
    fn test_resolve_targets_have_independent_entries() {
        let cache =
            DynamicConfigCache::new(Arc::new(StargateState::new()), Duration::from_secs(60));
        let first_target = RoutingTargetKey::new(Some("tenant-a".to_owned()), "model-a");
        let (first, _) = cache
            .resolve(&first_target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        for target in [
            RoutingTargetKey::new(Some("tenant-b".to_owned()), "model-a"),
            RoutingTargetKey::new(Some("tenant-a".to_owned()), "model-b"),
        ] {
            let (other, outcome) = cache
                .resolve(&target, EXPRESSION, || compile(EXPRESSION))
                .unwrap();
            assert_eq!(outcome, Outcome::Build, "{target:?}");
            assert_ne!(first, other, "{target:?}");
        }
        let (again, outcome) = cache
            .resolve(&first_target, EXPRESSION, || panic!("target replaced"))
            .unwrap();
        assert_eq!(outcome, Outcome::Hit);
        assert_eq!(first, again);
    }

    #[tokio::test]
    async fn test_rebuild_racing_instance_creation_does_not_retain_retired_definitions() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache = DynamicConfigCache::new(state, Duration::from_secs(60));
        let (mut current, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        for value in 1..=32 {
            let barrier = Barrier::new(2);
            let header = format!("round-robin;max_input_work_seconds={value}");
            let next = std::thread::scope(|scope| {
                let selection = scope.spawn(|| {
                    barrier.wait();
                    snapshot.load_balancers().load_balancer(&current)
                });
                barrier.wait();
                let (next, outcome) = cache
                    .resolve(&target, &header, || compile(&header))
                    .unwrap();
                assert_eq!(outcome, Outcome::Rebuild);
                let _in_flight = selection.join().unwrap();
                next
            });
            assert!(!snapshot.load_balancers().contains(&current));
            assert_eq!(snapshot.load_balancers().instance_count(), 0);
            current = next;
        }
    }

    #[tokio::test]
    async fn test_idle_expiry_forgets_instance_and_rebuilds() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache = DynamicConfigCache::new(state, Duration::from_millis(50));
        let (first, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        let _instance = snapshot.load_balancers().load_balancer(&first);
        cache.run_pending_tasks();
        tokio::time::sleep(Duration::from_millis(100)).await;
        cache.run_pending_tasks();
        assert!(cache.entries.get(&target).is_none());
        assert_eq!(snapshot.load_balancers().instance_count(), 0);
        let (second, outcome) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        assert_eq!(outcome, Outcome::Build);
        assert_ne!(first, second);
        let _in_flight = snapshot.load_balancers().load_balancer(&first);
        assert_eq!(snapshot.load_balancers().instance_count(), 0);
    }

    #[test]
    fn test_hit_does_not_wait_for_concurrent_rebuild() {
        let cache =
            DynamicConfigCache::new(Arc::new(StargateState::new()), Duration::from_secs(60));
        let target = RoutingTargetKey::new(None, "model");
        let (current, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        let changed = "round-robin;require_input_tokens=true";
        let (hit_done, hit_observed) = std::sync::mpsc::channel();
        let (build_started, build_running) = std::sync::mpsc::channel();
        let (cache, target) = (&cache, &target);
        std::thread::scope(|scope| {
            let rebuild = scope.spawn(move || {
                cache.resolve(target, changed, || {
                    build_started.send(()).unwrap();
                    // The hit below must finish while this rebuild is still running.
                    let is_hit_done = hit_observed.recv_timeout(Duration::from_secs(5)).is_ok();
                    assert!(is_hit_done, "hit waited for the rebuild");
                    compile(changed)
                })
            });
            build_running.recv().unwrap();
            let hit = cache.resolve(target, EXPRESSION, || panic!("hit rebuilt"));
            hit_done.send(()).unwrap();
            assert_eq!(hit, Ok((current.clone(), Outcome::Hit)));
            assert_eq!(rebuild.join().unwrap().unwrap().1, Outcome::Rebuild);
        });
    }

    #[test]
    fn test_hits_keep_entry_alive_past_idle_window() {
        let cache =
            DynamicConfigCache::new(Arc::new(StargateState::new()), Duration::from_millis(200));
        let target = RoutingTargetKey::new(None, "model");
        let (first, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        for _ in 0..6 {
            std::thread::sleep(Duration::from_millis(80));
            let (again, outcome) = cache
                .resolve(&target, EXPRESSION, || panic!("entry expired while in use"))
                .unwrap();
            assert_eq!((again, outcome), (first.clone(), Outcome::Hit));
        }
    }

    #[test]
    fn test_failed_build_preserves_cache_contents() {
        for has_entry in [false, true] {
            let cache =
                DynamicConfigCache::new(Arc::new(StargateState::new()), Duration::from_secs(60));
            let target = RoutingTargetKey::new(None, "model");
            let before = has_entry.then(|| {
                cache
                    .resolve(&target, EXPRESSION, || compile(EXPRESSION))
                    .unwrap()
                    .0
            });
            let invalid = "wait-and-widen;n=0";
            let error = cache
                .resolve(&target, invalid, || compile(invalid))
                .unwrap_err();
            assert_eq!(error.class, "inert_value");
            assert_eq!(
                cache
                    .entries
                    .get(&target)
                    .map(|entry| entry.definition.clone()),
                before
            );
        }
    }

    #[test]
    fn test_concurrent_resolve_builds_once_and_reuses_instance() {
        let cache =
            DynamicConfigCache::new(Arc::new(StargateState::new()), Duration::from_secs(60));
        let target = RoutingTargetKey::new(None, "model");
        let instances = LoadBalancerTargetState::default();
        let builds = AtomicUsize::new(0);
        let barrier = Barrier::new(8);
        std::thread::scope(|scope| {
            let handles = (0..8)
                .map(|_| {
                    scope.spawn(|| {
                        barrier.wait();
                        let (definition, _) = cache
                            .resolve(&target, EXPRESSION, || {
                                builds.fetch_add(1, Ordering::SeqCst);
                                compile(EXPRESSION)
                            })
                            .unwrap();
                        let instance = instances.load_balancer(&definition);
                        (definition, instance)
                    })
                })
                .collect::<Vec<_>>();
            let results = handles
                .into_iter()
                .map(|handle| handle.join().unwrap())
                .collect::<Vec<_>>();
            for (definition, instance) in &results {
                assert_eq!(definition, &results[0].0);
                assert!(Arc::ptr_eq(instance, &results[0].1));
            }
        });
        assert_eq!(builds.load(Ordering::SeqCst), 1);
        assert_eq!(instances.instance_count(), 1);
    }
}
