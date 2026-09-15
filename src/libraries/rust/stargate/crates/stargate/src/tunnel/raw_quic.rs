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

use anyhow::{Context, Result};
use axum::http::{HeaderMap, HeaderName, HeaderValue};
use quinn::Connection;

use stargate_protocol::{RecvStream, SendStream};

use super::body::{OpenStreamingRequest, OpenStreamingRequestInner};
use super::request::OpenTunnelRequest;

#[derive(Clone)]
pub(super) struct RawQuicConnectionHandle {
    connection: Connection,
}

impl RawQuicConnectionHandle {
    pub(super) fn new(connection: Connection) -> Self {
        Self { connection }
    }

    pub(super) fn is_healthy(&self) -> bool {
        self.connection.close_reason().is_none()
    }

    pub(super) fn stable_id(&self) -> usize {
        self.connection.stable_id()
    }

    #[cfg(test)]
    pub(super) fn connection(&self) -> &Connection {
        &self.connection
    }

    pub(super) async fn open_streaming_request(
        self,
        request: OpenTunnelRequest<'_>,
    ) -> Result<OpenStreamingRequest> {
        let (quinn_send, quinn_recv) = self
            .connection
            .open_bi()
            .await
            .context("open bi stream failed")?;

        let mut send_stream = SendStream::new(quinn_send);
        let recv_stream = RecvStream::new(quinn_recv);

        let mut request_headers = HeaderMap::new();
        request_headers.insert(
            HeaderName::from_static("x-method"),
            HeaderValue::from_str(request.method.as_str()).context("invalid method")?,
        );
        request_headers.insert(
            HeaderName::from_static("x-path"),
            HeaderValue::from_str(request.path_and_query).context("invalid path")?,
        );
        for (name, value) in &request.headers {
            request_headers.append(name, value.clone());
        }

        send_stream
            .send_header(request_headers)
            .await
            .context("failed to send request headers")?;

        Ok(OpenStreamingRequest {
            inner: OpenStreamingRequestInner::RawQuic {
                send_stream,
                recv_stream,
            },
            response_header_timeout: request.response_header_timeout(),
        })
    }
}
