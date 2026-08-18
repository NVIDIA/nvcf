// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::HashSet;
use std::time::Duration;

use bytes::Bytes;
use futures::{Stream, StreamExt};
use serde::Deserialize;
use tokio_util::sync::CancellationToken;

use super::aggregator::{KvCacheStatsEnvelope, KvCacheStatsSnapshot};

const MAX_LINE_BYTES: usize = 1024 * 1024;

pub(super) struct KvStatsStreamConfig {
    pub(super) url: String,
    pub(super) reconnect_interval: Duration,
    pub(super) connect_timeout: Duration,
    pub(super) idle_timeout: Duration,
}

#[derive(Deserialize)]
struct RawSnapshot {
    v: u8,
    #[serde(rename = "type")]
    event_type: String,
    observed_at_unix_ms: u64,
    models: Vec<RawModelStats>,
}

#[derive(Deserialize)]
struct RawModelStats {
    model: String,
    #[serde(default)]
    aliases: Vec<String>,
    routing_cache: Option<RawRoutingCacheStats>,
}

#[derive(Deserialize)]
struct RawRoutingCacheStats {
    capacity_tokens: Option<u64>,
    used_tokens: Option<u64>,
    free_tokens: Option<u64>,
    complete: bool,
}

pub(super) fn parse_kv_stats_snapshot(line: &[u8]) -> anyhow::Result<KvCacheStatsEnvelope> {
    let snapshot: RawSnapshot = serde_json::from_slice(line)?;
    anyhow::ensure!(
        snapshot.v == 1,
        "unsupported KV stats version {}",
        snapshot.v
    );
    anyhow::ensure!(
        snapshot.event_type == "kv_stats_snapshot",
        "unsupported KV stats event type {}",
        snapshot.event_type
    );
    let mut identities = HashSet::new();
    let models = snapshot
        .models
        .into_iter()
        .map(|model| -> anyhow::Result<_> {
            anyhow::ensure!(!model.model.trim().is_empty(), "KV stats model is empty");
            anyhow::ensure!(
                identities.insert(model.model.clone()),
                "duplicate KV stats identity {}",
                model.model
            );
            for alias in &model.aliases {
                anyhow::ensure!(!alias.trim().is_empty(), "KV stats alias is empty");
                anyhow::ensure!(
                    identities.insert(alias.clone()),
                    "duplicate KV stats identity {alias}"
                );
            }
            let routing = model.routing_cache;
            let complete = snapshot.observed_at_unix_ms > 0
                && routing.as_ref().is_some_and(|routing| {
                    let Some((capacity, used, free)) = routing
                        .capacity_tokens
                        .zip(routing.used_tokens)
                        .zip(routing.free_tokens)
                        .map(|((capacity, used), free)| (capacity, used, free))
                    else {
                        return false;
                    };
                    routing.complete && capacity > 0 && used.checked_add(free) == Some(capacity)
                });
            let (capacity, used, free) = routing
                .map(|routing| {
                    (
                        routing.capacity_tokens.unwrap_or_default(),
                        routing.used_tokens.unwrap_or_default(),
                        routing.free_tokens.unwrap_or_default(),
                    )
                })
                .unwrap_or_default();
            Ok(KvCacheStatsSnapshot {
                model: model.model,
                aliases: model.aliases,
                kv_cache_capacity_tokens: capacity,
                kv_cache_used_tokens: used,
                kv_cache_free_tokens: free,
                source_observed_at_unix_ms: snapshot.observed_at_unix_ms,
                complete,
            })
        })
        .collect::<anyhow::Result<Vec<_>>>()?;
    Ok(KvCacheStatsEnvelope { models })
}

pub(super) async fn run_kv_stats_stream(
    config: KvStatsStreamConfig,
    updates: flume::Sender<KvCacheStatsEnvelope>,
    stop: CancellationToken,
) {
    let client = reqwest::Client::new();
    while !stop.is_cancelled() {
        if let Err(error) = read_stream_once(&config, &client, &updates, &stop).await {
            tracing::warn!(url = config.url, %error, "KV stats stream disconnected");
        }
        if stop
            .run_until_cancelled(tokio::time::sleep(config.reconnect_interval))
            .await
            .is_none()
        {
            break;
        }
    }
}

async fn read_stream_once(
    config: &KvStatsStreamConfig,
    client: &reqwest::Client,
    updates: &flume::Sender<KvCacheStatsEnvelope>,
    stop: &CancellationToken,
) -> anyhow::Result<()> {
    let response = tokio::select! {
        _ = stop.cancelled() => return Ok(()),
        response = tokio::time::timeout(
            config.connect_timeout,
            client
                .get(&config.url)
                .header(reqwest::header::ACCEPT, "application/x-ndjson")
                .send(),
        ) => response??,
    };
    anyhow::ensure!(
        response.status().is_success(),
        "KV stats endpoint returned {}",
        response.status()
    );
    drain_response(response.bytes_stream(), updates, stop, config.idle_timeout).await
}

async fn drain_response<S>(
    mut stream: S,
    updates: &flume::Sender<KvCacheStatsEnvelope>,
    stop: &CancellationToken,
    idle_timeout: Duration,
) -> anyhow::Result<()>
where
    S: Stream<Item = Result<Bytes, reqwest::Error>> + Unpin,
{
    let mut buffer = Vec::with_capacity(4096);
    let mut discarding_oversized_line = false;
    loop {
        let chunk = tokio::select! {
            _ = stop.cancelled() => return Ok(()),
            chunk = tokio::time::timeout(idle_timeout, stream.next()) => {
                chunk.map_err(|_| anyhow::anyhow!("KV stats stream became idle"))?
            },
        };
        let Some(chunk) = chunk else {
            anyhow::bail!("KV stats stream ended");
        };
        let chunk = chunk?;
        let mut remaining = chunk.as_ref();
        while let Some(newline) = remaining.iter().position(|byte| *byte == b'\n') {
            let segment = &remaining[..newline];
            remaining = &remaining[newline + 1..];
            if discarding_oversized_line {
                discarding_oversized_line = false;
                continue;
            }
            if buffer.len().saturating_add(segment.len()) > MAX_LINE_BYTES {
                tracing::warn!("dropping oversized KV stats line");
                buffer.clear();
                continue;
            }
            buffer.extend_from_slice(segment);
            if buffer.iter().all(u8::is_ascii_whitespace) {
                buffer.clear();
                continue;
            }
            match parse_kv_stats_snapshot(&buffer) {
                Ok(snapshot) => {
                    match stop.run_until_cancelled(updates.send_async(snapshot)).await {
                        None | Some(Err(_)) => return Ok(()),
                        Some(Ok(())) => {}
                    }
                }
                Err(error) => tracing::warn!(%error, "dropping invalid KV stats snapshot"),
            }
            buffer.clear();
        }
        if discarding_oversized_line {
            continue;
        }
        if buffer.len().saturating_add(remaining.len()) > MAX_LINE_BYTES {
            tracing::warn!("dropping oversized KV stats line");
            buffer.clear();
            discarding_oversized_line = true;
        } else {
            buffer.extend_from_slice(remaining);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_complete_snapshot_without_placement_data() {
        let snapshot = parse_kv_stats_snapshot(
            br#"{"v":1,"type":"kv_stats_snapshot","snapshot_id":9,"observed_at_unix_ms":42,"models":[{"model":"m","aliases":["alias"],"routing_cache":{"role":"decode","capacity_tokens":100,"used_tokens":40,"free_tokens":60,"complete":true},"pools":[]}]}"#,
        )
        .unwrap();
        assert_eq!(snapshot.models[0].source_observed_at_unix_ms, 42);
        assert_eq!(snapshot.models.len(), 1);
        assert_eq!(snapshot.models[0].aliases, ["alias"]);
        assert!(snapshot.models[0].complete);
    }

    #[test]
    fn inconsistent_complete_snapshot_is_not_usable() {
        let snapshot = parse_kv_stats_snapshot(
            br#"{"v":1,"type":"kv_stats_snapshot","observed_at_unix_ms":42,"models":[{"model":"m","aliases":[],"routing_cache":{"capacity_tokens":100,"used_tokens":80,"free_tokens":30,"complete":true},"pools":[]}]}"#,
        )
        .unwrap();
        assert!(!snapshot.models[0].complete);
    }

    #[test]
    fn duplicate_alias_ownership_rejects_the_whole_snapshot() {
        let result = parse_kv_stats_snapshot(
            br#"{"v":1,"type":"kv_stats_snapshot","observed_at_unix_ms":42,"models":[{"model":"a","aliases":["shared"],"routing_cache":null},{"model":"b","aliases":["shared"],"routing_cache":null}]}"#,
        );
        assert!(result.is_err());
    }

    #[test]
    fn line_limit_accepts_a_representative_thousand_model_snapshot() {
        let models = (0..1_000)
            .map(|index| {
                serde_json::json!({
                    "model": format!("model-{index:04}"),
                    "aliases": [format!("deployment-model-{index:04}")],
                    "routing_cache": {
                        "role": "decode",
                        "capacity_tokens": 65_536_000,
                        "used_tokens": 6_553_600,
                        "free_tokens": 58_982_400,
                        "complete": true
                    },
                    "pools": [{
                        "namespace": "dynamo",
                        "component": "backend",
                        "endpoint": "generate",
                        "role": "decode",
                        "storage_tier": "device",
                        "block_size_tokens": 64,
                        "expected_ranks": 8,
                        "observed_ranks": 8,
                        "capacity_blocks": 1_024_000,
                        "used_blocks": 102_400,
                        "free_blocks": 921_600,
                        "active_decode_blocks": 81_920,
                        "complete": true
                    }]
                })
            })
            .collect::<Vec<_>>();
        let line = serde_json::to_vec(&serde_json::json!({
            "v": 1,
            "type": "kv_stats_snapshot",
            "snapshot_id": 1,
            "observed_at_unix_ms": 1,
            "models": models
        }))
        .unwrap();

        assert!(
            line.len() <= MAX_LINE_BYTES,
            "representative snapshot is {} bytes",
            line.len()
        );
    }

    #[tokio::test]
    async fn fragmented_ndjson_is_reassembled_before_publication() {
        let chunks = futures::stream::iter([
            Ok::<_, reqwest::Error>(Bytes::from_static(
                b"{\"v\":1,\"type\":\"kv_stats_snapshot\",\"observed_at_unix_ms\":9,",
            )),
            Ok(Bytes::from_static(
                b"\"models\":[{\"model\":\"m\",\"routing_cache\":{\"capacity_tokens\":10,\"used_tokens\":4,\"free_tokens\":6,\"complete\":true}}]}\n",
            )),
        ]);
        let (tx, rx) = flume::bounded(1);
        let result = drain_response(
            chunks,
            &tx,
            &CancellationToken::new(),
            Duration::from_secs(1),
        )
        .await;
        assert!(
            result.is_err(),
            "finite response should end after publishing"
        );
        let snapshot = rx.try_recv().expect("snapshot should be published");
        assert_eq!(snapshot.models[0].source_observed_at_unix_ms, 9);
        assert!(snapshot.models[0].complete);
    }

    #[tokio::test]
    async fn oversized_line_is_dropped_without_buffering_or_losing_the_next_snapshot() {
        let valid =
            b"{\"v\":1,\"type\":\"kv_stats_snapshot\",\"observed_at_unix_ms\":9,\"models\":[]}\n";
        let mut first = vec![b'x'; MAX_LINE_BYTES + 1];
        first.extend_from_slice(b"\n");
        let chunks = futures::stream::iter([
            Ok::<_, reqwest::Error>(Bytes::from(first)),
            Ok(Bytes::from_static(valid)),
        ]);
        let (tx, rx) = flume::bounded(1);

        let result = drain_response(
            chunks,
            &tx,
            &CancellationToken::new(),
            Duration::from_secs(1),
        )
        .await;

        assert!(
            result.is_err(),
            "finite response should end after publishing"
        );
        assert_eq!(
            rx.try_recv()
                .expect("valid line should still publish")
                .models
                .len(),
            0
        );
    }

    #[tokio::test]
    async fn idle_stream_is_reconnected() {
        let stream = futures::stream::pending::<Result<Bytes, reqwest::Error>>();
        let (tx, _rx) = flume::bounded(1);

        let error = drain_response(
            stream,
            &tx,
            &CancellationToken::new(),
            Duration::from_millis(10),
        )
        .await
        .unwrap_err();

        assert!(error.to_string().contains("became idle"));
    }
}
