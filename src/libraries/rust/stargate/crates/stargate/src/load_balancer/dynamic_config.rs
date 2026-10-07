// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::sync::{Arc, Weak};
use std::time::Duration;

use moka::notification::RemovalCause;
use moka::ops::compute::{CompResult, Op};
use moka::sync::Cache;
use prometheus::IntGauge;
use prometheus::core::{Collector, Desc};
use prometheus::proto::MetricFamily;

use crate::metrics::StargateMetrics;
use crate::routing_state::{RoutingTargetKey, StargateState};

use super::expression::RejectionError;
use super::target_state::LoadBalancerDefinition;

#[derive(Clone)]
struct Configuration {
    expression: String,
    definition: LoadBalancerDefinition,
}

// Gateway replicas cache model metadata separately, so after an owner update they send the
// old and new expressions alternately until their caches converge. Keeping the previous
// expression makes that window a run of hits instead of a rebuild per switch.
struct DynamicConfigEntry {
    current: Configuration,
    previous: Option<Configuration>,
}

impl DynamicConfigEntry {
    fn definition_for(&self, header: &str) -> Option<&LoadBalancerDefinition> {
        std::iter::once(&self.current)
            .chain(&self.previous)
            .find(|configuration| configuration.expression == header)
            .map(|configuration| &configuration.definition)
    }

    // A replacement carries `current` into the new entry as its `previous`.
    fn dropped(&self, cause: RemovalCause) -> impl Iterator<Item = &Configuration> {
        let current = (cause != RemovalCause::Replaced).then_some(&self.current);
        current.into_iter().chain(&self.previous)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Outcome {
    Hit,
    Build,
    Rebuild,
}

impl Outcome {
    pub(crate) const ALL: [Self; 3] = [Self::Hit, Self::Build, Self::Rebuild];

    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::Hit => "hit",
            Self::Build => "build",
            Self::Rebuild => "rebuild",
        }
    }
}

pub(crate) const DYNAMIC_CONFIG_IDLE_EXPIRY: Duration = Duration::from_secs(15 * 60);
// A memory backstop well above the deployed (function, model) pairs one router serves.
pub(crate) const DYNAMIC_CONFIG_MAX_ENTRIES: u64 = 16_384;

pub(crate) struct DynamicConfigCache {
    entries: Cache<RoutingTargetKey, Arc<DynamicConfigEntry>>,
}

impl DynamicConfigCache {
    pub(crate) fn new(state: Arc<StargateState>, idle: Duration, max_entries: u64) -> Self {
        Self {
            entries: Cache::builder()
                .max_capacity(max_entries)
                .time_to_idle(idle)
                .eviction_listener(
                    move |target: Arc<RoutingTargetKey>, entry: Arc<DynamicConfigEntry>, cause| {
                        for configuration in entry.dropped(cause) {
                            configuration.definition.retire();
                            state.forget_load_balancer_instance(&target, &configuration.definition);
                        }
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
            && let Some(definition) = entry.definition_for(header)
        {
            return Ok((definition.clone(), Outcome::Hit));
        }
        let result = self
            .entries
            .entry_by_ref(target)
            .and_try_compute_with(|existing| {
                let existing = existing.as_ref().map(|entry| entry.value());
                if existing.is_some_and(|entry| entry.definition_for(header).is_some()) {
                    return Ok(Op::Nop);
                }
                Ok(Op::Put(Arc::new(DynamicConfigEntry {
                    current: Configuration {
                        expression: header.to_owned(),
                        definition: build()?,
                    },
                    previous: existing.map(|entry| entry.current.clone()),
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
        let definition = entry
            .into_value()
            .definition_for(header)
            .cloned()
            .expect("a hit matched this expression and a build stored it as current");
        Ok((definition, outcome))
    }

    /// Exports the entry count. Each scrape first runs the cache's pending tasks, which
    /// is what expires idle entries once expression traffic stops.
    pub(crate) fn register_entry_gauge(
        self: &Arc<Self>,
        metrics: &StargateMetrics,
    ) -> anyhow::Result<()> {
        let gauge = IntGauge::new(
            format!("{}routing_expression_cache_entries", metrics.prefix()),
            "Routing targets that hold a routing expression configuration",
        )?;
        metrics.registry().register(Box::new(EntryGauge {
            // The cache owns routing state, which owns the metrics; a strong reference would cycle.
            cache: Arc::downgrade(self),
            gauge,
        }))?;
        Ok(())
    }

    #[cfg(test)]
    fn run_pending_tasks(&self) {
        self.entries.run_pending_tasks();
    }
}

struct EntryGauge {
    cache: Weak<DynamicConfigCache>,
    gauge: IntGauge,
}

impl Collector for EntryGauge {
    fn desc(&self) -> Vec<&Desc> {
        self.gauge.desc()
    }

    fn collect(&self) -> Vec<MetricFamily> {
        if let Some(cache) = self.cache.upgrade() {
            cache.entries.run_pending_tasks();
            self.gauge
                .set(i64::try_from(cache.entries.entry_count()).unwrap_or(i64::MAX));
        }
        self.gauge.collect()
    }
}

#[cfg(test)]
mod tests {
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
        register_targets(state, std::slice::from_ref(target))
            .await
            .remove(0)
    }

    // All targets share one backend, so they must share one routing key.
    async fn register_targets(
        state: &StargateState,
        targets: &[RoutingTargetKey],
    ) -> Vec<RoutingTargetSnapshot> {
        let target = &targets[0];
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
                    models: targets
                        .iter()
                        .map(|target| {
                            (
                                target.model_id.clone(),
                                InferenceServerModelRegistration {
                                    stats: Some(ModelStats::default()),
                                    status: InferenceServerStatus::Active as i32,
                                },
                            )
                        })
                        .collect(),
                    reverse_tunnel: false,
                },
                false,
                Some(Duration::from_millis(1)),
            )
            .await;
        let mut snapshots = Vec::with_capacity(targets.len());
        for target in targets {
            snapshots.push(state.routing_target_snapshot(target).await.unwrap());
        }
        snapshots
    }

    #[tokio::test]
    async fn test_resolve_same_bytes_reuses_definition_and_instance() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache =
            DynamicConfigCache::new(state, Duration::from_secs(60), DYNAMIC_CONFIG_MAX_ENTRIES);
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
    async fn test_resolve_keeps_previous_expression_and_forgets_older_ones() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache =
            DynamicConfigCache::new(state, Duration::from_secs(60), DYNAMIC_CONFIG_MAX_ENTRIES);
        let static_definition = compile(EXPRESSION).unwrap();
        let static_instance = snapshot.load_balancers().load_balancer(&static_definition);
        let (first, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        let first_instance = snapshot.load_balancers().load_balancer(&first);

        // Equivalent values with different bytes still build a new configuration.
        let changed = "ROUND_ROBIN; require_input_tokens=false";
        let (second, outcome) = cache
            .resolve(&target, changed, || compile(changed))
            .unwrap();
        assert_eq!(outcome, Outcome::Rebuild);
        assert_ne!(first, second);
        let second_instance = snapshot.load_balancers().load_balancer(&second);
        assert_eq!(snapshot.load_balancers().instance_count(), 3);

        // Gateway replicas alternating between the two values only hit.
        for _ in 0..4 {
            for (header, definition, instance) in [
                (EXPRESSION, &first, &first_instance),
                (changed, &second, &second_instance),
            ] {
                let (resolved, outcome) = cache
                    .resolve(&target, header, || panic!("alternation rebuilt {header}"))
                    .unwrap();
                assert_eq!((&resolved, outcome), (definition, Outcome::Hit));
                assert!(Arc::ptr_eq(
                    instance,
                    &snapshot.load_balancers().load_balancer(&resolved)
                ));
            }
        }

        // A third value keeps the current one as previous and forgets the older one.
        let third = "round-robin;require_input_tokens=true";
        let (third_definition, outcome) = cache.resolve(&target, third, || compile(third)).unwrap();
        assert_eq!(outcome, Outcome::Rebuild);
        assert!(!snapshot.load_balancers().contains(&first));
        assert!(snapshot.load_balancers().contains(&second));
        assert_eq!(
            cache.resolve(&target, changed, || panic!("previous rebuilt")),
            Ok((second.clone(), Outcome::Hit))
        );
        assert_ne!(third_definition, second);
        assert!(Arc::ptr_eq(
            &static_instance,
            &snapshot.load_balancers().load_balancer(&static_definition)
        ));

        // A request paused after resolution must not reinsert a forgotten definition.
        let _in_flight = snapshot.load_balancers().load_balancer(&first);
        assert!(!snapshot.load_balancers().contains(&first));
        assert_eq!(snapshot.load_balancers().instance_count(), 2);
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

    #[tokio::test]
    async fn test_capacity_evictions_forget_instances() {
        let state = Arc::new(StargateState::new());
        let targets = (0..6)
            .map(|index| RoutingTargetKey::new(None, format!("model-{index}")))
            .collect::<Vec<_>>();
        let snapshots = register_targets(&state, &targets).await;
        let cache = DynamicConfigCache::new(state, Duration::from_secs(60), 2);
        for (target, snapshot) in targets.iter().zip(&snapshots) {
            let (definition, _) = cache
                .resolve(target, EXPRESSION, || compile(EXPRESSION))
                .unwrap();
            snapshot.load_balancers().load_balancer(&definition);
        }
        cache.run_pending_tasks();

        assert!(cache.entries.entry_count() <= 2);
        for (target, snapshot) in targets.iter().zip(&snapshots) {
            let expected = usize::from(cache.entries.contains_key(target));
            assert_eq!(
                snapshot.load_balancers().instance_count(),
                expected,
                "{target:?}"
            );
        }
    }

    #[test]
    fn test_resolve_targets_have_independent_entries() {
        let cache = DynamicConfigCache::new(
            Arc::new(StargateState::new()),
            Duration::from_secs(60),
            DYNAMIC_CONFIG_MAX_ENTRIES,
        );
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
        let cache =
            DynamicConfigCache::new(state, Duration::from_secs(60), DYNAMIC_CONFIG_MAX_ENTRIES);
        let (mut older, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        let header = "round-robin;max_input_work_seconds=1";
        let (mut newer, _) = cache.resolve(&target, header, || compile(header)).unwrap();
        for value in 2..=33 {
            let barrier = Barrier::new(2);
            let header = format!("round-robin;max_input_work_seconds={value}");
            // The next rebuild drops `older`; race a selection on it against that rebuild.
            let next = std::thread::scope(|scope| {
                let selection = scope.spawn(|| {
                    barrier.wait();
                    snapshot.load_balancers().load_balancer(&older)
                });
                barrier.wait();
                let (next, outcome) = cache
                    .resolve(&target, &header, || compile(&header))
                    .unwrap();
                assert_eq!(outcome, Outcome::Rebuild);
                let _in_flight = selection.join().unwrap();
                next
            });
            assert!(!snapshot.load_balancers().contains(&older));
            assert_eq!(snapshot.load_balancers().instance_count(), 0);
            older = newer;
            newer = next;
        }
    }

    #[tokio::test]
    async fn test_idle_expiry_forgets_instance_and_rebuilds() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache = DynamicConfigCache::new(
            state,
            Duration::from_millis(500),
            DYNAMIC_CONFIG_MAX_ENTRIES,
        );
        let (first, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        let _instance = snapshot.load_balancers().load_balancer(&first);
        let changed = "round-robin;require_input_tokens=true";
        let (previous_holder, _) = cache
            .resolve(&target, changed, || compile(changed))
            .unwrap();
        let _instance = snapshot.load_balancers().load_balancer(&previous_holder);
        assert_eq!(snapshot.load_balancers().instance_count(), 2);
        cache.run_pending_tasks();
        tokio::time::sleep(Duration::from_secs(1)).await;
        cache.run_pending_tasks();
        // Expiry forgets both the current and the previous configuration.
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

    fn scraped_entry_gauge(metrics: &StargateMetrics) -> f64 {
        metrics
            .registry()
            .gather()
            .iter()
            .find(|family| family.name() == "stargate_routing_expression_cache_entries")
            .expect("entry gauge should be registered")
            .get_metric()[0]
            .get_gauge()
            .value()
    }

    #[tokio::test]
    async fn test_metrics_scrape_reports_entries_and_drives_idle_expiry() {
        let state = Arc::new(StargateState::new());
        let target = RoutingTargetKey::new(None, "model");
        let snapshot = register_target(&state, &target).await;
        let cache = Arc::new(DynamicConfigCache::new(
            state,
            Duration::from_millis(500),
            DYNAMIC_CONFIG_MAX_ENTRIES,
        ));
        let metrics = StargateMetrics::new().unwrap();
        cache.register_entry_gauge(&metrics).unwrap();
        assert_eq!(scraped_entry_gauge(&metrics), 0.0);

        let (definition, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        let _instance = snapshot.load_balancers().load_balancer(&definition);
        assert_eq!(scraped_entry_gauge(&metrics), 1.0);

        // After the idle window only the scrape touches the cache.
        tokio::time::sleep(Duration::from_secs(1)).await;
        assert_eq!(scraped_entry_gauge(&metrics), 0.0);
        assert_eq!(snapshot.load_balancers().instance_count(), 0);
    }

    #[test]
    fn test_hit_does_not_wait_for_concurrent_rebuild() {
        let cache = DynamicConfigCache::new(
            Arc::new(StargateState::new()),
            Duration::from_secs(60),
            DYNAMIC_CONFIG_MAX_ENTRIES,
        );
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
        let cache = DynamicConfigCache::new(
            Arc::new(StargateState::new()),
            Duration::from_secs(1),
            DYNAMIC_CONFIG_MAX_ENTRIES,
        );
        let target = RoutingTargetKey::new(None, "model");
        let (first, _) = cache
            .resolve(&target, EXPRESSION, || compile(EXPRESSION))
            .unwrap();
        // The hits span longer than the idle window; each gap leaves wide margin for slow CI.
        for _ in 0..5 {
            std::thread::sleep(Duration::from_millis(300));
            let (again, outcome) = cache
                .resolve(&target, EXPRESSION, || panic!("entry expired while in use"))
                .unwrap();
            assert_eq!((again, outcome), (first.clone(), Outcome::Hit));
        }
    }

    #[test]
    fn test_failed_build_preserves_cache_contents() {
        for has_entry in [false, true] {
            let cache = DynamicConfigCache::new(
                Arc::new(StargateState::new()),
                Duration::from_secs(60),
                DYNAMIC_CONFIG_MAX_ENTRIES,
            );
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
                    .map(|entry| entry.current.definition.clone()),
                before
            );
        }
    }

    #[test]
    fn test_concurrent_resolve_builds_once_and_reuses_instance() {
        let cache = DynamicConfigCache::new(
            Arc::new(StargateState::new()),
            Duration::from_secs(60),
            DYNAMIC_CONFIG_MAX_ENTRIES,
        );
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
