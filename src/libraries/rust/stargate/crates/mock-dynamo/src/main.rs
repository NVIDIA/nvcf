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

mod kv_cache;
mod openai;
mod stats_stream;
// mock-dynamo is an integration fixture, so its runtime image intentionally
// exposes deterministic controls used by deployed behavior tests.
mod test_control;
mod timing;

use std::sync::Arc;
use std::time::Duration;

use anyhow::Result;
use axum::Router;
use axum::routing::{get, post, put};
use clap::Parser;
use tokio::net::TcpListener;
use tokio::sync::{Mutex, Semaphore, broadcast};
use tracing::info;
use tracing_subscriber::EnvFilter;

#[derive(clap::Parser, Debug)]
#[command(name = "mock-dynamo")]
struct Args {
    /// Use a named inference behavior profile
    #[arg(
        long,
        value_enum,
        value_name = "PROFILE",
        conflicts_with_all = [
            "num_tokens",
            "context_length_tokens",
            "token_delay_ms",
            "decode_jitter_ms",
            "output_tps_min",
            "output_tps_max",
            "ttft_ms",
            "ttft_jitter_ms",
            "prefill_tokens_per_s",
            "max_concurrent_requests",
            "kv_cache_capacity_tokens"
        ]
    )]
    profile: Option<Profile>,
    /// Print a profile's behavior and expected rates, then exit
    #[arg(long, value_enum, value_name = "PROFILE", exclusive = true)]
    explain_profile: Option<Profile>,
    /// HTTP listen address for the mock inference server
    #[arg(long, default_value = "127.0.0.1:8090", value_name = "ADDR")]
    http_listen_addr: String,
    /// Model name served by this server
    #[arg(long, default_value = "dummy-model", value_name = "MODEL")]
    model_name: String,
    /// Fixed output token count when no output-token distribution is configured
    #[arg(
        long,
        value_name = "N",
        conflicts_with_all = [
            "profile",
            "output_tokens_min",
            "output_tokens_max",
            "output_token_distribution"
        ]
    )]
    num_tokens: Option<usize>,
    /// Minimum selected output token count
    #[arg(
        long,
        value_name = "TOKENS",
        requires_all = ["output_tokens_max", "output_token_distribution"],
        conflicts_with = "num_tokens"
    )]
    output_tokens_min: Option<usize>,
    /// Maximum selected output token count
    #[arg(
        long,
        value_name = "TOKENS",
        requires_all = ["output_tokens_min", "output_token_distribution"],
        conflicts_with = "num_tokens"
    )]
    output_tokens_max: Option<usize>,
    /// Distribution used to select each request's output token count
    #[arg(
        long,
        value_enum,
        value_name = "SCHEME",
        requires_all = ["output_tokens_min", "output_tokens_max"],
        conflicts_with = "num_tokens"
    )]
    output_token_distribution: Option<OutputTokenDistribution>,
    /// Maximum combined input and output tokens. 0 disables the context limit
    #[arg(long, default_value_t = 0, value_name = "TOKENS")]
    context_length_tokens: usize,
    /// Delay between tokens in milliseconds
    #[arg(long, default_value_t = 100, value_name = "MS")]
    token_delay_ms: u64,
    /// Deterministic bounded jitter added to each decode token delay based on request id
    #[arg(long, default_value_t = 0, value_name = "MS")]
    decode_jitter_ms: u64,
    /// Minimum per-request decode rate for deterministic uniform rate variation
    #[arg(
        long,
        value_name = "TPS",
        value_parser = parse_positive_rate,
        requires = "output_tps_max",
        conflicts_with_all = ["token_delay_ms", "decode_jitter_ms"]
    )]
    output_tps_min: Option<f64>,
    /// Maximum per-request decode rate for deterministic uniform rate variation
    #[arg(
        long,
        value_name = "TPS",
        value_parser = parse_positive_rate,
        requires = "output_tps_min",
        conflicts_with_all = ["token_delay_ms", "decode_jitter_ms"]
    )]
    output_tps_max: Option<f64>,
    /// Delay before the first output token in milliseconds
    #[arg(long, default_value_t = 0, value_name = "MS")]
    ttft_ms: u64,
    /// Deterministic bounded jitter added to TTFT based on request id
    #[arg(long, default_value_t = 0, value_name = "MS")]
    ttft_jitter_ms: u64,
    /// Approximate prefill throughput. When set, TTFT scales with input token count
    #[arg(long, default_value_t = 0.0, value_name = "TPS")]
    prefill_tokens_per_s: f64,
    /// Maximum concurrent requests the mock backend processes. 0 means unlimited
    #[arg(long, default_value_t = 0, value_name = "N")]
    max_concurrent_requests: usize,
    /// Delay /health responses to create deterministic RTT differences in tests
    #[arg(long, default_value_t = 0, value_name = "MS")]
    health_delay_ms: u64,
    /// Total mock KV-cache capacity in tokens. 0 disables cache tracking
    #[arg(long, default_value_t = 0, value_name = "TOKENS")]
    kv_cache_capacity_tokens: u64,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, clap::ValueEnum)]
enum Profile {
    /// One H100 80GB serving Llama 3.1 8B with FP8 weights near concurrency 25
    #[value(name = "h100-llama-3.1-8b")]
    H100Llama31EightB,
}

#[derive(Clone, Copy, Debug, PartialEq)]
struct Behavior {
    context_length_tokens: usize,
    decode_rate: DecodeRate,
    ttft_ms: u64,
    ttft_jitter_ms: u64,
    prefill_tokens_per_s: f64,
    max_concurrent_requests: usize,
    kv_cache_capacity_tokens: u64,
}

#[derive(Clone, Copy, Debug, PartialEq)]
enum DecodeRate {
    TokenDelay {
        base_ms: u64,
        jitter_ms: u64,
    },
    Uniform {
        min_tokens_per_s: f64,
        max_tokens_per_s: f64,
    },
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, clap::ValueEnum)]
enum OutputTokenDistribution {
    Uniform,
    Gaussian,
}

impl OutputTokenDistribution {
    const fn name(self) -> &'static str {
        match self {
            Self::Uniform => "uniform",
            Self::Gaussian => "gaussian",
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
struct OutputTokenConfig {
    min: usize,
    max: usize,
    distribution: OutputTokenDistribution,
}

impl OutputTokenConfig {
    const fn fixed(tokens: usize) -> Self {
        let tokens = if tokens == 0 { 1 } else { tokens };
        Self {
            min: tokens,
            max: tokens,
            distribution: OutputTokenDistribution::Uniform,
        }
    }
}

impl Args {
    fn behavior(&self) -> Result<Behavior> {
        if let Some(profile) = self.profile {
            return Ok(profile.behavior());
        }

        let decode_rate = match (self.output_tps_min, self.output_tps_max) {
            (Some(min_tokens_per_s), Some(max_tokens_per_s)) => {
                anyhow::ensure!(
                    min_tokens_per_s <= max_tokens_per_s,
                    "--output-tps-min must be less than or equal to --output-tps-max"
                );
                DecodeRate::Uniform {
                    min_tokens_per_s,
                    max_tokens_per_s,
                }
            }
            (None, None) => DecodeRate::TokenDelay {
                base_ms: self.token_delay_ms,
                jitter_ms: self.decode_jitter_ms,
            },
            _ => unreachable!("clap requires both output rate bounds"),
        };

        Ok(Behavior {
            context_length_tokens: self.context_length_tokens,
            decode_rate,
            ttft_ms: self.ttft_ms,
            ttft_jitter_ms: self.ttft_jitter_ms,
            prefill_tokens_per_s: self.prefill_tokens_per_s,
            max_concurrent_requests: self.max_concurrent_requests,
            kv_cache_capacity_tokens: self.kv_cache_capacity_tokens,
        })
    }

    fn output_tokens(&self) -> Result<OutputTokenConfig> {
        let defaults = self.profile.map_or_else(
            || OutputTokenConfig::fixed(self.num_tokens.unwrap_or(10)),
            Profile::default_output_tokens,
        );
        match (
            self.output_tokens_min,
            self.output_tokens_max,
            self.output_token_distribution,
        ) {
            (None, None, None) => Ok(defaults),
            (Some(min), Some(max), Some(distribution)) => {
                anyhow::ensure!(min > 0, "--output-tokens-min must be greater than zero");
                anyhow::ensure!(
                    min <= max,
                    "--output-tokens-min must be less than or equal to --output-tokens-max"
                );
                Ok(OutputTokenConfig {
                    min,
                    max,
                    distribution,
                })
            }
            _ => unreachable!("clap requires complete output token distribution settings"),
        }
    }
}

fn parse_positive_rate(value: &str) -> Result<f64, String> {
    let rate = value
        .parse::<f64>()
        .map_err(|error| format!("invalid output rate: {error}"))?;
    if rate.is_finite() && rate > 0.0 {
        Ok(rate)
    } else {
        Err("output rate must be finite and greater than zero".to_string())
    }
}

impl Profile {
    const fn behavior(self) -> Behavior {
        match self {
            Self::H100Llama31EightB => {
                let context_length_tokens = 131_072;
                Behavior {
                    context_length_tokens,
                    decode_rate: DecodeRate::Uniform {
                        min_tokens_per_s: 128.0,
                        max_tokens_per_s: 200.0,
                    },
                    ttft_ms: 10,
                    ttft_jitter_ms: 20,
                    prefill_tokens_per_s: 7000.0,
                    max_concurrent_requests: 25,
                    kv_cache_capacity_tokens: 400_000,
                }
            }
        }
    }

    const fn default_output_tokens(self) -> OutputTokenConfig {
        match self {
            Self::H100Llama31EightB => OutputTokenConfig {
                min: 128,
                max: 8192,
                distribution: OutputTokenDistribution::Gaussian,
            },
        }
    }

    fn explanation(self) -> String {
        let behavior = self.behavior();
        let output_tokens = self.default_output_tokens();
        let DecodeRate::Uniform {
            min_tokens_per_s,
            max_tokens_per_s,
        } = behavior.decode_rate
        else {
            unreachable!("named profile must use an output rate distribution");
        };
        let average_output_tps = (min_tokens_per_s + max_tokens_per_s) / 2.0;
        let aggregate_output_tps = average_output_tps * behavior.max_concurrent_requests as f64;
        let average_ttft_ms = |input_tokens: usize| {
            behavior.ttft_ms as f64
                + behavior.ttft_jitter_ms as f64 / 2.0
                + input_tokens as f64 * 1000.0 / behavior.prefill_tokens_per_s
        };

        format!(
            r#"Profile: h100-llama-3.1-8b
Calibration: NVIDIA NIM 1.8.0, Llama 3.1 8B Instruct, one H100 80GB, FP8 TP1, near 25 concurrent requests.
Model: fixed-delay approximation; dynamic batching and load-dependent slowdown are not modeled.

Model and hardware behavior:
  context length: {} input + output tokens
  prefill rate: {:.0} uncached input tokens/s
  TTFT: {} ms + deterministic 0..={} ms jitter + uncached prefill time
  decode rate: deterministic uniform {:.1}..={:.1} tokens/s per request
  active request limit: {}; additional requests wait
  KV cache capacity: {} input tokens, keyed by x-cache-affinity-key

Default request workload (overridable with --output-tokens-min, --output-tokens-max, and --output-token-distribution):
  sampled output tokens: deterministic {} distribution over {}..={}
  gaussian mean: {:.1} tokens; standard deviation: {:.1} tokens; samples are clamped to the range
  selected output tokens: min(sampled output tokens, request max_tokens when present)
  actual output tokens: min(selected output tokens, context length - input tokens)
  Pylon canary requests: one output token, "2"

Expected steady-state output rate:
  per active request: {:.1} tokens/s average ({:.1}..={:.1})
  at {} active requests: about {:.1} tokens/s before runtime overhead

Expected cold-cache TTFT:
  1,000 input tokens: about {:.1} ms average
  5,000 input tokens: about {:.1} ms average

Benchmark reference: https://docs.nvidia.com/nim/benchmarking/llm/1.0.0/performance.html#llama-3-1-8b-instruct-results
"#,
            behavior.context_length_tokens,
            behavior.prefill_tokens_per_s,
            behavior.ttft_ms,
            behavior.ttft_jitter_ms,
            min_tokens_per_s,
            max_tokens_per_s,
            behavior.max_concurrent_requests,
            behavior.kv_cache_capacity_tokens,
            output_tokens.distribution.name(),
            output_tokens.min,
            output_tokens.max,
            (output_tokens.min as f64 + output_tokens.max as f64) / 2.0,
            (output_tokens.max - output_tokens.min) as f64 / 6.0,
            average_output_tps,
            min_tokens_per_s,
            max_tokens_per_s,
            behavior.max_concurrent_requests,
            aggregate_output_tps,
            average_ttft_ms(1000),
            average_ttft_ms(5000),
        )
    }
}

#[derive(Clone)]
struct AppState {
    model_name: String,
    output_tokens: OutputTokenConfig,
    context_length_tokens: usize,
    decode_rate: DecodeRate,
    ttft: Duration,
    ttft_jitter_ms: u64,
    prefill_tokens_per_s: f64,
    request_capacity: Option<RequestCapacity>,
    health_delay: Duration,
    kv_cache: Arc<Mutex<kv_cache::KvCacheState>>,
    stats_events: broadcast::Sender<stats_stream::StatsStreamEvent>,
    test_control: test_control::TestControlState,
}

#[derive(Clone)]
struct RequestCapacity {
    limit: usize,
    slots: Arc<Semaphore>,
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .with_target(false)
        .compact()
        .init();

    let args = Args::parse();
    if let Some(profile) = args.explain_profile {
        print!("{}", profile.explanation());
        return Ok(());
    }
    let behavior = args.behavior()?;
    let output_tokens = args.output_tokens()?;
    let http_addr: std::net::SocketAddr = args.http_listen_addr.parse()?;

    let (stats_events, _) = broadcast::channel(1024);
    let state = AppState {
        model_name: args.model_name.clone(),
        output_tokens,
        context_length_tokens: behavior.context_length_tokens,
        decode_rate: behavior.decode_rate,
        ttft: Duration::from_millis(behavior.ttft_ms),
        ttft_jitter_ms: behavior.ttft_jitter_ms,
        prefill_tokens_per_s: behavior.prefill_tokens_per_s,
        request_capacity: (behavior.max_concurrent_requests > 0).then(|| RequestCapacity {
            limit: behavior.max_concurrent_requests,
            slots: Arc::new(Semaphore::new(behavior.max_concurrent_requests)),
        }),
        health_delay: Duration::from_millis(args.health_delay_ms),
        kv_cache: Arc::new(Mutex::new(kv_cache::KvCacheState::new(
            behavior.kv_cache_capacity_tokens,
        ))),
        stats_events,
        test_control: test_control::TestControlState::with_discovered_models([args.model_name]),
    };

    let app = Router::new()
        .route("/v1/chat/completions", post(openai::chat_completions))
        .route("/v1/models", get(openai::list_models))
        .route("/v1/responses", post(openai::responses))
        .route("/v1/embeddings", post(openai::embeddings))
        .route("/pylon/v1/stats/stream", get(stats_stream::stats_stream))
        .route("/kv-cache/stats", get(openai::kv_cache_stats))
        .route(
            "/test-control/models/{model}",
            put(test_control::update_model_test_control),
        )
        .route("/test-control", get(test_control::test_control_snapshot))
        .route(
            "/test-control/discovery-models",
            put(test_control::replace_discovery_models),
        )
        .route("/health", get(openai::health))
        .with_state(state);

    let listener = TcpListener::bind(http_addr).await?;
    let actual_http_addr = listener.local_addr()?;
    info!(addr = %actual_http_addr, "mock-dynamo HTTP listening");
    info!("send POST to http://{actual_http_addr}/v1/chat/completions");
    info!("send POST to http://{actual_http_addr}/v1/responses");
    info!("send POST to http://{actual_http_addr}/v1/embeddings");

    Ok(axum::serve(listener, app).await?)
}

#[cfg(test)]
mod tests;
