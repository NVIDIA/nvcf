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

use crate::config::BenchmarkConfig;

#[derive(Debug, Clone, PartialEq)]
pub(crate) struct BackendRuntimeSpec {
    pub(crate) upstream_index: usize,
    pub(crate) name: String,
    pub(crate) profile_slug: String,
    pub(crate) per_token_delay_ms: u64,
    pub(crate) decode_jitter_ms: u64,
    pub(crate) ttft_ms: u64,
    pub(crate) ttft_jitter_ms: u64,
    pub(crate) prefill_tokens_per_s: f64,
    pub(crate) max_concurrent_requests: usize,
    pub(crate) kv_cache_capacity_tokens: u64,
}

impl BackendRuntimeSpec {
    pub(crate) fn for_upstream(config: &BenchmarkConfig, upstream_index: usize) -> Self {
        let profile = config.backends.profile_for_index(upstream_index);
        let pylon_count = config.backends.pylon_count_for_upstream(upstream_index);
        let max_concurrent_requests = profile
            .max_concurrent_requests
            .unwrap_or_default()
            .checked_mul(pylon_count)
            .expect("validated shared backend concurrency should fit usize");
        let kv_cache_capacity_tokens = profile
            .kv_cache_capacity_tokens
            .checked_mul(pylon_count as u64)
            .expect("validated shared backend KV capacity should fit u64");
        // The mock backend delay is millisecond-granular, so rates above 1000 TPS floor at 1 ms.
        let per_token_delay_ms = (1000 / profile.service_time_ms.decode_tokens_per_s).max(1);

        Self {
            upstream_index,
            name: backend_name(upstream_index),
            profile_slug: slugify(&profile.name),
            per_token_delay_ms,
            decode_jitter_ms: profile.service_time_ms.decode_jitter_ms,
            ttft_ms: profile.service_time_ms.ttft_mean,
            ttft_jitter_ms: profile.service_time_ms.ttft_jitter_ms,
            prefill_tokens_per_s: profile.service_time_ms.prefill_tokens_per_s.unwrap_or(0.0),
            max_concurrent_requests,
            kv_cache_capacity_tokens,
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
pub(crate) struct PylonRuntimeSpec {
    pub(crate) backend_index: usize,
    pub(crate) upstream_index: usize,
    pub(crate) upstream_backend_name: String,
    pub(crate) inference_server_id: String,
    pub(crate) cluster_id: Option<String>,
    pub(crate) profile_slug: String,
    pub(crate) last_mean_input_tps: f64,
}

impl PylonRuntimeSpec {
    pub(crate) fn for_backend(config: &BenchmarkConfig, backend_index: usize) -> Self {
        let profile = config.backends.profile_for_index(backend_index);
        let upstream_index = config.backends.upstream_index_for_index(backend_index);
        Self {
            backend_index,
            upstream_index,
            upstream_backend_name: backend_name(upstream_index),
            inference_server_id: backend_name(backend_index),
            cluster_id: config.backends.cluster_id_for_index(backend_index),
            profile_slug: slugify(&profile.name),
            last_mean_input_tps: profile.registration.last_mean_input_tps,
        }
    }

    pub(crate) fn owns_upstream_backend(&self) -> bool {
        self.backend_index == self.upstream_index
    }
}

pub(crate) fn slugify(value: &str) -> String {
    let slug: String = value
        .chars()
        .map(|ch| {
            if ch.is_ascii_alphanumeric() {
                ch.to_ascii_lowercase()
            } else {
                '-'
            }
        })
        .collect();
    slug.trim_matches('-').to_string()
}

fn backend_name(index: usize) -> String {
    format!("backend-{index}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn config() -> BenchmarkConfig {
        serde_json::from_value(json!({
            "name": "runtime",
            "model": "dummy-model",
            "seed": 42,
            "request_count": 5,
            "max_concurrency": 2,
            "backends": {
                "count": 2,
                "cluster_id_template": "cluster-{cluster_index}",
                "pylons_per_cluster": 2,
                "profile": {
                    "name": "Fast GPU",
                    "max_concurrent_requests": 3,
                    "kv_cache_capacity_tokens": 11,
                    "service_time_ms": {
                        "ttft_mean": 150,
                        "ttft_jitter_ms": 10,
                        "decode_tokens_per_s": 50,
                        "decode_jitter_ms": 2,
                        "prefill_tokens_per_s": 123.0
                    },
                    "registration": { "last_mean_input_tps": 100.0 }
                }
            },
            "traffic_pattern": {
                "kind": "uniform",
                "routing_keys": 2,
                "cache_affinity_keys": 2,
                "input_tokens": { "distribution": "constant", "value": 100 },
                "output_tokens": { "distribution": "constant", "value": 20 },
                "arrival": { "distribution": "constant", "interval_ms": 10 }
            }
        }))
        .expect("runtime test config should parse")
    }

    #[test]
    fn backend_runtime_spec_scales_shared_upstream_capacity_once() {
        let config = config();

        let spec = BackendRuntimeSpec::for_upstream(&config, 0);

        assert_eq!(spec.name, "backend-0");
        assert_eq!(spec.profile_slug, "fast-gpu");
        assert_eq!(spec.per_token_delay_ms, 20);
        assert_eq!(spec.max_concurrent_requests, 6);
        assert_eq!(spec.kv_cache_capacity_tokens, 22);
    }

    #[test]
    fn pylon_runtime_spec_targets_shared_upstream_and_keeps_registration_identity() {
        let config = config();

        let spec = PylonRuntimeSpec::for_backend(&config, 1);

        assert_eq!(spec.backend_index, 1);
        assert_eq!(spec.upstream_index, 0);
        assert_eq!(spec.upstream_backend_name, "backend-0");
        assert_eq!(spec.inference_server_id, "backend-1");
        assert_eq!(spec.cluster_id.as_deref(), Some("cluster-0"));
        assert!(!spec.owns_upstream_backend());
    }

    #[test]
    fn slugify_trims_and_lowercases_runtime_names() {
        assert_eq!(slugify("  Fancy/Profile 01 "), "fancy-profile-01");
    }
}
