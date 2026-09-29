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

//! Reconnect pacing and open-failure logging for the Stargate gRPC streams
//! Pylon keeps open: one registration stream per router and one discovery
//! watch per seed.

use std::error::Error;
use std::time::Duration;

use super::grpc_endpoint::is_stargate_grpc_certificate_failure;

/// Delay before the first reconnect to a router, and again after the router
/// acknowledges an update.
pub(super) const RECONNECT_INITIAL_BACKOFF: Duration = Duration::from_secs(1);
/// Each delay is spread uniformly over this fraction above and below its base.
pub(super) const RECONNECT_JITTER_FRACTION: f64 = 0.2;

/// Exponential reconnect delay for one registration stream. The base delay
/// starts at the initial delay, doubles after every attempt up to the cap, and
/// returns to the initial delay once the router acknowledges an update. Each
/// returned delay is the base with +/-20 percent jitter, never above the cap.
#[derive(Debug, Clone)]
pub(super) struct ReconnectBackoff {
    initial: Duration,
    max: Duration,
    next_base: Duration,
}

impl ReconnectBackoff {
    /// An initial delay above `max` is lowered to `max`.
    pub(super) fn new(initial: Duration, max: Duration) -> Self {
        let initial = initial.min(max);
        Self {
            initial,
            max,
            next_base: initial,
        }
    }

    /// Returns the jittered delay before the next attempt and doubles the base.
    pub(super) fn next_delay(&mut self) -> Duration {
        jittered_reconnect_delay(self.next_base_delay(), self.max, random_unit_interval())
    }

    /// Returns the base delay before the next attempt, without jitter, and
    /// doubles it up to the cap for the attempt after.
    pub(super) fn next_base_delay(&mut self) -> Duration {
        let base = self.next_base;
        self.next_base = base.saturating_mul(2).min(self.max);
        base
    }

    /// Restarts the schedule at the initial delay.
    pub(super) fn reset(&mut self) {
        self.next_base = self.initial;
    }
}

/// Scales `base` by a factor in `[0.8, 1.2]` chosen by `jitter_sample` in
/// `[0, 1]` (`0` is the shortest delay, `1` the longest; out-of-range samples
/// are clamped) and caps the result at `max`.
pub(super) fn jittered_reconnect_delay(
    base: Duration,
    max: Duration,
    jitter_sample: f64,
) -> Duration {
    let sample = if jitter_sample.is_finite() {
        jitter_sample.clamp(0.0, 1.0)
    } else {
        0.5
    };
    let factor = 1.0 - RECONNECT_JITTER_FRACTION + 2.0 * RECONNECT_JITTER_FRACTION * sample;
    base.mul_f64(factor).min(max)
}

/// A uniform sample in `[0, 1)`. The low 53 bits of a v4 UUID come from the
/// operating system generator and fill an `f64` mantissa exactly.
pub(super) fn random_unit_interval() -> f64 {
    const MANTISSA_BITS: u32 = 53;
    let bits = (uuid::Uuid::new_v4().as_u128() as u64) & ((1_u64 << MANTISSA_BITS) - 1);
    bits as f64 / (1_u64 << MANTISSA_BITS) as f64
}

/// Logs failures to open a Stargate gRPC stream. A failure logs at warn when
/// its message differs from the previous failure since a stream last opened,
/// and at debug while the same message repeats, so a misconfiguration is
/// visible once without flooding the log on every retry. Certificate failures
/// are skipped: `log_stargate_grpc_certificate_failure` already reports them.
#[derive(Debug)]
pub(super) struct StreamOpenFailureLog {
    operation: &'static str,
    last_logged: Option<String>,
}

impl StreamOpenFailureLog {
    pub(super) fn new(operation: &'static str) -> Self {
        Self {
            operation,
            last_logged: None,
        }
    }

    /// Re-arms warn logging once a stream opens.
    pub(super) fn record_opened(&mut self) {
        self.last_logged = None;
    }

    pub(super) fn record(
        &mut self,
        router: &str,
        error: &(dyn Error + 'static),
        retry_in: Duration,
    ) {
        if is_stargate_grpc_certificate_failure(error) {
            return;
        }
        let detail = open_failure_detail(error);
        let retry_in_ms = u64::try_from(retry_in.as_millis()).unwrap_or(u64::MAX);
        if self.last_logged.as_deref() == Some(detail.as_str()) {
            tracing::debug!(
                router,
                operation = self.operation,
                error = %detail,
                retry_in_ms,
                "failed to open stargate gRPC stream; retrying"
            );
            return;
        }
        tracing::warn!(
            router,
            operation = self.operation,
            error = %detail,
            retry_in_ms,
            "failed to open stargate gRPC stream; retrying"
        );
        self.last_logged = Some(detail);
    }
}

/// Joins the error chain with `: `. A gRPC status contributes its code and
/// message only; its `Display` includes the debug form of its source, which the
/// chain already covers.
pub(super) fn open_failure_detail(error: &(dyn Error + 'static)) -> String {
    let mut detail = String::new();
    let mut next = Some(error);
    while let Some(error) = next {
        if !detail.is_empty() {
            detail.push_str(": ");
        }
        match error.downcast_ref::<tonic::Status>() {
            Some(status) if status.message().is_empty() => {
                detail.push_str(&format!("{:?}", status.code()));
            }
            Some(status) => {
                detail.push_str(&format!("{:?}: {}", status.code(), status.message()));
            }
            None => detail.push_str(&error.to_string()),
        }
        next = error.source();
    }
    detail
}
