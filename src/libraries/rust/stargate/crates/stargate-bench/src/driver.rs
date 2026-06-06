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

use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::{Context, ensure};
use futures::StreamExt;
use serde::{Deserialize, Serialize};
use tokio::sync::Semaphore;

use crate::manifest::{Manifest, ManifestRequest};

#[derive(Debug, Clone)]
pub struct DriveConfig {
    pub endpoint: String,
    pub output_path: std::path::PathBuf,
    pub concurrency_limit: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RequestResult {
    pub request_index: usize,
    pub request_id: String,
    pub routing_key: Option<String>,
    pub cache_affinity_key: Option<String>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub scheduled_offset_ms: u64,
    pub status_code: u16,
    pub selected_backend_id: Option<String>,
    pub dispatch_offset_ms: u64,
    pub response_headers_ms: Option<u64>,
    pub first_output_ms: Option<u64>,
    pub completion_ms: u64,
    pub kv_cache_hit: Option<bool>,
    #[serde(default)]
    pub kv_cache_reused_input_tokens: Option<u64>,
    #[serde(default)]
    pub kv_cache_uncached_input_tokens: Option<u64>,
    pub kv_cache_evicted_entries: Option<u64>,
    pub kv_cache_evicted_tokens: Option<u64>,
    pub ok: bool,
    pub error: Option<String>,
}

pub fn load_manifest(path: &Path) -> anyhow::Result<Manifest> {
    let bytes = std::fs::read(path)
        .with_context(|| format!("failed to read manifest {}", path.display()))?;
    serde_json::from_slice(&bytes)
        .with_context(|| format!("failed to parse manifest {}", path.display()))
}

pub async fn drive_manifest(
    config: DriveConfig,
    manifest: Manifest,
) -> anyhow::Result<Vec<RequestResult>> {
    ensure!(
        config.concurrency_limit > 0,
        "concurrency_limit must be > 0"
    );
    let client = reqwest::Client::new();
    let start = Instant::now();
    let semaphore = Arc::new(Semaphore::new(config.concurrency_limit));
    let mut tasks = Vec::with_capacity(manifest.requests.len());

    for request in manifest.requests {
        let client = client.clone();
        let semaphore = semaphore.clone();
        let endpoint = config.endpoint.clone();
        let model = manifest.model.clone();
        let task = tokio::spawn(async move {
            let target = start + Duration::from_millis(request.scheduled_offset_ms);
            let now = Instant::now();
            if target > now {
                tokio::time::sleep_until(tokio::time::Instant::from_std(target)).await;
            }

            let _permit = semaphore
                .acquire_owned()
                .await
                .expect("semaphore should remain open");

            execute_request(&client, &endpoint, &model, &request, start).await
        });
        tasks.push(task);
    }

    let mut results = Vec::with_capacity(tasks.len());
    for task in tasks {
        results.push(task.await.context("request task failed to join")??);
    }
    results.sort_by_key(|result| result.request_index);
    write_results_jsonl(&config.output_path, &results)?;
    Ok(results)
}

async fn execute_request(
    client: &reqwest::Client,
    endpoint: &str,
    model: &str,
    request: &ManifestRequest,
    start: Instant,
) -> anyhow::Result<RequestResult> {
    let dispatch_time = Instant::now();
    let mut builder = client
        .post(endpoint)
        .header("x-request-id", request.request_id.clone())
        .header("x-model", model)
        .header("x-input-tokens", request.input_tokens.to_string())
        .header("x-output-tokens", request.output_tokens.to_string())
        .header("content-type", "application/json");
    if let Some(routing_key) = &request.routing_key {
        builder = builder.header("x-routing-key", routing_key);
    }
    if let Some(cache_affinity_key) = &request.cache_affinity_key {
        builder = builder.header("x-cache-affinity-key", cache_affinity_key);
    }

    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "benchmark"}],
        "max_tokens": request.output_tokens,
        "stream": true,
    });

    let response = builder.json(&body).send().await;
    // Dispatch timestamps are taken from the same monotonic clock; clamp only to keep malformed
    // test harness inputs from producing negative report offsets.
    let dispatch_offset_ms = duration_ms(dispatch_time.saturating_duration_since(start));
    match response {
        Ok(response) => {
            let status_code = response.status().as_u16();
            let selected_backend_id = response
                .headers()
                .get("x-inference-server-id")
                .and_then(|value| value.to_str().ok())
                .map(str::to_owned);
            let kv_cache_hit = bool_header(response.headers(), "x-kv-cache-hit");
            let kv_cache_reused_input_tokens =
                u64_header(response.headers(), "x-kv-cache-reused-input-tokens");
            let kv_cache_uncached_input_tokens =
                u64_header(response.headers(), "x-kv-cache-uncached-input-tokens");
            let kv_cache_evicted_entries =
                u64_header(response.headers(), "x-kv-cache-evicted-entries");
            let kv_cache_evicted_tokens =
                u64_header(response.headers(), "x-kv-cache-evicted-tokens");
            let response_headers_ms = duration_ms(dispatch_time.elapsed());
            let mut first_output_ms = None;
            let mut stream = response.bytes_stream();
            let mut stream_text = String::new();
            while let Some(chunk) = stream.next().await {
                match chunk {
                    Ok(bytes) => {
                        if !bytes.is_empty() {
                            stream_text.push_str(&String::from_utf8_lossy(&bytes));
                        }
                        if first_output_ms.is_none() && saw_content_delta(&stream_text) {
                            first_output_ms = Some(duration_ms(dispatch_time.elapsed()));
                        }
                    }
                    Err(error) => {
                        return Ok(RequestResult {
                            request_index: request.request_index,
                            request_id: request.request_id.clone(),
                            routing_key: request.routing_key.clone(),
                            cache_affinity_key: request.cache_affinity_key.clone(),
                            input_tokens: request.input_tokens,
                            output_tokens: request.output_tokens,
                            scheduled_offset_ms: request.scheduled_offset_ms,
                            status_code,
                            selected_backend_id,
                            dispatch_offset_ms,
                            response_headers_ms: Some(response_headers_ms),
                            first_output_ms,
                            completion_ms: duration_ms(dispatch_time.elapsed()),
                            kv_cache_hit,
                            kv_cache_reused_input_tokens,
                            kv_cache_uncached_input_tokens,
                            kv_cache_evicted_entries,
                            kv_cache_evicted_tokens,
                            ok: false,
                            error: Some(error.to_string()),
                        });
                    }
                }
            }

            Ok(RequestResult {
                request_index: request.request_index,
                request_id: request.request_id.clone(),
                routing_key: request.routing_key.clone(),
                cache_affinity_key: request.cache_affinity_key.clone(),
                input_tokens: request.input_tokens,
                output_tokens: request.output_tokens,
                scheduled_offset_ms: request.scheduled_offset_ms,
                status_code,
                selected_backend_id,
                dispatch_offset_ms,
                response_headers_ms: Some(response_headers_ms),
                first_output_ms,
                completion_ms: duration_ms(dispatch_time.elapsed()),
                kv_cache_hit,
                kv_cache_reused_input_tokens,
                kv_cache_uncached_input_tokens,
                kv_cache_evicted_entries,
                kv_cache_evicted_tokens,
                ok: (200..300).contains(&status_code),
                error: None,
            })
        }
        Err(error) => Ok(RequestResult {
            request_index: request.request_index,
            request_id: request.request_id.clone(),
            routing_key: request.routing_key.clone(),
            cache_affinity_key: request.cache_affinity_key.clone(),
            input_tokens: request.input_tokens,
            output_tokens: request.output_tokens,
            scheduled_offset_ms: request.scheduled_offset_ms,
            status_code: 0,
            selected_backend_id: None,
            dispatch_offset_ms,
            response_headers_ms: None,
            first_output_ms: None,
            completion_ms: duration_ms(dispatch_time.elapsed()),
            kv_cache_hit: None,
            kv_cache_reused_input_tokens: None,
            kv_cache_uncached_input_tokens: None,
            kv_cache_evicted_entries: None,
            kv_cache_evicted_tokens: None,
            ok: false,
            error: Some(error.to_string()),
        }),
    }
}

fn duration_ms(duration: Duration) -> u64 {
    duration.as_millis().try_into().unwrap_or(u64::MAX)
}

fn saw_content_delta(stream_text: &str) -> bool {
    stream_text.contains("\"content\":\"")
}

fn bool_header(headers: &reqwest::header::HeaderMap, name: &str) -> Option<bool> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| match value {
            "true" => Some(true),
            "false" => Some(false),
            _ => None,
        })
}

fn u64_header(headers: &reqwest::header::HeaderMap, name: &str) -> Option<u64> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse().ok())
}

pub fn write_results_jsonl(path: &Path, results: &[RequestResult]) -> anyhow::Result<()> {
    let mut out = String::new();
    for result in results {
        let line =
            serde_json::to_string(result).context("failed to serialize request result line")?;
        out.push_str(&line);
        out.push('\n');
    }
    std::fs::write(path, out).with_context(|| format!("failed to write {}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::manifest::Manifest;
    use std::collections::BTreeMap;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    fn request(index: usize, scheduled_offset_ms: u64) -> ManifestRequest {
        ManifestRequest {
            request_index: index,
            request_id: format!("req-{index}"),
            scheduled_offset_ms,
            routing_key: None,
            cache_affinity_key: None,
            input_tokens: 1,
            output_tokens: 1,
            backend_behavior_class: "default".to_string(),
        }
    }

    #[tokio::test]
    async fn scheduled_sleep_does_not_hold_concurrency_permit() {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("test server should bind");
        let endpoint = format!(
            "http://{}/v1/chat/completions",
            listener.local_addr().expect("local addr should exist")
        );
        let server = tokio::spawn(async move {
            let delays = BTreeMap::from([("req-0".to_string(), Duration::from_millis(250))]);
            for _ in 0..3 {
                let (mut socket, _) = listener.accept().await.expect("request should connect");
                let delays = delays.clone();
                tokio::spawn(async move {
                    let mut bytes = Vec::new();
                    let mut buffer = [0u8; 1024];
                    loop {
                        let read = socket.read(&mut buffer).await.expect("request should read");
                        if read == 0 {
                            break;
                        }
                        bytes.extend_from_slice(&buffer[..read]);
                        if bytes.windows(4).any(|window| window == b"\r\n\r\n") {
                            break;
                        }
                    }

                    let request = String::from_utf8_lossy(&bytes);
                    let request_id = request
                        .lines()
                        .find_map(|line| {
                            let (name, value) = line.split_once(':')?;
                            name.eq_ignore_ascii_case("x-request-id")
                                .then(|| value.trim().to_string())
                        })
                        .expect("request id header should be present");
                    if let Some(delay) = delays.get(&request_id) {
                        tokio::time::sleep(*delay).await;
                    }

                    socket
                        .write_all(
                            b"HTTP/1.1 200 OK\r\ncontent-length: 0\r\nconnection: close\r\nx-inference-server-id: backend-0\r\n\r\n",
                        )
                        .await
                        .expect("response should write");
                });
            }
        });

        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let output_path = tempdir.path().join("requests.jsonl");
        let manifest = Manifest {
            manifest_version: 1,
            benchmark_name: "schedule".to_string(),
            metadata: Default::default(),
            model: "dummy-model".to_string(),
            seed: 1,
            request_count: 3,
            max_concurrency: 2,
            stargate_count: 1,
            backend_count: 1,
            cluster_count: 1,
            pylons_per_cluster: 1,
            requests: vec![request(0, 0), request(1, 300), request(2, 50)],
        };

        let results = drive_manifest(
            DriveConfig {
                endpoint,
                output_path,
                concurrency_limit: 2,
            },
            manifest,
        )
        .await
        .expect("drive should complete");
        server.await.expect("server should complete");

        let request_two = results
            .iter()
            .find(|result| result.request_id == "req-2")
            .expect("req-2 result should exist");
        assert!(
            request_two.dispatch_offset_ms < 180,
            "req-2 dispatched at {}ms, indicating a future sleeping request held the permit",
            request_two.dispatch_offset_ms
        );
    }

    #[tokio::test]
    async fn zero_concurrency_limit_is_rejected() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let manifest = Manifest {
            manifest_version: 1,
            benchmark_name: "zero-concurrency".to_string(),
            metadata: Default::default(),
            model: "dummy-model".to_string(),
            seed: 1,
            request_count: 0,
            max_concurrency: 0,
            stargate_count: 1,
            backend_count: 1,
            cluster_count: 1,
            pylons_per_cluster: 1,
            requests: Vec::new(),
        };

        let err = drive_manifest(
            DriveConfig {
                endpoint: "http://127.0.0.1:9/v1/chat/completions".to_string(),
                output_path: tempdir.path().join("requests.jsonl"),
                concurrency_limit: 0,
            },
            manifest,
        )
        .await
        .expect_err("zero concurrency limit should fail validation");

        assert!(
            err.to_string().contains("concurrency_limit must be > 0"),
            "unexpected error: {err:#}"
        );
    }
}
