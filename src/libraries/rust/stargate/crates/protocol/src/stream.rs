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
    QuicBodyOrTrailer, QuicMessage, read_body_or_trailer_from_stream, read_from_stream,
    read_header_map_from_stream, write_body_to_stream, write_header_map_to_stream,
    write_trailer_map_to_stream,
};
use quinn::{StoppedError, VarInt};
use tracing::warn;

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
            return Err(ProtocolError::ProtocolViolation(
                "headers already sent".to_string(),
            ));
        }
        write_header_map_to_stream(&mut self.send, &header).await?;
        self.sent_headers = true;
        Ok(())
    }

    pub async fn send_body(&mut self, body: bytes::Bytes) -> Result<(), ProtocolError> {
        if !self.sent_headers {
            return Err(ProtocolError::ProtocolViolation(
                "must send headers before sending body".to_string(),
            ));
        }
        write_body_to_stream(&mut self.send, body).await
    }

    pub async fn send_trailer(&mut self, trailer: http::HeaderMap) -> Result<(), ProtocolError> {
        if !self.sent_headers {
            return Err(ProtocolError::ProtocolViolation(
                "must send headers before sending trailer".to_string(),
            ));
        }
        if self.eos {
            return Err(ProtocolError::ProtocolViolation(
                "stream already finished".to_string(),
            ));
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

    pub async fn recv_header(&mut self) -> Result<http::HeaderMap, ProtocolError> {
        if self.received_header {
            return Err(ProtocolError::ProtocolViolation(
                "recv_header called more than once".to_string(),
            ));
        }
        let header_map = match read_header_map_from_stream(self.recv.as_mut().ok_or_else(|| {
            ProtocolError::ProtocolViolation("stream already stopped or dropped".to_string())
        })?)
        .await?
        {
            Some(header_map) => header_map,
            None => {
                return Err(ProtocolError::ProtocolViolation(
                    "expected header message, got none".to_string(),
                ));
            }
        };
        self.received_header = true;
        Ok(header_map)
    }

    pub async fn recv_body(&mut self) -> Result<RecvBodyFrame, ProtocolError> {
        if !self.received_header {
            return Err(ProtocolError::ProtocolViolation(
                "must call recv_header once before recv_body".to_string(),
            ));
        }
        match read_body_or_trailer_from_stream(self.recv.as_mut().ok_or_else(|| {
            ProtocolError::ProtocolViolation("stream already stopped or dropped".to_string())
        })?)
        .await?
        {
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
            return Err(ProtocolError::ProtocolViolation(
                "must call recv_header once before recv_trailer".to_string(),
            ));
        }
        let trailer = match (self.received_trailer.take(), self.eos) {
            (Some(t), _) => Some(t),
            (None, true) => None,
            (None, false) => {
                match read_body_or_trailer_from_stream(self.recv.as_mut().ok_or_else(|| {
                    ProtocolError::ProtocolViolation(
                        "stream already stopped or dropped".to_string(),
                    )
                })?)
                .await?
                {
                    Some(QuicBodyOrTrailer::Trailer(trailer)) => Some(trailer),
                    None => {
                        self.eos = true;
                        None
                    }
                    Some(QuicBodyOrTrailer::Body(_)) => {
                        return Err(ProtocolError::ProtocolViolation(
                            "expected trailer message, got body".to_string(),
                        ));
                    }
                }
            }
        };

        if !self.eos {
            match self.recv_any().await? {
                None => (),
                Some(m) => {
                    return Err(ProtocolError::ProtocolViolation(format!(
                        "expected none after trailer, got {m}"
                    )));
                }
            }
        }

        Ok(trailer)
    }

    pub async fn recv_any(&mut self) -> Result<Option<QuicMessage>, ProtocolError> {
        let recv = self.recv.as_mut().ok_or_else(|| {
            ProtocolError::ProtocolViolation("stream already stopped or dropped".to_string())
        })?;
        let reader = match read_from_stream(recv).await? {
            Some(reader) => reader,
            None => {
                self.eos = true;
                return Ok(None);
            }
        };
        let message = QuicMessage::from_reader(reader)?;
        Ok(Some(message))
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
    use rustls::pki_types::CertificateDer;
    use std::future::Future;
    use std::sync::Arc;

    struct QuicPair {
        client_connection: quinn::Connection,
        server_connection: quinn::Connection,
        _client_endpoint: Endpoint,
        _server_endpoint: Endpoint,
    }

    struct SendOnlyStream {
        send: SendStream,
        _client_recv: quinn::RecvStream,
        _quic: QuicPair,
    }

    struct RecvOnlyStream {
        recv: RecvStream,
        writer: tokio::task::JoinHandle<()>,
        _server_send: quinn::SendStream,
        _client_recv: quinn::RecvStream,
        _quic: QuicPair,
    }

    async fn quic_pair() -> QuicPair {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let server_config = server_config();
        let server_endpoint =
            Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap()).unwrap();
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

        QuicPair {
            client_connection,
            server_connection,
            _client_endpoint: client_endpoint,
            _server_endpoint: server_endpoint,
        }
    }

    async fn send_only_stream() -> SendOnlyStream {
        let quic = quic_pair().await;
        let (client_send, client_recv) = quic.client_connection.open_bi().await.unwrap();
        SendOnlyStream {
            send: SendStream::new(client_send),
            _client_recv: client_recv,
            _quic: quic,
        }
    }

    async fn recv_stream_from_writer<F, Fut>(write: F) -> RecvOnlyStream
    where
        F: FnOnce(SendStream) -> Fut + Send + 'static,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let quic = quic_pair().await;
        let (client_send, client_recv) = quic.client_connection.open_bi().await.unwrap();
        let writer = tokio::spawn(write(SendStream::new(client_send)));
        let (server_send, server_recv) = quic.server_connection.accept_bi().await.unwrap();
        RecvOnlyStream {
            recv: RecvStream::new(server_recv),
            writer,
            _server_send: server_send,
            _client_recv: client_recv,
            _quic: quic,
        }
    }

    fn server_config() -> quinn::ServerConfig {
        let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
        let cert_chain: Vec<CertificateDer<'static>> = rustls_pemfile::certs(&mut &*cert_pem)
            .collect::<std::result::Result<_, _>>()
            .unwrap();
        let key = rustls_pemfile::private_key(&mut &*key_pem)
            .unwrap()
            .unwrap();
        let tls_config = rustls::ServerConfig::builder()
            .with_no_client_auth()
            .with_single_cert(cert_chain, key)
            .unwrap();
        quinn::ServerConfig::with_crypto(Arc::new(
            quinn::crypto::rustls::QuicServerConfig::try_from(tls_config).unwrap(),
        ))
    }

    fn headers() -> http::HeaderMap {
        let mut headers = http::HeaderMap::new();
        headers.insert("x-test", "value".parse().unwrap());
        headers
    }

    fn assert_protocol_violation<T>(result: Result<T, ProtocolError>, expected: &str) {
        match result {
            Err(ProtocolError::ProtocolViolation(message)) => {
                assert!(
                    message.contains(expected),
                    "expected violation containing {expected:?}, got {message:?}"
                );
            }
            Err(other) => panic!("expected protocol violation, got {other:?}"),
            Ok(_) => panic!("expected protocol violation, got success"),
        }
    }

    #[tokio::test]
    async fn send_stream_rejects_ordering_violations_and_finish_is_idempotent() {
        let mut pair = send_only_stream().await;

        assert_protocol_violation(
            pair.send.send_body(Bytes::from_static(b"early")).await,
            "must send headers before sending body",
        );
        assert_protocol_violation(
            pair.send.send_trailer(headers()).await,
            "must send headers before sending trailer",
        );

        pair.send.send_header(headers()).await.unwrap();
        assert_protocol_violation(
            pair.send.send_header(headers()).await,
            "headers already sent",
        );
        pair.send
            .send_body(Bytes::from_static(b"body"))
            .await
            .unwrap();
        pair.send.finish().unwrap();
        pair.send.finish().unwrap();
        assert_protocol_violation(
            pair.send.send_trailer(headers()).await,
            "stream already finished",
        );
    }

    #[tokio::test]
    async fn recv_stream_rejects_ordering_violations() {
        let mut pair = recv_stream_from_writer(|mut send| async move {
            send.send_header(headers()).await.unwrap();
            send.finish().unwrap();
        })
        .await;

        assert_protocol_violation(
            pair.recv.recv_body().await,
            "must call recv_header once before recv_body",
        );
        assert_protocol_violation(
            pair.recv.recv_trailer().await,
            "must call recv_header once before recv_trailer",
        );

        pair.recv.recv_header().await.unwrap();
        assert_protocol_violation(
            pair.recv.recv_header().await,
            "recv_header called more than once",
        );
        pair.writer.await.unwrap();
    }

    #[tokio::test]
    async fn recv_trailer_returns_buffered_trailer_after_body_read() {
        let mut pair = recv_stream_from_writer(|mut send| async move {
            let mut trailers = http::HeaderMap::new();
            trailers.insert("x-trailer", "done".parse().unwrap());
            send.send_header(headers()).await.unwrap();
            send.send_body(Bytes::from_static(b"body")).await.unwrap();
            send.send_trailer(trailers).await.unwrap();
            send.finish().unwrap();
        })
        .await;

        pair.recv.recv_header().await.unwrap();
        assert_eq!(
            pair.recv.recv_body().await.unwrap(),
            RecvBodyFrame::Body(Bytes::from_static(b"body"))
        );
        assert_eq!(
            pair.recv.recv_body().await.unwrap(),
            RecvBodyFrame::TrailersReady
        );
        let trailers = pair.recv.recv_trailer().await.unwrap().unwrap();
        assert_eq!(trailers.get("x-trailer").unwrap(), "done");
        pair.writer.await.unwrap();
    }

    #[tokio::test]
    async fn recv_body_returns_explicit_end_on_clean_eof() {
        let mut pair = recv_stream_from_writer(|mut send| async move {
            send.send_header(headers()).await.unwrap();
            send.finish().unwrap();
        })
        .await;

        pair.recv.recv_header().await.unwrap();
        assert_eq!(pair.recv.recv_body().await.unwrap(), RecvBodyFrame::End);
        pair.writer.await.unwrap();
    }

    #[tokio::test]
    async fn recv_trailer_returns_none_on_clean_eof() {
        let mut pair = recv_stream_from_writer(|mut send| async move {
            send.send_header(headers()).await.unwrap();
            send.finish().unwrap();
        })
        .await;

        pair.recv.recv_header().await.unwrap();
        assert!(pair.recv.recv_trailer().await.unwrap().is_none());
        assert!(pair.recv.recv_trailer().await.unwrap().is_none());
        pair.writer.await.unwrap();
    }

    #[tokio::test]
    async fn recv_trailer_rejects_body_before_trailers() {
        let mut pair = recv_stream_from_writer(|mut send| async move {
            send.send_header(headers()).await.unwrap();
            send.send_body(Bytes::from_static(b"body")).await.unwrap();
            send.finish().unwrap();
        })
        .await;

        pair.recv.recv_header().await.unwrap();
        assert_protocol_violation(
            pair.recv.recv_trailer().await,
            "expected trailer message, got body",
        );
        pair.writer.await.unwrap();
    }
}
