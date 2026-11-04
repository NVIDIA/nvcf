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

//! Raft network layer — TCP peer connections for RPC.
//!
//! One TCP connection is opened per RPC and closed when it completes.
//! Every exchange is bounded in wall-clock time (connect AND
//! request/response), so a peer that stalls after accepting the connection
//! cannot wedge elections, heartbeats, or metadata proposals.

use std::time::Duration;

use tokio::io::{BufReader, BufWriter};
use tokio::net::TcpStream;

use crate::rpc::{self, RpcError, RpcMessage};
use crate::types::*;

/// Default wall-clock bound on one complete RPC exchange (write + read),
/// separate from the connect timeout.
///
/// Must be short relative to the election timeout so a hung peer is
/// abandoned before it can prevent a quorum from making progress, but long
/// enough to absorb a normal AppendEntries carrying a batch of entries.
const DEFAULT_RPC_TIMEOUT: Duration = Duration::from_secs(2);

/// Manages connections to all Raft peers.
pub struct RaftNetwork {
    /// Our node ID.
    #[allow(dead_code)]
    node_id: NodeId,
    /// Peer configs.
    peers: Vec<PeerConfig>,
    /// Connect timeout.
    connect_timeout: Duration,
    /// Bound on one request/response exchange after connecting.
    rpc_timeout: Duration,
}

impl RaftNetwork {
    pub fn new(node_id: NodeId, peers: Vec<PeerConfig>) -> Self {
        Self {
            node_id,
            peers,
            connect_timeout: Duration::from_secs(2),
            rpc_timeout: DEFAULT_RPC_TIMEOUT,
        }
    }

    /// Override the per-RPC wall-clock bound (tests use a short one).
    pub fn with_rpc_timeout(mut self, timeout: Duration) -> Self {
        self.rpc_timeout = timeout;
        self
    }

    /// Send an RPC message to a specific peer (fire-and-forget).
    /// Opens a fresh connection. For request-response, use `send_recv`.
    pub async fn send(&self, peer_id: NodeId, msg: &RpcMessage) -> Result<(), RpcError> {
        let addr = self.peer_addr(peer_id).ok_or_else(|| {
            RpcError::Io(std::io::Error::new(
                std::io::ErrorKind::NotFound,
                format!("peer {peer_id} not found"),
            ))
        })?;

        let stream = tokio::time::timeout(self.connect_timeout, TcpStream::connect(&addr))
            .await
            .map_err(|_| {
                RpcError::Io(std::io::Error::new(
                    std::io::ErrorKind::TimedOut,
                    "connect timeout",
                ))
            })?
            .map_err(RpcError::Io)?;

        let (_rd, wr) = stream.into_split();
        let mut writer = BufWriter::new(wr);

        rpc::write_message(&mut writer, msg).await
    }

    /// Send an RPC and wait for a response.
    pub async fn send_recv(
        &self,
        peer_id: NodeId,
        msg: &RpcMessage,
    ) -> Result<RpcMessage, RpcError> {
        let addr = self.peer_addr(peer_id).ok_or_else(|| {
            RpcError::Io(std::io::Error::new(
                std::io::ErrorKind::NotFound,
                format!("peer {peer_id} not found"),
            ))
        })?;

        send_recv_addr(&addr, msg, self.connect_timeout, self.rpc_timeout).await
    }

    /// Send RequestVote to all peers, collect responses.
    pub async fn broadcast_request_vote(
        &self,
        rv: &RequestVote,
    ) -> Vec<(NodeId, Result<RequestVoteResp, RpcError>)> {
        let msg = RpcMessage::RequestVote(rv.clone());

        // Fan out concurrently. Sequential awaits meant one unreachable
        // peer burned its full timeout before the next was even asked, so a
        // round cost sum-of-timeouts instead of max — easily longer than the
        // election timeout, so the term expired and the node re-campaigned
        // without ever finishing a round. Spawning starts every RPC
        // immediately; awaiting the handles below just collects work that is
        // already running, so a round costs the SLOWEST peer, not the sum.
        let handles: Vec<_> = self
            .peers
            .iter()
            .map(|peer| {
                let peer_id = peer.node_id;
                let addr = peer.addr.clone();
                let msg = msg.clone();
                let connect_timeout = self.connect_timeout;
                let rpc_timeout = self.rpc_timeout;
                let task = tokio::spawn(async move {
                    send_recv_addr(&addr, &msg, connect_timeout, rpc_timeout).await
                });
                (peer_id, task)
            })
            .collect();

        let mut results = Vec::with_capacity(handles.len());
        for (peer_id, task) in handles {
            let mapped = match task.await {
                Ok(Ok(RpcMessage::RequestVoteResp(resp))) => Ok(resp),
                Ok(Ok(_)) => Err(RpcError::DeserializeFailed(
                    "unexpected response type".into(),
                )),
                Ok(Err(e)) => Err(e),
                // The task itself panicked or was cancelled — treat as an
                // unreachable peer rather than propagating the panic into
                // the election loop.
                Err(e) => Err(RpcError::Io(std::io::Error::other(format!(
                    "vote task failed: {e}"
                )))),
            };
            results.push((peer_id, mapped));
        }

        results
    }

    /// Send AppendEntries to a specific peer.
    pub async fn send_append_entries(
        &self,
        peer_id: NodeId,
        ae: &AppendEntries,
    ) -> Result<AppendEntriesResp, RpcError> {
        let msg = RpcMessage::AppendEntries(ae.clone());
        let resp = self.send_recv(peer_id, &msg).await?;

        match resp {
            RpcMessage::AppendEntriesResp(aer) => Ok(aer),
            _ => Err(RpcError::DeserializeFailed(
                "unexpected response type".into(),
            )),
        }
    }

    /// Get a peer's address.
    fn peer_addr(&self, peer_id: NodeId) -> Option<String> {
        self.peers
            .iter()
            .find(|p| p.node_id == peer_id)
            .map(|p| p.addr.clone())
    }

    // A per-peer connection cache used to live here (`connect_peer` /
    // `disconnect_peer` / `active_connections`), but nothing ever called it —
    // every RPC opened a fresh socket regardless. It was dead code that
    // implied pooling the transport did not do, so it is gone. If pooling is
    // added later it must handle concurrent in-flight RPCs per peer and
    // half-open sockets; the timeouts above are what bound liveness today.

    /// List of peer IDs.
    pub fn peer_ids(&self) -> Vec<NodeId> {
        self.peers.iter().map(|p| p.node_id).collect()
    }

    /// Total number of peers.
    pub fn peer_count(&self) -> usize {
        self.peers.len()
    }

    /// Quorum size (majority).
    pub fn quorum_size(&self) -> usize {
        (self.peers.len() + 1) / 2 + 1 // +1 for self
    }
}

/// Perform one request/response exchange against `addr`, fully bounded in
/// wall-clock time.
///
/// Free function (not a method) so callers can move owned arguments into a
/// spawned task and fan out concurrently without borrowing the network.
///
/// Two separate bounds, both required:
///   * `connect_timeout` — the peer is down / unroutable and the SYN never
///     completes.
///   * `rpc_timeout` — the peer accepted the connection and then stalled
///     (GC pause, frozen VM, half-open socket after a partition). Without
///     this the read blocks forever and wedges elections, heartbeats, and —
///     since metadata is routed through consensus — every metadata write.
async fn send_recv_addr(
    addr: &str,
    msg: &RpcMessage,
    connect_timeout: Duration,
    rpc_timeout: Duration,
) -> Result<RpcMessage, RpcError> {
    let stream = tokio::time::timeout(connect_timeout, TcpStream::connect(addr))
        .await
        .map_err(|_| {
            RpcError::Io(std::io::Error::new(
                std::io::ErrorKind::TimedOut,
                "connect timeout",
            ))
        })?
        .map_err(RpcError::Io)?;

    let (rd, wr) = stream.into_split();
    let mut reader = BufReader::new(rd);
    let mut writer = BufWriter::new(wr);

    tokio::time::timeout(rpc_timeout, async {
        rpc::write_message(&mut writer, msg).await?;
        rpc::read_message(&mut reader).await
    })
    .await
    .map_err(|_| {
        RpcError::Io(std::io::Error::new(
            std::io::ErrorKind::TimedOut,
            "raft rpc timeout",
        ))
    })?
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::net::TcpListener;

    /// Start a simple echo server that reads an RPC and sends back a response.
    async fn echo_raft_server(listener: TcpListener) {
        let (stream, _) = listener.accept().await.expect("accept");
        let (rd, wr) = stream.into_split();
        let mut reader = BufReader::new(rd);
        let mut writer = BufWriter::new(wr);

        let msg = rpc::read_message(&mut reader).await.expect("read");

        // Respond based on message type.
        let resp = match msg {
            RpcMessage::RequestVote(rv) => RpcMessage::RequestVoteResp(RequestVoteResp {
                term: rv.term,
                vote_granted: true,
            }),
            RpcMessage::AppendEntries(ae) => RpcMessage::AppendEntriesResp(AppendEntriesResp {
                term: ae.term,
                success: true,
                match_index: ae.prev_log_index + ae.entries.len() as u64,
            }),
            _ => return,
        };

        rpc::write_message(&mut writer, &resp).await.expect("write");
    }

    #[tokio::test]
    async fn send_recv_request_vote() {
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let addr = listener.local_addr().expect("addr");

        tokio::spawn(echo_raft_server(listener));

        let network = RaftNetwork::new(
            1,
            vec![PeerConfig {
                node_id: 2,
                addr: addr.to_string(),
            }],
        );

        let rv = RequestVote {
            term: 5,
            candidate_id: 1,
            last_log_index: 10,
            last_log_term: 4,
        };

        let msg = RpcMessage::RequestVote(rv);
        let resp = network.send_recv(2, &msg).await.expect("send_recv");

        match resp {
            RpcMessage::RequestVoteResp(rvr) => {
                assert_eq!(rvr.term, 5);
                assert!(rvr.vote_granted);
            }
            _ => panic!("wrong response type"),
        }
    }

    #[tokio::test]
    async fn send_recv_append_entries() {
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let addr = listener.local_addr().expect("addr");

        tokio::spawn(echo_raft_server(listener));

        let network = RaftNetwork::new(
            1,
            vec![PeerConfig {
                node_id: 2,
                addr: addr.to_string(),
            }],
        );

        let ae = AppendEntries {
            term: 3,
            leader_id: 1,
            prev_log_index: 5,
            prev_log_term: 2,
            leader_commit: 5,
            entries: vec![LogEntry {
                term: 3,
                index: 6,
                data: LogEntryData::Noop,
            }],
        };

        let resp = network.send_append_entries(2, &ae).await.expect("send");
        assert!(resp.success);
        assert_eq!(resp.match_index, 6);
    }

    /// Accepts the connection, reads the request, then never replies —
    /// the half-open / stalled-peer case (GC pause, frozen VM, partition).
    async fn stalled_raft_server(listener: TcpListener) {
        let (stream, _) = listener.accept().await.expect("accept");
        let (rd, _wr) = stream.into_split();
        let mut reader = BufReader::new(rd);
        let _ = rpc::read_message(&mut reader).await;
        // Hold the connection open forever without responding.
        std::future::pending::<()>().await;
    }

    // A peer that accepts and then stalls must not wedge the RPC. The
    // connect timeout does not cover this — only the request/response bound
    // does. Before the fix this call blocked forever.
    #[tokio::test]
    async fn stalled_peer_times_out_instead_of_hanging() {
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let addr = listener.local_addr().expect("addr");
        tokio::spawn(stalled_raft_server(listener));

        let network = RaftNetwork::new(
            1,
            vec![PeerConfig {
                node_id: 2,
                addr: addr.to_string(),
            }],
        )
        .with_rpc_timeout(Duration::from_millis(150));

        let msg = RpcMessage::RequestVote(RequestVote {
            term: 1,
            candidate_id: 1,
            last_log_index: 0,
            last_log_term: 0,
        });

        let started = std::time::Instant::now();
        let result = tokio::time::timeout(Duration::from_secs(5), network.send_recv(2, &msg))
            .await
            .expect("send_recv must return on its own, not hang until the outer bound");

        let err = result.expect_err("stalled peer must surface an error");
        assert_eq!(
            err_kind(&err),
            Some(std::io::ErrorKind::TimedOut),
            "expected a timeout error, got {err:?}"
        );
        assert!(
            started.elapsed() < Duration::from_secs(2),
            "should give up near the rpc_timeout, took {:?}",
            started.elapsed()
        );
    }

    fn err_kind(e: &RpcError) -> Option<std::io::ErrorKind> {
        match e {
            RpcError::Io(io) => Some(io.kind()),
            _ => None,
        }
    }

    // Vote fan-out must be concurrent. With three stalled peers and a
    // 150 ms bound, a sequential loop takes ~450 ms; concurrent fan-out
    // finishes in ~150 ms. Assert we're clearly under the sequential cost.
    #[tokio::test]
    async fn broadcast_request_vote_is_concurrent() {
        let mut peers = Vec::new();
        for id in 2..=4u64 {
            let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
            let addr = listener.local_addr().expect("addr");
            tokio::spawn(stalled_raft_server(listener));
            peers.push(PeerConfig {
                node_id: id,
                addr: addr.to_string(),
            });
        }

        let network = RaftNetwork::new(1, peers).with_rpc_timeout(Duration::from_millis(150));
        let rv = RequestVote {
            term: 1,
            candidate_id: 1,
            last_log_index: 0,
            last_log_term: 0,
        };

        let started = std::time::Instant::now();
        let results = network.broadcast_request_vote(&rv).await;
        let elapsed = started.elapsed();

        assert_eq!(results.len(), 3, "every peer must be reported");
        assert!(results.iter().all(|(_, r)| r.is_err()), "all peers stalled");
        assert!(
            elapsed < Duration::from_millis(400),
            "fan-out looks sequential: 3 peers x 150ms bound took {elapsed:?}"
        );
    }

    #[tokio::test]
    async fn peer_not_found() {
        let network = RaftNetwork::new(1, vec![]);
        let msg = RpcMessage::RequestVote(RequestVote {
            term: 1,
            candidate_id: 1,
            last_log_index: 0,
            last_log_term: 0,
        });

        let result = network.send_recv(99, &msg).await;
        assert!(result.is_err());
    }

    #[tokio::test]
    async fn connect_timeout() {
        // Non-routable address should timeout.
        let network = RaftNetwork::new(
            1,
            vec![PeerConfig {
                node_id: 2,
                addr: "192.0.2.1:9999".into(), // RFC 5737 TEST-NET
            }],
        );

        let msg = RpcMessage::RequestVote(RequestVote {
            term: 1,
            candidate_id: 1,
            last_log_index: 0,
            last_log_term: 0,
        });

        let result = network.send_recv(2, &msg).await;
        assert!(result.is_err());
    }

    #[test]
    fn quorum_size_3_nodes() {
        let network = RaftNetwork::new(
            1,
            vec![
                PeerConfig {
                    node_id: 2,
                    addr: "a:1".into(),
                },
                PeerConfig {
                    node_id: 3,
                    addr: "b:1".into(),
                },
            ],
        );
        // 3 nodes total (self + 2 peers), quorum = 2
        assert_eq!(network.quorum_size(), 2);
    }

    #[test]
    fn quorum_size_5_nodes() {
        let network = RaftNetwork::new(
            1,
            vec![
                PeerConfig {
                    node_id: 2,
                    addr: "a:1".into(),
                },
                PeerConfig {
                    node_id: 3,
                    addr: "b:1".into(),
                },
                PeerConfig {
                    node_id: 4,
                    addr: "c:1".into(),
                },
                PeerConfig {
                    node_id: 5,
                    addr: "d:1".into(),
                },
            ],
        );
        // 5 nodes total, quorum = 3
        assert_eq!(network.quorum_size(), 3);
    }
}
