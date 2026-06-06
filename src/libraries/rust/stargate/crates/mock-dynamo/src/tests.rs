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

use super::*;
use axum::Json;
use axum::extract::State;
use axum::http::HeaderMap;
use axum::http::HeaderValue;
use axum::routing::{get, post};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

fn request(max_tokens: Option<usize>) -> ChatRequest {
    ChatRequest {
        stream: Some(true),
        model: Some("dummy-model".to_string()),
        max_tokens,
        messages: Vec::new(),
    }
}

fn test_stats_events() -> broadcast::Sender<StatsStreamEvent> {
    let (tx, _) = broadcast::channel(1024);
    tx
}

#[test]
fn counts_openai_embedding_input_items() {
    assert_eq!(embedding_item_count(&serde_json::json!("single input")), 1);
    assert_eq!(embedding_item_count(&serde_json::json!(["a", "b"])), 2);
    assert_eq!(embedding_item_count(&serde_json::json!([1, 2, 3])), 1);
    assert_eq!(
        embedding_item_count(&serde_json::json!([[1, 2], [3, 4], [5]])),
        3
    );
    assert_eq!(embedding_item_count(&serde_json::json!([])), 0);
}

#[test]
fn embedding_format_controls_mock_embedding_value_shape() {
    assert!(matches!(
        deterministic_embedding_value(0, EmbeddingEncodingFormat::Float),
        EmbeddingValue::Float(_)
    ));
    assert!(matches!(
        deterministic_embedding_value(0, EmbeddingEncodingFormat::Base64),
        EmbeddingValue::Base64(_)
    ));
}

#[tokio::test]
async fn embeddings_endpoint_returns_json_without_stream() {
    let state = AppState {
        model_name: "dummy-model".to_string(),
        num_tokens: 1,
        token_delay: Duration::ZERO,
        decode_jitter_ms: 0,
        ttft: Duration::ZERO,
        ttft_jitter_ms: 0,
        prefill_tokens_per_s: 0.0,
        request_slots: None,
        health_delay: Duration::ZERO,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(0))),
        stats_events: test_stats_events(),
    };
    let app = Router::new()
        .route("/v1/embeddings", post(embeddings))
        .with_state(state);
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("test server should bind");
    let addr = listener.local_addr().expect("local address should exist");
    let server = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("test server should serve");
    });

    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    let request_body = serde_json::json!({
        "model": "request-model",
        "input": ["alpha", "beta"],
        "encoding_format": "float",
    })
    .to_string();
    stream
            .write_all(
                format!(
                    "POST /v1/embeddings HTTP/1.1\r\nhost: {addr}\r\ncontent-type: application/json\r\ncontent-length: {}\r\nconnection: close\r\nx-input-tokens: 11\r\n\r\n{request_body}",
                    request_body.len()
                )
                .as_bytes(),
            )
            .await
            .expect("request should write");

    let response = read_to_end(&mut stream).await;
    assert!(response.starts_with("HTTP/1.1 200 OK"));
    assert!(response.contains(r#""object":"list""#));
    assert!(response.contains(r#""model":"request-model""#));
    assert!(response.contains(r#""index":1"#));
    assert!(response.contains(r#""prompt_tokens":11"#));
    assert!(response.contains(r#""total_tokens":11"#));

    server.abort();
}

#[test]
fn output_tokens_prefer_benchmark_header() {
    let mut headers = HeaderMap::new();
    headers.insert("x-output-tokens", HeaderValue::from_static("64"));

    assert_eq!(request_output_tokens(&headers, &request(Some(16)), 8), 64);
}

#[test]
fn max_tokens_is_capped_by_default_when_header_absent() {
    assert_eq!(
        request_output_tokens(&HeaderMap::new(), &request(Some(128)), 8),
        8
    );
}

#[test]
fn prefill_delay_scales_with_input_tokens() {
    assert_eq!(prefill_delay(4_000, 2_000.0), Duration::from_secs(2));
}

fn completed_cache_access(
    cache: &mut KvCacheState,
    cache_affinity_key: Option<&str>,
    input_tokens: usize,
) -> KvCacheAccess {
    let access = cache.access(cache_affinity_key, input_tokens);
    access.with_commit(cache.commit(cache_affinity_key, input_tokens))
}

#[test]
fn kv_cache_hit_skips_subsequent_prefill() {
    let mut cache = KvCacheState::new(1_000);

    assert!(!completed_cache_access(&mut cache, Some("cak-a"), 100).hit);
    assert!(completed_cache_access(&mut cache, Some("cak-a"), 100).hit);
    assert_eq!(cache.stats("dummy-model").kv_cache_used_tokens, 100);
    assert_eq!(cache.stats("dummy-model").kv_cache_free_tokens, 900);
    assert_eq!(cache.stats("dummy-model").kv_cache_hit_count, 1);
    assert_eq!(cache.stats("dummy-model").kv_cache_miss_count, 1);
}

#[test]
fn kv_cache_hit_prefills_only_the_growing_prompt_suffix() {
    let mut cache = KvCacheState::new(200_000);

    let cold = completed_cache_access(&mut cache, Some("coding-session-a"), 100_000);
    assert_eq!(cold.reused_input_tokens, 0);
    assert_eq!(cold.uncached_input_tokens, 100_000);

    let follow_up = completed_cache_access(&mut cache, Some("coding-session-a"), 102_000);
    assert!(follow_up.hit);
    assert_eq!(follow_up.reused_input_tokens, 100_000);
    assert_eq!(follow_up.uncached_input_tokens, 2_000);
    assert_eq!(
        prefill_delay(follow_up.uncached_input_tokens as usize, 2_000.0),
        Duration::from_secs(1)
    );
    assert_eq!(cache.stats("dummy-model").kv_cache_used_tokens, 102_000);
}

#[test]
fn kv_cache_does_not_publish_an_in_flight_prefix_as_retained() {
    let mut cache = KvCacheState::new(200_000);

    let first = cache.access(Some("coding-session-a"), 100_000);
    assert!(!first.hit);

    let overlapping = cache.access(Some("coding-session-a"), 100_000);
    assert!(
        !overlapping.hit,
        "a request that has not completed modeled prefill must not warm later work"
    );
    cache.commit(Some("coding-session-a"), 100_000);
    assert!(cache.access(Some("coding-session-a"), 100_000).hit);
}

#[test]
fn kv_cache_shorter_follow_up_does_not_shrink_retained_prefix() {
    let mut cache = KvCacheState::new(200_000);

    assert!(!completed_cache_access(&mut cache, Some("coding-session-a"), 100_000).hit);
    let shorter = completed_cache_access(&mut cache, Some("coding-session-a"), 50_000);
    assert_eq!(shorter.reused_input_tokens, 50_000);

    let longer_again = cache.access(Some("coding-session-a"), 100_000);
    assert_eq!(longer_again.reused_input_tokens, 100_000);
    assert_eq!(longer_again.uncached_input_tokens, 0);
    assert_eq!(cache.stats("dummy-model").kv_cache_used_tokens, 100_000);
}

#[tokio::test]
async fn chat_completion_retains_prefix_only_after_modeled_prefill_completes() {
    let state = AppState {
        model_name: "dummy-model".to_string(),
        num_tokens: 1,
        token_delay: Duration::ZERO,
        decode_jitter_ms: 0,
        ttft: Duration::ZERO,
        ttft_jitter_ms: 0,
        prefill_tokens_per_s: 100.0,
        request_slots: None,
        health_delay: Duration::ZERO,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(1_000))),
        stats_events: test_stats_events(),
    };
    let observed_cache = state.kv_cache.clone();
    let mut stats_events = state.stats_events.subscribe();
    let mut headers = HeaderMap::new();
    headers.insert(
        "x-request-id",
        HeaderValue::from_static("req-prefill-cache"),
    );
    headers.insert("x-input-tokens", HeaderValue::from_static("100"));
    headers.insert("x-cache-affinity-key", HeaderValue::from_static("cache-a"));

    let request = tokio::spawn(chat_completions(
        State(state),
        headers,
        Json(request(Some(1))),
    ));
    tokio::time::timeout(Duration::from_millis(100), stats_events.recv())
        .await
        .expect("stream request should announce work before prefill completes")
        .expect("stats event stream should stay open");
    assert_eq!(
        observed_cache
            .lock()
            .await
            .stats("dummy-model")
            .kv_cache_entries,
        0,
        "in-flight input processing must not retain a reusable prefix"
    );

    tokio::time::timeout(Duration::from_secs(2), request)
        .await
        .expect("request should finish modeled prefill")
        .expect("request task should not fail");
    assert_eq!(
        observed_cache
            .lock()
            .await
            .stats("dummy-model")
            .kv_cache_used_tokens,
        100
    );
}

#[test]
fn kv_cache_evicts_least_recently_used_entry() {
    let mut cache = KvCacheState::new(300);

    assert!(!completed_cache_access(&mut cache, Some("cak-a"), 100).hit);
    assert!(!completed_cache_access(&mut cache, Some("cak-b"), 100).hit);
    assert!(completed_cache_access(&mut cache, Some("cak-a"), 100).hit);
    let access = completed_cache_access(&mut cache, Some("cak-c"), 150);
    assert!(!access.hit);
    assert_eq!(access.evicted_entries, 1);
    assert_eq!(access.evicted_tokens, 100);

    assert!(completed_cache_access(&mut cache, Some("cak-a"), 100).hit);
    assert!(!completed_cache_access(&mut cache, Some("cak-b"), 100).hit);
    assert_eq!(cache.stats("dummy-model").kv_cache_eviction_count, 2);
    assert_eq!(cache.stats("dummy-model").kv_cache_evicted_tokens, 250);
}

#[tokio::test]
async fn streaming_response_delays_first_data_frame_until_ttft() {
    let state = AppState {
        model_name: "dummy-model".to_string(),
        num_tokens: 1,
        token_delay: Duration::ZERO,
        decode_jitter_ms: 0,
        ttft: Duration::from_millis(120),
        ttft_jitter_ms: 0,
        prefill_tokens_per_s: 0.0,
        request_slots: None,
        health_delay: Duration::ZERO,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(0))),
        stats_events: test_stats_events(),
    };
    let app = Router::new()
        .route("/v1/chat/completions", post(chat_completions))
        .route("/pylon/v1/stats/stream", get(stats_stream))
        .with_state(state);
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("test server should bind");
    let addr = listener.local_addr().expect("local address should exist");
    let server = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("test server should serve");
    });

    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    let body = r#"{"model":"dummy-model","messages":[],"max_tokens":1,"stream":true}"#;
    stream
            .write_all(
                format!(
                    "POST /v1/chat/completions HTTP/1.1\r\nhost: {addr}\r\ncontent-type: application/json\r\ncontent-length: {}\r\nx-request-id: req-ttft\r\n\r\n{body}",
                    body.len()
                )
                .as_bytes(),
            )
            .await
            .expect("request should write");

    assert!(
        tokio::time::timeout(Duration::from_millis(50), read_until_sse_data(&mut stream))
            .await
            .is_err(),
        "mock emitted an SSE data frame before configured TTFT elapsed"
    );
    tokio::time::timeout(Duration::from_secs(1), read_until_sse_data(&mut stream))
        .await
        .expect("SSE data should arrive after TTFT")
        .expect("SSE data read should succeed");
    server.abort();
}

#[tokio::test]
async fn streaming_response_exposes_stats_stream_endpoint() {
    let state = AppState {
        model_name: "dummy-model".to_string(),
        num_tokens: 2,
        token_delay: Duration::ZERO,
        decode_jitter_ms: 0,
        ttft: Duration::ZERO,
        ttft_jitter_ms: 0,
        prefill_tokens_per_s: 0.0,
        request_slots: None,
        health_delay: Duration::ZERO,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(0))),
        stats_events: test_stats_events(),
    };
    let app = Router::new()
        .route("/v1/chat/completions", post(chat_completions))
        .route("/pylon/v1/stats/stream", get(stats_stream))
        .with_state(state);
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("test server should bind");
    let addr = listener.local_addr().expect("local address should exist");
    let server = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("test server should serve");
    });

    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    let body = r#"{"model":"dummy-model","messages":[],"max_tokens":2,"stream":true}"#;
    stream
            .write_all(
                format!(
                    "POST /v1/chat/completions HTTP/1.1\r\nhost: {addr}\r\ncontent-type: application/json\r\ncontent-length: {}\r\nx-request-id: req-contract\r\nx-input-tokens: 11\r\n\r\n{body}",
                    body.len()
                )
                .as_bytes(),
            )
            .await
            .expect("request should write");

    let response = read_until_done(&mut stream).await;
    assert!(!response.contains(r#""usage":"#));

    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    stream
        .write_all(
            format!(
                "GET /pylon/v1/stats/stream HTTP/1.1\r\nhost: {addr}\r\nconnection: close\r\n\r\n",
            )
            .as_bytes(),
        )
        .await
        .expect("stats stream request should write");

    let response = tokio::time::timeout(
        Duration::from_secs(2),
        read_until_contains(&mut stream, "application/x-ndjson"),
    )
    .await
    .expect("stats stream headers should arrive")
    .expect("stats stream response should read");
    assert!(response.starts_with("HTTP/1.1 200 OK"));
    assert!(response.contains("content-type: application/x-ndjson"));
    server.abort();
}

#[test]
fn stats_stream_events_are_ndjson() {
    let event = StatsStreamEvent::Stats {
        v: 1,
        request_id: "req-1".to_string(),
        model: "dummy-model".to_string(),
        tokens_processed: Some(11),
        tokens_generated: Some(2),
        finished: true,
    };

    let line = String::from_utf8(ndjson_event(&event).to_vec()).unwrap();

    assert_eq!(
            line,
            r#"{"type":"stats","v":1,"request_id":"req-1","model":"dummy-model","tokens_processed":11,"tokens_generated":2,"finished":true}"#
                .to_string()
                + "\n"
        );
}

#[tokio::test]
async fn responses_endpoint_streams_response_events_without_private_stats_headers() {
    let state = AppState {
        model_name: "dummy-model".to_string(),
        num_tokens: 2,
        token_delay: Duration::ZERO,
        decode_jitter_ms: 0,
        ttft: Duration::ZERO,
        ttft_jitter_ms: 0,
        prefill_tokens_per_s: 0.0,
        request_slots: None,
        health_delay: Duration::ZERO,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(10_000))),
        stats_events: test_stats_events(),
    };
    let app = Router::new()
        .route("/v1/responses", post(responses))
        .with_state(state);
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("test server should bind");
    let addr = listener.local_addr().expect("local address should exist");
    let server = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("test server should serve");
    });

    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    let request_body = serde_json::json!({
        "model": "request-model",
        "input": "hello",
        "max_output_tokens": 2,
        "stream": true,
    })
    .to_string();
    stream
            .write_all(
                format!(
                    "POST /v1/responses HTTP/1.1\r\nhost: {addr}\r\ncontent-type: application/json\r\ncontent-length: {}\r\nconnection: close\r\nx-request-id: req-responses-contract\r\nx-input-tokens: 7\r\nx-cache-affinity-key: cache-a\r\n\r\n{request_body}",
                    request_body.len()
                )
                .as_bytes(),
            )
            .await
            .expect("request should write");

    let response = read_to_end(&mut stream).await;
    assert!(response.starts_with("HTTP/1.1 200 OK"));
    assert!(response.contains("x-kv-cache-hit: false"));
    assert!(response.contains("x-kv-cache-reused-input-tokens: 0"));
    assert!(response.contains("x-kv-cache-uncached-input-tokens: 7"));
    assert!(response.contains("content-type: text/event-stream"));

    let (_, body_text) = response
        .split_once("\r\n\r\n")
        .expect("response should contain a body");
    assert!(body_text.contains("event: response.created"));
    assert!(body_text.contains("event: response.output_text.delta"));
    assert!(body_text.contains("event: response.completed"));
    assert!(body_text.contains(r#""type":"response.completed""#));
    assert!(body_text.contains(r#""object":"response""#));
    assert!(body_text.contains(r#""status":"completed""#));
    assert!(body_text.contains(r#""model":"request-model""#));
    assert!(body_text.contains(r#""input_tokens":7"#));
    assert!(body_text.contains(r#""output_tokens":2"#));
    assert!(body_text.contains(r#""total_tokens":9"#));

    server.abort();
}

#[tokio::test]
async fn responses_endpoint_rejects_non_streaming_requests() {
    let state = AppState {
        model_name: "dummy-model".to_string(),
        num_tokens: 2,
        token_delay: Duration::ZERO,
        decode_jitter_ms: 0,
        ttft: Duration::ZERO,
        ttft_jitter_ms: 0,
        prefill_tokens_per_s: 0.0,
        request_slots: None,
        health_delay: Duration::ZERO,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(10_000))),
        stats_events: test_stats_events(),
    };
    let app = Router::new()
        .route("/v1/responses", post(responses))
        .with_state(state);
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("test server should bind");
    let addr = listener.local_addr().expect("local address should exist");
    let server = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("test server should serve");
    });

    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    let request_body = serde_json::json!({
        "model": "request-model",
        "input": "hello",
        "stream": false,
    })
    .to_string();
    stream
            .write_all(
                format!(
                    "POST /v1/responses HTTP/1.1\r\nhost: {addr}\r\ncontent-type: application/json\r\ncontent-length: {}\r\nconnection: close\r\nx-request-id: req-responses-nonstream\r\nx-input-tokens: 7\r\n\r\n{request_body}",
                    request_body.len()
                )
                .as_bytes(),
            )
            .await
            .expect("request should write");

    let response = read_to_end(&mut stream).await;
    assert!(response.starts_with("HTTP/1.1 400 Bad Request"));
    assert!(response.contains("stream=true"));

    server.abort();
}

async fn read_until_sse_data(stream: &mut tokio::net::TcpStream) -> std::io::Result<()> {
    let mut bytes = Vec::new();
    let mut buffer = [0u8; 1024];
    loop {
        let read = stream.read(&mut buffer).await?;
        if read == 0 {
            return Ok(());
        }
        bytes.extend_from_slice(&buffer[..read]);
        if String::from_utf8_lossy(&bytes).contains("\ndata:") {
            return Ok(());
        }
    }
}

async fn read_until_done(stream: &mut tokio::net::TcpStream) -> String {
    let mut bytes = Vec::new();
    let mut buffer = [0u8; 1024];
    loop {
        let read = tokio::time::timeout(Duration::from_secs(2), stream.read(&mut buffer))
            .await
            .expect("response should continue")
            .expect("response should read");
        if read == 0 {
            break;
        }
        bytes.extend_from_slice(&buffer[..read]);
        if String::from_utf8_lossy(&bytes).contains("data: [DONE]") {
            break;
        }
    }
    String::from_utf8_lossy(&bytes).to_string()
}

async fn read_to_end(stream: &mut tokio::net::TcpStream) -> String {
    let mut bytes = Vec::new();
    stream
        .read_to_end(&mut bytes)
        .await
        .expect("response should read to end");
    String::from_utf8_lossy(&bytes).to_string()
}

async fn read_until_contains(
    stream: &mut tokio::net::TcpStream,
    needle: &str,
) -> std::io::Result<String> {
    let mut bytes = Vec::new();
    let mut buffer = [0u8; 1024];
    loop {
        let read = stream.read(&mut buffer).await?;
        if read == 0 {
            break;
        }
        bytes.extend_from_slice(&buffer[..read]);
        let text = String::from_utf8_lossy(&bytes);
        if text.contains(needle) {
            return Ok(text.to_string());
        }
    }
    Ok(String::from_utf8_lossy(&bytes).to_string())
}
