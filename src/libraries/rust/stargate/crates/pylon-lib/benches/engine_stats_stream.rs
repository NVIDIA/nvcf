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

use std::time::{Duration, Instant};
use std::{convert::Infallible, sync::Arc};

use axum::{
    Router,
    body::{Body, Bytes},
    extract::State,
    response::Response,
    routing::get,
};
use criterion::{Criterion, Throughput, black_box, criterion_group, criterion_main};
use futures::{StreamExt, stream};
use pylon_lib::{
    EngineStatsStreamConfig, EngineStatsStreamMode, PylonRuntimeState, RequestCounterUpdate,
    RequestCounterUpdateInput, StatsAggregatorUpdate, StatsCollectorConfig, StatsCollectorHandle,
    StatsUpdateSource, parse_engine_stats_line_for_benchmark, start_engine_stats_stream,
    start_stats_collector_with_engine_stats, stats_aggregator_update_channel,
};
use tokio::net::TcpListener;
use tokio::task::JoinHandle;
use tokio::time::Instant as TokioInstant;

const EVENT_COUNT: u64 = 50_000;
const TEST_EVENT_COUNT: u64 = 1_024;
const TEST_SENTINEL_TIMEOUT: Duration = Duration::from_secs(10);
const REQUEST_IDS: u64 = 1_024;
const SENTINEL_OUTPUT_TOKENS: u64 = 10_000;
const SENTINEL_OUTPUT_TPS: f64 = SENTINEL_OUTPUT_TOKENS as f64;
const COMPACT_STATS_EVENT: &[u8] = br#"{"v":1,"type":"stats","request_id":"req-1","model":"model-a","tokens_processed":4096,"tokens_generated":128}"#;

fn bench_engine_stats_stream(c: &mut Criterion) {
    let test_mode = running_in_criterion_test_mode();
    let (event_count, scale) = if test_mode {
        (TEST_EVENT_COUNT, "smoke")
    } else {
        (EVENT_COUNT, "50k")
    };
    let collector_benchmark_name =
        format!("collector_channel_{scale}_request_counters_to_final_snapshot");
    let endpoint_benchmark_name =
        format!("http_endpoint_to_collector_{scale}_request_counters_to_final_snapshot");

    let mut parser = c.benchmark_group("engine_stats_stream_parser");
    parser.throughput(Throughput::Elements(1));
    parser.bench_function("parse_compact_request_counter", |b| {
        b.iter(|| {
            parse_engine_stats_line_for_benchmark(
                black_box(COMPACT_STATS_EVENT),
                black_box(TokioInstant::now()),
            )
        })
    });
    parser.finish();

    let mut pipeline = c.benchmark_group("engine_stats_stream_pipeline");
    pipeline.sample_size(10);
    pipeline.throughput(Throughput::Elements(event_count));
    pipeline.bench_function(collector_benchmark_name, |b| {
        b.iter_custom(|iters| {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_time()
                .build()
                .expect("benchmark runtime should build");
            let mut total = Duration::ZERO;
            for _ in 0..iters {
                total += runtime.block_on(ingest_and_apply_request_counters(event_count));
            }
            total
        })
    });
    pipeline.bench_function(endpoint_benchmark_name, |b| {
        let events = Arc::new(endpoint_event_lines(event_count));
        b.iter_custom(|iters| {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .expect("benchmark runtime should build");
            let mut total = Duration::ZERO;
            for _ in 0..iters {
                total += runtime.block_on(ingest_endpoint_to_collector(events.clone()));
            }
            total
        })
    });
    pipeline.finish();
}

fn running_in_criterion_test_mode() -> bool {
    let arguments: Vec<_> = std::env::args_os().collect();
    // Criterion treats invocations without `--bench` as cargo-test smoke runs.
    !arguments.iter().any(|arg| arg == "--bench") || arguments.iter().any(|arg| arg == "--test")
}

async fn ingest_and_apply_request_counters(event_count: u64) -> Duration {
    let (runtime_state, stats_update_tx, collector) = start_benchmark_collector();

    let observed_start = TokioInstant::now();
    let started_at = Instant::now();
    for index in 0..event_count {
        let request_index = index % REQUEST_IDS;
        let step = index / REQUEST_IDS + 1;
        stats_update_tx
            .send_async(request_update(
                format!("req-{request_index}"),
                step * 8,
                step,
                false,
                observed_start + Duration::from_millis(index),
            ))
            .await
            .expect("stats collector should receive benchmark update");
    }
    send_sentinel_updates(&stats_update_tx, observed_start, event_count).await;
    wait_for_sentinel_snapshot(&runtime_state).await;
    let elapsed = started_at.elapsed();

    collector.shutdown().await;
    elapsed
}

async fn ingest_endpoint_to_collector(events: Arc<Vec<Bytes>>) -> Duration {
    let (runtime_state, stats_update_tx, collector) = start_benchmark_collector();
    let (base_url, endpoint) = start_stats_endpoint(events.clone()).await;
    let mut stream_config = EngineStatsStreamConfig::new(
        &base_url,
        "/pylon/v1/stats/stream",
        EngineStatsStreamMode::Required,
    );
    stream_config.initial_reconnect_backoff = Duration::from_secs(60);
    stream_config.max_reconnect_backoff = Duration::from_secs(60);
    let stream = start_engine_stats_stream(stream_config, stats_update_tx)
        .expect("benchmark stats stream should start");

    let started_at = Instant::now();
    wait_for_sentinel_snapshot(&runtime_state).await;
    let elapsed = started_at.elapsed();

    stream.shutdown().await;
    collector.shutdown().await;
    endpoint.abort();
    let _ = endpoint.await;
    elapsed
}

fn start_benchmark_collector() -> (
    PylonRuntimeState,
    flume::Sender<StatsAggregatorUpdate>,
    StatsCollectorHandle,
) {
    let config = StatsCollectorConfig {
        observation_channel_capacity: 4_096,
        engine_stats_request_ttl: Duration::from_secs(300),
        engine_stats_model_ttl: Duration::from_secs(300),
        ..Default::default()
    };
    let (runtime_state, observation_rx) = PylonRuntimeState::observed(
        stargate_proto::pb::InferenceServerStatus::Unknown,
        &[],
        config.observation_channel_capacity,
        None,
    );
    let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&config);
    let collector = start_stats_collector_with_engine_stats(
        config,
        observation_rx,
        Some(stats_update_rx),
        runtime_state.clone(),
    );
    (runtime_state, stats_update_tx, collector)
}

async fn start_stats_endpoint(events: Arc<Vec<Bytes>>) -> (String, JoinHandle<()>) {
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("benchmark stats endpoint should bind");
    let addr = listener
        .local_addr()
        .expect("benchmark stats endpoint should have local addr");
    let app = Router::new()
        .route("/pylon/v1/stats/stream", get(stats_endpoint))
        .with_state(events);
    let handle = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("benchmark stats endpoint should serve");
    });
    (format!("http://{addr}"), handle)
}

async fn stats_endpoint(State(events): State<Arc<Vec<Bytes>>>) -> Response {
    let stream = stream::iter(0..events.len()).map(move |index| {
        let event = events[index].clone();
        Ok::<Bytes, Infallible>(event)
    });
    Response::builder()
        .header("content-type", "application/x-ndjson")
        .body(Body::from_stream(stream))
        .expect("benchmark stats endpoint response should build")
}

fn endpoint_event_lines(event_count: u64) -> Vec<Bytes> {
    let mut events = Vec::with_capacity(usize::try_from(event_count).unwrap_or_default() + 2);
    events.push(Bytes::from(stats_event_line_for(
        "req-sentinel",
        0,
        0,
        false,
    )));
    events.extend((0..event_count).map(|index| Bytes::from(stats_event_line(index))));
    events.push(Bytes::from(stats_event_line_for(
        "req-sentinel",
        0,
        SENTINEL_OUTPUT_TOKENS,
        true,
    )));
    events
}

fn stats_event_line(index: u64) -> String {
    let request_index = index % REQUEST_IDS;
    let step = index / REQUEST_IDS + 1;
    format!(
        "{{\"v\":1,\"type\":\"stats\",\"request_id\":\"req-{request_index}\",\"model\":\"model-a\",\"tokens_processed\":{},\"tokens_generated\":{}}}\n",
        step * 8,
        step
    )
}

fn stats_event_line_for(
    request_id: &str,
    tokens_processed: u64,
    tokens_generated: u64,
    finished: bool,
) -> String {
    format!(
        "{{\"v\":1,\"type\":\"stats\",\"request_id\":\"{request_id}\",\"model\":\"model-a\",\"tokens_processed\":{tokens_processed},\"tokens_generated\":{tokens_generated},\"finished\":{finished}}}\n",
    )
}

async fn send_sentinel_updates(
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    observed_start: TokioInstant,
    event_count: u64,
) {
    let sentinel_start = observed_start + Duration::from_millis(event_count);
    stats_update_tx
        .send_async(request_update("req-sentinel", 0, 0, false, sentinel_start))
        .await
        .expect("stats collector should receive sentinel start");
    stats_update_tx
        .send_async(request_update(
            "req-sentinel",
            0,
            SENTINEL_OUTPUT_TOKENS,
            true,
            sentinel_start + Duration::from_secs(1),
        ))
        .await
        .expect("stats collector should receive sentinel finish");
}

fn request_update(
    request_id: impl Into<String>,
    tokens_processed: u64,
    tokens_generated: u64,
    finished: bool,
    observed_at: TokioInstant,
) -> StatsAggregatorUpdate {
    StatsAggregatorUpdate::RequestCounters(RequestCounterUpdate::new(RequestCounterUpdateInput {
        source: StatsUpdateSource::EngineStatsStream,
        request_id: request_id.into(),
        model_id: "model-a".to_string(),
        tokens_processed: Some(tokens_processed),
        tokens_generated: Some(tokens_generated),
        finished,
        observed_at,
    }))
}

async fn wait_for_sentinel_snapshot(runtime_state: &PylonRuntimeState) {
    let receive_sentinel = async {
        let mut poll = tokio::time::interval(Duration::from_millis(1));
        loop {
            poll.tick().await;
            if runtime_state
                .model_stats("model-a")
                .is_some_and(|stats| stats.max_output_tps >= SENTINEL_OUTPUT_TPS)
            {
                break;
            }
        }
    };
    if running_in_criterion_test_mode() {
        tokio::time::timeout(TEST_SENTINEL_TIMEOUT, receive_sentinel)
            .await
            .expect("sentinel stats snapshot should be published in benchmark smoke mode");
    } else {
        receive_sentinel.await;
    }
}

criterion_group!(benches, bench_engine_stats_stream);
criterion_main!(benches);
