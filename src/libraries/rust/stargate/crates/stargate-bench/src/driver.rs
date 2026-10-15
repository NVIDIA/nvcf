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
use std::str::FromStr;
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

impl RequestResult {
    fn pending(request: ManifestRequest) -> Self {
        Self {
            request_index: request.request_index,
            request_id: request.request_id,
            routing_key: request.routing_key,
            cache_affinity_key: request.cache_affinity_key,
            input_tokens: request.input_tokens,
            output_tokens: request.output_tokens,
            scheduled_offset_ms: request.scheduled_offset_ms,
            status_code: 0,
            selected_backend_id: None,
            dispatch_offset_ms: 0,
            response_headers_ms: None,
            first_output_ms: None,
            completion_ms: 0,
            kv_cache_hit: None,
            kv_cache_reused_input_tokens: None,
            kv_cache_uncached_input_tokens: None,
            kv_cache_evicted_entries: None,
            kv_cache_evicted_tokens: None,
            ok: false,
            error: None,
        }
    }

    fn finish(mut self, dispatch_time: Instant, error: Option<reqwest::Error>) -> Self {
        self.completion_ms = duration_ms(dispatch_time.elapsed());
        self.error = error.map(|error| error.to_string());
        self
    }
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
    let endpoint: Arc<str> = config.endpoint.into();
    let model: Arc<str> = manifest.model.into();
    let mut tasks = Vec::with_capacity(manifest.requests.len());

    for request in manifest.requests {
        let client = client.clone();
        let semaphore = semaphore.clone();
        let endpoint = endpoint.clone();
        let model = model.clone();
        tasks.push(tokio::spawn(async move {
            let target = start + Duration::from_millis(request.scheduled_offset_ms);
            tokio::time::sleep_until(target.into()).await;

            let _permit = semaphore
                .acquire_owned()
                .await
                .expect("semaphore should remain open");

            execute_request(&client, &endpoint, &model, request, start).await
        }));
    }

    let mut results = futures::future::try_join_all(tasks)
        .await
        .context("request task failed to join")?;
    results.sort_by_key(|result| result.request_index);
    write_results_jsonl(&config.output_path, &results)?;
    Ok(results)
}

async fn execute_request(
    client: &reqwest::Client,
    endpoint: &str,
    model: &str,
    request: ManifestRequest,
    start: Instant,
) -> RequestResult {
    let mut result = RequestResult::pending(request);
    let Ok(input_tokens) = usize::try_from(result.input_tokens) else {
        result.error = Some("input token count does not fit usize".to_string());
        return result;
    };
    let dispatch_time = Instant::now();
    let mut builder = client
        .post(endpoint)
        .header("x-request-id", &result.request_id)
        .header("x-model", model)
        .header("x-input-tokens", result.input_tokens.to_string())
        .header("x-output-tokens", result.output_tokens.to_string())
        .header("content-type", "application/json");
    if let Some(routing_key) = &result.routing_key {
        builder = builder.header("x-routing-key", routing_key);
    }
    if let Some(cache_affinity_key) = &result.cache_affinity_key {
        builder = builder.header("x-cache-affinity-key", cache_affinity_key);
    }

    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "x".repeat(input_tokens)}],
        "max_tokens": result.output_tokens,
        "stream": true,
    });

    // Dispatch timestamps are taken from the same monotonic clock; clamp only to keep malformed
    // test harness inputs from producing negative report offsets.
    let response = builder.json(&body).send().await;
    result.dispatch_offset_ms = duration_ms(dispatch_time.saturating_duration_since(start));
    let response = match response {
        Ok(response) => response,
        Err(error) => return result.finish(dispatch_time, Some(error)),
    };

    result.status_code = response.status().as_u16();
    result.selected_backend_id = header(response.headers(), "x-inference-server-id");
    result.kv_cache_hit = header(response.headers(), "x-kv-cache-hit");
    result.kv_cache_reused_input_tokens =
        header(response.headers(), "x-kv-cache-reused-input-tokens");
    result.kv_cache_uncached_input_tokens =
        header(response.headers(), "x-kv-cache-uncached-input-tokens");
    result.kv_cache_evicted_entries = header(response.headers(), "x-kv-cache-evicted-entries");
    result.kv_cache_evicted_tokens = header(response.headers(), "x-kv-cache-evicted-tokens");
    result.response_headers_ms = Some(duration_ms(dispatch_time.elapsed()));

    let mut stream = response.bytes_stream();
    let mut stream_text = String::new();
    while let Some(chunk) = stream.next().await {
        let bytes = match chunk {
            Ok(bytes) => bytes,
            Err(error) => return result.finish(dispatch_time, Some(error)),
        };
        if result.first_output_ms.is_none() && !bytes.is_empty() {
            stream_text.push_str(&String::from_utf8_lossy(&bytes));
            if stream_text.contains("\"content\":\"") {
                result.first_output_ms = Some(duration_ms(dispatch_time.elapsed()));
            }
        }
    }

    result.ok = (200..300).contains(&result.status_code);
    result.finish(dispatch_time, None)
}

fn duration_ms(duration: Duration) -> u64 {
    duration.as_millis().try_into().unwrap_or(u64::MAX)
}

fn header<T: FromStr>(headers: &reqwest::header::HeaderMap, name: &str) -> Option<T> {
    headers.get(name)?.to_str().ok()?.parse().ok()
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

    fn manifest(requests: Vec<ManifestRequest>) -> Manifest {
        Manifest {
            manifest_version: 1,
            benchmark_name: "driver-test".to_string(),
            metadata: Default::default(),
            model: "dummy-model".to_string(),
            seed: 1,
            request_count: requests.len(),
            max_concurrency: 2,
            stargate_count: 1,
            backend_count: 1,
            cluster_count: 1,
            pylons_per_cluster: 1,
            requests,
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
            for _ in 0..3 {
                let (mut socket, _) = listener.accept().await.expect("request should connect");
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
                    if request_id == "req-0" {
                        tokio::time::sleep(Duration::from_millis(250)).await;
                    }

                    socket
                        .write_all(
                            b"HTTP/1.1 200 OK\r\ncontent-length: 0\r\nconnection: close\r\nx-inference-server-id: backend-0\r\nx-kv-cache-hit: true\r\nx-kv-cache-reused-input-tokens: 1\r\nx-kv-cache-uncached-input-tokens: 0\r\n\r\n",
                        )
                        .await
                        .expect("response should write");
                });
            }
        });

        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let output_path = tempdir.path().join("requests.jsonl");
        let manifest = manifest(vec![request(0, 0), request(1, 300), request(2, 50)]);

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
        assert!(matches!(
            request_two,
            RequestResult {
                status_code: 200,
                selected_backend_id: Some(id),
                response_headers_ms: Some(_),
                kv_cache_hit: Some(true),
                kv_cache_reused_input_tokens: Some(1),
                kv_cache_uncached_input_tokens: Some(0),
                ok: true,
                error: None,
                ..
            } if id == "backend-0"
        ));
    }

    #[tokio::test]
    async fn zero_concurrency_limit_is_rejected() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");

        let err = drive_manifest(
            DriveConfig {
                endpoint: "http://127.0.0.1:9/v1/chat/completions".to_string(),
                output_path: tempdir.path().join("requests.jsonl"),
                concurrency_limit: 0,
            },
            manifest(Vec::new()),
        )
        .await
        .expect_err("zero concurrency limit should fail validation");

        assert!(
            err.to_string().contains("concurrency_limit must be > 0"),
            "unexpected error: {err:#}"
        );
    }
}
