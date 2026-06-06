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

use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result};
use bytes::Bytes;
use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};

use super::summary::{summarize_aggregates, summarize_comparisons};
use super::{
    PayloadShape, RequestSample, TransportBenchConfig, TransportBenchmarkOutcome, TransportKind,
    TransportRunOutcome, custom, http3, webtransport,
};

const TRANSPORT_ORDER_SEED: u64 = 0x051A_76A7_E135;

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
        .checked_mul(3)
        .context("transport trial count overflows run capacity")?;
    let mut runs = Vec::with_capacity(run_capacity);
    for trial_index in 0..trial_count {
        for transport in transport_order(config, trial_index, warmup) {
            let run = run_transport(transport, trial_index + 1, config, shape.clone()).await?;
            runs.push(run);
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
    let mut order = [
        TransportKind::CustomProtocol,
        TransportKind::Http3H3Quinn,
        TransportKind::WebTransportH3Quinn,
    ];
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

async fn run_transport(
    transport: TransportKind,
    trial_index: usize,
    config: TransportBenchConfig,
    shape: PayloadShape,
) -> Result<TransportRunOutcome> {
    match transport {
        TransportKind::CustomProtocol => {
            custom::run_custom_protocol(config, shape, trial_index).await
        }
        TransportKind::Http3H3Quinn => http3::run_http3_h3_quinn(config, shape, trial_index).await,
        TransportKind::WebTransportH3Quinn => {
            webtransport::run_webtransport_h3_quinn(config, shape, trial_index).await
        }
    }
}

pub(super) async fn collect_samples(
    tasks: Vec<tokio::task::JoinHandle<RequestSample>>,
) -> Result<Vec<RequestSample>> {
    let mut samples = Vec::with_capacity(tasks.len());
    for task in tasks {
        samples.push(task.await.context("transport request task panicked")?);
    }
    samples.sort_by_key(|sample| sample.request_index);
    Ok(samples)
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
