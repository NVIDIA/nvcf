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

//! Mock Dynamo KV DC Relay (`dynamo.kvrelay.v1.KvEventRelay`), served over gRPC
//! on the HTTP listener. One aggregated pool with one-token KV blocks serves
//! the configured model; its load comes from this server's own requests.

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use futures::Stream;
use stargate_proto::dynamo_kvrelay as proto;
use tonic::{Request, Response, Status};

use crate::AppState;

const WINDOW: Duration = Duration::from_secs(1);
const NAMESPACE: &str = "dynamo";

/// Relay state shared by the request handlers and the gRPC streams.
#[derive(Clone)]
pub(crate) struct RelayState {
    identity: proto::RelayIdentity,
    enabled: Arc<AtomicBool>,
    load: Arc<Mutex<LoadAccumulator>>,
}

impl RelayState {
    pub(crate) fn new() -> Self {
        Self {
            identity: proto::RelayIdentity {
                drt_instance_id: 1,
                // A restarted mock is a new incarnation, so its totals restart at zero.
                relay_incarnation: crate::openai::unix_millis(),
            },
            enabled: Arc::new(AtomicBool::new(true)),
            load: Arc::default(),
        }
    }

    /// Disabling refuses new subscriptions and ends open streams, as a lost
    /// Relay would.
    pub(crate) fn set_enabled(&self, enabled: bool) {
        self.enabled.store(enabled, Ordering::Relaxed);
    }

    fn is_enabled(&self) -> bool {
        self.enabled.load(Ordering::Relaxed)
    }

    pub(crate) fn observe(&self, event: RequestProgress<'_>) {
        self.load
            .lock()
            .expect("relay load lock should not be poisoned")
            .observe(event);
    }
}

/// A request's progress, reported at each stats-stream event.
pub(crate) struct RequestProgress<'a> {
    pub(crate) request_id: &'a str,
    pub(crate) model: &'a str,
    /// The prompt length, known from admission like a Dynamo frontend's
    /// tokenized input.
    pub(crate) input_tokens: u64,
    pub(crate) output_tokens: u64,
    pub(crate) finished: bool,
}

pub(crate) fn grpc_router(state: AppState) -> axum::Router {
    let service = proto::kv_event_relay_server::KvEventRelayServer::new(MockRelay { state });
    tonic::service::Routes::new(service).into_axum_router()
}

#[derive(Clone)]
struct MockRelay {
    state: AppState,
}

type ResponseStream<T> = Pin<Box<dyn Stream<Item = Result<T, Status>> + Send + 'static>>;

#[tonic::async_trait]
impl proto::kv_event_relay_server::KvEventRelay for MockRelay {
    type WatchKvPoolCatalogStream = ResponseStream<proto::KvPoolCatalogUpdate>;
    type SubscribeKvPoolStream = ResponseStream<proto::FilterUpdate>;
    type SubscribeServingReadinessStream = ResponseStream<proto::ServingReadinessUpdate>;
    type SubscribeKvPoolLoadStream = ResponseStream<proto::KvPoolLoadUpdate>;
    type SubscribeServingLoadStream = ResponseStream<proto::ServingLoadUpdate>;

    async fn get_relay_info(
        &self,
        _request: Request<proto::RelayInfoRequest>,
    ) -> Result<Response<proto::RelayInfo>, Status> {
        Ok(Response::new(proto::RelayInfo {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(self.state.relay.identity),
            contract_marker: proto::CONTRACT_MARKER,
        }))
    }

    async fn watch_kv_pool_catalog(
        &self,
        _request: Request<proto::WatchKvPoolCatalogRequest>,
    ) -> Result<Response<Self::WatchKvPoolCatalogStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let stream = async_stream::stream! {
            yield Ok(catalog(state.relay.identity, &state.model_name));
            // The catalog never changes; hold the stream open until disabled.
            let mut interval = window_interval();
            while state.relay.is_enabled() {
                interval.tick().await;
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }

    async fn subscribe_kv_pool(
        &self,
        _request: Request<proto::SubscribeKvPoolRequest>,
    ) -> Result<Response<Self::SubscribeKvPoolStream>, Status> {
        Err(Status::unimplemented(
            "mock Dynamo does not model KV pool filters",
        ))
    }

    async fn subscribe_serving_readiness(
        &self,
        _request: Request<proto::SubscribeServingReadinessRequest>,
    ) -> Result<Response<Self::SubscribeServingReadinessStream>, Status> {
        Err(Status::unimplemented(
            "mock Dynamo does not model serving readiness",
        ))
    }

    async fn subscribe_kv_pool_load(
        &self,
        _request: Request<proto::SubscribeKvPoolLoadRequest>,
    ) -> Result<Response<Self::SubscribeKvPoolLoadStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let stream = async_stream::stream! {
            let mut interval = window_interval();
            // The Relay sends no window before its first snapshot.
            interval.tick().await;
            for window_sequence in 1.. {
                interval.tick().await;
                if !state.relay.is_enabled() {
                    break;
                }
                yield Ok(kv_pool_load(&state, window_sequence).await);
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }

    async fn subscribe_serving_load(
        &self,
        _request: Request<proto::SubscribeServingLoadRequest>,
    ) -> Result<Response<Self::SubscribeServingLoadStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let max_concurrency = state.engine.as_ref().map(|engine| engine.max_concurrency());
        let stream = async_stream::stream! {
            let mut interval = window_interval();
            interval.tick().await;
            for window_sequence in 1.. {
                interval.tick().await;
                if !state.relay.is_enabled() {
                    break;
                }
                let models = state
                    .relay
                    .load
                    .lock()
                    .expect("relay load lock should not be poisoned")
                    .models(&state.model_name);
                yield Ok(serving_load(
                    state.relay.identity,
                    window_sequence,
                    max_concurrency,
                    models,
                ));
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }
}

fn ensure_enabled(state: &AppState) -> Result<(), Status> {
    if state.relay.is_enabled() {
        Ok(())
    } else {
        Err(Status::unavailable("mock Relay disabled by test control"))
    }
}

fn window_interval() -> tokio::time::Interval {
    let mut interval = tokio::time::interval(WINDOW);
    interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    interval
}

/// Request totals since the mock started (one `relay_incarnation`) and the
/// requests in flight.
#[derive(Default)]
struct LoadAccumulator {
    live: HashMap<String, LiveRequest>,
    totals: BTreeMap<String, TrafficTotals>,
}

struct LiveRequest {
    model: String,
    input_tokens: u64,
    output_tokens: u64,
}

#[derive(Default)]
struct TrafficTotals {
    requests_started: u64,
    requests_completed: u64,
    input_tokens: u64,
    output_tokens: u64,
}

impl LoadAccumulator {
    fn observe(&mut self, event: RequestProgress<'_>) {
        let totals = self.totals.entry(event.model.to_string()).or_default();
        match self.live.get_mut(event.request_id) {
            Some(request) => {
                totals.output_tokens = totals
                    .output_tokens
                    .saturating_add(event.output_tokens.saturating_sub(request.output_tokens));
                request.output_tokens = request.output_tokens.max(event.output_tokens);
            }
            None => {
                totals.requests_started = totals.requests_started.saturating_add(1);
                totals.input_tokens = totals.input_tokens.saturating_add(event.input_tokens);
                totals.output_tokens = totals.output_tokens.saturating_add(event.output_tokens);
                if !event.finished {
                    self.live.insert(
                        event.request_id.to_string(),
                        LiveRequest {
                            model: event.model.to_string(),
                            input_tokens: event.input_tokens,
                            output_tokens: event.output_tokens,
                        },
                    );
                }
            }
        }
        if event.finished {
            self.live.remove(event.request_id);
            totals.requests_completed = totals.requests_completed.saturating_add(1);
        }
    }

    /// Every model with traffic, plus the configured one.
    fn models(&self, configured_model: &str) -> Vec<proto::ModelServingLoad> {
        let mut model_ids = BTreeSet::from([configured_model]);
        model_ids.extend(self.totals.keys().map(String::as_str));
        model_ids
            .into_iter()
            .map(|model| self.model(model))
            .collect()
    }

    fn model(&self, model: &str) -> proto::ModelServingLoad {
        let totals = self.totals.get(model);
        let live = self
            .live
            .values()
            .filter(|request| request.model == model)
            .collect::<Vec<_>>();
        let awaiting = || live.iter().filter(|request| request.output_tokens == 0);
        let observed_ms = crate::openai::unix_millis();
        proto::ModelServingLoad {
            namespace: NAMESPACE.to_string(),
            canonical_model_id: model.to_string(),
            load: Some(proto::LoadView {
                requests: Some(proto::RequestLifecycleStats {
                    requests_started_total: totals.map_or(0, |totals| totals.requests_started),
                    requests_completed_total: totals.map_or(0, |totals| totals.requests_completed),
                    requests_failed_total: 0,
                    requests_cancelled_total: 0,
                    requests_awaiting_first_token: Some(awaiting().count() as u64),
                    requests_generating: Some((live.len() - awaiting().count()) as u64),
                }),
                tokens: Some(proto::TokenLoadStats {
                    awaiting_first_token_input_tokens: Some(
                        awaiting().map(|request| request.input_tokens).sum(),
                    ),
                    inflight_input_tokens: Some(
                        live.iter().map(|request| request.input_tokens).sum(),
                    ),
                    input_tokens_total: Some(totals.map_or(0, |totals| totals.input_tokens)),
                    output_tokens_total: Some(totals.map_or(0, |totals| totals.output_tokens)),
                    ..Default::default()
                }),
                status: proto::DataStatus::Complete as i32,
                source_observed_ms: observed_ms,
            }),
            deployment: Some(proto::ModelDeploymentStatus {
                expected_frontends: 1,
                observed_frontends: 1,
                ready_frontends: 1,
                serving_pools: vec![pool_id()],
            }),
        }
    }
}

fn serving_load(
    relay: proto::RelayIdentity,
    window_sequence: u64,
    max_concurrency: Option<u64>,
    models: Vec<proto::ModelServingLoad>,
) -> proto::ServingLoadUpdate {
    let observed_ms = crate::openai::unix_millis();
    proto::ServingLoadUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(relay),
        window_sequence,
        observed_ms,
        window_ms: WINDOW.as_millis() as u64,
        pools: vec![proto::PoolServingLoad {
            producer: Some(producer()),
            load: Some(proto::LoadView {
                status: proto::DataStatus::Complete as i32,
                source_observed_ms: observed_ms,
                ..Default::default()
            }),
            deployment: Some(proto::PoolDeploymentStatus {
                live_workers: Some(1),
                max_concurrency,
            }),
        }],
        models,
        contract_marker: proto::CONTRACT_MARKER,
    }
}

async fn kv_pool_load(state: &AppState, window_sequence: u64) -> proto::KvPoolLoadUpdate {
    let stats = state.kv_cache_stats().await;
    proto::KvPoolLoadUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(state.relay.identity),
        window_sequence,
        observed_ms: crate::openai::unix_millis(),
        window_ms: WINDOW.as_millis() as u64,
        pools: vec![proto::KvPoolLoadEntry {
            producer: Some(producer()),
            kv_used_blocks: stats.kv_cache_used_tokens,
            total_kv_blocks: stats.kv_cache_capacity_tokens,
            kv_observed_ranks: 1,
            kv_expected_ranks: 1,
        }],
        contract_marker: proto::CONTRACT_MARKER,
    }
}

fn catalog(relay: proto::RelayIdentity, model: &str) -> proto::KvPoolCatalogUpdate {
    proto::KvPoolCatalogUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(relay),
        revision: 1,
        snapshot: Some(proto::KvPoolCatalogSnapshot {
            pools: vec![proto::KvPoolDescriptor {
                producer: Some(producer()),
                serving_endpoint: Some(proto::DynamoEndpointId {
                    namespace: NAMESPACE.to_string(),
                    component: "backend".to_string(),
                    endpoint: "generate".to_string(),
                }),
                registrations: vec![proto::ModelRegistration {
                    canonical_model_id: model.to_string(),
                    target: Some(proto::ModelTarget {
                        target: Some(proto::model_target::Target::Base(proto::BaseModelTarget {
                            base_model: model.to_string(),
                        })),
                    }),
                    aliases: Vec::new(),
                }],
                query_semantics: Some(proto::KvQuerySemantics {
                    kv_block_size: 1,
                    hash_format: proto::KvQueryHashFormat::DynamoStandardV1 as i32,
                }),
                pool_roles: vec![proto::WorkerRole::Aggregated as i32],
            }],
        }),
        contract_marker: proto::CONTRACT_MARKER,
    }
}

fn producer() -> proto::ProducerIdentity {
    proto::ProducerIdentity {
        pool_id: Some(pool_id()),
        producer_incarnation: 1,
        layout_generation: 1,
        ckf_format: None,
    }
}

fn pool_id() -> proto::KvPoolId {
    let digest = |byte| proto::DigestIdentity {
        digest: vec![byte; 16],
        source: proto::IdentitySource::DefaultDerived as i32,
    };
    proto::KvPoolId {
        identity_version: 1,
        indexer_domain: Some(proto::IndexerDomainId {
            cache_semantics: Some(digest(1)),
            routing_scope: Some(digest(2)),
        }),
        dc_id: 1,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn progress<'a>(
        request_id: &'a str,
        input_tokens: u64,
        output_tokens: u64,
        finished: bool,
    ) -> RequestProgress<'a> {
        RequestProgress {
            request_id,
            model: "model-a",
            input_tokens,
            output_tokens,
            finished,
        }
    }

    fn model_load(accumulator: &LoadAccumulator) -> proto::LoadView {
        accumulator.models("model-a")[0].load.unwrap()
    }

    #[test]
    fn admitted_requests_count_their_prompt_before_the_first_token() {
        let mut accumulator = LoadAccumulator::default();
        accumulator.observe(progress("req-1", 10, 0, false));

        let load = model_load(&accumulator);
        let requests = load.requests.unwrap();
        let tokens = load.tokens.unwrap();
        assert_eq!(requests.requests_started_total, 1);
        assert_eq!(requests.requests_awaiting_first_token, Some(1));
        assert_eq!(requests.requests_generating, Some(0));
        assert_eq!(tokens.awaiting_first_token_input_tokens, Some(10));
        assert_eq!(tokens.inflight_input_tokens, Some(10));
        assert_eq!(tokens.input_tokens_total, Some(10));
    }

    #[test]
    fn totals_stay_cumulative_while_gauges_follow_live_requests() {
        let mut accumulator = LoadAccumulator::default();
        accumulator.observe(progress("req-1", 10, 0, false));
        accumulator.observe(progress("req-1", 10, 2, false));
        accumulator.observe(progress("req-1", 10, 2, false));

        let load = model_load(&accumulator);
        assert_eq!(load.requests.unwrap().requests_generating, Some(1));
        assert_eq!(load.tokens.unwrap().output_tokens_total, Some(2));

        accumulator.observe(progress("req-1", 10, 5, true));
        // A non-streaming request reports once, already finished.
        accumulator.observe(progress("req-2", 4, 3, true));

        let load = model_load(&accumulator);
        let requests = load.requests.unwrap();
        let tokens = load.tokens.unwrap();
        assert_eq!(requests.requests_started_total, 2);
        assert_eq!(requests.requests_completed_total, 2);
        assert_eq!(requests.requests_awaiting_first_token, Some(0));
        assert_eq!(requests.requests_generating, Some(0));
        assert_eq!(tokens.inflight_input_tokens, Some(0));
        assert_eq!(tokens.input_tokens_total, Some(14));
        assert_eq!(tokens.output_tokens_total, Some(8));
    }

    #[test]
    fn the_configured_model_is_reported_before_any_traffic() {
        let models = LoadAccumulator::default().models("model-a");

        assert_eq!(models.len(), 1);
        assert_eq!(models[0].canonical_model_id, "model-a");
        assert_eq!(
            models[0].deployment.as_ref().unwrap().ready_frontends,
            1,
            "an idle model is still ready"
        );
    }
}
