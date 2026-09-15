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

pub mod endpoints;
pub mod grpc;
pub mod health;
pub mod metrics;
pub mod quic;
mod tls;
pub mod watcher;
pub mod webtransport;
mod webtransport_network;

#[cfg(test)]
pub(crate) mod perf_tests {
    pub fn assert_twenty_percent_faster(bench_name: &str, baseline_ns_per_op: f64, ns_per_op: f64) {
        let target_ns_per_op = baseline_ns_per_op * 0.8;
        assert!(
            ns_per_op <= target_ns_per_op,
            "{bench_name} regressed past the 20% improvement target: baseline={baseline_ns_per_op:.2} ns/op target<={target_ns_per_op:.2} ns/op observed={ns_per_op:.2} ns/op"
        );
    }
}
