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

//! Two-sided SEND/RECV channel for small control messages.
//!
//! Each `QpLane` owns one `ControlChannel`. The channel carries the
//! S3-over-RDMA control plane (see `s3_rdma_proto`) and is decoupled
//! from the data path (one-sided `rdma_write` / `rdma_read` against
//! the bulk `MemoryPool`).
//!
//! Layout:
//!   * One SEND buffer per channel — control messages serialize
//!     through it. A `Mutex` on the channel covers the buffer until
//!     its SEND has completed; the contention is short and it
//!     avoids an `ibv_reg_mr` syscall per call.
//!   * `RECV_DEPTH` pre-posted RECV buffers. Every buffer is the
//!     transport's control message size: `CONTROL_BUF_SIZE` for
//!     S3-over-RDMA, `kv_proto::KV_MSG_SIZE` for the KV service; both ends
//!     of a connection use the same size. The kernel needs RECVs
//!     posted before the peer SENDs or we get RNR retry storms;
//!     pre-posting at lane bring-up amortises that cost.
//!   * Each RECV's `wr_id` is the lane key and its buffer index
//!     (`completion::recv_wr_id`), so the completion engine routes the
//!     CQE to this lane and the index tells which buffer the incoming
//!     SEND landed in. We re-post that exact buffer immediately after
//!     consuming the message so the depth stays steady.

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use parking_lot::Mutex;

use crate::completion::recv_wr_id;
use crate::connection::QueuePair;
use crate::device::{Access, MemoryRegion, RdmaDevice};
use crate::s3_rdma_proto::{Request, Response};

/// Bounds on a channel's message size.
pub const MIN_MSG_SIZE: usize = 256;
pub const MAX_MSG_SIZE: usize = 1 << 20;

/// Refuse a message size outside [`MIN_MSG_SIZE`]..=[`MAX_MSG_SIZE`].
pub fn check_msg_size(n: usize) -> Result<(), String> {
    if !(MIN_MSG_SIZE..=MAX_MSG_SIZE).contains(&n) {
        return Err(format!(
            "control message size {n} outside {MIN_MSG_SIZE}..={MAX_MSG_SIZE}"
        ));
    }
    Ok(())
}

/// Whether an encoder's reported length fits the buffer it was given.
fn check_encoded_len(len: usize, msg_size: usize) -> Result<(), String> {
    if len > msg_size {
        return Err(format!("encoded {len} bytes into a {msg_size}-byte buffer"));
    }
    Ok(())
}

/// Number of RECV buffers pre-posted per channel.
///
/// Sized for "burst of K incoming control messages while we're still
/// processing the previous one." 8 lets us absorb 8 messages of pending
/// control traffic per QP without RNR retry. Increase if the peer
/// pipelines more aggressively; the cost is just registered memory.
pub const RECV_DEPTH: usize = 8;
const _: () = assert!(
    RECV_DEPTH <= 1 << crate::completion::RECV_INDEX_BITS,
    "RECV buffer index must fit its wr_id bits"
);

/// SEND-side state, behind a Mutex so concurrent senders on the same
/// lane serialize cleanly. The buffer is registered once at channel
/// creation; the lock-held caller writes its bytes in place and posts
/// the SEND.
pub struct SendState {
    buf: SendBuf,
    #[allow(dead_code)] // MR Drop releases the registration
    mr: MemoryRegion,
}

/// The SEND buffer and whether it may be written.
struct SendBuf {
    bytes: Box<[u8]>,
    /// A SEND from this buffer was never seen to complete: the HCA may
    /// still read it, so it is never written again (until the lane's QP
    /// is destroyed, with the channel).
    poisoned: bool,
}

impl SendBuf {
    fn writable(&mut self) -> Result<&mut [u8], String> {
        if self.poisoned {
            return Err("control SEND buffer is still owned by an unfinished SEND".into());
        }
        Ok(&mut self.bytes[..])
    }
}

impl SendState {
    fn addr(&self) -> u64 {
        self.buf.bytes.as_ptr() as u64
    }
    fn lkey(&self) -> u32 {
        self.mr.lkey
    }

    /// Mark the buffer unusable: its last SEND's completion was never
    /// seen. Every later encode on this channel fails.
    pub fn poison(&mut self) {
        self.buf.poisoned = true;
    }

    fn writable(&mut self) -> Result<&mut [u8], String> {
        self.buf.writable()
    }
}

/// Per-lane control channel. Owns its own registered memory.
pub struct ControlChannel {
    /// SEND-side scratch + lock. Interior mutability lets the channel
    /// be shared as `&self` through the transport.
    send: Mutex<SendState>,

    /// RECV-side buffer ring. The MR is registered once; buffer
    /// contents are written by the NIC into the ring, then read out
    /// by the receiver via `with_recv_buf`. `wr_id` on each posted
    /// buffer is its index, so the completion queue tells us
    /// exactly which slot the SEND landed in.
    ///
    /// `recv_bufs` lives behind a raw pointer because the NIC writes
    /// to it asynchronously via DMA; we never form a Rust `&mut` to
    /// the same slot the NIC is writing. The contract: the kernel
    /// only writes into slot `i` while a RECV is posted with
    /// `wr_id=i`; consuming the CQE means the write has completed,
    /// and we read the buffer before re-posting.
    recv_base: u64,
    recv_mr: MemoryRegion,
    /// Kept alive for Drop. Read via raw `recv_base` pointer.
    _recv_bufs: Box<[u8]>,
    /// Bytes per message buffer, SEND and RECV alike.
    msg_size: usize,

    /// The lane's key in the completion engine, carried by every RECV's
    /// `wr_id`.
    lane_key: u64,

    /// Diagnostic counter.
    consumed_recvs: AtomicUsize,
}

// The NIC writes through `recv_base` from an unrelated thread, but we
// never alias those bytes with a Rust borrow. `Send`/`Sync` are safe
// because all interior mutation is gated by either:
//   - `send.lock()` for the SEND buffer, or
//   - the kernel-CQE handoff for each RECV slot.
unsafe impl Send for ControlChannel {}
unsafe impl Sync for ControlChannel {}

impl ControlChannel {
    /// Build a new channel and pre-post its RECV ring onto `qp`.
    ///
    /// `qp` must already be in `IBV_QPS_RTR` or later; otherwise
    /// post_recv returns -EINVAL.
    pub fn new(
        device: &Arc<RdmaDevice>,
        qp: &QueuePair,
        lane_key: u64,
        msg_size: usize,
    ) -> Result<Self, String> {
        check_msg_size(msg_size)?;
        let mut send_buf: Box<[u8]> = vec![0u8; msg_size].into_boxed_slice();
        let send_mr = device.register_memory(send_buf.as_mut_ptr(), msg_size, Access::LOCAL)?;

        let mut recv_bufs: Box<[u8]> = vec![0u8; msg_size * RECV_DEPTH].into_boxed_slice();
        let recv_base = recv_bufs.as_mut_ptr() as u64;
        let recv_mr =
            device.register_memory(recv_bufs.as_mut_ptr(), msg_size * RECV_DEPTH, Access::LOCAL)?;

        // Pre-post all RECVs. Each carries the lane key and its buffer
        // index, so the completion engine routes the CQE to this lane and
        // the index names the buffer the incoming SEND landed in.
        for i in 0..RECV_DEPTH {
            let addr = recv_base + (i * msg_size) as u64;
            qp.post_recv_with_id(addr, recv_mr.lkey, msg_size as u32, recv_wr_id(lane_key, i))?;
        }

        Ok(Self {
            send: Mutex::new(SendState {
                buf: SendBuf {
                    bytes: send_buf,
                    poisoned: false,
                },
                mr: send_mr,
            }),
            recv_base,
            recv_mr,
            _recv_bufs: recv_bufs,
            msg_size,
            lane_key,
            consumed_recvs: AtomicUsize::new(0),
        })
    }

    /// Encode a message into the SEND buffer under the send lock with
    /// `encode` (which returns the length written), then return the guard
    /// and `(addr, lkey, len)` for the SEND, as the typed encoders do.
    pub fn encode_with<'a>(
        &'a self,
        encode: impl FnOnce(&mut [u8]) -> std::io::Result<usize>,
    ) -> Result<(parking_lot::MutexGuard<'a, SendState>, u64, u32, usize), String> {
        let mut guard = self.send.lock();
        let len = encode(guard.writable()?).map_err(|e| format!("encode message: {e}"))?;
        check_encoded_len(len, self.msg_size)?;
        let addr = guard.addr();
        let lkey = guard.lkey();
        Ok((guard, addr, lkey, len))
    }

    /// Encode a `Request` into the SEND buffer under the send lock,
    /// then return `(addr, lkey, len)` so the caller can post the
    /// SEND. The lock is held by the returned guard — caller is
    /// expected to drop it after the SEND's completion confirms the
    /// bytes have left the NIC.
    pub fn encode_request<'a>(
        &'a self,
        msg: &Request,
    ) -> Result<(parking_lot::MutexGuard<'a, SendState>, u64, u32, usize), String> {
        let mut guard = self.send.lock();
        let len = msg
            .encode_into(guard.writable()?)
            .map_err(|e| format!("encode request: {e}"))?;
        let addr = guard.addr();
        let lkey = guard.lkey();
        Ok((guard, addr, lkey, len))
    }

    /// Encode a request wrapped in a signed auth envelope.
    ///
    /// `nonce` must be unique per access key within the replay window; the
    /// caller supplies it from a monotonic counter so two concurrent
    /// requests can never collide.
    pub fn encode_signed_request<'a>(
        &'a self,
        msg: &Request,
        creds: &crate::auth::Credentials,
        nonce: u64,
    ) -> Result<(parking_lot::MutexGuard<'a, SendState>, u64, u32, usize), String> {
        let mut guard = self.send.lock();
        let len = crate::s3_rdma_proto::encode_signed_into(msg, creds, nonce, guard.writable()?)
            .map_err(|e| format!("encode signed request: {e}"))?;
        let addr = guard.addr();
        let lkey = guard.lkey();
        Ok((guard, addr, lkey, len))
    }

    /// Same shape as `encode_request` but for `Response`.
    pub fn encode_response<'a>(
        &'a self,
        msg: &Response,
    ) -> Result<(parking_lot::MutexGuard<'a, SendState>, u64, u32, usize), String> {
        let mut guard = self.send.lock();
        let len = msg
            .encode_into(guard.writable()?)
            .map_err(|e| format!("encode response: {e}"))?;
        let addr = guard.addr();
        let lkey = guard.lkey();
        Ok((guard, addr, lkey, len))
    }

    /// Read the first `len` bytes from RECV buffer `index`. Returns a
    /// slice borrowing from the buffer; the caller must finish consuming
    /// it (and decode the message) before calling `repost_recv` for the
    /// same index.
    ///
    /// Safety: at the moment a CQE arrives for the buffer, the kernel
    /// has finished writing it. We're the only reader until the next
    /// `repost_recv(index)`, so the &[u8] is sound.
    pub fn with_recv_buf<R>(
        &self,
        index: usize,
        len: usize,
        f: impl FnOnce(&[u8]) -> R,
    ) -> Option<R> {
        if index >= RECV_DEPTH || len > self.msg_size {
            return None;
        }
        let addr = self.recv_base + (index * self.msg_size) as u64;
        // SAFETY: the kernel wrote `len` bytes into this slot; we read
        // them before re-posting (so no concurrent DMA).
        let slice = unsafe { std::slice::from_raw_parts(addr as *const u8, len) };
        Some(f(slice))
    }

    /// Re-post consumed RECV buffer `index` so the channel's depth stays
    /// at `RECV_DEPTH`. Must be called after the caller has finished
    /// reading it.
    pub fn repost_recv(&self, qp: &QueuePair, index: usize) -> Result<(), String> {
        if index >= RECV_DEPTH {
            return Err(format!("repost_recv: buffer {index} out of range"));
        }
        let addr = self.recv_base + (index * self.msg_size) as u64;
        qp.post_recv_with_id(
            addr,
            self.recv_mr.lkey,
            self.msg_size as u32,
            recv_wr_id(self.lane_key, index),
        )?;
        self.consumed_recvs.fetch_add(1, Ordering::Relaxed);
        Ok(())
    }

    /// Diagnostic: how many incoming SENDs have been consumed via this
    /// channel since it was created.
    pub fn consumed_recv_count(&self) -> usize {
        self.consumed_recvs.load(Ordering::Relaxed)
    }
}

#[cfg(test)]
mod send_buf_tests {
    use super::*;

    // Once a SEND from the buffer was never seen to
    // complete, the buffer is never written again.
    #[test]
    fn a_poisoned_send_buffer_refuses_writes() {
        let mut b = SendBuf {
            bytes: vec![0u8; crate::s3_rdma_proto::CONTROL_BUF_SIZE].into_boxed_slice(),
            poisoned: false,
        };
        b.writable().expect("writable")[0] = 1;
        b.poisoned = true;
        assert!(b.writable().is_err());
        assert_eq!(b.bytes[0], 1);
    }

    #[test]
    fn message_sizes_are_bounded() {
        assert!(check_msg_size(MIN_MSG_SIZE - 1).is_err());
        assert!(check_msg_size(MIN_MSG_SIZE).is_ok());
        assert!(check_msg_size(crate::s3_rdma_proto::CONTROL_BUF_SIZE).is_ok());
        assert!(check_msg_size(crate::kv_proto::KV_MSG_SIZE).is_ok());
        assert!(check_msg_size(MAX_MSG_SIZE).is_ok());
        assert!(check_msg_size(MAX_MSG_SIZE + 1).is_err());
    }

    #[test]
    fn an_encoder_reporting_more_than_its_buffer_is_refused() {
        assert!(check_encoded_len(4096, 4096).is_ok());
        assert!(check_encoded_len(4097, 4096).is_err());
    }
}
