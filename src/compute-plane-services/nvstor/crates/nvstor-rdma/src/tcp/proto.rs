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

//! S3-over-TCP wire format. Length-prefixed binary frames over a
//! single TCP stream per peer. Designed for raw throughput, not
//! schema flexibility — every field is fixed-width and little-endian.
//!
//! Frame shape (control messages):
//!
//! ```text
//!   [u32 frame_len][u8 op][payload...]
//! ```
//!
//! For PUT, the request frame is immediately followed by the
//! payload bytes (out-of-band — the `frame_len` covers only the
//! control header, not the data). This lets pipelined PUTs reuse
//! the same socket without buffering whole objects in the framer.
//!
//! Verbs:
//!
//! ```text
//!   OP_PUT    = 1  Request:  req_id:u64 key_len:u32 key[..] length:u64
//!                  Followed by `length` bytes of payload on the same socket.
//!                  Response: Ok{req_id, length, crc32c} | Err{req_id, status}
//!                  The server streams the payload into storage (bounded
//!                  memory) and answers with the CRC-32C of the bytes it
//!                  received, which the client checks against its own.
//!
//!   OP_GET    = 2  Request:  req_id:u64 key_len:u32 key[..] offset:u64
//!                            length:u64 if_generation:u128
//!                  Response: Range{req_id, length, object_size, generation,
//!                            crc32c} | Err
//!                  Followed by `length` bytes of payload (if Range).
//!                  Ranged: the server returns at most
//!                  min(length, MAX_GET_RANGE) bytes from `offset`; fewer
//!                  only at end of object. `crc32c` (Castagnoli) covers
//!                  exactly the payload bytes. `if_generation` != 0 makes
//!                  the server reject with STATUS_GENERATION_CHANGED if the
//!                  object was replaced, before any payload moves.
//!
//!   OP_DELETE = 3  Request:  req_id:u64 key_len:u32 key[..]
//!                  Response: Ok{length=0, crc32c=0} | Err{req_id, status}
//!
//!   OP_HEAD   = 4  Request:  req_id:u64 key_len:u32 key[..]
//!                  Response: Range{req_id, length=0, object_size,
//!                            generation, crc32c=0} | Err
//! ```
//!
//! Status codes mirror s3_rdma_proto for consistency:
//!   STATUS_OK=0  STATUS_NOT_FOUND=1  STATUS_ERROR=2  STATUS_TOO_LARGE=3
//!   STATUS_UNAUTHORIZED=4  STATUS_GENERATION_CHANGED=5

use std::io::{self, Read, Write};

pub const OP_PUT: u8 = 1;
pub const OP_GET: u8 = 2;
pub const OP_DELETE: u8 = 3;
pub const OP_HEAD: u8 = 4;

pub const RESP_OK: u8 = 0;
pub const RESP_ERR: u8 = 1;
pub const RESP_RANGE: u8 = 2;

pub const STATUS_OK: u8 = 0;
pub const STATUS_NOT_FOUND: u8 = 1;
pub const STATUS_ERROR: u8 = 2;
/// Payload length in a request exceeds `MAX_PAYLOAD` (or the server's
/// configured object cap). The server rejects the frame BEFORE
/// allocating, so a malicious/oversized length can't OOM-abort it.
pub const STATUS_TOO_LARGE: u8 = 3;
/// Rejected: request was unsigned, mis-signed, replayed, or denied by
/// policy. One code for all of them so the endpoint cannot be used to
/// enumerate valid access keys.
pub const STATUS_UNAUTHORIZED: u8 = 4;
/// GET carried a non-zero `if_generation` and the object has since been
/// replaced. The client must restart the object, not just the range.
pub const STATUS_GENERATION_CHANGED: u8 = 5;

/// Largest payload one ranged GET response carries. The server stages a
/// range in memory to checksum it before the header goes out, so this
/// bounds per-request server memory; clients ask for more ranges.
pub const MAX_GET_RANGE: u64 = 64 * 1024 * 1024;

/// Cap on access-key length in the auth envelope.
pub const MAX_ACCESS_KEY_LEN: usize = 64;

/// Cap on inline-control-message length. Keys can be long (S3 spec
/// allows 1024 bytes), so we size for headroom; the payload bytes
/// for PUT/GET don't go through this buffer.
pub const MAX_CONTROL_FRAME: usize = 4096;

/// Cap on key length. 1024 matches the S3 spec ceiling.
pub const MAX_KEY_LEN: usize = 1024;

/// Hard cap on a single PUT/GET payload, enforced at decode time
/// before any allocation. Without this, a client-supplied `length`
/// in the PUT header flows straight into `vec![0u8; length]`; a
/// crafted `length = 2^60` triggers `handle_alloc_error → abort()`,
/// killing the whole process on one unauthenticated request.
///
/// 64 GiB is chosen to comfortably exceed the largest single MPU
/// part / checkpoint shard we bench (16 MiB parts, but whole-object
/// PUTs of 100s of GB are chunked by the client into parts well
/// under this) while keeping a rejected oversize far below any real
/// node's RAM. Operators who need larger single objects raise this;
/// it is NOT meant to be per-object-store configurable on the hot
/// decode path.
pub const MAX_PAYLOAD: u64 = 64 * 1024 * 1024 * 1024;

#[derive(Debug, Clone, PartialEq)]
pub enum Request {
    Put {
        req_id: u64,
        key: String,
        length: u64,
    },
    Get {
        req_id: u64,
        key: String,
        offset: u64,
        length: u64,
        if_generation: u128,
    },
    Delete {
        req_id: u64,
        key: String,
    },
    Head {
        req_id: u64,
        key: String,
    },
}

#[derive(Debug, Clone, PartialEq)]
pub enum Response {
    /// PUT / DELETE result. `crc32c` covers the bytes a PUT received.
    Ok {
        req_id: u64,
        length: u64,
        crc32c: u32,
    },
    /// GET / HEAD result. `length` payload bytes follow a GET's frame.
    Range {
        req_id: u64,
        length: u64,
        object_size: u64,
        generation: u128,
        crc32c: u32,
    },
    Err {
        req_id: u64,
        status: u8,
    },
}

impl Request {
    pub fn req_id(&self) -> u64 {
        match self {
            Request::Put { req_id, .. } => *req_id,
            Request::Get { req_id, .. } => *req_id,
            Request::Delete { req_id, .. } => *req_id,
            Request::Head { req_id, .. } => *req_id,
        }
    }

    pub fn key(&self) -> &str {
        match self {
            Request::Put { key, .. } => key,
            Request::Get { key, .. } => key,
            Request::Delete { key, .. } => key,
            Request::Head { key, .. } => key,
        }
    }

    /// Encode into the provided buffer at offset 0. Returns the
    /// number of bytes written (the control-frame length, not
    /// including any payload that follows on the socket).
    pub fn encode(&self, out: &mut [u8]) -> io::Result<usize> {
        let mut w = Cursor::new(out);
        match self {
            Request::Put {
                req_id,
                key,
                length,
            } => {
                if key.len() > MAX_KEY_LEN {
                    return Err(io::Error::new(io::ErrorKind::InvalidInput, "key too long"));
                }
                w.write_u8(OP_PUT)?;
                w.write_u64(*req_id)?;
                w.write_str(key)?;
                w.write_u64(*length)?;
            }
            Request::Get {
                req_id,
                key,
                offset,
                length,
                if_generation,
            } => {
                if key.len() > MAX_KEY_LEN {
                    return Err(io::Error::new(io::ErrorKind::InvalidInput, "key too long"));
                }
                w.write_u8(OP_GET)?;
                w.write_u64(*req_id)?;
                w.write_str(key)?;
                w.write_u64(*offset)?;
                w.write_u64(*length)?;
                w.write_u128(*if_generation)?;
            }
            Request::Delete { req_id, key } | Request::Head { req_id, key } => {
                if key.len() > MAX_KEY_LEN {
                    return Err(io::Error::new(io::ErrorKind::InvalidInput, "key too long"));
                }
                let op = if matches!(self, Request::Delete { .. }) {
                    OP_DELETE
                } else {
                    OP_HEAD
                };
                w.write_u8(op)?;
                w.write_u64(*req_id)?;
                w.write_str(key)?;
            }
        }
        Ok(w.pos)
    }

    pub fn decode(buf: &[u8]) -> io::Result<Self> {
        let mut r = RCur::new(buf);
        let op = r.read_u8()?;
        let req_id = r.read_u64()?;
        let key = r.read_str()?;
        match op {
            OP_PUT => {
                let length = r.read_u64()?;
                // Reject an oversized payload length HERE, before the
                // server ever does `vec![0u8; length]`. The payload
                // follows the control frame raw (unframed), so a bogus
                // length desyncs the stream irrecoverably — the only
                // safe action is to fail the decode, which drops the
                // connection. This is the fix for the
                // unauthenticated-single-request process-abort (a
                // `length = 2^60` would route Vec's alloc failure to
                // handle_alloc_error → abort()).
                if length > MAX_PAYLOAD {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        format!("s3-tcp PUT length {length} exceeds MAX_PAYLOAD {MAX_PAYLOAD}"),
                    ));
                }
                Ok(Request::Put {
                    req_id,
                    key,
                    length,
                })
            }
            OP_GET => {
                let offset = r.read_u64()?;
                let length = r.read_u64()?;
                let if_generation = r.read_u128()?;
                Ok(Request::Get {
                    req_id,
                    key,
                    offset,
                    length,
                    if_generation,
                })
            }
            OP_DELETE => Ok(Request::Delete { req_id, key }),
            OP_HEAD => Ok(Request::Head { req_id, key }),
            other => Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("unknown s3-tcp op {other}"),
            )),
        }
    }
}

impl Response {
    pub fn req_id(&self) -> u64 {
        match self {
            Response::Ok { req_id, .. } => *req_id,
            Response::Range { req_id, .. } => *req_id,
            Response::Err { req_id, .. } => *req_id,
        }
    }

    pub fn encode(&self, out: &mut [u8]) -> io::Result<usize> {
        let mut w = Cursor::new(out);
        match self {
            Response::Ok {
                req_id,
                length,
                crc32c,
            } => {
                w.write_u8(RESP_OK)?;
                w.write_u64(*req_id)?;
                w.write_u64(*length)?;
                w.write_u32(*crc32c)?;
            }
            Response::Range {
                req_id,
                length,
                object_size,
                generation,
                crc32c,
            } => {
                w.write_u8(RESP_RANGE)?;
                w.write_u64(*req_id)?;
                w.write_u64(*length)?;
                w.write_u64(*object_size)?;
                w.write_u128(*generation)?;
                w.write_u32(*crc32c)?;
            }
            Response::Err { req_id, status } => {
                w.write_u8(RESP_ERR)?;
                w.write_u64(*req_id)?;
                w.write_u8(*status)?;
            }
        }
        Ok(w.pos)
    }

    pub fn decode(buf: &[u8]) -> io::Result<Self> {
        let mut r = RCur::new(buf);
        let tag = r.read_u8()?;
        let req_id = r.read_u64()?;
        match tag {
            RESP_OK => {
                let length = r.read_u64()?;
                let crc32c = r.read_u32()?;
                Ok(Response::Ok {
                    req_id,
                    length,
                    crc32c,
                })
            }
            RESP_RANGE => {
                let length = r.read_u64()?;
                let object_size = r.read_u64()?;
                let generation = r.read_u128()?;
                let crc32c = r.read_u32()?;
                Ok(Response::Range {
                    req_id,
                    length,
                    object_size,
                    generation,
                    crc32c,
                })
            }
            RESP_ERR => {
                let status = r.read_u8()?;
                Ok(Response::Err { req_id, status })
            }
            other => Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("unknown s3-tcp response tag {other}"),
            )),
        }
    }
}

// ---------------------------------------------------------------------
// Stream framing helpers. Each control message goes on the wire as
// [u32 LE length][bytes...]. PUT/GET payload bytes are NOT framed —
// they follow the control frame raw and are sized by the
// `length` field in the request/response.
// ---------------------------------------------------------------------

/// Read one length-prefixed control frame from the stream into a
/// caller-provided scratch buffer. Returns the slice of valid bytes.
/// Caller decodes with `Request::decode` or `Response::decode`.
pub fn read_frame<'a, R: Read>(r: &mut R, scratch: &'a mut [u8]) -> io::Result<&'a [u8]> {
    let mut len_buf = [0u8; 4];
    r.read_exact(&mut len_buf)?;
    let len = u32::from_le_bytes(len_buf) as usize;
    if len > scratch.len() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("s3-tcp frame len {len} > scratch {}", scratch.len()),
        ));
    }
    r.read_exact(&mut scratch[..len])?;
    Ok(&scratch[..len])
}

/// Write the bytes in `frame` as one length-prefixed control frame.
pub fn write_frame<W: Write>(w: &mut W, frame: &[u8]) -> io::Result<()> {
    let len = frame.len() as u32;
    w.write_all(&len.to_le_bytes())?;
    w.write_all(frame)?;
    Ok(())
}

// ---------------------------------------------------------------------
// Hand-rolled LE cursors — kept dependency-free + identical shape to
// s3_rdma_proto.rs so the two protocols stay easy to compare.
// Split into write (`Cursor`, holds &mut) and read (`RCur`, holds &)
// halves so decode-from-shared-slice and encode-into-owned-slice
// don't fight over mutability.
// ---------------------------------------------------------------------

struct Cursor<'a> {
    buf: &'a mut [u8],
    pos: usize,
}

struct RCur<'a> {
    buf: &'a [u8],
    pos: usize,
}

impl<'a> RCur<'a> {
    fn new(buf: &'a [u8]) -> Self {
        Self { buf, pos: 0 }
    }

    fn need(&self, n: usize) -> io::Result<()> {
        if self.pos + n > self.buf.len() {
            Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "s3-tcp cursor: short buf",
            ))
        } else {
            Ok(())
        }
    }

    fn read_u8(&mut self) -> io::Result<u8> {
        self.need(1)?;
        let v = self.buf[self.pos];
        self.pos += 1;
        Ok(v)
    }
    fn read_u32(&mut self) -> io::Result<u32> {
        self.need(4)?;
        let v = u32::from_le_bytes(
            self.buf[self.pos..self.pos + 4]
                .try_into()
                .expect("invariant: a 4-byte range after need(4)"),
        );
        self.pos += 4;
        Ok(v)
    }
    fn read_u64(&mut self) -> io::Result<u64> {
        self.need(8)?;
        let v = u64::from_le_bytes(
            self.buf[self.pos..self.pos + 8]
                .try_into()
                .expect("invariant: an 8-byte range after need(8)"),
        );
        self.pos += 8;
        Ok(v)
    }
    fn read_u128(&mut self) -> io::Result<u128> {
        let lo = self.read_u64()? as u128;
        let hi = self.read_u64()? as u128;
        Ok(lo | (hi << 64))
    }
    fn read_str(&mut self) -> io::Result<String> {
        let n = self.read_u32()? as usize;
        if n > MAX_KEY_LEN {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "key len > MAX"));
        }
        self.need(n)?;
        let s = std::str::from_utf8(&self.buf[self.pos..self.pos + n])
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "key not utf-8"))?
            .to_string();
        self.pos += n;
        Ok(s)
    }
}

impl<'a> Cursor<'a> {
    fn new(buf: &'a mut [u8]) -> Self {
        Self { buf, pos: 0 }
    }

    fn need(&self, n: usize) -> io::Result<()> {
        if self.pos + n > self.buf.len() {
            Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "s3-tcp cursor: short buf",
            ))
        } else {
            Ok(())
        }
    }

    fn write_u8(&mut self, v: u8) -> io::Result<()> {
        self.need(1)?;
        self.buf[self.pos] = v;
        self.pos += 1;
        Ok(())
    }
    fn write_u32(&mut self, v: u32) -> io::Result<()> {
        self.need(4)?;
        self.buf[self.pos..self.pos + 4].copy_from_slice(&v.to_le_bytes());
        self.pos += 4;
        Ok(())
    }
    fn write_u64(&mut self, v: u64) -> io::Result<()> {
        self.need(8)?;
        self.buf[self.pos..self.pos + 8].copy_from_slice(&v.to_le_bytes());
        self.pos += 8;
        Ok(())
    }
    fn write_u128(&mut self, v: u128) -> io::Result<()> {
        self.write_u64(v as u64)?;
        self.write_u64((v >> 64) as u64)
    }
    fn write_bytes(&mut self, b: &[u8]) -> io::Result<()> {
        self.need(b.len())?;
        self.buf[self.pos..self.pos + b.len()].copy_from_slice(b);
        self.pos += b.len();
        Ok(())
    }
    fn write_str(&mut self, s: &str) -> io::Result<()> {
        self.write_u32(s.len() as u32)?;
        self.write_bytes(s.as_bytes())
    }
}

// ---------------------------------------------------------------------------
// Signed envelope — mirrors the s3-rdma envelope so both transports
// share one auth model, one credential store, and one verifier.
// ---------------------------------------------------------------------------

/// Encode `req` wrapped in a signed envelope.
pub fn encode_signed(
    req: &Request,
    creds: &crate::auth::Credentials,
    nonce: u64,
    out: &mut [u8],
) -> io::Result<usize> {
    let mut body = vec![0u8; out.len()];
    let body_len = req.encode(&mut body)?;
    let header = crate::auth::sign_request(creds, nonce, &body[..body_len]);

    let mut w = Cursor::new(out);
    w.write_str(&header.access_key)?;
    w.write_u64(header.timestamp)?;
    w.write_u64(header.nonce)?;
    for b in header.signature.iter() {
        w.write_u8(*b)?;
    }
    w.write_u64(body_len as u64)?;
    for b in body[..body_len].iter() {
        w.write_u8(*b)?;
    }
    Ok(w.pos)
}

/// Decode a signed envelope into (header, request, signed body bytes).
pub fn decode_signed(buf: &[u8]) -> io::Result<(crate::auth::AuthHeader, Request, Vec<u8>)> {
    let mut r = RCur::new(buf);
    let access_key = r.read_str()?;
    if access_key.len() > MAX_ACCESS_KEY_LEN {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "access key too long",
        ));
    }
    let timestamp = r.read_u64()?;
    let nonce = r.read_u64()?;
    let mut signature = [0u8; crate::auth::SIGNATURE_LEN];
    for b in signature.iter_mut() {
        *b = r.read_u8()?;
    }
    let body_len = r.read_u64()? as usize;
    // Bound before allocating: `body_len` is attacker-controlled, and an
    // unchecked `vec![0; body_len]` is the C1 whole-process abort again.
    if body_len > buf.len() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "body length exceeds frame",
        ));
    }
    let mut body = Vec::with_capacity(body_len);
    for _ in 0..body_len {
        body.push(r.read_u8()?);
    }
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

#[cfg(test)]
mod tests {
    use super::*;

    fn round_trip_req(r: Request) {
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        let n = r.encode(&mut buf).expect("encode");
        let back = Request::decode(&buf[..n]).expect("decode");
        assert_eq!(r, back);
    }
    fn round_trip_resp(r: Response) {
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        let n = r.encode(&mut buf).expect("encode");
        let back = Response::decode(&buf[..n]).expect("decode");
        assert_eq!(r, back);
    }

    #[test]
    fn put_round_trip() {
        round_trip_req(Request::Put {
            req_id: 1,
            key: "ckpt/step-100".into(),
            length: 16 * 1024 * 1024,
        });
    }
    #[test]
    fn get_round_trip() {
        round_trip_req(Request::Get {
            req_id: 42,
            key: "foo/bar".into(),
            offset: 3 << 40,
            length: MAX_GET_RANGE,
            if_generation: 0xDEAD_BEEF_0000_0001_u128 << 64 | 7,
        });
    }
    #[test]
    fn delete_round_trip() {
        round_trip_req(Request::Delete {
            req_id: 99,
            key: "tmp/x".into(),
        });
    }
    #[test]
    fn head_round_trip() {
        round_trip_req(Request::Head {
            req_id: 7,
            key: "rank-0/step-200".into(),
        });
    }
    #[test]
    fn ok_round_trip() {
        round_trip_resp(Response::Ok {
            req_id: 5,
            length: 4096,
            crc32c: 0xE306_9283,
        });
    }
    #[test]
    fn range_round_trip() {
        round_trip_resp(Response::Range {
            req_id: 9,
            length: 1 << 20,
            object_size: 40 << 30,
            generation: u128::MAX - 1,
            crc32c: 0xE306_9283,
        });
    }
    #[test]
    fn rejects_truncated_range_response() {
        let r = Response::Range {
            req_id: 1,
            length: 2,
            object_size: 2,
            generation: 3,
            crc32c: 4,
        };
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        let n = r.encode(&mut buf).unwrap();
        assert!(Response::decode(&buf[..n - 1]).is_err());
    }
    #[test]
    fn err_round_trip() {
        round_trip_resp(Response::Err {
            req_id: 6,
            status: STATUS_NOT_FOUND,
        });
    }

    #[test]
    fn rejects_unknown_op() {
        let buf = [99u8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0];
        assert!(Request::decode(&buf).is_err());
    }

    #[test]
    fn rejects_oversized_key() {
        let big = "x".repeat(MAX_KEY_LEN + 1);
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        assert!(Request::Get {
            req_id: 1,
            key: big,
            offset: 0,
            length: 1,
            if_generation: 0
        }
        .encode(&mut buf)
        .is_err());
    }

    #[test]
    fn rejects_truncated_frame() {
        let r = Request::Put {
            req_id: 1,
            key: "k".into(),
            length: 100,
        };
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        let n = r.encode(&mut buf).unwrap();
        // Drop the trailing length field.
        assert!(Request::decode(&buf[..n - 4]).is_err());
    }

    #[test]
    fn rejects_oversized_put_payload_length() {
        // A PUT header claiming a payload past MAX_PAYLOAD must be
        // rejected at DECODE, before the server ever reaches
        // `vec![0u8; length]` (which on a crafted 2^60 length aborts
        // the whole process via handle_alloc_error). Encode a valid
        // PUT then overwrite its length field with a huge value.
        let r = Request::Put {
            req_id: 1,
            key: "k".into(),
            length: 16,
        };
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        let n = r.encode(&mut buf).unwrap();
        // Layout: op(1) req_id(8) key_len(4) key(1) length(8).
        // The trailing 8 bytes are the length field.
        let len_off = n - 8;
        buf[len_off..n].copy_from_slice(&(MAX_PAYLOAD + 1).to_le_bytes());
        assert!(
            Request::decode(&buf[..n]).is_err(),
            "decode must reject length > MAX_PAYLOAD"
        );

        // A length exactly at the cap is accepted (boundary).
        buf[len_off..n].copy_from_slice(&MAX_PAYLOAD.to_le_bytes());
        match Request::decode(&buf[..n]).expect("at-cap length decodes") {
            Request::Put { length, .. } => assert_eq!(length, MAX_PAYLOAD),
            other => panic!("expected Put, got {other:?}"),
        }
    }

    #[test]
    fn framing_round_trip() {
        use std::io::Cursor;
        let r = Request::Put {
            req_id: 7,
            key: "frame".into(),
            length: 1024,
        };
        let mut frame = [0u8; MAX_CONTROL_FRAME];
        let n = r.encode(&mut frame).unwrap();

        let mut wire = Vec::new();
        write_frame(&mut wire, &frame[..n]).unwrap();

        let mut rd = Cursor::new(&wire);
        let mut scratch = [0u8; MAX_CONTROL_FRAME];
        let read = read_frame(&mut rd, &mut scratch).unwrap();
        let back = Request::decode(read).unwrap();
        assert_eq!(r, back);
    }
}
