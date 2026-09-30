// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::HashMap;
use std::sync::Arc;
use std::task::Poll;
use std::time::{Duration, Instant};

use stargate_proto::pb::{
    InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
    ModelStats,
};

use super::{RequestRouting, RoutingRejection};
use crate::http_proxy::request::ProxyRequestInputs;
use crate::load_balancer::{
    LoadBalancerAlgorithmResolution, LoadBalancerConfig, LoadBalancerRouter,
};
use crate::metrics::StargateMetrics;
use crate::routing_state::{
    RegistrationIdentity, RoutingTargetKey, RunningRegistration, StargateState,
};

const WAIT_AND_WIDEN: &str = r#"{"models":{"model-a":"wait-and-widen"}}"#;
const AFFINITY_WAIT: &str = r#"{"models":{"model-a":{"algorithm":"wait-and-widen","cache_affinity_backend_selection_count":1,"cache_affinity_wait_ms":120000}}}"#;

struct Fixture {
    state: StargateState,
    router: LoadBalancerRouter,
    metrics: Arc<StargateMetrics>,
    inputs: ProxyRequestInputs,
    resolution: LoadBalancerAlgorithmResolution,
}

impl Fixture {
    fn new(config: &str, max_wait_ms: Option<u64>) -> Self {
        let config: LoadBalancerConfig = serde_json::from_str(config).unwrap();
        let router = LoadBalancerRouter::from_config(&config).unwrap();
        let inputs = ProxyRequestInputs {
            target: RoutingTargetKey::new(Some("tenant-a".to_string()), "model-a"),
            input_tokens: 4_000,
            priority: 0,
            max_wait_ms,
            request_slo_ms: None,
            cache_affinity_key: Some("prefix".to_string()),
            routing_algorithm_override: None,
        };
        let resolution = router
            .resolve_algorithm_override(&inputs.target.model_id, None)
            .unwrap();
        Self {
            state: StargateState::new(),
            router,
            metrics: StargateMetrics::new().unwrap(),
            inputs,
            resolution,
        }
    }

    fn routing(&self, request_start: Instant) -> RequestRouting<'_> {
        RequestRouting::new(
            &self.state,
            &self.router,
            &self.metrics,
            &self.inputs,
            &self.resolution,
            request_start,
            Instant::now(),
        )
    }

    async fn register(&self, backend: &str, cluster: &str, running: u64) -> RunningRegistration {
        let registration = self
            .state
            .begin_registration(&RegistrationIdentity {
                inference_server_id: backend.to_string(),
                cluster_id: cluster.to_string(),
                inference_server_url: "quic://127.0.0.1:5000".to_string(),
                routing_key: self.inputs.target.routing_key.clone(),
                reverse_tunnel: false,
            })
            .unwrap();
        self.publish(&registration, running).await;
        registration
    }

    async fn publish(&self, registration: &RunningRegistration, running: u64) {
        self.state
            .apply_registration_update(
                registration,
                &InferenceServerRegistration {
                    models: HashMap::from([(
                        "model-a".to_string(),
                        InferenceServerModelRegistration {
                            status: InferenceServerStatus::Active.into(),
                            stats: Some(ModelStats {
                                last_mean_input_tps: 1_000.0,
                                max_output_tps: 1_000.0,
                                max_engine_concurrency: 1,
                                num_running_queries: running,
                                ..Default::default()
                            }),
                        },
                    )]),
                    ..Default::default()
                },
                false,
                Some(Duration::from_millis(1)),
            )
            .await;
    }

    fn selection_count(&self) -> u64 {
        self.metrics
            .routing_selections_total(Some("tenant-a"), "model-a", "wait-and-widen", "primary")
            .get()
    }

    fn duration_count(&self) -> u64 {
        self.metrics
            .routing_duration_seconds(Some("tenant-a"), "model-a")
            .get_sample_count()
    }
}

async fn reject_without_waiting(routing: &mut RequestRouting<'_>) -> RoutingRejection {
    match tokio::time::timeout(Duration::from_secs(2), routing.next())
        .await
        .expect("routing should reject without waiting for the affinity hold")
    {
        Err(rejection) => rejection,
        Ok(_) => panic!("routing unexpectedly selected a backend"),
    }
}

async fn selected_backend(routing: &mut RequestRouting<'_>) -> String {
    match tokio::time::timeout(Duration::from_secs(2), routing.next())
        .await
        .expect("an available backend should be selected without waiting")
    {
        Ok((_, backend)) => backend.inference_server_id.clone(),
        Err(_) => panic!("routing unexpectedly rejected an available backend"),
    }
}

async fn assert_routing_waits(routing: &mut RequestRouting<'_>) {
    {
        let next = routing.next();
        let mut next = std::pin::pin!(next);
        assert!(matches!(futures::poll!(&mut next), Poll::Pending));
    }
    assert_eq!(routing.retry_attempts(), 1);
}

#[tokio::test]
async fn affinity_wait_without_header_is_allowed_but_zero_and_capped_budgets_stop_it() {
    let fixture = Fixture::new(AFFINITY_WAIT, None);
    fixture.register("backend-a", "cluster-a", 1).await;
    let mut routing = fixture.routing(Instant::now());
    assert_routing_waits(&mut routing).await;
    assert_eq!(fixture.selection_count(), 0);
    assert_eq!(fixture.duration_count(), 0);

    for (max_wait_ms, elapsed) in [
        (Some(0), Duration::ZERO),
        (None, Duration::from_secs(61)),
        (Some(u64::MAX), Duration::from_secs(61)),
    ] {
        let fixture = Fixture::new(AFFINITY_WAIT, max_wait_ms);
        fixture.register("backend-a", "cluster-a", 1).await;
        let mut routing = fixture.routing(Instant::now() - elapsed);
        assert!(matches!(
            reject_without_waiting(&mut routing).await,
            RoutingRejection::ServiceUnavailable
        ));
        assert_eq!(routing.retry_attempts(), 0);
        assert_eq!(fixture.selection_count(), 0);
        assert_eq!(fixture.duration_count(), 0);
    }
}

#[tokio::test]
async fn unavailable_capacity_retries_only_with_an_explicit_positive_budget() {
    for max_wait_ms in [None, Some(0)] {
        let fixture = Fixture::new(WAIT_AND_WIDEN, max_wait_ms);
        fixture.register("backend-a", "cluster-a", 1).await;
        let mut routing = fixture.routing(Instant::now());
        assert!(matches!(
            reject_without_waiting(&mut routing).await,
            RoutingRejection::ServiceUnavailable
        ));
        assert_eq!(routing.retry_attempts(), 0);
    }

    let fixture = Fixture::new(WAIT_AND_WIDEN, Some(60_000));
    fixture.register("backend-a", "cluster-a", 1).await;
    let mut routing = fixture.routing(Instant::now());
    assert_routing_waits(&mut routing).await;
    assert_eq!(fixture.selection_count(), 0);
    assert_eq!(fixture.duration_count(), 0);
}

#[tokio::test]
async fn exhausted_wait_budget_still_allows_an_available_backend() {
    for (max_wait_ms, elapsed) in [
        (Some(0), Duration::ZERO),
        (None, Duration::from_secs(61)),
        (Some(u64::MAX), Duration::from_secs(61)),
    ] {
        let fixture = Fixture::new(AFFINITY_WAIT, max_wait_ms);
        fixture.register("backend-a", "cluster-a", 0).await;
        let mut routing = fixture.routing(Instant::now() - elapsed);
        assert_eq!(selected_backend(&mut routing).await, "backend-a");
        assert_eq!(routing.retry_attempts(), 0);
        assert_eq!(fixture.selection_count(), 1);
        assert_eq!(fixture.duration_count(), 1);
    }
}

#[tokio::test]
async fn refreshed_capacity_is_selected_after_an_affinity_wait() {
    let fixture = Fixture::new(AFFINITY_WAIT, None);
    let registration = fixture.register("backend-a", "cluster-a", 1).await;
    let mut routing = fixture.routing(Instant::now());
    let backend = {
        let next = routing.next();
        let mut next = std::pin::pin!(next);
        assert!(matches!(futures::poll!(&mut next), Poll::Pending));
        fixture.publish(&registration, 0).await;
        match tokio::time::timeout(Duration::from_secs(2), next)
            .await
            .expect("the waiting request should see newly available capacity")
        {
            Ok((_, backend)) => backend.inference_server_id.clone(),
            Err(_) => panic!("routing rejected newly available capacity"),
        }
    };
    assert_eq!(backend, "backend-a");
    assert_eq!(routing.retry_attempts(), 1);
    assert_eq!(fixture.selection_count(), 1);
    assert_eq!(fixture.duration_count(), 1);
}

#[tokio::test]
async fn sibling_retries_keep_cluster_selection_and_exhaust_without_affinity_wait() {
    let fixture = Fixture::new(AFFINITY_WAIT, Some(60_000));
    fixture.register("backend-a", "cluster-a", 0).await;
    fixture.register("backend-b", "cluster-a", 0).await;
    let mut routing = fixture.routing(Instant::now());

    let first = selected_backend(&mut routing).await;
    assert_eq!(selected_backend(&mut routing).await, first);
    routing.exclude_backend(first.clone());
    let sibling = selected_backend(&mut routing).await;
    assert_ne!(sibling, first);
    assert_eq!(routing.failed_backend_count(), 1);
    assert_eq!(routing.failed_cluster_count(), 0);
    assert_eq!(fixture.selection_count(), 1);
    assert_eq!(fixture.duration_count(), 1);

    routing.exclude_backend(sibling);
    assert!(matches!(
        reject_without_waiting(&mut routing).await,
        RoutingRejection::ServiceUnavailable
    ));
    assert_eq!(routing.failed_backend_count(), 2);
    assert_eq!(routing.failed_cluster_count(), 1);
    assert_eq!(routing.retry_attempts(), 0);
    assert_eq!(fixture.selection_count(), 1);
    assert_eq!(fixture.duration_count(), 1);
}

#[tokio::test]
async fn disappearing_failed_target_keeps_failure_context() {
    let fixture = Fixture::new(WAIT_AND_WIDEN, Some(60_000));
    let registration = fixture.register("backend-a", "cluster-a", 0).await;
    let mut routing = fixture.routing(Instant::now());
    assert_eq!(selected_backend(&mut routing).await, "backend-a");
    routing.exclude_cluster("cluster-a".to_string());
    fixture.state.end_registration(registration).await;

    assert!(matches!(
        reject_without_waiting(&mut routing).await,
        RoutingRejection::ServiceUnavailable
    ));
    assert_eq!(routing.failed_backend_count(), 0);
    assert_eq!(routing.failed_cluster_count(), 1);
    assert_eq!(routing.retry_attempts(), 0);

    let mut fresh_request = fixture.routing(Instant::now());
    assert!(matches!(
        reject_without_waiting(&mut fresh_request).await,
        RoutingRejection::NoCandidatesNotFound
    ));
}

#[tokio::test]
async fn registered_inactive_target_is_unavailable_without_retrying() {
    let fixture = Fixture::new(WAIT_AND_WIDEN, Some(60_000));
    let mut unknown_target = fixture.routing(Instant::now());
    assert!(matches!(
        reject_without_waiting(&mut unknown_target).await,
        RoutingRejection::NoCandidatesNotFound
    ));
    assert_eq!(unknown_target.retry_attempts(), 0);

    let registration = fixture.register("backend-a", "cluster-a", 0).await;
    fixture
        .state
        .apply_registration_update(
            &registration,
            &InferenceServerRegistration {
                models: HashMap::from([(
                    "model-a".to_string(),
                    InferenceServerModelRegistration {
                        status: InferenceServerStatus::Inactive.into(),
                        stats: Some(ModelStats::default()),
                    },
                )]),
                ..Default::default()
            },
            false,
            None,
        )
        .await;
    let mut inactive_target = fixture.routing(Instant::now());
    assert!(matches!(
        reject_without_waiting(&mut inactive_target).await,
        RoutingRejection::ServiceUnavailable
    ));
    assert_eq!(inactive_target.retry_attempts(), 0);
    assert_eq!(fixture.selection_count(), 0);
    assert_eq!(fixture.duration_count(), 0);
}

#[tokio::test]
async fn input_work_admission_preempts_affinity_wait_and_capacity_retry() {
    let config = r#"{"models":{"model-a":{"algorithm":"wait-and-widen","max_input_work_seconds":0.001,"cache_affinity_backend_selection_count":1,"cache_affinity_wait_ms":120000}}}"#;
    let fixture = Fixture::new(config, Some(60_000));
    fixture.register("backend-a", "cluster-a", 1).await;
    let mut routing = fixture.routing(Instant::now());

    assert!(matches!(
        reject_without_waiting(&mut routing).await,
        RoutingRejection::Admission("input_work_limit_exceeded")
    ));
    assert_eq!(routing.retry_attempts(), 0);
    assert_eq!(fixture.selection_count(), 0);
    assert_eq!(fixture.duration_count(), 0);
}

#[tokio::test]
async fn alternate_cluster_routing_does_not_restart_the_request_wait_budget() {
    let fixture = Fixture::new(WAIT_AND_WIDEN, Some(u64::MAX));
    fixture.register("backend-a", "cluster-a", 0).await;
    fixture.register("backend-b", "cluster-b", 1).await;
    let mut routing = fixture.routing(Instant::now() - Duration::from_secs(61));
    assert_eq!(selected_backend(&mut routing).await, "backend-a");

    routing.exclude_cluster("cluster-a".to_string());
    assert!(matches!(
        reject_without_waiting(&mut routing).await,
        RoutingRejection::ServiceUnavailable
    ));
    assert_eq!(routing.failed_cluster_count(), 1);
    assert_eq!(routing.failed_backend_count(), 0);
    assert_eq!(routing.retry_attempts(), 0);
    assert_eq!(fixture.selection_count(), 1);
    assert_eq!(fixture.duration_count(), 1);
}
