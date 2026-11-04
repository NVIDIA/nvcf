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

//! KV-over-RDMA wire format. Experimental: part of the KV tier.
//!
//! The KV service has its own listener and its own message set, separate
//! from S3-over-RDMA (`s3_rdma_proto`): a KV client never speaks the
//! object protocol and never falls back to it. Messages are SEND/RECV on
//! lane 0, each at most [`KV_MSG_SIZE`] bytes, packed little-endian.
//!
//! ```text
//! client → Layout { layout_id, rkey, num_blocks, tensors[(base, stride, page)] }
//!   Describes the client's landing pool in its registered memory: block
//!   index `i` is landing slot `i`, whose bytes are the pages `base_t + i *
//!   stride_t .. + page_t` of every tensor `t`, in tensor order. That
//!   concatenation is the block's payload, the unit the server stores and
//!   checks. A pool laid out like vLLM's tensors (99 layer pages) lets the
//!   client place a landed block in vLLM's KV cache with one
//!   device-to-device copy per page.
//! server → LayoutAck { status }
//!
//! client → Get { namespace, layout_id, budget_ms, [(key, block index)] }
//!   server: for each hit, RDMA-WRITE the payload's pages to the block's
//!           addresses (one chain per block), and reply only after every
//!           WRITE's completion
//! server → Get { [(status, length, check)] }   one entry per request entry
//!
//! client → Put { namespace, layout_id, budget_ms, [(key, block index, check)] }
//!   server: RDMA-READ each block's pages into its store, verify `check`
//! server → Put { [status] }
//!
//! client → Head { namespace, [key] }
//! server → Head { [status] }
//! ```
//!
//! The server never names its own memory, and writes only to addresses
//! derived from a layout the client registered and signed, inside the
//! memory region the client's `rkey` grants.
//!
//! **Landing slots, not the engine's pages.** A GET
//! whose client deadline passes is reported to the engine as a miss at
//! once, but a WRITE the server already posted may still land. So the
//! layout describes a landing pool the client owns: a slot whose request
//! timed out stays quarantined until the reply or the destruction of the
//! connection's QPs, and the engine's own pages are written only by the
//! client, from a slot whose reply arrived. Registering the engine's pages
//! as the layout is refused by design until fencing them is specified.
//!
//! **What the server must enforce,** since the format cannot:
//! - the envelope's access key is the connection's authenticated identity
//!   (a signed request is not accepted from another connection);
//! - the access key may use the request's namespace (tenants are
//!   separated by namespace and credential);
//! - a request moves at most [`MAX_REQUEST_BYTES`] (entries × the layout's
//!   block bytes);
//! - `seq` increases on each connection (until the
//!   per-connection request key exists, the process-wide replay guard
//!   applies as for S3-over-RDMA);
//! - a layout id is registered once per connection: a second LAYOUT with
//!   the same id is refused with [`STATUS_BAD_LAYOUT`].
//!
//! Every message begins with [`KV_PROTO_VERSION`]; a peer that sends
//! another version is refused at decode. A status this build does not
//! know is treated by a client as [`STATUS_ERROR`]. `check` is the 64-bit block
//! check, computed by the writer over (namespace ‖ key
//! ‖ payload), stored with the block and returned verbatim on every GET;
//! the reader verifies it.
//!
//! Keys are 32 to 64 bytes (vLLM's sha256 block hash plus group index is
//! 36). Shorter keys, such as vLLM's 20-byte xxhash keys whose seed
//! differs per process, are refused: they do not name the same block
//! across processes.
//!
//! Every request is signed exactly as in `s3_rdma_proto`: the envelope
//! `[access_key][timestamp][nonce][signature][body_len][body]` with the
//! HMAC of `crate::auth::sign` over the body.

use std::io;

/// Largest KV message, request or response, envelope included.
pub const KV_MSG_SIZE: usize = 16 * 1024;
/// Most entries in one GET, PUT or HEAD: the largest such message (a PUT
/// of 64-byte keys) fits in [`KV_MSG_SIZE`] signed, checked below. 192
/// Blocks of the example hybrid-attention layout are 12,288 tokens; a longer prefix takes more batches.
pub const MAX_BATCH: usize = 192;
/// Most tensors in a layout (the example layout has 99).
pub const MAX_TENSORS: usize = 256;
/// Largest block payload (the example layout: 3,052,800 B).
pub const MAX_BLOCK_BYTES: u64 = 64 << 20;
/// Most bytes one GET or PUT may move (entries × block bytes), enforced
/// by the server against the layout: 256 MiB is 85 blocks of the example layout.
pub const MAX_REQUEST_BYTES: u64 = 256 << 20;
/// Longest server-side budget a request may carry (budgets are expected
/// to be tens to hundreds of milliseconds).
pub const MAX_BUDGET_MS: u32 = 10_000;
/// Wire version, the first byte of every message.
pub const KV_PROTO_VERSION: u8 = 1;
pub const MIN_KEY_LEN: usize = 32;
pub const MAX_KEY_LEN: usize = 64;
/// Namespace hash length.
pub const NAMESPACE_LEN: usize = 16;

/// Signed envelope around a body: access key (u32 length + up to 64 B),
/// timestamp, nonce, signature, body length.
const ENVELOPE_MAX: usize =
    4 + crate::s3_rdma_proto::MAX_ACCESS_KEY_LEN + 8 + 8 + crate::auth::SIGNATURE_LEN + 4;
/// The largest request: a PUT of MAX_BATCH entries with the longest keys.
const LARGEST_REQUEST: usize =
    1 + 1 + 8 + 8 + NAMESPACE_LEN + 4 + 4 + 2 + MAX_BATCH * (1 + MAX_KEY_LEN + 4 + 8);
/// The largest layout: MAX_TENSORS of (base, stride, page).
const LARGEST_LAYOUT: usize = 1 + 1 + 8 + 8 + 4 + 4 + 4 + 2 + MAX_TENSORS * (8 + 8 + 4);
const _: () = assert!(ENVELOPE_MAX + LARGEST_REQUEST <= KV_MSG_SIZE);
const _: () = assert!(ENVELOPE_MAX + LARGEST_LAYOUT <= KV_MSG_SIZE);
/// The largest response: a GET result per entry.
const _: () = assert!(1 + 1 + 8 + 2 + MAX_BATCH * (1 + 4 + 8) <= KV_MSG_SIZE);

/// Request opcodes. Stable wire constants: never renumber.
pub const OP_LAYOUT: u8 = 1;
pub const OP_GET: u8 = 2;
pub const OP_PUT: u8 = 3;
pub const OP_HEAD: u8 = 4;

/// Response tags.
const TAG_LAYOUT: u8 = 1;
const TAG_GET: u8 = 2;
const TAG_PUT: u8 = 3;
const TAG_HEAD: u8 = 4;
const TAG_ERR: u8 = 5;

/// Per-entry and per-request statuses. Stable wire constants.
pub const STATUS_OK: u8 = 0;
/// GET/HEAD: not stored, or not yet complete. A miss, never an error.
pub const STATUS_MISS: u8 = 1;
/// PUT: already stored; nothing was read.
pub const STATUS_EXISTS: u8 = 2;
/// PUT: another PUT of this key is in flight; nothing was read.
pub const STATUS_PENDING: u8 = 3;
/// The request's budget ran out on the server before this entry's data
/// moved: nothing was written to or read from the client for it.
pub const STATUS_EXPIRED: u8 = 4;
/// PUT: the payload read did not match its `check`; nothing was stored.
pub const STATUS_CHECK_MISMATCH: u8 = 5;
/// PUT: the store has no room even after eviction.
pub const STATUS_NO_SPACE: u8 = 6;
/// The layout id is unknown on this connection, or the block index is
/// outside it, or the payload size differs from the stored block's.
pub const STATUS_BAD_LAYOUT: u8 = 7;
pub const STATUS_UNAUTHORIZED: u8 = 8;
pub const STATUS_ERROR: u8 = 9;
/// PUT: the data was read but not stored (its deadline passed before
/// it could be committed). Unlike [`STATUS_EXPIRED`], bytes moved.
pub const STATUS_NOT_STORED: u8 = 10;

/// A block key: 32 to 64 bytes, kept inline (no allocation per entry).
#[derive(Clone, Copy, PartialEq, Eq, Hash)]
pub struct Key {
    len: u8,
    bytes: [u8; MAX_KEY_LEN],
}

impl Key {
    pub fn new(key: &[u8]) -> io::Result<Key> {
        if !(MIN_KEY_LEN..=MAX_KEY_LEN).contains(&key.len()) {
            return Err(invalid(format!(
                "key of {} bytes: keys are {MIN_KEY_LEN}..={MAX_KEY_LEN}",
                key.len()
            )));
        }
        let mut bytes = [0u8; MAX_KEY_LEN];
        bytes[..key.len()].copy_from_slice(key);
        Ok(Key {
            len: key.len() as u8,
            bytes,
        })
    }

    pub fn as_bytes(&self) -> &[u8] {
        &self.bytes[..self.len as usize]
    }
}

impl std::fmt::Debug for Key {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        for b in self.as_bytes() {
            write!(f, "{b:02x}")?;
        }
        Ok(())
    }
}

/// One tensor of a layout: block `i`'s page is `base + i * stride ..
/// + page` in the client's registered memory.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct TensorRun {
    pub base: u64,
    pub stride: u64,
    pub page: u32,
}

/// Where blocks live in the client's memory, registered once per
/// connection and named by id in GET and PUT.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Layout {
    pub layout_id: u32,
    /// The rkey of the client's region on this connection's rail.
    pub rkey: u32,
    /// Valid block indices are `0..num_blocks`.
    pub num_blocks: u32,
    pub tensors: Vec<TensorRun>,
}

impl Layout {
    /// Bytes of one block's payload: the sum of the pages.
    pub fn block_bytes(&self) -> u64 {
        self.tensors.iter().map(|t| t.page as u64).sum()
    }

    /// Refuse a layout whose addresses overflow, whose blocks overlap
    /// within a tensor, or whose payload is out of bounds. Called by both
    /// encoder and decoder, so a bad layout never reaches the wire or the
    /// server's address arithmetic.
    pub fn validate(&self) -> io::Result<()> {
        if self.tensors.is_empty() || self.tensors.len() > MAX_TENSORS {
            return Err(invalid(format!(
                "layout has {} tensors (1..={MAX_TENSORS})",
                self.tensors.len()
            )));
        }
        if self.num_blocks == 0 {
            return Err(invalid("layout has no blocks"));
        }
        for (i, t) in self.tensors.iter().enumerate() {
            if t.page == 0 {
                return Err(invalid(format!("tensor {i}: empty page")));
            }
            if self.num_blocks > 1 && t.stride < t.page as u64 {
                return Err(invalid(format!(
                    "tensor {i}: stride {} below page {}: blocks overlap",
                    t.stride, t.page
                )));
            }
            let last = (self.num_blocks as u64 - 1)
                .checked_mul(t.stride)
                .and_then(|o| o.checked_add(t.base))
                .and_then(|a| a.checked_add(t.page as u64));
            if last.is_none() {
                return Err(invalid(format!("tensor {i}: addresses overflow")));
            }
        }
        let bytes = self.block_bytes();
        if bytes > MAX_BLOCK_BYTES {
            return Err(invalid(format!(
                "block of {bytes} bytes exceeds {MAX_BLOCK_BYTES}"
            )));
        }
        Ok(())
    }

    /// The pieces of block `index`: (remote address, length, offset in
    /// the payload), in payload order. `None` if the index is outside the
    /// layout. Overflow-free for a layout that passed [`Layout::validate`].
    pub fn block_pieces(&self, index: u32) -> Option<impl Iterator<Item = (u64, u32, u64)> + '_> {
        if index >= self.num_blocks {
            return None;
        }
        let mut offset = 0u64;
        Some(self.tensors.iter().map(move |t| {
            let piece = (t.base + index as u64 * t.stride, t.page, offset);
            offset += t.page as u64;
            piece
        }))
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct GetEntry {
    pub key: Key,
    pub block: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PutEntry {
    pub key: Key,
    pub block: u32,
    /// The writer's 64-bit block check over (namespace ‖ key ‖ payload).
    pub check: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Request {
    Layout {
        req_id: u64,
        seq: u64,
        layout: Layout,
    },
    Get {
        req_id: u64,
        seq: u64,
        namespace: [u8; NAMESPACE_LEN],
        layout_id: u32,
        budget_ms: u32,
        entries: Vec<GetEntry>,
    },
    Put {
        req_id: u64,
        seq: u64,
        namespace: [u8; NAMESPACE_LEN],
        layout_id: u32,
        budget_ms: u32,
        entries: Vec<PutEntry>,
    },
    Head {
        req_id: u64,
        seq: u64,
        namespace: [u8; NAMESPACE_LEN],
        keys: Vec<Key>,
    },
}

/// One GET result: `length` and `check` are meaningful for
/// [`STATUS_OK`] only.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct GetResult {
    pub status: u8,
    pub length: u32,
    pub check: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Response {
    LayoutAck {
        req_id: u64,
        status: u8,
    },
    /// One result per request entry, in request order.
    Get {
        req_id: u64,
        results: Vec<GetResult>,
    },
    Put {
        req_id: u64,
        statuses: Vec<u8>,
    },
    Head {
        req_id: u64,
        statuses: Vec<u8>,
    },
    /// The whole request was refused (authentication, malformed batch).
    Err {
        req_id: u64,
        status: u8,
    },
}

impl Request {
    pub fn req_id(&self) -> u64 {
        match self {
            Request::Layout { req_id, .. }
            | Request::Get { req_id, .. }
            | Request::Put { req_id, .. }
            | Request::Head { req_id, .. } => *req_id,
        }
    }

    /// The connection-bound sequence number.
    pub fn seq(&self) -> u64 {
        match self {
            Request::Layout { seq, .. }
            | Request::Get { seq, .. }
            | Request::Put { seq, .. }
            | Request::Head { seq, .. } => *seq,
        }
    }

    /// Entries the reply must carry, one result per entry.
    pub fn entries(&self) -> usize {
        match self {
            Request::Layout { .. } => 0,
            Request::Get { entries, .. } => entries.len(),
            Request::Put { entries, .. } => entries.len(),
            Request::Head { keys, .. } => keys.len(),
        }
    }

    /// Whether `resp` answers this request: the same req_id, the matching
    /// kind and one result per entry (or a whole-request error).
    pub fn answered_by(&self, resp: &Response) -> bool {
        if resp.req_id() != self.req_id() {
            return false;
        }
        match (self, resp) {
            (_, Response::Err { .. }) => true,
            (Request::Layout { .. }, Response::LayoutAck { .. }) => true,
            (Request::Get { .. }, Response::Get { results, .. }) => results.len() == self.entries(),
            (Request::Put { .. }, Response::Put { statuses, .. })
            | (Request::Head { .. }, Response::Head { statuses, .. }) => {
                statuses.len() == self.entries()
            }
            _ => false,
        }
    }

    pub fn encode_into(&self, buf: &mut [u8]) -> io::Result<usize> {
        let mut w = Writer::new(buf);
        w.u8(KV_PROTO_VERSION)?;
        match self {
            Request::Layout {
                req_id,
                seq,
                layout,
            } => {
                layout.validate()?;
                w.u8(OP_LAYOUT)?;
                w.u64(*req_id)?;
                w.u64(*seq)?;
                w.u32(layout.layout_id)?;
                w.u32(layout.rkey)?;
                w.u32(layout.num_blocks)?;
                w.u16(layout.tensors.len() as u16)?;
                for t in &layout.tensors {
                    w.u64(t.base)?;
                    w.u64(t.stride)?;
                    w.u32(t.page)?;
                }
            }
            Request::Get {
                req_id,
                seq,
                namespace,
                layout_id,
                budget_ms,
                entries,
            } => {
                batch_len(entries.len())?;
                budget(*budget_ms)?;
                w.u8(OP_GET)?;
                w.u64(*req_id)?;
                w.u64(*seq)?;
                w.bytes(namespace)?;
                w.u32(*layout_id)?;
                w.u32(*budget_ms)?;
                w.u16(entries.len() as u16)?;
                for e in entries {
                    w.key(&e.key)?;
                    w.u32(e.block)?;
                }
            }
            Request::Put {
                req_id,
                seq,
                namespace,
                layout_id,
                budget_ms,
                entries,
            } => {
                batch_len(entries.len())?;
                budget(*budget_ms)?;
                w.u8(OP_PUT)?;
                w.u64(*req_id)?;
                w.u64(*seq)?;
                w.bytes(namespace)?;
                w.u32(*layout_id)?;
                w.u32(*budget_ms)?;
                w.u16(entries.len() as u16)?;
                for e in entries {
                    w.key(&e.key)?;
                    w.u32(e.block)?;
                    w.u64(e.check)?;
                }
            }
            Request::Head {
                req_id,
                seq,
                namespace,
                keys,
            } => {
                batch_len(keys.len())?;
                w.u8(OP_HEAD)?;
                w.u64(*req_id)?;
                w.u64(*seq)?;
                w.bytes(namespace)?;
                w.u16(keys.len() as u16)?;
                for k in keys {
                    w.key(k)?;
                }
            }
        }
        Ok(w.pos)
    }

    pub fn decode(buf: &[u8]) -> io::Result<Request> {
        let mut r = Reader::new(buf);
        r.version()?;
        let op = r.u8()?;
        let req_id = r.u64()?;
        let seq = r.u64()?;
        let req = match op {
            OP_LAYOUT => {
                let layout_id = r.u32()?;
                let rkey = r.u32()?;
                let num_blocks = r.u32()?;
                let n = r.u16()? as usize;
                if n == 0 || n > MAX_TENSORS {
                    return Err(invalid(format!(
                        "layout has {n} tensors (1..={MAX_TENSORS})"
                    )));
                }
                let mut tensors = Vec::with_capacity(n);
                for _ in 0..n {
                    tensors.push(TensorRun {
                        base: r.u64()?,
                        stride: r.u64()?,
                        page: r.u32()?,
                    });
                }
                let layout = Layout {
                    layout_id,
                    rkey,
                    num_blocks,
                    tensors,
                };
                layout.validate()?;
                Request::Layout {
                    req_id,
                    seq,
                    layout,
                }
            }
            OP_GET | OP_PUT => {
                let mut namespace = [0u8; NAMESPACE_LEN];
                r.bytes_into(&mut namespace)?;
                let layout_id = r.u32()?;
                let budget_ms = r.u32()?;
                budget(budget_ms)?;
                let n = r.u16()? as usize;
                batch_len(n)?;
                if op == OP_GET {
                    let mut entries = Vec::with_capacity(n);
                    for _ in 0..n {
                        entries.push(GetEntry {
                            key: r.key()?,
                            block: r.u32()?,
                        });
                    }
                    Request::Get {
                        req_id,
                        seq,
                        namespace,
                        layout_id,
                        budget_ms,
                        entries,
                    }
                } else {
                    let mut entries = Vec::with_capacity(n);
                    for _ in 0..n {
                        entries.push(PutEntry {
                            key: r.key()?,
                            block: r.u32()?,
                            check: r.u64()?,
                        });
                    }
                    Request::Put {
                        req_id,
                        seq,
                        namespace,
                        layout_id,
                        budget_ms,
                        entries,
                    }
                }
            }
            OP_HEAD => {
                let mut namespace = [0u8; NAMESPACE_LEN];
                r.bytes_into(&mut namespace)?;
                let n = r.u16()? as usize;
                batch_len(n)?;
                let mut keys = Vec::with_capacity(n);
                for _ in 0..n {
                    keys.push(r.key()?);
                }
                Request::Head {
                    req_id,
                    seq,
                    namespace,
                    keys,
                }
            }
            other => return Err(invalid(format!("unknown KV opcode {other}"))),
        };
        r.finish()?;
        Ok(req)
    }
}

impl Response {
    pub fn req_id(&self) -> u64 {
        match self {
            Response::LayoutAck { req_id, .. }
            | Response::Get { req_id, .. }
            | Response::Put { req_id, .. }
            | Response::Head { req_id, .. }
            | Response::Err { req_id, .. } => *req_id,
        }
    }

    pub fn encode_into(&self, buf: &mut [u8]) -> io::Result<usize> {
        let mut w = Writer::new(buf);
        w.u8(KV_PROTO_VERSION)?;
        match self {
            Response::LayoutAck { req_id, status } => {
                w.u8(TAG_LAYOUT)?;
                w.u64(*req_id)?;
                w.u8(*status)?;
            }
            Response::Get { req_id, results } => {
                batch_len(results.len())?;
                w.u8(TAG_GET)?;
                w.u64(*req_id)?;
                w.u16(results.len() as u16)?;
                for g in results {
                    w.u8(g.status)?;
                    w.u32(g.length)?;
                    w.u64(g.check)?;
                }
            }
            Response::Put { req_id, statuses } | Response::Head { req_id, statuses } => {
                batch_len(statuses.len())?;
                w.u8(if matches!(self, Response::Put { .. }) {
                    TAG_PUT
                } else {
                    TAG_HEAD
                })?;
                w.u64(*req_id)?;
                w.u16(statuses.len() as u16)?;
                for s in statuses {
                    w.u8(*s)?;
                }
            }
            Response::Err { req_id, status } => {
                w.u8(TAG_ERR)?;
                w.u64(*req_id)?;
                w.u8(*status)?;
            }
        }
        Ok(w.pos)
    }

    pub fn decode(buf: &[u8]) -> io::Result<Response> {
        let mut r = Reader::new(buf);
        r.version()?;
        let tag = r.u8()?;
        let req_id = r.u64()?;
        let resp = match tag {
            TAG_LAYOUT => Response::LayoutAck {
                req_id,
                status: r.u8()?,
            },
            TAG_GET => {
                let n = r.u16()? as usize;
                batch_len(n)?;
                let mut results = Vec::with_capacity(n);
                for _ in 0..n {
                    results.push(GetResult {
                        status: r.u8()?,
                        length: r.u32()?,
                        check: r.u64()?,
                    });
                }
                Response::Get { req_id, results }
            }
            TAG_PUT | TAG_HEAD => {
                let n = r.u16()? as usize;
                batch_len(n)?;
                let mut statuses = Vec::with_capacity(n);
                for _ in 0..n {
                    statuses.push(r.u8()?);
                }
                if tag == TAG_PUT {
                    Response::Put { req_id, statuses }
                } else {
                    Response::Head { req_id, statuses }
                }
            }
            TAG_ERR => Response::Err {
                req_id,
                status: r.u8()?,
            },
            other => return Err(invalid(format!("unknown KV response tag {other}"))),
        };
        r.finish()?;
        Ok(resp)
    }
}

/// Encode `req` in a signed envelope: `[access_key][timestamp][nonce]
/// [signature][body_len][body]`, the signature over the body bytes.
pub fn encode_signed_into(
    req: &Request,
    creds: &crate::auth::Credentials,
    nonce: u64,
    buf: &mut [u8],
) -> io::Result<usize> {
    let mut body = [0u8; KV_MSG_SIZE];
    let body_len = req.encode_into(&mut body)?;
    let header = crate::auth::sign_request(creds, nonce, &body[..body_len]);
    if header.access_key.len() > crate::s3_rdma_proto::MAX_ACCESS_KEY_LEN {
        return Err(invalid("access key too long"));
    }
    let mut w = Writer::new(buf);
    w.u32(header.access_key.len() as u32)?;
    w.bytes(header.access_key.as_bytes())?;
    w.u64(header.timestamp)?;
    w.u64(header.nonce)?;
    w.bytes(&header.signature)?;
    w.u32(body_len as u32)?;
    w.bytes(&body[..body_len])?;
    // Unreachable for any request the encoder accepts (the size asserts
    // above); kept so a later change to those bounds fails here, not on
    // the wire.
    if w.pos > KV_MSG_SIZE {
        return Err(invalid(format!(
            "signed message of {} bytes exceeds {KV_MSG_SIZE}",
            w.pos
        )));
    }
    Ok(w.pos)
}

/// Decode a signed envelope into its header, the request and the body
/// bytes. The signature is NOT verified here: the caller's `AuthFn` does.
pub fn decode_signed(buf: &[u8]) -> io::Result<(crate::auth::AuthHeader, Request, Vec<u8>)> {
    if buf.len() > KV_MSG_SIZE {
        return Err(invalid(format!(
            "message of {} bytes exceeds {KV_MSG_SIZE}",
            buf.len()
        )));
    }
    let mut r = Reader::new(buf);
    let key_len = r.u32()? as usize;
    if key_len > crate::s3_rdma_proto::MAX_ACCESS_KEY_LEN {
        return Err(invalid("access key too long"));
    }
    let mut key = vec![0u8; key_len];
    r.bytes_into(&mut key)?;
    let access_key = String::from_utf8(key).map_err(|e| invalid(e.to_string()))?;
    let timestamp = r.u64()?;
    let nonce = r.u64()?;
    let mut signature = [0u8; crate::auth::SIGNATURE_LEN];
    r.bytes_into(&mut signature)?;
    let body_len = r.u32()? as usize;
    if body_len > r.remaining() {
        return Err(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            "body runs past the message",
        ));
    }
    let mut body = vec![0u8; body_len];
    r.bytes_into(&mut body)?;
    // Nothing unsigned may follow the signed body.
    r.finish()?;
    let req = Request::decode(&body)?;
    Ok((
        crate::auth::AuthHeader {
            access_key,
            timestamp,
            nonce,
            signature,
        },
        req,
        body,
    ))
}

fn invalid(msg: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg.into())
}

fn budget(ms: u32) -> io::Result<()> {
    if ms == 0 || ms > MAX_BUDGET_MS {
        return Err(invalid(format!("budget of {ms} ms (1..={MAX_BUDGET_MS})")));
    }
    Ok(())
}

fn batch_len(n: usize) -> io::Result<()> {
    if n == 0 || n > MAX_BATCH {
        return Err(invalid(format!("batch of {n} entries (1..={MAX_BATCH})")));
    }
    Ok(())
}

/// Little-endian writer that refuses to run past its buffer.
struct Writer<'a> {
    buf: &'a mut [u8],
    pos: usize,
}

impl<'a> Writer<'a> {
    fn new(buf: &'a mut [u8]) -> Self {
        Self { buf, pos: 0 }
    }
    fn bytes(&mut self, v: &[u8]) -> io::Result<()> {
        let end = self
            .pos
            .checked_add(v.len())
            .filter(|&e| e <= self.buf.len())
            .ok_or_else(|| {
                io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "KV message does not fit its buffer",
                )
            })?;
        self.buf[self.pos..end].copy_from_slice(v);
        self.pos = end;
        Ok(())
    }
    fn u8(&mut self, v: u8) -> io::Result<()> {
        self.bytes(&[v])
    }
    fn u16(&mut self, v: u16) -> io::Result<()> {
        self.bytes(&v.to_le_bytes())
    }
    fn u32(&mut self, v: u32) -> io::Result<()> {
        self.bytes(&v.to_le_bytes())
    }
    fn u64(&mut self, v: u64) -> io::Result<()> {
        self.bytes(&v.to_le_bytes())
    }
    fn key(&mut self, k: &Key) -> io::Result<()> {
        self.u8(k.len)?;
        self.bytes(k.as_bytes())
    }
}

/// Little-endian reader that refuses to run past its buffer.
struct Reader<'a> {
    buf: &'a [u8],
    pos: usize,
}

impl<'a> Reader<'a> {
    fn new(buf: &'a [u8]) -> Self {
        Self { buf, pos: 0 }
    }
    fn remaining(&self) -> usize {
        self.buf.len() - self.pos
    }
    fn take(&mut self, n: usize) -> io::Result<&'a [u8]> {
        if n > self.remaining() {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                format!("read {n} bytes past the end"),
            ));
        }
        let s = &self.buf[self.pos..self.pos + n];
        self.pos += n;
        Ok(s)
    }
    fn bytes_into(&mut self, dst: &mut [u8]) -> io::Result<()> {
        dst.copy_from_slice(self.take(dst.len())?);
        Ok(())
    }
    fn u8(&mut self) -> io::Result<u8> {
        Ok(self.take(1)?[0])
    }
    fn u16(&mut self) -> io::Result<u16> {
        Ok(u16::from_le_bytes(
            self.take(2)?
                .try_into()
                .expect("invariant: take(2) is 2 bytes"),
        ))
    }
    fn u32(&mut self) -> io::Result<u32> {
        Ok(u32::from_le_bytes(
            self.take(4)?
                .try_into()
                .expect("invariant: take(4) is 4 bytes"),
        ))
    }
    fn u64(&mut self) -> io::Result<u64> {
        Ok(u64::from_le_bytes(
            self.take(8)?
                .try_into()
                .expect("invariant: take(8) is 8 bytes"),
        ))
    }
    fn key(&mut self) -> io::Result<Key> {
        let len = self.u8()? as usize;
        Key::new(self.take(len)?)
    }
    fn version(&mut self) -> io::Result<()> {
        match self.u8()? {
            KV_PROTO_VERSION => Ok(()),
            v => Err(invalid(format!(
                "KV protocol version {v}; this build speaks {KV_PROTO_VERSION}"
            ))),
        }
    }
    /// A message must be consumed exactly: trailing bytes mean the sender
    /// and receiver disagree on the format.
    fn finish(&self) -> io::Result<()> {
        if self.remaining() != 0 {
            return Err(invalid(format!("{} trailing bytes", self.remaining())));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::{verify_signature, Credentials};

    fn key(b: u8, len: usize) -> Key {
        Key::new(&vec![b; len]).expect("valid key")
    }

    /// The example hybrid-attention layout as vLLM allocates it: 78 pages of 36,864 B
    /// and 21 of 8,448 B, over 43,600 blocks.
    fn glm_layout() -> Layout {
        let mut tensors = Vec::new();
        let mut base = 0x1_0000_0000u64;
        for i in 0..99u64 {
            let page = if i < 78 { 36_864 } else { 8_448 };
            tensors.push(TensorRun {
                base,
                stride: page as u64,
                page,
            });
            base += page as u64 * 43_600;
        }
        Layout {
            layout_id: 7,
            rkey: 0x1234,
            num_blocks: 43_600,
            tensors,
        }
    }

    fn roundtrip(req: &Request) -> Request {
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = req.encode_into(&mut buf).expect("encode");
        Request::decode(&buf[..n]).expect("decode")
    }

    #[test]
    fn every_request_roundtrips() {
        let ns = [9u8; NAMESPACE_LEN];
        let reqs = [
            Request::Layout {
                req_id: 1,
                seq: 1,
                layout: glm_layout(),
            },
            Request::Get {
                req_id: 2,
                seq: 2,
                namespace: ns,
                layout_id: 7,
                budget_ms: 50,
                entries: vec![
                    GetEntry {
                        key: key(1, 36),
                        block: 0,
                    },
                    GetEntry {
                        key: key(2, 64),
                        block: 43_599,
                    },
                ],
            },
            Request::Put {
                req_id: 3,
                seq: 3,
                namespace: ns,
                layout_id: 7,
                budget_ms: MAX_BUDGET_MS,
                entries: vec![PutEntry {
                    key: key(3, 32),
                    block: 5,
                    check: u64::MAX,
                }],
            },
            Request::Head {
                req_id: 4,
                seq: u64::MAX,
                namespace: ns,
                keys: vec![key(4, 36), key(5, 36)],
            },
        ];
        for r in &reqs {
            assert_eq!(&roundtrip(r), r);
        }
    }

    #[test]
    fn every_response_roundtrips() {
        let resps = [
            Response::LayoutAck {
                req_id: 1,
                status: STATUS_OK,
            },
            Response::Get {
                req_id: 2,
                results: vec![
                    GetResult {
                        status: STATUS_OK,
                        length: 3_052_800,
                        check: 42,
                    },
                    GetResult {
                        status: STATUS_MISS,
                        length: 0,
                        check: 0,
                    },
                ],
            },
            Response::Put {
                req_id: 3,
                statuses: vec![STATUS_OK, STATUS_EXISTS, STATUS_PENDING],
            },
            Response::Head {
                req_id: 4,
                statuses: vec![STATUS_MISS],
            },
            Response::Err {
                req_id: 5,
                status: STATUS_UNAUTHORIZED,
            },
        ];
        for r in &resps {
            let mut buf = [0u8; KV_MSG_SIZE];
            let n = r.encode_into(&mut buf).expect("encode");
            assert_eq!(&Response::decode(&buf[..n]).expect("decode"), r);
        }
    }

    #[test]
    fn the_largest_messages_fit_signed() {
        let creds = Credentials::new(
            "k".repeat(crate::s3_rdma_proto::MAX_ACCESS_KEY_LEN),
            "secret",
        );
        let ns = [0u8; NAMESPACE_LEN];
        let mut layout = glm_layout();
        layout.tensors = (0..MAX_TENSORS as u64)
            .map(|i| TensorRun {
                base: i << 32,
                stride: 4096,
                page: 4096,
            })
            .collect();
        let largest = [
            Request::Layout {
                req_id: u64::MAX,
                seq: u64::MAX,
                layout,
            },
            Request::Put {
                req_id: u64::MAX,
                seq: u64::MAX,
                namespace: ns,
                layout_id: 1,
                budget_ms: 1,
                entries: vec![
                    PutEntry {
                        key: key(1, MAX_KEY_LEN),
                        block: 0,
                        check: 0
                    };
                    MAX_BATCH
                ],
            },
            Request::Get {
                req_id: u64::MAX,
                seq: u64::MAX,
                namespace: ns,
                layout_id: 1,
                budget_ms: 1,
                entries: vec![
                    GetEntry {
                        key: key(1, MAX_KEY_LEN),
                        block: 0
                    };
                    MAX_BATCH
                ],
            },
            Request::Head {
                req_id: u64::MAX,
                seq: u64::MAX,
                namespace: ns,
                keys: vec![key(1, MAX_KEY_LEN); MAX_BATCH],
            },
        ];
        for req in &largest {
            let mut buf = [0u8; KV_MSG_SIZE];
            let n = encode_signed_into(req, &creds, 1, &mut buf).expect("fits");
            let (header, back, body) = decode_signed(&buf[..n]).expect("decodes");
            assert_eq!(&back, req);
            assert!(verify_signature(
                "secret",
                header.timestamp,
                header.nonce,
                &body,
                &header.signature
            ));
        }
        let get = Response::Get {
            req_id: 1,
            results: vec![
                GetResult {
                    status: 0,
                    length: 0,
                    check: 0
                };
                MAX_BATCH
            ],
        };
        let mut buf = [0u8; KV_MSG_SIZE];
        assert!(get.encode_into(&mut buf).is_ok());
    }

    #[test]
    fn a_tampered_body_fails_verification() {
        let creds = Credentials::new("key", "secret");
        let req = Request::Head {
            req_id: 1,
            seq: 1,
            namespace: [0; NAMESPACE_LEN],
            keys: vec![key(1, 36)],
        };
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = encode_signed_into(&req, &creds, 9, &mut buf).expect("encode");
        buf[n - 1] ^= 1;
        let (header, _, body) = decode_signed(&buf[..n]).expect("still decodes");
        assert!(!verify_signature(
            "secret",
            header.timestamp,
            header.nonce,
            &body,
            &header.signature
        ));
    }

    #[test]
    fn short_and_long_keys_are_refused() {
        assert!(Key::new(&[0u8; 20]).is_err(), "xxhash keys");
        assert!(Key::new(&[0u8; 31]).is_err());
        assert!(Key::new(&[0u8; 65]).is_err());
        // A peer that sends a 20-byte key is refused at decode.
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = Request::Head {
            req_id: 1,
            seq: 1,
            namespace: [0; NAMESPACE_LEN],
            keys: vec![key(1, 32)],
        }
        .encode_into(&mut buf)
        .expect("encode");
        buf[1 + 1 + 8 + 8 + NAMESPACE_LEN + 2] = 20;
        assert!(Request::decode(&buf[..n - 12]).is_err());
    }

    #[test]
    fn empty_and_oversized_batches_are_refused() {
        let ns = [0u8; NAMESPACE_LEN];
        let mut buf = [0u8; KV_MSG_SIZE];
        assert!(Request::Head {
            req_id: 1,
            seq: 1,
            namespace: ns,
            keys: vec![]
        }
        .encode_into(&mut buf)
        .is_err());
        assert!(Request::Head {
            req_id: 1,
            seq: 1,
            namespace: ns,
            keys: vec![key(1, 32); MAX_BATCH + 1]
        }
        .encode_into(&mut buf)
        .is_err());
        // A count above MAX_BATCH on the wire is refused before any allocation.
        let n = Request::Head {
            req_id: 1,
            seq: 1,
            namespace: ns,
            keys: vec![key(1, 32)],
        }
        .encode_into(&mut buf)
        .expect("encode");
        buf[1 + 1 + 8 + 8 + NAMESPACE_LEN..1 + 1 + 8 + 8 + NAMESPACE_LEN + 2]
            .copy_from_slice(&u16::MAX.to_le_bytes());
        assert!(Request::decode(&buf[..n]).is_err());
    }

    #[test]
    fn bad_layouts_are_refused() {
        let ok = glm_layout();
        assert!(ok.validate().is_ok());
        assert_eq!(ok.block_bytes(), 3_052_800);
        let mut overlap = ok.clone();
        overlap.tensors[3].stride = 100;
        assert!(overlap.validate().is_err(), "blocks overlap");
        let mut overflow = ok.clone();
        overflow.tensors[0].base = u64::MAX - 10;
        assert!(overflow.validate().is_err(), "addresses overflow");
        let mut empty_page = ok.clone();
        empty_page.tensors[0].page = 0;
        assert!(empty_page.validate().is_err());
        let mut none = ok.clone();
        none.num_blocks = 0;
        assert!(none.validate().is_err());
        let mut huge = ok.clone();
        huge.tensors = vec![
            TensorRun {
                base: 0,
                stride: u32::MAX as u64,
                page: u32::MAX
            };
            20
        ];
        assert!(huge.validate().is_err(), "payload above MAX_BLOCK_BYTES");
        // The decoder applies the same checks.
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = Request::Layout {
            req_id: 1,
            seq: 1,
            layout: ok,
        }
        .encode_into(&mut buf)
        .expect("encode");
        let first_stride = 1 + 1 + 8 + 8 + 4 + 4 + 4 + 2 + 8;
        buf[first_stride..first_stride + 8].copy_from_slice(&1u64.to_le_bytes());
        assert!(Request::decode(&buf[..n]).is_err());
    }

    #[test]
    fn block_pieces_follow_the_layout() {
        let l = glm_layout();
        let pieces: Vec<_> = l.block_pieces(10).expect("in range").collect();
        assert_eq!(pieces.len(), 99);
        assert_eq!(pieces[0], (l.tensors[0].base + 10 * 36_864, 36_864, 0));
        assert_eq!(pieces[78].2, 78 * 36_864);
        let total: u64 = pieces.iter().map(|p| p.1 as u64).sum();
        assert_eq!(total, l.block_bytes());
        assert!(l.block_pieces(43_600).is_none());
    }

    #[test]
    fn truncated_trailing_and_unknown_messages_are_refused() {
        let req = Request::Get {
            req_id: 1,
            seq: 1,
            namespace: [0; NAMESPACE_LEN],
            layout_id: 1,
            budget_ms: 1,
            entries: vec![GetEntry {
                key: key(1, 36),
                block: 2,
            }],
        };
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = req.encode_into(&mut buf).expect("encode");
        for cut in 0..n {
            assert!(Request::decode(&buf[..cut]).is_err(), "truncated at {cut}");
        }
        assert!(Request::decode(&buf[..n + 1]).is_err(), "a trailing byte");
        buf[1] = 99;
        assert!(Request::decode(&buf[..n]).is_err(), "unknown opcode");
        buf[1] = OP_GET;
        buf[0] = KV_PROTO_VERSION + 1;
        assert!(Request::decode(&buf[..n]).is_err(), "another version");
        assert!(
            Response::decode(&[KV_PROTO_VERSION, 42, 0, 0, 0, 0, 0, 0, 0, 0]).is_err(),
            "unknown tag"
        );
    }

    #[test]
    fn a_small_buffer_is_an_error_not_a_panic() {
        let req = Request::Head {
            req_id: 1,
            seq: 1,
            namespace: [0; NAMESPACE_LEN],
            keys: vec![key(1, 36)],
        };
        let mut tiny = [0u8; 10];
        assert!(req.encode_into(&mut tiny).is_err());
    }

    #[test]
    fn an_oversized_signed_message_is_refused() {
        assert!(decode_signed(&vec![0u8; KV_MSG_SIZE + 1]).is_err());
        // A body length past the end of the message.
        let mut buf = vec![0u8; 4 + 8 + 8 + crate::auth::SIGNATURE_LEN + 4];
        let at = buf.len() - 4;
        buf[at..].copy_from_slice(&100u32.to_le_bytes());
        assert!(decode_signed(&buf).is_err());
    }

    #[test]
    fn budgets_must_be_set_and_bounded() {
        let mut buf = [0u8; KV_MSG_SIZE];
        for ms in [0, MAX_BUDGET_MS + 1] {
            let req = Request::Get {
                req_id: 1,
                seq: 1,
                namespace: [0; NAMESPACE_LEN],
                layout_id: 1,
                budget_ms: ms,
                entries: vec![GetEntry {
                    key: key(1, 36),
                    block: 0,
                }],
            };
            assert!(req.encode_into(&mut buf).is_err(), "{ms} ms");
        }
        let ok = Request::Get {
            req_id: 1,
            seq: 1,
            namespace: [0; NAMESPACE_LEN],
            layout_id: 1,
            budget_ms: 1,
            entries: vec![GetEntry {
                key: key(1, 36),
                block: 0,
            }],
        };
        let n = ok.encode_into(&mut buf).expect("encode");
        let at = 1 + 1 + 8 + 8 + NAMESPACE_LEN + 4;
        buf[at..at + 4].copy_from_slice(&0u32.to_le_bytes());
        assert!(
            Request::decode(&buf[..n]).is_err(),
            "a zero budget on the wire"
        );
    }

    #[test]
    fn responses_refuse_truncation_trailing_bytes_and_bad_counts() {
        let resp = Response::Get {
            req_id: 1,
            results: vec![
                GetResult {
                    status: STATUS_OK,
                    length: 1,
                    check: 2
                };
                3
            ],
        };
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = resp.encode_into(&mut buf).expect("encode");
        for cut in 0..n {
            assert!(Response::decode(&buf[..cut]).is_err(), "truncated at {cut}");
        }
        assert!(Response::decode(&buf[..n + 1]).is_err(), "a trailing byte");
        buf[1 + 1 + 8..1 + 1 + 8 + 2].copy_from_slice(&u16::MAX.to_le_bytes());
        assert!(
            Response::decode(&buf[..n]).is_err(),
            "count above MAX_BATCH"
        );
        assert!(
            Response::Put {
                req_id: 1,
                statuses: vec![]
            }
            .encode_into(&mut buf)
            .is_err(),
            "empty batch"
        );
        assert!(Response::Head {
            req_id: 1,
            statuses: vec![0; MAX_BATCH + 1]
        }
        .encode_into(&mut buf)
        .is_err());
    }

    #[test]
    fn a_reply_must_match_its_request() {
        let req = Request::Head {
            req_id: 5,
            seq: 1,
            namespace: [0; NAMESPACE_LEN],
            keys: vec![key(1, 36), key(2, 36)],
        };
        assert!(req.answered_by(&Response::Head {
            req_id: 5,
            statuses: vec![STATUS_OK, STATUS_MISS]
        }));
        assert!(req.answered_by(&Response::Err {
            req_id: 5,
            status: STATUS_UNAUTHORIZED
        }));
        assert!(
            !req.answered_by(&Response::Head {
                req_id: 5,
                statuses: vec![STATUS_OK]
            }),
            "one result short"
        );
        assert!(
            !req.answered_by(&Response::Head {
                req_id: 6,
                statuses: vec![STATUS_OK, STATUS_OK]
            }),
            "another req_id"
        );
        assert!(
            !req.answered_by(&Response::Put {
                req_id: 5,
                statuses: vec![STATUS_OK, STATUS_OK]
            }),
            "another kind"
        );
    }

    #[test]
    fn signed_envelopes_refuse_bad_keys_trailing_bytes_and_bad_bodies() {
        let creds = Credentials::new("key", "secret");
        let req = Request::Head {
            req_id: 1,
            seq: 1,
            namespace: [0; NAMESPACE_LEN],
            keys: vec![key(1, 36)],
        };
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = encode_signed_into(&req, &creds, 1, &mut buf).expect("encode");
        assert!(
            decode_signed(&buf[..n + 1]).is_err(),
            "unsigned bytes after the body"
        );
        let mut bad_utf8 = buf;
        bad_utf8[4] = 0xff;
        assert!(
            decode_signed(&bad_utf8[..n]).is_err(),
            "access key not UTF-8"
        );
        let mut long_key = buf;
        long_key[..4].copy_from_slice(&65u32.to_le_bytes());
        assert!(
            decode_signed(&long_key[..n]).is_err(),
            "access key over 64 bytes"
        );
        let mut bad_body = buf;
        let body_at = 4 + 3 + 8 + 8 + crate::auth::SIGNATURE_LEN + 4;
        bad_body[body_at] = KV_PROTO_VERSION + 1;
        assert!(
            decode_signed(&bad_body[..n]).is_err(),
            "a body that does not decode"
        );
        let too_long = Credentials::new("k".repeat(65), "s");
        assert!(encode_signed_into(&req, &too_long, 1, &mut buf).is_err());
    }

    #[test]
    fn layout_edge_cases() {
        // One block: any stride is harmless, only block 0 exists.
        let one = Layout {
            layout_id: 1,
            rkey: 1,
            num_blocks: 1,
            tensors: vec![TensorRun {
                base: 0,
                stride: 0,
                page: 4096,
            }],
        };
        assert!(one.validate().is_ok());
        assert_eq!(one.block_pieces(0).expect("block 0").count(), 1);
        assert!(one.block_pieces(1).is_none());
        // (n - 1) * stride overflows although base and page are small.
        let wide = Layout {
            layout_id: 1,
            rkey: 1,
            num_blocks: u32::MAX,
            tensors: vec![TensorRun {
                base: 0,
                stride: 1 << 40,
                page: 1,
            }],
        };
        assert!(wide.validate().is_err());
        // The last block of a layout ending just below u64::MAX.
        let page = 4096u32;
        let base = u64::MAX - (9 * 8192 + page as u64);
        let high = Layout {
            layout_id: 1,
            rkey: 1,
            num_blocks: 10,
            tensors: vec![TensorRun {
                base,
                stride: 8192,
                page,
            }],
        };
        assert!(high.validate().is_ok());
        let (addr, len, _) = high.block_pieces(9).expect("last").next().expect("piece");
        assert_eq!(addr.checked_add(len as u64), Some(u64::MAX));
        // A tensor count of 0 or above MAX_TENSORS on the wire.
        let mut buf = [0u8; KV_MSG_SIZE];
        let n = Request::Layout {
            req_id: 1,
            seq: 1,
            layout: one,
        }
        .encode_into(&mut buf)
        .expect("encode");
        let count_at = 1 + 1 + 8 + 8 + 4 + 4 + 4;
        for count in [0u16, MAX_TENSORS as u16 + 1] {
            buf[count_at..count_at + 2].copy_from_slice(&count.to_le_bytes());
            assert!(Request::decode(&buf[..n]).is_err(), "{count} tensors");
        }
    }
}
