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
use clap::{ArgAction, Args};
use serde::{Deserialize, Serialize};

#[derive(Args, Debug, Clone, Copy, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct TransportBenchConfig {
    #[arg(long = "requests", default_value_t = 20_000, value_name = "N")]
    pub request_count: usize,
    #[arg(long, default_value_t = 256, value_name = "N")]
    pub concurrency: usize,
    #[arg(long, default_value_t = 1, value_name = "N")]
    pub quic_connections: usize,
    #[arg(long, default_value_t = 1_000, value_name = "N")]
    pub warmup_requests: usize,
    #[arg(long, default_value_t = 1024, value_name = "BYTES")]
    pub request_body_bytes: usize,
    #[arg(long, default_value_t = 1024, value_name = "BYTES")]
    pub response_body_bytes: usize,
    #[arg(long, default_value_t = 16 * 1024, value_name = "BYTES")]
    pub request_chunk_bytes: usize,
    #[arg(long, default_value_t = 16 * 1024, value_name = "BYTES")]
    pub response_chunk_bytes: usize,
    #[arg(long = "disable-quic-send-fairness", action = ArgAction::SetFalse)]
    pub quic_send_fairness: bool,
    #[arg(long = "disable-http3-grease", action = ArgAction::SetFalse)]
    pub http3_send_grease: bool,
    #[arg(long, default_value_t = 1, value_name = "N")]
    pub trials: usize,
    #[arg(long, default_value_t = 0, value_name = "N")]
    pub warmup_trials: usize,
    #[arg(long, default_value_t = 0, value_name = "MS")]
    pub cooldown_ms: u64,
    #[arg(long)]
    pub randomize_order: bool,
    #[arg(long, default_value_t = 0.02, value_name = "CV")]
    pub noise_threshold_cv: f64,
    #[arg(long, default_value_t = 1.0, value_name = "PERCENT")]
    pub min_effect_size_percent: f64,
}

impl TransportBenchConfig {
    pub(super) fn validate(&self) -> Result<()> {
        for (value, name) in [
            (self.request_count, "requests"),
            (self.concurrency, "concurrency"),
            (self.quic_connections, "quic-connections"),
            (self.trials, "trials"),
            (self.request_chunk_bytes, "request-chunk-bytes"),
            (self.response_chunk_bytes, "response-chunk-bytes"),
        ] {
            ensure!(value > 0, "{name} must be > 0");
        }
        for (value, name) in [
            (self.noise_threshold_cv, "noise-threshold-cv"),
            (self.min_effect_size_percent, "min-effect-size-percent"),
        ] {
            ensure!(
                value.is_finite() && value >= 0.0,
                "{name} must be finite and >= 0"
            );
        }
        Ok(())
    }
}
