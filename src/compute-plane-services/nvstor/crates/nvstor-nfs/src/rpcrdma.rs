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

//! RPC-over-RDMA v1 framing per RFC 8166.
//!
//! Inline compatibility helpers and bounded chunked RPC transport framing.

pub mod chunks;
pub mod nfs;

use crate::xdr::{XdrReader, XdrWriter};

pub const RPCRDMA_VERSION_1: u32 = 1;

pub const RDMA_MSG: u32 = 0;
pub const RDMA_NOMSG: u32 = 1;
pub const RDMA_MSGP: u32 = 2;
pub const RDMA_DONE: u32 = 3;
pub const RDMA_ERROR: u32 = 4;

pub const ERR_VERS: u32 = 1;
pub const ERR_CHUNK: u32 = 2;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RpcRdmaMessage {
    pub xid: u32,
    pub version: u32,
    pub credits: u32,
    pub rpc_payload: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RpcRdmaError {
    VersionMismatch { got: u32 },
    UnsupportedProcedure { proc: u32 },
    ChunkListsNotSupported,
    InvalidHeader(String),
}

impl std::fmt::Display for RpcRdmaError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::VersionMismatch { got } => {
                write!(
                    f,
                    "RPC-over-RDMA version {got}, expected {RPCRDMA_VERSION_1}"
                )
            }
            Self::UnsupportedProcedure { proc } => {
                write!(f, "unsupported RPC-over-RDMA procedure {proc}")
            }
            Self::ChunkListsNotSupported => {
                write!(f, "chunked RPC-over-RDMA messages are not supported yet")
            }
            Self::InvalidHeader(msg) => write!(f, "invalid RPC-over-RDMA header: {msg}"),
        }
    }
}

impl std::error::Error for RpcRdmaError {}

fn skip_rdma_segment(r: &mut XdrReader<'_>) -> Result<(), RpcRdmaError> {
    r.get_u32()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
    r.get_u32()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
    r.get_u64()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
    Ok(())
}

fn skip_read_list(r: &mut XdrReader<'_>) -> Result<bool, RpcRdmaError> {
    let mut present = false;
    loop {
        let has_entry = r
            .get_bool()
            .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
        if !has_entry {
            return Ok(present);
        }
        present = true;
        r.get_u32()
            .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
        skip_rdma_segment(r)?;
    }
}

fn skip_write_chunk(r: &mut XdrReader<'_>) -> Result<(), RpcRdmaError> {
    let count = r
        .get_u32()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))? as usize;
    for _ in 0..count {
        skip_rdma_segment(r)?;
    }
    Ok(())
}

fn skip_write_list(r: &mut XdrReader<'_>) -> Result<bool, RpcRdmaError> {
    let mut present = false;
    loop {
        let has_entry = r
            .get_bool()
            .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
        if !has_entry {
            return Ok(present);
        }
        present = true;
        skip_write_chunk(r)?;
    }
}

fn skip_reply_chunk(r: &mut XdrReader<'_>) -> Result<bool, RpcRdmaError> {
    let has_entry = r
        .get_bool()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
    if has_entry {
        skip_write_chunk(r)?;
    }
    Ok(has_entry)
}

pub fn parse_inline_message(data: &[u8]) -> Result<RpcRdmaMessage, RpcRdmaError> {
    let mut r = XdrReader::new(data);
    let xid = r
        .get_u32()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
    let version = r
        .get_u32()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
    if version != RPCRDMA_VERSION_1 {
        return Err(RpcRdmaError::VersionMismatch { got: version });
    }
    let credits = r
        .get_u32()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;
    let proc = r
        .get_u32()
        .map_err(|e| RpcRdmaError::InvalidHeader(e.to_string()))?;

    match proc {
        RDMA_MSG => {
            let has_reads = skip_read_list(&mut r)?;
            let has_writes = skip_write_list(&mut r)?;
            let has_reply = skip_reply_chunk(&mut r)?;
            if has_reads || has_writes || has_reply {
                return Err(RpcRdmaError::ChunkListsNotSupported);
            }

            let payload = r.remaining_bytes().to_vec();
            if payload.len() < 4 {
                return Err(RpcRdmaError::InvalidHeader(
                    "inline RPC payload is truncated".to_string(),
                ));
            }
            let payload_xid = u32::from_be_bytes(payload[0..4].try_into().expect("xid slice"));
            if payload_xid != xid {
                return Err(RpcRdmaError::InvalidHeader(format!(
                    "rdma_xid {xid} does not match rpc xid {payload_xid}"
                )));
            }

            Ok(RpcRdmaMessage {
                xid,
                version,
                credits,
                rpc_payload: payload,
            })
        }
        RDMA_NOMSG => Err(RpcRdmaError::ChunkListsNotSupported),
        RDMA_MSGP | RDMA_DONE => Err(RpcRdmaError::UnsupportedProcedure { proc }),
        RDMA_ERROR => Err(RpcRdmaError::InvalidHeader(
            "received RDMA_ERROR from requester".to_string(),
        )),
        _ => Err(RpcRdmaError::UnsupportedProcedure { proc }),
    }
}

pub fn build_inline_reply(xid: u32, credits: u32, rpc_reply: &[u8]) -> Vec<u8> {
    let mut w = XdrWriter::with_capacity(32 + rpc_reply.len());
    w.put_u32(xid);
    w.put_u32(RPCRDMA_VERSION_1);
    w.put_u32(credits);
    w.put_u32(RDMA_MSG);
    w.put_bool(false); // no read list
    w.put_bool(false); // no write list
    w.put_bool(false); // no reply chunk
    w.put_raw(rpc_reply);
    w.into_bytes()
}

pub fn build_error_reply(xid: u32, credits: u32, err: &RpcRdmaError) -> Vec<u8> {
    let mut w = XdrWriter::with_capacity(32);
    w.put_u32(xid);
    w.put_u32(RPCRDMA_VERSION_1);
    w.put_u32(credits);
    w.put_u32(RDMA_ERROR);
    match err {
        RpcRdmaError::VersionMismatch { .. } => {
            w.put_u32(ERR_VERS);
            w.put_u32(RPCRDMA_VERSION_1);
            w.put_u32(RPCRDMA_VERSION_1);
        }
        _ => {
            w.put_u32(ERR_CHUNK);
        }
    }
    w.into_bytes()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpc;
    use crate::xdr::XdrWriter;

    fn sample_rpc_call(xid: u32) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_u32(xid);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(rpc::NFS_PROGRAM);
        w.put_u32(rpc::NFS_V4);
        w.put_u32(0);
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.into_bytes()
    }

    #[test]
    fn parse_inline_message_roundtrip() {
        let rpc_payload = sample_rpc_call(0x11223344);
        let encoded = build_inline_reply(0x11223344, 8, &rpc_payload);
        let parsed = parse_inline_message(&encoded).expect("inline message");
        assert_eq!(parsed.xid, 0x11223344);
        assert_eq!(parsed.version, RPCRDMA_VERSION_1);
        assert_eq!(parsed.credits, 8);
        assert_eq!(parsed.rpc_payload, rpc_payload);
    }

    #[test]
    fn parse_inline_message_rejects_chunks() {
        let rpc_payload = sample_rpc_call(7);
        let mut w = XdrWriter::new();
        w.put_u32(7);
        w.put_u32(RPCRDMA_VERSION_1);
        w.put_u32(4);
        w.put_u32(RDMA_MSG);
        w.put_bool(false); // no read list
        w.put_bool(true); // write list present
        w.put_u32(1); // one segment in write chunk
        w.put_u32(1);
        w.put_u32(4096);
        w.put_u64(8192);
        w.put_bool(false); // end of write list
        w.put_bool(false); // no reply chunk
        w.put_raw(&rpc_payload);

        let err = parse_inline_message(&w.into_bytes()).unwrap_err();
        assert_eq!(err, RpcRdmaError::ChunkListsNotSupported);
    }

    #[test]
    fn parse_inline_message_rejects_xid_mismatch() {
        let mut payload = sample_rpc_call(1);
        payload[0..4].copy_from_slice(&2u32.to_be_bytes());
        let encoded = build_inline_reply(1, 4, &payload);
        let err = parse_inline_message(&encoded).unwrap_err();
        assert!(matches!(err, RpcRdmaError::InvalidHeader(_)));
    }

    #[test]
    fn build_error_reply_version_mismatch() {
        let reply = build_error_reply(9, 3, &RpcRdmaError::VersionMismatch { got: 99 });
        let mut r = XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), 9);
        assert_eq!(r.get_u32().unwrap(), RPCRDMA_VERSION_1);
        assert_eq!(r.get_u32().unwrap(), 3);
        assert_eq!(r.get_u32().unwrap(), RDMA_ERROR);
        assert_eq!(r.get_u32().unwrap(), ERR_VERS);
        assert_eq!(r.get_u32().unwrap(), RPCRDMA_VERSION_1);
        assert_eq!(r.get_u32().unwrap(), RPCRDMA_VERSION_1);
    }
}
