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

//! Closes HTTP connections in stages so that clients can read the last response.
//!
//! hyper closes the socket as soon as it has answered with `Connection: close`.
//! If the client is still sending the request body, the kernel then resets the
//! connection, and the reset can destroy the response before the client reads it
//! (RFC 9112 section 9.6). After hyper is done, this listener keeps reading and
//! discarding until the client closes its side or `MAX_LINGER` passes.

use std::io;
use std::net::SocketAddr;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::Duration;

use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, ReadBuf};
use tokio::net::{TcpListener, TcpStream};

/// Bounds how long a closed connection keeps reading data the client still sends.
const MAX_LINGER: Duration = Duration::from_secs(2);

pub(super) struct LingeringListener(TcpListener);

impl LingeringListener {
    pub(super) fn new(listener: TcpListener) -> Self {
        Self(listener)
    }
}

impl axum::serve::Listener for LingeringListener {
    type Io = LingeringStream;
    type Addr = SocketAddr;

    async fn accept(&mut self) -> (Self::Io, Self::Addr) {
        let (stream, addr) = axum::serve::Listener::accept(&mut self.0).await;
        (LingeringStream(Some(stream)), addr)
    }

    fn local_addr(&self) -> io::Result<Self::Addr> {
        self.0.local_addr()
    }
}

/// `None` only while the stream is being dropped.
pub(super) struct LingeringStream(Option<TcpStream>);

impl LingeringStream {
    fn stream(&mut self) -> Pin<&mut TcpStream> {
        // The stream is taken only in `Drop`, after the last poll.
        Pin::new(self.0.as_mut().expect("stream is present until drop"))
    }
}

impl AsyncRead for LingeringStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        self.stream().poll_read(cx, buf)
    }
}

impl AsyncWrite for LingeringStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        self.stream().poll_write(cx, buf)
    }

    fn poll_write_vectored(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        bufs: &[io::IoSlice<'_>],
    ) -> Poll<io::Result<usize>> {
        self.stream().poll_write_vectored(cx, bufs)
    }

    fn is_write_vectored(&self) -> bool {
        self.0.as_ref().is_some_and(TcpStream::is_write_vectored)
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        self.stream().poll_flush(cx)
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        self.stream().poll_shutdown(cx)
    }
}

impl Drop for LingeringStream {
    fn drop(&mut self) {
        let Some(stream) = self.0.take() else {
            return;
        };
        // Without a runtime (process shutdown), the socket closes at once.
        if let Ok(runtime) = tokio::runtime::Handle::try_current() {
            runtime.spawn(linger(stream));
        }
    }
}

async fn linger(mut stream: TcpStream) {
    // Sends FIN if hyper did not; fails only when the client already reset the
    // connection, and then there is nothing left to protect.
    let _ = stream.shutdown().await;
    let mut discard = [0u8; 8 * 1024];
    let read_until_client_closes =
        async { while matches!(stream.read(&mut discard).await, Ok(read) if read > 0) {} };
    // A client that keeps sending past the limit gets the reset it would get without lingering.
    let _ = tokio::time::timeout(MAX_LINGER, read_until_client_closes).await;
}
