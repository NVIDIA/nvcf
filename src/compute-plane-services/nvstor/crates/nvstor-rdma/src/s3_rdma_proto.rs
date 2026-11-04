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

//! S3-over-RDMA control-plane wire format.
//!
//! Two-sided SEND/RECV messages travel between client and server over
//! the same QP that carries one-sided WRITE/READ for bulk data. Each
//! message fits in one SGE of [`CONTROL_BUF_SIZE`] bytes, so we lay it
//! out as a packed little-endian byte string — no bincode, no
//! prost, no per-op heap allocations on the control path.
//!
//! The format is versioned with the connection bootstrap
//! (`crate::bootstrap::PROTOCOL_VERSION`): peers that disagree fail the
//! handshake with a clear error before any message here is exchanged.
//!
//! Two workloads share this protocol:
//!
//! ```text
//! 1. **Training data fanout / model hydration** (server pushes to
//!    client RMR, one range per request):
//!      client → Request::Get { key, offset, if_generation,
//!                                client_addr, client_rkey, client_len }
//!      server: read_fn(key, offset) → bytes straight into a pool slot
//!              RDMA WRITE(bytes → client_addr/rkey)
//!      server → Response::Range { req_id, length, object_size,
//!                                 generation, crc32c }
//!    At most min(client_len, server slot size) bytes per range; fewer
//!    only at end of object. `crc32c` (Castagnoli) covers exactly the
//!    bytes written. A non-zero `if_generation` that no longer matches
//!    fails with STATUS_GENERATION_CHANGED before any WRITE. HEAD
//!    returns the same Range shape with length 0.
//!
//! 2. **Checkpoint write at scale** (server pulls from the client):
//!      client → Request::Alloc { size }
//!      server: reserves a pool slot (admission), exposes nothing
//!      server → Response::Slot { req_id, slot, length }
//!      client → Request::Persist { key, slot, client_addr,
//!                                  client_rkey, length, crc32c }
//!      server: RDMA READ(client_addr/rkey → the reserved slot),
//!              check crc32c, then write_fn(key, slot bytes)
//!      server → Response::Ok { req_id, length, etag }
//!    `Replicate` is the same exchange for a cluster peer's replica
//!    write; it reaches a separate store callback and only peers that
//!    authenticated as cluster members may send it.
//! ```
//!
//! The server never hands out an address or rkey of its own memory: the
//! client describes its buffer in the signed PERSIST request and the
//! server reads it. Server QPs grant peers no remote access at all.
//!
//! Each Request carries a `req_id` so a client driving many parallel
//! ops can correlate responses; the response echoes it back.

use std::io;

/// Opcodes. Stable wire constants — never renumber.
pub const OP_GET: u8 = 1;
pub const OP_ALLOC: u8 = 2;
pub const OP_PERSIST: u8 = 3;
pub const OP_DELETE: u8 = 4;
pub const OP_HEAD: u8 = 5;
pub const OP_RAILS: u8 = 6;
pub const OP_REPLICATE: u8 = 7;

/// Most rails (server NICs) one `Rails` response can list.
pub const MAX_RAILS: usize = 16;

// ---------------------------------------------------------------------
// Control-reply deadlines. Every bound that a server spends before it
// answers a request, and the client bound that must exceed their sum,
// in one place.
//
// The client holds one request per peer at a time (the per-peer control
// transaction), so a request's reply waits only on its own handling:
//
//   ADMIT_WAIT             1 s  dispatcher waits for a worker permit;
//                               past it the server answers NO_SLOT
//   SLOT_WAIT              2 s  GET / ALLOC wait for a pool slot; past
//                               it the server answers NO_SLOT
//   STORAGE_ALLOWANCE     10 s  one storage callback (read_fn, write_fn,
//                               replicate_fn). Not enforced: storage
//                               slower than this gets the client
//                               disconnected (it reconnects and retries)
//   LANE_OP_WORST         10 s  one RDMA operation on the success path:
//                               credits (5 s) + completion (5 s), see
//                               `transport::LANE_LIMITS`. A range is one
//                               slot, posted as one chain unless
//                               `set_max_wr_bytes` is below slot/128 (a
//                               benchmark setting)
//
//   SERVER_REPLY_WORST = ADMIT_WAIT + SLOT_WAIT + STORAGE_ALLOWANCE
//                      + LANE_OP_WORST (the GET WRITE / PERSIST READ)
//                      + LANE_OP_WORST (the reply SEND)
//                      = 1 + 2 + 10 + 10 + 10 = 33 s
//   CONTROL_REPLY_TIMEOUT = SERVER_REPLY_WORST + REPLY_MARGIN (5 s) = 38 s
//
// The client's reply wait starts after its request SEND has completed,
// so the server cannot have started earlier than that. A reply that
// misses CONTROL_REPLY_TIMEOUT fails the lane and the peer is removed
// (a late reply would otherwise pair with the next request).
// ---------------------------------------------------------------------

/// Server: longest wait for a worker permit before answering NO_SLOT.
pub const ADMIT_WAIT: std::time::Duration = std::time::Duration::from_secs(1);
/// Server: longest wait for a pool slot (GET, ALLOC) before NO_SLOT.
pub const SLOT_WAIT: std::time::Duration = std::time::Duration::from_secs(2);
/// Time allowed for one storage callback (documented, not enforced).
pub const STORAGE_ALLOWANCE: std::time::Duration = std::time::Duration::from_secs(10);
/// One RDMA operation on the success path: credit wait + completion wait.
pub const LANE_OP_WORST: std::time::Duration = std::time::Duration::from_secs(
    crate::transport::LANE_LIMITS.credits.as_secs()
        + crate::transport::LANE_LIMITS.completion.as_secs(),
);
/// Longest a healthy server takes to answer a request (see above).
pub const SERVER_REPLY_WORST: std::time::Duration = std::time::Duration::from_secs(
    ADMIT_WAIT.as_secs()
        + SLOT_WAIT.as_secs()
        + STORAGE_ALLOWANCE.as_secs()
        + 2 * LANE_OP_WORST.as_secs(),
);
/// Slack between the server's worst case and the client's bound.
pub const REPLY_MARGIN: std::time::Duration = std::time::Duration::from_secs(5);
/// How long a client waits for the reply to a sent request.
pub const CONTROL_REPLY_TIMEOUT: std::time::Duration =
    std::time::Duration::from_secs(SERVER_REPLY_WORST.as_secs() + REPLY_MARGIN.as_secs());

const _: () = assert!(
    CONTROL_REPLY_TIMEOUT.as_secs() > SERVER_REPLY_WORST.as_secs(),
    "the client must outwait the server's worst case"
);
const _: () = assert!(
    crate::transport::LANE_LIMITS.credits.subsec_nanos() == 0
        && crate::transport::LANE_LIMITS.completion.subsec_nanos() == 0,
    "LANE_OP_WORST counts whole seconds"
);

/// Response status codes.
pub const STATUS_OK: u8 = 0;
pub const STATUS_NOT_FOUND: u8 = 1;
pub const STATUS_ERROR: u8 = 2;
pub const STATUS_NO_SLOT: u8 = 3; // pool exhausted, retry later
/// GET's `if_generation` no longer matches: the object was replaced, so
/// the client must restart the whole object, not just this range.
pub const STATUS_GENERATION_CHANGED: u8 = 5;
/// This server does not serve the verb: a read-only export asked to
/// ALLOC / PERSIST / DELETE, or a server without a replica store asked
/// to REPLICATE.
pub const STATUS_UNSUPPORTED: u8 = 6;
/// The bytes the server pulled for PERSIST / REPLICATE do not match the
/// CRC-32C the client signed; nothing was stored.
pub const STATUS_CHECKSUM_MISMATCH: u8 = 7;

/// Hard cap on key length. Large enough for any path under Linux
/// `PATH_MAX` (4096 bytes including the NUL) plus a short key prefix
/// such as a manifest marker, so an exported directory tree never lists
/// a file it cannot serve.
pub const MAX_KEY_LEN: usize = 4096 + 64;

/// Cap on access-key length in the auth envelope.
pub const MAX_ACCESS_KEY_LEN: usize = 64;

/// Largest request body: GET with a max-length key (op 1, req_id 8, key
/// length 4 + key, offset 8, generation 16, addr 8, rkey 4, len 8).
/// PERSIST / REPLICATE are 12 bytes smaller.
pub const MAX_REQUEST_BODY: usize = 1 + 8 + 4 + MAX_KEY_LEN + 8 + 16 + 8 + 4 + 8;

/// Signed envelope around a body: access key (length 4 + key), timestamp
/// 8, nonce 8, signature 32, body length 4.
pub const SIGNED_ENVELOPE_OVERHEAD: usize =
    4 + MAX_ACCESS_KEY_LEN + 8 + 8 + crate::auth::SIGNATURE_LEN + 4;

/// Pre-posted RECV buffer size. Every control message fits.
///
/// Sized for the largest signed request (a max-length key plus the
/// envelope), rounded up to 256 bytes. An undersized RECV buffer fails
/// the SEND at the NIC (local length error) rather than truncating, so
/// this bound is checked at compile time below.
pub const CONTROL_BUF_SIZE: usize =
    (MAX_REQUEST_BODY + SIGNED_ENVELOPE_OVERHEAD).div_ceil(256) * 256;

const _: () = assert!(MAX_REQUEST_BODY + SIGNED_ENVELOPE_OVERHEAD <= CONTROL_BUF_SIZE);

/// Rejected: request was unsigned, mis-signed, replayed, or denied by
/// policy. Deliberately one code for all of them — distinguishing
/// "unknown key" from "bad signature" on the wire turns the endpoint into
/// a key-enumeration oracle.
pub const STATUS_UNAUTHORIZED: u8 = 4;

/// Client → Server.
#[derive(Debug, Clone, PartialEq)]
pub enum Request {
    /// Fetch up to `client_len` bytes of `key` from `offset`; the server
    /// RDMA-WRITEs them into client_addr/rkey. `if_generation` is 0 for
    /// "any generation".
    Get {
        req_id: u64,
        key: String,
        offset: u64,
        if_generation: u128,
        client_addr: u64,
        client_rkey: u32,
        client_len: u64,
    },
    /// Reserve a server pool slot for a `size`-byte PUT. The server
    /// answers with an opaque slot token, never an address or rkey.
    Alloc { req_id: u64, size: u64 },
    /// Store `length` bytes under `key`: the server RDMA-READs them from
    /// `client_addr`/`client_rkey` into the slot `slot` reserved, checks
    /// them against `crc32c`, then persists them. The slot returns to the
    /// pool when the request finishes.
    Persist {
        req_id: u64,
        key: String,
        slot: u64,
        client_addr: u64,
        client_rkey: u32,
        length: u64,
        crc32c: u32,
    },
    /// `Persist` into the server's replica store (cluster replication).
    /// Served only to peers that authenticated as cluster members.
    Replicate {
        req_id: u64,
        key: String,
        slot: u64,
        client_addr: u64,
        client_rkey: u32,
        length: u64,
        crc32c: u32,
    },
    /// Delete `key` from the chunk store.
    Delete { req_id: u64, key: String },
    /// Return object size + etag without moving any data.
    Head { req_id: u64, key: String },
    /// List the server's rails (one per NIC) so a multi-NIC client can
    /// spread connections across them. Authorized as a read of `key`,
    /// the object the client is about to fetch.
    Rails { req_id: u64, key: String },
}

/// Server → Client.
#[derive(Debug, Clone, PartialEq)]
pub enum Response {
    /// GET / HEAD result: `length` bytes were written to the client
    /// buffer (0 for HEAD).
    Range {
        req_id: u64,
        length: u64,
        object_size: u64,
        generation: u128,
        crc32c: u32,
    },
    /// Successful completion of PERSIST / DELETE.
    Ok {
        req_id: u64,
        length: u64,
        etag: [u8; 32],
    },
    /// Pool slot reservation. Comes back from ALLOC: an opaque token to
    /// name in PERSIST / REPLICATE, and the most bytes the slot holds.
    Slot { req_id: u64, slot: u64, length: u64 },
    /// RAILS result: the server's NICs, rail 0 (the one dialed) first,
    /// and the server process's random instance id. The id is an optional
    /// trailing field (marker byte 1, then 16 bytes): a server that does
    /// not send it leaves it `None`, and a decoder that does not know it
    /// ignores the trailing bytes, so both directions stay compatible.
    /// Trailing fields are ordered: the instance id, when present, is
    /// always first, right after the rails. Any future trailing field must
    /// come after it and be TLV (a marker byte, a length, then that many
    /// bytes), so a decoder can skip markers it does not know.
    Rails {
        req_id: u64,
        rails: Vec<crate::rail::RailInfo>,
        instance: Option<[u8; 16]>,
    },
    /// Operation failed. Status is one of `STATUS_NOT_FOUND`,
    /// `STATUS_ERROR`, `STATUS_NO_SLOT`.
    Err { req_id: u64, status: u8 },
}

// ---------------------------------------------------------------------
// Encode / decode. All little-endian. No allocations on the hot path
// (`encode_into` takes a caller-owned buffer slice).
// ---------------------------------------------------------------------

impl Request {
    /// Serialize into `buf`, returning the number of bytes written.
    /// `buf` must be at least `CONTROL_BUF_SIZE` long.
    pub fn encode_into(&self, buf: &mut [u8]) -> io::Result<usize> {
        if buf.len() < CONTROL_BUF_SIZE {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "control buffer too small",
            ));
        }
        let mut w = Writer::new(buf);
        match self {
            Request::Get {
                req_id,
                key,
                offset,
                if_generation,
                client_addr,
                client_rkey,
                client_len,
            } => {
                w.u8(OP_GET);
                w.u64(*req_id);
                w.string(key)?;
                w.u64(*offset);
                w.u128(*if_generation);
                w.u64(*client_addr);
                w.u32(*client_rkey);
                w.u64(*client_len);
            }
            Request::Alloc { req_id, size } => {
                w.u8(OP_ALLOC);
                w.u64(*req_id);
                w.u64(*size);
            }
            Request::Persist {
                req_id,
                key,
                slot,
                client_addr,
                client_rkey,
                length,
                crc32c,
            } => {
                w.u8(OP_PERSIST);
                w.u64(*req_id);
                w.pull(key, *slot, *client_addr, *client_rkey, *length, *crc32c)?;
            }
            Request::Replicate {
                req_id,
                key,
                slot,
                client_addr,
                client_rkey,
                length,
                crc32c,
            } => {
                w.u8(OP_REPLICATE);
                w.u64(*req_id);
                w.pull(key, *slot, *client_addr, *client_rkey, *length, *crc32c)?;
            }
            Request::Delete { req_id, key } => {
                w.u8(OP_DELETE);
                w.u64(*req_id);
                w.string(key)?;
            }
            Request::Head { req_id, key } => {
                w.u8(OP_HEAD);
                w.u64(*req_id);
                w.string(key)?;
            }
            Request::Rails { req_id, key } => {
                w.u8(OP_RAILS);
                w.u64(*req_id);
                w.string(key)?;
            }
        }
        Ok(w.pos)
    }

    pub fn decode(buf: &[u8]) -> io::Result<Request> {
        let mut r = Reader::new(buf);
        let op = r.u8()?;
        let req_id = r.u64()?;
        match op {
            OP_GET => {
                let key = r.string()?;
                let offset = r.u64()?;
                let if_generation = r.u128()?;
                let client_addr = r.u64()?;
                let client_rkey = r.u32()?;
                let client_len = r.u64()?;
                Ok(Request::Get {
                    req_id,
                    key,
                    offset,
                    if_generation,
                    client_addr,
                    client_rkey,
                    client_len,
                })
            }
            OP_ALLOC => {
                let size = r.u64()?;
                Ok(Request::Alloc { req_id, size })
            }
            OP_PERSIST => {
                let (key, slot, client_addr, client_rkey, length, crc32c) = r.pull()?;
                Ok(Request::Persist {
                    req_id,
                    key,
                    slot,
                    client_addr,
                    client_rkey,
                    length,
                    crc32c,
                })
            }
            OP_REPLICATE => {
                let (key, slot, client_addr, client_rkey, length, crc32c) = r.pull()?;
                Ok(Request::Replicate {
                    req_id,
                    key,
                    slot,
                    client_addr,
                    client_rkey,
                    length,
                    crc32c,
                })
            }
            OP_DELETE => {
                let key = r.string()?;
                Ok(Request::Delete { req_id, key })
            }
            OP_HEAD => {
                let key = r.string()?;
                Ok(Request::Head { req_id, key })
            }
            OP_RAILS => {
                let key = r.string()?;
                Ok(Request::Rails { req_id, key })
            }
            other => Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("unknown op {other}"),
            )),
        }
    }
}

// ---------------------------------------------------------------------------
// Signed envelope
//
// Layout: [access_key][timestamp][nonce][signature][body_len][body]
//
// The body is the ordinary `Request::encode_into` output, and the
// signature covers it verbatim. Wrapping rather than threading auth
// fields through all five variants keeps the two concerns separable: the
// request codec is unchanged, and a variant added later is signed
// automatically.
// ---------------------------------------------------------------------------

/// Encode `req` wrapped in a signed envelope.
pub fn encode_signed_into(
    req: &Request,
    creds: &crate::auth::Credentials,
    nonce: u64,
    buf: &mut [u8],
) -> io::Result<usize> {
    // Encode the body first — the signature has to cover the exact bytes
    // that go on the wire, so it cannot be computed until they exist.
    let mut body = [0u8; CONTROL_BUF_SIZE];
    let body_len = req.encode_into(&mut body)?;
    let header = crate::auth::sign_request(creds, nonce, &body[..body_len]);

    let mut w = Writer::new(buf);
    w.string_max(&header.access_key, MAX_ACCESS_KEY_LEN)?;
    w.u64(header.timestamp);
    w.u64(header.nonce);
    w.bytes(&header.signature);
    w.u32(body_len as u32);
    w.bytes(&body[..body_len]);
    Ok(w.pos)
}

/// Decode a signed envelope into its header, the decoded request, and the
/// raw body bytes the signature must be verified against.
///
/// Verification is NOT done here: this crate has no access to secrets. The
/// caller hands the returned body to its `AuthFn`.
pub fn decode_signed(buf: &[u8]) -> io::Result<(crate::auth::AuthHeader, Request, Vec<u8>)> {
    let mut r = Reader::new(buf);
    let access_key = r.string()?;
    if access_key.len() > MAX_ACCESS_KEY_LEN {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "access key too long",
        ));
    }
    let timestamp = r.u64()?;
    let nonce = r.u64()?;
    let mut signature = [0u8; crate::auth::SIGNATURE_LEN];
    r.bytes_into(&mut signature)?;
    let body_len = r.u32()? as usize;
    if body_len > CONTROL_BUF_SIZE {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "body too long"));
    }
    let mut body = vec![0u8; body_len];
    r.bytes_into(&mut body)?;

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

impl Response {
    /// Tag byte for the response variant (separate from request ops).
    const TAG_OK: u8 = 0;
    const TAG_SLOT: u8 = 1;
    const TAG_ERR: u8 = 2;
    const TAG_RANGE: u8 = 3;
    const TAG_RAILS: u8 = 4;
    /// Marker of the RAILS reply's optional instance id.
    const RAILS_INSTANCE: u8 = 1;

    pub fn encode_into(&self, buf: &mut [u8]) -> io::Result<usize> {
        if buf.len() < CONTROL_BUF_SIZE {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "control buffer too small",
            ));
        }
        let mut w = Writer::new(buf);
        match self {
            Response::Ok {
                req_id,
                length,
                etag,
            } => {
                w.u8(Self::TAG_OK);
                w.u64(*req_id);
                w.u64(*length);
                w.bytes(etag);
            }
            Response::Range {
                req_id,
                length,
                object_size,
                generation,
                crc32c,
            } => {
                w.u8(Self::TAG_RANGE);
                w.u64(*req_id);
                w.u64(*length);
                w.u64(*object_size);
                w.u128(*generation);
                w.u32(*crc32c);
            }
            Response::Slot {
                req_id,
                slot,
                length,
            } => {
                w.u8(Self::TAG_SLOT);
                w.u64(*req_id);
                w.u64(*slot);
                w.u64(*length);
            }
            Response::Rails {
                req_id,
                rails,
                instance,
            } => {
                if rails.len() > MAX_RAILS {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidInput,
                        format!("{} rails exceeds MAX_RAILS ({MAX_RAILS})", rails.len()),
                    ));
                }
                w.u8(Self::TAG_RAILS);
                w.u64(*req_id);
                w.u8(rails.len() as u8);
                for rail in rails {
                    w.u16(rail.port);
                    w.bytes(&rail.gid);
                }
                if let Some(id) = instance {
                    w.u8(Self::RAILS_INSTANCE);
                    w.bytes(id);
                }
            }
            Response::Err { req_id, status } => {
                w.u8(Self::TAG_ERR);
                w.u64(*req_id);
                w.u8(*status);
            }
        }
        Ok(w.pos)
    }

    pub fn decode(buf: &[u8]) -> io::Result<Response> {
        let mut r = Reader::new(buf);
        let tag = r.u8()?;
        let req_id = r.u64()?;
        match tag {
            Self::TAG_OK => {
                let length = r.u64()?;
                let mut etag = [0u8; 32];
                r.bytes_into(&mut etag)?;
                Ok(Response::Ok {
                    req_id,
                    length,
                    etag,
                })
            }
            Self::TAG_RANGE => {
                let length = r.u64()?;
                let object_size = r.u64()?;
                let generation = r.u128()?;
                let crc32c = r.u32()?;
                Ok(Response::Range {
                    req_id,
                    length,
                    object_size,
                    generation,
                    crc32c,
                })
            }
            Self::TAG_SLOT => {
                let slot = r.u64()?;
                let length = r.u64()?;
                Ok(Response::Slot {
                    req_id,
                    slot,
                    length,
                })
            }
            Self::TAG_RAILS => {
                let n = r.u8()? as usize;
                if n > MAX_RAILS {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        format!("{n} rails exceeds MAX_RAILS ({MAX_RAILS})"),
                    ));
                }
                let mut rails = Vec::with_capacity(n);
                for _ in 0..n {
                    let port = r.u16()?;
                    let mut gid = [0u8; 16];
                    r.bytes_into(&mut gid)?;
                    rails.push(crate::rail::RailInfo { port, gid });
                }
                // Optional trailing fields; unknown ones are ignored.
                let mut instance = None;
                if r.remaining() >= 17 && r.u8()? == Self::RAILS_INSTANCE {
                    let mut id = [0u8; 16];
                    r.bytes_into(&mut id)?;
                    instance = Some(id);
                }
                Ok(Response::Rails {
                    req_id,
                    rails,
                    instance,
                })
            }
            Self::TAG_ERR => {
                let status = r.u8()?;
                Ok(Response::Err { req_id, status })
            }
            other => Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("unknown response tag {other}"),
            )),
        }
    }
}

// ---------------------------------------------------------------------
// Minimal LE writer/reader. Hand-rolled to avoid bincode / prost on
// the control path; control messages are 30-60 bytes each so the
// cost saved per op shows up at high request rates.
// ---------------------------------------------------------------------

struct Writer<'a> {
    buf: &'a mut [u8],
    pos: usize,
}
impl<'a> Writer<'a> {
    fn new(buf: &'a mut [u8]) -> Self {
        Self { buf, pos: 0 }
    }
    fn u8(&mut self, v: u8) {
        self.buf[self.pos] = v;
        self.pos += 1;
    }
    fn u16(&mut self, v: u16) {
        self.buf[self.pos..self.pos + 2].copy_from_slice(&v.to_le_bytes());
        self.pos += 2;
    }
    fn u32(&mut self, v: u32) {
        self.buf[self.pos..self.pos + 4].copy_from_slice(&v.to_le_bytes());
        self.pos += 4;
    }
    fn u64(&mut self, v: u64) {
        self.buf[self.pos..self.pos + 8].copy_from_slice(&v.to_le_bytes());
        self.pos += 8;
    }
    fn u128(&mut self, v: u128) {
        self.u64(v as u64);
        self.u64((v >> 64) as u64);
    }
    fn bytes(&mut self, v: &[u8]) {
        self.buf[self.pos..self.pos + v.len()].copy_from_slice(v);
        self.pos += v.len();
    }
    /// Write a length-prefixed string with an explicit cap.
    fn string_max(&mut self, s: &str, max: usize) -> io::Result<()> {
        if s.len() > max {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("string longer than max ({max}): {}", s.len()),
            ));
        }
        self.u32(s.len() as u32);
        self.bytes(s.as_bytes());
        Ok(())
    }

    fn string(&mut self, s: &str) -> io::Result<()> {
        if s.len() > MAX_KEY_LEN {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("key longer than MAX_KEY_LEN ({MAX_KEY_LEN}): {}", s.len()),
            ));
        }
        self.u32(s.len() as u32);
        self.bytes(s.as_bytes());
        Ok(())
    }

    /// The shared body of PERSIST and REPLICATE.
    fn pull(
        &mut self,
        key: &str,
        slot: u64,
        client_addr: u64,
        client_rkey: u32,
        length: u64,
        crc32c: u32,
    ) -> io::Result<()> {
        self.string(key)?;
        self.u64(slot);
        self.u64(client_addr);
        self.u32(client_rkey);
        self.u64(length);
        self.u32(crc32c);
        Ok(())
    }
}

struct Reader<'a> {
    buf: &'a [u8],
    pos: usize,
}
impl<'a> Reader<'a> {
    fn new(buf: &'a [u8]) -> Self {
        Self { buf, pos: 0 }
    }
    fn remaining(&self) -> usize {
        self.buf.len().saturating_sub(self.pos)
    }
    fn need(&self, n: usize) -> io::Result<()> {
        if self.pos + n > self.buf.len() {
            Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                format!("read {n} past buf end"),
            ))
        } else {
            Ok(())
        }
    }
    fn u8(&mut self) -> io::Result<u8> {
        self.need(1)?;
        let v = self.buf[self.pos];
        self.pos += 1;
        Ok(v)
    }
    fn u16(&mut self) -> io::Result<u16> {
        self.need(2)?;
        let v = u16::from_le_bytes([self.buf[self.pos], self.buf[self.pos + 1]]);
        self.pos += 2;
        Ok(v)
    }
    fn u32(&mut self) -> io::Result<u32> {
        self.need(4)?;
        let v = u32::from_le_bytes(
            self.buf[self.pos..self.pos + 4]
                .try_into()
                .expect("invariant: need(4) checked"),
        );
        self.pos += 4;
        Ok(v)
    }
    fn u64(&mut self) -> io::Result<u64> {
        self.need(8)?;
        let v = u64::from_le_bytes(
            self.buf[self.pos..self.pos + 8]
                .try_into()
                .expect("invariant: need(8) checked"),
        );
        self.pos += 8;
        Ok(v)
    }
    fn u128(&mut self) -> io::Result<u128> {
        let lo = self.u64()? as u128;
        let hi = self.u64()? as u128;
        Ok(lo | (hi << 64))
    }
    fn bytes_into(&mut self, dst: &mut [u8]) -> io::Result<()> {
        self.need(dst.len())?;
        dst.copy_from_slice(&self.buf[self.pos..self.pos + dst.len()]);
        self.pos += dst.len();
        Ok(())
    }
    fn string(&mut self) -> io::Result<String> {
        let len = self.u32()? as usize;
        if len > MAX_KEY_LEN {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("string len {len} exceeds MAX_KEY_LEN"),
            ));
        }
        self.need(len)?;
        let s = std::str::from_utf8(&self.buf[self.pos..self.pos + len])
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?
            .to_string();
        self.pos += len;
        Ok(s)
    }

    /// The shared body of PERSIST and REPLICATE:
    /// (key, slot, client_addr, client_rkey, length, crc32c).
    #[allow(clippy::type_complexity)]
    fn pull(&mut self) -> io::Result<(String, u64, u64, u32, u64, u32)> {
        let key = self.string()?;
        let slot = self.u64()?;
        let client_addr = self.u64()?;
        let client_rkey = self.u32()?;
        let length = self.u64()?;
        let crc32c = self.u32()?;
        Ok((key, slot, client_addr, client_rkey, length, crc32c))
    }
}

// ---------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // A signed envelope must round-trip, and the recovered body must
    // verify against the signer's secret.
    #[test]
    fn signed_envelope_round_trips_and_verifies() {
        let creds = crate::auth::Credentials::new("AKIA123", "secret");
        let req = Request::Persist {
            req_id: 9,
            key: "tenant-a/ckpt/step-100".into(),
            slot: 3,
            client_addr: 0xdead_beef,
            client_rkey: 7,
            length: 4096,
            crc32c: 0x1234_5678,
        };

        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let n = encode_signed_into(&req, &creds, 42, &mut buf).expect("encode");
        assert!(
            n <= CONTROL_BUF_SIZE,
            "signed request must fit the control buffer"
        );

        let (header, decoded, body) = decode_signed(&buf[..n]).expect("decode");
        assert_eq!(header.access_key, "AKIA123");
        assert_eq!(header.nonce, 42);
        assert_eq!(decoded, req);
        assert!(crate::auth::verify_signature(
            "secret",
            header.timestamp,
            header.nonce,
            &body,
            &header.signature
        ));
    }

    // The largest legal request must still fit — this is why
    // CONTROL_BUF_SIZE grew when signing was added.
    #[test]
    fn max_size_signed_request_fits_the_control_buffer() {
        let creds = crate::auth::Credentials::new("A".repeat(MAX_ACCESS_KEY_LEN), "secret");
        let req = Request::Persist {
            req_id: u64::MAX,
            key: "k".repeat(MAX_KEY_LEN),
            slot: u64::MAX,
            client_addr: u64::MAX,
            client_rkey: u32::MAX,
            length: u64::MAX,
            crc32c: u32::MAX,
        };
        let replicate = Request::Replicate {
            req_id: u64::MAX,
            key: "r".repeat(MAX_KEY_LEN),
            slot: u64::MAX,
            client_addr: u64::MAX,
            client_rkey: u32::MAX,
            length: u64::MAX,
            crc32c: u32::MAX,
        };
        let get = Request::Get {
            req_id: u64::MAX,
            key: "k".repeat(MAX_KEY_LEN),
            offset: u64::MAX,
            if_generation: u128::MAX,
            client_addr: u64::MAX,
            client_rkey: u32::MAX,
            client_len: u64::MAX,
        };
        for req in [req, replicate, get] {
            let mut buf = [0u8; CONTROL_BUF_SIZE];
            let n = encode_signed_into(&req, &creds, u64::MAX, &mut buf)
                .expect("max-size signed request must encode");
            assert!(
                n <= CONTROL_BUF_SIZE,
                "worst case {n} exceeds buffer {CONTROL_BUF_SIZE}"
            );
            let (_, decoded, _) = decode_signed(&buf[..n]).expect("decode");
            assert_eq!(decoded, req);
        }
        // GET is the largest body, and the computed bound is exact for it.
        let mut body = [0u8; CONTROL_BUF_SIZE];
        let get = Request::Get {
            req_id: 0,
            key: "k".repeat(MAX_KEY_LEN),
            offset: 0,
            if_generation: 0,
            client_addr: 0,
            client_rkey: 0,
            client_len: 0,
        };
        assert_eq!(
            get.encode_into(&mut body).expect("encode"),
            MAX_REQUEST_BODY
        );
    }

    // Any relative path a Linux filesystem can hold (PATH_MAX is
    // 4096 bytes including the NUL) must be a requestable key, with room
    // for a short prefix such as a manifest marker.
    #[test]
    fn path_max_keys_are_requestable() {
        let creds = crate::auth::Credentials::new("AK", "secret");
        let path: String = std::iter::repeat_n("d/", 2047).collect::<String>() + "f";
        assert_eq!(path.len(), 4095);
        for key in [path.clone(), format!("/manifest:{path}")] {
            let req = Request::Get {
                req_id: 1,
                key,
                offset: 0,
                if_generation: 0,
                client_addr: 1,
                client_rkey: 2,
                client_len: 3,
            };
            let mut buf = [0u8; CONTROL_BUF_SIZE];
            let n = encode_signed_into(&req, &creds, 1, &mut buf).expect("PATH_MAX key encodes");
            assert_eq!(decode_signed(&buf[..n]).expect("decodes").1, req);
        }
    }

    // A decoder must refuse a declared key length above the cap even
    // when the buffer would hold it, so a peer cannot make it allocate
    // or scan past what any legal request carries.
    #[test]
    fn decoder_rejects_key_length_above_the_cap() {
        let mut buf = vec![0u8; CONTROL_BUF_SIZE * 2];
        buf[0] = OP_HEAD;
        buf[9..13].copy_from_slice(&((MAX_KEY_LEN + 1) as u32).to_le_bytes());
        for b in &mut buf[13..13 + MAX_KEY_LEN + 1] {
            *b = b'k';
        }
        assert!(Request::decode(&buf).is_err());
    }

    // Flipping any byte of the body must break verification — that is what
    // stops a captured signature being re-pointed at another key.
    #[test]
    fn tampering_with_the_encoded_body_breaks_verification() {
        let creds = crate::auth::Credentials::new("AKIA123", "secret");
        let req = Request::Delete {
            req_id: 1,
            key: "victim/object".into(),
        };
        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let n = encode_signed_into(&req, &creds, 1, &mut buf).expect("encode");

        let (header, _, mut body) = decode_signed(&buf[..n]).expect("decode");
        let last = body.len() - 1;
        body[last] ^= 0xFF;
        assert!(!crate::auth::verify_signature(
            "secret",
            header.timestamp,
            header.nonce,
            &body,
            &header.signature
        ));
    }

    fn roundtrip_req(r: Request) {
        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let n = r.encode_into(&mut buf).expect("encode");
        let back = Request::decode(&buf[..n]).expect("decode");
        assert_eq!(r, back);
    }

    fn roundtrip_resp(r: Response) {
        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let n = r.encode_into(&mut buf).expect("encode");
        let back = Response::decode(&buf[..n]).expect("decode");
        assert_eq!(r, back);
    }

    #[test]
    fn request_get_round_trip() {
        roundtrip_req(Request::Get {
            req_id: 0xdeadbeef,
            key: "checkpoint/rank-3/step-100.pt".into(),
            offset: 7 << 33,
            if_generation: (0x1234_u128 << 64) | 0x5678,
            client_addr: 0x7fff_1234_0000,
            client_rkey: 0xabcd,
            client_len: 1 << 30,
        });
    }

    #[test]
    fn request_alloc_round_trip() {
        roundtrip_req(Request::Alloc {
            req_id: 1,
            size: 4 * 1024 * 1024,
        });
    }

    #[test]
    fn request_persist_round_trip() {
        roundtrip_req(Request::Persist {
            req_id: 42,
            key: "weights/layer.42".into(),
            slot: 17,
            client_addr: 0x7fff_5678_0000,
            client_rkey: 0x1234,
            length: 16 * 1024 * 1024,
            crc32c: 0xE306_9283,
        });
    }

    #[test]
    fn request_replicate_round_trip_is_distinct_from_persist() {
        let fields = |op: u8| {
            let (req_id, key, slot, client_addr, client_rkey, length, crc32c) = (
                5,
                "chunks/ab/cdef".to_string(),
                9,
                0x7f00_0000_1000,
                0x55,
                1 << 20,
                7,
            );
            if op == OP_PERSIST {
                Request::Persist {
                    req_id,
                    key,
                    slot,
                    client_addr,
                    client_rkey,
                    length,
                    crc32c,
                }
            } else {
                Request::Replicate {
                    req_id,
                    key,
                    slot,
                    client_addr,
                    client_rkey,
                    length,
                    crc32c,
                }
            }
        };
        roundtrip_req(fields(OP_REPLICATE));
        let mut a = [0u8; CONTROL_BUF_SIZE];
        let mut b = [0u8; CONTROL_BUF_SIZE];
        let na = fields(OP_PERSIST).encode_into(&mut a).expect("encode");
        let nb = fields(OP_REPLICATE).encode_into(&mut b).expect("encode");
        // Same body, different opcode: a signature over one never
        // authorizes the other.
        assert_eq!((a[0], b[0]), (OP_PERSIST, OP_REPLICATE));
        assert_eq!(a[1..na], b[1..nb]);
    }

    #[test]
    fn request_head_and_delete_round_trip() {
        roundtrip_req(Request::Head {
            req_id: 7,
            key: "k".into(),
        });
        roundtrip_req(Request::Delete {
            req_id: 7,
            key: "k".into(),
        });
    }

    #[test]
    fn response_ok_round_trip() {
        roundtrip_resp(Response::Ok {
            req_id: 99,
            length: 12_345,
            etag: [0x55; 32],
        });
    }

    #[test]
    fn response_range_round_trip() {
        roundtrip_resp(Response::Range {
            req_id: 98,
            length: 16 << 20,
            object_size: 40 << 30,
            generation: u128::MAX - 3,
            crc32c: 0xE306_9283,
        });
    }

    #[test]
    fn response_slot_round_trip() {
        roundtrip_resp(Response::Slot {
            req_id: 100,
            slot: 0xfeed,
            length: 4 * 1024 * 1024,
        });
    }

    // ALLOC's answer carries no server address or rkey, only an
    // opaque token and a length: 1 tag + req_id + slot + length.
    #[test]
    fn slot_response_exposes_no_server_memory() {
        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let n = Response::Slot {
            req_id: 1,
            slot: 2,
            length: 3,
        }
        .encode_into(&mut buf)
        .expect("encode");
        assert_eq!(n, 1 + 8 + 8 + 8);
    }

    #[test]
    fn rails_round_trip_and_cap() {
        roundtrip_req(Request::Rails {
            req_id: 5,
            key: "models/llm/shard-00".into(),
        });
        roundtrip_resp(Response::Rails {
            req_id: 5,
            rails: vec![],
            instance: None,
        });
        roundtrip_resp(Response::Rails {
            req_id: 5,
            rails: vec![],
            instance: Some([7; 16]),
        });
        let full: Vec<crate::rail::RailInfo> = (0..MAX_RAILS as u16)
            .map(|k| crate::rail::RailInfo {
                port: 9893 + k,
                gid: [k as u8; 16],
            })
            .collect();
        roundtrip_resp(Response::Rails {
            req_id: 6,
            rails: full.clone(),
            instance: Some([9; 16]),
        });

        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let mut over = full;
        over.push(crate::rail::RailInfo {
            port: 1,
            gid: [0; 16],
        });
        assert!(Response::Rails {
            req_id: 7,
            rails: over,
            instance: None
        }
        .encode_into(&mut buf)
        .is_err());
        // A decoder must not trust a count above the cap either.
        let n = Response::Rails {
            req_id: 8,
            rails: vec![],
            instance: None,
        }
        .encode_into(&mut buf)
        .expect("encode");
        buf[9] = MAX_RAILS as u8 + 1;
        assert!(Response::decode(&buf[..n]).is_err());
    }

    // The instance id is optional and trailing: a reply
    // without it (an older server) decodes with `None`, and a reply with
    // it is the old reply plus trailing bytes, which a decoder that does
    // not know the field never reads.
    #[test]
    fn rails_instance_is_an_optional_trailing_field() {
        let rails = vec![crate::rail::RailInfo {
            port: 9890,
            gid: [3; 16],
        }];
        let mut old = [0u8; CONTROL_BUF_SIZE];
        let n_old = Response::Rails {
            req_id: 1,
            rails: rails.clone(),
            instance: None,
        }
        .encode_into(&mut old)
        .expect("encode");
        let mut new = [0u8; CONTROL_BUF_SIZE];
        let n_new = Response::Rails {
            req_id: 1,
            rails: rails.clone(),
            instance: Some([5; 16]),
        }
        .encode_into(&mut new)
        .expect("encode");
        assert_eq!(n_new, n_old + 17);
        assert_eq!(
            &new[..n_old],
            &old[..n_old],
            "the old reply is a prefix of the new one"
        );
        assert_eq!(
            Response::decode(&new[..n_old]).expect("decode"),
            Response::Rails {
                req_id: 1,
                rails: rails.clone(),
                instance: None
            }
        );
        assert_eq!(
            Response::decode(&new[..n_new]).expect("decode"),
            Response::Rails {
                req_id: 1,
                rails,
                instance: Some([5; 16])
            }
        );
    }

    #[test]
    fn response_err_round_trip() {
        roundtrip_resp(Response::Err {
            req_id: 200,
            status: STATUS_NOT_FOUND,
        });
        roundtrip_resp(Response::Err {
            req_id: 201,
            status: STATUS_NO_SLOT,
        });
    }

    #[test]
    fn rejects_oversized_key() {
        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let too_long = "x".repeat(MAX_KEY_LEN + 1);
        let r = Request::Get {
            req_id: 1,
            key: too_long,
            offset: 0,
            if_generation: 0,
            client_addr: 0,
            client_rkey: 0,
            client_len: 0,
        };
        assert!(r.encode_into(&mut buf).is_err());
    }

    #[test]
    fn rejects_truncated_buffer() {
        // Encode a normal Get, then try to decode from a buffer that's
        // 5 bytes shorter than what was written. Must fail cleanly,
        // not panic.
        let mut buf = [0u8; CONTROL_BUF_SIZE];
        let r = Request::Get {
            req_id: 1,
            key: "k".into(),
            offset: 0,
            if_generation: 0,
            client_addr: 0,
            client_rkey: 0,
            client_len: 0,
        };
        let n = r.encode_into(&mut buf).unwrap();
        assert!(Request::decode(&buf[..n.saturating_sub(5)]).is_err());
    }

    #[test]
    fn rejects_unknown_op_byte() {
        // A full zeroed buffer, so the req_id read cannot trip first.
        let mut buf = [0u8; CONTROL_BUF_SIZE];
        buf[0] = 0xff; // not any known op
        assert!(Request::decode(&buf).is_err());
    }
}
