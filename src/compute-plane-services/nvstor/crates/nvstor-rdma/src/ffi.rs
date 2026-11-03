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

//! Minimal FFI bindings to libibverbs (rdma-core).
//!
//! Hand-written to avoid bindgen compatibility issues with rdma-core 50+.
//! Only includes the subset of the API we actually use:
//!   - Device enumeration and context management
//!   - Protection domains and memory registration
//!   - Completion queues
//!   - Queue pairs (RC type) with RDMA WRITE/READ/SEND
//!
//! Struct layouts match libibverbs ABI exactly.
//! Verified against /usr/include/infiniband/verbs.h (rdma-core 50.0).

#![allow(non_camel_case_types, non_upper_case_globals, dead_code)]

use std::os::raw::{c_char, c_int, c_uint, c_void};

// ---------------------------------------------------------------------------
// Opaque types — never constructed, only used behind pointers
// ---------------------------------------------------------------------------

/// Opaque ibverbs device handle.
#[repr(C)]
pub struct ibv_device {
    _opaque: [u8; 0],
}

// ---------------------------------------------------------------------------
// ibv_context_ops — function pointer table inside ibv_context
//
// Layout from verbs.h. Each field is a function pointer (8 bytes on x86_64).
// We must get the offsets exactly right for poll_cq, post_send, post_recv.
// ---------------------------------------------------------------------------

/// Function pointer types for the data path operations.
type PollCqFn = unsafe extern "C" fn(cq: *mut ibv_cq, num_entries: c_int, wc: *mut ibv_wc) -> c_int;

type ReqNotifyCqFn = unsafe extern "C" fn(cq: *mut ibv_cq, solicited_only: c_int) -> c_int;

type PostSendFn = unsafe extern "C" fn(
    qp: *mut ibv_qp,
    wr: *mut ibv_send_wr,
    bad_wr: *mut *mut ibv_send_wr,
) -> c_int;

type PostRecvFn = unsafe extern "C" fn(
    qp: *mut ibv_qp,
    wr: *mut ibv_recv_wr,
    bad_wr: *mut *mut ibv_recv_wr,
) -> c_int;

/// ibv_context_ops — matches the C struct field-for-field.
/// All function pointers are 8 bytes on x86_64.
#[repr(C)]
pub struct ibv_context_ops {
    pub _compat_query_device: *mut c_void,    // fn ptr
    pub _compat_query_port: *mut c_void,      // fn ptr
    pub _compat_alloc_pd: *mut c_void,        // fn ptr
    pub _compat_dealloc_pd: *mut c_void,      // fn ptr
    pub _compat_reg_mr: *mut c_void,          // fn ptr
    pub _compat_rereg_mr: *mut c_void,        // fn ptr
    pub _compat_dereg_mr: *mut c_void,        // fn ptr
    pub alloc_mw: *mut c_void,                // fn ptr
    pub bind_mw: *mut c_void,                 // fn ptr
    pub dealloc_mw: *mut c_void,              // fn ptr
    pub _compat_create_cq: *mut c_void,       // fn ptr
    pub poll_cq: Option<PollCqFn>,            // fn ptr — offset 11
    pub req_notify_cq: Option<ReqNotifyCqFn>, // fn ptr — offset 12
    pub _compat_cq_event: *mut c_void,        // fn ptr
    pub _compat_resize_cq: *mut c_void,       // fn ptr
    pub _compat_destroy_cq: *mut c_void,      // fn ptr
    pub _compat_create_srq: *mut c_void,      // fn ptr
    pub _compat_modify_srq: *mut c_void,      // fn ptr
    pub _compat_query_srq: *mut c_void,       // fn ptr
    pub _compat_destroy_srq: *mut c_void,     // fn ptr
    pub post_srq_recv: *mut c_void,           // fn ptr
    pub _compat_create_qp: *mut c_void,       // fn ptr
    pub _compat_query_qp: *mut c_void,        // fn ptr
    pub _compat_modify_qp: *mut c_void,       // fn ptr
    pub _compat_destroy_qp: *mut c_void,      // fn ptr
    pub post_send: Option<PostSendFn>,        // fn ptr — offset 25
    pub post_recv: Option<PostRecvFn>,        // fn ptr — offset 26
    pub _compat_create_ah: *mut c_void,       // fn ptr
    pub _compat_destroy_ah: *mut c_void,      // fn ptr
    pub _compat_attach_mcast: *mut c_void,    // fn ptr
    pub _compat_detach_mcast: *mut c_void,    // fn ptr
    pub _compat_async_event: *mut c_void,     // fn ptr
}

/// ibv_context — the main device context with ops table.
///
/// Layout from verbs.h:
///   struct ibv_device      *device;
///   struct ibv_context_ops  ops;      // inline struct, not pointer
///   int                     cmd_fd;
///   int                     async_fd;
///   int                     num_comp_vectors;
///   pthread_mutex_t         mutex;
///   void                   *abi_compat;
#[repr(C)]
pub struct ibv_context {
    pub device: *mut ibv_device,
    pub ops: ibv_context_ops,
    pub cmd_fd: c_int,
    pub async_fd: c_int,
    /// Completion vectors (interrupts) a CQ may signal on: `comp_vector`
    /// of `ibv_create_cq` must be below this.
    pub num_comp_vectors: c_int,
    // Remaining fields (mutex, abi_compat) not accessed; this struct is
    // only ever read through a pointer libibverbs allocated.
}

/// Opaque protection domain.
#[repr(C)]
pub struct ibv_pd {
    _opaque: [u8; 0],
}

/// Completion queue — we need context pointer for ops dispatch.
///
/// Layout from verbs.h:
///   struct ibv_context      *context;
///   ...remaining fields
#[repr(C)]
pub struct ibv_cq {
    pub context: *mut ibv_context,
    // Remaining fields (channel, cq_context, handle, cqe, etc.) not accessed.
}

/// Opaque shared receive queue.
#[repr(C)]
pub struct ibv_srq {
    _opaque: [u8; 0],
}

/// Completion channel. Layout from verbs.h:
///   struct ibv_context *context; int fd; int refcnt;
#[repr(C)]
pub struct ibv_comp_channel {
    pub context: *mut ibv_context,
    /// Readable when a CQ attached to the channel has an event.
    pub fd: c_int,
    pub refcnt: c_int,
}

/// Opaque address handle.
#[repr(C)]
pub struct ibv_ah {
    _opaque: [u8; 0],
}

// ---------------------------------------------------------------------------
// Queue pair — we need to read qp_num from it
// ---------------------------------------------------------------------------

/// Queue pair. We only access `qp_num` — the rest is opaque to us,
/// but we must define the leading fields so qp_num is at the right offset.
///
/// Layout from verbs.h:
///   struct ibv_context *context;    // 8 bytes (ptr)
///   void               *qp_context; // 8 bytes (ptr)
///   struct ibv_pd      *pd;         // 8 bytes (ptr)
///   struct ibv_cq      *send_cq;    // 8 bytes (ptr)
///   struct ibv_cq      *recv_cq;    // 8 bytes (ptr)
///   struct ibv_srq     *srq;        // 8 bytes (ptr)
///   uint32_t            handle;     // 4 bytes
///   uint32_t            qp_num;     // 4 bytes  ← offset 52
#[repr(C)]
pub struct ibv_qp {
    pub context: *mut ibv_context,
    pub qp_context: *mut c_void,
    pub pd: *mut ibv_pd,
    pub send_cq: *mut ibv_cq,
    pub recv_cq: *mut ibv_cq,
    pub srq: *mut ibv_srq,
    pub handle: u32,
    pub qp_num: u32,
    // Remaining fields (state, qp_type, mutex, cond, events_completed)
    // are not accessed — intentionally omitted.
}

// ---------------------------------------------------------------------------
// Memory region — we need lkey and rkey
// ---------------------------------------------------------------------------

/// Memory region.
///
/// Layout from verbs.h:
///   struct ibv_context *context;   // 8 bytes (ptr)
///   struct ibv_pd      *pd;        // 8 bytes (ptr)
///   void               *addr;      // 8 bytes (ptr)
///   size_t              length;    // 8 bytes
///   uint32_t            handle;    // 4 bytes
///   uint32_t            lkey;      // 4 bytes  ← offset 36
///   uint32_t            rkey;      // 4 bytes  ← offset 40
#[repr(C)]
pub struct ibv_mr {
    pub context: *mut ibv_context,
    pub pd: *mut ibv_pd,
    pub addr: *mut c_void,
    pub length: usize,
    pub handle: u32,
    pub lkey: u32,
    pub rkey: u32,
}

// ---------------------------------------------------------------------------
// GID — 128-bit Global Identifier
// ---------------------------------------------------------------------------

/// Global Identifier (GID) — 16-byte union.
#[repr(C)]
#[derive(Clone, Copy)]
pub union ibv_gid {
    pub raw: [u8; 16],
    pub global: ibv_gid_global,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct ibv_gid_global {
    pub subnet_prefix: u64,
    pub interface_id: u64,
}

impl Default for ibv_gid {
    fn default() -> Self {
        Self { raw: [0u8; 16] }
    }
}

impl std::fmt::Debug for ibv_gid {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let raw = unsafe { self.raw };
        write!(f, "GID({:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x})",
            raw[0], raw[1], raw[2], raw[3], raw[4], raw[5], raw[6], raw[7],
            raw[8], raw[9], raw[10], raw[11], raw[12], raw[13], raw[14], raw[15])
    }
}

// ---------------------------------------------------------------------------
// Global routing header
// ---------------------------------------------------------------------------

#[repr(C)]
#[derive(Clone, Copy)]
pub struct ibv_global_route {
    pub dgid: ibv_gid,
    pub flow_label: u32,
    pub sgid_index: u8,
    pub hop_limit: u8,
    pub traffic_class: u8,
}

// ---------------------------------------------------------------------------
// Address handle attributes
// ---------------------------------------------------------------------------

#[repr(C)]
#[derive(Clone, Copy)]
pub struct ibv_ah_attr {
    pub grh: ibv_global_route,
    pub dlid: u16,
    pub sl: u8,
    pub src_path_bits: u8,
    pub static_rate: u8,
    pub is_global: u8,
    pub port_num: u8,
}

// ---------------------------------------------------------------------------
// QP capabilities
// ---------------------------------------------------------------------------

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct ibv_qp_cap {
    pub max_send_wr: u32,
    pub max_recv_wr: u32,
    pub max_send_sge: u32,
    pub max_recv_sge: u32,
    pub max_inline_data: u32,
}

// ---------------------------------------------------------------------------
// QP init attributes (for ibv_create_qp)
// ---------------------------------------------------------------------------

#[repr(C)]
pub struct ibv_qp_init_attr {
    pub qp_context: *mut c_void,
    pub send_cq: *mut ibv_cq,
    pub recv_cq: *mut ibv_cq,
    pub srq: *mut ibv_srq,
    pub cap: ibv_qp_cap,
    pub qp_type: u32, // ibv_qp_type
    pub sq_sig_all: c_int,
}

// ---------------------------------------------------------------------------
// QP modify attributes (for ibv_modify_qp)
// ---------------------------------------------------------------------------

#[repr(C)]
pub struct ibv_qp_attr {
    pub qp_state: u32,       // ibv_qp_state
    pub cur_qp_state: u32,   // ibv_qp_state
    pub path_mtu: u32,       // ibv_mtu
    pub path_mig_state: u32, // ibv_mig_state
    pub qkey: u32,
    pub rq_psn: u32,
    pub sq_psn: u32,
    pub dest_qp_num: u32,
    pub qp_access_flags: c_uint,
    pub cap: ibv_qp_cap,
    pub ah_attr: ibv_ah_attr,
    pub alt_ah_attr: ibv_ah_attr,
    pub pkey_index: u16,
    pub alt_pkey_index: u16,
    pub en_sqd_async_notify: u8,
    pub sq_draining: u8,
    pub max_rd_atomic: u8,
    pub max_dest_rd_atomic: u8,
    pub min_rnr_timer: u8,
    pub port_num: u8,
    pub timeout: u8,
    pub retry_cnt: u8,
    pub rnr_retry: u8,
    pub alt_port_num: u8,
    pub alt_timeout: u8,
    pub _pad: u8,
    pub rate_limit: u32,
}

// ---------------------------------------------------------------------------
// Scatter-gather element
// ---------------------------------------------------------------------------

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct ibv_sge {
    pub addr: u64,
    pub length: u32,
    pub lkey: u32,
}

// ---------------------------------------------------------------------------
// Send work request
// ---------------------------------------------------------------------------

/// RDMA-specific fields in send WR union.
#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct ibv_send_wr_rdma {
    pub remote_addr: u64,
    pub rkey: u32,
}

/// Atomic-specific fields in send WR union.
#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct ibv_send_wr_atomic {
    pub remote_addr: u64,
    pub compare_add: u64,
    pub swap: u64,
    pub rkey: u32,
}

/// UD-specific fields in send WR union.
#[repr(C)]
#[derive(Clone, Copy)]
pub struct ibv_send_wr_ud {
    pub ah: *mut ibv_ah,
    pub remote_qpn: u32,
    pub remote_qkey: u32,
}

/// Union for operation-specific data in send WR.
#[repr(C)]
pub union ibv_send_wr_wr {
    pub rdma: ibv_send_wr_rdma,
    pub atomic: ibv_send_wr_atomic,
    pub ud: ibv_send_wr_ud,
}

/// Send work request.
#[repr(C)]
pub struct ibv_send_wr {
    pub wr_id: u64,
    pub next: *mut ibv_send_wr,
    pub sg_list: *mut ibv_sge,
    pub num_sge: c_int,
    pub opcode: u32,        // ibv_wr_opcode
    pub send_flags: c_uint, // ibv_send_flags
    /// Immediate data or invalidate rkey.
    pub imm_data_or_inv_rkey: u32,
    pub wr: ibv_send_wr_wr,
    /// `qp_type.xrc.remote_srqn`; unused on RC.
    pub qp_type_xrc_remote_srqn: u32,
    /// The `bind_mw` / `tso` union (48 bytes, pointer-aligned); unused
    /// here. Present so the struct has libibverbs' size and an array of
    /// linked WRs matches `struct ibv_send_wr` element for element.
    pub bind_mw_or_tso: [u64; 6],
}

// ---------------------------------------------------------------------------
// Receive work request
// ---------------------------------------------------------------------------

#[repr(C)]
pub struct ibv_recv_wr {
    pub wr_id: u64,
    pub next: *mut ibv_recv_wr,
    pub sg_list: *mut ibv_sge,
    pub num_sge: c_int,
}

// ---------------------------------------------------------------------------
// Work completion
// ---------------------------------------------------------------------------

#[repr(C)]
#[derive(Clone, Copy)]
pub struct ibv_wc {
    pub wr_id: u64,
    pub status: u32, // ibv_wc_status
    pub opcode: u32, // ibv_wc_opcode
    pub vendor_err: u32,
    pub byte_len: u32,
    pub imm_data_or_inv_rkey: u32,
    pub qp_num: u32,
    pub src_qp: u32,
    pub wc_flags: c_uint,
    pub pkey_index: u16,
    pub slid: u16,
    pub sl: u8,
    pub dlid_path_bits: u8,
}

// ---------------------------------------------------------------------------
// Device attributes
// ---------------------------------------------------------------------------

#[repr(C)]
pub struct ibv_device_attr {
    pub fw_ver: [c_char; 64],
    pub node_guid: u64,
    pub sys_image_guid: u64,
    pub max_mr_size: u64,
    pub page_size_cap: u64,
    pub vendor_id: u32,
    pub vendor_part_id: u32,
    pub hw_ver: u32,
    pub max_qp: c_int,
    pub max_qp_wr: c_int,
    pub device_cap_flags: c_uint,
    pub max_sge: c_int,
    pub max_sge_rd: c_int,
    pub max_cq: c_int,
    pub max_cqe: c_int,
    pub max_mr: c_int,
    pub max_pd: c_int,
    pub max_qp_rd_atom: c_int,
    pub max_ee_rd_atom: c_int,
    pub max_res_rd_atom: c_int,
    pub max_qp_init_rd_atom: c_int,
    pub max_ee_init_rd_atom: c_int,
    pub atomic_cap: c_int, // ibv_atomic_cap enum
    pub max_ee: c_int,
    pub max_rdd: c_int,
    pub max_mw: c_int,
    pub max_raw_ipv6_qp: c_int,
    pub max_raw_ethy_qp: c_int,
    pub max_mcast_grp: c_int,
    pub max_mcast_qp_attach: c_int,
    pub max_total_mcast_qp_attach: c_int,
    pub max_ah: c_int,
    pub max_fmr: c_int,
    pub max_map_per_fmr: c_int,
    pub max_srq: c_int,
    pub max_srq_wr: c_int,
    pub max_srq_sge: c_int,
    pub max_pkeys: u16,
    pub local_ca_ack_delay: u8,
    pub phys_port_cnt: u8,
}

// ---------------------------------------------------------------------------
// Port attributes
// ---------------------------------------------------------------------------

#[repr(C)]
pub struct ibv_port_attr {
    pub state: u32,      // ibv_port_state
    pub max_mtu: u32,    // ibv_mtu
    pub active_mtu: u32, // ibv_mtu
    pub gid_tbl_len: c_int,
    pub port_cap_flags: u32,
    pub max_msg_sz: u32,
    pub bad_pkey_cntr: u32,
    pub qkey_viol_cntr: u32,
    pub pkey_tbl_len: u16,
    pub lid: u16,
    pub sm_lid: u16,
    pub lmc: u8,
    pub max_vl_num: u8,
    pub sm_sl: u8,
    pub subnet_timeout: u8,
    pub init_type_reply: u8,
    pub active_width: u8,
    pub active_speed: u8,
    pub phys_state: u8,
    pub link_layer: u8,
    pub flags: u8,
    pub port_cap_flags2: u16,
    pub active_speed_ex: u32,
}

// ---------------------------------------------------------------------------
// Enums / constants
// ---------------------------------------------------------------------------

// ibv_qp_type
pub const IBV_QPT_RC: u32 = 2;
pub const IBV_QPT_UC: u32 = 3;
pub const IBV_QPT_UD: u32 = 4;

// ibv_qp_state
pub const IBV_QPS_RESET: u32 = 0;
pub const IBV_QPS_INIT: u32 = 1;
pub const IBV_QPS_RTR: u32 = 2;
pub const IBV_QPS_RTS: u32 = 3;
pub const IBV_QPS_SQD: u32 = 4;
pub const IBV_QPS_SQE: u32 = 5;
pub const IBV_QPS_ERR: u32 = 6;

// ibv_mtu
pub const IBV_MTU_256: u32 = 1;
pub const IBV_MTU_512: u32 = 2;
pub const IBV_MTU_1024: u32 = 3;
pub const IBV_MTU_2048: u32 = 4;
pub const IBV_MTU_4096: u32 = 5;

// ibv_wr_opcode
pub const IBV_WR_RDMA_WRITE: u32 = 0;
pub const IBV_WR_RDMA_WRITE_WITH_IMM: u32 = 1;
pub const IBV_WR_SEND: u32 = 2;
pub const IBV_WR_SEND_WITH_IMM: u32 = 3;
pub const IBV_WR_RDMA_READ: u32 = 4;

// ibv_wc_status
pub const IBV_WC_SUCCESS: u32 = 0;
pub const IBV_WC_LOC_LEN_ERR: u32 = 1;
pub const IBV_WC_LOC_QP_OP_ERR: u32 = 2;
pub const IBV_WC_LOC_PROT_ERR: u32 = 4;
pub const IBV_WC_WR_FLUSH_ERR: u32 = 5;
pub const IBV_WC_BAD_RESP_ERR: u32 = 7;
pub const IBV_WC_LOC_ACCESS_ERR: u32 = 8;
pub const IBV_WC_REM_INV_REQ_ERR: u32 = 9;
pub const IBV_WC_REM_ACCESS_ERR: u32 = 10;
pub const IBV_WC_REM_OP_ERR: u32 = 11;
pub const IBV_WC_RETRY_EXC_ERR: u32 = 12;
pub const IBV_WC_RNR_RETRY_EXC_ERR: u32 = 13;
pub const IBV_WC_FATAL_ERR: u32 = 19;
pub const IBV_WC_GENERAL_ERR: u32 = 21;

// ibv_send_flags
pub const IBV_SEND_FENCE: c_uint = 1 << 0;
pub const IBV_SEND_SIGNALED: c_uint = 1 << 1;
pub const IBV_SEND_SOLICITED: c_uint = 1 << 2;
pub const IBV_SEND_INLINE: c_uint = 1 << 3;

// ibv_access_flags
pub const IBV_ACCESS_LOCAL_WRITE: c_int = 1;
pub const IBV_ACCESS_REMOTE_WRITE: c_int = 1 << 1;
pub const IBV_ACCESS_REMOTE_READ: c_int = 1 << 2;
pub const IBV_ACCESS_REMOTE_ATOMIC: c_int = 1 << 3;

// ibv_qp_attr_mask
pub const IBV_QP_STATE: c_int = 1 << 0;
pub const IBV_QP_CUR_STATE: c_int = 1 << 1;
pub const IBV_QP_ACCESS_FLAGS: c_int = 1 << 3;
pub const IBV_QP_PKEY_INDEX: c_int = 1 << 4;
pub const IBV_QP_PORT: c_int = 1 << 5;
pub const IBV_QP_AV: c_int = 1 << 7;
pub const IBV_QP_PATH_MTU: c_int = 1 << 8;
pub const IBV_QP_TIMEOUT: c_int = 1 << 9;
pub const IBV_QP_RETRY_CNT: c_int = 1 << 10;
pub const IBV_QP_RNR_RETRY: c_int = 1 << 11;
pub const IBV_QP_RQ_PSN: c_int = 1 << 12;
pub const IBV_QP_MAX_QP_RD_ATOMIC: c_int = 1 << 13;
pub const IBV_QP_MIN_RNR_TIMER: c_int = 1 << 15;
pub const IBV_QP_SQ_PSN: c_int = 1 << 16;
pub const IBV_QP_MAX_DEST_RD_ATOMIC: c_int = 1 << 17;
pub const IBV_QP_DEST_QPN: c_int = 1 << 20;

// ibv_port_state
pub const IBV_PORT_DOWN: u32 = 1;
pub const IBV_PORT_INIT: u32 = 2;
pub const IBV_PORT_ARMED: u32 = 3;
pub const IBV_PORT_ACTIVE: u32 = 4;

// ---------------------------------------------------------------------------
// Helper: human-readable WC status
// ---------------------------------------------------------------------------

pub fn wc_status_str(status: u32) -> &'static str {
    match status {
        IBV_WC_SUCCESS => "SUCCESS",
        IBV_WC_LOC_LEN_ERR => "LOC_LEN_ERR",
        IBV_WC_LOC_QP_OP_ERR => "LOC_QP_OP_ERR",
        IBV_WC_LOC_PROT_ERR => "LOC_PROT_ERR",
        IBV_WC_WR_FLUSH_ERR => "WR_FLUSH_ERR",
        IBV_WC_BAD_RESP_ERR => "BAD_RESP_ERR",
        IBV_WC_LOC_ACCESS_ERR => "LOC_ACCESS_ERR",
        IBV_WC_REM_INV_REQ_ERR => "REM_INV_REQ_ERR",
        IBV_WC_REM_ACCESS_ERR => "REM_ACCESS_ERR",
        IBV_WC_REM_OP_ERR => "REM_OP_ERR",
        IBV_WC_RETRY_EXC_ERR => "RETRY_EXC_ERR",
        IBV_WC_RNR_RETRY_EXC_ERR => "RNR_RETRY_EXC_ERR",
        IBV_WC_FATAL_ERR => "FATAL_ERR",
        IBV_WC_GENERAL_ERR => "GENERAL_ERR",
        _ => "UNKNOWN",
    }
}

// ---------------------------------------------------------------------------
// Extern functions — linked against libibverbs.so
// ---------------------------------------------------------------------------

extern "C" {
    // Device enumeration
    pub fn ibv_get_device_list(num_devices: *mut c_int) -> *mut *mut ibv_device;
    pub fn ibv_free_device_list(list: *mut *mut ibv_device);
    pub fn ibv_get_device_name(device: *mut ibv_device) -> *const c_char;

    // Context lifecycle
    pub fn ibv_open_device(device: *mut ibv_device) -> *mut ibv_context;
    pub fn ibv_close_device(context: *mut ibv_context) -> c_int;

    // Device/port queries
    pub fn ibv_query_device(context: *mut ibv_context, device_attr: *mut ibv_device_attr) -> c_int;
    pub fn ibv_query_port(
        context: *mut ibv_context,
        port_num: u8,
        port_attr: *mut ibv_port_attr,
    ) -> c_int;
    pub fn ibv_query_gid(
        context: *mut ibv_context,
        port_num: u8,
        index: c_int,
        gid: *mut ibv_gid,
    ) -> c_int;

    // Protection domain
    pub fn ibv_alloc_pd(context: *mut ibv_context) -> *mut ibv_pd;
    pub fn ibv_dealloc_pd(pd: *mut ibv_pd) -> c_int;

    // Memory registration
    pub fn ibv_reg_mr(
        pd: *mut ibv_pd,
        addr: *mut c_void,
        length: usize,
        access: c_int,
    ) -> *mut ibv_mr;
    pub fn ibv_dereg_mr(mr: *mut ibv_mr) -> c_int;

    // Completion queues
    pub fn ibv_create_cq(
        context: *mut ibv_context,
        cqe: c_int,
        cq_context: *mut c_void,
        channel: *mut ibv_comp_channel,
        comp_vector: c_int,
    ) -> *mut ibv_cq;
    pub fn ibv_destroy_cq(cq: *mut ibv_cq) -> c_int;

    // Completion channels: CQ events instead of polling.
    pub fn ibv_create_comp_channel(context: *mut ibv_context) -> *mut ibv_comp_channel;
    pub fn ibv_destroy_comp_channel(channel: *mut ibv_comp_channel) -> c_int;
    /// Blocks until an event arrives unless the channel fd is non-blocking.
    pub fn ibv_get_cq_event(
        channel: *mut ibv_comp_channel,
        cq: *mut *mut ibv_cq,
        cq_context: *mut *mut c_void,
    ) -> c_int;
    /// Every event from `ibv_get_cq_event` must be acknowledged before the
    /// CQ is destroyed.
    pub fn ibv_ack_cq_events(cq: *mut ibv_cq, nevents: c_uint);

    // Queue pairs
    pub fn ibv_create_qp(pd: *mut ibv_pd, qp_init_attr: *mut ibv_qp_init_attr) -> *mut ibv_qp;
    pub fn ibv_modify_qp(qp: *mut ibv_qp, attr: *mut ibv_qp_attr, attr_mask: c_int) -> c_int;
    pub fn ibv_destroy_qp(qp: *mut ibv_qp) -> c_int;

}

// ---------------------------------------------------------------------------
// Data path — these are inline functions in C that dispatch through
// the context's ops table. We replicate that dispatch here.
// ---------------------------------------------------------------------------

/// Poll a completion queue. Dispatches through cq->context->ops.poll_cq.
///
/// # Safety
/// `cq` must be a valid CQ pointer from ibv_create_cq.
#[inline]
pub unsafe fn ibv_poll_cq(cq: *mut ibv_cq, num_entries: c_int, wc: *mut ibv_wc) -> c_int {
    let ctx = (*cq).context;
    match (*ctx).ops.poll_cq {
        Some(f) => f(cq, num_entries, wc),
        None => -1,
    }
}

/// Ask for an event on the CQ's channel at the next completion
/// (`solicited_only` 0: any completion). Dispatches through
/// cq->context->ops.req_notify_cq.
///
/// # Safety
/// `cq` must be a valid CQ pointer from ibv_create_cq.
#[inline]
pub unsafe fn ibv_req_notify_cq(cq: *mut ibv_cq, solicited_only: c_int) -> c_int {
    let ctx = (*cq).context;
    match (*ctx).ops.req_notify_cq {
        Some(f) => f(cq, solicited_only),
        None => -1,
    }
}

/// Post a send work request. Dispatches through qp->context->ops.post_send.
///
/// # Safety
/// `qp` must be a valid QP pointer from ibv_create_qp.
#[inline]
pub unsafe fn ibv_post_send(
    qp: *mut ibv_qp,
    wr: *mut ibv_send_wr,
    bad_wr: *mut *mut ibv_send_wr,
) -> c_int {
    let ctx = (*qp).context;
    match (*ctx).ops.post_send {
        Some(f) => f(qp, wr, bad_wr),
        None => -1,
    }
}

/// Post a receive work request. Dispatches through qp->context->ops.post_recv.
///
/// # Safety
/// `qp` must be a valid QP pointer from ibv_create_qp.
#[inline]
pub unsafe fn ibv_post_recv(
    qp: *mut ibv_qp,
    wr: *mut ibv_recv_wr,
    bad_wr: *mut *mut ibv_recv_wr,
) -> c_int {
    let ctx = (*qp).context;
    match (*ctx).ops.post_recv {
        Some(f) => f(qp, wr, bad_wr),
        None => -1,
    }
}

// ---------------------------------------------------------------------------
// Safety helpers
// ---------------------------------------------------------------------------

/// Zero-initialize any struct. Used for ibv_qp_attr, ibv_qp_init_attr, etc.
/// where the C API expects zero-filled structs with specific fields set.
///
/// # Safety
/// Only safe for C-repr structs where all-zeros is a valid representation.
pub unsafe fn zeroed<T>() -> T {
    std::mem::zeroed()
}

/// `ibv_reg_dmabuf_mr` (rdma-core 34 and later, symbol version
/// IBVERBS_1.12): register memory exported as a dma-buf (GPU memory, from
/// `cuMemGetHandleForAddressRange`). `iova` is the address peers name in
/// RDMA requests against the region. Resolved at run time, not linked, so
/// a binary built here still starts against an older rdma-core (where only
/// dma-buf registration is unavailable). `None` when libibverbs lacks it.
pub type IbvRegDmabufMr = unsafe extern "C" fn(
    pd: *mut ibv_pd,
    offset: u64,
    length: usize,
    iova: u64,
    fd: c_int,
    access: c_int,
) -> *mut ibv_mr;

pub fn ibv_reg_dmabuf_mr() -> Option<IbvRegDmabufMr> {
    static SYM: std::sync::OnceLock<Option<IbvRegDmabufMr>> = std::sync::OnceLock::new();
    *SYM.get_or_init(|| {
        // SAFETY: RTLD_DEFAULT searches the objects already loaded, which
        // include the linked libibverbs; the name is NUL-terminated.
        let p = unsafe { libc::dlsym(libc::RTLD_DEFAULT, c"ibv_reg_dmabuf_mr".as_ptr()) };
        // SAFETY: when present, the symbol is libibverbs' function with
        // exactly this signature (verbs.h).
        (!p.is_null())
            .then(|| unsafe { std::mem::transmute::<*mut libc::c_void, IbvRegDmabufMr>(p) })
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    // `ibv_context` from verbs.h: device pointer, 32 ops pointers, then
    // cmd_fd, async_fd, num_comp_vectors. The completion engine reads
    // num_comp_vectors to spread its CQs over interrupt vectors.
    #[test]
    fn ibv_context_layout_matches_verbs_h() {
        let p = std::mem::size_of::<*mut c_void>();
        assert_eq!(std::mem::size_of::<ibv_context_ops>(), 32 * p);
        assert_eq!(std::mem::offset_of!(ibv_context, ops), p);
        assert_eq!(std::mem::offset_of!(ibv_context, cmd_fd), 33 * p);
        assert_eq!(std::mem::offset_of!(ibv_context, async_fd), 33 * p + 4);
        assert_eq!(
            std::mem::offset_of!(ibv_context, num_comp_vectors),
            33 * p + 8
        );
    }
}
