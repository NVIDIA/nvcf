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

//! Shared test helpers.

pub mod fake_gds;

use std::time::Duration;

use gds_client::{Backoff, ConnectPolicy};

/// A policy with short timeouts and backoff for tests against local
/// servers.
pub fn fast_policy() -> ConnectPolicy {
    ConnectPolicy {
        connect_timeout: Duration::from_millis(500),
        rpc_timeout: Duration::from_secs(2),
        primary_retry_interval: Duration::from_millis(300),
        allow_insecure_http: true,
        backoff: Backoff {
            initial: Duration::from_millis(10),
            max: Duration::from_millis(100),
            multiplier: 2.0,
        },
        max_attempts: 4,
        ..ConnectPolicy::default()
    }
}

/// Polls `condition` every 10 ms until it holds or `timeout` passes. Under a
/// paused clock the sleeps advance virtual time.
pub async fn wait_until(timeout: Duration, mut condition: impl FnMut() -> bool) -> bool {
    let deadline = tokio::time::Instant::now() + timeout;
    loop {
        if condition() {
            return true;
        }
        if tokio::time::Instant::now() >= deadline {
            return false;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}
