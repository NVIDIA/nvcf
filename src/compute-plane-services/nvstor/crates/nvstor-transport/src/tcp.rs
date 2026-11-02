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

//! TCP transport — async connection with framed messaging.
//!
//! Each message is framed as: [length: u32 LE] [payload: bytes]
//! Connections track state: Healthy, Error, Closed.

use std::io;
use std::net::SocketAddr;
use std::time::Duration;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

/// Connection state.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ConnState {
    Healthy,
    Error,
    Closed,
}

/// TCP configuration.
#[derive(Debug, Clone)]
pub struct TcpConfig {
    /// Connect timeout.
    pub connect_timeout: Duration,
    /// Read/write timeout per operation.
    pub io_timeout: Duration,
    /// Maximum message size (default: 256 MB).
    pub max_message_size: u32,
    /// TCP nodelay (disable Nagle's algorithm).
    pub nodelay: bool,
}

impl Default for TcpConfig {
    fn default() -> Self {
        Self {
            connect_timeout: Duration::from_secs(5),
            io_timeout: Duration::from_secs(30),
            max_message_size: 256 * 1024 * 1024, // 256 MB
            nodelay: true,
        }
    }
}

/// A framed TCP connection for sending/receiving length-prefixed messages.
pub struct TcpConnection {
    stream: TcpStream,
    addr: SocketAddr,
    state: ConnState,
    config: TcpConfig,
}

impl TcpConnection {
    /// Connect to a remote address.
    pub async fn connect(addr: SocketAddr, config: TcpConfig) -> io::Result<Self> {
        let stream = tokio::time::timeout(config.connect_timeout, TcpStream::connect(addr))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "connect timeout"))??;

        if config.nodelay {
            stream.set_nodelay(true)?;
        }

        Ok(Self {
            stream,
            addr,
            state: ConnState::Healthy,
            config,
        })
    }

    /// Wrap an existing stream (for server-accepted connections).
    pub fn from_stream(stream: TcpStream, addr: SocketAddr, config: TcpConfig) -> Self {
        if config.nodelay {
            let _ = stream.set_nodelay(true);
        }
        Self {
            stream,
            addr,
            state: ConnState::Healthy,
            config,
        }
    }

    /// Send a length-prefixed message.
    pub async fn send(&mut self, payload: &[u8]) -> io::Result<()> {
        if self.state != ConnState::Healthy {
            return Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "connection not healthy",
            ));
        }

        if payload.len() > self.config.max_message_size as usize {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "message too large",
            ));
        }

        let mut header = [0u8; 4];
        header.copy_from_slice(&(payload.len() as u32).to_le_bytes());

        let result = tokio::time::timeout(self.config.io_timeout, async {
            self.stream.write_all(&header).await?;
            self.stream.write_all(payload).await?;
            self.stream.flush().await?;
            Ok::<_, io::Error>(())
        })
        .await;

        match result {
            Ok(Ok(())) => Ok(()),
            Ok(Err(e)) => {
                self.state = ConnState::Error;
                Err(e)
            }
            Err(_) => {
                self.state = ConnState::Error;
                Err(io::Error::new(io::ErrorKind::TimedOut, "send timeout"))
            }
        }
    }

    /// Receive a length-prefixed message.
    pub async fn recv(&mut self) -> io::Result<Vec<u8>> {
        if self.state != ConnState::Healthy {
            return Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "connection not healthy",
            ));
        }

        let result = tokio::time::timeout(self.config.io_timeout, async {
            // Read length header (4 bytes).
            let mut header = [0u8; 4];
            match self.stream.read_exact(&mut header).await {
                Ok(_) => {}
                Err(e) if e.kind() == io::ErrorKind::UnexpectedEof => {
                    self.state = ConnState::Closed;
                    return Err(io::Error::new(
                        io::ErrorKind::ConnectionReset,
                        "peer closed",
                    ));
                }
                Err(e) => return Err(e),
            }

            let len = u32::from_le_bytes(header) as usize;
            if len > self.config.max_message_size as usize {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "message too large",
                ));
            }

            // Read payload.
            let mut payload = vec![0u8; len];
            self.stream.read_exact(&mut payload).await?;
            Ok(payload)
        })
        .await;

        match result {
            Ok(Ok(data)) => Ok(data),
            Ok(Err(e)) => {
                if self.state == ConnState::Healthy {
                    self.state = ConnState::Error;
                }
                Err(e)
            }
            Err(_) => {
                self.state = ConnState::Error;
                Err(io::Error::new(io::ErrorKind::TimedOut, "recv timeout"))
            }
        }
    }

    /// Current connection state.
    pub fn state(&self) -> ConnState {
        self.state
    }

    /// Remote address.
    pub fn addr(&self) -> SocketAddr {
        self.addr
    }

    /// Check if the connection is usable.
    pub fn is_healthy(&self) -> bool {
        self.state == ConnState::Healthy
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::net::TcpListener;

    #[tokio::test]
    async fn test_send_recv_roundtrip() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = tokio::spawn(async move {
            let (stream, peer) = listener.accept().await.unwrap();
            let mut conn = TcpConnection::from_stream(stream, peer, TcpConfig::default());
            let msg = conn.recv().await.unwrap();
            conn.send(&msg).await.unwrap(); // Echo back
            msg
        });

        let mut client = TcpConnection::connect(addr, TcpConfig::default())
            .await
            .unwrap();
        assert!(client.is_healthy());

        let payload = b"hello nvstor transport";
        client.send(payload).await.unwrap();
        let response = client.recv().await.unwrap();
        assert_eq!(response, payload);

        let server_msg = server.await.unwrap();
        assert_eq!(server_msg, payload);
    }

    #[tokio::test]
    async fn test_large_message() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let data = vec![0xABu8; 1024 * 1024]; // 1 MB
        let data_clone = data.clone();

        let server = tokio::spawn(async move {
            let (stream, peer) = listener.accept().await.unwrap();
            let mut conn = TcpConnection::from_stream(stream, peer, TcpConfig::default());
            let msg = conn.recv().await.unwrap();
            assert_eq!(msg.len(), 1024 * 1024);
            conn.send(&msg).await.unwrap();
        });

        let mut client = TcpConnection::connect(addr, TcpConfig::default())
            .await
            .unwrap();
        client.send(&data_clone).await.unwrap();
        let response = client.recv().await.unwrap();
        assert_eq!(response, data_clone);

        server.await.unwrap();
    }

    #[tokio::test]
    async fn test_message_too_large() {
        let config = TcpConfig {
            max_message_size: 100,
            ..Default::default()
        };

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let _server = tokio::spawn(async move {
            let _ = listener.accept().await;
        });

        let mut client = TcpConnection::connect(addr, config).await.unwrap();
        let big_payload = vec![0u8; 200];
        let result = client.send(&big_payload).await;
        assert!(result.is_err());
    }

    #[tokio::test]
    async fn test_connect_timeout() {
        // Connect to a non-routable address to trigger timeout.
        let config = TcpConfig {
            connect_timeout: Duration::from_millis(100),
            ..Default::default()
        };

        let addr: SocketAddr = "192.0.2.1:1".parse().unwrap(); // RFC 5737 TEST-NET
        let result = TcpConnection::connect(addr, config).await;
        assert!(result.is_err());
    }

    #[tokio::test]
    async fn test_peer_disconnect_detection() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            drop(stream); // Close immediately
        });

        let mut client = TcpConnection::connect(addr, TcpConfig::default())
            .await
            .unwrap();
        // Give server time to close
        tokio::time::sleep(Duration::from_millis(50)).await;

        let result = client.recv().await;
        assert!(result.is_err());
        assert_ne!(client.state(), ConnState::Healthy);

        server.await.unwrap();
    }

    #[tokio::test]
    async fn test_multiple_messages() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = tokio::spawn(async move {
            let (stream, peer) = listener.accept().await.unwrap();
            let mut conn = TcpConnection::from_stream(stream, peer, TcpConfig::default());
            for _ in 0..100 {
                let msg = conn.recv().await.unwrap();
                conn.send(&msg).await.unwrap();
            }
        });

        let mut client = TcpConnection::connect(addr, TcpConfig::default())
            .await
            .unwrap();
        for i in 0u32..100 {
            let payload = i.to_le_bytes().to_vec();
            client.send(&payload).await.unwrap();
            let response = client.recv().await.unwrap();
            assert_eq!(response, payload);
        }

        server.await.unwrap();
    }
}
