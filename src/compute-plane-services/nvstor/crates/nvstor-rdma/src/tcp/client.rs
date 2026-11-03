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

//! `S3TcpClient` — same API surface as `S3RdmaClient`. PUT streams
//! payload inline with the request frame. GET is ranged: each request
//! names an offset and a length, and the response header (size,
//! generation, CRC-32C) is followed by that range's bytes on the same
//! socket, read straight into the caller's buffer.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

pub use crate::object::{GetError, ObjectRead, ANY_GENERATION};
use crate::tcp::proto::{
    Request, Response, MAX_GET_RANGE, STATUS_GENERATION_CHANGED, STATUS_NOT_FOUND,
};
use crate::tcp::transport::TcpTransport;

/// Payload chunk `put_from_reader` reads and sends per write.
const PUT_CHUNK: usize = 8 * 1024 * 1024;

pub struct S3TcpClient {
    transport: Arc<TcpTransport>,
    peer_id: u64,
    next_req_id: AtomicU64,
}

impl S3TcpClient {
    pub fn new(transport: Arc<TcpTransport>, peer_id: u64) -> Self {
        Self {
            transport,
            peer_id,
            next_req_id: AtomicU64::new(1),
        }
    }

    fn req_id(&self) -> u64 {
        self.next_req_id.fetch_add(1, Ordering::Relaxed)
    }

    /// PUT `data` as one object. The server streams it into storage and
    /// returns the CRC-32C of what it received; a mismatch is an error.
    pub fn put(&self, key: &str, data: &[u8]) -> Result<u64, String> {
        let mut src = data;
        self.put_from_reader(key, data.len() as u64, &mut src)
    }

    /// PUT `length` bytes read from `src` (a file, for seeding a shard
    /// that doesn't fit in memory) in bounded chunks. The CRC-32C of the
    /// bytes sent is checked against the server's.
    pub fn put_from_reader(
        &self,
        key: &str,
        length: u64,
        src: &mut dyn std::io::Read,
    ) -> Result<u64, String> {
        // Hold the per-peer txn lock for the whole exchange so
        // concurrent workers on the same peer don't interleave
        // header + payload on the wire (would mis-frame the
        // server's next read).
        let txn = self
            .transport
            .txn_arc(self.peer_id)
            .map_err(|e| format!("PUT txn_arc: {e}"))?;
        let _guard = txn.lock();

        let req_id = self.req_id();
        let req = Request::Put {
            req_id,
            key: key.to_string(),
            length,
        };
        self.transport
            .send_request(self.peer_id, &req)
            .map_err(|e| format!("PUT send_request: {e}"))?;
        // Payload follows the request frame inline. A short or failed
        // source leaves the server waiting for bytes that never come,
        // so the connection is dropped rather than reused.
        let mut buf = vec![0u8; (length as usize).min(PUT_CHUNK)];
        let mut sent = 0u64;
        let mut crc = 0u32;
        while sent < length {
            let n = (length - sent).min(buf.len() as u64) as usize;
            if let Err(e) = src
                .read_exact(&mut buf[..n])
                .and_then(|()| self.transport.write_payload(self.peer_id, &buf[..n]))
            {
                self.transport.drop_peer(self.peer_id);
                return Err(format!("PUT payload at {sent} of {length}: {e}"));
            }
            crc = nvstor_core::checksum::crc32c_append(crc, &buf[..n]);
            sent += n as u64;
        }
        let resp = self
            .transport
            .recv_response(self.peer_id)
            .map_err(|e| format!("PUT recv_response: {e}"))?;
        match resp {
            Response::Ok {
                req_id: r,
                length: written,
                crc32c,
            } if r == req_id => {
                if crc32c != crc {
                    return Err(format!(
                        "PUT: server received crc32c {crc32c:08x}, sent {crc:08x}"
                    ));
                }
                Ok(written)
            }
            Response::Err { status, .. } => Err(format!("PUT failed: status={status}")),
            other => Err(format!("PUT: unexpected response {other:?}")),
        }
    }

    /// One ranged GET of up to `dest.len()` bytes (the server caps a
    /// range at `MAX_GET_RANGE`) from `offset`, read straight into
    /// `dest` and verified against the server's CRC-32C. Pass the
    /// generation from the first range (or `stat`) as `if_generation`
    /// for every later range of the same object.
    pub fn get_range_into(
        &self,
        key: &str,
        offset: u64,
        if_generation: u128,
        dest: &mut [u8],
    ) -> Result<ObjectRead, GetError> {
        let txn = self
            .transport
            .txn_arc(self.peer_id)
            .map_err(|e| GetError::Transport(format!("txn_arc: {e}")))?;
        let _guard = txn.lock();

        let req_id = self.req_id();
        let req = Request::Get {
            req_id,
            key: key.to_string(),
            offset,
            length: dest.len() as u64,
            if_generation,
        };
        self.transport
            .send_request(self.peer_id, &req)
            .map_err(|e| GetError::Transport(format!("send_request: {e}")))?;
        let resp = self
            .transport
            .recv_response(self.peer_id)
            .map_err(|e| GetError::Transport(format!("recv_response: {e}")))?;
        match resp {
            Response::Range {
                req_id: r,
                length,
                object_size,
                generation,
                crc32c,
            } if r == req_id => {
                let Some(len) = usize::try_from(length).ok().filter(|&n| n <= dest.len()) else {
                    // The unread payload has desynced the stream; this
                    // connection can't carry another frame.
                    self.transport.drop_peer(self.peer_id);
                    return Err(GetError::Transport(format!(
                        "server sent {length} bytes for a {}-byte range",
                        dest.len()
                    )));
                };
                self.transport
                    .read_payload(self.peer_id, &mut dest[..len])
                    .map_err(|e| GetError::Transport(format!("read_payload: {e}")))?;
                if if_generation != ANY_GENERATION && generation != if_generation {
                    return Err(GetError::GenerationChanged);
                }
                crate::object::verify_range(&dest[..len], crc32c)?;
                Ok(ObjectRead {
                    len,
                    size: object_size,
                    generation,
                })
            }
            Response::Err { req_id: r, status } if r == req_id => Err(match status {
                STATUS_NOT_FOUND => GetError::NotFound,
                STATUS_GENERATION_CHANGED => GetError::GenerationChanged,
                other => GetError::Server(other),
            }),
            other => Err(GetError::Transport(format!(
                "unexpected response {other:?}"
            ))),
        }
    }

    /// Whole object into memory: stat, then ranged reads straight into
    /// the result buffer, each CRC-checked and pinned to the stat's
    /// generation. For objects that don't fit in RAM, write ranges to a
    /// file instead.
    pub fn get(&self, key: &str) -> Result<Vec<u8>, String> {
        let (size, generation) = self.stat(key)?;
        let size = usize::try_from(size)
            .map_err(|_| format!("GET: object size {size} exceeds address space"))?;
        let mut out = Vec::new();
        out.try_reserve_exact(size)
            .map_err(|e| format!("GET: cannot buffer {size}-byte object: {e}"))?;
        out.resize(size, 0);
        let mut off = 0;
        while off < size {
            let end = off + (size - off).min(MAX_GET_RANGE as usize);
            let r = self.get_range_into(key, off as u64, generation, &mut out[off..end])?;
            if r.len == 0 {
                return Err(format!("GET: object ended at {off} of {size} bytes"));
            }
            off += r.len;
        }
        Ok(out)
    }

    /// Object size and generation without moving data.
    pub fn stat(&self, key: &str) -> Result<(u64, u128), GetError> {
        let req_id = self.req_id();
        let resp = self
            .transport
            .request_response(
                self.peer_id,
                &Request::Head {
                    req_id,
                    key: key.to_string(),
                },
            )
            .map_err(|e| GetError::Transport(format!("HEAD: {e}")))?;
        match resp {
            Response::Range {
                req_id: r,
                object_size,
                generation,
                ..
            } if r == req_id => Ok((object_size, generation)),
            Response::Err { req_id: r, status } if r == req_id => Err(match status {
                STATUS_NOT_FOUND => GetError::NotFound,
                other => GetError::Server(other),
            }),
            other => Err(GetError::Transport(format!(
                "HEAD: unexpected response {other:?}"
            ))),
        }
    }

    pub fn head(&self, key: &str) -> Result<u64, String> {
        Ok(self.stat(key)?.0)
    }

    pub fn delete(&self, key: &str) -> Result<(), String> {
        let req_id = self.req_id();
        let resp = self
            .transport
            .request_response(
                self.peer_id,
                &Request::Delete {
                    req_id,
                    key: key.to_string(),
                },
            )
            .map_err(|e| format!("DELETE: {e}"))?;
        match resp {
            Response::Ok { req_id: r, .. } if r == req_id => Ok(()),
            Response::Err { status, .. } => Err(format!("DELETE failed: status={status}")),
            other => Err(format!("DELETE: unexpected response {other:?}")),
        }
    }
}
