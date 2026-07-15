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

use super::kv_cache::*;
use super::openai::*;
use super::stats_stream::*;
use super::test_control::*;
use super::timing::*;
use super::*;
use axum::Json;
use axum::extract::State;
use axum::http::HeaderMap;
use axum::http::HeaderValue;
use axum::routing::{get, post, put};
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

fn test_state() -> AppState {
    AppState {
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
        test_control: TestControlState::with_discovered_models(["dummy-model".to_string()]),
    }
}

async fn spawn_test_app(app: Router) -> (std::net::SocketAddr, tokio::task::JoinHandle<()>) {
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("test server should bind");
    let addr = listener.local_addr().expect("local address should exist");
    let server = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("test server should serve");
    });
    (addr, server)
}

async fn send_json_request(
    addr: std::net::SocketAddr,
    method: &str,
    path: &str,
    headers: &str,
    body: &str,
) -> tokio::net::TcpStream {
    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    let request = format!(
        "{method} {path} HTTP/1.1\r\nhost: {addr}\r\ncontent-type: application/json\r\ncontent-length: {}\r\n{headers}\r\n\r\n{body}",
        body.len()
    );
    stream
        .write_all(request.as_bytes())
        .await
        .expect("request should write");
    stream
}

async fn json_response(
    addr: std::net::SocketAddr,
    method: &str,
    path: &str,
    headers: &str,
    body: &str,
) -> String {
    read_to_end(&mut send_json_request(addr, method, path, headers, body).await).await
}

#[tokio::test]
async fn test_controls_isolate_chat_failure_by_model() {
    let controls = TestControlState::default();

    controls
        .update_model(
            "model-b",
            ModelTestControlUpdate {
                chat_failure: Some(true),
                ..ModelTestControlUpdate::default()
            },
        )
        .await;

    assert!(!controls.chat_failure_enabled("model-a").await);
    assert!(controls.chat_failure_enabled("model-b").await);

    controls
        .update_model(
            "model-b",
            ModelTestControlUpdate {
                chat_failure: Some(false),
                ..ModelTestControlUpdate::default()
            },
        )
        .await;

    assert!(!controls.chat_failure_enabled("model-b").await);
}

#[tokio::test]
async fn test_controls_hold_bringup_until_the_model_gate_is_released() {
    let controls = TestControlState::default();
    controls
        .update_model(
            "model-a",
            ModelTestControlUpdate {
                bringup_blocked: Some(true),
                ..ModelTestControlUpdate::default()
            },
        )
        .await;
    let waiting_controls = controls.clone();
    let (entered_tx, entered_rx) = tokio::sync::oneshot::channel();
    let waiter = tokio::spawn(async move {
        entered_tx.send(()).expect("test should observe waiter");
        waiting_controls.wait_for_bringup_release("model-a").await;
    });
    entered_rx.await.expect("waiter should start");
    tokio::task::yield_now().await;
    assert!(!waiter.is_finished());

    controls
        .update_model(
            "model-a",
            ModelTestControlUpdate {
                bringup_blocked: Some(false),
                ..ModelTestControlUpdate::default()
            },
        )
        .await;

    tokio::time::timeout(Duration::from_secs(1), waiter)
        .await
        .expect("released bringup should finish")
        .expect("bringup waiter should not panic");
}

#[tokio::test]
async fn test_controls_count_endpoint_model_and_request_class() {
    let controls = TestControlState::default();
    let recorded = [
        (
            TestEndpoint::ChatCompletions,
            "model-a",
            TestRequestClass::PylonGenerated,
        ),
        (
            TestEndpoint::ChatCompletions,
            "model-a",
            TestRequestClass::ApiGateway,
        ),
        (
            TestEndpoint::Embeddings,
            "model-b",
            TestRequestClass::ApiGateway,
        ),
    ];
    for (endpoint, model, request_class) in recorded {
        controls
            .record_request(endpoint, model, request_class)
            .await;
    }

    let snapshot = controls.snapshot().await;
    for (endpoint, model, request_class, expected) in recorded
        .map(|(endpoint, model, class)| (endpoint, model, class, 1))
        .into_iter()
        .chain([(
            TestEndpoint::Responses,
            "model-a",
            TestRequestClass::ApiGateway,
            0,
        )])
    {
        assert_eq!(snapshot.counter(endpoint, model, request_class), expected);
    }
}

#[tokio::test]
async fn test_control_http_api_updates_one_model_and_reports_request_counters() {
    let state = test_state();
    let observed_control = state.test_control.clone();
    let app = Router::new()
        .route("/v1/chat/completions", post(chat_completions))
        .route(
            "/test-control/models/{model}",
            put(update_model_test_control),
        )
        .route("/test-control", get(test_control_snapshot))
        .with_state(state);
    let (addr, server) = spawn_test_app(app).await;

    let update_body = r#"{"chat_failure":true}"#;
    let update_response = json_response(
        addr,
        "PUT",
        "/test-control/models/model-b",
        "connection: close",
        update_body,
    )
    .await;
    assert!(update_response.starts_with("HTTP/1.1 200 OK"));
    assert!(update_response.contains(r#""model-b":{"chat_failure":true,"bringup_blocked":false}"#));

    let failed_body = r#"{"model":"model-b","messages":[],"max_tokens":1,"stream":false}"#;
    let failed_response = json_response(
        addr,
        "POST",
        "/v1/chat/completions",
        "connection: close\r\nx-request-id: calibration-00000000-0000-4000-8000-000000000000-7",
        failed_body,
    )
    .await;
    assert!(failed_response.starts_with("HTTP/1.1 503 Service Unavailable"));

    let successful_body = r#"{"model":"model-a","messages":[],"max_tokens":1,"stream":false}"#;
    let successful_response = json_response(
        addr,
        "POST",
        "/v1/chat/completions",
        "connection: close\r\nx-request-id: user-7",
        successful_body,
    )
    .await;
    assert!(successful_response.starts_with("HTTP/1.1 200 OK"));
    assert!(successful_response.contains("content-type: application/json"));
    let (_, body) = successful_response
        .split_once("\r\n\r\n")
        .expect("chat response should contain a body");
    let mut body: serde_json::Value =
        serde_json::from_str(body).expect("chat response should be valid JSON");
    assert!(
        body["id"]
            .as_str()
            .is_some_and(|id| id.starts_with("chatcmpl-mock-"))
    );
    body["id"] = serde_json::json!("chatcmpl-mock-id");
    assert_eq!(
        body,
        serde_json::from_str::<serde_json::Value>(
            r#"{"id":"chatcmpl-mock-id","object":"chat.completion","model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}"#,
        )
        .expect("expected chat response fixture should be valid JSON")
    );

    let snapshot_response = raw_http_request(
        addr,
        &format!("GET /test-control HTTP/1.1\r\nhost: {addr}\r\nconnection: close\r\n\r\n"),
    )
    .await;
    assert!(snapshot_response.starts_with("HTTP/1.1 200 OK"));
    assert!(snapshot_response.contains(r#""endpoint":"chat_completions""#));
    assert!(snapshot_response.contains(r#""request_class":"pylon_generated""#));
    assert!(snapshot_response.contains(r#""request_class":"api_gateway""#));

    let snapshot = observed_control.snapshot().await;
    assert_eq!(
        snapshot.counter(
            TestEndpoint::ChatCompletions,
            "model-b",
            TestRequestClass::PylonGenerated,
        ),
        1
    );
    assert_eq!(
        snapshot.counter(
            TestEndpoint::ChatCompletions,
            "model-a",
            TestRequestClass::ApiGateway,
        ),
        1
    );

    server.abort();
}

#[tokio::test]
async fn model_discovery_http_api_returns_and_replaces_authoritative_models() {
    let state = test_state();
    state
        .test_control
        .replace_discovered_models(vec!["model-b".to_string(), "model-a".to_string()])
        .await
        .expect("valid model IDs should be accepted");
    let test_control = state.test_control.clone();
    let app = Router::new()
        .route("/v1/models", get(list_models))
        .route(
            "/test-control/discovery-models",
            put(replace_discovery_models),
        )
        .with_state(state);
    let (addr, server) = spawn_test_app(app).await;

    let initial = json_response(addr, "GET", "/v1/models", "connection: close", "").await;
    assert!(initial.contains(
        r#"{"object":"list","data":[{"id":"model-a","object":"model"},{"id":"model-b","object":"model"}]}"#
    ));
    assert_eq!(test_control.snapshot().await.model_discovery_requests, 1);

    let replaced = json_response(
        addr,
        "PUT",
        "/test-control/discovery-models",
        "connection: close",
        r#"{"models":[]}"#,
    )
    .await;
    assert!(replaced.starts_with("HTTP/1.1 200 OK"));
    assert!(replaced.contains(r#""discovered_models":[]"#));

    let empty = json_response(addr, "GET", "/v1/models", "connection: close", "").await;
    assert!(empty.contains(r#"{"object":"list","data":[]}"#));
    assert_eq!(test_control.snapshot().await.model_discovery_requests, 2);

    server.abort();
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

fn embedding_tokens(input: serde_json::Value) -> usize {
    request_embedding_tokens(&HeaderMap::new(), &input)
}

#[test]
fn embedding_token_estimates_follow_input_shape() {
    assert_eq!(embedding_tokens(serde_json::json!("abcd")), 4);
    assert_eq!(embedding_tokens(serde_json::json!([1, 2, 3])), 3);
    assert_eq!(embedding_tokens(serde_json::json!(["alpha", "b"])), 6);
    assert_eq!(
        embedding_tokens(serde_json::json!([[1, 2], [3, 4], [5]])),
        5
    );
    assert_eq!(
        embedding_tokens(serde_json::json!(["abc", [1, 2], true])),
        6
    );
    assert_eq!(embedding_tokens(serde_json::json!({"unexpected": true})), 1);
}

#[test]
fn embedding_token_estimates_preserve_empty_batch_item_work() {
    assert_eq!(embedding_tokens(serde_json::json!(["", "b"])), 2);
    assert_eq!(embedding_tokens(serde_json::json!([[], [1, 2]])), 3);
}

#[test]
fn embedding_token_header_override_clamps_to_nonzero() {
    let mut headers = HeaderMap::new();
    headers.insert("x-input-tokens", HeaderValue::from_static("0"));

    assert_eq!(
        request_embedding_tokens(&headers, &serde_json::json!(["alpha", "beta"])),
        1
    );
}

#[test]
fn embedding_format_controls_mock_embedding_value_shape() {
    assert_eq!(
        serde_json::to_value(deterministic_embedding_value(
            0,
            EmbeddingEncodingFormat::Float
        ))
        .unwrap(),
        serde_json::json!([0.0, 0.125, -0.25])
    );
    assert_eq!(
        serde_json::to_value(deterministic_embedding_value(
            0,
            EmbeddingEncodingFormat::Base64
        ))
        .unwrap(),
        "AAAAAAAAAAA="
    );
}

#[test]
fn chat_stream_chunks_preserve_delta_and_finish_shapes() {
    for (chunk, delta, finish_reason) in [
        (
            ChatStreamChunk::Role,
            serde_json::json!({ "role": "assistant" }),
            serde_json::Value::Null,
        ),
        (
            ChatStreamChunk::Content("token"),
            serde_json::json!({ "content": "token" }),
            serde_json::Value::Null,
        ),
        (
            ChatStreamChunk::Stop,
            serde_json::json!({}),
            serde_json::json!("stop"),
        ),
    ] {
        let value: serde_json::Value =
            serde_json::from_str(&chat_chunk_json("id", "model", chunk)).unwrap();
        assert_eq!(value["choices"][0]["delta"], delta);
        assert_eq!(value["choices"][0]["finish_reason"], finish_reason);
        assert!(value.get("usage").is_none());
    }
}

#[tokio::test]
async fn embeddings_endpoint_returns_json_without_stream() {
    let state = test_state();
    let app = Router::new()
        .route("/v1/embeddings", post(embeddings))
        .with_state(state);
    let (addr, server) = spawn_test_app(app).await;

    let request_body = serde_json::json!({
        "model": "request-model",
        "input": ["alpha", "beta"],
        "encoding_format": "float",
    })
    .to_string();
    let response = json_response(
        addr,
        "POST",
        "/v1/embeddings",
        "connection: close\r\nx-input-tokens: 9",
        &request_body,
    )
    .await;
    assert!(response.starts_with("HTTP/1.1 200 OK"));
    assert!(response.contains(r#""object":"list""#));
    assert!(response.contains(r#""model":"request-model""#));
    assert!(response.contains(r#""index":1"#));
    assert!(response.contains(r#""prompt_tokens":9"#));
    assert!(response.contains(r#""total_tokens":9"#));

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

#[test]
fn timing_boundaries_saturate_and_keep_deterministic_jitter() {
    assert_eq!(prefill_delay(1, 0.0), Duration::ZERO);
    assert_eq!(prefill_delay(1, f64::NAN), Duration::ZERO);
    assert_eq!(prefill_delay(1, f64::INFINITY), Duration::ZERO);
    assert_eq!(prefill_delay(usize::MAX, 0.5), Duration::MAX);
    assert_eq!(jitter_ms("request-a", "salt", 0), 0);
    assert!(jitter_ms("request-a", "salt", 10) <= 10);
    assert_eq!(
        jitter_ms("request-a", "salt", u64::MAX),
        18_127_296_015_107_935_538
    );
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
        prefill_tokens_per_s: 100.0,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(1_000))),
        ..test_state()
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

    let mut chat_request = request(Some(1));
    chat_request.messages = vec![serde_json::json!({ "content": "x".repeat(100) })];
    let request = tokio::spawn(chat_completions(State(state), headers, Json(chat_request)));
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
        ttft: Duration::from_millis(120),
        ..test_state()
    };
    let app = Router::new()
        .route("/v1/chat/completions", post(chat_completions))
        .route("/pylon/v1/stats/stream", get(stats_stream))
        .with_state(state);
    let (addr, server) = spawn_test_app(app).await;

    let body = r#"{"model":"dummy-model","messages":[],"max_tokens":1,"stream":true}"#;
    let mut stream = send_json_request(
        addr,
        "POST",
        "/v1/chat/completions",
        "x-request-id: req-ttft",
        body,
    )
    .await;

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
        num_tokens: 2,
        ..test_state()
    };
    let app = Router::new()
        .route("/v1/chat/completions", post(chat_completions))
        .route("/pylon/v1/stats/stream", get(stats_stream))
        .with_state(state);
    let (addr, server) = spawn_test_app(app).await;

    let body = r#"{"model":"dummy-model","messages":[{"role":"user","content":"hello world"}],"max_tokens":2,"stream":true}"#;
    let mut stream = send_json_request(
        addr,
        "POST",
        "/v1/chat/completions",
        "x-request-id: req-contract\r\nx-input-tokens: 11",
        body,
    )
    .await;

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
        num_tokens: 2,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(10_000))),
        ..test_state()
    };
    let app = Router::new()
        .route("/v1/responses", post(responses))
        .with_state(state);
    let (addr, server) = spawn_test_app(app).await;

    let cases = [
        (None, 1),
        (Some(serde_json::json!("hello")), 5),
        (Some(serde_json::json!(["hi", {"x": 1}])), 9),
        (Some(serde_json::json!({"x": 1})), 7),
    ];
    let mut response = String::new();
    for (index, (input, expected_tokens)) in cases.into_iter().enumerate() {
        let mut request_body = serde_json::json!({
            "model": "request-model",
            "max_output_tokens": 2,
            "stream": true,
        });
        if let Some(input) = input {
            request_body["input"] = input;
        }
        response = json_response(
            addr,
            "POST",
            "/v1/responses",
            &format!("connection: close\r\nx-request-id: req-responses-{index}\r\nx-input-tokens: {expected_tokens}\r\nx-cache-affinity-key: cache-{index}"),
            &request_body.to_string(),
        )
        .await;
        assert!(response.contains(&format!(
            "x-kv-cache-uncached-input-tokens: {expected_tokens}"
        )));
        assert!(response.contains(&format!(r#""input_tokens":{expected_tokens}"#)));
    }
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
        num_tokens: 2,
        kv_cache: Arc::new(Mutex::new(KvCacheState::new(10_000))),
        ..test_state()
    };
    let app = Router::new()
        .route("/v1/responses", post(responses))
        .with_state(state);
    let (addr, server) = spawn_test_app(app).await;

    let request_body = serde_json::json!({
        "model": "request-model",
        "input": "hello",
        "stream": false,
    })
    .to_string();
    let response = json_response(
        addr,
        "POST",
        "/v1/responses",
        "connection: close\r\nx-request-id: req-responses-nonstream\r\nx-input-tokens: 5",
        &request_body,
    )
    .await;
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

async fn raw_http_request(addr: std::net::SocketAddr, request: &str) -> String {
    let mut stream = tokio::net::TcpStream::connect(addr)
        .await
        .expect("test client should connect");
    stream
        .write_all(request.as_bytes())
        .await
        .expect("request should write");
    read_to_end(&mut stream).await
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
