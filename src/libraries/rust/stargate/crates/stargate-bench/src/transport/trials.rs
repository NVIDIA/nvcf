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

use std::future::Future;
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::{Context, Result};
use bytes::Bytes;
use futures::future;
use http::{HeaderMap, HeaderValue};
use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};
use tokio::sync::Semaphore;

use super::summary::{summarize_aggregates, summarize_comparisons, summarize_samples};
use super::{
    PayloadShape, RequestSample, TransportBenchConfig, TransportBenchmarkOutcome, TransportKind,
    TransportRunOutcome, http3, raw_quic, webtransport,
};

const TRANSPORT_ORDER_SEED: u64 = 0x051A_76A7_E135;
const TRANSPORTS: [TransportKind; 3] = [
    TransportKind::RawQuic,
    TransportKind::Http3H3Quinn,
    TransportKind::WebTransportH3Quinn,
];

#[derive(Default)]
pub(super) struct ResponseMeasurement {
    status: Option<u16>,
    headers_us: u64,
    first_body_us: Option<u64>,
    body_bytes: usize,
}

impl ResponseMeasurement {
    pub(super) fn new(status: Option<u16>, headers_us: u64) -> Self {
        Self {
            status,
            headers_us,
            ..Self::default()
        }
    }

    pub(super) fn record_body(&mut self, started_at: Instant, bytes: usize) {
        self.first_body_us
            .get_or_insert_with(|| duration_us(started_at.elapsed()));
        self.body_bytes += bytes;
    }
}

pub async fn run_transport_benchmark(
    config: TransportBenchConfig,
) -> Result<TransportBenchmarkOutcome> {
    config.validate()?;
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

    let shape = PayloadShape {
        request_chunks: Arc::new(chunks(
            config.request_body_bytes,
            config.request_chunk_bytes,
            b'r',
        )),
        response_chunks: Arc::new(chunks(
            config.response_body_bytes,
            config.response_chunk_bytes,
            b's',
        )),
        request_bytes: config.request_body_bytes,
        response_bytes: config.response_body_bytes,
    };

    let warmup_runs = run_transport_trials(&shape, config, true).await?;
    let runs = run_transport_trials(&shape, config, false).await?;
    let aggregates = summarize_aggregates(&runs, config.noise_threshold_cv);
    let comparisons = summarize_comparisons(&aggregates, config.min_effect_size_percent);

    Ok(TransportBenchmarkOutcome {
        config,
        runs,
        warmup_runs,
        aggregates,
        comparisons,
    })
}

async fn run_transport_trials(
    shape: &PayloadShape,
    config: TransportBenchConfig,
    warmup: bool,
) -> Result<Vec<TransportRunOutcome>> {
    let trial_count = if warmup {
        config.warmup_trials
    } else {
        config.trials
    };
    let run_capacity = trial_count
        .checked_mul(TRANSPORTS.len())
        .context("transport trial count overflows run capacity")?;
    let mut runs = Vec::with_capacity(run_capacity);
    for trial_index in 0..trial_count {
        for transport in transport_order(config, trial_index, warmup) {
            let run = match transport {
                TransportKind::RawQuic => {
                    raw_quic::run_raw_quic(config, shape.clone(), trial_index + 1).await
                }
                TransportKind::Http3H3Quinn => {
                    http3::run_http3_h3_quinn(config, shape.clone(), trial_index + 1).await
                }
                TransportKind::WebTransportH3Quinn => {
                    webtransport::run_webtransport_h3_quinn(config, shape.clone(), trial_index + 1)
                        .await
                }
            };
            runs.push(run?);
            if config.cooldown_ms > 0 {
                tokio::time::sleep(Duration::from_millis(config.cooldown_ms)).await;
            }
        }
    }
    Ok(runs)
}

fn transport_order(
    config: TransportBenchConfig,
    trial_index: usize,
    warmup: bool,
) -> [TransportKind; 3] {
    let mut order = TRANSPORTS;
    if config.randomize_order {
        let warmup_salt = if warmup { 0x000A_11CE_u64 } else { 0 };
        let mut rng =
            StdRng::seed_from_u64(TRANSPORT_ORDER_SEED ^ trial_index as u64 ^ warmup_salt);
        let first = rng.random_range(0..order.len());
        order.swap(0, first);
        let second = rng.random_range(1..order.len());
        order.swap(1, second);
    }
    order
}

pub(super) async fn benchmark_requests<F, R, Fut>(
    config: TransportBenchConfig,
    transport: TransportKind,
    trial_index: usize,
    shape: PayloadShape,
    request: F,
) -> Result<TransportRunOutcome>
where
    F: Fn(usize, usize, PayloadShape) -> R + Clone,
    R: FnOnce(Instant) -> Fut + Send + 'static,
    Fut: Future<Output = Result<ResponseMeasurement>> + Send + 'static,
{
    let drive_requests = |request_count, shape: PayloadShape, request: F| async move {
        let request_bytes = shape.request_bytes;
        let expected_response_bytes = shape.response_bytes;
        let semaphore = Arc::new(Semaphore::new(config.concurrency));
        let tasks = (0..request_count).map(|request_index| {
            let connection_index = request_index % config.quic_connections;
            let semaphore = semaphore.clone();
            let request = request.clone();
            let shape = shape.clone();
            let request = request(request_index, connection_index, shape);
            tokio::spawn(async move {
                let _permit = semaphore
                    .acquire_owned()
                    .await
                    .expect("semaphore should remain open");
                let started_at = Instant::now();
                let response = request(started_at).await;
                let (response, error) = match response {
                    Ok(response) => (response, None),
                    Err(error) => (ResponseMeasurement::default(), Some(error)),
                };
                let ok = error.is_none()
                    && response.status == Some(200)
                    && response.body_bytes == expected_response_bytes;
                let completion_us = duration_us(started_at.elapsed());
                RequestSample {
                    request_index,
                    connection_index,
                    ok,
                    response_status: response.status,
                    request_bytes,
                    response_bytes: response.body_bytes,
                    response_headers_us: error.is_none().then_some(response.headers_us),
                    first_body_us: response.first_body_us,
                    completion_us,
                    error: error.map(|error| error.to_string()),
                }
            })
        });
        future::try_join_all(tasks)
            .await
            .context("transport request task panicked")
    };

    if config.warmup_requests > 0 {
        drive_requests(config.warmup_requests, shape.clone(), request.clone()).await?;
    }

    let started_at = Instant::now();
    let samples = drive_requests(config.request_count, shape, request).await?;
    let summary = summarize_samples(transport, &samples, started_at.elapsed());
    Ok(TransportRunOutcome {
        transport,
        trial_index,
        summary,
        samples,
    })
}

pub(super) fn chunks(total_bytes: usize, chunk_bytes: usize, byte: u8) -> Vec<Bytes> {
    let mut chunks = Vec::new();
    let mut remaining = total_bytes;
    while remaining > 0 {
        let len = remaining.min(chunk_bytes);
        chunks.push(Bytes::from(vec![byte; len]));
        remaining -= len;
    }
    chunks
}

pub(super) fn duration_us(duration: Duration) -> u64 {
    duration.as_micros().try_into().unwrap_or(u64::MAX)
}

pub(super) fn request_headers(request_index: usize) -> Result<HeaderMap> {
    let mut headers = HeaderMap::with_capacity(3);
    headers.insert(
        "x-request-id",
        HeaderValue::from_str(&format!("transport-bench-{request_index}"))
            .context("build request id")?,
    );
    headers.insert("x-model", HeaderValue::from_static("transport-bench-model"));
    headers.insert("x-input-tokens", HeaderValue::from_static("1"));
    Ok(headers)
}
