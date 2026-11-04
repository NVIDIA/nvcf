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

use super::raw_quic::{close_quic_clients, connect_quic_set, start_raw_quic_server};
use super::summary::{summarize_aggregates, summarize_comparisons, summarize_samples};
use super::trials::chunks;
use super::*;
use crate::statistics::summarize_distribution;
use stargate_protocol::TunnelTransportProtocol;
use std::collections::{BTreeMap, BTreeSet};

fn test_config() -> TransportBenchConfig {
    TransportBenchConfig {
        request_count: 1,
        concurrency: 1,
        quic_connections: 1,
        warmup_requests: 0,
        request_body_bytes: 32,
        response_body_bytes: 64,
        request_chunk_bytes: 16,
        response_chunk_bytes: 16,
        quic_send_fairness: true,
        http3_send_grease: true,
        trials: 1,
        warmup_trials: 0,
        cooldown_ms: 0,
        randomize_order: false,
        noise_threshold_cv: 0.02,
        min_effect_size_percent: 1.0,
    }
}

fn successful_summary(
    transport: TransportKind,
    throughput_rps: f64,
    goodput_mib_s: f64,
    latency_p95: u64,
    response_headers_p95: u64,
    first_body_p95: u64,
) -> TransportRunSummary {
    TransportRunSummary {
        transport,
        request_count: 1,
        success_count: 1,
        failure_count: 0,
        measured_duration_ms: 1,
        throughput_rps,
        goodput_mib_s,
        latency_us: LatencySummary {
            p95: Some(latency_p95),
            ..LatencySummary::default()
        },
        response_headers_us: LatencySummary {
            p95: Some(response_headers_p95),
            ..LatencySummary::default()
        },
        first_body_us: LatencySummary {
            p95: Some(first_body_p95),
            ..LatencySummary::default()
        },
    }
}

fn aggregate_fixture(
    trial_count: usize,
    classification: NoiseClassification,
    throughput: &[f64],
) -> TransportAggregateSummary {
    TransportAggregateSummary {
        transport: TransportKind::RawQuic,
        trial_count,
        classification,
        throughput_rps: summarize_distribution(throughput, 1),
        goodput_mib_s: summarize_distribution(&[1.0], 2),
        latency_p95_us: summarize_distribution(&[1.0], 3),
        response_headers_p95_us: summarize_distribution(&[1.0], 4),
        first_body_p95_us: summarize_distribution(&[1.0], 5),
    }
}

#[test]
fn summarizes_successful_samples_only() {
    let samples = vec![
        RequestSample {
            request_index: 0,
            connection_index: 0,
            ok: true,
            response_status: Some(200),
            request_bytes: 10,
            response_bytes: 20,
            response_headers_us: Some(100),
            first_body_us: Some(120),
            completion_us: 200,
            error: None,
        },
        RequestSample {
            request_index: 1,
            connection_index: 0,
            ok: false,
            response_status: Some(500),
            request_bytes: 10,
            response_bytes: 0,
            response_headers_us: Some(80),
            first_body_us: None,
            completion_us: 90,
            error: Some("boom".to_string()),
        },
        RequestSample {
            request_index: 2,
            connection_index: 0,
            ok: true,
            response_status: Some(200),
            request_bytes: 10,
            response_bytes: 20,
            response_headers_us: Some(110),
            first_body_us: Some(130),
            completion_us: 300,
            error: None,
        },
    ];

    let summary = summarize_samples(TransportKind::RawQuic, &samples, Duration::from_millis(100));

    assert_eq!(summary.request_count, 3);
    assert_eq!(summary.success_count, 2);
    assert_eq!(summary.failure_count, 1);
    assert_eq!(summary.latency_us.min, Some(200));
    assert_eq!(summary.latency_us.p50, Some(300));
    assert_eq!(summary.response_headers_us.p95, Some(110));
    assert!(summary.throughput_rps > 19.0);
    assert!(summary.goodput_mib_s > 0.0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn loopback_benchmark_exercises_all_transports() {
    let outcome = tokio::time::timeout(
        Duration::from_secs(20),
        run_transport_benchmark(TransportBenchConfig {
            request_count: 4,
            concurrency: 2,
            quic_connections: 2,
            warmup_requests: 1,
            cooldown_ms: 1,
            ..test_config()
        }),
    )
    .await
    .expect("benchmark should not hang")
    .expect("benchmark should complete");

    let by_transport = outcome
        .runs
        .iter()
        .map(|run| (run.transport, &run.summary))
        .collect::<BTreeMap<_, _>>();
    assert_eq!(by_transport.len(), 3);
    for transport in [
        TransportKind::RawQuic,
        TransportKind::Http3H3Quinn,
        TransportKind::WebTransportH3Quinn,
    ] {
        let summary = by_transport
            .get(&transport)
            .expect("summary should exist for transport");
        assert_eq!(summary.request_count, 4);
        assert_eq!(summary.success_count, 4);
        assert_eq!(summary.failure_count, 0);
        assert!(summary.latency_us.p50.is_some());
        let run = outcome
            .runs
            .iter()
            .find(|run| run.transport == transport)
            .expect("run should exist for transport");
        assert_eq!(
            run.samples
                .iter()
                .map(|sample| sample.request_index)
                .collect::<Vec<_>>(),
            [0, 1, 2, 3]
        );
        assert_eq!(
            run.samples
                .iter()
                .map(|sample| sample.connection_index)
                .collect::<Vec<_>>(),
            [0, 1, 0, 1]
        );
    }
    assert_eq!(outcome.aggregates.len(), 3);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn quic_connection_set_uses_one_client_endpoint() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let config = TransportBenchConfig {
        request_count: 2,
        concurrency: 2,
        quic_connections: 2,
        ..test_config()
    };
    let server = start_raw_quic_server(config, Arc::new(chunks(64, 16, b's')))
        .await
        .expect("Raw QUIC transport benchmark server should start");
    let clients = connect_quic_set(
        config,
        server.addr,
        TunnelTransportProtocol::RawQuic.alpn_protocols(),
        &server.cert_pem,
    )
    .await
    .expect("client connections should open");

    let endpoint_addrs = clients
        .iter()
        .map(|(endpoint, _)| endpoint.local_addr().expect("endpoint local addr"))
        .collect::<BTreeSet<_>>();
    assert_eq!(endpoint_addrs.len(), 1);

    close_quic_clients(clients).await;
    server.shutdown().await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn loopback_benchmark_exercises_webtransport_with_grease_disabled() {
    let outcome = tokio::time::timeout(
        Duration::from_secs(20),
        run_transport_benchmark(TransportBenchConfig {
            request_count: 2,
            http3_send_grease: false,
            ..test_config()
        }),
    )
    .await
    .expect("benchmark should not hang")
    .expect("benchmark should complete");

    let webtransport = outcome
        .runs
        .iter()
        .find(|run| run.transport == TransportKind::WebTransportH3Quinn)
        .expect("WebTransport run should exist");
    assert_eq!(webtransport.summary.success_count, 2);
    assert_eq!(webtransport.summary.failure_count, 0);
}

#[test]
fn report_includes_transport_knobs_and_ttft_tails() {
    let outcome = TransportBenchmarkOutcome {
        config: TransportBenchConfig {
            request_body_bytes: 16,
            response_body_bytes: 32,
            quic_send_fairness: false,
            http3_send_grease: false,
            ..test_config()
        },
        runs: vec![TransportRunOutcome {
            transport: TransportKind::Http3H3Quinn,
            trial_index: 1,
            samples: Vec::new(),
            summary: TransportRunSummary {
                latency_us: LatencySummary {
                    p50: Some(10),
                    p95: Some(20),
                    p99: Some(30),
                    max: Some(40),
                    ..LatencySummary::default()
                },
                response_headers_us: LatencySummary {
                    p50: Some(5),
                    p95: Some(6),
                    p99: Some(7),
                    ..LatencySummary::default()
                },
                first_body_us: LatencySummary {
                    p50: Some(8),
                    p95: Some(9),
                    p99: Some(10),
                    ..LatencySummary::default()
                },
                ..successful_summary(TransportKind::Http3H3Quinn, 1.0, 1.0, 20, 6, 9)
            },
        }],
        warmup_runs: Vec::new(),
        aggregates: vec![TransportAggregateSummary {
            transport: TransportKind::Http3H3Quinn,
            trial_count: 1,
            classification: NoiseClassification::Inconclusive,
            throughput_rps: summarize_distribution(&[1.0], 1),
            goodput_mib_s: summarize_distribution(&[1.0], 2),
            latency_p95_us: summarize_distribution(&[20.0], 3),
            response_headers_p95_us: summarize_distribution(&[6.0], 4),
            first_body_p95_us: summarize_distribution(&[9.0], 5),
        }],
        comparisons: vec![TransportComparisonSummary {
            baseline: TransportKind::RawQuic,
            candidate: TransportKind::Http3H3Quinn,
            throughput_delta_percent: Some(12.5),
            min_effect_size_percent: 1.0,
            confidence_intervals_overlap: Some(false),
            meaningful_difference: true,
        }],
    };

    let report = render_transport_benchmark_report(&outcome);

    assert!(report.contains("QUIC send fairness: `false`"));
    assert!(report.contains("QUIC connections: `1`"));
    assert!(report.contains("HTTP/3 grease: `false`"));
    assert!(report.contains("Trials: `1`"));
    assert!(report.contains("Min effect size"));
    assert!(report.contains("## Aggregate"));
    assert!(report.contains("## Comparisons"));
    assert!(report.contains("| raw-quic | http3-h3-quinn | 12.50% | false | true |"));
    assert!(report.contains("Headers P95"));
    assert!(report.contains("First Body P99"));
}

#[test]
fn aggregate_classifies_repeated_transport_trials() {
    let runs = vec![
        TransportRunOutcome {
            transport: TransportKind::RawQuic,
            trial_index: 1,
            samples: Vec::new(),
            summary: successful_summary(TransportKind::RawQuic, 100.0, 1.0, 100, 50, 75),
        },
        TransportRunOutcome {
            transport: TransportKind::RawQuic,
            trial_index: 2,
            samples: Vec::new(),
            summary: successful_summary(TransportKind::RawQuic, 100.5, 1.1, 101, 51, 76),
        },
    ];

    let aggregate = summarize_aggregates(&runs, 0.02)
        .into_iter()
        .next()
        .expect("aggregate should exist");

    assert_eq!(aggregate.transport, TransportKind::RawQuic);
    assert_eq!(aggregate.trial_count, 2);
    assert_eq!(aggregate.classification, NoiseClassification::Reliable);
}

#[test]
fn comparison_requires_minimum_effect_and_non_overlapping_intervals() {
    let mut raw_quic = aggregate_fixture(3, NoiseClassification::Reliable, &[100.0, 101.0, 99.0]);
    let mut http3 = raw_quic.clone();
    http3.transport = TransportKind::Http3H3Quinn;
    http3.throughput_rps = summarize_distribution(&[80.0, 81.0, 79.0], 6);

    let comparison = summarize_comparisons(&[raw_quic.clone(), http3], 5.0)
        .into_iter()
        .next()
        .expect("comparison should exist");
    assert!(comparison.meaningful_difference);

    raw_quic.throughput_rps = summarize_distribution(&[100.0, 101.0, 99.0], 7);
    let mut close = raw_quic.clone();
    close.transport = TransportKind::Http3H3Quinn;
    close.throughput_rps = summarize_distribution(&[99.5, 100.5, 100.0], 8);
    let comparison = summarize_comparisons(&[raw_quic, close], 5.0)
        .into_iter()
        .next()
        .expect("comparison should exist");
    assert!(!comparison.meaningful_difference);
}

#[test]
fn comparisons_include_each_non_baseline_transport() {
    let raw_quic = aggregate_fixture(3, NoiseClassification::Reliable, &[100.0, 101.0, 99.0]);
    let mut http3 = raw_quic.clone();
    http3.transport = TransportKind::Http3H3Quinn;
    http3.throughput_rps = summarize_distribution(&[90.0, 91.0, 89.0], 6);
    let mut webtransport = raw_quic.clone();
    webtransport.transport = TransportKind::WebTransportH3Quinn;
    webtransport.throughput_rps = summarize_distribution(&[80.0, 81.0, 79.0], 7);

    let comparisons = summarize_comparisons(&[raw_quic, http3, webtransport], 5.0);

    assert_eq!(comparisons.len(), 2);
    assert_eq!(comparisons[0].baseline, TransportKind::RawQuic);
    assert_eq!(comparisons[0].candidate, TransportKind::Http3H3Quinn);
    assert_eq!(comparisons[1].baseline, TransportKind::RawQuic);
    assert_eq!(comparisons[1].candidate, TransportKind::WebTransportH3Quinn);
    assert!(comparisons.iter().all(|comparison| {
        comparison.confidence_intervals_overlap == Some(false) && comparison.meaningful_difference
    }));
}

#[test]
fn single_trial_comparison_is_not_meaningful() {
    let raw_quic = aggregate_fixture(1, NoiseClassification::Inconclusive, &[100.0]);
    let mut http3 = raw_quic.clone();
    http3.transport = TransportKind::Http3H3Quinn;
    http3.throughput_rps = summarize_distribution(&[80.0], 6);

    let comparison = summarize_comparisons(&[raw_quic, http3], 1.0)
        .into_iter()
        .next()
        .expect("comparison should exist");

    assert_eq!(comparison.confidence_intervals_overlap, None);
    assert!(!comparison.meaningful_difference);
}

#[test]
fn repeated_trial_artifacts_include_trial_numbered_samples() {
    let tempdir = tempfile::tempdir().expect("tempdir should create");
    fn empty_run(transport: TransportKind, trial_index: usize) -> TransportRunOutcome {
        TransportRunOutcome {
            transport,
            trial_index,
            samples: vec![RequestSample {
                request_index: trial_index,
                connection_index: 0,
                ok: true,
                response_status: Some(200),
                request_bytes: 1,
                response_bytes: 1,
                response_headers_us: Some(1),
                first_body_us: Some(1),
                completion_us: 1,
                error: None,
            }],
            summary: summarize_samples(transport, &[], Duration::ZERO),
        }
    }

    let outcome = TransportBenchmarkOutcome {
        config: TransportBenchConfig {
            request_body_bytes: 1,
            response_body_bytes: 1,
            request_chunk_bytes: 1,
            response_chunk_bytes: 1,
            trials: 2,
            ..test_config()
        },
        runs: [
            TransportKind::RawQuic,
            TransportKind::Http3H3Quinn,
            TransportKind::WebTransportH3Quinn,
        ]
        .into_iter()
        .flat_map(|transport| (1..=2).map(move |trial| empty_run(transport, trial)))
        .collect(),
        warmup_runs: Vec::new(),
        aggregates: Vec::new(),
        comparisons: Vec::new(),
    };

    write_transport_benchmark_artifacts(tempdir.path(), &outcome).expect("artifacts should write");

    for transport in ["raw-quic", "http3-h3-quinn", "webtransport-h3-quinn"] {
        for trial in 1..=2 {
            assert!(
                tempdir
                    .path()
                    .join(format!("transport-samples-{transport}-trial-{trial}.jsonl"))
                    .exists()
            );
        }
    }
    let sample_json = std::fs::read_to_string(
        tempdir
            .path()
            .join("transport-samples-webtransport-h3-quinn-trial-2.jsonl"),
    )
    .expect("sample file should be readable");
    let sample_record = serde_json::from_str::<serde_json::Value>(
        sample_json
            .lines()
            .next()
            .expect("sample file should include a JSONL row"),
    )
    .expect("sample row should parse as JSON");
    assert_eq!(
        sample_record["transport"],
        serde_json::to_value(TransportKind::WebTransportH3Quinn)
            .expect("transport should serialize")
    );
    assert_eq!(sample_record["trial_index"].as_u64(), Some(2));
    assert_eq!(sample_record["request_index"].as_u64(), Some(2));
    assert_eq!(sample_record["ok"].as_bool(), Some(true));
}
