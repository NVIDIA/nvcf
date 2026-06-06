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

use anyhow::{Result, ensure};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct TransportBenchConfig {
    pub request_count: usize,
    pub concurrency: usize,
    pub quic_connections: usize,
    pub warmup_requests: usize,
    pub request_body_bytes: usize,
    pub response_body_bytes: usize,
    pub request_chunk_bytes: usize,
    pub response_chunk_bytes: usize,
    pub quic_send_fairness: bool,
    pub http3_send_grease: bool,
    pub trials: usize,
    pub warmup_trials: usize,
    pub cooldown_ms: u64,
    pub randomize_order: bool,
    pub noise_threshold_cv: f64,
    pub min_effect_size_percent: f64,
}

impl TransportBenchConfig {
    pub(super) fn validate(&self) -> Result<()> {
        ensure!(self.request_count > 0, "requests must be > 0");
        ensure!(self.concurrency > 0, "concurrency must be > 0");
        ensure!(self.quic_connections > 0, "quic-connections must be > 0");
        ensure!(self.trials > 0, "trials must be > 0");
        ensure!(
            self.request_chunk_bytes > 0,
            "request-chunk-bytes must be > 0"
        );
        ensure!(
            self.response_chunk_bytes > 0,
            "response-chunk-bytes must be > 0"
        );
        ensure!(
            self.noise_threshold_cv.is_finite() && self.noise_threshold_cv >= 0.0,
            "noise-threshold-cv must be finite and >= 0"
        );
        ensure!(
            self.min_effect_size_percent.is_finite() && self.min_effect_size_percent >= 0.0,
            "min-effect-size-percent must be finite and >= 0"
        );
        Ok(())
    }
}
