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
mod timing;

use std::sync::Arc;
use std::time::Duration;

use anyhow::Result;
use axum::Router;
use axum::routing::{get, post};
use clap::Parser;
use tokio::net::TcpListener;
use tokio::sync::{Mutex, Semaphore, broadcast};
use tracing::info;
use tracing_subscriber::EnvFilter;

#[cfg(test)]
use kv_cache::*;
#[cfg(test)]
use openai::*;
#[cfg(test)]
use stats_stream::*;
#[cfg(test)]
use timing::*;

#[derive(clap::Parser, Debug)]
#[command(name = "mock-dynamo")]
struct Args {
    /// HTTP listen address for the mock inference server
    #[arg(long, default_value = "127.0.0.1:8090", value_name = "ADDR")]
    http_listen_addr: String,

    /// Model name served by this server
    #[arg(long, default_value = "dummy-model", value_name = "MODEL")]
    model_name: String,

    /// Number of dummy tokens to generate
    #[arg(long, default_value_t = 10, value_name = "N")]
    num_tokens: usize,

    /// Delay between tokens in milliseconds
    #[arg(long, default_value_t = 100, value_name = "MS")]
    token_delay_ms: u64,

    /// Deterministic bounded jitter added to each decode token delay based on request id
    #[arg(long, default_value_t = 0, value_name = "MS")]
    decode_jitter_ms: u64,

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

#[derive(Clone)]
struct AppState {
    model_name: String,
    num_tokens: usize,
    token_delay: Duration,
    decode_jitter_ms: u64,
    ttft: Duration,
    ttft_jitter_ms: u64,
    prefill_tokens_per_s: f64,
    request_slots: Option<Arc<Semaphore>>,
    health_delay: Duration,
    kv_cache: Arc<Mutex<kv_cache::KvCacheState>>,
    stats_events: broadcast::Sender<stats_stream::StatsStreamEvent>,
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
    let http_addr: std::net::SocketAddr = args.http_listen_addr.parse()?;

    let (stats_events, _) = broadcast::channel(1024);
    let state = AppState {
        model_name: args.model_name.clone(),
        num_tokens: args.num_tokens,
        token_delay: Duration::from_millis(args.token_delay_ms),
        decode_jitter_ms: args.decode_jitter_ms,
        ttft: Duration::from_millis(args.ttft_ms),
        ttft_jitter_ms: args.ttft_jitter_ms,
        prefill_tokens_per_s: args.prefill_tokens_per_s,
        request_slots: (args.max_concurrent_requests > 0)
            .then(|| Arc::new(Semaphore::new(args.max_concurrent_requests))),
        health_delay: Duration::from_millis(args.health_delay_ms),
        kv_cache: Arc::new(Mutex::new(kv_cache::KvCacheState::new(
            args.kv_cache_capacity_tokens,
        ))),
        stats_events,
    };

    let app = Router::new()
        .route("/v1/chat/completions", post(openai::chat_completions))
        .route("/v1/responses", post(openai::responses))
        .route("/v1/embeddings", post(openai::embeddings))
        .route("/pylon/v1/stats/stream", get(stats_stream::stats_stream))
        .route("/kv-cache/stats", get(openai::kv_cache_stats))
        .route("/health", get(openai::health))
        .with_state(state);

    let listener = TcpListener::bind(http_addr).await?;
    let actual_http_addr = listener.local_addr()?;
    info!(addr = %actual_http_addr, "mock-dynamo HTTP listening");
    info!("send POST to http://{actual_http_addr}/v1/chat/completions");
    info!("send POST to http://{actual_http_addr}/v1/responses");
    info!("send POST to http://{actual_http_addr}/v1/embeddings");

    axum::serve(listener, app).await?;

    Ok(())
}

#[cfg(test)]
mod tests;
