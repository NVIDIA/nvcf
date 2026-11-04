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

use std::hint::black_box;
use std::time::Instant;

use anyhow::{Result, ensure};
use bytes::{Buf, Bytes};
use clap::Args;

const MAX_SPECULATIVE_BODY_PREALLOC_BYTES: usize = 64 * 1024;

#[derive(Args, Debug, Clone)]
pub(crate) struct BodyBufferMicrobenchConfig {
    #[arg(long, default_value_t = 20_000, value_name = "N")]
    pub iterations: usize,
    #[arg(long, default_value_t = 2_000, value_name = "N")]
    pub warmup_iterations: usize,
    #[arg(long, default_value_t = 65_536, value_name = "BYTES")]
    pub body_bytes: usize,
    #[arg(long, default_value_t = 1_024, value_name = "BYTES")]
    pub chunk_bytes: usize,
}

pub(crate) fn run_body_buffer_microbench(
    config: BodyBufferMicrobenchConfig,
) -> Result<BodyBufferMicrobenchOutcome> {
    config.validate()?;

    let chunks = body_chunks(config.body_bytes, config.chunk_bytes);
    let measurements = BODY_BUFFER_SCENARIOS.map(|scenario| {
        warm_up(
            &chunks,
            config.body_bytes,
            config.warmup_iterations,
            scenario,
        );
        BodyBufferMeasurement {
            baseline_ns_per_body: measure(
                &chunks,
                config.body_bytes,
                config.iterations,
                scenario.baseline,
            ),
            optimized_ns_per_body: measure(
                &chunks,
                config.body_bytes,
                config.iterations,
                scenario.optimized,
            ),
        }
    });

    Ok(BodyBufferMicrobenchOutcome {
        body_bytes: config.body_bytes,
        chunk_count: chunks.len(),
        measurements,
    })
}

pub(crate) fn render_body_buffer_microbench_report(
    outcome: &BodyBufferMicrobenchOutcome,
) -> String {
    let mut report = String::from(concat!(
        "# Body Buffer Microbench\n\n",
        "| Scenario | Body Bytes | Chunks | Baseline ns/body | Optimized ns/body | Improvement |\n",
        "| --- | ---: | ---: | ---: | ---: | ---: |\n",
    ));
    for (scenario, measurement) in BODY_BUFFER_SCENARIOS.iter().zip(&outcome.measurements) {
        report.push_str(&format!(
            "| {} | {} | {} | {:.2} | {:.2} | {:.2}% |\n",
            scenario.label,
            outcome.body_bytes,
            outcome.chunk_count,
            measurement.baseline_ns_per_body,
            measurement.optimized_ns_per_body,
            measurement.improvement_percent()
        ));
    }
    report
}

#[derive(Debug)]
pub(crate) struct BodyBufferMicrobenchOutcome {
    body_bytes: usize,
    chunk_count: usize,
    measurements: [BodyBufferMeasurement; BODY_BUFFER_SCENARIOS.len()],
}

#[derive(Debug)]
struct BodyBufferMeasurement {
    baseline_ns_per_body: f64,
    optimized_ns_per_body: f64,
}

impl BodyBufferMeasurement {
    fn improvement_percent(&self) -> f64 {
        if self.baseline_ns_per_body == 0.0 {
            return 0.0;
        }
        ((self.baseline_ns_per_body - self.optimized_ns_per_body) / self.baseline_ns_per_body)
            * 100.0
    }
}

type BodyBufferFn = fn(&[Bytes], usize) -> usize;

impl BodyBufferMicrobenchConfig {
    fn validate(&self) -> Result<()> {
        ensure!(self.iterations > 0, "iterations must be > 0");
        ensure!(self.body_bytes > 0, "body-bytes must be > 0");
        ensure!(self.chunk_bytes > 0, "chunk-bytes must be > 0");
        ensure!(
            self.iterations.checked_mul(self.body_bytes).is_some(),
            "iterations * body_bytes is too large"
        );
        Ok(())
    }
}

#[derive(Clone, Copy)]
struct BodyBufferScenario {
    label: &'static str,
    baseline: BodyBufferFn,
    optimized: BodyBufferFn,
}

#[rustfmt::skip]
const BODY_BUFFER_SCENARIOS: [BodyBufferScenario; 2] = [
    BodyBufferScenario { label: "bytes-extend-prealloc", baseline: collect_bytes_no_prealloc, optimized: collect_bytes_preallocated },
    BodyBufferScenario { label: "h3-buf-copy-prealloc", baseline: collect_h3_copy_to_bytes_no_prealloc, optimized: collect_h3_buf_chunk_preallocated },
];

fn warm_up(chunks: &[Bytes], body_bytes: usize, iterations: usize, scenario: BodyBufferScenario) {
    for _ in 0..=iterations {
        black_box((scenario.baseline)(chunks, body_bytes));
        black_box((scenario.optimized)(chunks, body_bytes));
    }
}

fn measure(chunks: &[Bytes], body_bytes: usize, iterations: usize, buffer: BodyBufferFn) -> f64 {
    let started_at = Instant::now();
    let mut checksum = 0usize;
    for _ in 0..iterations {
        checksum ^= buffer(chunks, body_bytes);
    }
    black_box(checksum);
    started_at.elapsed().as_nanos() as f64 / iterations as f64
}

fn body_chunks(total_bytes: usize, chunk_bytes: usize) -> Vec<Bytes> {
    let mut chunks = Vec::new();
    let mut remaining = total_bytes;
    while remaining > 0 {
        let len = remaining.min(chunk_bytes);
        chunks.push(Bytes::from(vec![b'r'; len]));
        remaining -= len;
    }
    chunks
}

fn collect_bytes_no_prealloc(chunks: &[Bytes], _body_bytes: usize) -> usize {
    let mut body = Vec::new();
    for chunk in chunks {
        body.extend_from_slice(chunk);
    }
    black_box(body.as_slice());
    body.len()
}

fn collect_bytes_preallocated(chunks: &[Bytes], body_bytes: usize) -> usize {
    let mut body = Vec::with_capacity(prealloc_capacity(body_bytes));
    for chunk in chunks {
        body.extend_from_slice(chunk);
    }
    black_box(body.as_slice());
    body.len()
}

fn collect_h3_copy_to_bytes_no_prealloc(chunks: &[Bytes], _body_bytes: usize) -> usize {
    let mut body = Vec::new();
    for chunk in chunks {
        let mut chunk = chunk.clone();
        while chunk.has_remaining() {
            let len = chunk.remaining();
            body.extend_from_slice(&chunk.copy_to_bytes(len));
        }
    }
    black_box(body.as_slice());
    body.len()
}

fn collect_h3_buf_chunk_preallocated(chunks: &[Bytes], body_bytes: usize) -> usize {
    let mut body = Vec::with_capacity(prealloc_capacity(body_bytes));
    for chunk in chunks {
        let mut chunk = chunk.clone();
        while chunk.has_remaining() {
            let bytes = chunk.chunk();
            body.extend_from_slice(bytes);
            chunk.advance(bytes.len());
        }
    }
    black_box(body.as_slice());
    body.len()
}

fn prealloc_capacity(body_bytes: usize) -> usize {
    body_bytes.min(MAX_SPECULATIVE_BODY_PREALLOC_BYTES)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn report_includes_every_body_buffer_scenario() -> Result<()> {
        let report = render_body_buffer_microbench_report(&run_body_buffer_microbench(
            BodyBufferMicrobenchConfig {
                iterations: 1,
                warmup_iterations: 0,
                body_bytes: 1024,
                chunk_bytes: 128,
            },
        )?);

        for scenario in BODY_BUFFER_SCENARIOS {
            assert!(report.contains(scenario.label));
        }
        Ok(())
    }

    #[test]
    fn optimized_body_buffers_match_baseline_lengths() {
        let chunks = body_chunks(4097, 333);

        for scenario in BODY_BUFFER_SCENARIOS {
            assert_eq!(
                (scenario.baseline)(&chunks, 4097),
                (scenario.optimized)(&chunks, 4097),
                "scenario={}",
                scenario.label
            );
        }
    }

    #[test]
    fn body_buffer_microbench_rejects_zero_chunk_size() {
        let error = run_body_buffer_microbench(BodyBufferMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            body_bytes: 1024,
            chunk_bytes: 0,
        })
        .unwrap_err();

        assert!(error.to_string().contains("chunk-bytes must be > 0"));
    }
}
