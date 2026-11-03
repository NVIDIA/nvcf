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

//! Raft RPC wire format — encode/decode messages over TCP.
//!
//! Wire format:
//!   Header (12 bytes): magic(4) + msg_type(1) + reserved(3) + payload_len(4)
//!   Payload: bincode-serialized RPC message
//!
//! All multi-byte fields are little-endian (matching bincode).

use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

use crate::types::*;

/// Maximum RPC payload size (64 MB — large enough for batch AppendEntries).
const MAX_PAYLOAD: u32 = 64 * 1024 * 1024;

/// An RPC message (request or response).
#[derive(Debug, Clone)]
pub enum RpcMessage {
    RequestVote(RequestVote),
    RequestVoteResp(RequestVoteResp),
    AppendEntries(AppendEntries),
    AppendEntriesResp(AppendEntriesResp),
}

/// Error during RPC encoding/decoding.
#[derive(Debug)]
pub enum RpcError {
    Io(std::io::Error),
    InvalidMagic(u32),
    InvalidType(u8),
    PayloadTooLarge(u32),
    DeserializeFailed(String),
    ConnectionClosed,
}

impl std::fmt::Display for RpcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Io(e) => write!(f, "RPC I/O: {e}"),
            Self::InvalidMagic(m) => write!(f, "RPC bad magic: {m:#x}"),
            Self::InvalidType(t) => write!(f, "RPC unknown type: {t:#x}"),
            Self::PayloadTooLarge(n) => write!(f, "RPC payload too large: {n}"),
            Self::DeserializeFailed(s) => write!(f, "RPC deserialize: {s}"),
            Self::ConnectionClosed => write!(f, "RPC connection closed"),
        }
    }
}

impl std::error::Error for RpcError {}

impl From<std::io::Error> for RpcError {
    fn from(e: std::io::Error) -> Self {
        if e.kind() == std::io::ErrorKind::UnexpectedEof {
            Self::ConnectionClosed
        } else {
            Self::Io(e)
        }
    }
}

// ============================================================================
// Encode
// ============================================================================

/// Encode an RPC message to bytes (header + payload).
pub fn encode(msg: &RpcMessage) -> Result<Vec<u8>, RpcError> {
    let (msg_type, payload) = match msg {
        RpcMessage::RequestVote(m) => (
            RpcType::RequestVote as u8,
            bincode::serialize(m).map_err(|e| RpcError::DeserializeFailed(e.to_string()))?,
        ),
        RpcMessage::RequestVoteResp(m) => (
            RpcType::RequestVoteResp as u8,
            bincode::serialize(m).map_err(|e| RpcError::DeserializeFailed(e.to_string()))?,
        ),
        RpcMessage::AppendEntries(m) => (
            RpcType::AppendEntries as u8,
            bincode::serialize(m).map_err(|e| RpcError::DeserializeFailed(e.to_string()))?,
        ),
        RpcMessage::AppendEntriesResp(m) => (
            RpcType::AppendEntriesResp as u8,
            bincode::serialize(m).map_err(|e| RpcError::DeserializeFailed(e.to_string()))?,
        ),
    };

    let mut buf = Vec::with_capacity(RAFT_HEADER_SIZE + payload.len());

    // Header: magic(4) + type(1) + reserved(3) + payload_len(4)
    buf.extend_from_slice(&RAFT_MAGIC.to_le_bytes());
    buf.push(msg_type);
    buf.extend_from_slice(&[0u8; 3]); // reserved
    buf.extend_from_slice(&(payload.len() as u32).to_le_bytes());

    // Payload
    buf.extend_from_slice(&payload);

    Ok(buf)
}

/// Write an RPC message to an async writer.
pub async fn write_message<W: AsyncWrite + Unpin>(
    writer: &mut W,
    msg: &RpcMessage,
) -> Result<(), RpcError> {
    let data = encode(msg)?;
    writer.write_all(&data).await?;
    writer.flush().await?;
    Ok(())
}

// ============================================================================
// Decode
// ============================================================================

/// Read and decode one RPC message from an async reader.
pub async fn read_message<R: AsyncRead + Unpin>(reader: &mut R) -> Result<RpcMessage, RpcError> {
    // Read header (12 bytes).
    let mut header = [0u8; RAFT_HEADER_SIZE];
    match reader.read_exact(&mut header).await {
        Ok(_) => {}
        Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => {
            return Err(RpcError::ConnectionClosed);
        }
        Err(e) => return Err(RpcError::Io(e)),
    }

    // Parse header.
    let magic = u32::from_le_bytes(header[0..4].try_into().expect("4 bytes"));
    if magic != RAFT_MAGIC {
        return Err(RpcError::InvalidMagic(magic));
    }

    let msg_type = header[4];
    let payload_len = u32::from_le_bytes(header[8..12].try_into().expect("4 bytes"));

    if payload_len > MAX_PAYLOAD {
        return Err(RpcError::PayloadTooLarge(payload_len));
    }

    // Read payload.
    let mut payload = vec![0u8; payload_len as usize];
    if payload_len > 0 {
        reader.read_exact(&mut payload).await?;
    }

    // Decode based on message type.
    let rpc_type = RpcType::from_u8(msg_type).ok_or(RpcError::InvalidType(msg_type))?;

    match rpc_type {
        RpcType::RequestVote => {
            let msg: RequestVote = bincode::deserialize(&payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::RequestVote(msg))
        }
        RpcType::RequestVoteResp => {
            let msg: RequestVoteResp = bincode::deserialize(&payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::RequestVoteResp(msg))
        }
        RpcType::AppendEntries => {
            let msg: AppendEntries = bincode::deserialize(&payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::AppendEntries(msg))
        }
        RpcType::AppendEntriesResp => {
            let msg: AppendEntriesResp = bincode::deserialize(&payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::AppendEntriesResp(msg))
        }
    }
}

/// Decode an RPC message from a byte buffer (non-async, for testing).
pub fn decode(data: &[u8]) -> Result<RpcMessage, RpcError> {
    if data.len() < RAFT_HEADER_SIZE {
        return Err(RpcError::Io(std::io::Error::new(
            std::io::ErrorKind::UnexpectedEof,
            "header too short",
        )));
    }

    let magic = u32::from_le_bytes(data[0..4].try_into().expect("4 bytes"));
    if magic != RAFT_MAGIC {
        return Err(RpcError::InvalidMagic(magic));
    }

    let msg_type = data[4];
    let payload_len = u32::from_le_bytes(data[8..12].try_into().expect("4 bytes"));
    let payload = &data[RAFT_HEADER_SIZE..RAFT_HEADER_SIZE + payload_len as usize];

    let rpc_type = RpcType::from_u8(msg_type).ok_or(RpcError::InvalidType(msg_type))?;

    match rpc_type {
        RpcType::RequestVote => {
            let msg: RequestVote = bincode::deserialize(payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::RequestVote(msg))
        }
        RpcType::RequestVoteResp => {
            let msg: RequestVoteResp = bincode::deserialize(payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::RequestVoteResp(msg))
        }
        RpcType::AppendEntries => {
            let msg: AppendEntries = bincode::deserialize(payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::AppendEntries(msg))
        }
        RpcType::AppendEntriesResp => {
            let msg: AppendEntriesResp = bincode::deserialize(payload)
                .map_err(|e| RpcError::DeserializeFailed(e.to_string()))?;
            Ok(RpcMessage::AppendEntriesResp(msg))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::duplex;

    #[test]
    fn encode_decode_request_vote() {
        let msg = RpcMessage::RequestVote(RequestVote {
            term: 5,
            candidate_id: 2,
            last_log_index: 10,
            last_log_term: 4,
        });

        let encoded = encode(&msg).expect("encode");
        assert!(encoded.len() >= RAFT_HEADER_SIZE);

        // Verify header.
        let magic = u32::from_le_bytes(encoded[0..4].try_into().expect("ok"));
        assert_eq!(magic, RAFT_MAGIC);
        assert_eq!(encoded[4], RpcType::RequestVote as u8);

        let decoded = decode(&encoded).expect("decode");
        match decoded {
            RpcMessage::RequestVote(rv) => {
                assert_eq!(rv.term, 5);
                assert_eq!(rv.candidate_id, 2);
                assert_eq!(rv.last_log_index, 10);
                assert_eq!(rv.last_log_term, 4);
            }
            _ => panic!("wrong message type"),
        }
    }

    #[test]
    fn encode_decode_request_vote_resp() {
        let msg = RpcMessage::RequestVoteResp(RequestVoteResp {
            term: 5,
            vote_granted: true,
        });
        let encoded = encode(&msg).expect("encode");
        let decoded = decode(&encoded).expect("decode");
        match decoded {
            RpcMessage::RequestVoteResp(rv) => {
                assert_eq!(rv.term, 5);
                assert!(rv.vote_granted);
            }
            _ => panic!("wrong type"),
        }
    }

    #[test]
    fn encode_decode_append_entries_empty() {
        let msg = RpcMessage::AppendEntries(AppendEntries {
            term: 3,
            leader_id: 1,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![],
        });
        let encoded = encode(&msg).expect("encode");
        let decoded = decode(&encoded).expect("decode");
        match decoded {
            RpcMessage::AppendEntries(ae) => {
                assert_eq!(ae.term, 3);
                assert_eq!(ae.leader_id, 1);
                assert!(ae.entries.is_empty());
            }
            _ => panic!("wrong type"),
        }
    }

    #[test]
    fn encode_decode_append_entries_with_data() {
        let msg = RpcMessage::AppendEntries(AppendEntries {
            term: 5,
            leader_id: 1,
            prev_log_index: 9,
            prev_log_term: 4,
            leader_commit: 8,
            entries: vec![
                LogEntry {
                    term: 5,
                    index: 10,
                    data: LogEntryData::Noop,
                },
                LogEntry {
                    term: 5,
                    index: 11,
                    data: LogEntryData::BucketPut {
                        name: "test".into(),
                        owner: "user".into(),
                        region: "us".into(),
                        versioning: 1,
                    },
                },
            ],
        });

        let encoded = encode(&msg).expect("encode");
        let decoded = decode(&encoded).expect("decode");

        match decoded {
            RpcMessage::AppendEntries(ae) => {
                assert_eq!(ae.entries.len(), 2);
                assert_eq!(ae.entries[0].data, LogEntryData::Noop);
                match &ae.entries[1].data {
                    LogEntryData::BucketPut { name, .. } => assert_eq!(name, "test"),
                    _ => panic!("wrong entry type"),
                }
            }
            _ => panic!("wrong type"),
        }
    }

    #[test]
    fn encode_decode_append_entries_resp() {
        let msg = RpcMessage::AppendEntriesResp(AppendEntriesResp {
            term: 5,
            success: true,
            match_index: 11,
        });
        let encoded = encode(&msg).expect("encode");
        let decoded = decode(&encoded).expect("decode");
        match decoded {
            RpcMessage::AppendEntriesResp(aer) => {
                assert_eq!(aer.term, 5);
                assert!(aer.success);
                assert_eq!(aer.match_index, 11);
            }
            _ => panic!("wrong type"),
        }
    }

    #[test]
    fn invalid_magic_rejected() {
        let mut data = encode(&RpcMessage::RequestVoteResp(RequestVoteResp {
            term: 1,
            vote_granted: false,
        }))
        .expect("encode");

        // Corrupt magic.
        data[0] = 0xFF;
        let err = decode(&data).expect_err("should fail");
        assert!(matches!(err, RpcError::InvalidMagic(_)));
    }

    #[test]
    fn invalid_type_rejected() {
        let mut data = encode(&RpcMessage::RequestVoteResp(RequestVoteResp {
            term: 1,
            vote_granted: false,
        }))
        .expect("encode");

        // Corrupt type.
        data[4] = 0xFF;
        let err = decode(&data).expect_err("should fail");
        assert!(matches!(err, RpcError::InvalidType(0xFF)));
    }

    #[tokio::test]
    async fn async_write_read_roundtrip() {
        let (mut client, mut server) = duplex(65536);

        let msg = RpcMessage::AppendEntries(AppendEntries {
            term: 10,
            leader_id: 3,
            prev_log_index: 99,
            prev_log_term: 9,
            leader_commit: 95,
            entries: vec![LogEntry {
                term: 10,
                index: 100,
                data: LogEntryData::ManifestDelete {
                    object_id: "bucket/key".into(),
                },
            }],
        });

        let msg_clone = msg.clone();
        let write_task = tokio::spawn(async move {
            write_message(&mut client, &msg_clone).await.expect("write");
        });

        let received = read_message(&mut server).await.expect("read");
        write_task.await.expect("join");

        match received {
            RpcMessage::AppendEntries(ae) => {
                assert_eq!(ae.term, 10);
                assert_eq!(ae.leader_id, 3);
                assert_eq!(ae.entries.len(), 1);
            }
            _ => panic!("wrong type"),
        }
    }

    #[tokio::test]
    async fn connection_closed_detected() {
        let (client, mut server) = duplex(1024);
        drop(client);

        let err = read_message(&mut server).await.expect_err("should fail");
        assert!(matches!(err, RpcError::ConnectionClosed));
    }

    #[tokio::test]
    async fn multiple_messages_on_stream() {
        let (mut client, mut server) = duplex(65536);

        let write_task = tokio::spawn(async move {
            for i in 0..10u64 {
                let msg = RpcMessage::AppendEntriesResp(AppendEntriesResp {
                    term: i,
                    success: true,
                    match_index: i * 10,
                });
                write_message(&mut client, &msg).await.expect("write");
            }
        });

        for i in 0..10u64 {
            let msg = read_message(&mut server).await.expect("read");
            match msg {
                RpcMessage::AppendEntriesResp(aer) => {
                    assert_eq!(aer.term, i);
                    assert_eq!(aer.match_index, i * 10);
                }
                _ => panic!("wrong type at {i}"),
            }
        }

        write_task.await.expect("join");
    }
}
