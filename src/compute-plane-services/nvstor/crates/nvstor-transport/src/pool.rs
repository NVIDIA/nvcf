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

//! Connection pool with per-peer pooling, backpressure, and auto-reconnect.
//!
//! Backpressure:
//! - max_in_flight permits per connection
//! - Backpressure timeout on permit acquisition
//!
//! Reconnect:
//! - Remove connections on fatal error
//! - Subsequent acquire creates fresh connections

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use parking_lot::Mutex;
use tokio::sync::Semaphore;

use crate::tcp::{TcpConfig, TcpConnection};

/// Pool configuration.
#[derive(Debug, Clone)]
pub struct PoolConfig {
    /// TCP configuration for new connections.
    pub tcp: TcpConfig,
    /// Maximum connections per peer.
    pub max_connections_per_peer: usize,
    /// Maximum in-flight operations per connection (backpressure).
    pub max_in_flight: usize,
    /// Timeout for acquiring a backpressure permit.
    pub backpressure_timeout: Duration,
}

impl Default for PoolConfig {
    fn default() -> Self {
        Self {
            tcp: TcpConfig::default(),
            max_connections_per_peer: 4,
            max_in_flight: 100,
            backpressure_timeout: Duration::from_secs(5),
        }
    }
}

/// Per-peer connection state.
struct PeerState {
    connections: Vec<TcpConnection>,
    semaphore: Arc<Semaphore>,
    next_idx: usize,
}

/// Connection pool for inter-node communication.
///
/// Manages a pool of TCP connections per peer address.
/// Connections are created on demand and removed on error.
pub struct ConnectionPool {
    config: PoolConfig,
    peers: Mutex<HashMap<SocketAddr, PeerState>>,
}

impl std::fmt::Debug for ConnectionPool {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ConnectionPool")
            .field("config", &self.config)
            .field("peers", &format!("{} peers", self.peers.lock().len()))
            .finish()
    }
}

impl ConnectionPool {
    /// Create a new connection pool.
    pub fn new(config: PoolConfig) -> Self {
        Self {
            config,
            peers: Mutex::new(HashMap::new()),
        }
    }

    /// Send a message to a peer, acquiring a backpressure permit first.
    ///
    /// Creates a connection if none exist for this peer.
    /// Retries with a new connection if the first attempt fails.
    pub async fn send(&self, addr: SocketAddr, payload: &[u8]) -> std::io::Result<Vec<u8>> {
        // Acquire backpressure permit.
        let semaphore = self.get_or_create_semaphore(addr);
        let _permit = tokio::time::timeout(self.config.backpressure_timeout, semaphore.acquire())
            .await
            .map_err(|_| std::io::Error::new(std::io::ErrorKind::TimedOut, "backpressure timeout"))?
            .map_err(|_| std::io::Error::new(std::io::ErrorKind::Other, "semaphore closed"))?;

        // Try existing connection.
        if let Some(mut conn) = self.take_connection(addr) {
            match conn.send(payload).await {
                Ok(()) => {
                    let response = conn.recv().await?;
                    self.return_connection(addr, conn);
                    return Ok(response);
                }
                Err(_) => {
                    // Connection failed — drop it and try a new one.
                    tracing::debug!(%addr, "connection failed, creating new");
                }
            }
        }

        // Create new connection.
        let mut conn = TcpConnection::connect(addr, self.config.tcp.clone()).await?;
        conn.send(payload).await?;
        let response = conn.recv().await?;
        self.return_connection(addr, conn);
        Ok(response)
    }

    /// Get the total number of active connections across all peers.
    pub fn active_connections(&self) -> usize {
        self.peers
            .lock()
            .values()
            .map(|p| p.connections.len())
            .sum()
    }

    /// Remove all connections to a specific peer.
    pub fn remove_peer(&self, addr: &SocketAddr) {
        self.peers.lock().remove(addr);
    }

    fn get_or_create_semaphore(&self, addr: SocketAddr) -> Arc<Semaphore> {
        let mut peers = self.peers.lock();
        peers
            .entry(addr)
            .or_insert_with(|| PeerState {
                connections: Vec::new(),
                semaphore: Arc::new(Semaphore::new(self.config.max_in_flight)),
                next_idx: 0,
            })
            .semaphore
            .clone()
    }

    fn take_connection(&self, addr: SocketAddr) -> Option<TcpConnection> {
        let mut peers = self.peers.lock();
        let state = peers.get_mut(&addr)?;

        // Remove unhealthy connections.
        state.connections.retain(|c| c.is_healthy());

        if state.connections.is_empty() {
            return None;
        }

        // Round-robin selection.
        let idx = state.next_idx % state.connections.len();
        state.next_idx = state.next_idx.wrapping_add(1);
        Some(state.connections.swap_remove(idx))
    }

    fn return_connection(&self, addr: SocketAddr, conn: TcpConnection) {
        if !conn.is_healthy() {
            return; // Don't return broken connections to pool.
        }

        let mut peers = self.peers.lock();
        let state = peers.entry(addr).or_insert_with(|| PeerState {
            connections: Vec::new(),
            semaphore: Arc::new(Semaphore::new(self.config.max_in_flight)),
            next_idx: 0,
        });

        if state.connections.len() < self.config.max_connections_per_peer {
            state.connections.push(conn);
        }
        // If at capacity, drop the connection.
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    /// Start an echo server that reads length-prefixed messages and echoes them back.
    async fn echo_server(listener: TcpListener) {
        loop {
            let (mut stream, _) = listener.accept().await.unwrap();
            tokio::spawn(async move {
                loop {
                    let mut header = [0u8; 4];
                    if stream.read_exact(&mut header).await.is_err() {
                        break;
                    }
                    let len = u32::from_le_bytes(header) as usize;
                    let mut payload = vec![0u8; len];
                    if stream.read_exact(&mut payload).await.is_err() {
                        break;
                    }
                    // Echo: send back same payload with length prefix.
                    let _ = stream.write_all(&header).await;
                    let _ = stream.write_all(&payload).await;
                    let _ = stream.flush().await;
                }
            });
        }
    }

    #[tokio::test]
    async fn test_pool_send_recv() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(echo_server(listener));

        let pool = ConnectionPool::new(PoolConfig::default());
        let response = pool.send(addr, b"hello pool").await.unwrap();
        assert_eq!(response, b"hello pool");
    }

    #[tokio::test]
    async fn test_pool_multiple_messages() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(echo_server(listener));

        let pool = ConnectionPool::new(PoolConfig::default());
        for i in 0u32..20 {
            let payload = format!("message {i}");
            let response = pool.send(addr, payload.as_bytes()).await.unwrap();
            assert_eq!(response, payload.as_bytes());
        }
    }

    #[tokio::test]
    async fn test_pool_connection_reuse() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(echo_server(listener));

        let pool = ConnectionPool::new(PoolConfig::default());

        // Send first message — creates connection.
        pool.send(addr, b"first").await.unwrap();
        assert_eq!(pool.active_connections(), 1);

        // Send second message — reuses connection.
        pool.send(addr, b"second").await.unwrap();
        assert_eq!(pool.active_connections(), 1);
    }

    #[tokio::test]
    async fn test_pool_remove_peer() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(echo_server(listener));

        let pool = ConnectionPool::new(PoolConfig::default());
        pool.send(addr, b"data").await.unwrap();
        assert_eq!(pool.active_connections(), 1);

        pool.remove_peer(&addr);
        assert_eq!(pool.active_connections(), 0);
    }

    #[tokio::test]
    async fn test_pool_connect_failure() {
        let pool = ConnectionPool::new(PoolConfig {
            tcp: TcpConfig {
                connect_timeout: Duration::from_millis(100),
                ..Default::default()
            },
            ..Default::default()
        });

        // Connect to non-routable address.
        let addr: SocketAddr = "192.0.2.1:1".parse().unwrap();
        let result = pool.send(addr, b"data").await;
        assert!(result.is_err());
    }
}
