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

//! The Relay translation contract, tested from the Relay's wire messages to
//! the stats payloads, independently of the translation's own unit tests.

use stargate_proto::dynamo_kvrelay as proto;

use super::relay_translate::{KvPoolCatalog, RelayLoadTranslator, kv_update};
use super::relay_update::RelayLoadUpdate;

const MODEL: &str = "llama";
const BLOCK_SIZE: u64 = 16;

fn relay(relay_incarnation: u64) -> proto::RelayIdentity {
    proto::RelayIdentity {
        drt_instance_id: 7,
        relay_incarnation,
    }
}

fn pool_id(seed: u8) -> proto::KvPoolId {
    let digest = |seed: u8, source: proto::IdentitySource| proto::DigestIdentity {
        digest: vec![seed; 16],
        source: source as i32,
    };
    proto::KvPoolId {
        identity_version: 1,
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

/// A pool serving `model` with the given worker roles (as raw enum values, so
/// a role this Pylon does not know can be expressed).
fn descriptor(seed: u8, roles: &[i32], model: &str) -> proto::KvPoolDescriptor {
    proto::KvPoolDescriptor {
        producer: Some(producer(seed)),
        serving_endpoint: None,
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
            kv_block_size: BLOCK_SIZE as u32,
            hash_format: proto::KvQueryHashFormat::DynamoStandardV1 as i32,
        }),
        pool_roles: roles.to_vec(),
    }
}

fn aggregated() -> Vec<i32> {
    vec![proto::WorkerRole::Aggregated as i32]
}

fn catalog(pools: Vec<proto::KvPoolDescriptor>) -> KvPoolCatalog {
    KvPoolCatalog::from_proto(proto::KvPoolCatalogUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(relay(2)),
        revision: 1,
        snapshot: Some(proto::KvPoolCatalogSnapshot { pools }),
        contract_marker: proto::CONTRACT_MARKER,
    })
    .expect("a well-formed catalog should translate")
}

fn kv_entry(
    seed: u8,
    total: u64,
    used: u64,
    observed: u32,
    expected: u32,
) -> proto::KvPoolLoadEntry {
    proto::KvPoolLoadEntry {
        producer: Some(producer(seed)),
        kv_used_blocks: used,
        total_kv_blocks: total,
        kv_observed_ranks: observed,
        kv_expected_ranks: expected,
    }
}

fn kv_window(pools: Vec<proto::KvPoolLoadEntry>) -> proto::KvPoolLoadUpdate {
    proto::KvPoolLoadUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(relay(2)),
        window_sequence: 1,
        observed_ms: 1_000,
        window_ms: 1_000,
        pools,
        contract_marker: proto::CONTRACT_MARKER,
    }
}

/// One model's view in a serving-load window.
struct View {
    status: proto::DataStatus,
    ready_frontends: u64,
    serving: bool,
    source_observed_ms: u64,
    output_tokens_total: u64,
}

impl View {
    fn complete() -> Self {
        Self {
            status: proto::DataStatus::Complete,
            ready_frontends: 1,
            serving: true,
            source_observed_ms: 5_000,
            output_tokens_total: 0,
        }
    }
}

fn model_view(view: &View) -> proto::ModelServingLoad {
    let complete = view.status == proto::DataStatus::Complete;
    proto::ModelServingLoad {
        namespace: "dynamo".to_string(),
        canonical_model_id: MODEL.to_string(),
        load: Some(proto::LoadView {
            requests: Some(proto::RequestLifecycleStats {
                requests_started_total: 4,
                requests_completed_total: 3,
                requests_failed_total: 0,
                requests_cancelled_total: 0,
                requests_awaiting_first_token: complete.then_some(2),
                requests_generating: complete.then_some(1),
            }),
            tokens: Some(proto::TokenLoadStats {
                awaiting_first_token_input_tokens: complete.then_some(17),
                inflight_input_tokens: complete.then_some(31),
                input_tokens_total: Some(0),
                output_tokens_total: Some(view.output_tokens_total),
                ..Default::default()
            }),
            status: view.status as i32,
            source_observed_ms: view.source_observed_ms,
        }),
        deployment: Some(proto::ModelDeploymentStatus {
            expected_frontends: 1,
            observed_frontends: u64::from(view.status != proto::DataStatus::Unavailable),
            ready_frontends: view.ready_frontends,
            serving_pools: if view.serving {
                vec![pool_id(1)]
            } else {
                Vec::new()
            },
        }),
    }
}

fn load_window(
    relay_incarnation: u64,
    window_sequence: u64,
    observed_ms: u64,
    view: &View,
) -> proto::ServingLoadUpdate {
    proto::ServingLoadUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(relay(relay_incarnation)),
        window_sequence,
        observed_ms,
        window_ms: 1_000,
        pools: Vec::new(),
        models: vec![model_view(view)],
        contract_marker: proto::CONTRACT_MARKER,
    }
}

fn translate(view: &View) -> RelayLoadUpdate {
    RelayLoadTranslator::default()
        .translate(
            &catalog(vec![descriptor(1, &aggregated(), MODEL)]),
            load_window(2, 1, 1_000, view),
        )
        .expect("a well-formed window should translate")
}

/// 1: readiness comes from a ready frontend and a serving pool, whatever the
/// window's coverage; coverage only decides whether the gauges are trusted.
#[test]
fn readiness_is_independent_of_load_coverage() {
    let complete = translate(&View::complete());
    let model = &complete.models[MODEL];
    assert!(model.ready);
    assert!(model.load.is_some(), "a complete window carries gauges");

    let degraded = translate(&View {
        status: proto::DataStatus::Degraded,
        ..View::complete()
    });
    let model = &degraded.models[MODEL];
    assert!(
        model.ready,
        "a degraded window must not deactivate a ready model"
    );
    assert!(
        model.load.is_none(),
        "a degraded window must not report gauges"
    );

    let no_ready_frontend = translate(&View {
        status: proto::DataStatus::Unavailable,
        ready_frontends: 0,
        ..View::complete()
    });
    assert!(
        no_ready_frontend
            .models
            .get(MODEL)
            .is_none_or(|model| !model.ready && model.load.is_none())
    );

    let no_serving_pool = translate(&View {
        serving: false,
        ..View::complete()
    });
    assert!(
        no_serving_pool
            .models
            .get(MODEL)
            .is_none_or(|model| !model.ready)
    );
}

/// 2: rates use the Relay's clock between windows, not the frontends'
/// publish times, and start over with each Relay incarnation.
#[test]
fn rates_use_the_relay_clock_and_restart_per_incarnation() {
    let catalog = catalog(vec![descriptor(1, &aggregated(), MODEL)]);
    let mut translator = RelayLoadTranslator::default();
    let first = translator
        .translate(&catalog, load_window(2, 1, 10_000, &View::complete()))
        .unwrap();
    assert_eq!(
        first.models[MODEL].load.as_ref().unwrap().output_tps,
        None,
        "the first window of an incarnation has no rate"
    );

    // One Relay second later, while the oldest frontend publish time moved
    // backwards (a different frontend is now the oldest).
    let second = translator
        .translate(
            &catalog,
            load_window(
                2,
                2,
                11_000,
                &View {
                    source_observed_ms: 4_800,
                    output_tokens_total: 100,
                    ..View::complete()
                },
            ),
        )
        .unwrap();
    assert_eq!(second.observed_at_unix_ms, 11_000);
    assert_eq!(
        second.models[MODEL].load.as_ref().unwrap().output_tps,
        Some(100.0)
    );

    let restarted = translator
        .translate(
            &catalog,
            load_window(
                3,
                1,
                12_000,
                &View {
                    output_tokens_total: 150,
                    ..View::complete()
                },
            ),
        )
        .unwrap();
    assert_eq!(
        restarted.models[MODEL].load.as_ref().unwrap().output_tps,
        None,
        "a new Relay incarnation starts a new rate baseline"
    );
}

/// 6a: a model's KV usage is reported only when every pool serving it gives
/// complete, nonzero usage; anything partial reads as absent, never as zero
/// usage of a real capacity.
#[test]
fn kv_usage_is_reported_only_when_complete() {
    const UNKNOWN_ROLE: i32 = 1_000;
    let catalog = catalog(vec![
        descriptor(1, &aggregated(), "complete"),
        descriptor(2, &aggregated(), "partial"),
        descriptor(3, &aggregated(), "zero-capacity"),
        descriptor(4, &aggregated(), "missing"),
        descriptor(5, &[UNKNOWN_ROLE], "quarantined"),
    ]);
    let update = kv_update(
        &catalog,
        kv_window(vec![
            kv_entry(1, 100, 40, 2, 2),
            kv_entry(2, 100, 40, 1, 2),
            kv_entry(3, 0, 0, 1, 1),
            kv_entry(5, 100, 40, 1, 1),
        ]),
    )
    .expect("a well-formed KV window should translate");

    let complete = update.models["complete"];
    assert_eq!(complete.capacity_tokens, 100 * BLOCK_SIZE);
    assert_eq!(complete.used_tokens, 40 * BLOCK_SIZE);
    for absent in ["partial", "zero-capacity", "missing", "quarantined"] {
        assert!(
            !update.models.contains_key(absent),
            "{absent} must not report KV usage"
        );
    }
}

mod session {
    use std::time::Duration;

    use tokio::sync::mpsc;
    use tokio_stream::wrappers::UnboundedReceiverStream;
    use tokio_util::sync::CancellationToken;

    use super::super::StatsAggregatorUpdate;
    use super::super::relay_stream::{DynamoRelayConfig, RelaySubscriber, SessionEnd};
    use super::*;

    type Feed<T> = mpsc::UnboundedSender<Result<T, tonic::Status>>;

    struct Session {
        catalogs: Feed<proto::KvPoolCatalogUpdate>,
        kv_loads: Feed<proto::KvPoolLoadUpdate>,
        serving_loads: Feed<proto::ServingLoadUpdate>,
        updates: flume::Receiver<StatsAggregatorUpdate>,
        stop: CancellationToken,
        end: tokio::task::JoinHandle<SessionEnd>,
    }

    impl Session {
        fn start() -> Self {
            let (catalogs, catalog_stream) = mpsc::unbounded_channel();
            let (kv_loads, kv_stream) = mpsc::unbounded_channel();
            let (serving_loads, serving_stream) = mpsc::unbounded_channel();
            let (tx, updates) = flume::unbounded();
            let stop = CancellationToken::new();
            let mut subscriber = RelaySubscriber::new(DynamoRelayConfig::new("", "test"), tx);
            let end = tokio::spawn({
                let stop = stop.clone();
                async move {
                    subscriber
                        .drive(
                            UnboundedReceiverStream::new(catalog_stream),
                            UnboundedReceiverStream::new(kv_stream),
                            UnboundedReceiverStream::new(serving_stream),
                            &stop,
                        )
                        .await
                }
            });
            Self {
                catalogs,
                kv_loads,
                serving_loads,
                updates,
                stop,
                end,
            }
        }

        /// Sends the catalog and one window on each load stream.
        async fn open(&self, window_ms: u64) {
            self.catalogs
                .send(Ok(catalog_update(vec![descriptor(
                    1,
                    &aggregated(),
                    MODEL,
                )])))
                .unwrap();
            settle().await;
            self.windows(1, window_ms).await;
        }

        async fn windows(&self, window_sequence: u64, window_ms: u64) {
            let mut kv = kv_window(vec![kv_entry(1, 100, 40, 1, 1)]);
            kv.window_sequence = window_sequence;
            kv.window_ms = window_ms;
            self.kv_loads.send(Ok(kv)).unwrap();
            let mut load = load_window(
                2,
                window_sequence,
                1_000 * window_sequence,
                &View::complete(),
            );
            load.window_ms = window_ms;
            self.serving_loads.send(Ok(load)).unwrap();
            settle().await;
        }

        /// Asserts the session has ended as `expected`, having retracted its
        /// data with `RelayLost` as its last update and never asked for the
        /// OpenAI fallback.
        async fn assert_lost(self, expected: &'static str) {
            let end = tokio::time::timeout(Duration::from_secs(1), self.end)
                .await
                .expect("the session should have ended")
                .unwrap();
            assert_eq!(end, SessionEnd::Lost(expected));
            let updates = self.updates.drain().collect::<Vec<_>>();
            assert!(
                matches!(updates.last(), Some(StatsAggregatorUpdate::RelayLost)),
                "a lost session must retract its data last: {updates:?}"
            );
            assert!(
                !updates
                    .iter()
                    .any(|update| matches!(update, StatsAggregatorUpdate::EnableOpenAiFallback)),
                "a lost Relay must not fall back to OpenAI stats"
            );
        }
    }

    fn catalog_update(pools: Vec<proto::KvPoolDescriptor>) -> proto::KvPoolCatalogUpdate {
        proto::KvPoolCatalogUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(relay(2)),
            revision: 1,
            snapshot: Some(proto::KvPoolCatalogSnapshot { pools }),
            contract_marker: proto::CONTRACT_MARKER,
        }
    }

    async fn settle() {
        tokio::time::sleep(Duration::from_millis(1)).await;
    }

    /// 3: a hung Relay is detected after three of its windows (at least 3 s),
    /// not after a fixed 3 s, and the first window gets a longer allowance.
    #[tokio::test(start_paused = true)]
    async fn silence_is_measured_in_the_relays_own_windows() {
        let session = Session::start();
        session.open(5_000).await;
        tokio::time::sleep(Duration::from_secs(14)).await;
        assert!(
            !session.end.is_finished(),
            "with 5 s windows, 14 s of silence is not yet three windows"
        );
        assert!(
            !session
                .updates
                .drain()
                .any(|update| matches!(update, StatsAggregatorUpdate::RelayLost))
        );
        tokio::time::sleep(Duration::from_secs(2)).await;
        session.assert_lost("silence").await;

        let first = Session::start();
        first
            .catalogs
            .send(Ok(catalog_update(vec![descriptor(
                1,
                &aggregated(),
                MODEL,
            )])))
            .unwrap();
        tokio::time::sleep(Duration::from_secs(29)).await;
        assert!(
            !first.end.is_finished(),
            "a load stream gets 30 s for its first window"
        );
        tokio::time::sleep(Duration::from_secs(2)).await;
        first.assert_lost("silence").await;
    }

    /// 4: every way of losing the Relay fails closed: the session ends and
    /// retracts its data with `RelayLost`.
    #[tokio::test(start_paused = true)]
    async fn losing_the_relay_always_retracts_its_data() {
        // The Relay stops sending.
        let silent = Session::start();
        silent.open(1_000).await;
        tokio::time::sleep(Duration::from_secs(4)).await;
        silent.assert_lost("silence").await;

        // A stream ends.
        let ended = Session::start();
        ended.open(1_000).await;
        drop(ended.serving_loads);
        let ended = Session {
            serving_loads: mpsc::unbounded_channel().0,
            ..ended
        };
        ended.assert_lost("stream_ended").await;

        // The Relay restarts under the session.
        let restarted = Session::start();
        restarted.open(1_000).await;
        restarted
            .serving_loads
            .send(Ok(load_window(3, 2, 2_000, &View::complete())))
            .unwrap();
        settle().await;
        restarted.assert_lost("relay_restart").await;

        // A window breaks the contract.
        let skewed = Session::start();
        skewed.open(1_000).await;
        let mut window = load_window(2, 2, 2_000, &View::complete());
        window.contract_marker = 0;
        skewed.serving_loads.send(Ok(window)).unwrap();
        settle().await;
        skewed.assert_lost("contract").await;
    }

    /// 4: stopping Pylon ends the session without retracting anything; the
    /// collector is shutting down too.
    #[tokio::test(start_paused = true)]
    async fn stopping_pylon_ends_the_session_quietly() {
        let session = Session::start();
        session.open(1_000).await;
        session.updates.drain().for_each(drop);
        session.stop.cancel();
        let end = tokio::time::timeout(Duration::from_secs(1), session.end)
            .await
            .expect("the session should stop")
            .unwrap();
        assert_eq!(end, SessionEnd::Stopped);
        assert!(session.updates.is_empty());
    }
}
