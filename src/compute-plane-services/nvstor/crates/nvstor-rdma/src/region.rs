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

//! Memory outside the pools that GETs land in directly: GPU memory
//! exported as a dma-buf (GPUDirect RDMA), so the server's RDMA WRITE goes
//! from the NIC straight into the GPU, with no host buffer on the way.
//!
//! - A [`RailRegion`] is one dma-buf registered once on every local NIC of
//!   a [`Rails`] (one memory region per NIC: a QP can only address memory
//!   registered in its own NIC's protection domain), remote-write only.
//! - A GET names a [`RegionSlice`] of it; slices are bounds-checked when
//!   they are made, so a GET can never land outside the region.
//! - The region holds the memory's owner, so the memory cannot be freed
//!   while any NIC can still write into it.
//! - The CPU may not be able to read the region (on some platforms a CPU load of
//!   `cuMemAlloc` memory faults), so a range that lands there is not
//!   checked on arrival. It comes back as [`Landed`], carrying the
//!   server's CRC-32C, and whoever can read the bytes checks them
//!   ([`Landed::verify`] over a copy, or a check on the device) before
//!   they are used. The NIC's writes are complete when the reply arrives,
//!   but GPU work (a copy, a kernel) is only guaranteed to see them once
//!   they are made visible to it (for CUDA, `cuFlushGPUDirectRDMAWrites`
//!   when the device does not order them itself): that is the
//!   consumer's step, before it reads.

use std::os::fd::BorrowedFd;
use std::sync::Arc;

use crate::device::{Access, MemoryRegion};
use crate::object::{GetError, ObjectRead};
use crate::rail::Rails;

/// One dma-buf registered on every NIC of a [`Rails`].
pub struct RailRegion {
    rails: Arc<Rails>,
    iova: u64,
    len: usize,
    /// Indexed by rail position. Declared before `_owner`, so the NICs
    /// lose access before the memory can be released.
    mrs: Vec<MemoryRegion>,
    _owner: Arc<dyn Send + Sync>,
}

impl RailRegion {
    /// Register `len` bytes of the dma-buf `fd` from `offset` on every NIC
    /// of `rails`, addressed by peers as `iova..iova + len` (for GPU
    /// memory, its device address). `owner` owns the memory and is kept
    /// until the region is dropped. Fails if any NIC refuses it.
    pub fn dmabuf(
        rails: &Arc<Rails>,
        fd: BorrowedFd<'_>,
        offset: u64,
        len: usize,
        iova: u64,
        owner: Arc<dyn Send + Sync>,
    ) -> Result<Self, String> {
        Self::dmabuf_with(rails, fd, offset, len, iova, owner, Access::REMOTE_WRITE)
    }

    /// [`Self::dmabuf`] with the access peers get: `REMOTE_READ_WRITE` for
    /// a region a server also reads from (the KV tier's landing pool, which
    /// PUTs are pulled from).
    pub fn dmabuf_with(
        rails: &Arc<Rails>,
        fd: BorrowedFd<'_>,
        offset: u64,
        len: usize,
        iova: u64,
        owner: Arc<dyn Send + Sync>,
        access: Access,
    ) -> Result<Self, String> {
        if rails.is_empty() {
            return Err("no RDMA NIC to register the region on".into());
        }
        iova.checked_add(len as u64)
            .ok_or_else(|| format!("region of {len} bytes at {iova:#x} wraps"))?;
        let mrs = (0..rails.len())
            .map(|i| {
                let t = rails
                    .transport(i)
                    .ok_or_else(|| format!("rail {i} has no transport"))?;
                t.device().register_dmabuf(fd, offset, len, iova, access)
            })
            .collect::<Result<Vec<_>, String>>()?;
        Ok(Self {
            rails: Arc::clone(rails),
            iova,
            len,
            mrs,
            _owner: owner,
        })
    }

    pub fn len(&self) -> usize {
        self.len
    }

    pub fn is_empty(&self) -> bool {
        self.len == 0
    }

    /// The address peers use for byte 0.
    pub fn iova(&self) -> u64 {
        self.iova
    }

    /// The region's rkey on the NIC at position `rail` of its [`Rails`]
    /// (the key a server uses to write it through that NIC).
    pub fn rkey(&self, rail: usize) -> Option<u32> {
        self.mrs.get(rail).map(|mr| mr.rkey)
    }

    /// Bytes `at..at + len`, or `None` if they are not all inside.
    pub fn slice(&self, at: usize, len: usize) -> Option<RegionSlice<'_>> {
        fits(at, len, self.len).then_some(RegionSlice {
            region: self,
            at,
            len,
        })
    }

    /// Registered on `rails` (the same set, not an equal one).
    pub(crate) fn is_on(&self, rails: &Arc<Rails>) -> bool {
        Arc::ptr_eq(&self.rails, rails)
    }
}

/// `at..at + len` is a non-empty range inside `0..total`.
fn fits(at: usize, len: usize, total: usize) -> bool {
    len > 0 && at.checked_add(len).is_some_and(|end| end <= total)
}

/// Part of a [`RailRegion`], inside it by construction.
#[derive(Clone, Copy)]
pub struct RegionSlice<'r> {
    region: &'r RailRegion,
    at: usize,
    len: usize,
}

impl RegionSlice<'_> {
    pub fn len(&self) -> usize {
        self.len
    }

    pub fn is_empty(&self) -> bool {
        self.len == 0
    }

    /// Offset of the slice in its region.
    pub fn at(&self) -> usize {
        self.at
    }

    pub(crate) fn region(&self) -> &RailRegion {
        self.region
    }

    /// Address and rkey the server writes byte 0 of the slice to, through
    /// the NIC at position `rail`.
    pub(crate) fn target(&self, rail: usize) -> Option<(u64, u32)> {
        let mr = self.region.mrs.get(rail)?;
        Some((self.region.iova + self.at as u64, mr.rkey))
    }
}

/// A range the server wrote into a [`RegionSlice`]: the generation is
/// checked, the bytes are not.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[must_use = "the bytes are unchecked until Landed::verify (or a check on the device) passes"]
pub struct Landed {
    pub read: ObjectRead,
    /// The server's CRC-32C of the `read.len` bytes.
    pub crc32c: u32,
}

impl Landed {
    /// Check a copy of the landed bytes against the server's CRC-32C.
    pub fn verify(&self, bytes: &[u8]) -> Result<(), GetError> {
        if bytes.len() != self.read.len {
            return Err(GetError::Transport(format!(
                "verifying {} bytes of a {}-byte range",
                bytes.len(),
                self.read.len
            )));
        }
        crate::object::verify_range(bytes, self.crc32c)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn slices_must_be_inside_and_non_empty() {
        assert!(fits(0, 4096, 4096));
        assert!(fits(4095, 1, 4096));
        assert!(!fits(4095, 2, 4096));
        assert!(!fits(0, 0, 4096));
        assert!(!fits(usize::MAX, 2, usize::MAX));
    }

    #[test]
    fn landed_verify_checks_length_and_crc() {
        let landed = Landed {
            read: ObjectRead {
                len: 9,
                size: 9,
                generation: 1,
            },
            crc32c: 0xE306_9283,
        };
        assert_eq!(landed.verify(b"123456789"), Ok(()));
        assert!(matches!(
            landed.verify(b"123456780"),
            Err(GetError::Corrupt { .. })
        ));
        assert!(matches!(
            landed.verify(b"12345678"),
            Err(GetError::Transport(_))
        ));
    }
}
