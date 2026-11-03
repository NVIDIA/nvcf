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

//! RDMA connection — queue pair management and data transfer.
//!
//! Each connection represents a bidirectional RDMA channel between two nodes.
//! Supports one-sided (WRITE/READ) and two-sided (SEND/RECV) operations.

use std::ops::Range;
use std::ptr;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

use tracing::{debug, error};

use crate::device::{Access, RdmaDevice};
use crate::ffi;

/// Completion queue wrapper. Keeps its device context alive until the
/// CQ is destroyed.
pub struct CompletionQueue {
    cq: *mut ffi::ibv_cq,
    _device: Arc<RdmaDevice>,
}

unsafe impl Send for CompletionQueue {}
unsafe impl Sync for CompletionQueue {}

impl CompletionQueue {
    pub fn new(device: &Arc<RdmaDevice>, cq_size: i32) -> Result<Self, String> {
        unsafe {
            let cq = ffi::ibv_create_cq(
                device.context(),
                cq_size,
                ptr::null_mut(),
                ptr::null_mut(),
                0,
            );
            if cq.is_null() {
                return Err("failed to create CQ".into());
            }
            Ok(Self {
                cq,
                _device: Arc::clone(device),
            })
        }
    }

    /// A CQ that raises events on `channel` once armed with
    /// `ibv_req_notify_cq` (see `crate::completion`), signaled on
    /// completion vector `comp_vector` (clamped below the device's
    /// `num_comp_vectors`).
    ///
    /// # Safety
    /// `channel` must be a live completion channel of `device`'s context,
    /// and must outlive the CQ.
    pub unsafe fn with_channel(
        device: &Arc<RdmaDevice>,
        cq_size: i32,
        channel: *mut ffi::ibv_comp_channel,
        comp_vector: i32,
    ) -> Result<Self, String> {
        let vectors = i32::try_from(device.num_comp_vectors()).unwrap_or(i32::MAX);
        let comp_vector = if (0..vectors).contains(&comp_vector) {
            comp_vector
        } else {
            0
        };
        // SAFETY: the caller's contract; the context is the device's own.
        let cq = unsafe {
            ffi::ibv_create_cq(
                device.context(),
                cq_size,
                ptr::null_mut(),
                channel,
                comp_vector,
            )
        };
        if cq.is_null() {
            return Err(format!(
                "failed to create a {cq_size}-entry CQ on a completion channel \
                                (vector {comp_vector}): {}",
                std::io::Error::last_os_error()
            ));
        }
        Ok(Self {
            cq,
            _device: Arc::clone(device),
        })
    }

    pub fn as_ptr(&self) -> *mut ffi::ibv_cq {
        self.cq
    }

    /// Poll for completions. Returns number of completed work requests.
    pub fn poll(&self, wc: &mut [ffi::ibv_wc]) -> i32 {
        unsafe { ffi::ibv_poll_cq(self.cq, wc.len() as i32, wc.as_mut_ptr()) }
    }
}

impl Drop for CompletionQueue {
    fn drop(&mut self) {
        unsafe {
            if !self.cq.is_null() {
                ffi::ibv_destroy_cq(self.cq);
            }
        }
    }
}

/// Queue pair for RDMA operations. Keeps its device (PD and context)
/// alive until the QP is destroyed.
pub struct QueuePair {
    qp: *mut ffi::ibv_qp,
    /// Queue pair number.
    pub qp_num: u32,
    /// RDMA READs this QP may have outstanding as initiator.
    max_rd_atomic: u8,
    /// RDMA READs this QP may serve at once as responder.
    max_dest_rd_atomic: u8,
    /// Send queue depth the driver granted (at least what was asked).
    max_send_wr: u32,
    /// `wr_id`s for chained posts (see [`SendChain`]), distinct from the
    /// fixed ids (1..=4) of the single-WR posts.
    next_wr_id: AtomicU64,
    _device: Arc<RdmaDevice>,
}

/// First `wr_id` handed out to chained posts.
const CHAIN_WR_ID_BASE: u64 = 1 << 32;

unsafe impl Send for QueuePair {}
unsafe impl Sync for QueuePair {}

impl QueuePair {
    /// Create a Reliable Connected (RC) queue pair.
    pub fn create_rc(
        device: &Arc<RdmaDevice>,
        send_cq: &CompletionQueue,
        recv_cq: &CompletionQueue,
        max_send_wr: u32,
        max_recv_wr: u32,
    ) -> Result<Self, String> {
        unsafe {
            let mut qp_init_attr: ffi::ibv_qp_init_attr = ffi::zeroed();
            qp_init_attr.send_cq = send_cq.as_ptr();
            qp_init_attr.recv_cq = recv_cq.as_ptr();
            qp_init_attr.qp_type = ffi::IBV_QPT_RC;
            qp_init_attr.cap.max_send_wr = max_send_wr;
            qp_init_attr.cap.max_recv_wr = max_recv_wr;
            qp_init_attr.cap.max_send_sge = 1;
            qp_init_attr.cap.max_recv_sge = 1;
            qp_init_attr.cap.max_inline_data = 64;

            let qp = ffi::ibv_create_qp(device.pd(), &mut qp_init_attr);
            if qp.is_null() {
                return Err("failed to create QP".into());
            }

            let qp_num = (*qp).qp_num;
            debug!(qp_num, "created RC queue pair");

            let (max_rd_atomic, max_dest_rd_atomic) = device.rd_atomic_limits();
            Ok(Self {
                qp,
                qp_num,
                max_rd_atomic,
                max_dest_rd_atomic,
                // ibv_create_qp writes the granted capacities back.
                max_send_wr: qp_init_attr.cap.max_send_wr.max(max_send_wr),
                next_wr_id: AtomicU64::new(CHAIN_WR_ID_BASE),
                _device: Arc::clone(device),
            })
        }
    }

    /// Transition QP to INIT state.
    ///
    /// `remote` is what the PEER may do through this QP: only its remote
    /// bits are used. A server's QPs pass [`Access::LOCAL`], so no client
    /// can RDMA-READ or RDMA-WRITE server memory whatever rkey it holds;
    /// a client passes the access its own pool was registered with.
    pub fn to_init(&self, port: u8, remote: Access) -> Result<(), String> {
        unsafe {
            let mut attr: ffi::ibv_qp_attr = ffi::zeroed();
            attr.qp_state = ffi::IBV_QPS_INIT;
            attr.pkey_index = 0;
            attr.port_num = port;
            attr.qp_access_flags = remote.remote_bits();

            let mask = ffi::IBV_QP_STATE
                | ffi::IBV_QP_PKEY_INDEX
                | ffi::IBV_QP_PORT
                | ffi::IBV_QP_ACCESS_FLAGS;

            let ret = ffi::ibv_modify_qp(self.qp, &mut attr, mask);
            if ret != 0 {
                return Err(format!("QP to INIT failed: {ret}"));
            }
            Ok(())
        }
    }

    /// Transition QP to RTR (Ready to Receive) state.
    ///
    /// `path_mtu` should be the active MTU from the port (e.g., IBV_MTU_1024).
    /// `sgid_index` selects the local GID from the GID table (3 = RoCE v2 routable).
    pub fn to_rtr(
        &self,
        remote_qp_num: u32,
        remote_lid: u16,
        remote_gid: &ffi::ibv_gid,
        port: u8,
        path_mtu: u32,
        sgid_index: u8,
    ) -> Result<(), String> {
        unsafe {
            let mut attr: ffi::ibv_qp_attr = ffi::zeroed();
            attr.qp_state = ffi::IBV_QPS_RTR;
            attr.path_mtu = path_mtu;
            attr.dest_qp_num = remote_qp_num;
            attr.rq_psn = 0;
            attr.max_dest_rd_atomic = self.max_dest_rd_atomic;
            attr.min_rnr_timer = 12;
            attr.ah_attr.dlid = remote_lid;
            attr.ah_attr.sl = 0;
            attr.ah_attr.src_path_bits = 0;
            attr.ah_attr.port_num = port;

            // RoCE: use GRH (Global Routing Header) with GID.
            attr.ah_attr.is_global = 1;
            attr.ah_attr.grh.dgid = *remote_gid;
            attr.ah_attr.grh.sgid_index = sgid_index;
            attr.ah_attr.grh.hop_limit = 64;
            // ECN capable transport (ECT(0) = 0x02). Enables DCQCN congestion
            // control on RoCE v2 fabrics. Switches mark packets with CE (0x03)
            // when queues build up, and the NIC throttles the sender.
            attr.ah_attr.grh.traffic_class = 0x02;

            let mask = ffi::IBV_QP_STATE
                | ffi::IBV_QP_AV
                | ffi::IBV_QP_PATH_MTU
                | ffi::IBV_QP_DEST_QPN
                | ffi::IBV_QP_RQ_PSN
                | ffi::IBV_QP_MAX_DEST_RD_ATOMIC
                | ffi::IBV_QP_MIN_RNR_TIMER;

            let ret = ffi::ibv_modify_qp(self.qp, &mut attr, mask);
            if ret != 0 {
                return Err(format!(
                    "QP to RTR failed: {ret} ({})",
                    std::io::Error::from_raw_os_error(ret)
                ));
            }
            Ok(())
        }
    }

    /// Transition QP to RTS (Ready to Send) state.
    pub fn to_rts(&self) -> Result<(), String> {
        unsafe {
            let mut attr: ffi::ibv_qp_attr = ffi::zeroed();
            attr.qp_state = ffi::IBV_QPS_RTS;
            attr.sq_psn = 0;
            attr.timeout = 14;
            attr.retry_cnt = 7;
            attr.rnr_retry = 7;
            attr.max_rd_atomic = self.max_rd_atomic;

            let mask = ffi::IBV_QP_STATE
                | ffi::IBV_QP_TIMEOUT
                | ffi::IBV_QP_RETRY_CNT
                | ffi::IBV_QP_RNR_RETRY
                | ffi::IBV_QP_SQ_PSN
                | ffi::IBV_QP_MAX_QP_RD_ATOMIC;

            let ret = ffi::ibv_modify_qp(self.qp, &mut attr, mask);
            if ret != 0 {
                return Err(format!("QP to RTS failed: {ret}"));
            }
            Ok(())
        }
    }

    /// Move the QP to the ERROR state. Every posted work request,
    /// including idle RECVs, completes with a flush error, which is how a
    /// thread blocked waiting on this QP's receive CQ learns that the
    /// connection was torn down: an idle RC responder gets no CQE when
    /// its remote QP is destroyed.
    pub fn to_error(&self) -> Result<(), String> {
        unsafe {
            let mut attr: ffi::ibv_qp_attr = ffi::zeroed();
            attr.qp_state = ffi::IBV_QPS_ERR;
            let ret = ffi::ibv_modify_qp(self.qp, &mut attr, ffi::IBV_QP_STATE);
            if ret != 0 {
                return Err(format!("QP to ERR failed: {ret}"));
            }
            Ok(())
        }
    }

    /// Post an RDMA WRITE (one-sided, no remote CPU involvement).
    pub fn rdma_write(
        &self,
        local_addr: u64,
        local_lkey: u32,
        remote_addr: u64,
        remote_rkey: u32,
        len: u32,
    ) -> Result<(), String> {
        unsafe {
            let mut sge: ffi::ibv_sge = ffi::zeroed();
            sge.addr = local_addr;
            sge.length = len;
            sge.lkey = local_lkey;

            let mut wr: ffi::ibv_send_wr = ffi::zeroed();
            wr.wr_id = 1;
            wr.opcode = ffi::IBV_WR_RDMA_WRITE;
            wr.send_flags = ffi::IBV_SEND_SIGNALED;
            wr.sg_list = &mut sge;
            wr.num_sge = 1;
            wr.wr.rdma.remote_addr = remote_addr;
            wr.wr.rdma.rkey = remote_rkey;

            let mut bad_wr: *mut ffi::ibv_send_wr = ptr::null_mut();
            let ret = ffi::ibv_post_send(self.qp, &mut wr, &mut bad_wr);
            if ret != 0 {
                return Err(format!("rdma_write post_send failed: {ret}"));
            }
            Ok(())
        }
    }

    /// Post an RDMA READ (one-sided).
    pub fn rdma_read(
        &self,
        local_addr: u64,
        local_lkey: u32,
        remote_addr: u64,
        remote_rkey: u32,
        len: u32,
    ) -> Result<(), String> {
        unsafe {
            let mut sge: ffi::ibv_sge = ffi::zeroed();
            sge.addr = local_addr;
            sge.length = len;
            sge.lkey = local_lkey;

            let mut wr: ffi::ibv_send_wr = ffi::zeroed();
            wr.wr_id = 2;
            wr.opcode = ffi::IBV_WR_RDMA_READ;
            wr.send_flags = ffi::IBV_SEND_SIGNALED;
            wr.sg_list = &mut sge;
            wr.num_sge = 1;
            wr.wr.rdma.remote_addr = remote_addr;
            wr.wr.rdma.rkey = remote_rkey;

            let mut bad_wr: *mut ffi::ibv_send_wr = ptr::null_mut();
            let ret = ffi::ibv_post_send(self.qp, &mut wr, &mut bad_wr);
            if ret != 0 {
                return Err(format!("rdma_read post_send failed: {ret}"));
            }
            Ok(())
        }
    }

    /// Post a SEND operation (two-sided).
    pub fn send(&self, local_addr: u64, local_lkey: u32, len: u32) -> Result<(), String> {
        self.send_with_id(local_addr, local_lkey, len, 3)
    }

    /// Post a signaled SEND whose completion carries `wr_id`.
    pub fn send_with_id(
        &self,
        local_addr: u64,
        local_lkey: u32,
        len: u32,
        wr_id: u64,
    ) -> Result<(), String> {
        unsafe {
            let mut sge: ffi::ibv_sge = ffi::zeroed();
            sge.addr = local_addr;
            sge.length = len;
            sge.lkey = local_lkey;

            let mut wr: ffi::ibv_send_wr = ffi::zeroed();
            wr.wr_id = wr_id;
            wr.opcode = ffi::IBV_WR_SEND;
            wr.send_flags = ffi::IBV_SEND_SIGNALED;
            wr.sg_list = &mut sge;
            wr.num_sge = 1;

            let mut bad_wr: *mut ffi::ibv_send_wr = ptr::null_mut();
            let ret = ffi::ibv_post_send(self.qp, &mut wr, &mut bad_wr);
            if ret != 0 {
                return Err(format!("send post_send failed: {ret}"));
            }
            Ok(())
        }
    }

    /// Send queue depth: the most WRs that may be outstanding at once.
    pub fn max_send_wr(&self) -> u32 {
        self.max_send_wr
    }

    /// Reserve `n` consecutive `wr_id`s for a chain.
    pub fn next_wr_ids(&self, n: usize) -> u64 {
        self.next_wr_id.fetch_add(n as u64, Ordering::Relaxed)
    }

    /// Post every WR of `chain` with one `ibv_post_send` (one doorbell).
    ///
    /// On failure the provider stops at the first WR it could not post:
    /// the WRs before it ARE posted and will execute. The error says how
    /// many; see [`run_chain`] for what the caller must then do.
    pub fn post_chain(&self, chain: &mut SendChain) -> Result<(), ChainPostError> {
        if chain.is_empty() {
            return Ok(());
        }
        let mut bad_wr: *mut ffi::ibv_send_wr = ptr::null_mut();
        // SAFETY: the chain's WRs and SGEs are linked within its own
        // vectors (see `SendChain::new`), which outlive this call; the
        // provider copies them into the send queue before returning.
        let ret = unsafe { ffi::ibv_post_send(self.qp, chain.head(), &mut bad_wr) };
        if ret == 0 {
            return Ok(());
        }
        Err(ChainPostError {
            posted: chain.posted_before(bad_wr),
            code: ret,
        })
    }

    /// Post a zero-length, signaled SEND with `wr_id`. Used only as a
    /// fence on a QP in the error state, where it completes (with a flush
    /// error) after every WR posted before it.
    pub(crate) fn post_fence(&self, wr_id: u64) -> Result<(), i32> {
        // SAFETY: a zeroed WR with no SGEs; it points at nothing.
        unsafe {
            let mut wr: ffi::ibv_send_wr = ffi::zeroed();
            wr.wr_id = wr_id;
            wr.opcode = ffi::IBV_WR_SEND;
            wr.send_flags = ffi::IBV_SEND_SIGNALED;
            wr.num_sge = 0;
            let mut bad_wr: *mut ffi::ibv_send_wr = ptr::null_mut();
            match ffi::ibv_post_send(self.qp, &mut wr, &mut bad_wr) {
                0 => Ok(()),
                e => Err(e),
            }
        }
    }

    /// Post a RECV buffer (two-sided, pre-posts receive buffer).
    /// Uses `wr_id = 4` for legacy callers that don't care about
    /// distinguishing completions.
    pub fn post_recv(&self, local_addr: u64, local_lkey: u32, len: u32) -> Result<(), String> {
        self.post_recv_with_id(local_addr, local_lkey, len, 4)
    }

    /// Post a RECV buffer with a caller-chosen `wr_id`. The completion
    /// queue echoes that id back on the matching CQE so callers can
    /// pool many RECV buffers and route each completion to the right
    /// caller / buffer slot. RC QPs on the same QP guarantee in-order
    /// completion for the SEND/RECV channel, but `wr_id` is the
    /// portable way to track buffer ownership regardless.
    pub fn post_recv_with_id(
        &self,
        local_addr: u64,
        local_lkey: u32,
        len: u32,
        wr_id: u64,
    ) -> Result<(), String> {
        unsafe {
            let mut sge: ffi::ibv_sge = ffi::zeroed();
            sge.addr = local_addr;
            sge.length = len;
            sge.lkey = local_lkey;

            let mut wr: ffi::ibv_recv_wr = ffi::zeroed();
            wr.wr_id = wr_id;
            wr.sg_list = &mut sge;
            wr.num_sge = 1;

            let mut bad_wr: *mut ffi::ibv_recv_wr = ptr::null_mut();
            let ret = ffi::ibv_post_recv(self.qp, &mut wr, &mut bad_wr);
            if ret != 0 {
                return Err(format!("post_recv failed: {ret}"));
            }
            Ok(())
        }
    }
}

impl QueuePair {
    /// Destroy the QP now. `Ok` once it is gone: the provider has
    /// discarded its unpolled CQEs and the HCA no longer touches any
    /// buffer its WRs named. `Err(code)` if `ibv_destroy_qp` refused: the
    /// QP may still be live, so nothing it used may be reused (it is never
    /// destroyed again; whatever it holds is leaked). Idempotent.
    pub fn destroy(&mut self) -> Result<(), i32> {
        if self.qp.is_null() {
            return Ok(());
        }
        // SAFETY: a QP this wrapper created and has not destroyed.
        let ret = unsafe { ffi::ibv_destroy_qp(self.qp) };
        self.qp = ptr::null_mut();
        if ret == 0 {
            Ok(())
        } else {
            Err(ret)
        }
    }
}

impl Drop for QueuePair {
    fn drop(&mut self) {
        let qp_num = self.qp_num;
        if let Err(code) = self.destroy() {
            error!(
                qp_num,
                code, "ibv_destroy_qp failed; the QP and what it used are leaked"
            );
        }
    }
}

/// The one-sided operation of a chained work request.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OneSided {
    /// RDMA WRITE: local → remote.
    Write,
    /// RDMA READ: remote → local.
    Read,
}

/// One work request of a chain: `len` bytes between a local buffer
/// (`local_addr`, `lkey`) and a remote one (`remote_addr`, `rkey`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Segment {
    pub op: OneSided,
    pub local_addr: u64,
    pub lkey: u32,
    pub remote_addr: u64,
    pub rkey: u32,
    pub len: u32,
}

/// Split one transfer of `len` bytes into consecutive segments of at most
/// `max_wr_bytes` each (the port's `max_msg_sz`, or a smaller cap).
/// `len == 0` gives no segments.
pub fn split_transfer(
    op: OneSided,
    local_addr: u64,
    lkey: u32,
    remote_addr: u64,
    rkey: u32,
    len: u64,
    max_wr_bytes: u32,
) -> Result<Vec<Segment>, String> {
    if max_wr_bytes == 0 {
        return Err("max_wr_bytes is 0".into());
    }
    if local_addr.checked_add(len).is_none() || remote_addr.checked_add(len).is_none() {
        return Err(format!(
            "transfer of {len} bytes overflows the address space"
        ));
    }
    let n = usize::try_from(len.div_ceil(u64::from(max_wr_bytes)))
        .map_err(|_| "transfer needs too many work requests")?;
    let mut segs = Vec::with_capacity(n);
    let mut off = 0u64;
    while off < len {
        let chunk = (len - off).min(u64::from(max_wr_bytes));
        segs.push(Segment {
            op,
            local_addr: local_addr + off,
            lkey,
            remote_addr: remote_addr + off,
            rkey,
            // chunk <= max_wr_bytes, a u32.
            len: chunk as u32,
        });
        off += chunk;
    }
    Ok(segs)
}

/// One piece of a scatter or gather: `len` bytes at `local_offset` in
/// the call's one local buffer, to or from `remote_addr` under `rkey`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Piece {
    pub local_offset: u64,
    pub remote_addr: u64,
    pub rkey: u32,
    pub len: u64,
}

/// Most pieces in one scatter or gather (a KV block has one per layer
/// tensor; kv_proto allows 256).
pub const MAX_PIECES: usize = 4096;

/// Split pieces of one local buffer (`local_addr`, `local_len`, `lkey`),
/// in order, into segments of at most `max_wr_bytes` each: a scatter (one
/// block's payload to many remote pages) or a gather (many remote pages
/// into one buffer) as one list of WRs. Every piece must lie inside the
/// local buffer; one bad piece refuses the whole list. Empty pieces give
/// no segments.
pub fn split_pieces(
    op: OneSided,
    local_addr: u64,
    local_len: u64,
    lkey: u32,
    pieces: &[Piece],
    max_wr_bytes: u32,
) -> Result<Vec<Segment>, String> {
    if pieces.len() > MAX_PIECES {
        return Err(format!("{} pieces exceed {MAX_PIECES}", pieces.len()));
    }
    if max_wr_bytes == 0 {
        return Err("max_wr_bytes is 0".into());
    }
    let mut wrs = 0usize;
    for (i, p) in pieces.iter().enumerate() {
        match p.local_offset.checked_add(p.len) {
            Some(end) if end <= local_len => {}
            _ => {
                return Err(format!(
                    "piece {i} ({} bytes at {}) lies outside the {local_len}-byte local buffer",
                    p.len, p.local_offset
                ))
            }
        }
        if p.remote_addr.checked_add(p.len).is_none() {
            return Err(format!("piece {i} overflows the remote address space"));
        }
        wrs = usize::try_from(p.len.div_ceil(u64::from(max_wr_bytes)))
            .ok()
            .and_then(|n| wrs.checked_add(n))
            .ok_or("transfer needs too many work requests")?;
    }
    let mut segs = Vec::with_capacity(wrs);
    for p in pieces {
        let mut off = 0u64;
        while off < p.len {
            let chunk = (p.len - off).min(u64::from(max_wr_bytes));
            segs.push(Segment {
                op,
                local_addr: local_addr + p.local_offset + off,
                lkey,
                remote_addr: p.remote_addr + off,
                rkey: p.rkey,
                // chunk <= max_wr_bytes, a u32.
                len: chunk as u32,
            });
            off += chunk;
        }
    }
    Ok(segs)
}

/// Split `n` WRs into consecutive posts of at most `max_batch` WRs.
///
/// Only the last WR of each post is signaled, and the next post waits
/// for that completion. An unsignaled WR's send-queue slot is reclaimed
/// only when a later signaled WR completes, so `max_batch` must not
/// exceed the send queue depth, or the queue would wrap onto WRs whose
/// completion was never seen. It must not exceed the send CQ depth
/// either: after an error every flushed WR of the post completes with a
/// CQE, signaled or not.
pub fn plan_posts(n: usize, max_batch: usize) -> Vec<Range<usize>> {
    let step = max_batch.max(1);
    (0..n)
        .step_by(step)
        .map(|start| start..(start + step).min(n))
        .collect()
}

/// Work requests linked into one list for a single `ibv_post_send`
/// (doorbell batching), with selective signaling: only the last WR asks
/// for a completion. RC executes and completes a QP's send queue in
/// order, so that one CQE means every WR of the chain has completed.
pub struct SendChain {
    /// Referenced by `wrs[i].sg_list`; kept alive with them.
    _sges: Vec<ffi::ibv_sge>,
    wrs: Vec<ffi::ibv_send_wr>,
}

impl SendChain {
    /// Chain `segs` with `wr_id`s `first_wr_id`, `first_wr_id + 1`, ...
    pub fn new(segs: &[Segment], first_wr_id: u64) -> Self {
        Self::build(segs, |i, _| first_wr_id.wrapping_add(i as u64))
    }

    /// Chain `segs` for one completion slot: every WR carries `wr_id`, the
    /// last one `tail_wr_id` (see `crate::completion`).
    pub fn for_slot(segs: &[Segment], wr_id: u64, tail_wr_id: u64) -> Self {
        Self::build(segs, |_, last| if last { tail_wr_id } else { wr_id })
    }

    fn build(segs: &[Segment], id: impl Fn(usize, bool) -> u64) -> Self {
        let mut sges: Vec<ffi::ibv_sge> = segs
            .iter()
            .map(|s| {
                // SAFETY: ibv_sge is plain data.
                let mut sge: ffi::ibv_sge = unsafe { ffi::zeroed() };
                sge.addr = s.local_addr;
                sge.length = s.len;
                sge.lkey = s.lkey;
                sge
            })
            .collect();
        let mut wrs: Vec<ffi::ibv_send_wr> = segs
            .iter()
            .enumerate()
            .map(|(i, s)| {
                // SAFETY: ibv_send_wr is plain data and pointers, for
                // which all-zero is null.
                let mut wr: ffi::ibv_send_wr = unsafe { ffi::zeroed() };
                wr.wr_id = id(i, i + 1 == segs.len());
                wr.opcode = match s.op {
                    OneSided::Write => ffi::IBV_WR_RDMA_WRITE,
                    OneSided::Read => ffi::IBV_WR_RDMA_READ,
                };
                wr.send_flags = if i + 1 == segs.len() {
                    ffi::IBV_SEND_SIGNALED
                } else {
                    0
                };
                wr.num_sge = 1;
                wr.wr.rdma = ffi::ibv_send_wr_rdma {
                    remote_addr: s.remote_addr,
                    rkey: s.rkey,
                };
                wr
            })
            .collect();
        // Link only now that neither vector will reallocate: the pointers
        // are into their heap buffers, which moving the vectors (or the
        // chain) does not move.
        let sge_base = sges.as_mut_ptr();
        let wr_base = wrs.as_mut_ptr();
        let n = wrs.len();
        for (i, wr) in wrs.iter_mut().enumerate() {
            // SAFETY: i < n == sges.len(); i + 1 < n where it is used.
            wr.sg_list = unsafe { sge_base.add(i) };
            wr.next = if i + 1 < n {
                unsafe { wr_base.add(i + 1) }
            } else {
                ptr::null_mut()
            };
        }
        Self { _sges: sges, wrs }
    }

    pub fn len(&self) -> usize {
        self.wrs.len()
    }

    pub fn is_empty(&self) -> bool {
        self.wrs.is_empty()
    }

    /// The `wr_id` of the signaled last WR.
    pub fn tail_wr_id(&self) -> Option<u64> {
        self.wrs.last().map(|w| w.wr_id)
    }

    fn head(&mut self) -> *mut ffi::ibv_send_wr {
        self.wrs.as_mut_ptr()
    }

    /// How many WRs precede `bad`, the WR a failed post stopped at: those
    /// were posted. Null means none was. A pointer outside the chain (a
    /// provider that does not set it properly) is taken as "any may
    /// have been": the whole chain.
    fn posted_before(&self, bad: *const ffi::ibv_send_wr) -> usize {
        if bad.is_null() {
            return 0;
        }
        let base = self.wrs.as_ptr() as usize;
        let size = std::mem::size_of::<ffi::ibv_send_wr>();
        let at = (bad as usize).wrapping_sub(base);
        if at.is_multiple_of(size) && at / size < self.wrs.len() {
            at / size
        } else {
            self.wrs.len()
        }
    }
}

/// `ibv_post_send` refused a chain at WR index `posted`; the WRs before
/// it were posted.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ChainPostError {
    pub posted: usize,
    pub code: i32,
}

impl std::fmt::Display for ChainPostError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "post_send failed ({}) after {} work requests of the chain were posted",
            self.code, self.posted
        )
    }
}

/// Run `segs` on `qp` as chained posts of at most `max_batch` WRs (see
/// [`plan_posts`]), each followed by a wait for its signaled last WR on
/// `send_cq`. The caller must be the only one posting to `qp` and
/// polling `send_cq` meanwhile (the lane's send lock).
///
/// An error leaves the QP unusable (a failed WR moves an RC QP to the
/// error state) and the caller is expected to disconnect it, as after a
/// single-WR failure. Before returning an error this waits until the
/// hardware is done with every posted WR, so the caller may free or
/// reuse the buffers:
///
/// * a WR that completed with an error: its CQE and the flush
///   completions of the WRs after it (errors are always reported, even
///   for unsignaled WRs) are drained up to the chain's last WR;
/// * a post the provider refused partway: the WRs it did post have no
///   signaled WR behind them, so the QP is moved to the error state and
///   a signaled fence WR posted after them; its flush completion comes
///   after theirs. If even that fails the error says so, and only
///   destroying the QP (disconnecting) guarantees the buffers are idle.
pub fn run_chain(
    qp: &QueuePair,
    send_cq: &CompletionQueue,
    segs: &[Segment],
    max_batch: usize,
) -> Result<(), String> {
    for range in plan_posts(segs.len(), max_batch) {
        let n = range.len();
        let mut chain = SendChain::new(&segs[range], qp.next_wr_ids(n));
        let Some(tail) = chain.tail_wr_id() else {
            continue;
        };
        let deadline = Instant::now() + INFLIGHT_POLL_TIMEOUT;
        match qp.post_chain(&mut chain) {
            Ok(()) => {
                if let Some(e) = drain_to(send_cq, tail, deadline)? {
                    return Err(e);
                }
            }
            Err(e) if e.posted == 0 => return Err(e.to_string()),
            Err(e) => return Err(format!("{e}; {}", fence_after_partial_post(qp, send_cq))),
        }
    }
    Ok(())
}

/// After a partial post: stop the QP and wait until the WRs that were
/// posted are finished. Returns what happened, for the error message.
fn fence_after_partial_post(qp: &QueuePair, send_cq: &CompletionQueue) -> String {
    if let Err(e) = qp.to_error() {
        return format!("could not stop the QP ({e}); posted work requests may still be running");
    }
    let fence = qp.next_wr_ids(1);
    let deadline = Instant::now() + INFLIGHT_POLL_TIMEOUT;
    loop {
        match qp.post_fence(fence) {
            Ok(()) => break,
            Err(code) if Instant::now() < deadline => {
                debug!(code, "fence post refused; retrying while the QP flushes");
                std::thread::sleep(std::time::Duration::from_millis(1));
            }
            Err(code) => {
                return format!("QP stopped, fence post failed ({code}); posted work requests may still be running");
            }
        }
    }
    match drain_to(send_cq, fence, deadline) {
        Ok(_) => "QP stopped; posted work requests finished".into(),
        Err(e) => {
            format!("QP stopped, fence not seen ({e}); posted work requests may still be running")
        }
    }
}

/// Consume CQEs from `cq` up to and including the one for `wr_id`.
/// Returns the first error completion among them (`None` if all
/// succeeded); `Err` when `deadline` passes first or the CQ fails.
fn drain_to(cq: &CompletionQueue, wr_id: u64, deadline: Instant) -> Result<Option<String>, String> {
    let mut first_error = None;
    loop {
        let wc = poll_raw(cq, Some(deadline)).map_err(|e| match e {
            PollError::Timeout => {
                format!("poll timeout ({INFLIGHT_POLL_TIMEOUT:?} wall, no CQE for wr_id {wr_id})")
            }
            PollError::Failed(msg) => msg,
        })?;
        if wc.status != ffi::IBV_WC_SUCCESS && first_error.is_none() {
            first_error = Some(wc_error(&wc));
        }
        if wc.wr_id == wr_id {
            return Ok(first_error);
        }
    }
}

/// Connection info exchanged between peers for QP setup.
#[derive(Debug, Clone, Copy)]
pub struct ConnectionInfo {
    pub qp_num: u32,
    pub lid: u16,
    pub gid: ffi::ibv_gid,
}

/// Wall-clock bound for an IN-FLIGHT completion: we posted a work request
/// and are waiting for its CQE.
///
/// Must exceed the RC retry horizon (timeout=14, retry=7 → ~470 ms) so a
/// genuine QP error surfaces as a flush CQE with a real status instead of
/// timing out first and hiding the cause.
pub const INFLIGHT_POLL_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(5);

/// How long to spin before falling back to sleeping while waiting.
///
/// Spinning wins on the microsecond-scale path (a posted op completing);
/// past that it is just burning a core, which matters because callers can
/// wait minutes on an idle connection.
const SPIN_WINDOW: std::time::Duration = std::time::Duration::from_micros(200);

/// Longest sleep between polls once we've stopped spinning. Bounds idle CPU
/// to ~1000 polls/sec/connection while adding at most 1 ms of wake-up
/// latency — irrelevant next to the seconds-to-minutes idle it covers.
const MAX_POLL_BACKOFF: std::time::Duration = std::time::Duration::from_millis(1);

/// Helper: poll a CQ for one completion, bounded by wall clock.
///
/// This is the IN-FLIGHT variant: a work request has been posted and its
/// CQE is expected promptly. For waiting on an idle connection (a server
/// waiting for a client's next request) use [`poll_completion_idle`] —
/// applying this 5 s bound there disconnects clients that are merely
/// between operations.
///
/// `max_polls` is kept for source compatibility and is unused; the real
/// bound is wall-clock.
pub fn poll_completion(cq: &CompletionQueue, _max_polls: u64) -> Result<ffi::ibv_wc, String> {
    poll_completion_deadline(cq, Some(std::time::Instant::now() + INFLIGHT_POLL_TIMEOUT)).map_err(
        |e| match e {
            PollError::Timeout => "poll timeout (5s wall, no CQE)".to_string(),
            PollError::Failed(msg) => msg,
        },
    )
}

/// Wait for a completion on an otherwise IDLE connection.
///
/// Unlike [`poll_completion`] this does not impose a short deadline: the
/// caller is waiting for a peer to send something on its own schedule, and
/// for the checkpoint workload that gap is legitimately minutes.
/// Bounding it at 5 s made every idle client look like a dead one — the
/// dispatcher tore the session down and the client's next request was never
/// received, so it hung. That never showed up in benches because bench
/// traffic is continuous.
///
/// `timeout: None` waits indefinitely; the connection dying surfaces as a
/// CQ error (flush CQE), which is the real disconnect signal.
pub fn poll_completion_idle(
    cq: &CompletionQueue,
    timeout: Option<std::time::Duration>,
) -> Result<ffi::ibv_wc, String> {
    let deadline = timeout.map(|t| std::time::Instant::now() + t);
    poll_completion_deadline(cq, deadline).map_err(|e| match e {
        PollError::Timeout => "idle poll timeout (no CQE)".to_string(),
        PollError::Failed(msg) => msg,
    })
}

/// Distinguishes "nothing arrived in time" from "the CQ reported an error",
/// so callers can treat an idle timeout differently from a dead connection.
enum PollError {
    Timeout,
    Failed(String),
}

fn wc_error(wc: &ffi::ibv_wc) -> String {
    format!(
        "WC error: opcode={} status={} (vendor_err=0x{:x}) wr_id={}",
        wc.opcode,
        ffi::wc_status_str(wc.status),
        wc.vendor_err,
        wc.wr_id,
    )
}

/// Core poll loop for one CQE that must have succeeded. `deadline: None`
/// waits forever.
fn poll_completion_deadline(
    cq: &CompletionQueue,
    deadline: Option<std::time::Instant>,
) -> Result<ffi::ibv_wc, PollError> {
    let wc = poll_raw(cq, deadline)?;
    if wc.status != ffi::IBV_WC_SUCCESS {
        return Err(PollError::Failed(wc_error(&wc)));
    }
    Ok(wc)
}

/// The next CQE, whatever its status: spin briefly for latency, then
/// sleep-backoff to keep an idle wait from pinning a core.
fn poll_raw(
    cq: &CompletionQueue,
    deadline: Option<std::time::Instant>,
) -> Result<ffi::ibv_wc, PollError> {
    let mut wc: [ffi::ibv_wc; 1] = unsafe { ffi::zeroed() };
    let started = std::time::Instant::now();
    let mut backoff = std::time::Duration::from_micros(1);

    loop {
        let n = cq.poll(&mut wc);
        if n > 0 {
            return Ok(wc[0]);
        }
        if n < 0 {
            return Err(PollError::Failed(format!("poll_cq error: {n}")));
        }

        let now = std::time::Instant::now();
        if let Some(deadline) = deadline {
            if now >= deadline {
                return Err(PollError::Timeout);
            }
        }

        if now.duration_since(started) < SPIN_WINDOW {
            std::hint::spin_loop();
        } else {
            // Past the spin window: sleep with exponential backoff so a
            // long wait costs ~no CPU. Never overshoot the deadline.
            let mut nap = backoff;
            if let Some(deadline) = deadline {
                nap = nap.min(deadline.saturating_duration_since(now));
            }
            if !nap.is_zero() {
                std::thread::sleep(nap);
            }
            backoff = (backoff * 2).min(MAX_POLL_BACKOFF);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The idle wait must honor the CALLER's timeout, not the 5 s
    // in-flight bound. Before the split there was one hardcoded 5 s
    // deadline, so a server waiting on an idle client gave up after 5 s and
    // the dispatcher killed a perfectly healthy session.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn idle_poll_uses_caller_timeout_not_the_inflight_bound() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let cq = CompletionQueue::new(&dev, 16).unwrap();

        let started = std::time::Instant::now();
        let result = poll_completion_idle(&cq, Some(std::time::Duration::from_millis(300)));
        let elapsed = started.elapsed();

        assert!(result.is_err(), "empty CQ must time out, not return a CQE");
        assert!(
            elapsed >= std::time::Duration::from_millis(250),
            "returned too early ({elapsed:?}) — timeout not honored"
        );
        assert!(
            elapsed < std::time::Duration::from_secs(2),
            "took {elapsed:?} — still using the 5s in-flight bound"
        );
    }

    // The idle path must not busy-spin. A spin loop would peg a core
    // for the whole wait; the backoff sleeps instead. Measure CPU time
    // consumed across a 400 ms idle wait — spinning burns ~400 ms of CPU,
    // sleeping burns ~0.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn idle_poll_does_not_burn_cpu() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let cq = CompletionQueue::new(&dev, 16).unwrap();

        let cpu_before = thread_cpu_time();
        let _ = poll_completion_idle(&cq, Some(std::time::Duration::from_millis(400)));
        let cpu_used = thread_cpu_time().saturating_sub(cpu_before);

        assert!(
            cpu_used < std::time::Duration::from_millis(150),
            "idle wait burned {cpu_used:?} of CPU over 400ms — looks like a busy-spin"
        );
    }

    /// This thread's consumed CPU time (user + system).
    fn thread_cpu_time() -> std::time::Duration {
        let mut ts = libc::timespec {
            tv_sec: 0,
            tv_nsec: 0,
        };
        // SAFETY: writing into a stack timespec we own.
        unsafe { libc::clock_gettime(libc::CLOCK_THREAD_CPUTIME_ID, &mut ts) };
        std::time::Duration::new(ts.tv_sec as u64, ts.tv_nsec as u32)
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn create_cq_and_qp() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let send_cq = CompletionQueue::new(&dev, 16).unwrap();
        let recv_cq = CompletionQueue::new(&dev, 16).unwrap();
        let qp = QueuePair::create_rc(&dev, &send_cq, &recv_cq, 16, 16).unwrap();

        assert_ne!(qp.qp_num, 0);

        // Transition to INIT.
        qp.to_init(dev.port, Access::REMOTE_READ_WRITE).unwrap();
    }

    /// Loopback RDMA WRITE + READ: connect a QP to itself,
    /// WRITE data from src buffer to dst buffer, then READ it back.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn loopback_rdma_write_read() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let send_cq = CompletionQueue::new(&dev, 64).unwrap();
        let recv_cq = CompletionQueue::new(&dev, 64).unwrap();
        let qp = QueuePair::create_rc(&dev, &send_cq, &recv_cq, 64, 64).unwrap();

        // Register two buffers: src (data to write) and dst (target).
        let size = 4096;
        let mut src_buf = vec![0xABu8; size];
        let mut dst_buf = vec![0u8; size];
        let mut read_buf = vec![0u8; size];

        let src_mr = dev
            .register_memory(src_buf.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .unwrap();
        let dst_mr = dev
            .register_memory(dst_buf.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .unwrap();
        let read_mr = dev
            .register_memory(read_buf.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .unwrap();

        // Get local connection info for loopback.
        let gid_index = dev.roce_gid_index();
        let gid = dev.gid(gid_index as i32).unwrap();
        let lid = dev.lid().unwrap_or(0);

        // QP state transitions: INIT → RTR → RTS (pointing to itself).
        qp.to_init(dev.port, Access::REMOTE_READ_WRITE).unwrap();
        let mtu = dev.active_mtu().unwrap();
        // sgid_index=0 is fine for loopback (link-local GID).
        qp.to_rtr(qp.qp_num, lid, &gid, dev.port, mtu, gid_index)
            .unwrap();
        qp.to_rts().unwrap();

        println!("QP {} in RTS state (loopback)", qp.qp_num);

        // RDMA WRITE: src_buf → dst_buf (one-sided, no remote CPU).
        qp.rdma_write(
            src_buf.as_ptr() as u64,
            src_mr.lkey,
            dst_buf.as_ptr() as u64,
            dst_mr.rkey,
            size as u32,
        )
        .unwrap();

        let wc = poll_completion(&send_cq, 1_000_000).unwrap();
        println!("RDMA WRITE complete: {} bytes", wc.byte_len);

        // Verify dst_buf now contains 0xAB.
        assert_eq!(dst_buf[0], 0xAB, "first byte mismatch");
        assert_eq!(dst_buf[size - 1], 0xAB, "last byte mismatch");
        assert!(
            dst_buf.iter().all(|&b| b == 0xAB),
            "RDMA WRITE data mismatch"
        );
        println!("RDMA WRITE verified: all {} bytes = 0xAB", size);

        // RDMA READ: read dst_buf back into read_buf.
        qp.rdma_read(
            read_buf.as_mut_ptr() as u64,
            read_mr.lkey,
            dst_buf.as_ptr() as u64,
            dst_mr.rkey,
            size as u32,
        )
        .unwrap();

        let wc = poll_completion(&send_cq, 1_000_000).unwrap();
        println!("RDMA READ complete: {} bytes", wc.byte_len);

        assert!(
            read_buf.iter().all(|&b| b == 0xAB),
            "RDMA READ data mismatch"
        );
        println!("RDMA READ verified: all {} bytes = 0xAB", size);
    }

    fn seg(i: u64, len: u32) -> Segment {
        Segment {
            op: OneSided::Write,
            local_addr: 0x1000 + i,
            lkey: 7,
            remote_addr: 0x9000 + i,
            rkey: 9,
            len,
        }
    }

    // The WR array is handed to libibverbs as `struct ibv_send_wr[]`
    // through `next` pointers, and `bad_wr` is turned back into an index
    // by its size: the layout must be libibverbs' own.
    #[test]
    #[cfg(target_pointer_width = "64")]
    fn send_wr_has_the_libibverbs_layout() {
        assert_eq!(std::mem::size_of::<ffi::ibv_send_wr>(), 128);
        assert_eq!(std::mem::offset_of!(ffi::ibv_send_wr, next), 8);
        assert_eq!(std::mem::offset_of!(ffi::ibv_send_wr, sg_list), 16);
        assert_eq!(std::mem::offset_of!(ffi::ibv_send_wr, opcode), 28);
        assert_eq!(std::mem::offset_of!(ffi::ibv_send_wr, send_flags), 32);
        assert_eq!(std::mem::offset_of!(ffi::ibv_send_wr, wr), 40);
        assert_eq!(
            std::mem::offset_of!(ffi::ibv_send_wr, qp_type_xrc_remote_srqn),
            72
        );
        assert_eq!(std::mem::offset_of!(ffi::ibv_send_wr, bind_mw_or_tso), 80);
        assert_eq!(std::mem::size_of::<ffi::ibv_sge>(), 16);
    }

    #[test]
    fn split_transfer_cuts_at_the_wr_size() {
        let s = split_transfer(OneSided::Read, 1000, 1, 5000, 2, 10, 4).expect("split");
        assert_eq!(s.iter().map(|g| g.len).collect::<Vec<_>>(), [4, 4, 2]);
        assert_eq!(
            s.iter().map(|g| g.local_addr).collect::<Vec<_>>(),
            [1000, 1004, 1008]
        );
        assert_eq!(
            s.iter().map(|g| g.remote_addr).collect::<Vec<_>>(),
            [5000, 5004, 5008]
        );
        assert!(s
            .iter()
            .all(|g| g.op == OneSided::Read && g.lkey == 1 && g.rkey == 2));
        // Exact multiple, one WR, nothing.
        assert_eq!(
            split_transfer(OneSided::Write, 0, 0, 0, 0, 8, 4)
                .expect("split")
                .len(),
            2
        );
        assert_eq!(
            split_transfer(OneSided::Write, 0, 0, 0, 0, 3, 4)
                .expect("split")
                .len(),
            1
        );
        assert!(split_transfer(OneSided::Write, 0, 0, 0, 0, 0, 4)
            .expect("split")
            .is_empty());
        // A 16 MiB GET range under a 1 GiB port limit stays one WR.
        assert_eq!(
            split_transfer(OneSided::Write, 0, 0, 0, 0, 16 << 20, 1 << 30)
                .expect("split")
                .len(),
            1
        );
        // More than 4 GiB: allowed now, as several WRs.
        let big = split_transfer(OneSided::Write, 0, 0, 0, 0, 5 << 30, 1 << 30).expect("split");
        assert_eq!(big.len(), 5);
        assert_eq!(big[4].remote_addr, 4 << 30);
    }

    #[test]
    fn split_pieces_keeps_each_piece_s_addresses_and_order() {
        let pieces = [
            Piece {
                local_offset: 0,
                remote_addr: 9000,
                rkey: 2,
                len: 10,
            },
            Piece {
                local_offset: 10,
                remote_addr: 100,
                rkey: 3,
                len: 0,
            },
            Piece {
                local_offset: 10,
                remote_addr: 500,
                rkey: 3,
                len: 4,
            },
        ];
        let s = split_pieces(OneSided::Write, 1000, 14, 1, &pieces, 4).expect("split");
        assert_eq!(
            s.iter()
                .map(|g| (g.local_addr, g.remote_addr, g.rkey, g.len))
                .collect::<Vec<_>>(),
            [
                (1000, 9000, 2, 4),
                (1004, 9004, 2, 4),
                (1008, 9008, 2, 2),
                (1010, 500, 3, 4)
            ]
        );
        assert!(s.iter().all(|g| g.op == OneSided::Write && g.lkey == 1));
        assert_eq!(s.capacity(), 4, "sized once");
        assert!(split_pieces(OneSided::Read, 0, 0, 1, &[], 4)
            .expect("empty")
            .is_empty());
        // One bad piece refuses the whole list.
        let overflow = [
            pieces[0],
            Piece {
                local_offset: 0,
                remote_addr: u64::MAX,
                rkey: 2,
                len: 1,
            },
        ];
        assert!(split_pieces(OneSided::Read, 0, 100, 1, &overflow, 4).is_err());
        // A piece past the end of the local buffer.
        assert!(split_pieces(OneSided::Read, 0, 13, 1, &pieces, 4).is_err());
        let wrap = [Piece {
            local_offset: u64::MAX,
            remote_addr: 0,
            rkey: 2,
            len: 2,
        }];
        assert!(split_pieces(OneSided::Read, 0, u64::MAX, 1, &wrap, 4).is_err());
        assert!(
            split_pieces(OneSided::Read, 0, 14, 1, &pieces, 0).is_err(),
            "max_wr_bytes 0"
        );
        let too_many = vec![
            Piece {
                local_offset: 0,
                remote_addr: 0,
                rkey: 0,
                len: 0
            };
            MAX_PIECES + 1
        ];
        assert!(split_pieces(OneSided::Read, 0, 1, 1, &too_many, 4).is_err());
        // A block of the example layout, 99 pages, under a 1 GiB WR limit: 99 WRs.
        let mut off = 0;
        let block: Vec<Piece> = (0..99u64)
            .map(|i| {
                let page = if i < 78 { 36_864 } else { 8_448 };
                let p = Piece {
                    local_offset: off,
                    remote_addr: i << 32,
                    rkey: 2,
                    len: page,
                };
                off += page;
                p
            })
            .collect();
        assert_eq!(
            split_pieces(OneSided::Write, 0, 3_052_800, 1, &block, 1 << 30)
                .expect("split")
                .len(),
            99
        );
    }

    #[test]
    fn split_transfer_rejects_bad_input() {
        assert!(split_transfer(OneSided::Write, 0, 0, 0, 0, 1, 0).is_err());
        assert!(split_transfer(OneSided::Write, u64::MAX - 1, 0, 0, 0, 4, 4).is_err());
        assert!(split_transfer(OneSided::Write, 0, 0, u64::MAX, 0, 1, 4).is_err());
    }

    #[test]
    fn plan_posts_never_exceeds_the_batch() {
        assert!(plan_posts(0, 4).is_empty());
        assert_eq!(plan_posts(3, 4), vec![0..3]);
        assert_eq!(plan_posts(5, 2), [0..2, 2..4, 4..5]);
        assert_eq!(plan_posts(4, 2), [0..2, 2..4]);
        assert_eq!(
            plan_posts(2, 0),
            [0..1, 1..2],
            "a zero batch posts one WR at a time"
        );
        let plan = plan_posts(1000, 128);
        assert!(plan.iter().all(|r| r.len() <= 128));
        assert_eq!(plan.iter().map(|r| r.len()).sum::<usize>(), 1000);
        assert_eq!(plan.len(), 8);
    }

    #[test]
    fn chain_links_wrs_and_signals_only_the_last() {
        let segs: Vec<Segment> = (0..3).map(|i| seg(i * 100, 100)).collect();
        let chain = SendChain::new(&segs, 40);
        // Moving the chain must not move what the pointers point at.
        let chain = Box::new(chain);
        assert_eq!(chain.len(), 3);
        assert_eq!(chain.tail_wr_id(), Some(42));
        let base = chain.wrs.as_ptr();
        for (i, wr) in chain.wrs.iter().enumerate() {
            assert_eq!(wr.wr_id, 40 + i as u64);
            assert_eq!(wr.opcode, ffi::IBV_WR_RDMA_WRITE);
            assert_eq!(wr.num_sge, 1);
            let last = i == 2;
            assert_eq!(wr.send_flags & ffi::IBV_SEND_SIGNALED != 0, last, "WR {i}");
            if last {
                assert!(wr.next.is_null());
            } else {
                assert_eq!(wr.next as *const ffi::ibv_send_wr, base.wrapping_add(i + 1));
            }
            // SAFETY: sg_list points into the chain's own SGE vector.
            let sge = unsafe { &*wr.sg_list };
            assert_eq!(
                (sge.addr, sge.length, sge.lkey),
                (0x1000 + i as u64 * 100, 100, 7)
            );
            // SAFETY: an RDMA op's union arm is `rdma`.
            let rdma = unsafe { wr.wr.rdma };
            assert_eq!((rdma.remote_addr, rdma.rkey), (0x9000 + i as u64 * 100, 9));
        }
        // Reads use the READ opcode; a single WR is its own signaled tail.
        let read = SendChain::new(
            &[Segment {
                op: OneSided::Read,
                ..seg(0, 8)
            }],
            7,
        );
        assert_eq!(read.wrs[0].opcode, ffi::IBV_WR_RDMA_READ);
        assert_eq!(read.wrs[0].send_flags, ffi::IBV_SEND_SIGNALED);
        assert!(read.wrs[0].next.is_null());
        assert!(SendChain::new(&[], 0).is_empty());
        assert_eq!(SendChain::new(&[], 0).tail_wr_id(), None);
    }

    // Every chain a plan produces has exactly one signaled WR, its last,
    // so no more than `max_batch` WRs are ever unsignaled in a row.
    #[test]
    fn planned_chains_signal_at_least_every_batch() {
        let segs: Vec<Segment> = (0..300).map(|i| seg(i, 1)).collect();
        let mut unsignaled_run = 0usize;
        for r in plan_posts(segs.len(), 128) {
            let chain = SendChain::new(&segs[r], 0);
            assert!(chain.len() <= 128);
            for wr in &chain.wrs {
                if wr.send_flags & ffi::IBV_SEND_SIGNALED != 0 {
                    unsignaled_run = 0;
                } else {
                    unsignaled_run += 1;
                    assert!(unsignaled_run < 128);
                }
            }
            assert_eq!(
                chain
                    .wrs
                    .iter()
                    .filter(|w| w.send_flags & ffi::IBV_SEND_SIGNALED != 0)
                    .count(),
                1
            );
        }
    }

    // A failed post reports the WR it stopped at; the ones before it
    // were posted and must be waited for.
    #[test]
    fn partial_post_counts_the_wrs_before_bad_wr() {
        let segs: Vec<Segment> = (0..4).map(|i| seg(i, 1)).collect();
        let chain = SendChain::new(&segs, 0);
        assert_eq!(chain.posted_before(ptr::null()), 0);
        for i in 0..4 {
            assert_eq!(chain.posted_before(&chain.wrs[i]), i);
        }
        // Not one of ours (or not on a WR boundary): assume all posted.
        let other = SendChain::new(&segs, 0);
        assert_eq!(chain.posted_before(&other.wrs[1]), 4);
        let inside =
            (&chain.wrs[1] as *const ffi::ibv_send_wr as usize + 8) as *const ffi::ibv_send_wr;
        assert_eq!(chain.posted_before(inside), 4);
        let e = ChainPostError {
            posted: 2,
            code: 12,
        };
        assert!(e.to_string().contains("after 2 work requests"));
    }

    /// Loopback chained WRITE then READ, in WRs of 512 bytes and posts
    /// of 3 WRs, so several posts and chains are exercised.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn loopback_chained_write_read() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let send_cq = CompletionQueue::new(&dev, 64).expect("cq");
        let recv_cq = CompletionQueue::new(&dev, 64).expect("cq");
        let qp = QueuePair::create_rc(&dev, &send_cq, &recv_cq, 64, 64).expect("qp");
        let size = 8192;
        let mut src: Vec<u8> = (0..size).map(|i| (i % 251) as u8).collect();
        let mut dst = vec![0u8; size];
        let mut back = vec![0u8; size];
        let src_mr = dev
            .register_memory(src.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .expect("mr");
        let dst_mr = dev
            .register_memory(dst.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .expect("mr");
        let back_mr = dev
            .register_memory(back.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .expect("mr");
        let gid_index = dev.roce_gid_index();
        let gid = dev.gid(i32::from(gid_index)).expect("gid");
        qp.to_init(dev.port, Access::REMOTE_READ_WRITE)
            .expect("init");
        qp.to_rtr(
            qp.qp_num,
            dev.lid().unwrap_or(0),
            &gid,
            dev.port,
            dev.active_mtu().expect("mtu"),
            gid_index,
        )
        .expect("rtr");
        qp.to_rts().expect("rts");

        let w = split_transfer(
            OneSided::Write,
            src.as_ptr() as u64,
            src_mr.lkey,
            dst.as_ptr() as u64,
            dst_mr.rkey,
            size as u64,
            512,
        )
        .expect("split");
        assert_eq!(w.len(), 16);
        run_chain(&qp, &send_cq, &w, 3).expect("chained write");
        assert_eq!(dst, src);
        let r = split_transfer(
            OneSided::Read,
            back.as_mut_ptr() as u64,
            back_mr.lkey,
            dst.as_ptr() as u64,
            dst_mr.rkey,
            size as u64,
            512,
        )
        .expect("split");
        run_chain(&qp, &send_cq, &r, 3).expect("chained read");
        assert_eq!(back, src);

        // A bad rkey in the middle of a chain: the error is reported and
        // every completion of the chain is drained, none left behind.
        let mut bad = w.clone();
        bad[5].rkey ^= 0xdead;
        assert!(run_chain(&qp, &send_cq, &bad, 16).is_err());
        let mut wc: [ffi::ibv_wc; 1] = unsafe { ffi::zeroed() };
        assert_eq!(
            send_cq.poll(&mut wc),
            0,
            "no completion of the failed chain left on the CQ"
        );
    }

    /// Loopback SEND/RECV test.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn loopback_send_recv() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let send_cq = CompletionQueue::new(&dev, 64).unwrap();
        let recv_cq = CompletionQueue::new(&dev, 64).unwrap();
        let qp = QueuePair::create_rc(&dev, &send_cq, &recv_cq, 64, 64).unwrap();

        let size = 256;
        let mut send_buf = vec![0x42u8; size];
        let mut recv_buf = vec![0u8; size];

        let send_mr = dev
            .register_memory(send_buf.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .unwrap();
        let recv_mr = dev
            .register_memory(recv_buf.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .unwrap();

        let gid_index = dev.roce_gid_index();
        let gid = dev.gid(gid_index as i32).unwrap();
        let lid = dev.lid().unwrap_or(0);

        qp.to_init(dev.port, Access::REMOTE_READ_WRITE).unwrap();
        let mtu = dev.active_mtu().unwrap();
        // sgid_index=0 is fine for loopback (link-local GID).
        qp.to_rtr(qp.qp_num, lid, &gid, dev.port, mtu, gid_index)
            .unwrap();
        qp.to_rts().unwrap();

        // Post RECV first (must be ready before SEND).
        qp.post_recv(recv_buf.as_ptr() as u64, recv_mr.lkey, size as u32)
            .unwrap();

        // Post SEND.
        qp.send(send_buf.as_ptr() as u64, send_mr.lkey, size as u32)
            .unwrap();

        // Poll send CQ.
        let wc = poll_completion(&send_cq, 1_000_000).unwrap();
        println!("SEND complete: wr_id={}", wc.wr_id);

        // Poll recv CQ.
        let wc = poll_completion(&recv_cq, 1_000_000).unwrap();
        println!("RECV complete: {} bytes, wr_id={}", wc.byte_len, wc.wr_id);

        assert_eq!(recv_buf[0], 0x42);
        assert!(
            recv_buf.iter().all(|&b| b == 0x42),
            "SEND/RECV data mismatch"
        );
        println!("SEND/RECV verified: all {} bytes = 0x42", size);
    }

    /// Large transfer: 1MB RDMA WRITE.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn loopback_large_write() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let send_cq = CompletionQueue::new(&dev, 64).unwrap();
        let recv_cq = CompletionQueue::new(&dev, 64).unwrap();
        let qp = QueuePair::create_rc(&dev, &send_cq, &recv_cq, 64, 64).unwrap();

        let size = 1024 * 1024; // 1 MB
        let mut src_buf = vec![0xCDu8; size];
        let mut dst_buf = vec![0u8; size];

        let src_mr = dev
            .register_memory(src_buf.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .unwrap();
        let dst_mr = dev
            .register_memory(dst_buf.as_mut_ptr(), size, Access::REMOTE_READ_WRITE)
            .unwrap();

        let gid_index = dev.roce_gid_index();
        let gid = dev.gid(gid_index as i32).unwrap();
        let lid = dev.lid().unwrap_or(0);

        qp.to_init(dev.port, Access::REMOTE_READ_WRITE).unwrap();
        let mtu = dev.active_mtu().unwrap();
        // sgid_index=0 is fine for loopback (link-local GID).
        qp.to_rtr(qp.qp_num, lid, &gid, dev.port, mtu, gid_index)
            .unwrap();
        qp.to_rts().unwrap();

        let start = std::time::Instant::now();

        qp.rdma_write(
            src_buf.as_ptr() as u64,
            src_mr.lkey,
            dst_buf.as_ptr() as u64,
            dst_mr.rkey,
            size as u32,
        )
        .unwrap();

        poll_completion(&send_cq, 10_000_000).unwrap();
        let elapsed = start.elapsed();

        assert!(
            dst_buf.iter().all(|&b| b == 0xCD),
            "1MB RDMA WRITE data mismatch"
        );

        let throughput_gbps = (size as f64 * 8.0) / elapsed.as_secs_f64() / 1e9;
        println!(
            "1MB RDMA WRITE: {:?} ({:.2} Gbps)",
            elapsed, throughput_gbps
        );
    }
}
