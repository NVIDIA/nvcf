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

//! Preflight DDP eligibility before executing any NFS operation. Only WRITE
//! data and CREATE symlink targets can be reduced in a call (RFC 8267 6.1).
//! Position-zero calls have no eligibility restriction. Unknown argument
//! layouts in a data-bearing compound are rejected with ERR_CHUNK.
//!
//! The walk runs over the reduced message, in which each Read chunk's data
//! (and padding) is absent; positions are compared in complete-stream
//! coordinates (reduced offset plus the bytes of earlier chunks).

use super::{
    chunks::{ChunkUse, ReadChunk},
    RpcRdmaError,
};
use crate::{nfs4::*, rpc};
use std::ops::Range;

fn bad() -> RpcRdmaError {
    RpcRdmaError::InvalidHeader("invalid NFS Read chunk placement or argument layout".into())
}

struct Cursor<'a> {
    data: &'a [u8],
    pos: usize,
    /// Bytes of matched chunks absent before `pos` (complete stream = pos + absent).
    absent: usize,
}
impl Cursor<'_> {
    fn skip(&mut self, n: usize) -> Result<(), RpcRdmaError> {
        self.pos = self
            .pos
            .checked_add(n)
            .filter(|n| *n <= self.data.len())
            .ok_or_else(bad)?;
        Ok(())
    }
    fn u32(&mut self) -> Result<u32, RpcRdmaError> {
        let pos = self.pos;
        self.skip(4)?;
        Ok(u32::from_be_bytes(
            self.data[pos..pos + 4]
                .try_into()
                .expect("invariant: four-byte field"),
        ))
    }
    fn opaque(&mut self) -> Result<Range<usize>, RpcRdmaError> {
        let n = self.u32()? as usize;
        let start = self.pos;
        self.skip(n.checked_add(3).ok_or_else(bad)? & !3)?;
        Ok(start..start + n)
    }
    fn bitmap(&mut self) -> Result<(), RpcRdmaError> {
        let n = self.u32()? as usize;
        self.skip(n.checked_mul(4).ok_or_else(bad)?)
    }
    fn attrs(&mut self) -> Result<(), RpcRdmaError> {
        self.bitmap()?;
        self.opaque()?;
        Ok(())
    }
    fn owner(&mut self) -> Result<(), RpcRdmaError> {
        self.skip(8)?;
        self.opaque()?;
        Ok(())
    }
    /// An eligible opaque: its data is either the next wanted chunk (absent
    /// here) or inline. Records the chunk's use when it matches.
    fn eligible(
        &mut self,
        wanted: &[&ReadChunk],
        uses: &mut Vec<ChunkUse>,
        kind: ChunkUse,
    ) -> Result<(), RpcRdmaError> {
        let n = self.u32()? as usize;
        let padded = n.checked_add(3).ok_or_else(bad)? & !3;
        match wanted.get(uses.len()) {
            Some(chunk) if chunk.position as usize == self.pos + self.absent => {
                let size = chunk.segments.iter().try_fold(0usize, |total, s| {
                    total.checked_add(s.length as usize).ok_or_else(bad)
                })?;
                if size != n && size != padded {
                    return Err(bad());
                }
                self.absent += padded;
                uses.push(kind);
                Ok(())
            }
            _ => self.skip(padded),
        }
    }
}

/// Check that every non-zero Read chunk of `chunks` carries DDP-eligible
/// data of the reduced call `reduced`, and say how each is delivered: WRITE
/// data stays placed, a symlink target is copied inline. Returns one use per
/// non-zero chunk, in order.
pub fn classify_read_chunks(
    reduced: &[u8],
    chunks: &[ReadChunk],
) -> Result<Vec<ChunkUse>, RpcRdmaError> {
    let wanted: Vec<_> = chunks.iter().filter(|c| c.position != 0).collect();
    let mut uses = Vec::with_capacity(wanted.len());
    if wanted.is_empty() {
        return Ok(uses);
    }
    let call = rpc::parse_call(reduced).map_err(|_| bad())?;
    if call.prog != rpc::NFS_PROGRAM || call.vers != 4 || call.proc_num != 1 {
        return Err(bad());
    }
    let header = reduced.len() - call.payload.len();
    let mut r = Cursor {
        data: reduced,
        pos: header,
        absent: 0,
    };
    r.opaque()?;
    r.u32()?;
    let operations = r.u32()?;
    if operations > MAX_COMPOUND_OPS {
        return Err(bad());
    }
    for _ in 0..operations {
        let op = r.u32()?;
        match op {
            OP_PUTROOTFH | OP_PUTPUBFH | OP_GETFH | OP_SAVEFH | OP_RESTOREFH | OP_LOOKUPP
            | OP_READLINK => {}
            OP_SEQUENCE => r.skip(32)?,
            OP_PUTFH | OP_LOOKUP | OP_REMOVE | OP_LINK | OP_SECINFO | OP_GETXATTR
            | OP_REMOVEXATTR => {
                r.opaque()?;
            }
            OP_GETATTR => r.bitmap()?,
            OP_ACCESS | OP_OPENATTR | OP_RECLAIM_COMPLETE | OP_SECINFO_NO_NAME => r.skip(4)?,
            OP_RENEW | OP_DELEGPURGE | OP_DESTROY_CLIENTID => r.skip(8)?,
            OP_COMMIT => r.skip(12)?,
            OP_DELEGRETURN | OP_DESTROY_SESSION | OP_FREE_STATEID | OP_SETCLIENTID_CONFIRM => {
                r.skip(16)?
            }
            OP_CLOSE | OP_OPEN_CONFIRM => r.skip(20)?,
            OP_OPEN_DOWNGRADE | OP_READ | OP_READ_PLUS | OP_SEEK => r.skip(28)?,
            OP_BIND_CONN_TO_SESSION => r.skip(24)?,
            OP_ALLOCATE | OP_DEALLOCATE => r.skip(32)?,
            OP_CLONE => r.skip(56)?,
            OP_RENAME => {
                r.opaque()?;
                r.opaque()?;
            }
            OP_VERIFY | OP_NVERIFY => r.attrs()?,
            OP_SETATTR => {
                r.skip(16)?;
                r.attrs()?;
            }
            OP_READDIR => {
                r.skip(24)?;
                r.bitmap()?;
            }
            OP_RELEASE_LOCKOWNER => r.owner()?,
            OP_TEST_STATEID => {
                let n = r.u32()? as usize;
                r.skip(n.checked_mul(16).ok_or_else(bad)?)?;
            }
            OP_WRITE => {
                r.skip(28)?;
                r.eligible(&wanted, &mut uses, ChunkUse::Placed)?;
            }
            OP_CREATE => {
                match r.u32()? {
                    5 => r.eligible(&wanted, &mut uses, ChunkUse::Inline)?,
                    3 | 4 => r.skip(8)?,
                    1 | 2 | 6 | 7 => {}
                    _ => return Err(bad()),
                }
                r.opaque()?;
                r.attrs()?;
            }
            OP_OPEN => {
                r.skip(12)?;
                r.owner()?;
                match r.u32()? {
                    0 => {}
                    1 => match r.u32()? {
                        0 | 1 => r.attrs()?,
                        2 => r.skip(8)?,
                        3 => {
                            r.skip(8)?;
                            r.attrs()?;
                        }
                        _ => return Err(bad()),
                    },
                    _ => return Err(bad()),
                }
                match r.u32()? {
                    0 | 3 => {
                        r.opaque()?;
                    }
                    1 => r.skip(4)?,
                    2 => {
                        r.skip(16)?;
                        r.opaque()?;
                    }
                    4 | 6 => {}
                    5 => r.skip(16)?,
                    _ => return Err(bad()),
                }
            }
            OP_LOCK => {
                r.skip(24)?;
                match r.u32()? {
                    0 => r.skip(20)?,
                    1 => {
                        r.skip(24)?;
                        r.owner()?;
                    }
                    _ => return Err(bad()),
                }
            }
            OP_LOCKT => {
                r.skip(20)?;
                r.owner()?;
            }
            OP_LOCKU => r.skip(40)?,
            OP_SETXATTR => {
                r.skip(4)?;
                r.opaque()?;
                r.opaque()?;
            }
            OP_LISTXATTRS => r.skip(12)?,
            // Protocol setup, pNFS, and offload calls use inline or position-zero
            // encoding; none have DDP-eligible arguments in RFC 8267.
            _ => return Err(bad()),
        }
        if uses.len() == wanted.len() {
            return Ok(uses);
        }
        // A chunk the walk has passed without matching names ineligible data.
        if (wanted[uses.len()].position as usize) < r.pos + r.absent {
            return Err(bad());
        }
    }
    Err(bad())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpcrdma::chunks::Segment;
    use crate::xdr::XdrWriter;

    /// The call with the chunk's data (and padding) removed, as sent.
    fn reduce(record: &[u8], chunk: &ReadChunk) -> Vec<u8> {
        let start = chunk.position as usize;
        let size: usize = chunk.segments.iter().map(|s| s.length as usize).sum();
        let end = (start + ((size + 3) & !3)).min(record.len());
        [&record[..start], &record[end..]].concat()
    }
    fn validate_read_chunks(
        record: &[u8],
        chunks: &[ReadChunk],
    ) -> Result<Vec<ChunkUse>, RpcRdmaError> {
        classify_read_chunks(&reduce(record, &chunks[0]), chunks)
    }
    fn write_call() -> (Vec<u8>, ReadChunk) {
        let mut w = XdrWriter::new();
        for n in [42, 0, 2, 100003, 4, 1, 0, 0, 0, 0, 0, 1, 3, OP_SEQUENCE] {
            w.put_u32(n);
        }
        w.put_raw(&[0; 32]);
        w.put_u32(OP_PUTFH);
        w.put_opaque(b"filehandle");
        w.put_u32(OP_WRITE);
        w.put_raw(&[0; 28]);
        w.put_u32(3);
        let position = w.len() as u32;
        w.put_opaque_fixed(b"abc");
        (
            w.into_bytes(),
            ReadChunk {
                position,
                segments: vec![Segment {
                    handle: 1,
                    length: 3,
                    offset: 0,
                }],
            },
        )
    }
    #[test]
    fn write_data_is_eligible_with_or_without_padding() {
        let (record, mut chunk) = write_call();
        assert_eq!(
            validate_read_chunks(&record, &[chunk.clone()]).unwrap(),
            vec![ChunkUse::Placed]
        );
        chunk.segments[0].length = 4;
        assert_eq!(
            validate_read_chunks(&record, &[chunk]).unwrap(),
            vec![ChunkUse::Placed]
        );
    }
    #[test]
    fn credentials_and_filehandles_cannot_be_reduced() {
        let (record, mut chunk) = write_call();
        for position in [8, 24, 40, 60, 92] {
            chunk.position = position;
            assert!(validate_read_chunks(&record, &[chunk.clone()]).is_err());
        }
        chunk.position = record.len() as u32 - 4;
        chunk.segments[0].length = 2;
        assert!(validate_read_chunks(&record, &[chunk]).is_err());
    }
    #[test]
    fn create_symlink_target_is_eligible_but_name_is_not() {
        let mut w = XdrWriter::new();
        for n in [
            42,
            0,
            2,
            100003,
            4,
            1,
            0,
            0,
            0,
            0,
            0,
            0,
            2,
            OP_PUTROOTFH,
            OP_CREATE,
            5,
        ] {
            w.put_u32(n);
        }
        let target_position = w.len() as u32 + 4;
        w.put_opaque(b"target");
        let name_position = w.len() as u32 + 4;
        w.put_opaque(b"link");
        w.put_u32(0);
        w.put_opaque(&[]);
        let record = w.into_bytes();
        let mut chunk = ReadChunk {
            position: target_position,
            segments: vec![Segment {
                handle: 1,
                length: 6,
                offset: 0,
            }],
        };
        assert_eq!(
            validate_read_chunks(&record, &[chunk.clone()]).unwrap(),
            vec![ChunkUse::Inline]
        );
        chunk.position = name_position;
        chunk.segments[0].length = 4;
        assert!(validate_read_chunks(&record, &[chunk]).is_err());
    }
}
