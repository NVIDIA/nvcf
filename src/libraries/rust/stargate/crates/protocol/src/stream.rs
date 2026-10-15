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

use crate::ProtocolError;
use crate::protocol::{
    QuicBodyOrTrailer, QuicMessage, expect_stream_end, read_body_or_trailer_from_stream,
    read_from_stream, read_header_map_from_stream, write_body_to_stream,
    write_header_map_to_stream, write_trailer_map_to_stream,
};
use quinn::{StoppedError, VarInt};
use tracing::warn;

fn protocol_violation(message: &'static str) -> ProtocolError {
    ProtocolError::ProtocolViolation(message.to_string())
}

pub struct SendStream {
    sent_headers: bool,
    send: quinn::SendStream,
    eos: bool,
}

impl SendStream {
    pub fn new(send: quinn::SendStream) -> Self {
        Self {
            sent_headers: false,
            send,
            eos: false,
        }
    }

    pub async fn send_header(&mut self, header: http::HeaderMap) -> Result<(), ProtocolError> {
        if self.sent_headers {
            return Err(protocol_violation("headers already sent"));
        }
        write_header_map_to_stream(&mut self.send, &header).await?;
        self.sent_headers = true;
        Ok(())
    }

    pub async fn send_body(&mut self, body: bytes::Bytes) -> Result<(), ProtocolError> {
        if !self.sent_headers {
            return Err(protocol_violation("must send headers before sending body"));
        }
        write_body_to_stream(&mut self.send, body).await
    }

    pub async fn send_trailer(&mut self, trailer: http::HeaderMap) -> Result<(), ProtocolError> {
        if !self.sent_headers {
            return Err(protocol_violation(
                "must send headers before sending trailer",
            ));
        }
        if self.eos {
            return Err(protocol_violation("stream already finished"));
        }
        write_trailer_map_to_stream(&mut self.send, &trailer).await
    }

    pub fn finish(&mut self) -> Result<(), ProtocolError> {
        if self.eos {
            return Ok(());
        }
        self.send
            .finish()
            .map_err(|e| ProtocolError::Io(std::io::Error::other(e)))?;
        self.eos = true;
        Ok(())
    }

    pub async fn stopped(&mut self) -> Result<Option<VarInt>, StoppedError> {
        self.send.stopped().await
    }
}

impl Drop for SendStream {
    fn drop(&mut self) {
        if !self.eos {
            warn!("SendStream: dropped before finishing");
            let _ = self.send.reset(0_u8.into());
        }
    }
}

pub struct RecvStream {
    recv: Option<quinn::RecvStream>,
    received_header: bool,
    received_trailer: Option<http::HeaderMap>,
    eos: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RecvBodyFrame {
    /// A body chunk read from the stream.
    Body(bytes::Bytes),
    /// A trailer frame was read and buffered; call `recv_trailer` to retrieve it.
    TrailersReady,
    /// The peer finished the stream without sending more body or trailers.
    End,
}

impl RecvBodyFrame {
    /// Returns the body bytes when this frame is `Body`.
    pub fn into_body(self) -> Option<bytes::Bytes> {
        match self {
            Self::Body(body) => Some(body),
            Self::TrailersReady | Self::End => None,
        }
    }
}

impl RecvStream {
    pub fn new(recv: quinn::RecvStream) -> Self {
        Self {
            recv: Some(recv),
            received_header: false,
            received_trailer: None,
            eos: false,
        }
    }

    fn recv_mut(&mut self) -> Result<&mut quinn::RecvStream, ProtocolError> {
        self.recv
            .as_mut()
            .ok_or_else(|| protocol_violation("stream already stopped or dropped"))
    }

    pub async fn recv_header(&mut self) -> Result<http::HeaderMap, ProtocolError> {
        if self.received_header {
            return Err(protocol_violation("recv_header called more than once"));
        }
        let Some(header_map) = read_header_map_from_stream(self.recv_mut()?).await? else {
            return Err(protocol_violation("expected header message, got none"));
        };
        self.received_header = true;
        Ok(header_map)
    }

    pub async fn recv_body(&mut self) -> Result<RecvBodyFrame, ProtocolError> {
        if !self.received_header {
            return Err(protocol_violation(
                "must call recv_header once before recv_body",
            ));
        }
        match read_body_or_trailer_from_stream(self.recv_mut()?).await? {
            Some(QuicBodyOrTrailer::Body(body)) => Ok(RecvBodyFrame::Body(body)),
            Some(QuicBodyOrTrailer::Trailer(trailer)) => {
                self.received_trailer = Some(trailer);
                Ok(RecvBodyFrame::TrailersReady)
            }
            None => {
                self.eos = true;
                Ok(RecvBodyFrame::End)
            }
        }
    }

    pub async fn recv_trailer(&mut self) -> Result<Option<http::HeaderMap>, ProtocolError> {
        if !self.received_header {
            return Err(protocol_violation(
                "must call recv_header once before recv_trailer",
            ));
        }
        let trailer = match (self.received_trailer.take(), self.eos) {
            (Some(t), _) => Some(t),
            (None, true) => None,
            (None, false) => match read_body_or_trailer_from_stream(self.recv_mut()?).await? {
                Some(QuicBodyOrTrailer::Trailer(trailer)) => Some(trailer),
                None => {
                    self.eos = true;
                    None
                }
                Some(QuicBodyOrTrailer::Body(_)) => {
                    return Err(protocol_violation("expected trailer message, got body"));
                }
            },
        };

        if !self.eos {
            expect_stream_end(self.recv_mut()?).await?;
            self.eos = true;
        }

        Ok(trailer)
    }

    pub async fn recv_any(&mut self) -> Result<Option<QuicMessage>, ProtocolError> {
        let Some(reader) = read_from_stream(self.recv_mut()?).await? else {
            self.eos = true;
            return Ok(None);
        };
        QuicMessage::from_reader(reader).map(Some)
    }

    pub async fn stop(mut self, code: u32) -> Option<VarInt> {
        let mut recv = self.recv.take()?;
        if recv.stop(code.into()).is_err() {
            return None;
        }
        recv.received_reset().await.unwrap_or(None)
    }
}

impl Drop for RecvStream {
    fn drop(&mut self) {
        if !self.eos {
            warn!("RecvStream: dropped before eos");
        }
        if let Some(mut recv) = self.recv.take() {
            let _ = recv.stop(0_u8.into());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use bytes::Bytes;
    use quinn::Endpoint;
    use std::future::Future;

    type QuicPair = (quinn::Connection, quinn::Connection, Endpoint, Endpoint);

    async fn quic_pair() -> QuicPair {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let server_endpoint =
            Endpoint::server(server_config(), "127.0.0.1:0".parse().unwrap()).unwrap();
        let server_addr = server_endpoint.local_addr().unwrap();
        let server_task = tokio::spawn(async move {
            let incoming = server_endpoint.accept().await.unwrap();
            let server_connection = incoming.await.unwrap();
            (server_endpoint, server_connection)
        });

        let mut client_endpoint = Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        client_endpoint
            .set_default_client_config(stargate_tls::build_insecure_quic_client_config().unwrap());
        let client_connection = client_endpoint
            .connect(server_addr, "stargate")
            .unwrap()
            .await
            .unwrap();
        let (server_endpoint, server_connection) = server_task.await.unwrap();

        (
            client_connection,
            server_connection,
            client_endpoint,
            server_endpoint,
        )
    }

    async fn recv_stream_from_writer<F, Fut>(write: F) -> (RecvStream, QuicPair)
    where
        F: FnOnce(SendStream) -> Fut,
        Fut: Future<Output = ()>,
    {
        let quic = quic_pair().await;
        let (client_send, _) = quic.0.open_bi().await.unwrap();
        write(SendStream::new(client_send)).await;
        let (_, server_recv) = quic.1.accept_bi().await.unwrap();
        (RecvStream::new(server_recv), quic)
    }

    async fn recv_header_only_stream() -> (RecvStream, QuicPair) {
        recv_stream_from_writer(|mut send| async move {
            send.send_header(headers()).await.unwrap();
            send.finish().unwrap();
        })
        .await
    }

    fn server_config() -> quinn::ServerConfig {
        let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
        let cert_chain = rustls_pemfile::certs(&mut &*cert_pem)
            .collect::<std::result::Result<Vec<_>, _>>()
            .unwrap();
        let key = rustls_pemfile::private_key(&mut &*key_pem)
            .unwrap()
            .unwrap();
        quinn::ServerConfig::with_single_cert(cert_chain, key).unwrap()
    }

    fn headers() -> http::HeaderMap {
        [(
            http::HeaderName::from_static("x-test"),
            "value".parse().unwrap(),
        )]
        .into_iter()
        .collect()
    }

    fn assert_protocol_violation<T>(result: Result<T, ProtocolError>, expected: &str) {
        let Err(ProtocolError::ProtocolViolation(message)) = result else {
            panic!("expected protocol violation")
        };
        assert!(
            message.contains(expected),
            "expected {expected:?}, got {message:?}"
        );
    }

    #[tokio::test]
    async fn send_stream_rejects_ordering_violations_and_finish_is_idempotent() {
        let quic = quic_pair().await;
        let (client_send, _) = quic.0.open_bi().await.unwrap();
        let mut send = SendStream::new(client_send);

        assert_protocol_violation(
            send.send_body(Bytes::from_static(b"early")).await,
            "must send headers before sending body",
        );
        assert_protocol_violation(
            send.send_trailer(headers()).await,
            "must send headers before sending trailer",
        );

        send.send_header(headers()).await.unwrap();
        assert_protocol_violation(send.send_header(headers()).await, "headers already sent");
        send.send_body(Bytes::from_static(b"body")).await.unwrap();
        send.finish().unwrap();
        send.finish().unwrap();
        assert_protocol_violation(
            send.send_trailer(headers()).await,
            "stream already finished",
        );
    }

    #[tokio::test]
    async fn recv_stream_rejects_ordering_violations() {
        let (mut recv, _quic) = recv_header_only_stream().await;

        assert_protocol_violation(
            recv.recv_body().await,
            "must call recv_header once before recv_body",
        );
        assert_protocol_violation(
            recv.recv_trailer().await,
            "must call recv_header once before recv_trailer",
        );

        recv.recv_header().await.unwrap();
        assert_protocol_violation(
            recv.recv_header().await,
            "recv_header called more than once",
        );
    }

    #[tokio::test]
    async fn recv_trailer_returns_buffered_trailer_after_body_read() {
        let (mut recv, _quic) = recv_stream_from_writer(|mut send| async move {
            let mut trailers = http::HeaderMap::new();
            trailers.insert("x-trailer", "done".parse().unwrap());
            send.send_header(headers()).await.unwrap();
            send.send_body(Bytes::from_static(b"body")).await.unwrap();
            send.send_trailer(trailers).await.unwrap();
            send.finish().unwrap();
        })
        .await;

        recv.recv_header().await.unwrap();
        assert_eq!(
            recv.recv_body().await.unwrap(),
            RecvBodyFrame::Body(Bytes::from_static(b"body"))
        );
        assert_eq!(
            recv.recv_body().await.unwrap(),
            RecvBodyFrame::TrailersReady
        );
        let trailers = recv.recv_trailer().await.unwrap().unwrap();
        assert_eq!(trailers.get("x-trailer").unwrap(), "done");
    }

    #[tokio::test]
    async fn recv_body_returns_explicit_end_on_clean_eof() {
        let (mut recv, _quic) = recv_header_only_stream().await;

        recv.recv_header().await.unwrap();
        assert_eq!(recv.recv_body().await.unwrap(), RecvBodyFrame::End);
    }

    #[tokio::test]
    async fn recv_trailer_returns_none_on_clean_eof() {
        let (mut recv, _quic) = recv_header_only_stream().await;

        recv.recv_header().await.unwrap();
        assert!(recv.recv_trailer().await.unwrap().is_none());
        assert!(recv.recv_trailer().await.unwrap().is_none());
    }

    #[tokio::test]
    async fn recv_trailer_rejects_body_before_trailers() {
        let (mut recv, _quic) = recv_stream_from_writer(|mut send| async move {
            send.send_header(headers()).await.unwrap();
            send.send_body(Bytes::from_static(b"body")).await.unwrap();
            send.finish().unwrap();
        })
        .await;

        recv.recv_header().await.unwrap();
        assert_protocol_violation(
            recv.recv_trailer().await,
            "expected trailer message, got body",
        );
    }

    #[tokio::test]
    async fn recv_trailer_rejects_frames_after_trailer() {
        let (mut recv, _quic) = recv_stream_from_writer(|mut send| async move {
            send.send_header(headers()).await.unwrap();
            send.send_trailer(headers()).await.unwrap();
            send.send_body(Bytes::from_static(b"late body"))
                .await
                .unwrap();
            send.finish().unwrap();
        })
        .await;

        recv.recv_header().await.unwrap();
        assert_protocol_violation(
            recv.recv_trailer().await,
            "expected none after trailer, got Body",
        );
    }
}
