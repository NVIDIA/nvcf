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

//! Raft RPC server — accepts incoming peer connections and dispatches RPCs.
//!
//! Runs on the Raft port (default 9102). Each peer connects via TCP.
//! The server reads RPC messages and dispatches to the RaftNode's
//! handle_request_vote / handle_append_entries methods.

use std::sync::Arc;

use tokio::io::{BufReader, BufWriter};
use tokio::net::{TcpListener, TcpStream};
use tokio::task::JoinSet;
use tracing::{debug, info, warn};

use crate::node::RaftNode;
use crate::rpc::{self, RpcError, RpcMessage};

/// Run the Raft RPC server. Accepts connections and dispatches RPCs.
///
/// Connection handlers are owned by this future: when it is dropped or its
/// task aborted, every open connection is aborted with it. A node that has
/// been shut down therefore stops answering RPCs (it cannot keep voting or
/// acknowledging entries over a connection accepted before the shutdown),
/// and it releases its `RaftNode` — and with it the Raft log's RocksDB lock —
/// so the same directory can be reopened.
pub async fn run(listener: TcpListener, node: Arc<RaftNode>) -> Result<(), std::io::Error> {
    info!(
        addr = %listener.local_addr()?,
        "raft RPC server listening"
    );

    let mut connections = JoinSet::new();
    loop {
        tokio::select! {
            accepted = listener.accept() => {
                let (stream, addr) = accepted?;
                debug!(%addr, "raft: peer connected");

                let node = node.clone();
                connections.spawn(async move {
                    if let Err(e) = handle_peer(stream, &node).await {
                        match e {
                            RpcError::ConnectionClosed => {
                                debug!(%addr, "raft: peer disconnected");
                            }
                            _ => {
                                warn!(%addr, error = %e, "raft: peer error");
                            }
                        }
                    }
                });
            }
            // Reap finished handlers so the set does not grow without bound.
            Some(_) = connections.join_next(), if !connections.is_empty() => {}
        }
    }
}

/// Handle a single peer connection — read RPCs, dispatch, send responses.
async fn handle_peer(stream: TcpStream, node: &RaftNode) -> Result<(), RpcError> {
    let (rd, wr) = stream.into_split();
    let mut reader = BufReader::new(rd);
    let mut writer = BufWriter::new(wr);

    loop {
        let msg = rpc::read_message(&mut reader).await?;

        let response = match msg {
            RpcMessage::RequestVote(rv) => {
                let resp = node.handle_request_vote(&rv);
                RpcMessage::RequestVoteResp(resp)
            }
            RpcMessage::AppendEntries(ae) => {
                let resp = node.handle_append_entries(&ae);
                RpcMessage::AppendEntriesResp(resp)
            }
            // Responses shouldn't arrive here (they're sent in response to our requests).
            other => {
                warn!(
                    "raft: unexpected message type: {:?}",
                    std::mem::discriminant(&other)
                );
                continue;
            }
        };

        rpc::write_message(&mut writer, &response).await?;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::log::RaftLog;
    use crate::network::RaftNetwork;
    use crate::types::*;

    fn make_node(node_id: NodeId) -> (tempfile::TempDir, Arc<RaftNode>) {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let log = RaftLog::open(dir.path()).expect("open");
        let network = Arc::new(RaftNetwork::new(node_id, vec![]));
        let config = RaftConfig {
            node_id,
            ..Default::default()
        };
        let node = Arc::new(RaftNode::new(
            config,
            log,
            network,
            Box::new(crate::state_machine::NullStateMachine),
        ));
        (dir, node)
    }

    #[tokio::test]
    async fn server_handles_request_vote() {
        let (_dir, node) = make_node(1);

        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let addr = listener.local_addr().expect("addr");

        let node_clone = node.clone();
        tokio::spawn(async move {
            run(listener, node_clone).await.expect("run");
        });

        // Connect as a peer and send RequestVote.
        let stream = TcpStream::connect(addr).await.expect("connect");
        let (rd, wr) = stream.into_split();
        let mut reader = BufReader::new(rd);
        let mut writer = BufWriter::new(wr);

        let rv = RpcMessage::RequestVote(RequestVote {
            term: 1,
            candidate_id: 2,
            last_log_index: 0,
            last_log_term: 0,
        });

        rpc::write_message(&mut writer, &rv).await.expect("send");
        let resp = rpc::read_message(&mut reader).await.expect("recv");

        match resp {
            RpcMessage::RequestVoteResp(rvr) => {
                assert!(rvr.vote_granted);
                assert_eq!(rvr.term, 1);
            }
            _ => panic!("wrong response type"),
        }
    }

    #[tokio::test]
    async fn server_handles_append_entries() {
        let (_dir, node) = make_node(1);

        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let addr = listener.local_addr().expect("addr");

        let node_clone = node.clone();
        tokio::spawn(async move {
            run(listener, node_clone).await.expect("run");
        });

        let stream = TcpStream::connect(addr).await.expect("connect");
        let (rd, wr) = stream.into_split();
        let mut reader = BufReader::new(rd);
        let mut writer = BufWriter::new(wr);

        let ae = RpcMessage::AppendEntries(AppendEntries {
            term: 1,
            leader_id: 2,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![LogEntry {
                term: 1,
                index: 1,
                data: LogEntryData::Noop,
            }],
        });

        rpc::write_message(&mut writer, &ae).await.expect("send");
        let resp = rpc::read_message(&mut reader).await.expect("recv");

        match resp {
            RpcMessage::AppendEntriesResp(aer) => {
                assert!(aer.success);
                assert_eq!(aer.match_index, 1);
            }
            _ => panic!("wrong response type"),
        }

        // Verify the entry was actually appended.
        assert_eq!(node.last_log_index(), 1);
    }

    #[tokio::test]
    async fn server_handles_multiple_rpcs() {
        let (_dir, node) = make_node(1);

        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let addr = listener.local_addr().expect("addr");

        let node_clone = node.clone();
        tokio::spawn(async move {
            run(listener, node_clone).await.expect("run");
        });

        let stream = TcpStream::connect(addr).await.expect("connect");
        let (rd, wr) = stream.into_split();
        let mut reader = BufReader::new(rd);
        let mut writer = BufWriter::new(wr);

        // Send 5 AppendEntries with incrementing entries.
        for i in 1..=5u64 {
            let ae = RpcMessage::AppendEntries(AppendEntries {
                term: 1,
                leader_id: 2,
                prev_log_index: i - 1,
                prev_log_term: if i > 1 { 1 } else { 0 },
                leader_commit: 0,
                entries: vec![LogEntry {
                    term: 1,
                    index: i,
                    data: LogEntryData::BucketPut {
                        name: format!("bucket-{i}"),
                        owner: "test".into(),
                        region: "us".into(),
                        versioning: 0,
                    },
                }],
            });

            rpc::write_message(&mut writer, &ae).await.expect("send");
            let resp = rpc::read_message(&mut reader).await.expect("recv");

            match resp {
                RpcMessage::AppendEntriesResp(aer) => {
                    assert!(aer.success);
                    assert_eq!(aer.match_index, i);
                }
                _ => panic!("wrong type"),
            }
        }

        assert_eq!(node.last_log_index(), 5);
    }

    /// Aborting the server closes connections it already accepted and
    /// releases the node. Before the fix, accepted connections were detached
    /// tasks that kept serving RPCs (and held the Raft log open) after the
    /// server task was gone.
    #[tokio::test]
    async fn aborting_server_closes_accepted_connections() {
        let (_dir, node) = make_node(1);

        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let addr = listener.local_addr().expect("addr");
        let server = tokio::spawn(run(listener, node.clone()));

        let stream = TcpStream::connect(addr).await.expect("connect");
        let (rd, wr) = stream.into_split();
        let mut reader = BufReader::new(rd);
        let mut writer = BufWriter::new(wr);

        // One round trip proves the connection is being served.
        let rv = RpcMessage::RequestVote(RequestVote {
            term: 1,
            candidate_id: 2,
            last_log_index: 0,
            last_log_term: 0,
        });
        rpc::write_message(&mut writer, &rv).await.expect("send");
        rpc::read_message(&mut reader).await.expect("recv");

        server.abort();
        let _ = server.await;

        // The open connection is closed, not served.
        let next = tokio::time::timeout(
            std::time::Duration::from_secs(5),
            rpc::read_message(&mut reader),
        )
        .await
        .expect("connection must close, not hang");
        assert!(next.is_err(), "server answered after it was aborted");

        // Nothing but this test holds the node any more.
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            while Arc::strong_count(&node) > 1 {
                tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            }
        })
        .await
        .expect("aborted server released the node");
    }
}
