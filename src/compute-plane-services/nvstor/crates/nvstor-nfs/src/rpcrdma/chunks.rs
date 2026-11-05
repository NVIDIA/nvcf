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

//! RFC 8166 chunk lists and RFC 8797 connection negotiation.
//!
//! Transport plans contain only validated offsets into owned buffers. Remote
//! access is performed by the connection owner, never by the NFS worker tasks.

use super::{RpcRdmaError, RDMA_MSG, RDMA_NOMSG, RPCRDMA_VERSION_1};
use crate::ddp::{DataItem, DdpBuffer};
use crate::xdr::{XdrReader, XdrWriter};
use std::ops::Range;

pub const INLINE_SIZE: usize = 4096;
pub const MAX_RPC_SIZE: usize = 16 * 1024 * 1024;
pub const MAX_SEGMENTS: usize = 128;
pub const MAX_CHUNKS: usize = 16;
/// Credits granted to each client: its in-flight RPCs on one connection.
/// The Linux client asks for 128.
pub const DEFAULT_CREDITS: u32 = 128;

fn invalid(message: impl Into<String>) -> RpcRdmaError {
    RpcRdmaError::InvalidHeader(message.into())
}

impl From<crate::xdr::XdrError> for RpcRdmaError {
    fn from(error: crate::xdr::XdrError) -> Self {
        invalid(error.to_string())
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Segment {
    pub handle: u32,
    pub length: u32,
    pub offset: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReadChunk {
    pub position: u32,
    pub segments: Vec<Segment>,
}

#[derive(Clone, Debug)]
pub struct Call {
    pub xid: u32,
    pub credits: u32,
    pub reads: Vec<ReadChunk>,
    pub writes: Vec<Vec<Segment>>,
    pub reply: Option<Vec<Segment>>,
    pub inline: Vec<u8>,
}

fn boolean(r: &mut XdrReader<'_>) -> Result<bool, RpcRdmaError> {
    match r.get_u32()? {
        0 => Ok(false),
        1 => Ok(true),
        _ => Err(invalid("invalid XDR list discriminant")),
    }
}

fn segment(r: &mut XdrReader<'_>, count: &mut usize) -> Result<Segment, RpcRdmaError> {
    *count += 1;
    if *count > MAX_SEGMENTS {
        return Err(invalid("too many RDMA segments"));
    }
    let s = Segment {
        handle: r.get_u32()?,
        length: r.get_u32()?,
        offset: r.get_u64()?,
    };
    if s.offset.checked_add(s.length as u64).is_none() {
        return Err(invalid("remote address overflow"));
    }
    Ok(s)
}

fn chunk(r: &mut XdrReader<'_>, count: &mut usize) -> Result<Vec<Segment>, RpcRdmaError> {
    let n = r.get_u32()? as usize;
    if n > MAX_SEGMENTS.saturating_sub(*count) || n > r.remaining() / 16 {
        return Err(invalid("invalid RDMA segment count"));
    }
    (0..n).map(|_| segment(r, count)).collect()
}

fn length(segments: &[Segment]) -> Result<usize, RpcRdmaError> {
    segments.iter().try_fold(0usize, |n, s| {
        n.checked_add(s.length as usize)
            .ok_or_else(|| invalid("chunk size overflow"))
    })
}

fn roundup(size: usize) -> Result<usize, RpcRdmaError> {
    size.checked_add(3)
        .map(|n| n & !3)
        .ok_or_else(|| invalid("chunk size overflow"))
}

/// Decode a requester header. The caller silently discards messages shorter
/// than 28 bytes and RDMA_ERROR, as required by RFC 8166 section 4.5.
pub fn parse(data: &[u8]) -> Result<Call, RpcRdmaError> {
    if data.len() < 28 || data.len() > INLINE_SIZE {
        return Err(invalid("invalid inline size"));
    }
    let mut r = XdrReader::new(data);
    let xid = r.get_u32()?;
    let version = r.get_u32()?;
    if version != RPCRDMA_VERSION_1 {
        return Err(RpcRdmaError::VersionMismatch { got: version });
    }
    let credits = r.get_u32()?;
    let procedure = r.get_u32()?;
    if procedure != RDMA_MSG && procedure != RDMA_NOMSG {
        return Err(RpcRdmaError::UnsupportedProcedure { proc: procedure });
    }
    let mut reads: Vec<ReadChunk> = Vec::new();
    let mut count = 0;
    while boolean(&mut r)? {
        let position = r.get_u32()?;
        if position as usize > MAX_RPC_SIZE || position % 4 != 0 {
            return Err(invalid("invalid read position"));
        }
        let s = segment(&mut r, &mut count)?;
        if let Some(last) = reads.last_mut() {
            if position < last.position {
                return Err(invalid("read positions are not ordered"));
            }
            if position == last.position {
                last.segments.push(s);
                continue;
            }
        }
        if reads.len() == MAX_CHUNKS {
            return Err(invalid("too many read chunks"));
        }
        reads.push(ReadChunk {
            position,
            segments: vec![s],
        });
    }
    let mut writes = Vec::new();
    while boolean(&mut r)? {
        if writes.len() == MAX_CHUNKS {
            return Err(invalid("too many write chunks"));
        }
        writes.push(chunk(&mut r, &mut count)?);
    }
    let reply = if boolean(&mut r)? {
        Some(chunk(&mut r, &mut count)?)
    } else {
        None
    };
    let inline = r.remaining_bytes().to_vec();
    let has_zero = reads.first().is_some_and(|c| c.position == 0);
    if (procedure == RDMA_NOMSG && (!has_zero || !inline.is_empty()))
        || (procedure == RDMA_MSG && (has_zero || inline.len() < 8))
    {
        return Err(invalid("payload does not match RDMA procedure"));
    }
    let total = reads.iter().try_fold(inline.len(), |n, c| {
        n.checked_add(roundup(length(&c.segments)?)?)
            .ok_or_else(|| invalid("call size overflow"))
    })?;
    if total > MAX_RPC_SIZE {
        return Err(invalid("RPC call exceeds size limit"));
    }
    Ok(Call {
        xid,
        credits,
        reads,
        writes,
        reply,
        inline,
    })
}

/// How a non-zero Read chunk reaches the NFS processor.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ChunkUse {
    /// Copied into the reconstructed call (small arguments, e.g. a symlink target).
    Inline,
    /// Left in its transport buffer and handed to the processor (WRITE data).
    Placed,
}

/// Reconstruct the RPC call from its reduced message (the inline message, or
/// the position-zero chunk) and the fetched data of the non-zero Read chunks
/// `chunks` (`data[i]` holds chunk i's bytes, `uses[i]` how to deliver it).
/// Inline chunks are copied in with their XDR padding. Placed chunks stay out
/// of line: for each, `(chunk index, record offset)` is returned, the offset
/// being where its data would start in the returned record.
pub fn rebuild(
    xid: u32,
    reduced: &[u8],
    chunks: &[ReadChunk],
    uses: &[ChunkUse],
    data: &[&[u8]],
) -> Result<(Vec<u8>, Vec<(usize, usize)>), RpcRdmaError> {
    if chunks.len() != uses.len()
        || chunks.len() != data.len()
        || chunks.iter().any(|c| c.position == 0)
    {
        return Err(invalid("read chunk layout does not match call"));
    }
    let inline_bytes = chunks
        .iter()
        .zip(uses)
        .filter(|(_, u)| **u == ChunkUse::Inline)
        .try_fold(0usize, |n, (c, _)| {
            n.checked_add(roundup(length(&c.segments)?)?)
                .ok_or_else(|| invalid("call size overflow"))
        })?;
    let mut result =
        Vec::with_capacity(reduced.len().saturating_add(inline_bytes).min(MAX_RPC_SIZE));
    let mut placed = Vec::new();
    let mut consumed = 0usize;
    // Position in the complete XDR stream (every chunk present) of reduced[consumed].
    let mut full = 0usize;
    for (i, c) in chunks.iter().enumerate() {
        let position = c.position as usize;
        let before = position
            .checked_sub(full)
            .ok_or_else(|| invalid("overlapping read chunks"))?;
        let end = consumed
            .checked_add(before)
            .filter(|end| *end <= reduced.len())
            .ok_or_else(|| invalid("read position beyond RPC payload"))?;
        result.extend_from_slice(&reduced[consumed..end]);
        consumed = end;
        let size = length(&c.segments)?;
        if data[i].len() != size {
            return Err(invalid("read chunk data size mismatch"));
        }
        let padded = roundup(size)?;
        match uses[i] {
            ChunkUse::Inline => {
                result.extend_from_slice(data[i]);
                result.resize(result.len() + padded - size, 0);
            }
            ChunkUse::Placed => placed.push((i, result.len())),
        }
        full = position
            .checked_add(padded)
            .filter(|n| *n <= MAX_RPC_SIZE)
            .ok_or_else(|| invalid("RPC call exceeds size limit"))?;
    }
    if full + (reduced.len() - consumed) > MAX_RPC_SIZE {
        return Err(invalid("RPC call exceeds size limit"));
    }
    result.extend_from_slice(&reduced[consumed..]);
    if result.len() < 8 || result[..4] != xid.to_be_bytes() || result[4..8] != 0u32.to_be_bytes() {
        return Err(invalid("RPC xid or direction does not match call header"));
    }
    Ok((result, placed))
}

/// Where a transfer's local bytes come from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Source {
    /// `ReplyPlan::payload[range]` (heap memory; the transport registers it).
    Payload(Range<usize>),
    /// `ReplyPlan::items[item].bytes()[range]` (placed READ data).
    Item { item: usize, range: Range<usize> },
}

/// One RDMA WRITE of a reply.
#[derive(Debug)]
pub struct Transfer {
    pub remote: Segment,
    pub source: Source,
}

/// A reply ready to post: RDMA WRITE every transfer, then SEND `send`.
/// `payload` and `items` own every byte the transfers reference.
pub struct ReplyPlan {
    pub payload: Vec<u8>,
    pub items: Vec<Box<dyn DdpBuffer>>,
    pub transfers: Vec<Transfer>,
    pub send: Vec<u8>,
}

impl std::fmt::Debug for ReplyPlan {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ReplyPlan")
            .field("payload", &self.payload.len())
            .field("items", &self.items.len())
            .field("transfers", &self.transfers)
            .field("send", &self.send.len())
            .finish()
    }
}

/// Split `len` bytes over `segments`; `source` maps a data range to its origin.
fn scatter(
    segments: &[Segment],
    len: usize,
    source: impl Fn(Range<usize>) -> Source,
    transfers: &mut Vec<Transfer>,
) -> Result<Vec<Segment>, RpcRdmaError> {
    if length(segments)? < len {
        return Err(invalid("reply chunk is too small"));
    }
    let mut offset = 0;
    let mut returned = Vec::with_capacity(segments.len());
    for s in segments {
        let size = (len - offset).min(s.length as usize);
        let mut actual = s.clone();
        actual.length = size as u32;
        if size != 0 {
            transfers.push(Transfer {
                remote: actual.clone(),
                source: source(offset..offset + size),
            });
        }
        returned.push(actual);
        offset += size;
    }
    Ok(returned)
}

fn encode_chunk(w: &mut XdrWriter, segments: &[Segment]) {
    w.put_u32(segments.len() as u32);
    for s in segments {
        w.put_u32(s.handle);
        w.put_u32(s.length);
        w.put_u64(s.offset);
    }
}

/// `items` are the READ/READLINK data items of the full RPC reply `payload`,
/// in operation order (an error result occupies its own None entry); item i
/// pairs with Write chunk i. Opaque lengths remain inline; data and padding
/// move to the Write chunk. Placed (external) data is written straight from
/// its buffer; when its Write chunk is missing or empty it is copied inline.
pub fn plan_reply(
    call: &Call,
    credits: u32,
    payload: Vec<u8>,
    items: Vec<Option<DataItem>>,
    inline_limit: usize,
) -> Result<ReplyPlan, RpcRdmaError> {
    if payload.len() > MAX_RPC_SIZE
        || payload.len() < 8
        || payload[..4] != call.xid.to_be_bytes()
        || payload[4..8] != 1u32.to_be_bytes()
    {
        return Err(invalid("invalid RPC reply"));
    }
    let mut transfers = Vec::new();
    let mut writes = Vec::new();
    let mut reduced = Vec::new();
    let mut buffers: Vec<Box<dyn DdpBuffer>> = Vec::new();
    let mut consumed = 0;
    let item_count = items.len();
    let mut items = items.into_iter();
    for i in 0..item_count.max(call.writes.len()) {
        let item = items.next().flatten();
        let segments = call.writes.get(i);
        let Some(item) = item else {
            if let Some(segments) = segments {
                // RFC 8267 6.4.1 requires an empty chunk for an error arm.
                writes.push(if i < item_count {
                    Vec::new()
                } else {
                    scatter(segments, 0, |r| Source::Payload(r), &mut transfers)?
                });
            }
            continue;
        };
        if item.at < consumed || item.at > payload.len() {
            return Err(invalid("invalid DDP result range"));
        }
        let padded_len = roundup(item.len)?;
        match (segments.filter(|s| !s.is_empty()), item.external) {
            (Some(segments), Some(buf)) => {
                if buf.bytes().len() < item.len {
                    return Err(invalid("placed READ data shorter than its result"));
                }
                let index = buffers.len();
                buffers.push(buf);
                writes.push(scatter(
                    segments,
                    item.len,
                    |range| Source::Item { item: index, range },
                    &mut transfers,
                )?);
                reduced.extend_from_slice(&payload[consumed..item.at]);
                consumed = item.at;
            }
            (Some(segments), None) => {
                let at = item.at;
                if at + padded_len > payload.len() {
                    return Err(invalid("invalid DDP result range"));
                }
                writes.push(scatter(
                    segments,
                    item.len,
                    |r| Source::Payload(at + r.start..at + r.end),
                    &mut transfers,
                )?);
                reduced.extend_from_slice(&payload[consumed..at]);
                consumed = at + padded_len;
            }
            (None, external) => {
                // No usable Write chunk: the data travels in the message.
                if let Some(buf) = external {
                    let data = buf
                        .bytes()
                        .get(..item.len)
                        .ok_or_else(|| invalid("placed READ data shorter than its result"))?;
                    reduced.extend_from_slice(&payload[consumed..item.at]);
                    reduced.extend_from_slice(data);
                    reduced.resize(reduced.len() + padded_len - item.len, 0);
                    consumed = item.at;
                }
                if let Some(segments) = segments {
                    writes.push(scatter(
                        segments,
                        0,
                        |r| Source::Payload(r),
                        &mut transfers,
                    )?);
                }
            }
        }
    }
    reduced.extend_from_slice(&payload[consumed..]);
    let mut all_payload = payload;
    let mut w = XdrWriter::new();
    w.put_u32(call.xid);
    w.put_u32(1);
    w.put_u32(credits);
    // Include every offered Reply chunk even when unused (RFC 8166 4.3.3).
    let header_size = 28
        + writes.iter().map(|s| 8 + s.len() * 16).sum::<usize>()
        + call.reply.as_ref().map_or(0, |s| 4 + s.len() * 16);
    let long = header_size + reduced.len() > inline_limit;
    w.put_u32(if long { RDMA_NOMSG } else { RDMA_MSG });
    w.put_bool(false);
    for s in &writes {
        w.put_bool(true);
        encode_chunk(&mut w, s);
    }
    w.put_bool(false);
    if long {
        let segments = call
            .reply
            .as_ref()
            .ok_or_else(|| invalid("large reply requires a Reply chunk"))?;
        let start = all_payload.len();
        all_payload.extend_from_slice(&reduced);
        let returned = scatter(
            segments,
            reduced.len(),
            |r| Source::Payload(start + r.start..start + r.end),
            &mut transfers,
        )?;
        w.put_bool(true);
        encode_chunk(&mut w, &returned);
    } else {
        w.put_bool(call.reply.is_some());
        if let Some(s) = &call.reply {
            encode_chunk(
                &mut w,
                &scatter(s, 0, |r| Source::Payload(r), &mut transfers)?,
            );
        }
        w.put_raw(&reduced);
    }
    if w.len() > inline_limit {
        return Err(invalid("reply transport header exceeds inline limit"));
    }
    Ok(ReplyPlan {
        payload: all_payload,
        items: buffers,
        transfers,
        send: w.into_bytes(),
    })
}

/// No remote invalidation: memory keys remain owned by the Linux client.
pub fn private_data() -> [u8; 8] {
    [0xf6, 0xab, 0x0e, 0x18, 1, 0, 3, 3]
}

pub fn peer_inline_limit(private: &[u8]) -> usize {
    private
        .windows(8)
        .find(|b| b[..4] == private_data()[..4] && b[4] == 1)
        .map_or(1024, |b| ((b[7] as usize + 1) * 1024).min(INLINE_SIZE))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn seg(offset: u64, length: u32) -> Segment {
        Segment {
            handle: 7,
            offset,
            length,
        }
    }
    fn rpc() -> Vec<u8> {
        [
            42u32.to_be_bytes(),
            0u32.to_be_bytes(),
            123u32.to_be_bytes(),
        ]
        .concat()
    }
    fn call() -> Call {
        Call {
            xid: 42,
            credits: 8,
            reads: vec![],
            writes: vec![],
            reply: None,
            inline: rpc(),
        }
    }
    fn reply() -> Vec<u8> {
        let mut bytes = rpc();
        bytes[4..8].copy_from_slice(&1u32.to_be_bytes());
        bytes
    }
    fn encode(c: &Call, procedure: u32) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_u32(c.xid);
        w.put_u32(1);
        w.put_u32(c.credits);
        w.put_u32(procedure);
        for chunk in &c.reads {
            for s in &chunk.segments {
                w.put_bool(true);
                w.put_u32(chunk.position);
                w.put_u32(s.handle);
                w.put_u32(s.length);
                w.put_u64(s.offset);
            }
        }
        w.put_bool(false);
        for chunk in &c.writes {
            w.put_bool(true);
            encode_chunk(&mut w, chunk);
        }
        w.put_bool(false);
        w.put_bool(c.reply.is_some());
        if let Some(chunk) = &c.reply {
            encode_chunk(&mut w, chunk);
        }
        w.put_raw(&c.inline);
        w.into_bytes()
    }
    #[test]
    fn rebuild_inlines_split_chunk_with_roundup() {
        let mut c = call();
        c.reads = vec![ReadChunk {
            position: 8,
            segments: vec![seg(0, 2), seg(2, 3)],
        }];
        let parsed = parse(&encode(&c, RDMA_MSG)).unwrap();
        let (record, placed) = rebuild(
            42,
            &parsed.inline,
            &parsed.reads,
            &[ChunkUse::Inline],
            &[b"hello"],
        )
        .unwrap();
        assert!(placed.is_empty());
        assert_eq!(
            record,
            [
                rpc()[..8].to_vec(),
                b"hello\0\0\0".to_vec(),
                rpc()[8..].to_vec()
            ]
            .concat()
        );
    }
    #[test]
    fn rebuild_leaves_placed_chunks_out_of_line_and_tracks_offsets() {
        // Two chunks: a placed one at 8 (5 bytes, padded to 8 in the full
        // stream) and an inline one right after it at full position 16.
        let mut reduced = rpc()[..8].to_vec();
        reduced.extend_from_slice(&[0xee; 4]);
        let chunks = vec![
            ReadChunk {
                position: 8,
                segments: vec![seg(0, 5)],
            },
            ReadChunk {
                position: 16,
                segments: vec![seg(0, 2)],
            },
        ];
        let (record, placed) = rebuild(
            42,
            &reduced,
            &chunks,
            &[ChunkUse::Placed, ChunkUse::Inline],
            &[b"hello", b"ab"],
        )
        .unwrap();
        assert_eq!(placed, vec![(0, 8)]);
        assert_eq!(record, [&rpc()[..8], b"ab\0\0", &[0xee; 4]].concat());
        // Data size must match the chunk's segments.
        assert!(rebuild(
            42,
            &reduced,
            &chunks,
            &[ChunkUse::Placed, ChunkUse::Inline],
            &[b"hell", b"ab"]
        )
        .is_err());
    }
    #[test]
    fn position_zero_can_contain_a_reduced_call() {
        let mut c = call();
        c.inline.clear();
        c.reads = vec![
            ReadChunk {
                position: 0,
                segments: vec![seg(0, 12)],
            },
            ReadChunk {
                position: 8,
                segments: vec![seg(100, 3)],
            },
        ];
        let parsed = parse(&encode(&c, RDMA_NOMSG)).unwrap();
        let (record, _) = rebuild(
            42,
            &rpc(),
            &parsed.reads[1..],
            &[ChunkUse::Inline],
            &[b"xyz"],
        )
        .unwrap();
        assert_eq!(&record[8..12], b"xyz\0");
        assert_eq!(&record[12..], &rpc()[8..]);
    }
    #[test]
    fn rejects_truncation_counts_discriminants_and_address_overflow() {
        let mut c = call();
        c.writes = vec![vec![seg(1, 32)]];
        let encoded = encode(&c, RDMA_MSG);
        for n in 0..encoded.len() - c.inline.len() {
            assert!(parse(&encoded[..n]).is_err(), "prefix {n}");
        }
        let mut bad = encoded.clone();
        bad[24..28].copy_from_slice(&u32::MAX.to_be_bytes());
        assert!(parse(&bad).is_err());
        let mut bad = encoded;
        bad[16..20].copy_from_slice(&2u32.to_be_bytes());
        assert!(parse(&bad).is_err());
        c.writes[0][0] = seg(u64::MAX, 1);
        assert!(parse(&encode(&c, RDMA_MSG)).is_err());
    }
    #[test]
    fn rejects_overlapping_unaligned_and_oversized_reads() {
        let mut c = call();
        c.reads = vec![ReadChunk {
            position: 8,
            segments: vec![seg(0, MAX_RPC_SIZE as u32)],
        }];
        assert!(parse(&encode(&c, RDMA_MSG)).is_err());
        c.reads[0].position = 9;
        c.reads[0].segments[0].length = 4;
        assert!(parse(&encode(&c, RDMA_MSG)).is_err());
        c.reads[0].position = 8;
        c.reads.push(ReadChunk {
            position: 12,
            segments: vec![seg(1, 4)],
        });
        c.reads[0].segments[0].length = 8;
        let uses = [ChunkUse::Inline, ChunkUse::Inline];
        assert!(rebuild(42, &c.inline, &c.reads, &uses, &[&[0; 8], &[0; 4]]).is_err());
        c.reads.truncate(1);
        c.reads[0].position = 100;
        assert!(rebuild(42, &c.inline, &c.reads, &[ChunkUse::Inline], &[&[0; 8]]).is_err());
    }
    #[test]
    fn malformed_long_call_and_xid_are_rejected() {
        let c = call();
        assert!(parse(&encode(&c, RDMA_NOMSG)).is_err());
        assert!(rebuild(99, &c.inline, &[], &[], &[]).is_err());
    }
    #[test]
    fn reply_scatter_removes_only_data_and_padding() {
        let mut c = call();
        c.writes = vec![vec![seg(100, 2), seg(200, 8), seg(300, 9)]];
        let mut payload = rpc();
        payload[4..8].copy_from_slice(&1u32.to_be_bytes());
        payload.extend_from_slice(b"hello\0\0\0tail");
        let plan = plan_reply(
            &c,
            8,
            payload.clone(),
            vec![Some(DataItem::inline(12..17))],
            1024,
        )
        .unwrap();
        assert_eq!(plan.transfers.len(), 2);
        assert_eq!(plan.transfers[0].source, Source::Payload(12..14));
        assert_eq!(plan.transfers[1].source, Source::Payload(14..17));
        let mut r = XdrReader::new(&plan.send[16..]);
        assert!(!r.get_bool().unwrap());
        assert!(r.get_bool().unwrap());
        assert_eq!(r.get_u32().unwrap(), 3);
        for (address, length) in [(100, 2), (200, 3), (300, 0)] {
            assert_eq!(r.get_u32().unwrap(), 7);
            assert_eq!(r.get_u32().unwrap(), length);
            assert_eq!(r.get_u64().unwrap(), address);
        }
        assert!(!r.get_bool().unwrap());
        assert!(!r.get_bool().unwrap());
        assert_eq!(r.remaining_bytes(), [&payload[..12], b"tail"].concat());
    }
    #[test]
    fn empty_error_and_unused_chunks_keep_their_positions() {
        let mut c = call();
        c.writes = vec![vec![], vec![seg(100, 10)], vec![seg(200, 10)]];
        let plan = plan_reply(
            &c,
            8,
            reply(),
            vec![Some(DataItem::inline(8..12)), None],
            1024,
        )
        .unwrap();
        assert!(plan.transfers.is_empty());
        let mut r = XdrReader::new(&plan.send[20..]);
        assert!(r.get_bool().unwrap());
        assert_eq!(r.get_u32().unwrap(), 0);
        assert!(r.get_bool().unwrap());
        assert_eq!(r.get_u32().unwrap(), 0);
        assert!(r.get_bool().unwrap());
        assert_eq!(r.get_u32().unwrap(), 1);
        assert_eq!(r.get_u32().unwrap(), 7);
        assert_eq!(r.get_u32().unwrap(), 0);
    }
    #[test]
    fn long_reply_uses_reply_chunk_after_write_chunks() {
        let mut c = call();
        c.writes = vec![vec![seg(100, 4)]];
        c.reply = Some(vec![seg(200, 4096)]);
        let mut payload = reply();
        payload.resize(2048, 1);
        let plan = plan_reply(
            &c,
            8,
            payload.clone(),
            vec![Some(DataItem::inline(8..12))],
            1024,
        )
        .unwrap();
        assert_eq!(
            u32::from_be_bytes(plan.send[12..16].try_into().unwrap()),
            RDMA_NOMSG
        );
        assert_eq!(plan.transfers.len(), 2);
        assert_eq!(plan.transfers[1].remote.length, 2044);
        assert_eq!(plan.transfers[1].source, Source::Payload(2048..2048 + 2044));
        c.reply = Some(vec![seg(200, 1)]);
        assert!(matches!(plan_reply(&c, 8, payload, vec![], 1024),
            Err(RpcRdmaError::InvalidHeader(message)) if message == "reply chunk is too small"));
    }
    #[test]
    fn too_small_write_chunk_fails_before_any_remote_io() {
        let mut c = call();
        c.writes = vec![vec![seg(0, 2)]];
        assert!(
            matches!(plan_reply(&c, 8, reply(), vec![Some(DataItem::inline(8..12))], 1024),
            Err(RpcRdmaError::InvalidHeader(message)) if message == "reply chunk is too small")
        );
    }
    fn placed(at: usize, data: &[u8]) -> DataItem {
        DataItem {
            at,
            len: data.len(),
            external: Some(Box::new(crate::ddp::HeapBuffer(data.to_vec()))),
        }
    }
    #[test]
    fn placed_read_data_is_written_from_its_buffer_without_copy() {
        // payload: rpc header (12) + eof/len words; the data itself is absent.
        let mut c = call();
        c.writes = vec![vec![seg(100, 4), seg(200, 8)]];
        let mut payload = reply();
        payload.extend_from_slice(&[0, 0, 0, 0, 0, 0, 0, 6, 0xaa, 0xbb]);
        let plan = plan_reply(
            &c,
            8,
            payload.clone(),
            vec![Some(placed(20, b"abcdef"))],
            1024,
        )
        .unwrap();
        assert_eq!(plan.items.len(), 1);
        assert_eq!(plan.transfers.len(), 2);
        assert_eq!(
            plan.transfers[0].source,
            Source::Item {
                item: 0,
                range: 0..4
            }
        );
        assert_eq!(
            plan.transfers[1].source,
            Source::Item {
                item: 0,
                range: 4..6
            }
        );
        assert_eq!(plan.transfers[1].remote.length, 2);
        // The reduced message is the payload unchanged (nothing to remove).
        assert!(plan.send.ends_with(&payload));
    }
    #[test]
    fn placed_read_data_without_write_chunk_is_copied_inline_with_padding() {
        let c = call();
        let mut payload = reply();
        payload.extend_from_slice(&[0, 0, 0, 0, 0, 0, 0, 3, 0xaa, 0xbb]);
        let plan =
            plan_reply(&c, 8, payload.clone(), vec![Some(placed(20, b"xyz"))], 1024).unwrap();
        assert!(plan.transfers.is_empty() && plan.items.is_empty());
        assert!(plan
            .send
            .ends_with(&[&payload[..20], b"xyz\0", &[0xaa, 0xbb]].concat()));
    }
    #[test]
    fn items_must_be_ordered_and_inside_the_reply() {
        let mut c = call();
        c.writes = vec![vec![seg(0, 8)], vec![seg(8, 8)]];
        let mut payload = reply();
        payload.resize(32, 0);
        let items = vec![Some(placed(24, b"ab")), Some(placed(16, b"cd"))];
        assert!(plan_reply(&c, 8, payload.clone(), items, 1024).is_err());
        assert!(plan_reply(&c, 8, payload, vec![Some(placed(40, b"ab"))], 1024).is_err());
    }
    #[test]
    fn private_data_handles_legacy_and_prefixed_peers() {
        assert_eq!(peer_inline_limit(&[]), 1024);
        let mut prefixed = vec![0xaa, 0xbb, 0xcc];
        prefixed.extend_from_slice(&private_data());
        assert_eq!(peer_inline_limit(&prefixed), 4096);
        prefixed[7] = 2;
        assert_eq!(peer_inline_limit(&prefixed), 1024);
        assert_eq!(private_data()[5], 0);
    }
}
