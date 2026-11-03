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

//! Direct data placement (RFC 8267) between the NFS processor and a
//! transport that can move bulk data without copying it.
//!
//! READ: the processor leases a buffer from the transport's
//! [`DdpAllocator`], reads file data straight into it, and returns it beside
//! the reply as an external [`DataItem`]; the transport RDMA-WRITEs it into
//! the client's Write chunk. WRITE: the transport RDMA-READs the client's
//! Read chunk into its own buffer and hands it in as a [`DdpArg`]; the
//! processor writes the file from that buffer. The processor never copies
//! bulk data on either path. Transports without DDP (TCP) pass nothing and
//! get inline data as before.

use std::ops::Range;

/// Memory owned by a transport that it can move without copying.
pub trait DdpBuffer: Send {
    fn bytes(&self) -> &[u8];
    fn bytes_mut(&mut self) -> &mut [u8];
    /// (address of `bytes()[0]`, local key) when the memory is registered
    /// with the transport's device. None: the transport must register or
    /// copy it before DMA.
    fn registration(&self) -> Option<(u64, u32)> {
        None
    }
}

/// Leases buffers for READ results. Returning None (exhausted, or `len`
/// larger than a buffer) makes the processor fall back to inline data.
pub trait DdpAllocator: Send + Sync {
    fn lease(&self, len: usize) -> Option<Box<dyn DdpBuffer>>;
}

/// Bulk data of one READ/READLINK result, in reply order. With `external`
/// None the `len` data bytes and their XDR padding are inline at
/// `reply[at..]`. With `external` Some they are absent from the reply bytes
/// (which continue at `at` with whatever follows the data) and live in
/// `external.bytes()[..len]`.
pub struct DataItem {
    pub at: usize,
    pub len: usize,
    pub external: Option<Box<dyn DdpBuffer>>,
}

impl DataItem {
    pub fn inline(range: Range<usize>) -> Self {
        Self {
            at: range.start,
            len: range.len(),
            external: None,
        }
    }
}

impl std::fmt::Debug for DataItem {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("DataItem")
            .field("at", &self.at)
            .field("len", &self.len)
            .field("external", &self.external.is_some())
            .finish()
    }
}

/// Bulk call argument placed by the transport: the opaque data whose XDR
/// length word ends at `offset` in the call payload is absent from the
/// payload and is `buf.bytes()[..len]` (possibly followed by padding).
pub struct DdpArg {
    pub offset: usize,
    pub len: usize,
    pub buf: Box<dyn DdpBuffer>,
}

impl DdpArg {
    pub fn data(&self) -> &[u8] {
        &self.buf.bytes()[..self.len]
    }
}

/// Copy external items back into the reply bytes, producing an all-inline
/// reply. Used where a self-contained copy is required (the session reply
/// cache) and only for replies small enough to be cached.
pub fn flatten(reply: &[u8], items: &[Option<DataItem>]) -> (Vec<u8>, Vec<Option<Range<usize>>>) {
    let extra: usize = items
        .iter()
        .flatten()
        .filter(|item| item.external.is_some())
        .map(|item| (item.len + 3) & !3)
        .sum();
    let mut out = Vec::with_capacity(reply.len() + extra);
    let mut ranges = Vec::with_capacity(items.len());
    let mut consumed = 0;
    for item in items {
        let Some(item) = item else {
            ranges.push(None);
            continue;
        };
        let at = item.at.min(reply.len()).max(consumed);
        out.extend_from_slice(&reply[consumed..at]);
        consumed = at;
        let start = out.len();
        match &item.external {
            Some(buf) => {
                out.extend_from_slice(&buf.bytes()[..item.len]);
                out.resize(out.len() + (4 - item.len % 4) % 4, 0);
            }
            None => {
                let end = (at + ((item.len + 3) & !3)).min(reply.len());
                out.extend_from_slice(&reply[at..end]);
                consumed = end;
            }
        }
        ranges.push(Some(start..start + item.len));
    }
    out.extend_from_slice(&reply[consumed..]);
    (out, ranges)
}

/// A heap buffer, for tests and for transports that cannot register memory.
pub struct HeapBuffer(pub Vec<u8>);

impl DdpBuffer for HeapBuffer {
    fn bytes(&self) -> &[u8] {
        &self.0
    }
    fn bytes_mut(&mut self) -> &mut [u8] {
        &mut self.0
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn flatten_inserts_external_data_with_padding_and_keeps_inline_items() {
        // reply: [hdr 4][len=3][external: absent][tail 4][len=2][inline "xy" + 2 pad][end 4]
        let mut reply = vec![1, 1, 1, 1, 0, 0, 0, 3, 9, 9, 9, 9, 0, 0, 0, 2];
        let inline_at = reply.len();
        reply.extend_from_slice(b"xy\0\0");
        reply.extend_from_slice(&[7, 7, 7, 7]);
        let items = vec![
            Some(DataItem {
                at: 8,
                len: 3,
                external: Some(Box::new(HeapBuffer(b"abcXXX".to_vec()))),
            }),
            None,
            Some(DataItem::inline(inline_at..inline_at + 2)),
        ];
        let (flat, ranges) = flatten(&reply, &items);
        assert_eq!(
            flat,
            [
                &[1, 1, 1, 1, 0, 0, 0, 3][..],
                b"abc\0",
                &[9, 9, 9, 9, 0, 0, 0, 2],
                b"xy\0\0",
                &[7, 7, 7, 7]
            ]
            .concat()
        );
        assert_eq!(ranges, vec![Some(8..11), None, Some(20..22)]);
    }
}
