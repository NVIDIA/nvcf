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
use std::time::{Duration, Instant};

use anyhow::{Result, ensure};
use bytes::{Buf, Bytes};

const MAX_SPECULATIVE_BODY_PREALLOC_BYTES: usize = 64 * 1024;

#[derive(Debug, Clone)]
pub(crate) struct BodyBufferMicrobenchConfig {
    pub iterations: usize,
    pub warmup_iterations: usize,
    pub body_bytes: usize,
    pub chunk_bytes: usize,
}

pub(crate) fn run_body_buffer_microbench(
    config: BodyBufferMicrobenchConfig,
) -> Result<BodyBufferMicrobenchOutcome> {
    config.validate()?;

    let chunks = body_chunks(config.body_bytes, config.chunk_bytes);
    let mut rows = Vec::new();
    for scenario in BodyBufferScenario::ALL {
        warm_up(
            &chunks,
            config.body_bytes,
            config.warmup_iterations,
            scenario,
        );
        let baseline = measure(
            &chunks,
            config.body_bytes,
            config.iterations,
            scenario.baseline(),
        )?;
        let optimized = measure(
            &chunks,
            config.body_bytes,
            config.iterations,
            scenario.optimized(),
        )?;
        rows.push(BodyBufferMicrobenchRow {
            scenario,
            baseline,
            optimized,
        });
    }

    Ok(BodyBufferMicrobenchOutcome { rows })
}

pub(crate) fn render_body_buffer_microbench_report(
    outcome: &BodyBufferMicrobenchOutcome,
) -> String {
    let mut report = String::new();
    report.push_str("# Body Buffer Microbench\n\n");
    report.push_str(
        "| Scenario | Body Bytes | Chunks | Baseline ns/body | Optimized ns/body | Improvement |\n",
    );
    report.push_str("| --- | ---: | ---: | ---: | ---: | ---: |\n");
    for row in &outcome.rows {
        report.push_str(&format!(
            "| {} | {} | {} | {:.2} | {:.2} | {:.2}% |\n",
            row.scenario.label(),
            row.baseline.body_bytes,
            row.baseline.chunk_count,
            row.baseline.ns_per_body,
            row.optimized.ns_per_body,
            row.improvement_percent()
        ));
    }
    report
}

#[derive(Debug)]
pub(crate) struct BodyBufferMicrobenchOutcome {
    rows: Vec<BodyBufferMicrobenchRow>,
}

#[derive(Debug)]
struct BodyBufferMicrobenchRow {
    scenario: BodyBufferScenario,
    baseline: BodyBufferMeasurement,
    optimized: BodyBufferMeasurement,
}

impl BodyBufferMicrobenchRow {
    fn improvement_percent(&self) -> f64 {
        if self.baseline.ns_per_body == 0.0 {
            return 0.0;
        }
        ((self.baseline.ns_per_body - self.optimized.ns_per_body) / self.baseline.ns_per_body)
            * 100.0
    }
}

#[derive(Debug)]
struct BodyBufferMeasurement {
    body_bytes: usize,
    chunk_count: usize,
    ns_per_body: f64,
}

type BodyBufferFn = fn(&[Bytes], usize) -> usize;

impl BodyBufferMicrobenchConfig {
    fn validate(&self) -> Result<()> {
        ensure!(self.iterations > 0, "iterations must be > 0");
        ensure!(self.body_bytes > 0, "body-bytes must be > 0");
        ensure!(self.chunk_bytes > 0, "chunk-bytes must be > 0");
        self.iterations
            .checked_mul(self.body_bytes)
            .ok_or_else(|| anyhow::anyhow!("iterations * body_bytes is too large"))?;
        Ok(())
    }
}

#[derive(Clone, Copy, Debug)]
enum BodyBufferScenario {
    BytesExtend,
    H3BufExtend,
}

impl BodyBufferScenario {
    const ALL: [Self; 2] = [Self::BytesExtend, Self::H3BufExtend];

    fn label(self) -> &'static str {
        match self {
            Self::BytesExtend => "bytes-extend-prealloc",
            Self::H3BufExtend => "h3-buf-copy-prealloc",
        }
    }

    fn baseline(self) -> BodyBufferFn {
        match self {
            Self::BytesExtend => collect_bytes_no_prealloc,
            Self::H3BufExtend => collect_h3_copy_to_bytes_no_prealloc,
        }
    }

    fn optimized(self) -> BodyBufferFn {
        match self {
            Self::BytesExtend => collect_bytes_preallocated,
            Self::H3BufExtend => collect_h3_buf_chunk_preallocated,
        }
    }
}

fn warm_up(chunks: &[Bytes], body_bytes: usize, iterations: usize, scenario: BodyBufferScenario) {
    black_box((scenario.baseline())(chunks, body_bytes));
    black_box((scenario.optimized())(chunks, body_bytes));
    for _ in 0..iterations {
        black_box((scenario.baseline())(chunks, body_bytes));
        black_box((scenario.optimized())(chunks, body_bytes));
    }
}

fn measure(
    chunks: &[Bytes],
    body_bytes: usize,
    iterations: usize,
    buffer: BodyBufferFn,
) -> Result<BodyBufferMeasurement> {
    let started_at = Instant::now();
    let mut checksum = 0usize;
    for _ in 0..iterations {
        checksum ^= buffer(chunks, body_bytes);
    }
    black_box(checksum);
    Ok(BodyBufferMeasurement {
        body_bytes,
        chunk_count: chunks.len(),
        ns_per_body: ns_per_body(started_at.elapsed(), iterations),
    })
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

fn ns_per_body(elapsed: Duration, iterations: usize) -> f64 {
    elapsed.as_nanos() as f64 / iterations as f64
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn report_includes_every_body_buffer_scenario() -> Result<()> {
        let outcome = run_body_buffer_microbench(BodyBufferMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            body_bytes: 1024,
            chunk_bytes: 128,
        })?;

        let report = render_body_buffer_microbench_report(&outcome);

        for scenario in BodyBufferScenario::ALL {
            assert!(report.contains(scenario.label()));
        }
        Ok(())
    }

    #[test]
    fn optimized_body_buffers_match_baseline_lengths() {
        let chunks = body_chunks(4097, 333);

        for scenario in BodyBufferScenario::ALL {
            assert_eq!(
                (scenario.baseline())(&chunks, 4097),
                (scenario.optimized())(&chunks, 4097),
                "scenario={}",
                scenario.label()
            );
        }
    }

    #[test]
    fn body_buffer_microbench_rejects_zero_chunk_size() {
        let Err(error) = run_body_buffer_microbench(BodyBufferMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            body_bytes: 1024,
            chunk_bytes: 0,
        }) else {
            panic!("zero chunk bytes should fail");
        };

        assert!(error.to_string().contains("chunk-bytes must be > 0"));
    }
}
