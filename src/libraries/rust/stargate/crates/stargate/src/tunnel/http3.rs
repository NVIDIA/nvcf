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

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use anyhow::{Context, Result};
use axum::http::HeaderName;
use futures::future;
use quinn::Connection;
use tracing::warn;

use super::direct::QuicHttpProxy;
use super::direct::TunnelConnection;

pub(super) type H3ClientBidiStream =
    <h3_quinn::OpenStreams as h3::quic::OpenStreams<bytes::Bytes>>::BidiStream;
pub(super) type H3ClientConnection = h3::client::Connection<h3_quinn::Connection, bytes::Bytes>;
pub(super) type H3ServerConnection = h3::server::Connection<h3_quinn::Connection, bytes::Bytes>;
pub(super) type H3ClientRequestStream = h3::client::RequestStream<H3ClientBidiStream, bytes::Bytes>;
pub(super) type H3ClientRequestSendStream = h3::client::RequestStream<
    <H3ClientBidiStream as h3::quic::BidiStream<bytes::Bytes>>::SendStream,
    bytes::Bytes,
>;
pub(super) type H3ClientRequestRecvStream = h3::client::RequestStream<
    <H3ClientBidiStream as h3::quic::BidiStream<bytes::Bytes>>::RecvStream,
    bytes::Bytes,
>;
pub(super) type H3ServerRequestStream = h3::server::RequestStream<H3ClientBidiStream, bytes::Bytes>;

#[derive(Clone)]
pub(super) struct Http3ConnectionHandle {
    pub(super) connection: Connection,
    pub(super) send_request: h3::client::SendRequest<h3_quinn::OpenStreams, bytes::Bytes>,
    pub(super) driver_closed: Arc<AtomicBool>,
    _driver_task: Arc<Http3DriverTask>,
}

struct Http3DriverTask {
    connection: Connection,
    task: tokio::task::JoinHandle<()>,
}

impl Drop for Http3DriverTask {
    fn drop(&mut self) {
        // The driver task is Arc-held by live H3 handles. When the last handle
        // drops, close QUIC first; abort is only a leak-prevention fallback.
        self.connection.close(0u32.into(), b"h3 driver dropped");
        self.task.abort();
    }
}

impl QuicHttpProxy {
    pub(super) async fn build_h3_client_connection(
        &self,
        connection: Connection,
    ) -> Result<TunnelConnection> {
        let driver_closed = Arc::new(AtomicBool::new(false));
        let driver_closed_for_task = driver_closed.clone();
        let (mut driver, send_request) = h3::client::builder()
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .map_err(h3_error)
            .context("create h3 client connection")?;
        let driver_task = tokio::spawn(async move {
            let error = future::poll_fn(|cx| driver.poll_close(cx)).await;
            driver_closed_for_task.store(true, Ordering::Release);
            if !error.is_h3_no_error() {
                warn!(error = ?error, "h3 client connection closed with error");
            }
        });
        Ok(TunnelConnection::Http3(Http3ConnectionHandle {
            connection: connection.clone(),
            send_request,
            driver_closed,
            _driver_task: Arc::new(Http3DriverTask {
                connection,
                task: driver_task,
            }),
        }))
    }
}

pub(super) fn should_forward_h3_tunnel_request_header(name: &HeaderName) -> bool {
    !matches!(
        name.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
    )
}

pub(super) fn h3_error<E>(error: E) -> anyhow::Error
where
    E: std::error::Error + Send + Sync + 'static,
{
    anyhow::Error::new(error)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn h3_error_context_preserves_source_chain() {
        let error = h3_error(std::io::Error::other("inner h3 failure")).context("outer h3 context");

        assert_eq!(error.to_string(), "outer h3 context");
        assert!(
            error
                .chain()
                .any(|source| source.to_string() == "inner h3 failure"),
            "source chain should retain the original error: {error:#}"
        );
    }
}
