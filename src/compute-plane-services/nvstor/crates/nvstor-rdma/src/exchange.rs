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

//! Out-of-band QP info exchange via TCP.
//!
//! Before RDMA data transfer can happen, both sides need to know each other's:
//!   - QP number
//!   - LID (Local Identifier, 0 for RoCE)
//!   - GID (Global Identifier, 16 bytes)
//!   - Memory region address and rkey (for one-sided ops)
//!
//! This module provides a simple TCP-based exchange protocol.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};

use crate::ffi;

/// Exchanged connection metadata.
#[derive(Debug, Clone, Copy)]
pub struct PeerInfo {
    pub qp_num: u32,
    pub lid: u16,
    pub gid: [u8; 16],
    /// Remote memory region base address.
    pub mr_addr: u64,
    /// Remote memory region rkey.
    pub mr_rkey: u32,
    /// Remote memory region length.
    pub mr_len: u64,
}

impl PeerInfo {
    /// Serialize to 42-byte wire format.
    /// Layout: qp_num(4) + lid(2) + gid(16) + mr_addr(8) + mr_rkey(4) + mr_len(8) = 42
    pub fn to_bytes(&self) -> Vec<u8> {
        let mut buf = Vec::with_capacity(42);
        buf.extend_from_slice(&self.qp_num.to_le_bytes());
        buf.extend_from_slice(&self.lid.to_le_bytes());
        buf.extend_from_slice(&self.gid);
        buf.extend_from_slice(&self.mr_addr.to_le_bytes());
        buf.extend_from_slice(&self.mr_rkey.to_le_bytes());
        buf.extend_from_slice(&self.mr_len.to_le_bytes());
        buf
    }

    /// Deserialize from 42-byte wire format.
    pub fn from_bytes(buf: &[u8]) -> Result<Self, String> {
        if buf.len() < 42 {
            return Err(format!("PeerInfo too short: {} bytes", buf.len()));
        }
        // SAFETY: all try_into below are infallible because buf.len() >= 42 (checked above).
        let qp_num = u32::from_le_bytes(buf[0..4].try_into().expect("invariant: len >= 42"));
        let lid = u16::from_le_bytes(buf[4..6].try_into().expect("invariant: len >= 42"));
        let mut gid = [0u8; 16];
        gid.copy_from_slice(&buf[6..22]);
        let mr_addr = u64::from_le_bytes(buf[22..30].try_into().expect("invariant: len >= 42"));
        let mr_rkey = u32::from_le_bytes(buf[30..34].try_into().expect("invariant: len >= 42"));
        let mr_len = u64::from_le_bytes(buf[34..42].try_into().expect("invariant: len >= 42"));
        Ok(Self {
            qp_num,
            lid,
            gid,
            mr_addr,
            mr_rkey,
            mr_len,
        })
    }

    /// Convert GID bytes to ibv_gid.
    pub fn to_ibv_gid(&self) -> ffi::ibv_gid {
        ffi::ibv_gid { raw: self.gid }
    }
}

/// Server side: listen on a port, accept one connection, exchange info.
pub fn exchange_server(port: u16, local: &PeerInfo) -> Result<PeerInfo, String> {
    let listener =
        TcpListener::bind(format!("0.0.0.0:{port}")).map_err(|e| format!("bind failed: {e}"))?;
    println!("Waiting for peer on port {port}...");

    let (mut stream, addr) = listener
        .accept()
        .map_err(|e| format!("accept failed: {e}"))?;
    println!("Peer connected from {addr}");

    // Send our info.
    let local_bytes = local.to_bytes();
    stream
        .write_all(&local_bytes)
        .map_err(|e| format!("send failed: {e}"))?;

    // Receive peer info.
    let mut remote_bytes = vec![0u8; 42];
    stream
        .read_exact(&mut remote_bytes)
        .map_err(|e| format!("recv failed: {e}"))?;

    PeerInfo::from_bytes(&remote_bytes)
}

/// Client side: connect to server, exchange info.
pub fn exchange_client(server_addr: &str, local: &PeerInfo) -> Result<PeerInfo, String> {
    let mut stream = TcpStream::connect(server_addr)
        .map_err(|e| format!("connect to {server_addr} failed: {e}"))?;
    println!("Connected to {server_addr}");

    // Receive server info first.
    let mut remote_bytes = vec![0u8; 42];
    stream
        .read_exact(&mut remote_bytes)
        .map_err(|e| format!("recv failed: {e}"))?;

    // Send our info.
    let local_bytes = local.to_bytes();
    stream
        .write_all(&local_bytes)
        .map_err(|e| format!("send failed: {e}"))?;

    PeerInfo::from_bytes(&remote_bytes)
}
