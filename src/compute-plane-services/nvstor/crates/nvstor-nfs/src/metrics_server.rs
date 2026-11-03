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

//! Tiny HTTP/1.x server that serves Prometheus metrics from `NfsMetrics`.
//!
//! Implements just enough HTTP to satisfy `prometheus`, `curl`, and
//! `wget` — one request per connection, no keep-alive, no chunked
//! encoding. The intent is "the smallest thing that makes Prometheus
//! work" so we don't pull in `hyper` for a 5 KB text payload.
//!
//! Endpoints:
//!   GET /metrics  → text/plain Prometheus exposition format.
//!   GET /healthz  → "ok\n" (200) for liveness probes.
//!   anything else → 404.

use std::io;
use std::sync::Arc;
use std::time::Duration;

use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::{TcpListener, TcpStream};

use crate::metrics::NfsMetrics;

/// Bind on `addr` and serve `/metrics` from `metrics`. Runs forever.
pub async fn serve(addr: &str, metrics: Arc<NfsMetrics>) -> io::Result<()> {
    let listener = TcpListener::bind(addr).await?;
    tracing::info!(addr, "metrics: HTTP server listening");

    loop {
        let (stream, peer) = listener.accept().await?;
        let metrics = metrics.clone();
        tokio::spawn(async move {
            if let Err(e) = handle_one(stream, metrics).await {
                tracing::debug!(%peer, error = %e, "metrics: connection closed");
            }
        });
    }
}

async fn handle_one(stream: TcpStream, metrics: Arc<NfsMetrics>) -> io::Result<()> {
    // Hard cap on per-request work — we don't want a slow client tying up
    // a tokio task forever.
    let timeout = Duration::from_secs(10);
    tokio::time::timeout(timeout, handle_one_inner(stream, metrics))
        .await
        .unwrap_or_else(|_| {
            Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "metrics request timeout",
            ))
        })
}

async fn handle_one_inner(stream: TcpStream, metrics: Arc<NfsMetrics>) -> io::Result<()> {
    let (read_half, mut write_half) = stream.into_split();
    let mut reader = BufReader::new(read_half);

    // Parse just the request line. We don't need headers.
    let mut line = String::new();
    reader.read_line(&mut line).await?;
    let line = line.trim_end_matches(['\r', '\n']);

    // Drain the rest of the headers so the client doesn't see RST.
    loop {
        let mut h = String::new();
        let n = reader.read_line(&mut h).await?;
        if n == 0 || h == "\r\n" || h == "\n" {
            break;
        }
    }

    let mut parts = line.split_whitespace();
    let method = parts.next().unwrap_or("");
    let path = parts.next().unwrap_or("");

    if method != "GET" {
        write_status(
            &mut write_half,
            405,
            "Method Not Allowed",
            "method not allowed\n",
        )
        .await?;
        return Ok(());
    }

    match path {
        "/metrics" => {
            let body = metrics.to_prometheus();
            write_response(
                &mut write_half,
                200,
                "OK",
                "text/plain; version=0.0.4; charset=utf-8",
                body.as_bytes(),
            )
            .await?;
        }
        "/healthz" => {
            write_response(&mut write_half, 200, "OK", "text/plain", b"ok\n").await?;
        }
        _ => {
            write_status(&mut write_half, 404, "Not Found", "not found\n").await?;
        }
    }
    Ok(())
}

async fn write_response(
    w: &mut (impl AsyncWriteExt + Unpin),
    code: u16,
    reason: &str,
    content_type: &str,
    body: &[u8],
) -> io::Result<()> {
    let header = format!(
        "HTTP/1.1 {code} {reason}\r\n\
         Content-Type: {content_type}\r\n\
         Content-Length: {len}\r\n\
         Connection: close\r\n\
         \r\n",
        len = body.len(),
    );
    w.write_all(header.as_bytes()).await?;
    w.write_all(body).await?;
    w.flush().await?;
    Ok(())
}

async fn write_status(
    w: &mut (impl AsyncWriteExt + Unpin),
    code: u16,
    reason: &str,
    body: &str,
) -> io::Result<()> {
    write_response(w, code, reason, "text/plain", body.as_bytes()).await
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpStream;

    async fn spawn_server() -> (Arc<NfsMetrics>, std::net::SocketAddr) {
        let metrics = Arc::new(NfsMetrics::new());
        // Pre-record a hit so /metrics has something interesting.
        metrics.record_op_latency(crate::metrics::OpCategory::Read, 42);

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let m = metrics.clone();
        tokio::spawn(async move {
            loop {
                let (stream, _) = listener.accept().await.unwrap();
                let m = m.clone();
                tokio::spawn(async move {
                    let _ = handle_one(stream, m).await;
                });
            }
        });
        (metrics, addr)
    }

    async fn http_get(addr: std::net::SocketAddr, path: &str) -> (u16, String) {
        let mut s = TcpStream::connect(addr).await.unwrap();
        s.write_all(format!("GET {path} HTTP/1.1\r\nHost: x\r\n\r\n").as_bytes())
            .await
            .unwrap();
        let mut buf = String::new();
        s.read_to_string(&mut buf).await.unwrap();
        let mut lines = buf.split("\r\n");
        let status_line = lines.next().unwrap_or("");
        let code = status_line
            .split_whitespace()
            .nth(1)
            .and_then(|c| c.parse().ok())
            .unwrap_or(0);
        // Return whole response; tests grep for substrings.
        (code, buf)
    }

    #[tokio::test]
    async fn metrics_endpoint_returns_prometheus_text() {
        let (_m, addr) = spawn_server().await;
        let (code, body) = http_get(addr, "/metrics").await;
        assert_eq!(code, 200);
        assert!(body.contains("Content-Type: text/plain"));
        assert!(body.contains("# TYPE nvstor_nfs_op_duration_seconds histogram"));
        assert!(body.contains("nvstor_nfs_op_duration_seconds_count{category=\"read\"} 1"));
    }

    #[tokio::test]
    async fn healthz_returns_ok() {
        let (_m, addr) = spawn_server().await;
        let (code, body) = http_get(addr, "/healthz").await;
        assert_eq!(code, 200);
        assert!(body.contains("\r\n\r\nok\n"));
    }

    #[tokio::test]
    async fn unknown_path_returns_404() {
        let (_m, addr) = spawn_server().await;
        let (code, _body) = http_get(addr, "/nope").await;
        assert_eq!(code, 404);
    }

    #[tokio::test]
    async fn post_returns_405() {
        let (_m, addr) = spawn_server().await;
        let mut s = TcpStream::connect(addr).await.unwrap();
        s.write_all(b"POST /metrics HTTP/1.1\r\nHost: x\r\n\r\n")
            .await
            .unwrap();
        let mut buf = String::new();
        s.read_to_string(&mut buf).await.unwrap();
        assert!(buf.starts_with("HTTP/1.1 405"));
    }
}
