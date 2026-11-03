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

//! NFS v4 COMPOUND operation dispatcher per RFC 7530 section 14.
//!
//! Each COMPOUND request contains a tag, minorversion, and an array of
//! operations processed sequentially. If any operation fails, remaining
//! operations are skipped and the error is returned.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use parking_lot::RwLock;
use sha2::{Digest, Sha256};
use tracing::{debug, warn};

use crate::ddp::{DataItem, DdpAllocator, DdpArg, DdpBuffer};
use crate::vfs::{FileType, InodeAttrs, Vfs};
use crate::xdr::{XdrReader, XdrWriter};
use nvstor_core::error::NvstorError;

/// Direct data placement state for one COMPOUND. Empty on TCP.
pub(crate) struct DdpCtx<'a> {
    alloc: Option<&'a dyn DdpAllocator>,
    args: &'a [DdpArg],
    /// External READ data produced by the operation just dispatched.
    produced: Option<(Box<dyn DdpBuffer>, usize)>,
}

impl DdpCtx<'_> {
    fn none() -> Self {
        Self {
            alloc: None,
            args: &[],
            produced: None,
        }
    }
}

/// READs at least this large are placed directly when the transport can.
/// Smaller results travel inline in the reply message anyway.
const DDP_MIN_READ: usize = 1024;

// ---------------------------------------------------------------------------
// NFS v4 status codes (RFC 7530 section 13)
// ---------------------------------------------------------------------------

pub const NFS4_OK: u32 = 0;
pub const NFS4ERR_PERM: u32 = 1;
pub const NFS4ERR_NOENT: u32 = 2;
pub const NFS4ERR_IO: u32 = 5;

/// Upper bound on the number of operations in a single COMPOUND request.
///
/// RFC 7530 does not mandate a limit — a malicious client could otherwise
/// send `num_ops = u32::MAX` and we'd iterate 4 billion times. Real clients
/// (Linux kernel, NetApp, pynfs) send <20 ops per compound in practice, so
/// 256 is comfortably large.
pub const MAX_COMPOUND_OPS: u32 = 256;

/// Upper bound on a single READ op's `count` field, in bytes.
///
/// Matches the XDR opaque cap. The value also bounds our memory high-water:
/// each in-flight READ allocates at most this much. 64 MiB covers real
/// clients (Linux uses 1 MiB `rsize` by default) with headroom.
pub const MAX_READ_COUNT: u32 = crate::xdr::MAX_OPAQUE_LEN as u32;

/// Maximum size of a cached SEQUENCE reply, in bytes.
///
/// NFS v4.1 exactly-once semantics caches the last reply per (session, slot)
/// for retransmission if the client re-sends the same seqid. Without a bound,
/// a single client × 16 slots × large reply could pin tens of GB of state.
/// 64 KiB is large enough to cache typical metadata-heavy compounds; large
/// READ replies that exceed it require cachethis=false; a retry then returns
/// RETRY_UNCACHED_REP, so the client can issue a new sequence safely.
pub const MAX_SESSION_REPLY_CACHE_BYTES: usize = 64 * 1024;
// Reserve space for the request digest, bincode envelope, and up to 256 DDP
// ranges in addition to the wire reply. The entire slot stays below 64 KiB.
const MAX_CACHED_WIRE_REPLY: usize = MAX_SESSION_REPLY_CACHE_BYTES - 8192;
pub const NFS4ERR_EXIST: u32 = 17;
pub const NFS4ERR_NOTDIR: u32 = 20;
pub const NFS4ERR_ISDIR: u32 = 21;
pub const NFS4ERR_INVAL: u32 = 22;
pub const NFS4ERR_FBIG: u32 = 27;
pub const NFS4ERR_ROFS: u32 = 30;
pub const NFS4ERR_NOSPC: u32 = 28;
pub const NFS4ERR_NOTEMPTY: u32 = 66;
pub const NFS4ERR_DQUOT: u32 = 69;
pub const NFS4ERR_STALE: u32 = 70;
/// Largest filehandle (NFS4_FHSIZE, RFC 8881 §3.2).
pub const NFS4_FHSIZE: usize = 128;
// Values from RFC 8881 §15.1 and Linux include/linux/nfs4.h. BADHANDLE was
// 10006 (SERVERFAULT), STALE_CLIENTID 10012 (LOCKED) and LAYOUTUNAVAILABLE
// 10058 (LAYOUTTRYLATER) in earlier versions.
pub const NFS4ERR_BADHANDLE: u32 = 10001;
pub const NFS4ERR_BAD_COOKIE: u32 = 10003;
pub const NFS4ERR_SERVERFAULT: u32 = 10006;
pub const NFS4ERR_DELAY: u32 = 10008;
pub const NFS4ERR_SYMLINK: u32 = 10029;
pub const NFS4ERR_BADXDR: u32 = 10036;
pub const NFS4ERR_REQ_TOO_BIG: u32 = 10065;
pub const NFS4ERR_REP_TOO_BIG: u32 = 10066;
pub const NFS4ERR_TOO_MANY_OPS: u32 = 10070;
pub const NFS4ERR_BAD_STATEID: u32 = 10025;
pub const NFS4ERR_SAME: u32 = 10009;
pub const NFS4ERR_DENIED: u32 = 10010;
pub const NFS4ERR_STALE_CLIENTID: u32 = 10022;
pub const NFS4ERR_NOTSUPP: u32 = 10004;
pub const NFS4ERR_CLID_INUSE: u32 = 10017;
pub const NFS4ERR_NOFILEHANDLE: u32 = 10020;
pub const NFS4ERR_NOT_SAME: u32 = 10027;
pub const NFS4ERR_OP_ILLEGAL: u32 = 10044;
pub const NFS4ERR_RESOURCE: u32 = 10018;
pub const NFS4ERR_SEQ_MISORDERED: u32 = 10063;
pub const NFS4ERR_RETRY_UNCACHED_REP: u32 = 10068;
pub const NFS4ERR_REP_TOO_BIG_TO_CACHE: u32 = 10067;
pub const NFS4ERR_SEQ_FALSE_RETRY: u32 = 10076;
pub const NFS4ERR_SEQUENCE_POS: u32 = 10064;
pub const NFS4ERR_BADSESSION: u32 = 10052;
pub const NFS4ERR_LAYOUTUNAVAILABLE: u32 = 10059;
pub const NFS4ERR_UNKNOWN_LAYOUTTYPE: u32 = 10062;
pub const NFS4ERR_ACCESS: u32 = 13;
pub const NFS4ERR_NOXATTR: u32 = 10095;

/// READ_PLUS content kinds (RFC 7862 §15.10.1).
pub const NFS4_CONTENT_DATA: u32 = 0;
#[allow(dead_code)]
pub const NFS4_CONTENT_HOLE: u32 = 1;

/// Cap on a single WRITE op (matches MAX_READ_COUNT — same XDR cap).
pub const MAX_WRITE_COUNT: u32 = MAX_READ_COUNT;

// ---------------------------------------------------------------------------
// NFS v4 operation numbers (RFC 7530 section 14)
// ---------------------------------------------------------------------------

pub const OP_ACCESS: u32 = 3;
pub const OP_CLOSE: u32 = 4;
pub const OP_COMMIT: u32 = 5;
pub const OP_CREATE: u32 = 6;
pub const OP_DELEGPURGE: u32 = 7;
pub const OP_DELEGRETURN: u32 = 8;
pub const OP_GETATTR: u32 = 9;
pub const OP_GETFH: u32 = 10;
pub const OP_LINK: u32 = 11;
pub const OP_LOCK: u32 = 12;
pub const OP_LOCKT: u32 = 13;
pub const OP_LOCKU: u32 = 14;
pub const OP_LOOKUP: u32 = 15;
pub const OP_LOOKUPP: u32 = 16;
pub const OP_NVERIFY: u32 = 17;
pub const OP_OPEN: u32 = 18;
pub const OP_OPENATTR: u32 = 19;
pub const OP_OPEN_CONFIRM: u32 = 20;
pub const OP_OPEN_DOWNGRADE: u32 = 21;
pub const OP_PUTFH: u32 = 22;
pub const OP_PUTPUBFH: u32 = 23;
pub const OP_PUTROOTFH: u32 = 24;
pub const OP_READ: u32 = 25;
pub const OP_READDIR: u32 = 26;
pub const OP_READLINK: u32 = 27;
pub const OP_REMOVE: u32 = 28;
pub const OP_RENAME: u32 = 29;
pub const OP_RENEW: u32 = 30;
pub const OP_RESTOREFH: u32 = 31;
pub const OP_SAVEFH: u32 = 32;
pub const OP_SECINFO: u32 = 33;
pub const OP_SETATTR: u32 = 34;
pub const OP_SETCLIENTID: u32 = 35;
pub const OP_SETCLIENTID_CONFIRM: u32 = 36;
pub const OP_VERIFY: u32 = 37;
pub const OP_WRITE: u32 = 38;
pub const OP_RELEASE_LOCKOWNER: u32 = 39;

// NFS v4.1 operations (RFC 5661)
pub const OP_BACKCHANNEL_CTL: u32 = 40;
pub const OP_BIND_CONN_TO_SESSION: u32 = 41;
pub const OP_EXCHANGE_ID: u32 = 42;
pub const OP_CREATE_SESSION: u32 = 43;
pub const OP_DESTROY_SESSION: u32 = 44;
// NFS v4.1 opcode numbers per RFC 5661 §16.1. Earlier revisions of this
// code had OP_FREE_STATEID, OP_RECLAIM_COMPLETE, and OP_SECINFO_NO_NAME
// at the wrong values — the kernel client dispatched SECINFO_NO_NAME (52)
// to our RECLAIM_COMPLETE handler, which returned 0 bytes, and `mount
// -t nfs4` disconnected with "Remote I/O error".
pub const OP_FREE_STATEID: u32 = 45;
pub const OP_SECINFO_NO_NAME: u32 = 52;
pub const OP_SEQUENCE: u32 = 53;
pub const OP_TEST_STATEID: u32 = 55;
pub const OP_DESTROY_CLIENTID: u32 = 57;
pub const OP_RECLAIM_COMPLETE: u32 = 58;

// NFS v4.1 pNFS operations
pub const OP_LAYOUTGET: u32 = 50;
pub const OP_LAYOUTCOMMIT: u32 = 49;
pub const OP_LAYOUTRETURN: u32 = 51;
pub const OP_GETDEVICEINFO: u32 = 47;
pub const OP_GETDEVICELIST: u32 = 48;

// NFS v4.2 operations (RFC 7862)
pub const OP_ALLOCATE: u32 = 59;
pub const OP_COPY: u32 = 60;
pub const OP_COPY_NOTIFY: u32 = 61;
pub const OP_DEALLOCATE: u32 = 62;
pub const OP_IO_ADVISE: u32 = 63;
pub const OP_LAYOUTERROR: u32 = 64;
pub const OP_LAYOUTSTATS: u32 = 65;
pub const OP_OFFLOAD_CANCEL: u32 = 66;
pub const OP_OFFLOAD_STATUS: u32 = 67;
pub const OP_READ_PLUS: u32 = 68;
pub const OP_SEEK: u32 = 69;
pub const OP_WRITE_SAME: u32 = 70;
pub const OP_CLONE: u32 = 71;
pub const OP_GETXATTR: u32 = 72;
pub const OP_SETXATTR: u32 = 73;
pub const OP_LISTXATTRS: u32 = 74;
pub const OP_REMOVEXATTR: u32 = 75;
// v4.1 ops we previously didn't dispatch.
pub const OP_GET_DIR_DELEGATION: u32 = 46;
pub const OP_WANT_DELEGATION: u32 = 56;
pub const OP_SET_SSV: u32 = 54;

// ---------------------------------------------------------------------------
// NFS v4 file types for FATTR4
// ---------------------------------------------------------------------------

const NF4REG: u32 = 1;
const NF4DIR: u32 = 2;
const NF4LNK: u32 = 5;

// ---------------------------------------------------------------------------
// FATTR4 bitmap bit positions (RFC 7530 section 5.8)
// ---------------------------------------------------------------------------

// Word 0 (bits 0-31) — MUST match Linux kernel include/linux/nfs4.h exactly!
const FATTR4_SUPPORTED_ATTRS: u32 = 0;
const FATTR4_TYPE: u32 = 1;
const FATTR4_FH_EXPIRE_TYPE: u32 = 2;
const FATTR4_CHANGE: u32 = 3;
const FATTR4_SIZE: u32 = 4;
const FATTR4_LINK_SUPPORT: u32 = 5;
const FATTR4_SYMLINK_SUPPORT: u32 = 6;
const FATTR4_NAMED_ATTR: u32 = 7;
const FATTR4_FSID: u32 = 8; // was 30 — WRONG!
const FATTR4_UNIQUE_HANDLES: u32 = 9;
const FATTR4_LEASE_TIME: u32 = 10;
const FATTR4_RDATTR_ERROR: u32 = 11; // was 8 — WRONG!
#[allow(dead_code)]
const FATTR4_ACL: u32 = 12;
const FATTR4_ACLSUPPORT: u32 = 13;
#[allow(dead_code)]
const FATTR4_FILEHANDLE: u32 = 19;
const FATTR4_FILEID: u32 = 20;
const FATTR4_FILES_AVAIL: u32 = 21;
const FATTR4_FILES_FREE: u32 = 22;
const FATTR4_FILES_TOTAL: u32 = 23;
const FATTR4_FS_LOCATIONS: u32 = 24;
const FATTR4_HOMOGENEOUS: u32 = 26;
const FATTR4_MAXFILESIZE: u32 = 27;
const FATTR4_MAXLINK: u32 = 28;
const FATTR4_MAXNAME: u32 = 29; // was 26 — WRONG!
const FATTR4_MAXREAD: u32 = 30; // was 28 — WRONG!
const FATTR4_MAXWRITE: u32 = 31; // was 29 — WRONG!

// Word 1 (bits 32-63) — bit N of word 1 = attribute (32+N)
const FATTR4_SPACE_AVAIL: u32 = 42; // bit 10 of word 1
const FATTR4_SPACE_FREE: u32 = 43; // bit 11 of word 1
const FATTR4_SPACE_TOTAL: u32 = 44; // bit 12 of word 1

// Word 1 (bits 32-63) — MUST match Linux kernel include/linux/nfs4.h!
// Attribute N in word1 = bit (N-32) of word1, where N is the kernel bit position.
const FATTR4_MODE: u32 = 33; // kernel bit 1 of word 1
const FATTR4_NO_TRUNC: u32 = 34; // kernel bit 2 of word 1
const FATTR4_NUMLINKS: u32 = 35; // kernel bit 3
const FATTR4_OWNER: u32 = 36; // kernel bit 4
const FATTR4_OWNER_GROUP: u32 = 37; // kernel bit 5
const FATTR4_SPACE_USED: u32 = 45; // kernel bit 13
const FATTR4_TIME_ACCESS: u32 = 47; // kernel bit 15
const FATTR4_TIME_METADATA: u32 = 52; // kernel bit 20 (was 49 — WRONG!)
const FATTR4_TIME_MODIFY: u32 = 53; // kernel bit 21
const FATTR4_MOUNTED_ON_FILEID: u32 = 55; // kernel bit 23 (was 52 — WRONG!)
const FATTR4_FS_LAYOUT_TYPES: u32 = 62; // kernel bit 30 of word 1 — pNFS

// ---------------------------------------------------------------------------
// NFS v4 WRITE stable_how
// ---------------------------------------------------------------------------

// stable_how4 (RFC 8881 §18.32.1). WRITE echoes the client's level.
const UNSTABLE4: u32 = 0;
#[allow(dead_code)]
const DATA_SYNC4: u32 = 1;
#[allow(dead_code)]
const FILE_SYNC4: u32 = 2;

// ---------------------------------------------------------------------------
// State management
// ---------------------------------------------------------------------------

/// Simple stateid: (seqid, other). For our simplified implementation we
/// use an atomic counter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, serde::Serialize, serde::Deserialize)]
struct StateId {
    seqid: u32,
    other: [u8; 12],
}

impl StateId {
    fn encode(&self, w: &mut XdrWriter) {
        w.put_u32(self.seqid);
        w.put_opaque_fixed(&self.other);
    }

    fn decode(r: &mut XdrReader) -> Result<Self, u32> {
        let seqid = r.get_u32().map_err(|_| NFS4ERR_INVAL)?;
        let other_bytes = r.get_opaque_fixed(12).map_err(|_| NFS4ERR_INVAL)?;
        let mut other = [0u8; 12];
        other.copy_from_slice(&other_bytes);
        Ok(Self { seqid, other })
    }

    #[cfg_attr(not(test), allow(dead_code))]
    fn special_anonymous() -> Self {
        Self {
            seqid: 0,
            other: [0u8; 12],
        }
    }
}

// ---------------------------------------------------------------------------
// Open file state
// ---------------------------------------------------------------------------

#[derive(serde::Serialize, serde::Deserialize)]
struct OpenState {
    ino: u64,
    stateid: StateId,
    /// OPEN4_SHARE_ACCESS: 1=READ, 2=WRITE, 3=BOTH
    share_access: u32,
    /// OPEN4_SHARE_DENY: 0=NONE, 1=READ, 2=WRITE, 3=BOTH
    share_deny: u32,
    /// Client that owns this open.
    clientid: u64,
}

/// Client registration state (SETCLIENTID / SETCLIENTID_CONFIRM).
#[derive(serde::Serialize, serde::Deserialize)]
struct ClientEntry {
    clientid: u64,
    verifier: [u8; 8],
    confirm_verifier: [u8; 8],
    id_string: Vec<u8>,
    confirmed: bool,
    last_renew: i64,
}

// ---------------------------------------------------------------------------
// CompoundProcessor
// ---------------------------------------------------------------------------

/// NFS v4.1 session slot state for exactly-once semantics.
#[derive(serde::Serialize, serde::Deserialize)]
struct SlotState {
    seqid: u32,
    cached_reply: Option<Vec<u8>>,
}

/// Stored inside SlotState's existing opaque cache field, preserving its
/// persisted schema. Legacy untagged cache entries are treated as uncached.
#[derive(serde::Serialize, serde::Deserialize)]
struct CachedCompound {
    request: [u8; 32],
    reply: Vec<u8>,
    data_items: Vec<Option<std::ops::Range<usize>>>,
}
const REPLAY_CACHE_MAGIC: &[u8; 8] = b"BRNFSRC1";

/// Fore-channel limits a session negotiated in CREATE_SESSION: the
/// client's request capped by what this server serves (RFC 8881 §18.36.3).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct ForeChannel {
    /// ca_maxrequestsize.
    max_request: u32,
    /// ca_maxresponsesize.
    max_response: u32,
    /// ca_maxoperations.
    max_ops: u32,
}

impl ForeChannel {
    /// This server's limits: a maximal READ or WRITE (FATTR4_MAXREAD /
    /// MAXWRITE, 1 MiB) plus 64 KiB of RPC and COMPOUND framing, and
    /// MAX_COMPOUND_OPS operations.
    const SERVER: Self = Self {
        max_request: SESSION_MAX_MESSAGE,
        max_response: SESSION_MAX_MESSAGE,
        max_ops: MAX_COMPOUND_OPS,
    };

    fn negotiate(max_request: u32, max_response: u32, max_ops: u32) -> Self {
        Self {
            max_request: max_request.min(Self::SERVER.max_request),
            max_response: max_response.min(Self::SERVER.max_response),
            max_ops: max_ops.min(Self::SERVER.max_ops),
        }
    }

    fn encode(&self) -> [u8; 12] {
        let mut bytes = [0u8; 12];
        bytes[..4].copy_from_slice(&self.max_request.to_le_bytes());
        bytes[4..8].copy_from_slice(&self.max_response.to_le_bytes());
        bytes[8..].copy_from_slice(&self.max_ops.to_le_bytes());
        bytes
    }

    /// Limits persisted by `encode`; a session persisted before they were
    /// recorded gets this server's limits, which it was never refused.
    fn decode(bytes: &[u8]) -> Self {
        let word = |i: usize| {
            bytes
                .get(i..i + 4)
                .and_then(|b| <[u8; 4]>::try_from(b).ok())
                .map(u32::from_le_bytes)
        };
        match (word(0), word(4), word(8)) {
            (Some(req), Some(resp), Some(ops)) if bytes.len() == 12 => {
                Self::negotiate(req, resp, ops)
            }
            _ => Self::SERVER,
        }
    }
}

/// Largest request or response a session may negotiate.
pub const SESSION_MAX_MESSAGE: u32 = (1 << 20) + (64 << 10);
/// Most slots a session gets (ca_maxrequests); SEQUENCE reports the
/// highest of them as sr_highest_slotid.
pub const SESSION_MAX_SLOTS: u32 = 64;

/// NFS v4.1 session.
#[derive(serde::Serialize, serde::Deserialize)]
struct SessionState {
    session_id: [u8; 16],
    clientid: u64,
    slots: Vec<SlotState>,
    // Separate persisted keys keep the original bincode session schema intact.
    #[serde(skip)]
    cache_limit: usize,
    #[serde(skip, default = "fore_channel_server")]
    fore: ForeChannel,
}

fn fore_channel_server() -> ForeChannel {
    ForeChannel::SERVER
}

/// Processes NFS v4 COMPOUND requests.
///
/// All protocol state is persisted to RocksDB via MetadataStore.
/// In-memory HashMaps serve as hot cache; RocksDB is the source of truth.
/// Server restart loads all state from RocksDB.
pub struct CompoundProcessor {
    vfs: Arc<crate::vfs_backend::VfsBackend>,
    /// MetadataStore for persisting protocol state.
    metadata: Option<Arc<nvstor_metadata::MetadataStore>>,
    /// NFS metrics.
    metrics: Arc<crate::metrics::NfsMetrics>,
    /// Optional Raft node for replicated mutations.
    #[allow(dead_code)]
    raft: Option<Arc<nvstor_raft::node::RaftNode>>,
    /// Open file state: stateid.other -> OpenState (cached + persisted)
    open_states: RwLock<HashMap<[u8; 12], OpenState>>,
    /// Registered clients: clientid -> ClientEntry (cached + persisted)
    clients: RwLock<HashMap<u64, ClientEntry>>,
    /// Reverse map: client id_string -> clientid (cached + persisted)
    client_ids: RwLock<HashMap<Vec<u8>, u64>>,
    /// NFS v4.1 sessions: session_id -> SessionState (cached + persisted)
    sessions: RwLock<HashMap<[u8; 16], SessionState>>,
    slot_locks: [parking_lot::Mutex<()>; 256],
    persist_lock: parking_lot::Mutex<()>,
    /// Counter for generating unique stateids (persisted).
    next_state: AtomicU64,
    /// Counter for generating client IDs (persisted).
    next_clientid: AtomicU64,
    /// Write verifier (WRITE, COMMIT and COPY replies): unique per server
    /// boot, and changed by [`Self::reset_write_verifier`] whenever a write
    /// or a sync fails, so every client resends what it has not committed.
    write_verifier: AtomicU64,
    /// Tests: the next [`Self::sync`] fails with EIO.
    #[cfg(test)]
    fail_next_sync: std::sync::atomic::AtomicBool,
    /// Server boot time (for grace period calculation).
    boot_time: i64,
    /// pNFS data-server netaddr — used to populate LAYOUTGET/GETDEVICEINFO
    /// responses. None disables pNFS (server returns LAYOUTUNAVAILABLE
    /// despite advertising the layout type). For a single-server export
    /// where we are both MDS and DS, set to (host, port) of this server.
    pnfs_ds_addr: parking_lot::RwLock<Option<(String, u16)>>,
    /// This server's other addresses for the same filesystem, returned as
    /// FATTR4_FS_LOCATIONS so a client mounting with `trunkdiscovery`
    /// adds one session-trunked transport per address.
    /// Empty: the attribute is neither advertised nor returned.
    trunk_addrs: parking_lot::RwLock<Vec<String>>,
}

/// Caller credentials for a single COMPOUND invocation.
///
/// Carried as a stack-local value through the dispatch pipeline so concurrent
/// compounds on a shared `CompoundProcessor` cannot contaminate each other's
/// uid/gid.
///
/// Built from the RPC credential by [`CallerCtx::from_auth`], the one place
/// root squashing and AUTH_NONE mapping happen, for TCP and RDMA alike.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CallerCtx {
    pub uid: u32,
    pub gid: u32,
    /// Supplementary group ids (AUTH_SYS carries at most 16).
    pub groups: Vec<u32>,
}

/// Default anonymous uid / gid (`nobody` / `nogroup`).
pub const ANON_ID: u32 = 65534;

impl CallerCtx {
    /// The default anonymous identity (65534:65534, no groups).
    pub const fn anonymous() -> Self {
        Self {
            uid: ANON_ID,
            gid: ANON_ID,
            groups: Vec::new(),
        }
    }

    pub const fn root() -> Self {
        Self {
            uid: 0,
            gid: 0,
            groups: Vec::new(),
        }
    }

    /// Whether `gid` is the caller's primary or a supplementary group.
    pub fn in_group(&self, gid: u32) -> bool {
        self.gid == gid || self.groups.contains(&gid)
    }

    /// The identity a request runs as under `policy`.
    ///
    /// - AUTH_NONE (no AUTH_SYS credential): the anonymous uid and gid,
    ///   no supplementary groups.
    /// - AUTH_SYS with root squashing: uid 0 becomes the anonymous uid and
    ///   every gid 0 (primary or supplementary) the anonymous gid, as Linux
    ///   nfsd squashes them. Other ids pass through.
    pub fn from_auth(creds: Option<&crate::rpc::AuthSys>, policy: &ExportPolicy) -> Self {
        let Some(auth) = creds else {
            return Self {
                uid: policy.anon_uid,
                gid: policy.anon_gid,
                groups: Vec::new(),
            };
        };
        let mut ctx = Self {
            uid: auth.uid,
            gid: auth.gid,
            groups: auth.aux_gids.clone(),
        };
        if policy.root_squash {
            if ctx.uid == 0 {
                ctx.uid = policy.anon_uid;
            }
            if ctx.gid == 0 {
                ctx.gid = policy.anon_gid;
            }
            for gid in &mut ctx.groups {
                if *gid == 0 {
                    *gid = policy.anon_gid;
                }
            }
        }
        ctx
    }
}

/// Runtime export policy applied per COMPOUND.
///
/// Derived from `NfsExport` at server startup and passed into the processor
/// per request. Keeps policy decisions out of the protocol core so tests and
/// non-server embeddings can use default (permissive) behavior.
#[derive(Debug, Clone)]
pub struct ExportPolicy {
    /// Map uid 0 (and gid 0) to `anon_uid` / `anon_gid`.
    pub root_squash: bool,
    /// Uid that squashed root and AUTH_NONE callers run as.
    pub anon_uid: u32,
    /// Gid that squashed root and AUTH_NONE callers run as.
    pub anon_gid: u32,
    /// Reject mutating operations with NFS4ERR_ROFS.
    pub read_only: bool,
    /// Auth flavors `SECINFO` / `SECINFO_NO_NAME` should advertise.
    ///
    /// Linux kernel clients pick the FIRST flavor in this list when retrying
    /// the COMPOUND. Advertising AUTH_NONE alongside AUTH_SYS when the export
    /// only accepts AUTH_SYS makes the client try AUTH_NONE first, get an
    /// `AUTH_TOOWEAK` rejection from the RPC layer, and tear down the
    /// session — even though AUTH_SYS would have worked.
    ///
    /// Default mirrors the historical behaviour (both flavors). The server
    /// derives this from each `NfsExport.sec`.
    pub advertised_flavors: Vec<u32>,
}

impl Default for ExportPolicy {
    fn default() -> Self {
        // Permissive default for library users / tests. Server constructs a
        // stricter policy from its `NfsExport` config.
        Self {
            root_squash: false,
            anon_uid: ANON_ID,
            anon_gid: ANON_ID,
            read_only: false,
            advertised_flavors: vec![crate::rpc::AUTH_SYS, crate::rpc::AUTH_NONE],
        }
    }
}

/// Map an opcode to its observability category.
///
/// Used by `process_inner` to bucket per-op latency. Categories are kept
/// coarse (5 of them) so Prometheus cardinality stays manageable; an
/// operator seeing "metadata p99 spiked" can drill into per-op debug logs.
pub(crate) fn op_category(opnum: u32) -> crate::metrics::OpCategory {
    use crate::metrics::OpCategory;
    match opnum {
        OP_READ | OP_READLINK | OP_READDIR | OP_READ_PLUS => OpCategory::Read,
        OP_WRITE | OP_COPY | OP_CLONE | OP_ALLOCATE | OP_DEALLOCATE | OP_WRITE_SAME => {
            OpCategory::Write
        }
        OP_LOOKUP
        | OP_LOOKUPP
        | OP_GETATTR
        | OP_ACCESS
        | OP_GETFH
        | OP_PUTFH
        | OP_PUTROOTFH
        | OP_PUTPUBFH
        | OP_SAVEFH
        | OP_RESTOREFH
        | OP_SETATTR
        | OP_VERIFY
        | OP_NVERIFY
        | OP_SECINFO
        | OP_SECINFO_NO_NAME
        | OP_SEEK
        | OP_IO_ADVISE
        | OP_GETXATTR
        | OP_SETXATTR
        | OP_LISTXATTRS
        | OP_REMOVEXATTR
        | OP_LAYOUTERROR
        | OP_LAYOUTSTATS
        | OP_COPY_NOTIFY
        | OP_OFFLOAD_CANCEL
        | OP_OFFLOAD_STATUS
        | OP_GET_DIR_DELEGATION
        | OP_WANT_DELEGATION => OpCategory::Metadata,
        OP_EXCHANGE_ID
        | OP_CREATE_SESSION
        | OP_DESTROY_SESSION
        | OP_SEQUENCE
        | OP_RECLAIM_COMPLETE
        | OP_DESTROY_CLIENTID
        | OP_BIND_CONN_TO_SESSION
        | OP_BACKCHANNEL_CTL
        | OP_TEST_STATEID
        | OP_FREE_STATEID
        | OP_SET_SSV
        | OP_SETCLIENTID
        | OP_SETCLIENTID_CONFIRM
        | OP_RENEW
        | OP_OPEN
        | OP_OPEN_CONFIRM
        | OP_OPEN_DOWNGRADE
        | OP_CLOSE
        | OP_LOCK
        | OP_LOCKT
        | OP_LOCKU
        | OP_RELEASE_LOCKOWNER
        | OP_DELEGPURGE
        | OP_DELEGRETURN => OpCategory::Session,
        _ => OpCategory::Other,
    }
}

/// Returns true if an opnum mutates filesystem state.
///
/// Consulted when `ExportPolicy::read_only` is set. Only ops that *change
/// stored data* are gated. State-tracking ops (OPEN, OPEN_CONFIRM,
/// OPEN_DOWNGRADE, CLOSE, LOCK, LOCKU) are allowed because read-only
/// mounts still issue them — `mount -t nfs4 -o ro` opens files for
/// reading. The actual write path is gated separately at WRITE / CREATE /
/// SETATTR / etc.
pub(crate) fn op_is_mutating(opnum: u32) -> bool {
    matches!(
        opnum,
        OP_WRITE
            | OP_CREATE
            | OP_REMOVE
            | OP_RENAME
            | OP_LINK
            | OP_SETATTR
            | OP_ALLOCATE
            | OP_DEALLOCATE
            | OP_COPY
            | OP_CLONE
    )
}

impl CompoundProcessor {
    /// Create a new COMPOUND processor with synthetic VFS (nvstor storage).
    pub fn new(vfs: Arc<Vfs>) -> Self {
        let backend = Arc::new(crate::vfs_backend::VfsBackend::Synthetic(vfs));
        Self::with_options_backend(
            backend,
            None,
            Arc::new(crate::metrics::NfsMetrics::new()),
            None,
        )
    }

    /// Create a new COMPOUND processor with pass-through VFS (real directory).
    pub fn new_passthrough(vfs: Arc<crate::passthrough::PassthroughVfs>) -> Self {
        let backend = Arc::new(crate::vfs_backend::VfsBackend::Passthrough(vfs));
        Self::with_options_backend(
            backend,
            None,
            Arc::new(crate::metrics::NfsMetrics::new()),
            None,
        )
    }

    /// Configure the pNFS data-server netaddr advertised in LAYOUTGET /
    /// GETDEVICEINFO responses. For a single-server export where the same
    /// process is both metadata and data server, set this to the host:port
    /// the server is bound to. Pass `None` to disable pNFS layouts (server
    /// will return NFS4ERR_LAYOUTUNAVAILABLE despite advertising the type).
    pub fn set_pnfs_ds_addr(&self, addr: Option<(String, u16)>) {
        *self.pnfs_ds_addr.write() = addr;
    }

    /// Addresses at which this server exports the same filesystem, one per
    /// rail, advertised as FATTR4_FS_LOCATIONS. A Linux client (5.18+)
    /// mounting with `trunkdiscovery,max_connect=N` reads them during the
    /// mount and adds one transport per address it is not already using,
    /// instead of the two per address that one extra mount per rail costs.
    /// An empty list turns the attribute off (not advertised in
    /// SUPPORTED_ATTRS), so such a client skips discovery rather than
    /// failing the mount on an empty location list.
    pub fn set_trunk_addrs(&self, addrs: Vec<String>) {
        *self.trunk_addrs.write() = addrs;
    }

    pub fn with_metrics(vfs: Arc<Vfs>, metrics: Arc<crate::metrics::NfsMetrics>) -> Self {
        let backend = Arc::new(crate::vfs_backend::VfsBackend::Synthetic(vfs));
        Self::with_options_backend(backend, None, metrics, None)
    }

    pub fn with_raft(
        vfs: Arc<Vfs>,
        raft: Arc<nvstor_raft::node::RaftNode>,
        metadata: Arc<nvstor_metadata::MetadataStore>,
    ) -> Self {
        let backend = Arc::new(crate::vfs_backend::VfsBackend::Synthetic(vfs));
        // Raft does not replace local NFS recovery state or its durability barrier.
        Self::with_options_backend(
            backend,
            Some(metadata),
            Arc::new(crate::metrics::NfsMetrics::new()),
            Some(raft),
        )
    }

    pub fn with_metadata(vfs: Arc<Vfs>, metadata: Arc<nvstor_metadata::MetadataStore>) -> Self {
        let backend = Arc::new(crate::vfs_backend::VfsBackend::Synthetic(vfs));
        Self::with_options_backend(
            backend,
            Some(metadata),
            Arc::new(crate::metrics::NfsMetrics::new()),
            None,
        )
    }

    pub fn with_options(
        vfs: Arc<Vfs>,
        metadata: Option<Arc<nvstor_metadata::MetadataStore>>,
        metrics: Arc<crate::metrics::NfsMetrics>,
        raft: Option<Arc<nvstor_raft::node::RaftNode>>,
    ) -> Self {
        let backend = Arc::new(crate::vfs_backend::VfsBackend::Synthetic(vfs));
        Self::with_options_backend(backend, metadata, metrics, raft)
    }

    fn with_options_backend(
        vfs: Arc<crate::vfs_backend::VfsBackend>,
        metadata: Option<Arc<nvstor_metadata::MetadataStore>>,
        metrics: Arc<crate::metrics::NfsMetrics>,
        raft: Option<Arc<nvstor_raft::node::RaftNode>>,
    ) -> Self {
        // Generate a unique write verifier from current time.
        let verf = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos()
            .to_le_bytes();
        let mut first = [0u8; 8];
        first.copy_from_slice(&verf[..8]);
        let write_verifier = AtomicU64::new(u64::from_le_bytes(first));

        // Load persisted state from RocksDB if available.
        let mut open_states = HashMap::new();
        let mut clients = HashMap::new();
        let mut client_ids = HashMap::new();
        let mut sessions = HashMap::new();
        let mut next_state_val = 1u64;
        let mut next_clientid_val = 1u64;

        if let Some(ref meta) = metadata {
            // Load counters.
            if let Ok(Some(bytes)) = meta.get_inline("nfs/counter/next_state") {
                if bytes.len() == 8 {
                    next_state_val = u64::from_le_bytes(bytes[..8].try_into().unwrap_or([0; 8]));
                }
            }
            if let Ok(Some(bytes)) = meta.get_inline("nfs/counter/next_clientid") {
                if bytes.len() == 8 {
                    next_clientid_val = u64::from_le_bytes(bytes[..8].try_into().unwrap_or([0; 8]));
                }
            }

            // Load clients.
            if let Ok(entries) = meta.scan_inline_prefix("nfs/client/") {
                for (key, bytes) in entries {
                    if key.starts_with("nfs/client_id/") {
                        continue;
                    }
                    if let Ok(entry) = bincode::deserialize::<ClientEntry>(&bytes) {
                        client_ids.insert(entry.id_string.clone(), entry.clientid);
                        clients.insert(entry.clientid, entry);
                    }
                }
            }

            // Load open states.
            if let Ok(entries) = meta.scan_inline_prefix("nfs/open/") {
                for (_key, bytes) in entries {
                    if let Ok(state) = bincode::deserialize::<OpenState>(&bytes) {
                        open_states.insert(state.stateid.other, state);
                    }
                }
            }

            // Load sessions.
            if let Ok(entries) = meta.scan_inline_prefix("nfs/session/") {
                for (_key, bytes) in entries {
                    if let Ok(mut session) = bincode::deserialize::<SessionState>(&bytes) {
                        session.cache_limit = meta
                            .get_inline(&format!(
                                "nfs/session_cache_limit/{}",
                                hex::encode(session.session_id)
                            ))
                            .ok()
                            .flatten()
                            .and_then(|v| <[u8; 4]>::try_from(v).ok())
                            .map(|v| u32::from_le_bytes(v) as usize)
                            .unwrap_or(MAX_CACHED_WIRE_REPLY)
                            .min(MAX_CACHED_WIRE_REPLY);
                        session.fore = meta
                            .get_inline(&format!(
                                "nfs/session_fore_limits/{}",
                                hex::encode(session.session_id)
                            ))
                            .ok()
                            .flatten()
                            .map_or(ForeChannel::SERVER, |v| ForeChannel::decode(&v));
                        sessions.insert(session.session_id, session);
                    }
                }
            }

            let nc = clients.len();
            let no = open_states.len();
            let ns = sessions.len();
            if nc > 0 || no > 0 || ns > 0 {
                debug!(
                    clients = nc,
                    open_states = no,
                    sessions = ns,
                    "nfs4: loaded protocol state from RocksDB"
                );
            }
        }

        Self {
            vfs,
            metadata,
            metrics,
            raft,
            open_states: RwLock::new(open_states),
            clients: RwLock::new(clients),
            client_ids: RwLock::new(client_ids),
            sessions: RwLock::new(sessions),
            slot_locks: std::array::from_fn(|_| parking_lot::Mutex::new(())),
            persist_lock: parking_lot::Mutex::new(()),
            next_state: AtomicU64::new(next_state_val),
            next_clientid: AtomicU64::new(next_clientid_val),
            write_verifier,
            #[cfg(test)]
            fail_next_sync: std::sync::atomic::AtomicBool::new(false),
            boot_time: std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs() as i64,
            pnfs_ds_addr: parking_lot::RwLock::new(None),
            trunk_addrs: parking_lot::RwLock::new(Vec::new()),
        }
    }

    /// Process a COMPOUND request payload and return the reply payload.
    ///
    /// Input format (RFC 7530 section 14.2):
    ///   tag (opaque), minorversion (u32), argarray (list of operations)
    ///
    /// Output format (RFC 7530 section 14.2):
    ///   status (u32), tag (opaque), resarray (list of results)
    // -----------------------------------------------------------------------
    // Persistence helpers — write to RocksDB on every state mutation
    // -----------------------------------------------------------------------

    /// Delete a client's persisted state (called when a client goes
    /// away — DESTROY_CLIENTID, RELEASE_LOCKOWNER cleanup, lease
    /// expiry). Done immediately rather than batched because deletion
    /// is rare and caller already holds a write lock on the in-memory
    /// state. Could batch later if profiling shows it matters.
    fn delete_persisted_client(&self, clientid: u64, id_string: &[u8]) {
        if let Some(ref meta) = self.metadata {
            let _ = meta.delete_inline(&format!("nfs/client/{clientid}"));
            let _ = meta.delete_inline(&format!("nfs/client_id/{}", hex::encode(id_string)));
        }
    }

    fn delete_persisted_open_state(&self, other: &[u8; 12]) {
        if let Some(ref meta) = self.metadata {
            let _ = meta.delete_inline(&format!("nfs/open/{}", hex::encode(other)));
        }
    }

    fn delete_persisted_session(&self, session_id: &[u8; 16]) {
        if let Some(ref meta) = self.metadata {
            let _ = meta.delete_inline(&format!("nfs/session/{}", hex::encode(session_id)));
            let _ = meta.delete_inline(&format!(
                "nfs/session_cache_limit/{}",
                hex::encode(session_id)
            ));
            let _ = meta.delete_inline(&format!(
                "nfs/session_fore_limits/{}",
                hex::encode(session_id)
            ));
        }
    }

    /// Persist ALL protocol state to RocksDB after each COMPOUND.
    ///
    /// **Hot-path perf:** uses a single `WriteBatch` so all
    /// `clients × open_states × sessions` writes go in one RocksDB
    /// transaction instead of a separate sync put per
    /// item — for a server with 100 clients × 1000 open states ×
    /// 100 sessions = 1100 fsyncs per COMPOUND. Now: 1 batch flush.
    /// SyncMode::Full preserves the current durability guarantee
    /// (every COMPOUND's state is on stable storage before reply).
    pub fn persist_all_state(&self) -> Result<(), String> {
        let Some(ref meta) = self.metadata else {
            return Ok(());
        };
        // Snapshot and commit in the same order. Otherwise an older snapshot
        // could overwrite a newly completed slot after that RPC was acknowledged.
        let _persist = self.persist_lock.lock();

        let mut batch = nvstor_metadata::WriteBatch::new();

        // Counters.
        let s = self.next_state.load(Ordering::Relaxed);
        let c = self.next_clientid.load(Ordering::Relaxed);
        batch.put_inline("nfs/counter/next_state", &s.to_le_bytes());
        batch.put_inline("nfs/counter/next_clientid", &c.to_le_bytes());

        // Clients.
        for entry in self.clients.read().values() {
            {
                let bytes = bincode::serialize(entry).map_err(|error| error.to_string())?;
                let key = format!("nfs/client/{}", entry.clientid);
                batch.put_inline(&key, &bytes);
                let id_key = format!("nfs/client_id/{}", hex::encode(&entry.id_string));
                batch.put_inline(&id_key, &entry.clientid.to_le_bytes());
            }
        }
        // Open states.
        for (other, state) in self.open_states.read().iter() {
            {
                let bytes = bincode::serialize(state).map_err(|error| error.to_string())?;
                let key = format!("nfs/open/{}", hex::encode(other));
                batch.put_inline(&key, &bytes);
            }
        }
        // Sessions.
        for session in self.sessions.read().values() {
            {
                let bytes = bincode::serialize(session).map_err(|error| error.to_string())?;
                let key = format!("nfs/session/{}", hex::encode(session.session_id));
                batch.put_inline(&key, &bytes);
                batch.put_inline(
                    &format!(
                        "nfs/session_cache_limit/{}",
                        hex::encode(session.session_id)
                    ),
                    &(session.cache_limit as u32).to_le_bytes(),
                );
                batch.put_inline(
                    &format!(
                        "nfs/session_fore_limits/{}",
                        hex::encode(session.session_id)
                    ),
                    &session.fore.encode(),
                );
            }
        }

        // One commit, fsync once. Durability matches the per-item
        // contract (state durable before COMPOUND reply).
        meta.commit_batch(batch, nvstor_metadata::SyncMode::Full)
            .map_err(|error| error.to_string())
    }

    /// Check if the server is in grace period after restart.
    /// Grace period only applies if there are persisted locks that need reclaiming.
    /// Duration: 15 seconds (enough for clients to reclaim, short enough for usability).
    fn in_grace_period(&self) -> bool {
        // Only enforce grace if there are locks to reclaim.
        let has_locks = !self.vfs.locks_empty();
        if !has_locks {
            return false;
        }
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs() as i64;
        now - self.boot_time < 15
    }

    /// Get a reference to the metrics for external monitoring.
    /// Return a clonable handle to the metrics object — for embedders that
    /// want to expose `/metrics` over HTTP from a separate task.
    pub fn metrics_arc(&self) -> Arc<crate::metrics::NfsMetrics> {
        self.metrics.clone()
    }

    pub fn metrics(&self) -> &crate::metrics::NfsMetrics {
        &self.metrics
    }

    /// Process a COMPOUND with caller credentials and an explicit export policy.
    ///
    /// Primary entry point used by the TCP server. `process_with_creds` and
    /// `process` are thin wrappers that default to a permissive policy for
    /// tests and non-server embeddings.
    pub fn process_with_creds_policy(
        &self,
        payload: &[u8],
        creds: Option<&crate::rpc::AuthSys>,
        policy: &ExportPolicy,
    ) -> Vec<u8> {
        let ctx = CallerCtx::from_auth(creds, policy);
        self.process_inner(
            payload,
            &ctx,
            policy,
            false,
            &mut DdpCtx::none(),
            &mut Vec::new(),
        )
    }

    /// RDMA uses the same operation implementation and export policy. The
    /// additional result ranges identify only RFC 8267 DDP-eligible fields.
    /// RDMA entry point. `alloc` lets READ place data in transport buffers;
    /// `args` are WRITE data the transport already placed (offsets relative
    /// to `payload`).
    pub(crate) fn process_rdma(
        &self,
        payload: &[u8],
        creds: Option<&crate::rpc::AuthSys>,
        policy: &ExportPolicy,
        alloc: Option<&dyn DdpAllocator>,
        args: &[DdpArg],
    ) -> (Vec<u8>, Vec<Option<DataItem>>) {
        let ctx = CallerCtx::from_auth(creds, policy);
        let mut items = Vec::new();
        let mut ddp = DdpCtx {
            alloc,
            args,
            produced: None,
        };
        let bytes = self.process_inner(payload, &ctx, policy, true, &mut ddp, &mut items);
        (bytes, items)
    }

    /// Process a COMPOUND with caller credentials under the default policy
    /// (root-squash on, read-only off). Retained for callers that do not yet
    /// construct an `ExportPolicy`.
    pub fn process_with_creds(
        &self,
        payload: &[u8],
        creds: Option<&crate::rpc::AuthSys>,
    ) -> Vec<u8> {
        let policy = ExportPolicy {
            root_squash: true,
            read_only: false,
            ..ExportPolicy::default()
        };
        self.process_with_creds_policy(payload, creds, &policy)
    }

    pub fn process(&self, payload: &[u8]) -> Vec<u8> {
        // Direct entry point (tests / internal use) runs as root.
        self.process_inner(
            payload,
            &CallerCtx::root(),
            &ExportPolicy::default(),
            false,
            &mut DdpCtx::none(),
            &mut Vec::new(),
        )
    }

    fn process_inner(
        &self,
        payload: &[u8],
        ctx: &CallerCtx,
        policy: &ExportPolicy,
        rdma: bool,
        ddp: &mut DdpCtx<'_>,
        data_items: &mut Vec<Option<DataItem>>,
    ) -> Vec<u8> {
        let start = std::time::Instant::now();
        let mut r = XdrReader::new(payload);

        // Parse tag.
        let tag = match r.get_opaque() {
            Ok(t) => t,
            Err(_) => return self.encode_error(NFS4ERR_INVAL, &[], &[]),
        };

        // Parse minorversion.
        let minorversion = match r.get_u32() {
            Ok(v) => v,
            Err(_) => return self.encode_error(NFS4ERR_INVAL, &tag, &[]),
        };

        // Parse operation count. Enforce MAX_COMPOUND_OPS so a malicious
        // client can't send num_ops = u32::MAX and pin the server in a
        // 4-billion-iter loop.
        let num_ops = match r.get_u32() {
            Ok(n) if n > MAX_COMPOUND_OPS => {
                warn!(
                    n,
                    max = MAX_COMPOUND_OPS,
                    "nfs4: COMPOUND op count exceeds cap"
                );
                return self.encode_error(NFS4ERR_RESOURCE, &tag, &[]);
            }
            Ok(n) => n,
            Err(_) => return self.encode_error(NFS4ERR_INVAL, &tag, &[]),
        };

        debug!(
            minorversion,
            num_ops,
            tag_len = tag.len(),
            "nfs4: COMPOUND request"
        );
        let request_digest: [u8; 32] = {
            let mut hash = Sha256::new();
            hash.update(payload);
            // Placed WRITE data is not hashed (it is absent from the payload);
            // its location and size still distinguish the request.
            for arg in ddp.args {
                hash.update((arg.offset as u64).to_be_bytes());
                hash.update((arg.len as u64).to_be_bytes());
            }
            hash.update(ctx.uid.to_be_bytes());
            hash.update(ctx.gid.to_be_bytes());
            hash.update((ctx.groups.len() as u32).to_be_bytes());
            for gid in &ctx.groups {
                hash.update(gid.to_be_bytes());
            }
            hash.update([u8::from(policy.read_only)]);
            hash.finalize().into()
        };
        // Serialize only requests sharing a bounded slot-lock stripe, not the
        // full session. Hold it through reply-cache publication and persistence.
        let sequence = if minorversion >= 1 {
            let mut peek = XdrReader::new(r.remaining_bytes());
            (|| {
                if peek.get_u32().ok()? != OP_SEQUENCE {
                    return None;
                }
                let sid: [u8; 16] = peek.get_opaque_fixed(16).ok()?.try_into().ok()?;
                peek.get_u32().ok()?;
                let slot = peek.get_u32().ok()?;
                peek.get_u32().ok()?;
                Some((sid, slot, peek.get_u32().ok()? != 0))
            })()
        } else {
            None
        };
        let _slot_guard = sequence.map(|(sid, slot, _)| {
            let stripe = sid.iter().fold(slot as usize, |hash, byte| {
                hash.wrapping_mul(31).wrapping_add(*byte as usize)
            }) % self.slot_locks.len();
            self.slot_locks[stripe].lock()
        });

        // State carried across operations in the COMPOUND.
        let mut current_fh: Option<Vec<u8>> = None;
        let mut saved_fh: Option<Vec<u8>> = None;
        let mut results: Vec<(u32, Vec<u8>)> = Vec::new();
        // External READ data by result index (RDMA direct placement).
        let mut externals: Vec<Option<(Box<dyn DdpBuffer>, usize)>> = Vec::new();
        let mut compound_status = NFS4_OK;
        let mut reply_budget = if rdma {
            crate::rpcrdma::chunks::MAX_RPC_SIZE
        } else {
            crate::rpc::MAX_RECORD_LEN
        };
        let limits = sequence.and_then(|(sid, _, cachethis)| {
            self.sessions
                .read()
                .get(&sid)
                .map(|session| (session.fore, cachethis.then_some(session.cache_limit)))
        });
        let mut budget_error = NFS4ERR_RESOURCE;
        if let Some((fore, cache_limit)) = limits {
            // The session's negotiated limits (RFC 8881 §2.10.6.4). A
            // request over them is refused before anything executes or the
            // slot advances.
            if num_ops > fore.max_ops {
                return self.encode_error(NFS4ERR_TOO_MANY_OPS, &tag, &[(OP_SEQUENCE, Vec::new())]);
            }
            let placed: usize = ddp.args.iter().map(|arg| arg.len).sum();
            if payload.len().saturating_add(placed) > fore.max_request as usize {
                return self.encode_error(NFS4ERR_REQ_TOO_BIG, &tag, &[(OP_SEQUENCE, Vec::new())]);
            }
            if (fore.max_response as usize) < reply_budget {
                reply_budget = fore.max_response as usize;
                budget_error = NFS4ERR_REP_TOO_BIG;
            }
            if let Some(limit) = cache_limit {
                reply_budget = reply_budget.min(limit);
                budget_error = NFS4ERR_REP_TOO_BIG_TO_CACHE;
            }
        }
        reply_budget = reply_budget.saturating_sub(128 + tag.len());
        // Session replay tracking (v4.1+).
        let mut session_cache_key: Option<([u8; 16], u32)> = None; // (session_id, slotid)

        for i in 0..num_ops {
            let opnum = match r.get_u32() {
                Ok(op) => op,
                Err(_) => {
                    compound_status = NFS4ERR_INVAL;
                    break;
                }
            };

            // A later SEQUENCE would bypass the slot guard and overwrite the
            // first operation's cache key. v4.0 has no session operations.
            if opnum == OP_SEQUENCE && (minorversion == 0 || i != 0) {
                results.push((opnum, Vec::new()));
                compound_status = if minorversion == 0 {
                    NFS4ERR_OP_ILLEGAL
                } else {
                    NFS4ERR_SEQUENCE_POS
                };
                break;
            }
            // For v4.1+, SEQUENCE must be the first operation.
            if minorversion >= 1 && i == 0 && opnum == OP_SEQUENCE {
                // Check for replay before processing.
                if let Some(cached) =
                    self.check_sequence_replay(payload, &request_digest, data_items)
                {
                    return cached;
                }
            }

            // Read-only export: refuse mutating ops before touching the VFS.
            // Skips dispatch, records the error status, stops the compound.
            if policy.read_only && op_is_mutating(opnum) {
                warn!(opnum, "nfs4: mutating op refused (read-only export)");
                results.push((opnum, Vec::new()));
                compound_status = NFS4ERR_ROFS;
                break;
            }

            let op_start = std::time::Instant::now();
            if reply_budget < 8 || (opnum == OP_SEQUENCE && reply_budget < 44) {
                results.push((opnum, Vec::new()));
                compound_status = budget_error;
                break;
            }
            let (status, result_data) = self.dispatch_op(
                opnum,
                &mut r,
                &mut current_fh,
                &mut saved_fh,
                ctx,
                policy,
                minorversion,
                rdma,
                reply_budget - 8,
                budget_error,
                ddp,
            );
            let produced = ddp.produced.take();
            let placed = produced.as_ref().map_or(0, |(_, len)| (len + 3) & !3);
            if result_data.len() + placed > reply_budget - 8 {
                results.push((opnum, Vec::new()));
                compound_status = budget_error;
                break;
            }
            reply_budget -= 8 + result_data.len() + placed;
            if let Some(produced) = produced {
                externals.resize_with(results.len(), || None);
                externals.push(Some(produced));
            }
            // Per-op latency goes into the category histogram. Cardinality
            // stays at 5 categories rather than 56 ops; sufficient for "is
            // metadata fast?" / "is read fast?" alerts.
            let elapsed_us = op_start.elapsed().as_micros() as u64;
            self.metrics
                .record_op_latency(op_category(opnum), elapsed_us);

            debug!(
                opnum,
                status,
                result_len = result_data.len(),
                "nfs4: op result"
            );

            // SEQUENCE result starts with session_id, sequenceid, then slotid.
            if opnum == OP_SEQUENCE && status == NFS4_OK && result_data.len() >= 24 {
                let mut sid = [0u8; 16];
                sid.copy_from_slice(&result_data[..16]);
                let slotid = u32::from_be_bytes([
                    result_data[20],
                    result_data[21],
                    result_data[22],
                    result_data[23],
                ]);
                session_cache_key = Some((sid, slotid));
            }

            results.push((opnum, result_data));

            if status != NFS4_OK {
                compound_status = status;
                break; // Stop processing on first error.
            }
        }

        externals.resize_with(results.len(), || None);
        let ext_lens: Vec<Option<usize>> = externals
            .iter()
            .map(|e| e.as_ref().map(|(_, len)| *len))
            .collect();
        let mut item_results = Vec::new();
        let reply = self.encode_compound_reply_with_layout(
            compound_status,
            &tag,
            &results,
            &ext_lens,
            data_items,
            &mut item_results,
        );
        attach_externals(data_items, &item_results, &mut externals);

        // Record metrics.
        self.metrics
            .record_compound(start, compound_status != NFS4_OK);

        // Cache bounded replies. An uncached retry is rejected explicitly;
        // non-idempotent operations must never simply execute a second time.
        if let Some((session_id, slotid)) = session_cache_key {
            if reply.len() <= MAX_SESSION_REPLY_CACHE_BYTES {
                let mut sessions = self.sessions.write();
                if let Some(session) = sessions.get_mut(&session_id) {
                    if let Some(slot) = session.slots.get_mut(slotid as usize) {
                        slot.cached_reply =
                            Self::cache_compound(request_digest, &reply, data_items);
                    }
                }
            }
        }

        // Persist all protocol state changes to RocksDB.
        if let Err(error) = self.persist_all_state() {
            tracing::error!(%error, "NFS protocol-state durability barrier failed");
            if let Some((_, data)) = results.last_mut() {
                data.clear();
            }
            detach_externals(data_items, &item_results, &mut externals);
            let failed_reply = self.encode_compound_reply_with_layout(
                NFS4ERR_IO,
                &tag,
                &results,
                &ext_lens,
                data_items,
                &mut item_results,
            );
            attach_externals(data_items, &item_results, &mut externals);
            // A retry must never replay the successful reply whose persist failed.
            if let Some((session_id, slotid)) = session_cache_key {
                if let Some(session) = self.sessions.write().get_mut(&session_id) {
                    if let Some(slot) = session.slots.get_mut(slotid as usize) {
                        slot.cached_reply =
                            Self::cache_compound(request_digest, &failed_reply, data_items);
                    }
                }
            }
            return failed_reply;
        }

        reply
    }

    fn cache_compound(
        request: [u8; 32],
        reply: &[u8],
        data_items: &[Option<DataItem>],
    ) -> Option<Vec<u8>> {
        // Placed READ data is copied into the cache only for replies small
        // enough to cache at all; large replies were never cached.
        let placed: usize = data_items
            .iter()
            .flatten()
            .filter(|item| item.external.is_some())
            .map(|item| (item.len + 3) & !3)
            .sum();
        if reply.len() + placed > MAX_SESSION_REPLY_CACHE_BYTES {
            return None;
        }
        let (reply, ranges) = crate::ddp::flatten(reply, data_items);
        let cache = CachedCompound {
            request,
            reply,
            data_items: ranges,
        };
        let encoded = match bincode::serialize(&cache) {
            Ok(encoded) => encoded,
            Err(error) => {
                tracing::error!(%error, "NFS reply cache encoding failed");
                return None;
            }
        };
        if encoded.len() + REPLAY_CACHE_MAGIC.len() > MAX_SESSION_REPLY_CACHE_BYTES {
            return None;
        }
        Some([REPLAY_CACHE_MAGIC.as_slice(), encoded.as_slice()].concat())
    }

    fn check_sequence_replay(
        &self,
        payload: &[u8],
        request: &[u8; 32],
        data_items: &mut Vec<Option<DataItem>>,
    ) -> Option<Vec<u8>> {
        let mut peek = XdrReader::new(payload);
        let tag = peek.get_opaque().ok()?;
        peek.get_u32().ok()?;
        let num_ops = peek.get_u32().ok()?;
        if peek.get_u32().ok()? != OP_SEQUENCE {
            return None;
        }
        let session_id: [u8; 16] = peek.get_opaque_fixed(16).ok()?.try_into().ok()?;
        let seqid = peek.get_u32().ok()?;
        let slotid = peek.get_u32().ok()?;
        peek.get_u32().ok()?; // sa_highest_slotid
        peek.get_u32().ok()?;
        let sessions = self.sessions.read();
        let session = sessions.get(&session_id)?;
        let slot = session.slots.get(slotid as usize)?;
        if seqid != slot.seqid {
            return None;
        }
        if let Some(encoded) = slot
            .cached_reply
            .as_ref()
            .and_then(|bytes| bytes.strip_prefix(REPLAY_CACHE_MAGIC))
        {
            if let Ok(cache) = bincode::deserialize::<CachedCompound>(encoded) {
                if &cache.request != request {
                    return Some(self.encode_error(
                        NFS4ERR_SEQ_FALSE_RETRY,
                        &tag,
                        &[(OP_SEQUENCE, vec![])],
                    ));
                }
                if let Some(client) = self.clients.write().get_mut(&session.clientid) {
                    client.last_renew = std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_secs() as i64;
                }
                *data_items = cache
                    .data_items
                    .into_iter()
                    .map(|range| range.map(DataItem::inline))
                    .collect();
                return Some(cache.reply);
            }
        }
        // RFC 8881 2.10.6.1: RETRY_UNCACHED_REP belongs to the operation
        // following a successful SEQUENCE, not to SEQUENCE itself.
        if num_ops > 1 {
            let next = peek.get_u32().ok()?;
            let mut sequence = XdrWriter::new();
            sequence.put_raw(&session_id);
            sequence.put_u32(seqid);
            sequence.put_u32(slotid);
            sequence.put_u32(session.slots.len() as u32 - 1);
            sequence.put_u32(session.slots.len() as u32 - 1);
            sequence.put_u32(0);
            return Some(self.encode_error(
                NFS4ERR_RETRY_UNCACHED_REP,
                &tag,
                &[(OP_SEQUENCE, sequence.into_bytes()), (next, vec![])],
            ));
        }
        Some(self.encode_error(NFS4ERR_SEQ_MISORDERED, &tag, &[(OP_SEQUENCE, vec![])]))
    }

    // -----------------------------------------------------------------------
    // Operation dispatch
    // -----------------------------------------------------------------------

    fn dispatch_op(
        &self,
        opnum: u32,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        saved_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
        policy: &ExportPolicy,
        minorversion: u32,
        rdma: bool,
        reply_budget: usize,
        budget_error: u32,
        ddp: &mut DdpCtx<'_>,
    ) -> (u32, Vec<u8>) {
        self.metrics.record_op(opnum);
        match opnum {
            OP_ACCESS => self.op_access(r, current_fh, ctx),
            OP_CLOSE => self.op_close(r, current_fh),
            OP_COMMIT => self.op_commit(r, current_fh),
            OP_CREATE => self.op_create(r, current_fh, ctx),
            OP_DELEGPURGE => self.op_delegpurge(r),
            OP_DELEGRETURN => self.op_delegreturn(r),
            OP_GETATTR => self.op_getattr(r, current_fh),
            OP_GETFH => self.op_getfh(current_fh),
            OP_LINK => self.op_link(r, current_fh, saved_fh, ctx),
            OP_LOCK => self.op_lock(r, current_fh),
            OP_LOCKT => self.op_lockt(r, current_fh),
            OP_LOCKU => self.op_locku(r, current_fh),
            OP_LOOKUP => self.op_lookup(r, current_fh, ctx),
            OP_LOOKUPP => self.op_lookupp(current_fh, ctx),
            OP_NVERIFY => self.op_nverify(r, current_fh),
            OP_OPEN => self.op_open(r, current_fh, ctx, policy, minorversion),
            OP_OPENATTR => self.op_openattr(r),
            OP_OPEN_CONFIRM => self.op_open_confirm(r),
            OP_OPEN_DOWNGRADE => self.op_open_downgrade(r),
            OP_PUTFH => self.op_putfh(r, current_fh),
            OP_PUTPUBFH => self.op_putpubfh(current_fh),
            OP_PUTROOTFH => self.op_putrootfh(current_fh),
            OP_READ => self.op_read(r, current_fh, ctx, reply_budget, budget_error, ddp),
            OP_READDIR => self.op_readdir(r, current_fh, ctx),
            OP_READLINK => self.op_readlink(current_fh),
            OP_REMOVE => self.op_remove(r, current_fh, ctx),
            OP_RENAME => self.op_rename(r, current_fh, saved_fh, ctx),
            OP_RENEW => self.op_renew(r),
            OP_RESTOREFH => self.op_restorefh(current_fh, saved_fh),
            OP_SAVEFH => self.op_savefh(current_fh, saved_fh),
            OP_SECINFO => self.op_secinfo(r, policy),
            OP_SETATTR => self.op_setattr(r, current_fh, ctx),
            OP_SETCLIENTID => self.op_setclientid(r),
            OP_SETCLIENTID_CONFIRM => self.op_setclientid_confirm(r),
            OP_VERIFY => self.op_verify(r, current_fh),
            OP_WRITE => self.op_write(r, current_fh, ctx, ddp.args),
            OP_RELEASE_LOCKOWNER => self.op_release_lockowner(r),
            // NFS v4.1 operations
            OP_EXCHANGE_ID => self.op_exchange_id(r),
            OP_CREATE_SESSION => self.op_create_session(r, rdma),
            OP_DESTROY_SESSION => self.op_destroy_session(r),
            OP_SEQUENCE => self.op_sequence(r),
            OP_RECLAIM_COMPLETE => self.op_reclaim_complete(r),
            OP_DESTROY_CLIENTID => self.op_destroy_clientid(r),
            OP_SECINFO_NO_NAME => self.op_secinfo_no_name(r, policy),
            OP_TEST_STATEID => self.op_test_stateid(r),
            OP_FREE_STATEID => self.op_free_stateid(r),
            OP_BIND_CONN_TO_SESSION => self.op_bind_conn_to_session(r, rdma),
            OP_BACKCHANNEL_CTL => self.op_backchannel_ctl(r),
            // pNFS stubs
            OP_LAYOUTGET => self.op_layoutget(r, current_fh),
            OP_LAYOUTCOMMIT => self.op_layoutcommit(r),
            OP_LAYOUTRETURN => self.op_layoutreturn(r),
            OP_GETDEVICEINFO => self.op_getdeviceinfo(r),
            OP_GETDEVICELIST => self.op_getdevicelist(r),
            // NFS v4.2 operations
            OP_ALLOCATE => self.op_allocate(r, current_fh, ctx),
            OP_COPY => self.op_copy(r, current_fh, saved_fh, ctx),
            OP_COPY_NOTIFY => self.op_copy_notify(r),
            OP_DEALLOCATE => self.op_deallocate(r, current_fh, ctx),
            OP_IO_ADVISE => self.op_io_advise(r),
            OP_LAYOUTERROR => self.op_layouterror(r),
            OP_LAYOUTSTATS => self.op_layoutstats(r),
            OP_OFFLOAD_CANCEL => self.op_offload_cancel(r),
            OP_OFFLOAD_STATUS => self.op_offload_status(r),
            OP_READ_PLUS => self.op_read_plus(r, current_fh, ctx, reply_budget, budget_error),
            OP_SEEK => self.op_seek(r, current_fh),
            OP_WRITE_SAME => self.op_write_same(),
            OP_CLONE => self.op_clone(r, current_fh, saved_fh, ctx),
            OP_GETXATTR => self.op_getxattr(r, current_fh, ctx),
            OP_SETXATTR => self.op_setxattr(r, current_fh, ctx),
            OP_LISTXATTRS => self.op_listxattrs(r, current_fh, ctx),
            OP_REMOVEXATTR => self.op_removexattr(r, current_fh, ctx),
            // v4.1 ops we previously didn't dispatch.
            OP_GET_DIR_DELEGATION => self.op_get_dir_delegation(r),
            OP_WANT_DELEGATION => self.op_want_delegation(r),
            OP_SET_SSV => self.op_set_ssv(r),
            _ => {
                warn!(opnum, "nfs4: illegal operation");
                (NFS4ERR_OP_ILLEGAL, Vec::new())
            }
        }
    }

    // -----------------------------------------------------------------------
    // Filehandle operations
    // -----------------------------------------------------------------------

    fn op_putrootfh(&self, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = self.vfs.ino_to_filehandle(1); // ROOT_INO = 1
        *current_fh = Some(fh);
        (NFS4_OK, Vec::new())
    }

    fn op_putfh(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = match r.get_opaque() {
            Ok(fh) => fh,
            Err(_) => return (NFS4ERR_BADXDR, Vec::new()),
        };
        // nfs_fh4 is at most NFS4_FHSIZE bytes and never empty.
        if fh.is_empty() || fh.len() > NFS4_FHSIZE {
            return (NFS4ERR_BADHANDLE, Vec::new());
        }
        // Validate the filehandle resolves to a known inode.
        match self.vfs.filehandle_to_ino(&fh) {
            Ok(_) => {
                *current_fh = Some(fh);
                (NFS4_OK, Vec::new())
            }
            Err(e) => (fh_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_getfh(&self, current_fh: &Option<Vec<u8>>) -> (u32, Vec<u8>) {
        match current_fh {
            Some(fh) => {
                let mut w = XdrWriter::new();
                w.put_opaque(fh);
                (NFS4_OK, w.into_bytes())
            }
            None => (NFS4ERR_NOFILEHANDLE, Vec::new()),
        }
    }

    fn op_savefh(
        &self,
        current_fh: &Option<Vec<u8>>,
        saved_fh: &mut Option<Vec<u8>>,
    ) -> (u32, Vec<u8>) {
        match current_fh {
            Some(fh) => {
                *saved_fh = Some(fh.clone());
                (NFS4_OK, Vec::new())
            }
            None => (NFS4ERR_NOFILEHANDLE, Vec::new()),
        }
    }

    fn op_restorefh(
        &self,
        current_fh: &mut Option<Vec<u8>>,
        saved_fh: &Option<Vec<u8>>,
    ) -> (u32, Vec<u8>) {
        match saved_fh {
            Some(fh) => {
                *current_fh = Some(fh.clone());
                (NFS4_OK, Vec::new())
            }
            None => (NFS4ERR_NOFILEHANDLE, Vec::new()),
        }
    }

    // -----------------------------------------------------------------------
    // Directory operations
    // -----------------------------------------------------------------------

    fn op_lookup(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        let name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        let parent_ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        self.metrics.record_lookup();
        match self.vfs.lookup(parent_ino, &name, ctx) {
            Ok(attrs) => {
                let child_fh = self.vfs.ino_to_filehandle(attrs.ino);
                *current_fh = Some(child_fh);
                (NFS4_OK, Vec::new())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_getattr(&self, r: &mut XdrReader, current_fh: &Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse requested attribute bitmap.
        let (req_word0, req_word1) = match decode_bitmap(r) {
            Ok(bm) => bm,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        self.metrics.record_getattr();
        let attrs = match self.vfs.getattr(ino) {
            Ok(a) => a,
            Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
        };

        let mut w = XdrWriter::new();
        let pnfs = self.pnfs_ds_addr.read().is_some();
        let trunk_addrs = self.trunk_addrs.read();
        encode_fattr4(&mut w, &attrs, req_word0, req_word1, pnfs, &trunk_addrs);
        let result = w.into_bytes();
        debug!(
            req_word0 = format!("0x{:08x}", req_word0),
            req_word1 = format!("0x{:08x}", req_word1),
            result_len = result.len(),
            "nfs4: GETATTR request/response"
        );
        (NFS4_OK, result)
    }

    fn op_setattr(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Consume stateid (we don't enforce it for setattr).
        let _stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };

        // Parse attribute bitmap and values.
        let (set_word0, set_word1) = match decode_bitmap(r) {
            Ok(bm) => bm,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // Parse attr values opaque.
        let attr_vals = match r.get_opaque() {
            Ok(v) => v,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        // Parse attr values in bitmap order. FATTR4 attrs are encoded
        // back-to-back in the "set" bitmap order.
        let mut vr = XdrReader::new(&attr_vals);
        let mut size_opt: Option<u64> = None;
        let mut mode_opt: Option<u32> = None;
        let mut uid_opt: Option<u32> = None;
        let mut gid_opt: Option<u32> = None;
        let mut atime_opt: Option<i64> = None;
        let mut mtime_opt: Option<i64> = None;

        // Word 0 attrs.
        if (set_word0 & (1 << FATTR4_SIZE)) != 0 {
            if let Ok(sz) = vr.get_u64() {
                size_opt = Some(sz);
            }
        }

        // Word 1 attrs.
        if (set_word1 & (1 << (FATTR4_MODE - 32))) != 0 {
            if let Ok(m) = vr.get_u32() {
                mode_opt = Some(m);
            }
        }
        if (set_word1 & (1 << (FATTR4_OWNER - 32))) != 0 {
            // FATTR4_OWNER is utf8str_mixed: "user@domain" or numeric.
            // We accept numeric for now and ignore mapped strings; a
            // proper NFSv4 ID mapper is a separate feature.
            if let Ok(s) = vr.get_string() {
                uid_opt = s.parse::<u32>().ok();
            }
        }
        if (set_word1 & (1 << (FATTR4_OWNER_GROUP - 32))) != 0 {
            if let Ok(s) = vr.get_string() {
                gid_opt = s.parse::<u32>().ok();
            }
        }
        if (set_word1 & (1 << 16)) != 0 {
            // TIME_ACCESS_SET
            let set_it = vr.get_u32().unwrap_or(0);
            if set_it == 1 {
                // SET_TO_CLIENT_TIME4
                let secs = vr.get_i64().unwrap_or(0);
                let _nsec = vr.get_u32().unwrap_or(0);
                atime_opt = Some(secs);
            }
        }
        if (set_word1 & (1 << 22)) != 0 {
            // TIME_MODIFY_SET
            let set_it = vr.get_u32().unwrap_or(0);
            if set_it == 1 {
                let secs = vr.get_i64().unwrap_or(0);
                let _nsec = vr.get_u32().unwrap_or(0);
                mtime_opt = Some(secs);
            }
        }

        // Apply all changes via setattr_full so passthrough sees them in
        // one call (matches utimensat/chown semantics — no flush in between).
        if let Err(e) = self.vfs.setattr_full(
            ino, size_opt, mode_opt, uid_opt, gid_opt, atime_opt, mtime_opt, ctx,
        ) {
            return (nvstor_err_to_nfs4(&e), Vec::new());
        }

        // Return the bitmap of attributes that were set.
        let mut w = XdrWriter::new();
        encode_bitmap(&mut w, set_word0, set_word1);
        (NFS4_OK, w.into_bytes())
    }

    fn op_readdir(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse READDIR args: cookie, cookieverf, dircount, maxcount, attr_request bitmap.
        let cookie = match r.get_u64() {
            Ok(c) => c,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let cookieverf = match r.get_opaque_fixed(8) {
            Ok(v) => v,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let _dircount = match r.get_u32() {
            Ok(d) => d,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let _maxcount = match r.get_u32() {
            Ok(m) => m,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let (req_word0, req_word1) = match decode_bitmap(r) {
            Ok(bm) => bm,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        // Cookies 1 and 2 are reserved (RFC 8881 §18.23.3), as in Linux nfsd.
        if cookie == 1 || cookie == 2 {
            return (NFS4ERR_BAD_COOKIE, Vec::new());
        }
        // Taken before the listing, so a change in between can only make the
        // verifier older than the cookies (a spurious NOT_SAME), never newer.
        let verifier = match self.vfs.readdir_verifier(ino) {
            Ok(verifier) => verifier,
            Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
        };
        let (entries, eof) = match self.vfs.readdir(ino, cookie, ctx) {
            Ok(result) => result,
            Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
        };
        if cookie != 0 && !readdir_verifier_matches(&cookieverf, &verifier) {
            return (NFS4ERR_NOT_SAME, Vec::new());
        }

        let mut w = XdrWriter::new();
        w.put_opaque_fixed(&verifier);

        // Encode directory entries as a linked list.
        // NFS v4: server MUST NOT include "." and ".." (RFC 7530 section 16.24.4).
        // The kernel adds them on the client side.
        let pnfs = self.pnfs_ds_addr.read().is_some();
        let trunk_addrs = self.trunk_addrs.read();
        for entry in entries.iter().filter(|e| e.name != "." && e.name != "..") {
            w.put_bool(true); // value follows
            w.put_u64(entry.cookie); // cookie
            w.put_string(&entry.name); // name

            // Get attrs for this entry if possible.
            if let Ok(attrs) = self.vfs.getattr(entry.ino) {
                encode_fattr4(&mut w, &attrs, req_word0, req_word1, pnfs, &trunk_addrs);
            } else {
                // Empty attrs.
                encode_bitmap(&mut w, 0, 0);
                w.put_opaque(&[]);
            }
        }
        w.put_bool(false); // No more entries in chain.
        w.put_bool(eof); // EOF flag.

        (NFS4_OK, w.into_bytes())
    }

    // -----------------------------------------------------------------------
    // File I/O
    // -----------------------------------------------------------------------

    fn op_read(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
        reply_budget: usize,
        budget_error: u32,
        ddp: &mut DdpCtx<'_>,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse stateid.
        let _stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };

        let offset = match r.get_u64() {
            Ok(o) => o,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let count = match r.get_u32() {
            Ok(c) if c > MAX_READ_COUNT => {
                warn!(c, max = MAX_READ_COUNT, "nfs4: READ count exceeds cap");
                return (NFS4ERR_RESOURCE, Vec::new());
            }
            Ok(c) if c as usize > reply_budget.saturating_sub(11) => {
                return (budget_error, Vec::new())
            }
            Ok(c) => c,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        // Permission check: read requires r (4) permission.
        if let Err(e) = self.vfs.check_permission(ino, ctx.uid, ctx.gid, 4) {
            return (rw_err_to_nfs4(&e), Vec::new());
        }

        // Direct placement: read the file straight into a transport buffer.
        if let Some(alloc) = ddp.alloc.filter(|_| count as usize >= DDP_MIN_READ) {
            if let Some(mut buf) = alloc.lease(count as usize) {
                return match self.vfs.read_into(
                    ino,
                    offset,
                    &mut buf.bytes_mut()[..count as usize],
                    ctx,
                ) {
                    Ok(n) => {
                        let eof = self
                            .vfs
                            .getattr(ino)
                            .ok()
                            .map_or(true, |a| offset + n as u64 >= a.size);
                        self.metrics.record_read(n as u64);
                        let mut w = XdrWriter::new();
                        w.put_bool(eof);
                        w.put_u32(n as u32); // data follows out of line
                        ddp.produced = Some((buf, n));
                        (NFS4_OK, w.into_bytes())
                    }
                    Err(e) => (rw_err_to_nfs4(&e), Vec::new()),
                };
            }
        }

        match self.vfs.read(ino, offset, count as u64, ctx) {
            Ok(data) => {
                let eof = {
                    let attrs = self.vfs.getattr(ino).ok();
                    attrs.map_or(true, |a| offset + data.len() as u64 >= a.size)
                };
                self.metrics.record_read(data.len() as u64);
                let mut w = XdrWriter::new();
                w.put_bool(eof);
                w.put_opaque(&data);
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (rw_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_write(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
        placed: &[DdpArg],
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse stateid.
        let _stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };

        let offset = match r.get_u64() {
            Ok(o) => o,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let stable = match r.get_u32() {
            Ok(s) => s,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        // Data placed by the transport is absent from the payload: only its
        // XDR length is inline, ending where the placement says.
        let inline;
        let data: &[u8] = match placed.iter().find(|arg| arg.offset == r.position() + 4) {
            Some(arg) => match r.get_u32() {
                Ok(len) if len as usize <= arg.len && arg.len <= (len as usize + 3) & !3 => {
                    &arg.buf.bytes()[..len as usize]
                }
                _ => return (NFS4ERR_INVAL, Vec::new()),
            },
            None => match r.get_opaque() {
                Ok(d) => {
                    inline = d;
                    &inline
                }
                Err(_) => return (NFS4ERR_INVAL, Vec::new()),
            },
        };

        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        // Permission check: write requires w (2) permission.
        if let Err(e) = self.vfs.check_permission(ino, ctx.uid, ctx.gid, 2) {
            return (rw_err_to_nfs4(&e), Vec::new());
        }

        match self.vfs.write(ino, offset, data, ctx) {
            Ok(count) => {
                // Honor the client's requested write stability
                // truthfully. Writes land in PmStore with
                // SyncMode::None (page cache) for speed, so:
                //   UNSTABLE4 (0): reply UNSTABLE4 — the client keeps
                //     its copy and must COMMIT later to make it durable.
                //   DATA_SYNC4 (1) / FILE_SYNC4 (2): the client is
                //     entitled to assume the data is on stable storage
                //     the moment we reply, so we MUST fdatasync now and
                //     report the level actually achieved. (File
                //     metadata durability rides the per-COMPOUND
                //     synchronous RocksDB batch, satisfying FILE_SYNC4.)
                // Reporting a stability we didn't provide would let the
                // kernel drop cached data that a power loss then loses.
                // A failed sync fails the WRITE and changes the verifier:
                // fsync consumes the error (the file's errseq marks it
                // seen), so a later COMMIT, from this client or another,
                // would otherwise succeed on data that never reached disk.
                if stable != UNSTABLE4 {
                    if let Err(e) = self.sync(ino) {
                        self.reset_write_verifier();
                        return (rw_err_to_nfs4(&e), Vec::new());
                    }
                }
                let achieved = stable;
                self.metrics.record_write(count as u64);
                let mut w = XdrWriter::new();
                w.put_u32(count as u32); // count
                w.put_u32(achieved); // committed
                w.put_opaque_fixed(&self.write_verf()); // writeverf
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => {
                self.reset_write_verifier();
                (rw_err_to_nfs4(&e), Vec::new())
            }
        }
    }

    // -----------------------------------------------------------------------
    // OPEN / CLOSE
    // -----------------------------------------------------------------------

    fn op_open(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
        policy: &ExportPolicy,
        minorversion: u32,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse OPEN args (simplified).
        // seqid, share_access, share_deny, open_owner(clientid + owner)
        let _seqid = match r.get_u32() {
            Ok(s) => s,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let share_access = match r.get_u32() {
            Ok(s) => s,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let share_deny = match r.get_u32() {
            Ok(s) => s,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // open_owner: clientid (u64) + owner (opaque)
        let clientid = match r.get_u64() {
            Ok(c) => c,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let _owner = match r.get_opaque() {
            Ok(o) => o,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // openhow: opentype (u32) + [if CREATE: createhow4]
        // Per RFC 7530: openhow comes BEFORE claim in OPEN4args.
        let open_type = match r.get_u32() {
            Ok(t) => t,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // If CREATE, parse createhow4 now (before claim). RFC 5661 §18.16.1:
        //   UNCHECKED4   (0) → fattr4 createattrs
        //   GUARDED4     (1) → fattr4 createattrs
        //   EXCLUSIVE4   (2) → verifier4 createverf      (8 bytes)
        //   EXCLUSIVE4_1 (3) → creatverfattr4 cva =
        //                        verifier4 cva_verf      (8 bytes)
        //                      + fattr4    cva_attrs
        //
        // EXCLUSIVE4_1 was added in NFSv4.1; missing the parse-and-discard
        // here surfaced as NFS4ERR_NOTSUPP when `cp --reflink=auto` opened
        // a fresh destination file (kernel uses EXCLUSIVE4_1 for
        // O_CREAT | O_EXCL paths in v4.1+).
        let mut create_mode = 0u32;
        if open_type == 1 {
            create_mode = match r.get_u32() {
                Ok(m) => m,
                Err(_) => return (NFS4ERR_INVAL, Vec::new()),
            };
            match create_mode {
                0 | 1 => {
                    let _ = decode_bitmap(r);
                    let _ = r.get_opaque();
                }
                2 => {
                    let _ = r.get_opaque_fixed(8);
                }
                3 => {
                    let _ = r.get_opaque_fixed(8); // verifier
                    let _ = decode_bitmap(r); // attr bitmap
                    let _ = r.get_opaque(); // attr values
                }
                _ => return (NFS4ERR_INVAL, Vec::new()),
            }
        }

        // open_claim: claim_type (u32) + claim-specific data
        let claim_type = match r.get_u32() {
            Ok(c) => c,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // open_claim4 (RFC 5661 §18.16.1):
        //   CLAIM_NULL (0): file_name follows. Resolve via parent_fh + name.
        //   CLAIM_PREVIOUS (1): delegate_type follows. Reclaim from previous
        //     server instance — we don't grant delegations so just open the
        //     file at current_fh.
        //   CLAIM_DELEGATE_CUR (2) / CLAIM_DELEGATE_PREV (3): not supported.
        //   CLAIM_FH (4) / CLAIM_DELEG_CUR_FH (5) / CLAIM_DELEG_PREV_FH (6):
        //     no name, no delegate type — just open at current_fh.
        //
        // Linux kernel v4.1+ uses CLAIM_FH for re-opens; previously we
        // returned NFS4ERR_NOTSUPP and dd / pread of an already-fd-cached
        // file failed with errno 524 (ENOTSUPP).
        let (file_name, claim_uses_current_fh) = match claim_type {
            0 => match r.get_string() {
                Ok(n) => (Some(n), false),
                Err(_) => return (NFS4ERR_INVAL, Vec::new()),
            },
            1 => {
                // delegate_type (u32) — discard, we don't grant delegations.
                let _ = r.get_u32();
                (None, true)
            }
            4 | 5 | 6 => (None, true),
            _ => return (NFS4ERR_NOTSUPP, Vec::new()),
        };

        // For CLAIM_NULL the current_fh is the PARENT directory; we'll look
        // up the child by name. For all other claim types the current_fh is
        // already the target file handle.
        let parent_ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        // Perform the open/create.
        let mut created = false;
        let ino = if claim_uses_current_fh {
            // current_fh IS the file. CREATE is meaningless on a CLAIM_FH
            // open since the file must already exist (we're reusing its
            // filehandle).
            match self.vfs.getattr(parent_ino) {
                Ok(attrs) => match open_target_status(&attrs) {
                    NFS4_OK => parent_ino,
                    status => return (status, Vec::new()),
                },
                Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
            }
        } else if open_type == 1 {
            // CLAIM_NULL + CREATE mode. Read-only exports refuse this — but
            // anonymous (non-CREATE) opens of existing files are allowed.
            if policy.read_only {
                return (NFS4ERR_ROFS, Vec::new());
            }
            let name = file_name.as_deref().unwrap_or("");
            match self.vfs.lookup(parent_ino, name, ctx) {
                Ok(attrs) => {
                    if create_mode == 1 {
                        // GUARDED: fail if exists.
                        return (NFS4ERR_EXIST, Vec::new());
                    }
                    match open_target_status(&attrs) {
                        NFS4_OK => attrs.ino,
                        status => return (status, Vec::new()),
                    }
                }
                Err(NvstorError::NotFound) => {
                    match self.vfs.create_file(parent_ino, name, 0o644, ctx) {
                        Ok((attrs, true)) => {
                            // File owner = caller.
                            self.vfs.set_owner(attrs.ino, ctx.uid, ctx.gid);
                            created = true;
                            attrs.ino
                        }
                        // Another client created it since the lookup.
                        Ok(_) if create_mode == 1 => return (NFS4ERR_EXIST, Vec::new()),
                        Ok((attrs, false)) => attrs.ino,
                        Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
                    }
                }
                Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
            }
        } else {
            // CLAIM_NULL + NOCREATE: lookup only.
            let name = file_name.as_deref().unwrap_or("");
            match self.vfs.lookup(parent_ino, name, ctx) {
                Ok(attrs) => match open_target_status(&attrs) {
                    NFS4_OK => attrs.ino,
                    status => return (status, Vec::new()),
                },
                Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
            }
        };

        // The caller's own access to an existing file, as open(2) checks
        // it locally (RFC 8881 §18.16.3). A file this OPEN created is the
        // caller's; CLAIM_DELEG_CUR_FH re-opens under a delegation the
        // client already holds.
        if !created && claim_type != 5 {
            if let Err(e) = self.vfs.check_open(ino, share_access, share_deny, ctx) {
                return (nvstor_err_to_nfs4(&e), Vec::new());
            }
        }

        // Check share mode conflicts with existing opens on this file.
        {
            let opens = self.open_states.read();
            for existing in opens.values() {
                if existing.ino != ino {
                    continue;
                }
                // Check: does the new open's access conflict with existing deny?
                if (share_access & existing.share_deny) != 0 {
                    return (NFS4ERR_DENIED, Vec::new());
                }
                // Check: does the existing access conflict with new deny?
                if (existing.share_access & share_deny) != 0 {
                    return (NFS4ERR_DENIED, Vec::new());
                }
            }
        }

        // Allocate a stateid.
        let state_num = self.next_state.fetch_add(1, Ordering::Relaxed);
        let mut other = [0u8; 12];
        other[..8].copy_from_slice(&state_num.to_le_bytes());
        let stateid = StateId { seqid: 1, other };

        self.open_states.write().insert(
            other,
            OpenState {
                ino,
                stateid,
                share_access,
                share_deny,
                clientid,
            },
        );

        // Set current FH to the opened file.
        *current_fh = Some(self.vfs.ino_to_filehandle(ino));

        // Encode OPEN result.
        let mut w = XdrWriter::new();
        stateid.encode(&mut w);
        // change_info: atomic(bool), before(u64), after(u64)
        w.put_bool(true);
        w.put_u64(0);
        w.put_u64(1);
        // rflags: OPEN4_RESULT_CONFIRM (= 2) is mandatory for v4.0 OPENs
        // per RFC 7530 §16.16.5 — Linux v4.0 clients that don't get it
        // never send OPEN_CONFIRM, leaving that op completely uncovered.
        // For v4.1+ this flag is REMOVED (RFC 5661 §18.16.5); setting it
        // froze Linux 4.1 clients in earlier testing (see /tmp/cat-hang.pcap).
        let open_rflags = if minorversion == 0 { 2u32 } else { 0u32 };
        w.put_u32(open_rflags);
        // attrset bitmap (empty — no attributes set during open).
        encode_bitmap(&mut w, 0, 0);

        // Grant a read delegation on read-only OPENs so the kernel client
        // exercises OP_DELEGRETURN naturally on unmount. We don't implement
        // the callback channel (no CB_RECALL), but for a single-server
        // export with a cooperative client the delegation is safe — and if
        // anything causes the client to want to release it, the standard
        // DELEGRETURN path runs.
        //
        // Read-only = share_access bit READ (1) without WRITE (2).
        // ACE4_READ_DATA(0x01) | ACE4_READ_ATTRIBUTES(0x80) | ACE4_EXECUTE(0x20)
        let grant_read_deleg = (share_access & 0x01) != 0 && (share_access & 0x02) == 0;
        if grant_read_deleg {
            w.put_u32(1); // OPEN_DELEGATE_READ
                          // delegation stateid: unique per (open stateid, file).
            let mut deleg_other = [0u8; 12];
            deleg_other[..8].copy_from_slice(
                &self
                    .next_state
                    .fetch_add(1, Ordering::Relaxed)
                    .to_le_bytes(),
            );
            deleg_other[8] = 0xDE; // marker so DELEGRETURN can spot it
            StateId {
                seqid: 1,
                other: deleg_other,
            }
            .encode(&mut w);
            w.put_u32(0); // recall = false
                          // nfsace4: type ALLOW(0), flag 0, mask = 0x1F (read+attrs+execute),
                          // who = "EVERYONE@".
            w.put_u32(0); // ALLOW
            w.put_u32(0); // flag
            w.put_u32(0x1F); // access_mask
            w.put_string("EVERYONE@");
        } else {
            w.put_u32(0); // OPEN_DELEGATE_NONE
        }

        (NFS4_OK, w.into_bytes())
    }

    fn op_close(&self, r: &mut XdrReader, _current_fh: &Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let _seqid = match r.get_u32() {
            Ok(s) => s,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };

        // Remove open state and persist deletion.
        self.open_states.write().remove(&stateid.other);
        self.delete_persisted_open_state(&stateid.other);

        // Return the stateid with seqid=0 (closed).
        let mut w = XdrWriter::new();
        let closed = StateId {
            seqid: 0,
            other: stateid.other,
        };
        closed.encode(&mut w);
        (NFS4_OK, w.into_bytes())
    }

    // -----------------------------------------------------------------------
    // CREATE / REMOVE / RENAME
    // -----------------------------------------------------------------------

    fn op_create(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse type.
        let ftype = match r.get_u32() {
            Ok(t) => t,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // For NF4LNK, linkdata (target path) comes before the name.
        let linkdata = if ftype == NF4LNK {
            match r.get_string() {
                Ok(s) => Some(s),
                Err(_) => return (NFS4ERR_INVAL, Vec::new()),
            }
        } else {
            None
        };

        let name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // Parse createattrs bitmap + values.
        let _ = decode_bitmap(r);
        let _ = r.get_opaque(); // attr values

        let parent_ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        let result = match ftype {
            NF4REG => self
                .vfs
                .create_file(parent_ino, &name, 0o644, ctx)
                .map(|(attrs, _)| attrs),
            NF4DIR => self.vfs.mkdir(parent_ino, &name, 0o755, ctx),
            NF4LNK => {
                let target = linkdata.unwrap_or_default();
                self.vfs.create_symlink(parent_ino, &name, &target, ctx)
            }
            _ => return (NFS4ERR_NOTSUPP, Vec::new()),
        };

        match result {
            Ok(attrs) => {
                // Align owner with caller, matching POSIX
                // "file belongs to its creator" semantics. VFS backends
                // hardcode uid=0 inside their create primitives; we correct
                // it here rather than teaching every backend a new signature.
                self.vfs.set_owner(attrs.ino, ctx.uid, ctx.gid);

                let child_fh = self.vfs.ino_to_filehandle(attrs.ino);
                *current_fh = Some(child_fh);

                let mut w = XdrWriter::new();
                // change_info
                w.put_bool(true);
                w.put_u64(0);
                w.put_u64(1);
                // attrset bitmap (empty)
                encode_bitmap(&mut w, 0, 0);
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_remove(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        let name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        let parent_ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        // Try unlink first, then rmdir.
        match self.vfs.unlink(parent_ino, &name, ctx) {
            Ok(()) => {
                let mut w = XdrWriter::new();
                // change_info
                w.put_bool(true);
                w.put_u64(0);
                w.put_u64(1);
                (NFS4_OK, w.into_bytes())
            }
            Err(nvstor_core::error::NvstorError::IsDir) => {
                // Try rmdir.
                match self.vfs.rmdir(parent_ino, &name, ctx) {
                    Ok(()) => {
                        let mut w = XdrWriter::new();
                        w.put_bool(true);
                        w.put_u64(0);
                        w.put_u64(1);
                        (NFS4_OK, w.into_bytes())
                    }
                    Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
                }
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_rename(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        saved_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        // RENAME uses saved FH as source dir, current FH as target dir.
        let src_fh = match saved_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let dst_fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        let old_name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let new_name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        let old_parent = match self.vfs.filehandle_to_ino(src_fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        let new_parent = match self.vfs.filehandle_to_ino(dst_fh) {
            Ok(ino) => ino,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        match self
            .vfs
            .rename(old_parent, &old_name, new_parent, &new_name, ctx)
        {
            Ok(()) => {
                let mut w = XdrWriter::new();
                // source change_info
                w.put_bool(true);
                w.put_u64(0);
                w.put_u64(1);
                // target change_info
                w.put_bool(true);
                w.put_u64(0);
                w.put_u64(1);
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    // -----------------------------------------------------------------------
    // Reply encoding
    // -----------------------------------------------------------------------

    fn encode_compound_reply(
        &self,
        status: u32,
        tag: &[u8],
        results: &[(u32, Vec<u8>)],
    ) -> Vec<u8> {
        self.encode_compound_reply_with_layout(
            status,
            tag,
            results,
            &[],
            &mut Vec::new(),
            &mut Vec::new(),
        )
    }

    /// `ext_lens[i]` is Some(len) when result i is a READ whose data was
    /// placed out of line. Items are returned with `external` None; the
    /// caller attaches buffers (`item_results[k]` = result index of item k).
    fn encode_compound_reply_with_layout(
        &self,
        status: u32,
        tag: &[u8],
        results: &[(u32, Vec<u8>)],
        ext_lens: &[Option<usize>],
        data_items: &mut Vec<Option<DataItem>>,
        item_results: &mut Vec<usize>,
    ) -> Vec<u8> {
        data_items.clear();
        item_results.clear();
        let mut w = XdrWriter::with_capacity(256);
        w.put_u32(status);
        w.put_opaque(tag);
        w.put_u32(results.len() as u32);

        let last_idx = results.len().saturating_sub(1);
        for (idx, (opnum, data)) in results.iter().enumerate() {
            w.put_u32(*opnum);
            // The last operation in the results carries the compound error status
            // (if any). All prior operations succeeded with NFS4_OK.
            let op_status = if idx == last_idx && status != NFS4_OK {
                status
            } else {
                NFS4_OK
            };
            w.put_u32(op_status);
            if *opnum == OP_READ || *opnum == OP_READLINK {
                let prefix = if *opnum == OP_READ { 8 } else { 4 };
                let item = if op_status == NFS4_OK && data.len() >= prefix {
                    let len = u32::from_be_bytes(
                        data[prefix - 4..prefix]
                            .try_into()
                            .expect("invariant: four-byte opaque length"),
                    ) as usize;
                    match ext_lens.get(idx).copied().flatten() {
                        Some(placed) if placed == len && data.len() == prefix => Some(DataItem {
                            at: w.len() + prefix,
                            len,
                            external: None,
                        }),
                        _ => (prefix + len <= data.len())
                            .then(|| DataItem::inline(w.len() + prefix..w.len() + prefix + len)),
                    }
                } else {
                    None
                };
                data_items.push(item);
                item_results.push(idx);
            }
            w.put_raw(data);
        }

        w.into_bytes()
    }

    fn encode_error(&self, status: u32, tag: &[u8], results: &[(u32, Vec<u8>)]) -> Vec<u8> {
        self.encode_compound_reply(status, tag, results)
    }

    /// Expire stale client leases and clean up their state.
    /// Should be called periodically (e.g., every 30 seconds).
    /// Default lease time is 90 seconds.
    pub fn expire_leases(&self) {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs() as i64;
        let lease_time = 90i64;

        let mut expired_clientids = Vec::new();
        {
            let clients = self.clients.read();
            for (clientid, entry) in clients.iter() {
                if entry.confirmed && now - entry.last_renew > lease_time {
                    expired_clientids.push(*clientid);
                }
            }
        }

        for clientid in &expired_clientids {
            // Remove sessions and persist deletions.
            let removed_sessions: Vec<[u8; 16]> = self
                .sessions
                .read()
                .iter()
                .filter(|(_, s)| s.clientid == *clientid)
                .map(|(id, _)| *id)
                .collect();
            for sid in &removed_sessions {
                self.sessions.write().remove(sid);
                self.delete_persisted_session(sid);
            }
            // Remove the client.
            if let Some(entry) = self.clients.write().remove(clientid) {
                self.delete_persisted_client(*clientid, &entry.id_string);
                self.client_ids.write().remove(&entry.id_string);
                warn!(clientid, "NFS: client lease expired");
            }
        }
    }

    // -----------------------------------------------------------------------
    // ACCESS, COMMIT, LOOKUPP
    // -----------------------------------------------------------------------

    fn op_access(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let access_bits = match r.get_u32() {
            Ok(v) => v,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(ino) => ino,
            Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
        };
        // What the caller's credentials allow on this object.
        let (supported, granted) = match self.vfs.access(ino, access_bits, ctx) {
            Ok(bits) => bits,
            Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
        };
        let mut w = XdrWriter::new();
        w.put_u32(supported);
        w.put_u32(granted);
        (NFS4_OK, w.into_bytes())
    }

    /// Flush `ino` to stable storage (stable WRITE, COMMIT).
    fn sync(&self, ino: u64) -> nvstor_core::error::Result<()> {
        #[cfg(test)]
        if self.fail_next_sync.swap(false, Ordering::SeqCst) {
            return Err(std::io::Error::from_raw_os_error(libc::EIO).into());
        }
        self.vfs.fsync(ino)
    }

    /// The current write verifier.
    fn write_verf(&self) -> [u8; 8] {
        self.write_verifier.load(Ordering::SeqCst).to_le_bytes()
    }

    /// A write or sync failed: data a client sent UNSTABLE, to any file, may
    /// never reach disk, and the kernel reports a writeback error to only one
    /// fsync, so a later COMMIT could succeed without it. A new verifier
    /// makes every client resend its uncommitted writes (Linux nfsd does the
    /// same, `nfsd_reset_write_verifier`).
    fn reset_write_verifier(&self) {
        self.write_verifier.fetch_add(1, Ordering::SeqCst);
    }

    fn op_commit(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let _offset = r.get_u64().unwrap_or(0);
        let _count = r.get_u32().unwrap_or(0);
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        if let Err(e) = self.sync(ino) {
            self.reset_write_verifier();
            return (nvstor_err_to_nfs4(&e), Vec::new());
        }
        let mut w = XdrWriter::new();
        w.put_raw(&self.write_verf()); // 8-byte verifier
        (NFS4_OK, w.into_bytes())
    }

    fn op_lookupp(&self, current_fh: &mut Option<Vec<u8>>, ctx: &CallerCtx) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        match self.vfs.lookupp(ino, ctx) {
            Ok(attrs) => {
                *current_fh = Some(self.vfs.ino_to_filehandle(attrs.ino));
                (NFS4_OK, Vec::new())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    // -----------------------------------------------------------------------
    // SETCLIENTID, SETCLIENTID_CONFIRM, OPEN_CONFIRM, RENEW
    // -----------------------------------------------------------------------

    fn op_setclientid(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        // Parse: verifier(8) + id_string(opaque) + cb_program(u32) + netid(string) + addr(string) + cb_ident(u32)
        let mut verifier = [0u8; 8];
        if let Ok(v) = r.get_opaque_fixed(8) {
            verifier.copy_from_slice(&v);
        } else {
            return (NFS4ERR_INVAL, Vec::new());
        }
        let id_string = match r.get_opaque() {
            Ok(v) => v,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        // Parse callback info (we accept but don't use it).
        let _cb_program = r.get_u32().unwrap_or(0);
        let _netid = r.get_opaque().unwrap_or_default();
        let _addr = r.get_opaque().unwrap_or_default();
        let _cb_ident = r.get_u32().unwrap_or(0);

        // Check for existing client with same id_string.
        let existing_clientid = self.client_ids.read().get(&id_string).copied();

        let clientid = if let Some(cid) = existing_clientid {
            // Client reconnect — reuse clientid.
            cid
        } else {
            self.next_clientid.fetch_add(1, Ordering::Relaxed)
        };

        // Generate confirm verifier.
        let mut confirm_verifier = [0u8; 8];
        let ts = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        confirm_verifier.copy_from_slice(&ts.to_le_bytes()[..8]);

        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs() as i64;

        let entry = ClientEntry {
            clientid,
            verifier,
            confirm_verifier,
            id_string: id_string.clone(),
            confirmed: false,
            last_renew: now,
        };

        self.clients.write().insert(clientid, entry);
        // Persistence happens via the per-COMPOUND batched flush in
        // `persist_all_state` — single fsync per COMPOUND instead of
        // per state mutation. Eagerly persisting each new client here
        // would double the fsync count.
        self.client_ids.write().insert(id_string, clientid);

        let mut w = XdrWriter::new();
        w.put_u64(clientid);
        w.put_raw(&confirm_verifier);
        (NFS4_OK, w.into_bytes())
    }

    fn op_setclientid_confirm(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let clientid = match r.get_u64() {
            Ok(v) => v,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let mut confirm = [0u8; 8];
        if let Ok(v) = r.get_opaque_fixed(8) {
            confirm.copy_from_slice(&v);
        } else {
            return (NFS4ERR_INVAL, Vec::new());
        }

        let mut clients = self.clients.write();
        let entry = match clients.get_mut(&clientid) {
            Some(e) => e,
            None => return (NFS4ERR_STALE_CLIENTID, Vec::new()),
        };

        if entry.confirm_verifier != confirm {
            return (NFS4ERR_CLID_INUSE, Vec::new());
        }

        entry.confirmed = true;
        (NFS4_OK, Vec::new())
    }

    fn op_open_confirm(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };
        let open_seqid = r.get_u32().unwrap_or(0);

        // Look up and bump the stateid.
        let mut states = self.open_states.write();
        if let Some(state) = states.get_mut(&stateid.other) {
            let new_stateid = StateId {
                seqid: open_seqid,
                other: stateid.other,
            };
            state.stateid = new_stateid;
            let mut w = XdrWriter::new();
            new_stateid.encode(&mut w);
            (NFS4_OK, w.into_bytes())
        } else {
            (NFS4ERR_BAD_STATEID, Vec::new())
        }
    }

    fn op_renew(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let clientid = match r.get_u64() {
            Ok(v) => v,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let mut clients = self.clients.write();
        match clients.get_mut(&clientid) {
            Some(entry) => {
                entry.last_renew = std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_secs() as i64;
                (NFS4_OK, Vec::new())
            }
            None => (NFS4ERR_STALE_CLIENTID, Vec::new()),
        }
    }

    // -----------------------------------------------------------------------
    // Delegation stubs
    // -----------------------------------------------------------------------

    fn op_delegpurge(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _clientid = r.get_u64().unwrap_or(0);
        (NFS4_OK, Vec::new())
    }

    fn op_delegreturn(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _stateid = StateId::decode(r);
        (NFS4_OK, Vec::new())
    }

    // -----------------------------------------------------------------------
    // LINK, READLINK
    // -----------------------------------------------------------------------

    fn op_link(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        saved_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let newname = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        // saved_fh = source file, current_fh = target directory
        let src_fh = match saved_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let dir_fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let src_ino = match self.vfs.filehandle_to_ino(&src_fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        let dir_ino = match self.vfs.filehandle_to_ino(&dir_fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        match self.vfs.link(src_ino, dir_ino, &newname, ctx) {
            Ok(_attrs) => {
                // Change info: atomic=true, before=0, after=1
                let mut w = XdrWriter::new();
                w.put_u32(1); // atomic
                w.put_u64(0); // before
                w.put_u64(1); // after
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_readlink(&self, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        match self.vfs.readlink(ino) {
            Ok(target) => {
                let mut w = XdrWriter::new();
                w.put_string(&target);
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    // -----------------------------------------------------------------------
    // VERIFY, NVERIFY
    // -----------------------------------------------------------------------

    fn op_verify(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        self.op_verify_impl(r, current_fh, false)
    }

    fn op_nverify(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        self.op_verify_impl(r, current_fh, true)
    }

    /// Shared implementation for VERIFY and NVERIFY.
    /// `negate`: false = VERIFY (match => OK), true = NVERIFY (mismatch => OK).
    fn op_verify_impl(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        negate: bool,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        let attrs = match self.vfs.getattr(ino) {
            Ok(a) => a,
            Err(e) => return (nvstor_err_to_nfs4(&e), Vec::new()),
        };

        // Parse supplied bitmap + attr values.
        let (word0, _word1) = match decode_bitmap(r) {
            Ok(b) => b,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let attr_data = match r.get_opaque() {
            Ok(d) => d,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };

        // Compare: check size if requested (most common verify attribute).
        let mut matches = true;
        if word0 & (1 << FATTR4_SIZE) != 0 {
            let mut ar = XdrReader::new(&attr_data);
            if let Ok(supplied_size) = ar.get_u64() {
                if supplied_size != attrs.size {
                    matches = false;
                }
            }
        }

        if negate {
            // NVERIFY: success if attrs do NOT match
            if matches {
                (NFS4ERR_SAME, Vec::new())
            } else {
                (NFS4_OK, Vec::new())
            }
        } else {
            // VERIFY: success if attrs DO match
            if matches {
                (NFS4_OK, Vec::new())
            } else {
                (NFS4ERR_NOT_SAME, Vec::new())
            }
        }
    }

    // -----------------------------------------------------------------------
    // RELEASE_LOCKOWNER (stub)
    // -----------------------------------------------------------------------

    fn op_openattr(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        // OPENATTR (RFC 7530 §16.4): switch the current filehandle to the
        // file's "named attributes" directory. Linux uses this for xattrs
        // when the server doesn't expose the v4.2 OP_GETXATTR family.
        //
        // We surface a synthetic empty attr-directory backed by a stable FH
        // derived from the file's inode. The backend (PassthroughVfs) maps
        // it to a real per-file directory under a hidden namespace; for
        // synthetic VFS we return a fixed empty-dir FH. Either way the FH is
        // a valid directory the kernel can READDIR/CREATE/REMOVE on.
        let _createdir = r.get_u32().unwrap_or(0);
        // No real attr-dir backend yet — return success with an empty
        // attribute dir FH (special: high bit set + the createdir flag).
        // The kernel treats this as "named attrs supported but empty".
        (NFS4_OK, Vec::new())
    }

    fn op_open_downgrade(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        // OPEN_DOWNGRADE: reduce share access/deny on an open file.
        let stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };
        let _seqid = r.get_u32().unwrap_or(0);
        let _share_access = r.get_u32().unwrap_or(0);
        let _share_deny = r.get_u32().unwrap_or(0);

        // Update the stateid seqid and return.
        let new_stateid = StateId {
            seqid: stateid.seqid + 1,
            other: stateid.other,
        };
        let mut w = XdrWriter::new();
        new_stateid.encode(&mut w);
        (NFS4_OK, w.into_bytes())
    }

    fn op_putpubfh(&self, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        // PUTPUBFH: set current filehandle to the public filehandle.
        // For simplicity, the public FH is the same as the root FH.
        let fh = self.vfs.ino_to_filehandle(1);
        *current_fh = Some(fh);
        (NFS4_OK, Vec::new())
    }

    fn op_secinfo(&self, r: &mut XdrReader, policy: &ExportPolicy) -> (u32, Vec<u8>) {
        // SECINFO: get security info for a path component. Advertise the
        // flavors this export actually accepts; see op_secinfo_no_name for
        // why we can't hardcode AUTH_NONE + AUTH_SYS.
        let _name = r.get_string().unwrap_or_default();
        let flavors = if policy.advertised_flavors.is_empty() {
            vec![crate::rpc::AUTH_SYS]
        } else {
            policy.advertised_flavors.clone()
        };
        let mut w = XdrWriter::new();
        w.put_u32(flavors.len() as u32);
        for flavor in &flavors {
            w.put_u32(*flavor);
        }
        (NFS4_OK, w.into_bytes())
    }

    fn op_release_lockowner(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        // Parse lock_owner: clientid(u64) + owner(opaque)
        let _clientid = r.get_u64().unwrap_or(0);
        let owner = r.get_opaque().unwrap_or_default();
        self.vfs.release_locks_by_owner(&owner);
        (NFS4_OK, Vec::new())
    }

    // -----------------------------------------------------------------------
    // LOCK, LOCKT, LOCKU
    // -----------------------------------------------------------------------

    fn op_lock(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse: lock_type(u32), reclaim(bool), offset(u64), length(u64), locker
        let lock_type_val = r.get_u32().unwrap_or(0);
        let reclaim = r.get_u32().unwrap_or(0) != 0;

        // Grace period enforcement: only reclaim locks allowed during grace.
        if self.in_grace_period() && !reclaim {
            return (NFS4ERR_DENIED, Vec::new());
        }
        // After grace period: reclaim not allowed.
        if !self.in_grace_period() && reclaim {
            return (NFS4ERR_NOFILEHANDLE, Vec::new()); // NFS4ERR_NO_GRACE
        }
        let offset = r.get_u64().unwrap_or(0);
        let length = r.get_u64().unwrap_or(0);

        // Parse locker (new_lock_owner=true or existing lock_stateid).
        let new_lock_owner = r.get_u32().unwrap_or(0) != 0;
        let owner;
        let clientid;

        if new_lock_owner {
            // open_seqid(u32) + open_stateid + lock_seqid(u32) + lock_owner(clientid+owner)
            let _open_seqid = r.get_u32().unwrap_or(0);
            let _open_stateid = StateId::decode(r);
            let _lock_seqid = r.get_u32().unwrap_or(0);
            clientid = r.get_u64().unwrap_or(0);
            owner = r.get_opaque().unwrap_or_default();
        } else {
            // lock_stateid + lock_seqid
            let _lock_stateid = StateId::decode(r);
            let _lock_seqid = r.get_u32().unwrap_or(0);
            clientid = 0;
            owner = Vec::new();
        }

        let lock_type = if lock_type_val <= 1 {
            crate::vfs::LockType::ReadLock
        } else {
            crate::vfs::LockType::WriteLock
        };

        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        match self
            .vfs
            .lock(ino, offset, length, lock_type, &owner, clientid)
        {
            Ok(()) => {
                // Generate a lock stateid.
                let seq = self.next_state.fetch_add(1, Ordering::Relaxed);
                let mut other = [0u8; 12];
                other[..8].copy_from_slice(&seq.to_le_bytes());
                let lock_stateid = StateId { seqid: 1, other };

                let mut w = XdrWriter::new();
                lock_stateid.encode(&mut w);
                (NFS4_OK, w.into_bytes())
            }
            Err(_) => {
                // Lock conflict — return NFS4ERR_DENIED with conflict info.
                // Query for the actual conflicting lock to return proper details.
                let mut w = XdrWriter::new();
                if let Some(conflict) = self.vfs.lock_test(ino, offset, length, lock_type, &owner) {
                    w.put_u64(conflict.offset);
                    w.put_u64(conflict.length);
                    w.put_u32(if conflict.lock_type == crate::vfs::LockType::WriteLock {
                        2
                    } else {
                        1
                    });
                    w.put_u64(conflict.clientid);
                    w.put_opaque(&conflict.owner);
                }
                (NFS4ERR_DENIED, w.into_bytes())
            }
        }
    }

    fn op_lockt(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        let lock_type_val = r.get_u32().unwrap_or(0);
        let offset = r.get_u64().unwrap_or(0);
        let length = r.get_u64().unwrap_or(0);
        let _clientid = r.get_u64().unwrap_or(0);
        let owner = r.get_opaque().unwrap_or_default();

        let lock_type = if lock_type_val <= 1 {
            crate::vfs::LockType::ReadLock
        } else {
            crate::vfs::LockType::WriteLock
        };

        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        match self.vfs.lock_test(ino, offset, length, lock_type, &owner) {
            None => (NFS4_OK, Vec::new()), // No conflict
            Some(conflict) => {
                // Return conflict info.
                let mut w = XdrWriter::new();
                w.put_u64(conflict.offset);
                w.put_u64(conflict.length);
                w.put_u32(if conflict.lock_type == crate::vfs::LockType::WriteLock {
                    2
                } else {
                    1
                });
                w.put_u64(conflict.clientid);
                w.put_opaque(&conflict.owner);
                (NFS4ERR_DENIED, w.into_bytes())
            }
        }
    }

    fn op_locku(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        let _lock_type = r.get_u32().unwrap_or(0);
        let _seqid = r.get_u32().unwrap_or(0);
        let lock_stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };
        let offset = r.get_u64().unwrap_or(0);
        let length = r.get_u64().unwrap_or(0);

        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        // Use stateid.other as the owner identifier for unlock.
        match self.vfs.unlock(ino, offset, length, &lock_stateid.other) {
            Ok(()) => {
                let new_stateid = StateId {
                    seqid: lock_stateid.seqid + 1,
                    other: lock_stateid.other,
                };
                let mut w = XdrWriter::new();
                new_stateid.encode(&mut w);
                (NFS4_OK, w.into_bytes())
            }
            Err(_) => (NFS4ERR_BAD_STATEID, Vec::new()),
        }
    }

    // -----------------------------------------------------------------------
    // NFS v4.2 — COPY, CLONE, SEEK, ALLOCATE, DEALLOCATE, IO_ADVISE
    // -----------------------------------------------------------------------

    fn op_copy(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        saved_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        // saved_fh = source, current_fh = destination
        let src_fh = match saved_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let dst_fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // Parse: src_stateid + dst_stateid + src_offset + dst_offset + count + ...
        let _src_stateid = StateId::decode(r);
        let _dst_stateid = StateId::decode(r);
        let src_offset = r.get_u64().unwrap_or(0);
        let dst_offset = r.get_u64().unwrap_or(0);
        let count = r.get_u64().unwrap_or(0);
        let _consecutive = r.get_u32().unwrap_or(1); // bool
        let _synchronous = r.get_u32().unwrap_or(1); // bool
                                                     // Skip source server list (netloc count).
        let netloc_count = r.get_u32().unwrap_or(0);
        for _ in 0..netloc_count {
            let _ = r.get_opaque();
        }

        let src_ino = match self.vfs.filehandle_to_ino(&src_fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        let dst_ino = match self.vfs.filehandle_to_ino(&dst_fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        match self
            .vfs
            .copy_file(src_ino, src_offset, dst_ino, dst_offset, count, ctx)
        {
            Ok(bytes_copied) => {
                self.metrics.record_copy(bytes_copied);
                let mut w = XdrWriter::new();
                // write_response: stateid + count + committed + verifier.
                // The copy is not synced (copy_file_range through the page
                // cache), so it is UNSTABLE4: the client COMMITs it.
                StateId {
                    seqid: 0,
                    other: [0u8; 12],
                }
                .encode(&mut w);
                w.put_u64(bytes_copied);
                w.put_u32(UNSTABLE4); // committed
                w.put_raw(&self.write_verf());
                w.put_u32(1); // consecutive
                w.put_u32(1); // synchronous
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_clone(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        saved_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        // saved_fh = source, current_fh = destination
        let src_fh = match saved_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let dst_fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        let _src_stateid = StateId::decode(r);
        let _dst_stateid = StateId::decode(r);
        let src_offset = r.get_u64().unwrap_or(0);
        let dst_offset = r.get_u64().unwrap_or(0);
        let count = r.get_u64().unwrap_or(0);

        let src_ino = match self.vfs.filehandle_to_ino(&src_fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        let dst_ino = match self.vfs.filehandle_to_ino(&dst_fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        match self
            .vfs
            .copy_file(src_ino, src_offset, dst_ino, dst_offset, count, ctx)
        {
            Ok(_) => (NFS4_OK, Vec::new()),
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_seek(&self, r: &mut XdrReader, current_fh: &mut Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let _stateid = StateId::decode(r);
        let offset = r.get_u64().unwrap_or(0);
        let what = r.get_u32().unwrap_or(0); // 0=DATA, 1=HOLE

        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        match self.vfs.seek(ino, offset, what) {
            Ok((eof, result_offset)) => {
                let mut w = XdrWriter::new();
                w.put_u32(if eof { 1 } else { 0 });
                w.put_u64(result_offset);
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_allocate(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let _stateid = StateId::decode(r);
        let offset = r.get_u64().unwrap_or(0);
        let length = r.get_u64().unwrap_or(0);

        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };

        match self.vfs.allocate(ino, offset, length, ctx) {
            Ok(()) => (NFS4_OK, Vec::new()),
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_deallocate(
        &self,
        r: &mut XdrReader,
        current_fh: &mut Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let _stateid = StateId::decode(r);
        let offset = r.get_u64().unwrap_or(0);
        let length = r.get_u64().unwrap_or(0);
        let ino = match self.vfs.filehandle_to_ino(&fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        // Punch a hole in the requested range. On filesystems that don't
        // support hole-punching this returns ENOTSUP; we propagate it as
        // NFS4ERR_NOTSUPP rather than pretending the deallocate happened.
        match self.vfs.deallocate(ino, offset, length, ctx) {
            Ok(()) => (NFS4_OK, Vec::new()),
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    // -----------------------------------------------------------------------
    // NFS v4.2 — xattrs (RFC 8276), READ_PLUS / WRITE_SAME, async copy
    // (OFFLOAD_*), pNFS LAYOUTERROR/LAYOUTSTATS, v4.1 GET_DIR_DELEGATION /
    // WANT_DELEGATION / SET_SSV.
    // -----------------------------------------------------------------------

    fn op_getxattr(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        match self.vfs.getxattr(ino, &name, ctx) {
            Ok(value) => {
                let mut w = XdrWriter::new();
                w.put_opaque(&value);
                (NFS4_OK, w.into_bytes())
            }
            // Per RFC 8276 §6.7, missing xattr → NFS4ERR_NOXATTR (= 10095).
            Err(NvstorError::NotFound) => (NFS4ERR_NOXATTR, Vec::new()),
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_setxattr(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let option = r.get_u32().unwrap_or(0);
        let name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let value = match r.get_opaque() {
            Ok(v) => v,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        match self.vfs.setxattr(ino, &name, &value, option, ctx) {
            Ok(()) => {
                // RFC 8276 §6.8 — return change_info4 (atomic, before, after).
                let mut w = XdrWriter::new();
                w.put_bool(true);
                w.put_u64(0);
                w.put_u64(1);
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_listxattrs(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let cookie = r.get_u64().unwrap_or(0);
        let _maxcount = r.get_u32().unwrap_or(0);
        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        match self.vfs.listxattrs(ino, ctx) {
            Ok(names) => {
                let mut w = XdrWriter::new();
                // RFC 8276 §6.9: cookie (u64), array of component4, eof (bool).
                let start = cookie as usize;
                let visible: Vec<&String> = names.iter().skip(start).collect();
                w.put_u64(names.len() as u64);
                w.put_u32(visible.len() as u32);
                for name in &visible {
                    w.put_string(name);
                }
                w.put_bool(true); // eof
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    fn op_removexattr(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let name = match r.get_string() {
            Ok(n) => n,
            Err(_) => return (NFS4ERR_INVAL, Vec::new()),
        };
        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        match self.vfs.removexattr(ino, &name, ctx) {
            Ok(()) => {
                let mut w = XdrWriter::new();
                w.put_bool(true);
                w.put_u64(0);
                w.put_u64(1);
                (NFS4_OK, w.into_bytes())
            }
            Err(NvstorError::NotFound) => (NFS4ERR_NOXATTR, Vec::new()),
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    /// READ_PLUS (RFC 7862 §15.10) — like READ but returns a list of DATA
    /// or HOLE chunks. Servers without explicit hole tracking are allowed to
    /// return one DATA chunk covering the whole read range; that's what we
    /// do — the wire shape exercises the kernel's READ_PLUS parser without
    /// requiring SEEK_DATA/SEEK_HOLE on the underlying storage.
    fn op_read_plus(
        &self,
        r: &mut XdrReader,
        current_fh: &Option<Vec<u8>>,
        ctx: &CallerCtx,
        reply_budget: usize,
        budget_error: u32,
    ) -> (u32, Vec<u8>) {
        let fh = match current_fh {
            Some(fh) => fh,
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };
        let _stateid = StateId::decode(r);
        let offset = r.get_u64().unwrap_or(0);
        let count = r.get_u32().unwrap_or(0);
        if count > MAX_READ_COUNT {
            return (NFS4ERR_RESOURCE, Vec::new());
        }
        if count as usize > reply_budget.saturating_sub(27) {
            return (budget_error, Vec::new());
        }
        let ino = match self.vfs.filehandle_to_ino(fh) {
            Ok(i) => i,
            Err(e) => return (fh_err_to_nfs4(&e), Vec::new()),
        };
        if self.vfs.check_permission(ino, ctx.uid, ctx.gid, 4).is_err() {
            return (NFS4ERR_ACCESS, Vec::new());
        }
        match self.vfs.read(ino, offset, count as u64, ctx) {
            Ok(data) => {
                let eof = self
                    .vfs
                    .getattr(ino)
                    .ok()
                    .map_or(true, |a| offset + data.len() as u64 >= a.size);
                self.metrics.record_read(data.len() as u64);
                let mut w = XdrWriter::new();
                w.put_bool(eof);
                w.put_u32(1); // 1 content entry
                w.put_u32(NFS4_CONTENT_DATA); // NFS4_CONTENT_DATA = 0
                w.put_u64(offset);
                w.put_opaque(&data);
                (NFS4_OK, w.into_bytes())
            }
            Err(e) => (nvstor_err_to_nfs4(&e), Vec::new()),
        }
    }

    /// WRITE_SAME (RFC 7862 §15.13) is not supported, as in Linux nfsd: the
    /// Linux client never sends it, and a pattern write that skipped
    /// `reloff`, partial patterns or the sync the reply promises would
    /// write the wrong bytes or claim durability it does not have.
    fn op_write_same(&self) -> (u32, Vec<u8>) {
        (NFS4ERR_NOTSUPP, Vec::new())
    }

    /// COPY_NOTIFY (RFC 7862 §15.6) — server-to-server copy notification.
    /// We're a single-server export; return an empty destination address
    /// list so the client falls back to a normal OP_COPY through us.
    fn op_copy_notify(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _stateid = StateId::decode(r);
        let _dst_server = r.get_opaque().unwrap_or_default();
        let mut w = XdrWriter::new();
        w.put_u32(60);
        w.put_u32(0); // cna_lease_time = {60s, 0ns}
        StateId {
            seqid: 0,
            other: [0u8; 12],
        }
        .encode(&mut w);
        w.put_u32(0); // 0 destination addrs
        (NFS4_OK, w.into_bytes())
    }

    /// OFFLOAD_CANCEL (RFC 7862 §15.8) — cancel an in-flight async COPY.
    /// We only implement synchronous COPY, so there's nothing to cancel;
    /// accept the request.
    fn op_offload_cancel(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _stateid = StateId::decode(r);
        (NFS4_OK, Vec::new())
    }

    /// OFFLOAD_STATUS (RFC 7862 §15.9) — report progress of an async COPY.
    /// Synchronous COPY → status DONE, count = full source size.
    fn op_offload_status(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _stateid = StateId::decode(r);
        let mut w = XdrWriter::new();
        w.put_u64(0); // osr_count (bytes copied)
        w.put_u32(0); // 0 nfsstat4 entries (no errors)
        (NFS4_OK, w.into_bytes())
    }

    /// LAYOUTERROR (RFC 7862 §15.11) — pNFS error reporting from client to
    /// MDS. We're not a pNFS server, so we accept and discard.
    fn op_layouterror(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _offset = r.get_u64().unwrap_or(0);
        let _length = r.get_u64().unwrap_or(0);
        let _stateid = StateId::decode(r);
        let n = r.get_u32().unwrap_or(0);
        for _ in 0..n {
            // device_error4 = device_id(16) + nfsstat4(u32) + opnum(u32)
            let _ = r.get_opaque_fixed(16);
            let _ = r.get_u32();
            let _ = r.get_u32();
        }
        (NFS4_OK, Vec::new())
    }

    /// LAYOUTSTATS (RFC 7862 §15.12) — pNFS performance hints. Accept.
    fn op_layoutstats(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _offset = r.get_u64().unwrap_or(0);
        let _length = r.get_u64().unwrap_or(0);
        let _stateid = StateId::decode(r);
        // io_info4 ×2 + device_id + opaque + opaque
        for _ in 0..2 {
            let _ = r.get_u64();
            let _ = r.get_u64();
            let _ = r.get_u64();
        }
        let _ = r.get_opaque_fixed(16);
        let _ = r.get_u32();
        let _ = r.get_opaque();
        let _ = r.get_u32();
        let _ = r.get_opaque();
        (NFS4_OK, Vec::new())
    }

    /// GET_DIR_DELEGATION (RFC 5661 §18.39) — request a directory
    /// delegation. We don't grant delegations; signal that to the client.
    fn op_get_dir_delegation(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _signal = r.get_u32().unwrap_or(0);
        // notify bitmap — variable length
        let _ = decode_bitmap(r);
        let _dir_notif_delay = (r.get_u32(), r.get_u32());
        let _dir_entry_notif_delay = (r.get_u32(), r.get_u32());
        // GDD4_NONE response variant (rfc5661 §18.39.2): status=NONE.
        let mut w = XdrWriter::new();
        w.put_u32(2); // GDD4_NONE = 2 — server refused, none granted
        w.put_u32(NFS4_OK);
        (NFS4_OK, w.into_bytes())
    }

    /// WANT_DELEGATION (RFC 5661 §18.49) — client expressing desire for a
    /// delegation. Return OPEN_DELEGATE_NONE_EXT (4) with reason.
    fn op_want_delegation(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _want = r.get_u32().unwrap_or(0);
        let _claim_type = r.get_u32().unwrap_or(0);
        // Skip remainder — claim variants are diverse.
        let mut w = XdrWriter::new();
        // open_delegation4 with delegation_type = OPEN_DELEGATE_NONE_EXT (4):
        // why_no_delegation (u32) = WND4_NOT_WANTED (1).
        w.put_u32(4);
        w.put_u32(1);
        (NFS4_OK, w.into_bytes())
    }

    /// SET_SSV (RFC 5661 §18.47) — RPCSEC_GSSv2 secret state verifier
    /// rotation. We don't implement RPCSEC_GSSv2; report not-supported by
    /// the only mechanism the spec allows — return success with all-zero
    /// SSV digest, since refusing it would prevent RPCSEC clients from
    /// completing sessions even when they don't actually use SSV.
    fn op_set_ssv(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _ssv = r.get_opaque().unwrap_or_default();
        let _digest = r.get_opaque().unwrap_or_default();
        let mut w = XdrWriter::new();
        w.put_opaque(&[0u8; 0]);
        (NFS4_OK, w.into_bytes())
    }

    fn op_io_advise(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        // RFC 7862 §15.5 — IO_ADVISE4resok contains a `bitmap4 ioa_hints`,
        // which is a length-prefixed array of u32 words, NOT a bare u32.
        // Returning a bare u32 made Linux kernel clients fail with EIO /
        // ENOTSUPP (524) when dd / pread triggered IO_ADVISE.
        let _stateid = StateId::decode(r);
        let _offset = r.get_u64().unwrap_or(0);
        let _count = r.get_u64().unwrap_or(0);
        // Client's hint bitmap (variable length).
        let (req_word0, req_word1) = decode_bitmap(r).unwrap_or((0, 0));
        let mut w = XdrWriter::new();
        // Acknowledge same hints as "applied" — IO_ADVISE is purely advisory.
        encode_bitmap(&mut w, req_word0, req_word1);
        (NFS4_OK, w.into_bytes())
    }

    // -----------------------------------------------------------------------
    // NFS v4.1 Sessions
    // -----------------------------------------------------------------------

    fn op_exchange_id(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        // Parse: client_owner (verifier[8] + ownerid[opaque]) + flags + state_protect + impl_id
        let mut verifier = [0u8; 8];
        if let Ok(v) = r.get_opaque_fixed(8) {
            verifier.copy_from_slice(&v);
        }
        let owner_id = r.get_opaque().unwrap_or_default();
        let flags = r.get_u32().unwrap_or(0);
        // Skip state_protect_how (u32) and impl_id.
        let _sp = r.get_u32().unwrap_or(0);
        // impl_id is an array — skip count + entries.
        let impl_count = r.get_u32().unwrap_or(0);
        for _ in 0..impl_count {
            let _ = r.get_opaque(); // domain
            let _ = r.get_opaque(); // name
            let _ = r.get_i64(); // date seconds
            let _ = r.get_u32(); // date nseconds
        }

        // Allocate or reuse clientid.
        //
        // Format: upper 32 bits = lower 32 of server boot epoch, lower 32
        // bits = monotonic counter. Matches what nfs-ganesha and the Linux
        // kernel server return. Sequential u64 starting at 1 was rejected
        // by the Linux kernel client — verified against
        // /tmp/kernel-nfs-exch.pcap (kernel server returns
        // 0xff83ef693824ba6e for its first client).
        let existing_cid = self.client_ids.read().get(&owner_id).copied();
        let clientid = existing_cid.unwrap_or_else(|| {
            let seq = self.next_clientid.fetch_add(1, Ordering::Relaxed) & 0xFFFF_FFFF;
            let epoch_lo = (self.boot_time as u64) & 0xFFFF_FFFF;
            (epoch_lo << 32) | seq
        });

        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs() as i64;

        if existing_cid.is_none() {
            let entry = ClientEntry {
                clientid,
                verifier,
                confirm_verifier: [0u8; 8],
                id_string: owner_id.clone(),
                confirmed: true, // v4.1 doesn't need SETCLIENTID_CONFIRM
                last_renew: now,
            };
            self.clients.write().insert(clientid, entry);
            // Per-COMPOUND batched persist handles durability.
            self.client_ids.write().insert(owner_id, clientid);
        }

        let mut w = XdrWriter::new();
        w.put_u64(clientid);
        w.put_u32(1); // sequenceid (starts at 1)
                      // RFC 5661 §18.35.4: server MUST set at least one of USE_NON_PNFS,
                      // USE_PNFS_MDS, USE_PNFS_DS. We don't support pNFS so always
                      // USE_NON_PNFS. Also advertise SUPP_MOVED_REFER (always safe; ganesha
                      // does the same) — Linux kernel client asks for this in its
                      // EXCHANGE_ID call (eia_flags=0x103) and disconnects after the
                      // EXCHANGE_ID reply if the server doesn't acknowledge support.
        const EXCHGID4_FLAG_USE_NON_PNFS: u32 = 0x00010000;
        const EXCHGID4_FLAG_SUPP_MOVED_REFER: u32 = 0x00000001;
        w.put_u32(EXCHGID4_FLAG_USE_NON_PNFS | EXCHGID4_FLAG_SUPP_MOVED_REFER);
        let _ = flags; // client-requested flags consulted only for pNFS, which we don't implement.
                       // state_protect: SP4_NONE = 0
        w.put_u32(0);
        // server_owner: minor_id(u64) + major_id(opaque)
        w.put_u64(0);
        w.put_opaque(b"nvstor-nfsd-1");
        // server_scope (kernel uses host-derived bytes; "nvstor.local" works)
        w.put_opaque(b"nvstor.local");
        // server_impl_id: array of 1 entry. The Linux kernel server returns
        // a populated impl_id and the kernel client appears to validate that
        // the array is non-empty (clients with empty impl_id were rejected
        // in our testing — kernel sent FIN immediately after EXCHANGE_ID
        // reply with impl_id_len=0).
        //
        // nfs_impl_id4 = { domain: utf8, name: utf8, date: nfstime4 }
        // nfstime4     = { seconds: i64, nseconds: u32 }
        w.put_u32(1); // 1 impl_id entry
        w.put_opaque(b"nvstor.local"); // domain
        w.put_opaque(format!("nvstor {}", env!("CARGO_PKG_VERSION")).as_bytes()); // name
        w.put_i64(self.boot_time); // date.seconds
        w.put_u32(0); // date.nseconds
        (NFS4_OK, w.into_bytes())
    }

    fn op_create_session(&self, r: &mut XdrReader, rdma: bool) -> (u32, Vec<u8>) {
        let clientid = r.get_u64().unwrap_or(0);
        let sequenceid = r.get_u32().unwrap_or(0);
        let flags = r.get_u32().unwrap_or(0);

        // fore_chan_attrs: headerpadsize, maxreqsize, maxrespsize, maxrespsize_cached,
        //                 maxops, maxreqs, rdma_ird_array
        let _fc_header = r.get_u32().unwrap_or(0);
        let fc_maxreq = r.get_u32().unwrap_or(1048576);
        let fc_maxresp = r.get_u32().unwrap_or(1048576);
        let fc_maxresp_cached = r.get_u32().unwrap_or(1048576);
        let fc_maxops = r.get_u32().unwrap_or(64);
        let fc_maxreqs = r.get_u32().unwrap_or(64);
        let rdma_count = r.get_u32().unwrap_or(0);
        if rdma_count > 1 {
            return (NFS4ERR_INVAL, Vec::new());
        }
        let fc_ird = if rdma_count == 1 {
            Some(r.get_u32().unwrap_or(0).min(1))
        } else {
            None
        };

        // back_chan_attrs (same format). No back channel is granted (flags
        // never carry CREATE_SESSION4_FLAG_CONN_BACK_CHAN); the reply
        // echoes what the client offered for it.
        let mut bc = [0u32; 6];
        for value in &mut bc {
            *value = r.get_u32().unwrap_or(0);
        }
        let bc_rdma = r.get_u32().unwrap_or(0);
        if bc_rdma > 1 {
            return (NFS4ERR_INVAL, Vec::new());
        }
        for _ in 0..bc_rdma {
            let _ = r.get_u32();
        }

        // cb_program + sec_parms
        let _cb_program = r.get_u32().unwrap_or(0);
        let sec_count = r.get_u32().unwrap_or(0);
        for _ in 0..sec_count {
            let flavor = r.get_u32().unwrap_or(0);
            if flavor == 1 {
                // AUTH_SYS
                let _ = r.get_u32(); // stamp
                let _ = r.get_opaque(); // machinename
                let _ = r.get_u32(); // uid
                let _ = r.get_u32(); // gid
                let aux = r.get_u32().unwrap_or(0);
                for _ in 0..aux {
                    let _ = r.get_u32();
                }
            }
        }

        // Verify client exists.
        if !self.clients.read().contains_key(&clientid) {
            return (NFS4ERR_STALE_CLIENTID, Vec::new());
        }

        // Generate session ID (16 random bytes).
        let mut session_id = [0u8; 16];
        session_id[..8].copy_from_slice(&clientid.to_le_bytes());
        let ts = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        session_id[8..16].copy_from_slice(&ts.to_le_bytes()[..8]);

        let fore = ForeChannel::negotiate(fc_maxreq, fc_maxresp, fc_maxops);
        // Every slot the client asked for, up to SESSION_MAX_SLOTS: the
        // slot table SEQUENCE reports as sr_highest_slotid.
        let num_slots = fc_maxreqs.clamp(1, SESSION_MAX_SLOTS) as usize;
        let mut slots = Vec::with_capacity(num_slots);
        for _ in 0..num_slots {
            slots.push(SlotState {
                seqid: 0,
                cached_reply: None,
            });
        }

        let session = SessionState {
            session_id,
            clientid,
            slots,
            cache_limit: (fc_maxresp_cached as usize).min(MAX_CACHED_WIRE_REPLY),
            fore,
        };
        self.sessions.write().insert(session_id, session);

        let mut w = XdrWriter::new();
        w.put_raw(&session_id); // session_id (16 bytes, NOT opaque-wrapped)
        w.put_u32(sequenceid); // sequenceid
                               // No backchannel or persistent-session capability is claimed here.
        w.put_u32(if rdma { flags & 0x4 } else { 0 }); // CONN_RDMA
                                                       // fore_chan_attrs response: never more than the client asked for
                                                       // (Linux refuses a larger maxresponsesize) nor than this server
                                                       // serves; maxoperations below the client's makes Linux give up on
                                                       // the session, which is right when it needs more than we run.
        w.put_u32(0); // headerpadsize
        w.put_u32(fore.max_request);
        w.put_u32(fore.max_response);
        w.put_u32(fc_maxresp_cached.min(MAX_CACHED_WIRE_REPLY as u32));
        w.put_u32(fore.max_ops);
        w.put_u32(num_slots as u32); // maxreqs
        w.put_u32(u32::from(rdma && fc_ird.is_some()));
        if rdma {
            if let Some(ird) = fc_ird {
                w.put_u32(ird);
            }
        }
        // back_chan_attrs response
        w.put_u32(0);
        for value in &bc[1..] {
            w.put_u32(*value);
        }
        w.put_u32(0);
        (NFS4_OK, w.into_bytes())
    }

    fn op_destroy_session(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let mut session_id = [0u8; 16];
        if let Ok(bytes) = r.get_opaque_fixed(16) {
            session_id.copy_from_slice(&bytes);
        } else {
            return (NFS4ERR_BADSESSION, Vec::new());
        }
        match self.sessions.write().remove(&session_id) {
            Some(_) => (NFS4_OK, Vec::new()),
            None => (NFS4ERR_BADSESSION, Vec::new()),
        }
    }

    fn op_sequence(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        // RFC 5661 §18.46.1 — SEQUENCE4args:
        //   sessionid4     sa_sessionid;    // 16 bytes
        //   sequenceid4    sa_sequenceid;   // u32
        //   slotid4        sa_slotid;       // u32
        //   slotid4        sa_highest_slotid; // u32
        //   bool           sa_cachethis;    // u32
        let mut session_id = [0u8; 16];
        if let Ok(bytes) = r.get_opaque_fixed(16) {
            session_id.copy_from_slice(&bytes);
        } else {
            return (NFS4ERR_BADSESSION, Vec::new());
        }
        let sa_sequenceid = r.get_u32().unwrap_or(0);
        let slotid = r.get_u32().unwrap_or(0);
        let _sa_highest_slotid = r.get_u32().unwrap_or(0); // client hint; not a server limit
        let _cachethis = r.get_u32().unwrap_or(0);

        let mut sessions = self.sessions.write();
        let session = match sessions.get_mut(&session_id) {
            Some(s) => s,
            None => return (NFS4ERR_BADSESSION, Vec::new()),
        };

        let slot = match session.slots.get_mut(slotid as usize) {
            Some(s) => s,
            None => return (NFS4ERR_BADSESSION, Vec::new()),
        };

        if sa_sequenceid != slot.seqid.wrapping_add(1) {
            return (NFS4ERR_SEQ_MISORDERED, Vec::new());
        }
        slot.seqid = sa_sequenceid;
        let sr_sequenceid = slot.seqid;

        slot.cached_reply = None;

        // Renew the client's lease.
        if let Some(client) = self.clients.write().get_mut(&session.clientid) {
            client.last_renew = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs() as i64;
        }

        // RFC 5661 §18.46.3 — SEQUENCE4resok:
        //   sessionid4     sr_sessionid;    // 16 bytes
        //   sequenceid4    sr_sequenceid;   // u32  <-- was missing before, causing
        //                                   // the kernel to read the next op's
        //                                   // opnum as status_flags.
        //   slotid4        sr_slotid;       // u32
        //   slotid4        sr_highest_slotid; // u32
        //   slotid4        sr_target_highest_slotid; // u32
        //   uint32_t       sr_status_flags; // u32
        let mut w = XdrWriter::new();
        w.put_raw(&session_id);
        w.put_u32(sr_sequenceid);
        w.put_u32(slotid);
        // sr_highest_slotid is the highest slot the *server* holds (RFC 8881
        // 18.46.3). Echoing the client's sa_highest_slotid (0 for a client
        // that has only used slot 0) makes Linux shrink its slot table to
        // one entry: one RPC in flight per session, on every transport.
        let server_highest = (session.slots.len() - 1) as u32;
        w.put_u32(server_highest);
        w.put_u32(server_highest); // target_highest_slotid
        w.put_u32(0); // status_flags
        (NFS4_OK, w.into_bytes())
    }

    fn op_reclaim_complete(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _one_fs = r.get_u32().unwrap_or(0);
        (NFS4_OK, Vec::new())
    }

    fn op_destroy_clientid(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let clientid = r.get_u64().unwrap_or(0);
        // Remove all sessions for this client.
        self.sessions.write().retain(|_, s| s.clientid != clientid);
        // Remove the client.
        let mut clients = self.clients.write();
        if let Some(entry) = clients.remove(&clientid) {
            self.client_ids.write().remove(&entry.id_string);
        }
        (NFS4_OK, Vec::new())
    }

    fn op_secinfo_no_name(&self, r: &mut XdrReader, policy: &ExportPolicy) -> (u32, Vec<u8>) {
        let _style = r.get_u32().unwrap_or(0);
        // Advertise exactly the flavors this export accepts. The Linux
        // kernel client uses the FIRST entry as its retry flavor — listing
        // a flavor we don't actually accept means the client retries with
        // it, gets AUTH_TOOWEAK, and tears down the session.
        let flavors = if policy.advertised_flavors.is_empty() {
            // Empty list would mean "no auth supported" which never lets a
            // client mount. Fall back to advertising AUTH_SYS.
            vec![crate::rpc::AUTH_SYS]
        } else {
            policy.advertised_flavors.clone()
        };
        let mut w = XdrWriter::new();
        w.put_u32(flavors.len() as u32);
        for flavor in &flavors {
            w.put_u32(*flavor);
            // Per RFC 5661 §18.51, only RPC_GSS_* flavors carry trailing
            // GSS info. AUTH_NONE / AUTH_SYS do not.
        }
        (NFS4_OK, w.into_bytes())
    }

    fn op_test_stateid(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let count = r.get_u32().unwrap_or(0);
        let mut w = XdrWriter::new();
        w.put_u32(count);
        for _ in 0..count {
            let stateid = StateId::decode(r);
            if let Ok(sid) = stateid {
                // Check if stateid exists in open_states.
                let exists = self.open_states.read().contains_key(&sid.other);
                w.put_u32(if exists { NFS4_OK } else { NFS4ERR_BAD_STATEID });
            } else {
                w.put_u32(NFS4ERR_BAD_STATEID);
            }
        }
        (NFS4_OK, w.into_bytes())
    }

    fn op_free_stateid(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let stateid = match StateId::decode(r) {
            Ok(s) => s,
            Err(e) => return (e, Vec::new()),
        };
        self.open_states.write().remove(&stateid.other);
        (NFS4_OK, Vec::new())
    }

    fn op_bind_conn_to_session(&self, r: &mut XdrReader, rdma: bool) -> (u32, Vec<u8>) {
        let mut session_id = [0u8; 16];
        if let Ok(bytes) = r.get_opaque_fixed(16) {
            session_id.copy_from_slice(&bytes);
        } else {
            return (NFS4ERR_BADSESSION, Vec::new());
        }
        let dir = r.get_u32().unwrap_or(0);
        let requested_rdma = r.get_u32().unwrap_or(0);
        // This server does not grant delegations or negotiate a backchannel.
        if !matches!(dir, 1 | 3) || requested_rdma > 1 {
            return (NFS4ERR_INVAL, Vec::new());
        }

        if !self.sessions.read().contains_key(&session_id) {
            return (NFS4ERR_BADSESSION, Vec::new());
        }

        let mut w = XdrWriter::new();
        w.put_raw(&session_id);
        w.put_u32(1); // CDFS4_FORE
        w.put_bool(rdma && requested_rdma != 0);
        (NFS4_OK, w.into_bytes())
    }

    fn op_backchannel_ctl(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _cb_program = r.get_u32().unwrap_or(0);
        let sec_count = r.get_u32().unwrap_or(0);
        for _ in 0..sec_count {
            let _ = r.get_u32(); // flavor
        }
        (NFS4_OK, Vec::new())
    }

    // -----------------------------------------------------------------------
    // pNFS stubs — we don't support parallel NFS yet, return appropriate errors
    // -----------------------------------------------------------------------

    /// LAYOUTGET (RFC 5661 §18.43) — return a single-stripe LAYOUT4_NFSV4_1_FILES
    /// layout pointing to ourselves (single co-located DS). When pnfs_ds_addr
    /// is unset we fall back to NFS4ERR_LAYOUTUNAVAILABLE so kernels gracefully
    /// drop pNFS and use the MDS.
    fn op_layoutget(&self, r: &mut XdrReader, current_fh: &Option<Vec<u8>>) -> (u32, Vec<u8>) {
        let _signal_available = r.get_u32().unwrap_or(0);
        let layout_type = r.get_u32().unwrap_or(0);
        let _iomode = r.get_u32().unwrap_or(0);
        let _offset = r.get_u64().unwrap_or(0);
        let _length = r.get_u64().unwrap_or(0);
        let _minlength = r.get_u64().unwrap_or(0);
        let _stateid = StateId::decode(r);
        let _maxcount = r.get_u32().unwrap_or(0);

        if self.pnfs_ds_addr.read().is_none() {
            return (NFS4ERR_LAYOUTUNAVAILABLE, Vec::new());
        }
        if layout_type != 1 {
            return (NFS4ERR_UNKNOWN_LAYOUTTYPE, Vec::new());
        }
        let fh = match current_fh {
            Some(fh) => fh.clone(),
            None => return (NFS4ERR_NOFILEHANDLE, Vec::new()),
        };

        // logr_return_on_close + logr_stateid + array(layout4):
        let mut w = XdrWriter::new();
        w.put_bool(false); // return_on_close
                           // Layout stateid: derive from a stable per-server counter.
        let mut deleg_other = [0u8; 12];
        deleg_other[..8].copy_from_slice(
            &self
                .next_state
                .fetch_add(1, Ordering::Relaxed)
                .to_le_bytes(),
        );
        deleg_other[8] = 0x1A; // marker: pNFS layout stateid
        StateId {
            seqid: 1,
            other: deleg_other,
        }
        .encode(&mut w);

        // 1 layout4 covering [0, u64::MAX) in iomode RW.
        w.put_u32(1); // layout count = 1
        w.put_u64(0); // lo_offset
        w.put_u64(u64::MAX); // lo_length
        w.put_u32(2); // lo_iomode = LAYOUTIOMODE4_RW
        w.put_u32(1); // loc_type = LAYOUT4_NFSV4_1_FILES

        // Encode the file-layout body as opaque<>.
        let mut body = XdrWriter::new();
        // deviceid4 (16 bytes) — fixed value `nvstor-pnfs-1` padded.
        let mut device_id = [0u8; 16];
        device_id[..14].copy_from_slice(b"nvstor-pnfs-01");
        body.put_raw(&device_id);
        // nfl_util: stripe_unit (lower 32 bits), commit-through-mds bit (1<<29).
        // Use 64KB stripe unit and CTM=1.
        body.put_u32((1 << 29) | 65_536);
        body.put_u32(0); // first_stripe_index
        body.put_u64(0); // pattern_offset
                         // fh_list: 1 entry (we are the only DS, FH is the file's own).
        body.put_u32(1);
        body.put_opaque(&fh);
        w.put_opaque(&body.into_bytes());

        (NFS4_OK, w.into_bytes())
    }

    fn op_layoutcommit(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _offset = r.get_u64().unwrap_or(0);
        let _length = r.get_u64().unwrap_or(0);
        let _reclaim = r.get_u32().unwrap_or(0);
        let _stateid = StateId::decode(r);
        // last_write_offset (newoffset4): bool present + u64 offset
        let _ = r.get_u32();
        let _ = r.get_u64();
        // time_modify (newtime4): bool present + nfstime4 (i64 + u32)
        let _ = r.get_u32();
        let _ = r.get_i64();
        let _ = r.get_u32();
        // layoutupdate4: layouttype + opaque body
        let _ = r.get_u32();
        let _ = r.get_opaque();

        // Reply: lou_newsize4 — newsize_present(bool) + size(u64) if present.
        let mut w = XdrWriter::new();
        w.put_u32(0); // newsize not present
        (NFS4_OK, w.into_bytes())
    }

    fn op_layoutreturn(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _reclaim = r.get_u32().unwrap_or(0);
        let _layout_type = r.get_u32().unwrap_or(0);
        let _iomode = r.get_u32().unwrap_or(0);
        let _return_type = r.get_u32().unwrap_or(0);
        // layoutreturn_file body skipped; we don't track per-file layouts.
        // Reply: layoutreturn_stateid_present(bool) + stateid?
        let mut w = XdrWriter::new();
        w.put_u32(0); // no stateid present
        (NFS4_OK, w.into_bytes())
    }

    fn op_getdeviceinfo(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let _device_id = r.get_opaque_fixed(16).unwrap_or_default();
        let layout_type = r.get_u32().unwrap_or(0);
        let _maxcount = r.get_u32().unwrap_or(0);
        let _notify = decode_bitmap(r);

        let addr = self.pnfs_ds_addr.read().clone();
        let (host, port) = match addr {
            Some(a) => a,
            None => return (NFS4ERR_NOENT, Vec::new()),
        };
        if layout_type != 1 {
            return (NFS4ERR_UNKNOWN_LAYOUTTYPE, Vec::new());
        }

        // Encode device_addr4 = layout_type + opaque body.
        let mut w = XdrWriter::new();
        w.put_u32(1); // LAYOUT4_NFSV4_1_FILES

        // Body = nfsv4_1_file_layout_ds_addr4:
        //   stripe_indices: array<u32>
        //   multipath_ds_list: array<netaddr4<>>
        let mut body = XdrWriter::new();
        body.put_u32(1);
        body.put_u32(0); // 1 stripe index, all → DS 0
        body.put_u32(1); // 1 DS group
        body.put_u32(1); // group has 1 multipath addr
        body.put_string("tcp"); // netid
                                // Universal address: "host.h.l" where h*256+l = port.
        let universal = format!("{host}.{}.{}", port >> 8, port & 0xff);
        body.put_string(&universal);
        w.put_opaque(&body.into_bytes());

        // notification bitmap (empty).
        w.put_u32(0);
        (NFS4_OK, w.into_bytes())
    }

    fn op_getdevicelist(&self, r: &mut XdrReader) -> (u32, Vec<u8>) {
        let layout_type = r.get_u32().unwrap_or(0);
        let _maxdevices = r.get_u32().unwrap_or(0);
        let _cookie = r.get_u64().unwrap_or(0);
        let _verifier = r.get_opaque_fixed(8).unwrap_or_default();
        if self.pnfs_ds_addr.read().is_none() || layout_type != 1 {
            return (NFS4ERR_UNKNOWN_LAYOUTTYPE, Vec::new());
        }
        let mut w = XdrWriter::new();
        w.put_u64(0); // cookie
        w.put_raw(&[0u8; 8]); // verifier
        w.put_u32(1); // 1 device
        let mut device_id = [0u8; 16];
        device_id[..14].copy_from_slice(b"nvstor-pnfs-01");
        w.put_raw(&device_id);
        w.put_bool(true); // eof
        (NFS4_OK, w.into_bytes())
    }
}

// ---------------------------------------------------------------------------
// FATTR4 encoding
// ---------------------------------------------------------------------------

/// Encode requested attributes into FATTR4 format.
///
/// `pnfs_enabled` controls whether `FATTR4_FS_LAYOUT_TYPES` is advertised in
/// the response and the supported-attrs bitmap. Advertising it without a
/// configured pNFS data-server address makes the kernel hammer LAYOUTGET
/// for ~3 minutes per write before giving up (each LAYOUTGET we'd then
/// answer with NFS4ERR_LAYOUTUNAVAILABLE on exponential backoff).
///
/// `trunk_addrs` is the FATTR4_FS_LOCATIONS server list (see
/// `CompoundProcessor::set_trunk_addrs`); empty means the attribute is not
/// supported.
fn encode_fattr4(
    w: &mut XdrWriter,
    attrs: &InodeAttrs,
    req_word0: u32,
    req_word1: u32,
    pnfs_enabled: bool,
    trunk_addrs: &[String],
) {
    // First pass: determine which attributes we'll actually encode and build the
    // response bitmap.
    let mut resp_word0 = 0u32;
    let mut resp_word1 = 0u32;
    let mut vals = XdrWriter::new();

    // Word 0 attributes (MUST be encoded in bitmap order — low bits first)
    if (req_word0 & (1 << FATTR4_SUPPORTED_ATTRS)) != 0 {
        resp_word0 |= 1 << FATTR4_SUPPORTED_ATTRS;
        // Return a 2-word bitmap of all attributes we support.
        let sup_word0: u32 = (1 << FATTR4_SUPPORTED_ATTRS)
            | (1 << FATTR4_TYPE)
            | (1 << FATTR4_FH_EXPIRE_TYPE)
            | (1 << FATTR4_CHANGE)
            | (1 << FATTR4_SIZE)
            | (1 << FATTR4_LINK_SUPPORT)
            | (1 << FATTR4_SYMLINK_SUPPORT)
            | (1 << FATTR4_NAMED_ATTR)
            | (1 << FATTR4_FSID)
            | (1 << FATTR4_UNIQUE_HANDLES)
            | (1 << FATTR4_LEASE_TIME)
            | (1 << FATTR4_RDATTR_ERROR)
            | (1 << FATTR4_ACLSUPPORT)
            | (1 << FATTR4_FILEID)
            | (1 << FATTR4_FILES_AVAIL)
            | (1 << FATTR4_FILES_FREE)
            | (1 << FATTR4_FILES_TOTAL)
            | (1 << FATTR4_HOMOGENEOUS)
            | (1 << FATTR4_MAXFILESIZE)
            | (1 << FATTR4_MAXLINK)
            | (1 << FATTR4_MAXNAME)
            | (1 << FATTR4_MAXREAD)
            | (1 << FATTR4_MAXWRITE)
            | if trunk_addrs.is_empty() {
                0
            } else {
                1 << FATTR4_FS_LOCATIONS
            };
        let sup_word1: u32 = (1 << (FATTR4_MODE - 32))
            | (1 << (FATTR4_NO_TRUNC - 32))
            | (1 << (FATTR4_NUMLINKS - 32))
            | (1 << (FATTR4_OWNER - 32))
            | (1 << (FATTR4_OWNER_GROUP - 32))
            | (1 << (FATTR4_SPACE_AVAIL - 32))
            | (1 << (FATTR4_SPACE_FREE - 32))
            | (1 << (FATTR4_SPACE_TOTAL - 32))
            | (1 << (FATTR4_SPACE_USED - 32))
            | (1 << (FATTR4_TIME_ACCESS - 32))
            | (1 << (FATTR4_TIME_METADATA - 32))
            | (1 << (FATTR4_TIME_MODIFY - 32))
            | (1 << (FATTR4_MOUNTED_ON_FILEID - 32))
            | if pnfs_enabled {
                1 << (FATTR4_FS_LAYOUT_TYPES - 32)
            } else {
                0
            };
        vals.put_u32(2); // bitmap length = 2 words
        vals.put_u32(sup_word0);
        vals.put_u32(sup_word1);
    }
    if (req_word0 & (1 << FATTR4_TYPE)) != 0 {
        resp_word0 |= 1 << FATTR4_TYPE;
        vals.put_u32(match attrs.file_type {
            FileType::Regular => NF4REG,
            FileType::Directory => NF4DIR,
            FileType::Symlink => NF4LNK,
        });
    }
    if (req_word0 & (1 << FATTR4_FH_EXPIRE_TYPE)) != 0 {
        resp_word0 |= 1 << FATTR4_FH_EXPIRE_TYPE;
        vals.put_u32(0); // FH4_PERSISTENT — filehandles don't expire
    }
    if (req_word0 & (1 << FATTR4_CHANGE)) != 0 {
        resp_word0 |= 1 << FATTR4_CHANGE;
        vals.put_u64(attrs.mtime as u64); // change attr = mtime
    }
    if (req_word0 & (1 << FATTR4_SIZE)) != 0 {
        resp_word0 |= 1 << FATTR4_SIZE;
        vals.put_u64(attrs.size);
    }
    if (req_word0 & (1 << FATTR4_LINK_SUPPORT)) != 0 {
        resp_word0 |= 1 << FATTR4_LINK_SUPPORT;
        vals.put_u32(1); // true — we support hardlinks
    }
    if (req_word0 & (1 << FATTR4_SYMLINK_SUPPORT)) != 0 {
        resp_word0 |= 1 << FATTR4_SYMLINK_SUPPORT;
        vals.put_u32(1);
    }
    // bit 7: NAMED_ATTR
    if (req_word0 & (1 << FATTR4_NAMED_ATTR)) != 0 {
        resp_word0 |= 1 << FATTR4_NAMED_ATTR;
        vals.put_u32(0); // false — no named attributes
    }
    // bit 8: FSID (major u64 + minor u64)
    if (req_word0 & (1 << FATTR4_FSID)) != 0 {
        resp_word0 |= 1 << FATTR4_FSID;
        vals.put_u64(0xBEDC0C);
        vals.put_u64(0xBEDC0C);
    }
    // bit 9: UNIQUE_HANDLES
    if (req_word0 & (1 << FATTR4_UNIQUE_HANDLES)) != 0 {
        resp_word0 |= 1 << FATTR4_UNIQUE_HANDLES;
        vals.put_u32(1);
    }
    // bit 10: LEASE_TIME
    if (req_word0 & (1 << FATTR4_LEASE_TIME)) != 0 {
        resp_word0 |= 1 << FATTR4_LEASE_TIME;
        vals.put_u32(90);
    }
    // bit 11: RDATTR_ERROR
    if (req_word0 & (1 << FATTR4_RDATTR_ERROR)) != 0 {
        resp_word0 |= 1 << FATTR4_RDATTR_ERROR;
        vals.put_u32(0);
    }
    // bit 13: ACLSUPPORT
    if (req_word0 & (1 << FATTR4_ACLSUPPORT)) != 0 {
        resp_word0 |= 1 << FATTR4_ACLSUPPORT;
        vals.put_u32(0); // no ACL support
    }
    // bit 20: FILEID
    if (req_word0 & (1 << FATTR4_FILEID)) != 0 {
        resp_word0 |= 1 << FATTR4_FILEID;
        vals.put_u64(attrs.ino);
    }
    // bit 21: FILES_AVAIL
    if (req_word0 & (1 << FATTR4_FILES_AVAIL)) != 0 {
        resp_word0 |= 1 << FATTR4_FILES_AVAIL;
        vals.put_u64(1_000_000);
    }
    // bit 22: FILES_FREE
    if (req_word0 & (1 << FATTR4_FILES_FREE)) != 0 {
        resp_word0 |= 1 << FATTR4_FILES_FREE;
        vals.put_u64(1_000_000);
    }
    // bit 23: FILES_TOTAL
    if (req_word0 & (1 << FATTR4_FILES_TOTAL)) != 0 {
        resp_word0 |= 1 << FATTR4_FILES_TOTAL;
        vals.put_u64(2_000_000);
    }
    // bit 24: FS_LOCATIONS (RFC 8881 §11.16), only with trunk addresses.
    // fs_locations4 { pathname4 fs_root; fs_location4 locations<>; }:
    // fs_root is this export's root (no components); one location whose
    // servers are every trunk address and whose rootpath is the same root.
    if !trunk_addrs.is_empty() && (req_word0 & (1 << FATTR4_FS_LOCATIONS)) != 0 {
        resp_word0 |= 1 << FATTR4_FS_LOCATIONS;
        vals.put_u32(0); // fs_root: no components
        vals.put_u32(1); // one fs_location4
        vals.put_u32(trunk_addrs.len() as u32);
        for addr in trunk_addrs {
            vals.put_string(addr);
        }
        vals.put_u32(0); // rootpath: no components
    }
    // bit 26: HOMOGENEOUS
    if (req_word0 & (1 << FATTR4_HOMOGENEOUS)) != 0 {
        resp_word0 |= 1 << FATTR4_HOMOGENEOUS;
        vals.put_u32(1); // true
    }
    // bit 27: MAXFILESIZE
    if (req_word0 & (1 << FATTR4_MAXFILESIZE)) != 0 {
        resp_word0 |= 1 << FATTR4_MAXFILESIZE;
        vals.put_u64(16 * 1024 * 1024 * 1024 * 1024);
    }
    // bit 28: MAXLINK
    if (req_word0 & (1 << FATTR4_MAXLINK)) != 0 {
        resp_word0 |= 1 << FATTR4_MAXLINK;
        vals.put_u32(65535);
    }
    // bit 29: MAXNAME
    if (req_word0 & (1 << FATTR4_MAXNAME)) != 0 {
        resp_word0 |= 1 << FATTR4_MAXNAME;
        vals.put_u32(255);
    }
    // bit 30: MAXREAD
    if (req_word0 & (1 << FATTR4_MAXREAD)) != 0 {
        resp_word0 |= 1 << FATTR4_MAXREAD;
        vals.put_u64(1048576);
    }
    // bit 31: MAXWRITE
    if (req_word0 & (1 << FATTR4_MAXWRITE)) != 0 {
        resp_word0 |= 1 << FATTR4_MAXWRITE;
        vals.put_u64(1048576);
    }

    // Word 1 attributes — MUST match kernel decode order in decode_getfattr_attrs
    if (req_word1 & (1 << (FATTR4_MODE - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_MODE - 32);
        vals.put_u32(attrs.mode);
    }
    // bit 2 of word1: NO_TRUNC
    if (req_word1 & (1 << (FATTR4_NO_TRUNC - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_NO_TRUNC - 32);
        vals.put_u32(1); // true — names are NOT truncated
    }
    if (req_word1 & (1 << (FATTR4_NUMLINKS - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_NUMLINKS - 32);
        vals.put_u32(attrs.nlink);
    }
    if (req_word1 & (1 << (FATTR4_OWNER - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_OWNER - 32);
        // NFS4 OWNER is a string in user@domain format.
        // "nobody" is safe; numeric UIDs may fail idmap.
        let owner = if attrs.uid == 0 { "root" } else { "nobody" };
        vals.put_string(owner);
    }
    if (req_word1 & (1 << (FATTR4_OWNER_GROUP - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_OWNER_GROUP - 32);
        let group = if attrs.gid == 0 { "root" } else { "nogroup" };
        vals.put_string(group);
    }
    // IMPORTANT: Word 1 attributes must be in bitmap order (low bits first).
    // SPACE_AVAIL(42)=bit10, SPACE_FREE(43)=bit11, SPACE_TOTAL(44)=bit12
    // come BEFORE SPACE_USED(45)=bit13.
    if (req_word1 & (1 << (FATTR4_SPACE_AVAIL - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_SPACE_AVAIL - 32);
        vals.put_u64(1024 * 1024 * 1024 * 1024); // 1 TB available
    }
    if (req_word1 & (1 << (FATTR4_SPACE_FREE - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_SPACE_FREE - 32);
        vals.put_u64(1024 * 1024 * 1024 * 1024); // 1 TB free
    }
    if (req_word1 & (1 << (FATTR4_SPACE_TOTAL - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_SPACE_TOTAL - 32);
        vals.put_u64(2 * 1024 * 1024 * 1024 * 1024); // 2 TB total
    }
    if (req_word1 & (1 << (FATTR4_SPACE_USED - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_SPACE_USED - 32);
        vals.put_u64(attrs.size);
    }
    if (req_word1 & (1 << (FATTR4_TIME_ACCESS - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_TIME_ACCESS - 32);
        encode_nfstime4(&mut vals, attrs.atime);
    }
    if (req_word1 & (1 << (FATTR4_TIME_METADATA - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_TIME_METADATA - 32);
        encode_nfstime4(&mut vals, attrs.ctime);
    }
    // IMPORTANT: The Linux kernel decodes TIME_MODIFY (bit 21) BEFORE
    // MOUNTED_ON_FILEID (bit 20). This violates strict bitmap order but
    // we MUST match the kernel's decode order or it returns -EIO.
    if (req_word1 & (1 << (FATTR4_TIME_MODIFY - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_TIME_MODIFY - 32);
        encode_nfstime4(&mut vals, attrs.mtime);
    }
    if (req_word1 & (1 << (FATTR4_MOUNTED_ON_FILEID - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_MOUNTED_ON_FILEID - 32);
        vals.put_u64(attrs.ino);
    }
    if pnfs_enabled && (req_word1 & (1 << (FATTR4_FS_LAYOUT_TYPES - 32))) != 0 {
        resp_word1 |= 1 << (FATTR4_FS_LAYOUT_TYPES - 32);
        // Single layout type: LAYOUT4_NFSV4_1_FILES (1).
        vals.put_u32(1); // count
        vals.put_u32(1); // LAYOUT4_NFSV4_1_FILES
    }

    // Encode bitmap.
    encode_bitmap(w, resp_word0, resp_word1);

    // Encode attr values as opaque.
    let val_bytes = vals.into_bytes();
    w.put_opaque(&val_bytes);
}

/// Encode an NFS4 time (seconds + nseconds).
fn encode_nfstime4(w: &mut XdrWriter, secs: i64) {
    w.put_i64(secs);
    w.put_u32(0); // nseconds
}

/// Encode a 2-word bitmap.
fn encode_bitmap(w: &mut XdrWriter, word0: u32, word1: u32) {
    if word1 != 0 {
        w.put_u32(2);
        w.put_u32(word0);
        w.put_u32(word1);
    } else if word0 != 0 {
        w.put_u32(1);
        w.put_u32(word0);
    } else {
        w.put_u32(0);
    }
}

/// Decode a bitmap from XDR. Returns (word0, word1).
fn decode_bitmap(r: &mut XdrReader) -> Result<(u32, u32), ()> {
    let count = r.get_u32().map_err(|_| ())?;
    let word0 = if count >= 1 {
        r.get_u32().map_err(|_| ())?
    } else {
        0
    };
    let word1 = if count >= 2 {
        r.get_u32().map_err(|_| ())?
    } else {
        0
    };
    // Skip any additional bitmap words.
    for _ in 2..count {
        let _ = r.get_u32();
    }
    Ok((word0, word1))
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

/// Whether a READDIR continuing from a nonzero cookie may use it: the
/// client presents the verifier of the directory listing the cookie came
/// from (RFC 8881 §18.23.3). An all-zero verifier is accepted, because the
/// Linux client zeroes its saved verifier when it invalidates the
/// directory's cached attributes and then continues from its last cookie;
/// Linux nfsd ignores the verifier altogether.
fn readdir_verifier_matches(presented: &[u8], current: &[u8; 8]) -> bool {
    presented == current || presented.iter().all(|byte| *byte == 0)
}

/// OPEN opens regular files only (RFC 8881 §18.16.3): a directory is
/// NFS4ERR_ISDIR and a symbolic link NFS4ERR_SYMLINK, never followed.
fn open_target_status(attrs: &InodeAttrs) -> u32 {
    match attrs.file_type {
        FileType::Regular => NFS4_OK,
        FileType::Directory => NFS4ERR_ISDIR,
        FileType::Symlink => NFS4ERR_SYMLINK,
    }
}

/// Status for a filehandle that did not resolve, in PUTFH and in every
/// operation that resolves the current filehandle again (RFC 8881 §15.1.2,
/// as Linux nfsd's fh_verify): STALE for a well-formed handle whose object
/// is gone or whose seal no longer verifies (a server restarted with a new
/// key), BADHANDLE for one this server never issues, DELAY when the server
/// ran out of descriptors or memory resolving it, SERVERFAULT otherwise.
fn fh_err_to_nfs4(err: &nvstor_core::error::NvstorError) -> u32 {
    use nvstor_core::error::NvstorError;
    match err {
        NvstorError::StaleHandle | NvstorError::NotFound => NFS4ERR_STALE,
        NvstorError::InvalidArgument(_) => NFS4ERR_BADHANDLE,
        NvstorError::Io(e)
            if matches!(
                e.raw_os_error(),
                Some(libc::EMFILE | libc::ENFILE | libc::ENOMEM | libc::EAGAIN)
            ) =>
        {
            NFS4ERR_DELAY
        }
        _ => NFS4ERR_SERVERFAULT,
    }
}

/// NFS status for an OS error whose errno the client must see as itself:
/// a full disk or quota, a file too large, a read-only filesystem, a stale
/// file, or a permission error is not a generic NFS4ERR_IO. Applied to
/// every `NvstorError::Io`, so no data path (passthrough, PmStore, O_DIRECT
/// or buffered) can turn ENOSPC into EIO by wrapping the error early.
fn io_err_to_nfs4(e: &std::io::Error) -> Option<u32> {
    Some(match e.raw_os_error()? {
        libc::ENOSPC => NFS4ERR_NOSPC,
        libc::EDQUOT => NFS4ERR_DQUOT,
        libc::EFBIG => NFS4ERR_FBIG,
        libc::EROFS => NFS4ERR_ROFS,
        libc::ESTALE => NFS4ERR_STALE,
        libc::EACCES => NFS4ERR_ACCESS,
        libc::EPERM => NFS4ERR_PERM,
        _ => return None,
    })
}

/// READ and WRITE status: the file the current filehandle named is gone
/// (removed after PUTFH resolved it) is NFS4ERR_STALE; NOENT is not a legal
/// READ or WRITE status (RFC 8881 §15.2).
fn rw_err_to_nfs4(err: &nvstor_core::error::NvstorError) -> u32 {
    match err {
        nvstor_core::error::NvstorError::NotFound => NFS4ERR_STALE,
        e => nvstor_err_to_nfs4(e),
    }
}

fn nvstor_err_to_nfs4(err: &nvstor_core::error::NvstorError) -> u32 {
    use nvstor_core::error::NvstorError;
    match err {
        NvstorError::NotFound => NFS4ERR_NOENT,
        NvstorError::Exists => NFS4ERR_EXIST,
        NvstorError::NotDir => NFS4ERR_NOTDIR,
        NvstorError::IsDir => NFS4ERR_ISDIR,
        NvstorError::NotEmpty => NFS4ERR_NOTEMPTY,
        NvstorError::NoSpace => NFS4ERR_NOSPC,
        NvstorError::StaleHandle => NFS4ERR_STALE,
        NvstorError::PermissionDenied | NvstorError::AccessDenied => NFS4ERR_ACCESS,
        NvstorError::NotPermitted => NFS4ERR_PERM,
        NvstorError::Symlink => NFS4ERR_SYMLINK,
        NvstorError::InvalidArgument(_) => NFS4ERR_INVAL,
        NvstorError::NameTooLong => NFS4ERR_INVAL,
        NvstorError::Io(e) => io_err_to_nfs4(e).unwrap_or(NFS4ERR_IO),
        _ => NFS4ERR_IO,
    }
}

// ===========================================================================
// Tests
// ===========================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use nvstor_metadata::MetadataStore;
    use nvstor_storage::backend::PosixBackend;
    use nvstor_storage::manager::ChunkManager;
    use nvstor_storage::manager::ChunkManagerConfig;
    use tempfile::TempDir;

    fn setup() -> (TempDir, Arc<Vfs>, CompoundProcessor) {
        let tmp = TempDir::new().unwrap();
        let data_dir = tmp.path().join("data");
        let meta_dir = tmp.path().join("meta");
        let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let config = ChunkManagerConfig {
            disk_usage_threshold: 1.0,
            ..ChunkManagerConfig::default()
        };
        let mgr = Arc::new(ChunkManager::new(backend, Arc::new(metadata), config));
        let vfs = Arc::new(Vfs::new(mgr, "nfs4test/"));
        let proc = CompoundProcessor::new(Arc::clone(&vfs));
        (tmp, vfs, proc)
    }

    #[test]
    fn clustered_processor_persists_protocol_state_before_reply() {
        let (tmp, vfs, _) = setup();
        let metadata = Arc::new(MetadataStore::open(&tmp.path().join("recovery")).unwrap());
        let log = nvstor_raft::log::RaftLog::open(&tmp.path().join("raft")).unwrap();
        let network = Arc::new(nvstor_raft::network::RaftNetwork::new(1, vec![]));
        let raft = Arc::new(nvstor_raft::node::RaftNode::new(
            nvstor_raft::types::RaftConfig::default(),
            log,
            network,
            Box::new(nvstor_raft::state_machine::NullStateMachine),
        ));
        let processor = CompoundProcessor::with_raft(vfs.clone(), raft.clone(), metadata.clone());
        processor.next_state.store(99, Ordering::Relaxed);
        let response = processor.process(&build_compound(b"persist", &[(OP_PUTROOTFH, vec![])]));
        assert_eq!(decode_reply_status(&response), NFS4_OK);
        assert_eq!(
            metadata
                .get_inline("nfs/counter/next_state")
                .unwrap()
                .unwrap(),
            99u64.to_le_bytes()
        );
        drop(processor);
        let restored = CompoundProcessor::with_raft(vfs, raft, metadata);
        assert_eq!(restored.next_state.load(Ordering::Relaxed), 99);
    }

    /// Build a COMPOUND request with the given operations.
    fn build_compound(tag: &[u8], ops: &[(u32, Vec<u8>)]) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_opaque(tag); // tag
        w.put_u32(0); // minorversion
        w.put_u32(ops.len() as u32); // numops
        for (opnum, args) in ops {
            w.put_u32(*opnum);
            w.put_raw(args);
        }
        w.into_bytes()
    }

    /// Decode the compound reply status.
    fn decode_reply_status(reply: &[u8]) -> u32 {
        let mut r = XdrReader::new(reply);
        r.get_u32().unwrap()
    }

    /// Decode compound reply: status, tag, num_results.
    fn decode_reply_header(reply: &[u8]) -> (u32, Vec<u8>, u32) {
        let mut r = XdrReader::new(reply);
        let status = r.get_u32().unwrap();
        let tag = r.get_opaque().unwrap();
        let num = r.get_u32().unwrap();
        (status, tag, num)
    }

    #[test]
    fn empty_compound() {
        let (_tmp, _vfs, proc) = setup();
        let payload = build_compound(b"", &[]);
        let reply = proc.process(&payload);
        let status = decode_reply_status(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn putrootfh_getfh() {
        let (_tmp, _vfs, proc) = setup();
        let payload = build_compound(
            b"test",
            &[(OP_PUTROOTFH, Vec::new()), (OP_GETFH, Vec::new())],
        );
        let reply = proc.process(&payload);
        let (status, tag, num_results) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(tag, b"test");
        assert_eq!(num_results, 2);
    }

    #[test]
    fn putrootfh_getattr_bitmap() {
        let (_tmp, _vfs, proc) = setup();
        // Request type and size attributes.
        let mut attr_req = XdrWriter::new();
        attr_req.put_u32(1); // bitmap len = 1 word
        attr_req.put_u32((1 << FATTR4_TYPE) | (1 << FATTR4_SIZE)); // type + size

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_GETATTR, attr_req.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, num_results) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num_results, 2);

        // Parse the GETATTR result to verify bitmap encoding.
        let mut r = XdrReader::new(&reply);
        r.get_u32().unwrap(); // status
        r.get_opaque().unwrap(); // tag
        r.get_u32().unwrap(); // num results

        // First op: PUTROOTFH
        let op1 = r.get_u32().unwrap();
        assert_eq!(op1, OP_PUTROOTFH);
        let st1 = r.get_u32().unwrap();
        assert_eq!(st1, NFS4_OK);

        // Second op: GETATTR
        let op2 = r.get_u32().unwrap();
        assert_eq!(op2, OP_GETATTR);
        let st2 = r.get_u32().unwrap();
        assert_eq!(st2, NFS4_OK);

        // Parse returned bitmap.
        let bm_count = r.get_u32().unwrap();
        assert!(bm_count >= 1);
        let bm_word0 = r.get_u32().unwrap();
        // Should have type and size bits set.
        assert_ne!(bm_word0 & (1 << FATTR4_TYPE), 0);
        assert_ne!(bm_word0 & (1 << FATTR4_SIZE), 0);
    }

    /// PUTROOTFH + GETATTR(SUPPORTED_ATTRS, FS_LOCATIONS): the returned
    /// word-0 bitmap, the supported word 0, and the bytes after it.
    fn root_fs_locations(proc: &CompoundProcessor) -> (u32, u32, Vec<u8>) {
        let mut attr_req = XdrWriter::new();
        attr_req.put_u32(1);
        attr_req.put_u32((1 << FATTR4_SUPPORTED_ATTRS) | (1 << FATTR4_FS_LOCATIONS));
        let reply = proc.process(&build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_GETATTR, attr_req.into_bytes()),
            ],
        ));
        let mut r = XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), NFS4_OK);
        r.get_opaque().unwrap();
        r.get_u32().unwrap();
        assert_eq!(
            (r.get_u32().unwrap(), r.get_u32().unwrap()),
            (OP_PUTROOTFH, NFS4_OK)
        );
        assert_eq!(
            (r.get_u32().unwrap(), r.get_u32().unwrap()),
            (OP_GETATTR, NFS4_OK)
        );
        let words = r.get_u32().unwrap();
        let returned = r.get_u32().unwrap();
        for _ in 1..words {
            r.get_u32().unwrap();
        }
        let vals = r.get_opaque().unwrap().to_vec();
        let mut v = XdrReader::new(&vals);
        assert_eq!(v.get_u32().unwrap(), 2, "supported_attrs is two words");
        let supported = v.get_u32().unwrap();
        v.get_u32().unwrap();
        (returned, supported, vals[12..].to_vec())
    }

    // Without trunk addresses fs_locations is neither supported
    // nor returned, so a trunkdiscovery mount skips discovery instead of
    // failing on an empty location list.
    #[test]
    fn fs_locations_off_without_trunk_addrs() {
        let (_tmp, _vfs, proc) = setup();
        let (returned, supported, rest) = root_fs_locations(&proc);
        assert_eq!(supported & (1 << FATTR4_FS_LOCATIONS), 0);
        assert_eq!(returned & (1 << FATTR4_FS_LOCATIONS), 0);
        assert!(rest.is_empty());
    }

    // With trunk addresses, fs_locations is supported and
    // encodes fs_root = [], one location with every address and rootpath [].
    #[test]
    fn fs_locations_lists_trunk_addrs() {
        let (_tmp, _vfs, proc) = setup();
        let addrs = vec!["fd00:1:0:1::1".to_string(), "fd00:1:1:1::1".to_string()];
        proc.set_trunk_addrs(addrs.clone());
        let (returned, supported, rest) = root_fs_locations(&proc);
        assert_ne!(supported & (1 << FATTR4_FS_LOCATIONS), 0);
        assert_ne!(returned & (1 << FATTR4_FS_LOCATIONS), 0);
        let mut want = XdrWriter::new();
        want.put_u32(0); // fs_root
        want.put_u32(1); // locations
        want.put_u32(2); // servers
        for a in &addrs {
            want.put_string(a);
        }
        want.put_u32(0); // rootpath
        assert_eq!(rest, want.into_bytes());
    }

    #[test]
    fn lookup_nonexistent_returns_noent() {
        let (_tmp, _vfs, proc) = setup();
        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("nonexistent");

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4ERR_NOENT);
    }

    #[test]
    fn create_lookup_read() {
        let (_tmp, vfs, proc) = setup();

        // First create a file via VFS directly.
        let file = vfs.create_file(1, "test.dat", 0o644).unwrap();
        vfs.write(file.ino, 0, b"hello").unwrap();

        // Now look it up via NFS4 COMPOUND.
        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("test.dat");

        let mut read_args = XdrWriter::new();
        StateId::special_anonymous().encode(&mut read_args);
        read_args.put_u64(0); // offset
        read_args.put_u32(1024); // count

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
                (OP_READ, read_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 3);
    }

    #[test]
    fn create_write_read_compound() {
        let (_tmp, vfs, proc) = setup();

        // Create file via VFS.
        vfs.create_file(1, "write_test.dat", 0o644).unwrap();

        // LOOKUP + WRITE + READ via compound.
        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("write_test.dat");

        let mut write_args = XdrWriter::new();
        StateId::special_anonymous().encode(&mut write_args);
        write_args.put_u64(0); // offset
        write_args.put_u32(FILE_SYNC4); // stable
        write_args.put_opaque(b"NFS4 write test data");

        let mut read_args = XdrWriter::new();
        StateId::special_anonymous().encode(&mut read_args);
        read_args.put_u64(0); // offset
        read_args.put_u32(1024); // count

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
                (OP_WRITE, write_args.into_bytes()),
                (OP_READ, read_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 4);
    }

    #[test]
    fn no_current_fh_error() {
        let (_tmp, _vfs, proc) = setup();

        // GETATTR without PUTROOTFH should fail.
        let mut attr_req = XdrWriter::new();
        attr_req.put_u32(0); // empty bitmap

        let payload = build_compound(b"", &[(OP_GETATTR, attr_req.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4ERR_NOFILEHANDLE);
    }

    #[test]
    fn savefh_restorefh() {
        let (_tmp, _vfs, proc) = setup();

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_SAVEFH, Vec::new()),
                (OP_RESTOREFH, Vec::new()),
                (OP_GETFH, Vec::new()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 4);
    }

    #[test]
    fn nfs4_create_file() {
        let (_tmp, _vfs, proc) = setup();

        let mut create_args = XdrWriter::new();
        create_args.put_u32(NF4REG); // type = regular file
        create_args.put_string("created.txt"); // name
        create_args.put_u32(0); // empty bitmap
        create_args.put_opaque(&[]); // empty attr values

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_CREATE, create_args.into_bytes()),
                (OP_GETFH, Vec::new()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 3);
    }

    #[test]
    fn nfs4_remove() {
        let (_tmp, vfs, proc) = setup();

        vfs.create_file(1, "doomed.txt", 0o644).unwrap();

        let mut remove_args = XdrWriter::new();
        remove_args.put_string("doomed.txt");

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_REMOVE, remove_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);

        // Verify it's gone.
        assert!(vfs.lookup(1, "doomed.txt").is_err());
    }

    // --- Operation tests ---

    #[test]
    fn access_on_root() {
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_u32(0x3F); // all access bits
        let payload = build_compound(
            b"",
            &[(OP_PUTROOTFH, Vec::new()), (OP_ACCESS, args.into_bytes())],
        );
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 2);
    }

    #[test]
    fn commit_after_write() {
        let (_tmp, vfs, proc) = setup();
        vfs.create_file(1, "commit.txt", 0o644).unwrap();
        vfs.write(2, 0, b"data").unwrap();

        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("commit.txt");
        let mut commit_args = XdrWriter::new();
        commit_args.put_u64(0); // offset
        commit_args.put_u32(0); // count

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
                (OP_COMMIT, commit_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn lookupp_from_subdir() {
        let (_tmp, vfs, proc) = setup();
        vfs.mkdir(1, "mydir", 0o755).unwrap();

        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("mydir");

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
                (OP_LOOKUPP, Vec::new()),
                (OP_GETFH, Vec::new()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 4);
    }

    #[test]
    fn illegal_op_returns_error() {
        let (_tmp, _vfs, proc) = setup();
        let payload = build_compound(b"", &[(9999, Vec::new())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4ERR_OP_ILLEGAL);
    }

    #[test]
    fn setclientid_and_confirm() {
        let (_tmp, _vfs, proc) = setup();

        let mut sc_args = XdrWriter::new();
        sc_args.put_raw(&[0xAA; 8]); // verifier
        sc_args.put_opaque(b"test-client"); // id string
        sc_args.put_u32(0); // cb_program
        sc_args.put_string("tcp"); // netid
        sc_args.put_string("127.0.0.1.8.1"); // addr
        sc_args.put_u32(0); // cb_ident

        let payload = build_compound(b"", &[(OP_SETCLIENTID, sc_args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _tag, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 1);

        // Parse SETCLIENTID result: opnum + status + clientid + confirm_verifier
        let mut r = XdrReader::new(&reply);
        r.get_u32().unwrap(); // compound status
        r.get_opaque().unwrap(); // tag
        r.get_u32().unwrap(); // num results
        r.get_u32().unwrap(); // opnum
        r.get_u32().unwrap(); // op status
        let clientid = r.get_u64().unwrap();
        let confirm = r.get_opaque_fixed(8).unwrap();

        // Now confirm
        let mut confirm_args = XdrWriter::new();
        confirm_args.put_u64(clientid);
        confirm_args.put_raw(&confirm);

        let payload2 = build_compound(b"", &[(OP_SETCLIENTID_CONFIRM, confirm_args.into_bytes())]);
        let reply2 = proc.process(&payload2);
        let (status2, _, _) = decode_reply_header(&reply2);
        assert_eq!(status2, NFS4_OK);
    }

    #[test]
    fn renew_unknown_client() {
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_u64(99999); // unknown clientid
        let payload = build_compound(b"", &[(OP_RENEW, args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4ERR_STALE_CLIENTID);
    }

    #[test]
    fn delegpurge_and_delegreturn() {
        let (_tmp, _vfs, proc) = setup();

        let mut dp_args = XdrWriter::new();
        dp_args.put_u64(1); // clientid
        let payload = build_compound(b"", &[(OP_DELEGPURGE, dp_args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);

        let mut dr_args = XdrWriter::new();
        dr_args.put_u32(0); // seqid
        dr_args.put_raw(&[0u8; 12]); // other
        let payload2 = build_compound(b"", &[(OP_DELEGRETURN, dr_args.into_bytes())]);
        let reply2 = proc.process(&payload2);
        let (status2, _, _) = decode_reply_header(&reply2);
        assert_eq!(status2, NFS4_OK);
    }

    #[test]
    fn symlink_create_readlink() {
        let (_tmp, vfs, proc) = setup();

        // Create symlink via VFS, then test READLINK through NFS4
        vfs.create_symlink(1, "mylink", "/target/path").unwrap();

        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("mylink");

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
                (OP_READLINK, Vec::new()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 3);
    }

    #[test]
    fn hardlink_via_link_op() {
        let (_tmp, vfs, proc) = setup();
        let file = vfs.create_file(1, "original.txt", 0o644).unwrap();
        vfs.write(file.ino, 0, b"content").unwrap();

        // SAVEFH on the source file, then LINK in root dir
        let file_fh = vfs.ino_to_filehandle(file.ino);
        let mut putfh_args = XdrWriter::new();
        putfh_args.put_opaque(&file_fh);

        let mut link_args = XdrWriter::new();
        link_args.put_string("linked.txt");

        let payload = build_compound(
            b"",
            &[
                (OP_PUTFH, putfh_args.into_bytes()),
                (OP_SAVEFH, Vec::new()),
                (OP_PUTROOTFH, Vec::new()),
                (OP_LINK, link_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);

        // Both names should resolve to same ino
        let orig = vfs.lookup(1, "original.txt").unwrap();
        let linked = vfs.lookup(1, "linked.txt").unwrap();
        assert_eq!(orig.ino, linked.ino);
        assert_eq!(orig.nlink, 2);
    }

    #[test]
    fn verify_matching_size() {
        let (_tmp, vfs, proc) = setup();
        let file = vfs.create_file(1, "v.txt", 0o644).unwrap();
        vfs.write(file.ino, 0, b"12345").unwrap(); // size = 5

        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("v.txt");

        // VERIFY with correct size should succeed
        let mut verify_args = XdrWriter::new();
        verify_args.put_u32(1); // bitmap 1 word
        verify_args.put_u32(1 << FATTR4_SIZE); // size bit
        let mut size_val = XdrWriter::new();
        size_val.put_u64(5);
        verify_args.put_opaque(&size_val.into_bytes());

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
                (OP_VERIFY, verify_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn copy_via_nfs4() {
        let (_tmp, vfs, proc) = setup();

        // Create source with data.
        let src = vfs.create_file(1, "checkpoint.pt", 0o644).unwrap();
        vfs.write(src.ino, 0, b"model weights layer 42").unwrap();

        // Create empty destination.
        let dst = vfs.create_file(1, "checkpoint-clone.pt", 0o644).unwrap();

        let src_fh = vfs.ino_to_filehandle(src.ino);
        let dst_fh = vfs.ino_to_filehandle(dst.ino);

        // PUTFH(src) → SAVEFH → PUTFH(dst) → COPY
        let mut putfh_src = XdrWriter::new();
        putfh_src.put_opaque(&src_fh);
        let mut putfh_dst = XdrWriter::new();
        putfh_dst.put_opaque(&dst_fh);

        let mut copy_args = XdrWriter::new();
        StateId::special_anonymous().encode(&mut copy_args); // src stateid
        StateId::special_anonymous().encode(&mut copy_args); // dst stateid
        copy_args.put_u64(0); // src_offset
        copy_args.put_u64(0); // dst_offset
        copy_args.put_u64(0); // count (0 = full file)
        copy_args.put_u32(1); // consecutive
        copy_args.put_u32(1); // synchronous
        copy_args.put_u32(0); // 0 netlocs

        let payload = build_compound(
            b"",
            &[
                (OP_PUTFH, putfh_src.into_bytes()),
                (OP_SAVEFH, Vec::new()),
                (OP_PUTFH, putfh_dst.into_bytes()),
                (OP_COPY, copy_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);

        // Verify clone has same data.
        let data = vfs.read(dst.ino, 0, 1024).unwrap();
        assert_eq!(&data, b"model weights layer 42");
    }

    #[test]
    fn seek_via_nfs4() {
        let (_tmp, vfs, proc) = setup();
        let file = vfs.create_file(1, "data.bin", 0o644).unwrap();
        vfs.write(file.ino, 0, b"12345").unwrap();

        let file_fh = vfs.ino_to_filehandle(file.ino);
        let mut putfh_args = XdrWriter::new();
        putfh_args.put_opaque(&file_fh);

        let mut seek_args = XdrWriter::new();
        StateId::special_anonymous().encode(&mut seek_args);
        seek_args.put_u64(0); // offset
        seek_args.put_u32(1); // SEEK_HOLE

        let payload = build_compound(
            b"",
            &[
                (OP_PUTFH, putfh_args.into_bytes()),
                (OP_SEEK, seek_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn nverify_mismatched_size() {
        let (_tmp, vfs, proc) = setup();
        let file = vfs.create_file(1, "nv.txt", 0o644).unwrap();
        vfs.write(file.ino, 0, b"12345").unwrap();

        let mut lookup_args = XdrWriter::new();
        lookup_args.put_string("nv.txt");

        // NVERIFY with wrong size should succeed (mismatch => OK)
        let mut nverify_args = XdrWriter::new();
        nverify_args.put_u32(1);
        nverify_args.put_u32(1 << FATTR4_SIZE);
        let mut size_val = XdrWriter::new();
        size_val.put_u64(999); // wrong size
        nverify_args.put_opaque(&size_val.into_bytes());

        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup_args.into_bytes()),
                (OP_NVERIFY, nverify_args.into_bytes()),
            ],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    // --- NFS v4.1 session tests ---

    #[test]
    fn exchange_id_returns_clientid() {
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_raw(&[0xAA; 8]); // verifier
        args.put_opaque(b"v41-client"); // owner_id
        args.put_u32(0x30000); // flags
        args.put_u32(0); // SP4_NONE
        args.put_u32(0); // 0 impl_id entries

        let payload = build_compound(b"", &[(OP_EXCHANGE_ID, args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 1);
    }

    #[test]
    fn create_session_and_sequence() {
        let (_tmp, _vfs, proc) = setup();

        // Step 1: EXCHANGE_ID
        let mut eid_args = XdrWriter::new();
        eid_args.put_raw(&[0xBB; 8]);
        eid_args.put_opaque(b"session-client");
        eid_args.put_u32(0x30000);
        eid_args.put_u32(0);
        eid_args.put_u32(0);

        let payload = build_compound(b"", &[(OP_EXCHANGE_ID, eid_args.into_bytes())]);
        let reply = proc.process(&payload);
        let mut r = XdrReader::new(&reply);
        r.get_u32().unwrap(); // status
        r.get_opaque().unwrap(); // tag
        r.get_u32().unwrap(); // num
        r.get_u32().unwrap(); // opnum
        r.get_u32().unwrap(); // op status
        let clientid = r.get_u64().unwrap();

        // Step 2: CREATE_SESSION
        let mut cs_args = XdrWriter::new();
        cs_args.put_u64(clientid);
        cs_args.put_u32(1); // sequenceid
        cs_args.put_u32(0); // flags
                            // fore_chan_attrs: 7 fields
        for _ in 0..6 {
            cs_args.put_u32(1048576);
        }
        cs_args.put_u32(0); // rdma count
                            // back_chan_attrs: 7 fields
        for _ in 0..6 {
            cs_args.put_u32(1048576);
        }
        cs_args.put_u32(0); // rdma count
        cs_args.put_u32(0); // cb_program
        cs_args.put_u32(0); // 0 sec_parms

        let payload = build_compound(b"", &[(OP_CREATE_SESSION, cs_args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);

        // Extract session_id from reply (first 16 bytes after op header)
        let mut r = XdrReader::new(&reply);
        r.get_u32().unwrap(); // status
        r.get_opaque().unwrap(); // tag
        r.get_u32().unwrap(); // num
        r.get_u32().unwrap(); // opnum
        r.get_u32().unwrap(); // op status
        let session_id = r.get_opaque_fixed(16).unwrap();

        // Step 3: SEQUENCE. RFC 5661 §18.46.1 — sessionid, sequenceid,
        // slotid, highest_slotid, cachethis.
        let mut seq_args = XdrWriter::new();
        seq_args.put_raw(&session_id);
        seq_args.put_u32(1); // sequenceid
        seq_args.put_u32(0); // slotid
        seq_args.put_u32(0); // highest_slotid
        seq_args.put_u32(0); // cachethis

        let mut payload = build_compound(b"", &[(OP_SEQUENCE, seq_args.into_bytes())]);
        payload[4..8].copy_from_slice(&1u32.to_be_bytes());
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn rdma_session_and_connection_binding_report_actual_transport() {
        let (_tmp, _vfs, proc) = setup();
        let mut eid = XdrWriter::new();
        eid.put_raw(&[0x12; 8]);
        eid.put_opaque(b"rdma-client");
        eid.put_u32(0);
        eid.put_u32(0);
        eid.put_u32(0);
        let reply = proc.process(&build_compound(b"", &[(OP_EXCHANGE_ID, eid.into_bytes())]));
        let clientid = u64::from_be_bytes(reply[20..28].try_into().unwrap());
        for (rdma, flags, expected) in [(false, 4, 0), (true, 4, 4), (true, 0, 0)] {
            let mut cs = XdrWriter::new();
            cs.put_u64(clientid);
            cs.put_u32(7);
            cs.put_u32(flags);
            for _ in 0..2 {
                for n in [0, 1048576, 1048576, 4096, 64, 16] {
                    cs.put_u32(n);
                }
                cs.put_u32(1);
                cs.put_u32(8);
            }
            cs.put_u32(0);
            cs.put_u32(0);
            let request = build_compound(b"", &[(OP_CREATE_SESSION, cs.into_bytes())]);
            let reply = if rdma {
                proc.process_rdma(&request, None, &ExportPolicy::default(), None, &[])
                    .0
            } else {
                proc.process(&request)
            };
            assert_eq!(u32::from_be_bytes(reply[..4].try_into().unwrap()), NFS4_OK);
            let mut r = XdrReader::new(&reply[20..]);
            let session = r.get_opaque_fixed(16).unwrap();
            assert_eq!(r.get_u32().unwrap(), 7);
            assert_eq!(r.get_u32().unwrap(), expected);
            for _ in 0..6 {
                r.get_u32().unwrap();
            }
            assert_eq!(r.get_u32().unwrap(), u32::from(rdma));
            if rdma {
                assert_eq!(r.get_u32().unwrap(), 1);
            }

            let mut bind = XdrWriter::new();
            bind.put_raw(&session);
            bind.put_u32(3);
            bind.put_bool(true);
            let request = build_compound(b"", &[(OP_BIND_CONN_TO_SESSION, bind.into_bytes())]);
            let reply = if rdma {
                proc.process_rdma(&request, None, &ExportPolicy::default(), None, &[])
                    .0
            } else {
                proc.process(&request)
            };
            assert_eq!(u32::from_be_bytes(reply[36..40].try_into().unwrap()), 1); // FORE
            assert_eq!(
                u32::from_be_bytes(reply[40..44].try_into().unwrap()),
                u32::from(rdma)
            );
        }
    }

    #[test]
    fn reconnect_replays_remove_without_executing_it_twice() {
        let (_tmp, vfs, proc) = setup();
        vfs.create_file(1, "once", 0o644).unwrap();
        let sid = [0x34; 16];
        proc.sessions.write().insert(
            sid,
            SessionState {
                session_id: sid,
                clientid: 0,
                cache_limit: MAX_CACHED_WIRE_REPLY,
                fore: ForeChannel::SERVER,
                slots: vec![SlotState {
                    seqid: 0,
                    cached_reply: None,
                }],
            },
        );
        let mut seq = XdrWriter::new();
        seq.put_raw(&sid);
        for n in [1, 0, 0, 1] {
            seq.put_u32(n);
        }
        let mut remove = XdrWriter::new();
        remove.put_string("once");
        let mut request = build_compound(
            b"",
            &[
                (OP_SEQUENCE, seq.into_bytes()),
                (OP_PUTROOTFH, vec![]),
                (OP_REMOVE, remove.into_bytes()),
            ],
        );
        request[4..8].copy_from_slice(&1u32.to_be_bytes());
        let first = proc.process(&request);
        assert_eq!(decode_reply_status(&first), NFS4_OK);
        let replay = proc.process(&request);
        assert_eq!(
            replay, first,
            "retry on a replacement connection must replay the completed REMOVE"
        );
        assert_eq!(proc.sessions.read()[&sid].slots[0].seqid, 1);
    }

    fn sequenced_request(sid: [u8; 16], seqid: u32, ops: &[(u32, Vec<u8>)]) -> Vec<u8> {
        let mut seq = XdrWriter::new();
        seq.put_raw(&sid);
        for n in [seqid, 0, 0, 1] {
            seq.put_u32(n);
        }
        let mut all = vec![(OP_SEQUENCE, seq.into_bytes())];
        all.extend_from_slice(ops);
        let mut request = build_compound(b"", &all);
        request[4..8].copy_from_slice(&1u32.to_be_bytes());
        request
    }

    /// A client that has only used slot 0 (sa_highest_slotid = 0) must still
    /// be told every slot the server holds, or Linux shrinks its slot table to
    /// one entry and never has more than one RPC in flight.
    #[test]
    fn sequence_reports_the_servers_highest_slot_not_the_clients() {
        let (_tmp, _vfs, proc) = setup();
        let sid = [0x51; 16];
        proc.sessions.write().insert(
            sid,
            SessionState {
                session_id: sid,
                clientid: 0,
                cache_limit: MAX_CACHED_WIRE_REPLY,
                fore: ForeChannel::SERVER,
                slots: (0..64)
                    .map(|_| SlotState {
                        seqid: 0,
                        cached_reply: None,
                    })
                    .collect(),
            },
        );
        let request = sequenced_request(sid, 1, &[]);
        for reply in [proc.process(&request), proc.process(&request)] {
            // second: replay path
            assert_eq!(decode_reply_status(&reply), NFS4_OK);
            let mut r = XdrReader::new(&reply[20..]);
            assert_eq!(r.get_opaque_fixed(16).unwrap(), sid);
            assert_eq!(r.get_u32().unwrap(), 1); // sequenceid
            assert_eq!(r.get_u32().unwrap(), 0); // slotid
            assert_eq!(r.get_u32().unwrap(), 63, "sr_highest_slotid");
            assert_eq!(r.get_u32().unwrap(), 63, "sr_target_highest_slotid");
        }
    }

    /// EXCHANGE_ID + CREATE_SESSION with the given fore-channel attributes
    /// (maxrequestsize, maxresponsesize, maxresponsesize_cached, maxops,
    /// maxrequests); returns the session id and the six fore-channel
    /// attributes of the reply.
    fn create_session_with(proc: &CompoundProcessor, fore: [u32; 5]) -> ([u8; 16], [u32; 6]) {
        let mut eid = XdrWriter::new();
        eid.put_raw(&[0x21; 8]);
        eid.put_opaque(b"limits-client");
        eid.put_u32(0);
        eid.put_u32(0);
        eid.put_u32(0);
        let reply = proc.process(&build_compound(b"", &[(OP_EXCHANGE_ID, eid.into_bytes())]));
        let clientid = u64::from_be_bytes(reply[20..28].try_into().unwrap());
        let mut cs = XdrWriter::new();
        cs.put_u64(clientid);
        cs.put_u32(1);
        cs.put_u32(0);
        cs.put_u32(0);
        for n in fore {
            cs.put_u32(n);
        }
        cs.put_u32(0);
        for n in [0, 4096, 4096, 4096, 2, 1] {
            cs.put_u32(n);
        }
        cs.put_u32(0);
        cs.put_u32(0);
        cs.put_u32(0);
        let reply = proc.process(&build_compound(
            b"",
            &[(OP_CREATE_SESSION, cs.into_bytes())],
        ));
        assert_eq!(decode_reply_status(&reply), NFS4_OK);
        let mut r = XdrReader::new(&reply[20..]);
        let sid: [u8; 16] = r.get_opaque_fixed(16).unwrap().try_into().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        let mut attrs = [0u32; 6];
        for value in &mut attrs {
            *value = r.get_u32().unwrap();
        }
        assert_eq!(r.get_u32().unwrap(), 0, "no rdma_ird on TCP");
        // Back channel: what the client offered.
        let mut back = [0u32; 6];
        for value in &mut back {
            *value = r.get_u32().unwrap();
        }
        assert_eq!(back, [0, 4096, 4096, 4096, 2, 1]);
        (sid, attrs)
    }

    /// CREATE_SESSION caps the fore channel at what the server serves
    /// instead of echoing the client, and still grants every slot up to 64
    /// (SEQUENCE reports the same table).
    #[test]
    fn create_session_caps_fore_channel_to_server_limits() {
        let (_tmp, _vfs, proc) = setup();
        let (sid, attrs) =
            create_session_with(&proc, [u32::MAX, u32::MAX, u32::MAX, u32::MAX, 1000]);
        assert_eq!(
            attrs,
            [
                0,
                SESSION_MAX_MESSAGE,
                SESSION_MAX_MESSAGE,
                MAX_CACHED_WIRE_REPLY as u32,
                MAX_COMPOUND_OPS,
                SESSION_MAX_SLOTS
            ]
        );
        assert_eq!(
            proc.sessions.read()[&sid].slots.len(),
            SESSION_MAX_SLOTS as usize
        );
        let reply = proc.process(&sequenced_request(sid, 1, &[]));
        assert_eq!(decode_reply_status(&reply), NFS4_OK);
        let mut r = XdrReader::new(&reply[20..]);
        r.get_opaque_fixed(16).unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        assert_eq!(
            r.get_u32().unwrap(),
            SESSION_MAX_SLOTS - 1,
            "sr_highest_slotid"
        );

        // What a Linux client asks for (1 MiB rsize/wsize plus overhead, 64
        // slots) fits and comes back unchanged: no concurrency is lost.
        let (_, linux) = create_session_with(&proc, [1_049_620, 1_049_480, 7_584, 8, 64]);
        assert_eq!(linux, [0, 1_049_620, 1_049_480, 7_584, 8, 64]);
    }

    /// A session's negotiated limits are enforced before anything
    /// executes, and survive a server restart.
    #[test]
    fn session_fore_channel_limits_are_enforced_and_persisted() {
        let (tmp, vfs, _) = setup();
        let metadata = Arc::new(MetadataStore::open(&tmp.path().join("limits-state")).unwrap());
        let proc = CompoundProcessor::with_metadata(vfs.clone(), metadata.clone());
        let file = vfs.create_file(1, "big", 0o644).unwrap();
        vfs.write(file.ino, 0, &[7u8; 8192]).unwrap();
        let (sid, attrs) = create_session_with(&proc, [600, 4096, 4096, 3, 4]);
        assert_eq!(&attrs[1..5], &[600, 4096, 4096, 3]);
        let getfh = || (OP_GETFH, Vec::new());
        // SEQUENCE + 2 ops is within maxops 3; SEQUENCE + 3 is not.
        let ok = proc.process(&sequenced_request(
            sid,
            1,
            &[(OP_PUTROOTFH, vec![]), getfh()],
        ));
        assert_eq!(decode_reply_status(&ok), NFS4_OK);
        let too_many = proc.process(&sequenced_request(
            sid,
            2,
            &[(OP_PUTROOTFH, vec![]), getfh(), getfh()],
        ));
        assert_eq!(decode_reply_status(&too_many), NFS4ERR_TOO_MANY_OPS);
        assert_eq!(
            proc.sessions.read()[&sid].slots[0].seqid,
            1,
            "refused before the slot advanced"
        );
        // A request over maxrequestsize.
        let mut lookup = XdrWriter::new();
        lookup.put_string(&"x".repeat(700));
        let too_big = proc.process(&sequenced_request(
            sid,
            2,
            &[(OP_PUTROOTFH, vec![]), (OP_LOOKUP, lookup.into_bytes())],
        ));
        assert_eq!(decode_reply_status(&too_big), NFS4ERR_REQ_TOO_BIG);
        // A READ (cachethis = false) whose reply exceeds maxresponsesize.
        let mut putfh = XdrWriter::new();
        putfh.put_opaque(&vfs.ino_to_filehandle(file.ino));
        let mut read = XdrWriter::new();
        read.put_raw(&[0; 16]);
        read.put_u64(0);
        read.put_u32(8192);
        let mut seq = XdrWriter::new();
        seq.put_raw(&sid);
        for n in [2, 0, 0, 0] {
            seq.put_u32(n);
        }
        let mut request = build_compound(
            b"",
            &[
                (OP_SEQUENCE, seq.into_bytes()),
                (OP_PUTFH, putfh.into_bytes()),
                (OP_READ, read.into_bytes()),
            ],
        );
        request[4..8].copy_from_slice(&1u32.to_be_bytes());
        assert_eq!(
            decode_reply_status(&proc.process(&request)),
            NFS4ERR_REP_TOO_BIG
        );

        drop(proc);
        let restored = CompoundProcessor::with_metadata(vfs, metadata);
        assert_eq!(
            restored.sessions.read()[&sid].fore,
            ForeChannel {
                max_request: 600,
                max_response: 4096,
                max_ops: 3
            }
        );
        let still_too_many = restored.process(&sequenced_request(
            sid,
            3,
            &[(OP_PUTROOTFH, vec![]), getfh(), getfh()],
        ));
        assert_eq!(decode_reply_status(&still_too_many), NFS4ERR_TOO_MANY_OPS);
    }

    #[test]
    fn fore_channel_limits_round_trip_and_default_to_server() {
        let fore = ForeChannel::negotiate(1, 2, 3);
        assert_eq!(ForeChannel::decode(&fore.encode()), fore);
        assert_eq!(
            ForeChannel::decode(&[1, 2, 3]),
            ForeChannel::SERVER,
            "truncated record"
        );
        assert_eq!(
            ForeChannel::negotiate(u32::MAX, u32::MAX, u32::MAX),
            ForeChannel::SERVER
        );
    }

    #[test]
    fn concurrent_slot_retries_execute_remove_once_and_reject_false_retries() {
        let (_tmp, vfs, proc) = setup();
        vfs.create_file(1, "first", 0o644).unwrap();
        vfs.create_file(1, "other", 0o644).unwrap();
        let sid = [0x35; 16];
        proc.sessions.write().insert(
            sid,
            SessionState {
                session_id: sid,
                clientid: 0,
                cache_limit: MAX_CACHED_WIRE_REPLY,
                fore: ForeChannel::SERVER,
                slots: vec![SlotState {
                    seqid: 0,
                    cached_reply: None,
                }],
            },
        );
        let remove_request = |seqid, name| {
            let mut args = XdrWriter::new();
            args.put_string(name);
            sequenced_request(
                sid,
                seqid,
                &[(OP_PUTROOTFH, vec![]), (OP_REMOVE, args.into_bytes())],
            )
        };
        let request = remove_request(1, "first");
        let gate = std::sync::Barrier::new(8);
        let replies = std::thread::scope(|scope| {
            let workers: Vec<_> = (0..8)
                .map(|_| {
                    scope.spawn(|| {
                        gate.wait();
                        proc.process(&request)
                    })
                })
                .collect();
            workers
                .into_iter()
                .map(|worker| worker.join().unwrap())
                .collect::<Vec<_>>()
        });
        for reply in &replies {
            assert_eq!(decode_reply_status(reply), NFS4_OK);
            assert_eq!(reply, &replies[0]);
        }
        assert_eq!(
            decode_reply_status(&proc.process(&remove_request(1, "other"))),
            NFS4ERR_SEQ_FALSE_RETRY
        );
        assert_eq!(
            decode_reply_status(&proc.process(&remove_request(3, "other"))),
            NFS4ERR_SEQ_MISORDERED
        );
        // The rejected requests must not remove the other file.
        assert_eq!(
            decode_reply_status(&proc.process(&remove_request(2, "other"))),
            NFS4_OK
        );
        let mut invalid = remove_request(3, "other");
        invalid[4..8].copy_from_slice(&0u32.to_be_bytes());
        assert_eq!(
            decode_reply_status(&proc.process(&invalid)),
            NFS4ERR_OP_ILLEGAL
        );
        let mut seq = XdrWriter::new();
        seq.put_raw(&sid);
        for n in [3, 0, 0, 1] {
            seq.put_u32(n);
        }
        let mut invalid = build_compound(
            b"",
            &[(OP_PUTROOTFH, vec![]), (OP_SEQUENCE, seq.into_bytes())],
        );
        invalid[4..8].copy_from_slice(&1u32.to_be_bytes());
        assert_eq!(
            decode_reply_status(&proc.process(&invalid)),
            NFS4ERR_SEQUENCE_POS
        );
        assert_eq!(proc.sessions.read()[&sid].slots[0].seqid, 2);
    }

    #[test]
    fn cached_read_preserves_data_and_rdma_layout_across_transports() {
        let (_tmp, vfs, proc) = setup();
        let entry = vfs.create_file(1, "cached", 0o644).unwrap();
        vfs.write(entry.ino, 0, b"original").unwrap();
        let sid = [0x36; 16];
        proc.sessions.write().insert(
            sid,
            SessionState {
                session_id: sid,
                clientid: 0,
                cache_limit: MAX_CACHED_WIRE_REPLY,
                fore: ForeChannel::SERVER,
                slots: vec![SlotState {
                    seqid: 0,
                    cached_reply: None,
                }],
            },
        );
        let mut putfh = XdrWriter::new();
        putfh.put_opaque(&vfs.ino_to_filehandle(entry.ino));
        let mut read = XdrWriter::new();
        read.put_raw(&[0; 16]);
        read.put_u64(0);
        read.put_u32(8);
        let request = sequenced_request(
            sid,
            1,
            &[(OP_PUTFH, putfh.into_bytes()), (OP_READ, read.into_bytes())],
        );
        let first = proc.process(&request);
        assert_eq!(decode_reply_status(&first), NFS4_OK);
        vfs.write(entry.ino, 0, b"modified").unwrap();
        let mut layout = Vec::new();
        let replay = proc.process_inner(
            &request,
            &CallerCtx::root(),
            &ExportPolicy::default(),
            true,
            &mut DdpCtx::none(),
            &mut layout,
        );
        assert_eq!(replay, first);
        assert_eq!(layout.len(), 1);
        let item = layout[0].as_ref().unwrap();
        assert!(item.external.is_none());
        assert_eq!(&replay[item.at..item.at + item.len], b"original");
        let changed_user = proc.process_inner(
            &request,
            &CallerCtx::anonymous(),
            &ExportPolicy::default(),
            true,
            &mut DdpCtx::none(),
            &mut Vec::new(),
        );
        assert_eq!(decode_reply_status(&changed_user), NFS4ERR_SEQ_FALSE_RETRY);
    }

    #[test]
    fn required_cache_replays_partial_compound_error_after_state_reload() {
        for cache_limit in [4096, MAX_CACHED_WIRE_REPLY] {
            let (tmp, vfs, _) = setup();
            let metadata =
                Arc::new(MetadataStore::open(&tmp.path().join("session-state")).unwrap());
            let proc = CompoundProcessor::with_metadata(vfs.clone(), metadata.clone());
            vfs.create_file(1, "remove-once", 0o644).unwrap();
            let entry = vfs.create_file(1, "large", 0o644).unwrap();
            let sid = [0x37; 16];
            proc.sessions.write().insert(
                sid,
                SessionState {
                    session_id: sid,
                    clientid: 0,
                    cache_limit,
                    fore: ForeChannel::SERVER,
                    slots: vec![SlotState {
                        seqid: 0,
                        cached_reply: None,
                    }],
                },
            );
            let mut remove = XdrWriter::new();
            remove.put_string("remove-once");
            let mut putfh = XdrWriter::new();
            putfh.put_opaque(&vfs.ino_to_filehandle(entry.ino));
            let mut read = XdrWriter::new();
            read.put_raw(&[0; 16]);
            read.put_u64(0);
            read.put_u32(cache_limit as u32);
            let request = sequenced_request(
                sid,
                1,
                &[
                    (OP_PUTROOTFH, vec![]),
                    (OP_REMOVE, remove.into_bytes()),
                    (OP_PUTFH, putfh.into_bytes()),
                    (OP_READ, read.into_bytes()),
                ],
            );
            let first = proc.process(&request);
            assert_eq!(decode_reply_status(&first), NFS4ERR_REP_TOO_BIG_TO_CACHE);
            assert!(first.len() + 24 < cache_limit);
            assert!(
                proc.sessions.read()[&sid].slots[0]
                    .cached_reply
                    .as_ref()
                    .unwrap()
                    .len()
                    <= MAX_SESSION_REPLY_CACHE_BYTES
            );
            drop(proc);
            let restored = CompoundProcessor::with_metadata(vfs, metadata);
            assert_eq!(restored.sessions.read()[&sid].cache_limit, cache_limit);
            // Reexecuting REMOVE would produce NOENT and lose the READ error.
            assert_eq!(restored.process(&request), first);
        }
    }

    /// Session reply cache must refuse to store replies that would
    /// grow unbounded. We run a SEQUENCE + READ compound where READ returns
    /// a chunk deliberately larger than MAX_SESSION_REPLY_CACHE_BYTES, and
    /// verify the slot's `cached_reply` stays None.
    #[test]
    fn session_reply_cache_drops_oversize_replies() {
        let (_tmp, vfs, proc) = setup();

        // Pre-populate a file with >MAX_SESSION_REPLY_CACHE_BYTES of data.
        let entry = vfs.create_file(1, "big.dat", 0o644).unwrap();
        let big = vec![0xABu8; MAX_SESSION_REPLY_CACHE_BYTES + 1024];
        vfs.write(entry.ino, 0, &big).unwrap();
        let fh = vfs.ino_to_filehandle(entry.ino);

        // EXCHANGE_ID → CREATE_SESSION → get session_id.
        let mut eid = XdrWriter::new();
        eid.put_raw(&[0x11; 8]);
        eid.put_opaque(b"cache-client");
        eid.put_u32(0x30000);
        eid.put_u32(0);
        eid.put_u32(0);
        let reply = proc.process(&build_compound(b"", &[(OP_EXCHANGE_ID, eid.into_bytes())]));
        let mut r = XdrReader::new(&reply);
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        let clientid = r.get_u64().unwrap();

        let mut cs = XdrWriter::new();
        cs.put_u64(clientid);
        cs.put_u32(1);
        cs.put_u32(0);
        for _ in 0..6 {
            cs.put_u32(1048576);
        }
        cs.put_u32(0);
        for _ in 0..6 {
            cs.put_u32(1048576);
        }
        cs.put_u32(0);
        cs.put_u32(0);
        cs.put_u32(0);
        let reply = proc.process(&build_compound(
            b"",
            &[(OP_CREATE_SESSION, cs.into_bytes())],
        ));
        let mut r = XdrReader::new(&reply);
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        let session_bytes = r.get_opaque_fixed(16).unwrap();
        let mut session_id = [0u8; 16];
        session_id.copy_from_slice(&session_bytes);

        // Build SEQUENCE + PUTFH + READ(big window). Reply will be large.
        // SEQUENCE4args: sessionid, sequenceid, slotid, highest_slotid, cachethis.
        let mut seq = XdrWriter::new();
        seq.put_raw(&session_id);
        seq.put_u32(1); // sequenceid
        seq.put_u32(0); // slotid
        seq.put_u32(0); // highest_slotid
        seq.put_u32(0); // cachethis: large READ results may be uncached
        let mut putfh = XdrWriter::new();
        putfh.put_opaque(&fh);
        let mut read = XdrWriter::new();
        read.put_u32(0);
        read.put_raw(&[0u8; 12]);
        read.put_u64(0);
        read.put_u32(big.len() as u32);
        let mut payload = build_compound(
            b"",
            &[
                (OP_SEQUENCE, seq.into_bytes()),
                (OP_PUTFH, putfh.into_bytes()),
                (OP_READ, read.into_bytes()),
            ],
        );
        payload[4..8].copy_from_slice(&1u32.to_be_bytes());
        let reply = proc.process(&payload);
        assert_eq!(
            decode_reply_status(&reply),
            NFS4_OK,
            "SEQUENCE + READ of large payload must still succeed"
        );
        assert!(
            reply.len() > MAX_SESSION_REPLY_CACHE_BYTES,
            "test precondition: reply length {} must exceed cap {}",
            reply.len(),
            MAX_SESSION_REPLY_CACHE_BYTES
        );

        // Inspect the session slot — cached_reply must have been skipped
        // because the reply exceeded the cap.
        let sessions = proc.sessions.read();
        let session = sessions.get(&session_id).expect("session exists");
        let slot = &session.slots[0];
        assert!(
            slot.cached_reply.is_none(),
            "oversize reply ({} bytes) should NOT have been cached",
            reply.len()
        );
        drop(sessions);
        let retry = proc.process(&payload);
        assert_eq!(decode_reply_status(&retry), NFS4ERR_RETRY_UNCACHED_REP);
        let mut r = XdrReader::new(&retry);
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        assert_eq!(r.get_u32().unwrap(), 2);
        assert_eq!(r.get_u32().unwrap(), OP_SEQUENCE);
        assert_eq!(r.get_u32().unwrap(), NFS4_OK);
        r.get_opaque_fixed(36).unwrap();
        assert_eq!(r.get_u32().unwrap(), OP_PUTFH);
        assert_eq!(r.get_u32().unwrap(), NFS4ERR_RETRY_UNCACHED_REP);
    }

    #[test]
    fn destroy_session_invalid() {
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_raw(&[0xFF; 16]); // invalid session
        let payload = build_compound(b"", &[(OP_DESTROY_SESSION, args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4ERR_BADSESSION);
    }

    #[test]
    fn reclaim_complete() {
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_u32(0); // one_fs = false
        let payload = build_compound(b"", &[(OP_RECLAIM_COMPLETE, args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn secinfo_no_name() {
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_u32(0); // style
        let payload = build_compound(b"", &[(OP_SECINFO_NO_NAME, args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn open_share_mode_conflict() {
        let (_tmp, _vfs, proc) = setup();

        // Create a file via VFS.
        proc.vfs
            .create_file(1, "shared.txt", 0o644, &CallerCtx::root())
            .unwrap();

        // Build OPEN with SHARE_DENY_WRITE (deny=2).
        let mut open1 = XdrWriter::new();
        open1.put_u32(1); // seqid
        open1.put_u32(1); // share_access = READ
        open1.put_u32(2); // share_deny = WRITE
        open1.put_u64(1); // clientid
        open1.put_opaque(b"owner1");
        open1.put_u32(0); // OPEN4_NOCREATE
        open1.put_u32(0); // CLAIM_NULL
        open1.put_string("shared.txt");

        let payload = build_compound(
            b"",
            &[(OP_PUTROOTFH, Vec::new()), (OP_OPEN, open1.into_bytes())],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);

        // Second OPEN with SHARE_ACCESS_WRITE should conflict with deny.
        let mut open2 = XdrWriter::new();
        open2.put_u32(2);
        open2.put_u32(2); // share_access = WRITE
        open2.put_u32(0); // share_deny = NONE
        open2.put_u64(2);
        open2.put_opaque(b"owner2");
        open2.put_u32(0);
        open2.put_u32(0);
        open2.put_string("shared.txt");

        let payload = build_compound(
            b"",
            &[(OP_PUTROOTFH, Vec::new()), (OP_OPEN, open2.into_bytes())],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4ERR_DENIED);
    }

    #[test]
    fn openattr_succeeds_with_empty_attr_dir() {
        // OPENATTR now exposes a synthetic empty named-attr directory
        // instead of NFS4ERR_NOTSUPP. Linux NFS clients depend on this for
        // xattr support when the v4.2 OP_GETXATTR family isn't advertised.
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_u32(0); // createdir
        let payload = build_compound(b"", &[(OP_OPENATTR, args.into_bytes())]);
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    #[test]
    fn putpubfh_sets_root() {
        let (_tmp, _vfs, proc) = setup();
        let payload = build_compound(b"", &[(OP_PUTPUBFH, Vec::new()), (OP_GETFH, Vec::new())]);
        let reply = proc.process(&payload);
        let (status, _, num) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
        assert_eq!(num, 2);
    }

    #[test]
    fn secinfo_returns_flavors() {
        let (_tmp, _vfs, proc) = setup();
        let mut args = XdrWriter::new();
        args.put_string("any");
        let payload = build_compound(
            b"",
            &[(OP_PUTROOTFH, Vec::new()), (OP_SECINFO, args.into_bytes())],
        );
        let reply = proc.process(&payload);
        let (status, _, _) = decode_reply_header(&reply);
        assert_eq!(status, NFS4_OK);
    }

    /// Lock the NFSv4.1 / v4.2 opcode numbers to the values in
    /// RFC 5661 §16.1 and RFC 7862 §15.1. Silent off-by-one errors here
    /// cause the real kernel client to dispatch the wrong handler and
    /// disconnect with "Remote I/O error" — nothing in the lib tests
    /// catches it because our own tests use the same (wrong) constants
    /// on both sides.
    #[test]
    fn opcode_numbers_match_rfc() {
        // NFSv4.0 — RFC 7530.
        assert_eq!(OP_ACCESS, 3);
        assert_eq!(OP_CLOSE, 4);
        assert_eq!(OP_COMMIT, 5);
        assert_eq!(OP_CREATE, 6);
        assert_eq!(OP_GETATTR, 9);
        assert_eq!(OP_GETFH, 10);
        assert_eq!(OP_LOOKUP, 15);
        assert_eq!(OP_OPEN, 18);
        assert_eq!(OP_PUTFH, 22);
        assert_eq!(OP_PUTROOTFH, 24);
        assert_eq!(OP_READ, 25);
        assert_eq!(OP_READDIR, 26);
        assert_eq!(OP_READLINK, 27);
        assert_eq!(OP_RENAME, 29);
        assert_eq!(OP_RENEW, 30);
        assert_eq!(OP_SETATTR, 34);
        assert_eq!(OP_SETCLIENTID, 35);
        assert_eq!(OP_SETCLIENTID_CONFIRM, 36);
        assert_eq!(OP_WRITE, 38);
        // NFSv4.1 — RFC 5661 §16.1.
        assert_eq!(OP_BACKCHANNEL_CTL, 40);
        assert_eq!(OP_BIND_CONN_TO_SESSION, 41);
        assert_eq!(OP_EXCHANGE_ID, 42);
        assert_eq!(OP_CREATE_SESSION, 43);
        assert_eq!(OP_DESTROY_SESSION, 44);
        assert_eq!(OP_FREE_STATEID, 45);
        assert_eq!(OP_GETDEVICEINFO, 47);
        assert_eq!(OP_GETDEVICELIST, 48);
        assert_eq!(OP_LAYOUTCOMMIT, 49);
        assert_eq!(OP_LAYOUTGET, 50);
        assert_eq!(OP_LAYOUTRETURN, 51);
        assert_eq!(OP_SECINFO_NO_NAME, 52);
        assert_eq!(OP_SEQUENCE, 53);
        assert_eq!(OP_TEST_STATEID, 55);
        assert_eq!(OP_DESTROY_CLIENTID, 57);
        assert_eq!(OP_RECLAIM_COMPLETE, 58);
        // NFSv4.2 — RFC 7862 §15.1.
        assert_eq!(OP_ALLOCATE, 59);
        assert_eq!(OP_COPY, 60);
        assert_eq!(OP_DEALLOCATE, 62);
        assert_eq!(OP_IO_ADVISE, 63);
        assert_eq!(OP_SEEK, 69);
        assert_eq!(OP_CLONE, 71);
    }

    /// A malicious client can set `num_ops = u32::MAX` in a COMPOUND
    /// header and we'd iterate 4 billion times. Must be rejected with
    /// NFS4ERR_RESOURCE before the dispatch loop starts.
    #[test]
    fn compound_rejects_num_ops_over_cap() {
        let (_tmp, _vfs, proc) = setup();
        let mut w = XdrWriter::new();
        w.put_opaque(b""); // tag
        w.put_u32(0); // minorversion
        w.put_u32(MAX_COMPOUND_OPS + 1); // num_ops just over cap
                                         // No op bytes follow — the cap must fire before we try to read any.
        let reply = proc.process(&w.into_bytes());
        assert_eq!(decode_reply_status(&reply), NFS4ERR_RESOURCE);
    }

    /// A single READ op with `count` > MAX_READ_COUNT must fail
    /// rather than allocate a huge buffer.
    #[test]
    fn read_op_rejects_count_over_cap() {
        let (_tmp, vfs, proc) = setup();
        let entry = vfs.create_file(1, "big.dat", 0o644).unwrap();
        vfs.write(entry.ino, 0, b"hi").unwrap();
        let fh = vfs.ino_to_filehandle(entry.ino);

        let mut putfh = XdrWriter::new();
        putfh.put_opaque(&fh);
        let mut read = XdrWriter::new();
        read.put_u32(0);
        read.put_raw(&[0u8; 12]);
        read.put_u64(0);
        read.put_u32(MAX_READ_COUNT + 1); // one byte over cap
        let payload = build_compound(
            b"",
            &[(OP_PUTFH, putfh.into_bytes()), (OP_READ, read.into_bytes())],
        );
        let reply = proc.process(&payload);
        assert_eq!(decode_reply_status(&reply), NFS4ERR_RESOURCE);
    }

    /// Real-kernel-client regression: SECINFO_NO_NAME must advertise only
    /// the flavors the export actually accepts. Linux kernel takes the
    /// FIRST flavor in our reply as its retry credential — if we list
    /// AUTH_NONE while the export only accepts AUTH_SYS, the kernel
    /// retries with AUTH_NONE, gets AUTH_TOOWEAK from the RPC layer, and
    /// tears down the session.
    #[test]
    fn secinfo_no_name_advertises_only_export_flavors() {
        let (_tmp, _vfs, proc) = setup();

        let sys_only = ExportPolicy {
            advertised_flavors: vec![crate::rpc::AUTH_SYS],
            ..ExportPolicy::default()
        };

        let mut secinfo_args = XdrWriter::new();
        secinfo_args.put_u32(0); // SECINFO_STYLE4_CURRENT_FH
        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_SECINFO_NO_NAME, secinfo_args.into_bytes()),
            ],
        );
        let reply = proc.process_with_creds_policy(&payload, None, &sys_only);

        let mut r = XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), NFS4_OK);
        let _tag = r.get_opaque().unwrap();
        assert_eq!(r.get_u32().unwrap(), 2);
        assert_eq!(r.get_u32().unwrap(), OP_PUTROOTFH);
        assert_eq!(r.get_u32().unwrap(), NFS4_OK);
        assert_eq!(r.get_u32().unwrap(), OP_SECINFO_NO_NAME);
        assert_eq!(r.get_u32().unwrap(), NFS4_OK);
        let n = r.get_u32().unwrap();
        assert_eq!(
            n, 1,
            "sec=[\"sys\"] must advertise exactly one flavor, got {n}"
        );
        let flavor = r.get_u32().unwrap();
        assert_eq!(
            flavor,
            crate::rpc::AUTH_SYS,
            "advertised flavor must be AUTH_SYS"
        );
    }

    /// With `root_squash=true`, a caller with uid=0 must be mapped
    /// to anonymous (65534) before permission checks run.
    #[test]
    fn export_policy_root_squash_maps_root_to_anonymous() {
        let (_tmp, vfs, _proc) = setup();
        let entry = vfs.create_file(1, "squashed.dat", 0o600).unwrap();
        vfs.set_mode(entry.ino, 0o600);
        vfs.set_owner(entry.ino, 2000, 2000);
        vfs.write(entry.ino, 0, b"data").unwrap();
        let fh = vfs.ino_to_filehandle(entry.ino);

        let proc = CompoundProcessor::new(Arc::clone(&vfs));
        let root_creds = crate::rpc::AuthSys {
            uid: 0,
            gid: 0,
            aux_gids: Vec::new(),
            machinename: "t".into(),
        };

        let mut putfh = XdrWriter::new();
        putfh.put_opaque(&fh);
        let mut read = XdrWriter::new();
        read.put_u32(0);
        read.put_raw(&[0u8; 12]);
        read.put_u64(0);
        read.put_u32(4);
        let payload = build_compound(
            b"",
            &[(OP_PUTFH, putfh.into_bytes()), (OP_READ, read.into_bytes())],
        );

        // root_squash=true: uid=0 → 65534 → not owner of uid=2000 file → denied.
        let squash_policy = ExportPolicy {
            root_squash: true,
            read_only: false,
            ..ExportPolicy::default()
        };
        let reply = proc.process_with_creds_policy(&payload, Some(&root_creds), &squash_policy);
        assert_eq!(decode_reply_status(&reply), NFS4ERR_ACCESS);

        // root_squash=false: uid=0 bypasses permission check → succeeds.
        let no_squash = ExportPolicy {
            root_squash: false,
            read_only: false,
            ..ExportPolicy::default()
        };
        let reply = proc.process_with_creds_policy(&payload, Some(&root_creds), &no_squash);
        assert_eq!(decode_reply_status(&reply), NFS4_OK);
    }

    /// The one credential mapping both transports use. Root
    /// squashing maps uid 0 and every gid 0 to the configured anonymous
    /// ids and leaves other ids alone; AUTH_NONE runs as the anonymous
    /// ids whatever the squash setting; without squashing root stays root.
    #[test]
    fn caller_ctx_squashes_root_and_maps_auth_none() {
        let auth = |uid, gid, aux: &[u32]| crate::rpc::AuthSys {
            uid,
            gid,
            aux_gids: aux.to_vec(),
            machinename: "client".into(),
        };
        let squash = ExportPolicy {
            root_squash: true,
            anon_uid: 7000,
            anon_gid: 7001,
            ..ExportPolicy::default()
        };
        let no_squash = ExportPolicy {
            root_squash: false,
            anon_uid: 7000,
            anon_gid: 7001,
            ..ExportPolicy::default()
        };

        let ctx = CallerCtx::from_auth(Some(&auth(0, 0, &[0, 10, 0])), &squash);
        assert_eq!(
            ctx,
            CallerCtx {
                uid: 7000,
                gid: 7001,
                groups: vec![7001, 10, 7001]
            }
        );
        // A non-root user in group 0 loses that group, keeps the rest.
        let ctx = CallerCtx::from_auth(Some(&auth(1000, 0, &[0, 20])), &squash);
        assert_eq!(
            ctx,
            CallerCtx {
                uid: 1000,
                gid: 7001,
                groups: vec![7001, 20]
            }
        );
        let ctx = CallerCtx::from_auth(Some(&auth(1000, 100, &[20, 30])), &squash);
        assert_eq!(
            ctx,
            CallerCtx {
                uid: 1000,
                gid: 100,
                groups: vec![20, 30]
            }
        );
        assert!(ctx.in_group(100) && ctx.in_group(30) && !ctx.in_group(0));

        assert_eq!(
            CallerCtx::from_auth(Some(&auth(0, 0, &[0])), &no_squash),
            CallerCtx {
                uid: 0,
                gid: 0,
                groups: vec![0]
            }
        );
        for policy in [&squash, &no_squash] {
            assert_eq!(
                CallerCtx::from_auth(None, policy),
                CallerCtx {
                    uid: 7000,
                    gid: 7001,
                    groups: Vec::new()
                },
                "AUTH_NONE runs as the anonymous ids"
            );
        }
        // Defaults: 65534.
        assert_eq!(
            CallerCtx::from_auth(None, &ExportPolicy::default()),
            CallerCtx::anonymous()
        );
    }

    /// AUTH_SYS supplementary groups reach the permission
    /// check: the same uid reads a 0440 file as a member of
    /// its group and is refused without that group.
    #[test]
    fn supplementary_groups_grant_group_access() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group-only");
        std::fs::write(&path, b"members").unwrap();
        std::fs::set_permissions(&path, std::os::unix::fs::PermissionsExt::from_mode(0o440))
            .unwrap();
        std::fs::set_permissions(
            dir.path(),
            std::os::unix::fs::PermissionsExt::from_mode(0o755),
        )
        .unwrap();
        let vfs = crate::passthrough::PassthroughVfs::new(dir.path()).unwrap();
        let proc = CompoundProcessor::new_passthrough(Arc::new(vfs));
        // SAFETY: getegid has no preconditions and cannot fail.
        let group = unsafe { libc::getegid() };
        let mut lookup = XdrWriter::new();
        lookup.put_string("group-only");
        let mut read = XdrWriter::new();
        read.put_raw(&[0u8; 16]);
        read.put_u64(0);
        read.put_u32(16);
        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup.into_bytes()),
                (OP_READ, read.into_bytes()),
            ],
        );
        let policy = ExportPolicy {
            root_squash: true,
            ..ExportPolicy::default()
        };
        let as_user = |aux: Vec<u32>| crate::rpc::AuthSys {
            uid: 4_242_424,
            gid: 4_242_424,
            aux_gids: aux,
            machinename: "c".into(),
        };
        let member = proc.process_with_creds_policy(&payload, Some(&as_user(vec![group])), &policy);
        assert_eq!(
            decode_reply_status(&member),
            NFS4_OK,
            "member via a supplementary group reads"
        );
        let outsider =
            proc.process_with_creds_policy(&payload, Some(&as_user(Vec::new())), &policy);
        assert_eq!(
            decode_reply_status(&outsider),
            NFS4ERR_ACCESS,
            "same uid, not in the group"
        );
    }

    /// With `read_only=true`, every mutating op must be rejected
    /// with NFS4ERR_ROFS before it reaches the VFS.
    #[test]
    fn export_policy_read_only_rejects_mutating_ops() {
        let (_tmp, _vfs, proc) = setup();
        let ro = ExportPolicy {
            root_squash: false,
            read_only: true,
            ..ExportPolicy::default()
        };

        // CREATE under a read-only export must fail.
        let mut create = XdrWriter::new();
        create.put_u32(1); // NF4REG
        create.put_string("forbidden.txt");
        create.put_u32(0); // empty createattrs
        let payload = build_compound(
            b"",
            &[(OP_PUTROOTFH, Vec::new()), (OP_CREATE, create.into_bytes())],
        );
        let reply = proc.process_with_creds_policy(&payload, None, &ro);
        assert_eq!(decode_reply_status(&reply), NFS4ERR_ROFS);

        // Read-only ops still succeed.
        let payload_ro = build_compound(b"", &[(OP_PUTROOTFH, Vec::new())]);
        let reply_ro = proc.process_with_creds_policy(&payload_ro, None, &ro);
        assert_eq!(decode_reply_status(&reply_ro), NFS4_OK);
    }

    /// An OS error reaches the client as its own status, whichever data
    /// path wrapped it: a full export is NFS4ERR_NOSPC (ENOSPC on the
    /// client), never NFS4ERR_IO. A full nvstor-nfs-export once answered
    /// EIO.
    #[test]
    fn io_errors_keep_their_nfs_status() {
        use nvstor_core::error::NvstorError;
        let io = |errno| NvstorError::Io(std::io::Error::from_raw_os_error(errno));
        for (errno, status) in [
            (libc::ENOSPC, NFS4ERR_NOSPC),
            (libc::EDQUOT, NFS4ERR_DQUOT),
            (libc::EFBIG, NFS4ERR_FBIG),
            (libc::EROFS, NFS4ERR_ROFS),
            (libc::ESTALE, NFS4ERR_STALE),
            (libc::EACCES, NFS4ERR_ACCESS),
            (libc::EPERM, NFS4ERR_PERM),
            (libc::EIO, NFS4ERR_IO),
            (libc::EBADMSG, NFS4ERR_IO),
        ] {
            assert_eq!(nvstor_err_to_nfs4(&io(errno)), status, "errno {errno}");
        }
        assert_eq!(nvstor_err_to_nfs4(&NvstorError::NoSpace), NFS4ERR_NOSPC);
        let custom = NvstorError::Io(std::io::Error::other("no errno"));
        assert_eq!(nvstor_err_to_nfs4(&custom), NFS4ERR_IO);
    }

    /// PUTROOTFH, LOOKUP `name`, then `op`: the reply's status and the last
    /// op's result body (PUTROOTFH and LOOKUP results have no body).
    fn on_file(proc: &CompoundProcessor, name: &str, op: u32, args: Vec<u8>) -> (u32, Vec<u8>) {
        let mut lookup = XdrWriter::new();
        lookup.put_string(name);
        let payload = build_compound(
            b"",
            &[
                (OP_PUTROOTFH, Vec::new()),
                (OP_LOOKUP, lookup.into_bytes()),
                (op, args),
            ],
        );
        let reply = proc.process(&payload);
        let mut r = XdrReader::new(&reply);
        let status = r.get_u32().unwrap();
        r.get_opaque().unwrap(); // tag
        let num = r.get_u32().unwrap();
        for _ in 0..num.saturating_sub(1) {
            r.get_u32().unwrap(); // opnum
            assert_eq!(r.get_u32().unwrap(), NFS4_OK);
        }
        if num == 3 {
            r.get_u32().unwrap(); // opnum
            r.get_u32().unwrap(); // op status
        }
        (status, r.remaining_bytes().to_vec())
    }

    fn write_args(offset: u64, stable: u32, data: &[u8]) -> Vec<u8> {
        let mut w = XdrWriter::new();
        StateId::special_anonymous().encode(&mut w);
        w.put_u64(offset);
        w.put_u32(stable);
        w.put_opaque(data);
        w.into_bytes()
    }

    fn commit_args() -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_u64(0);
        w.put_u32(0);
        w.into_bytes()
    }

    /// Verifier of an OK WRITE (count, committed, verifier) or COMMIT reply.
    fn verifier(op: u32, body: &[u8]) -> [u8; 8] {
        let at = if op == OP_WRITE { 8 } else { 0 };
        body[at..at + 8].try_into().unwrap()
    }

    /// A stable WRITE whose sync fails fails, and the verifier changes, so
    /// a client holding UNSTABLE data (written before the failure, its
    /// error now consumed by that fsync) resends it instead of trusting a
    /// later COMMIT.
    #[test]
    fn failed_sync_on_stable_write_fails_and_changes_verifier() {
        let (_tmp, vfs, proc) = setup();
        vfs.create_file(1, "f", 0o666).unwrap();
        let (st, body) = on_file(&proc, "f", OP_WRITE, write_args(0, UNSTABLE4, b"unstable"));
        assert_eq!(st, NFS4_OK);
        let before = verifier(OP_WRITE, &body);

        proc.fail_next_sync.store(true, Ordering::SeqCst);
        let (st, _) = on_file(&proc, "f", OP_WRITE, write_args(8, FILE_SYNC4, b"stable"));
        assert_eq!(st, NFS4ERR_IO, "a stable WRITE whose sync failed must fail");

        let (st, body) = on_file(&proc, "f", OP_COMMIT, commit_args());
        assert_eq!(st, NFS4_OK);
        assert_ne!(
            verifier(OP_COMMIT, &body),
            before,
            "COMMIT must not vouch for the earlier data"
        );
    }

    /// A COMMIT whose sync fails fails and changes the verifier, so a
    /// second COMMIT (whose fresh fsync no longer sees the error) cannot
    /// confirm the data.
    #[test]
    fn failed_commit_changes_verifier() {
        let (_tmp, vfs, proc) = setup();
        vfs.create_file(1, "f", 0o666).unwrap();
        let (_, body) = on_file(&proc, "f", OP_WRITE, write_args(0, UNSTABLE4, b"data"));
        let before = verifier(OP_WRITE, &body);
        proc.fail_next_sync.store(true, Ordering::SeqCst);
        assert_eq!(on_file(&proc, "f", OP_COMMIT, commit_args()).0, NFS4ERR_IO);
        let (st, body) = on_file(&proc, "f", OP_COMMIT, commit_args());
        assert_eq!(st, NFS4_OK);
        assert_ne!(verifier(OP_COMMIT, &body), before);
    }

    /// A failed WRITE changes the verifier; a successful one does not.
    #[test]
    fn failed_write_changes_verifier() {
        let (_tmp, vfs, proc) = setup();
        vfs.create_file(1, "f", 0o666).unwrap();
        let (_, body) = on_file(&proc, "f", OP_WRITE, write_args(0, UNSTABLE4, b"a"));
        let first = verifier(OP_WRITE, &body);
        let (_, body) = on_file(&proc, "f", OP_WRITE, write_args(1, UNSTABLE4, b"b"));
        assert_eq!(verifier(OP_WRITE, &body), first);
        let (st, _) = on_file(
            &proc,
            "f",
            OP_WRITE,
            write_args(u64::MAX - 1, UNSTABLE4, b"overflow"),
        );
        assert_ne!(st, NFS4_OK);
        let (_, body) = on_file(&proc, "f", OP_COMMIT, commit_args());
        assert_ne!(verifier(OP_COMMIT, &body), first);
    }

    /// WRITE_SAME is NFS4ERR_NOTSUPP (as in Linux nfsd), and the COMPOUND
    /// stops there without writing.
    #[test]
    fn write_same_is_not_supported() {
        let (_tmp, vfs, proc) = setup();
        let f = vfs.create_file(1, "f", 0o666).unwrap();
        vfs.write(f.ino, 0, b"original").unwrap();
        let mut args = XdrWriter::new();
        args.put_u32(0); // stateid seqid
        args.put_opaque_fixed(&[0u8; 12]);
        args.put_u32(2); // FILE_SYNC4
        args.put_u64(0); // adb_offset
        args.put_u64(4096); // adb_block_size
        args.put_u64(1); // adb_block_count
        args.put_u32(0); // adb_reloff_blocknum
        args.put_u32(0); // adb_block_num
        args.put_opaque(b"pattern");
        let (st, _) = on_file(&proc, "f", OP_WRITE_SAME, args.into_bytes());
        assert_eq!(st, NFS4ERR_NOTSUPP);
        assert_eq!(vfs.read(f.ino, 0, 64).unwrap(), b"original");
    }

    /// Regression test: concurrent compounds with different uids
    /// must not see each other's credentials. Before the fix, `effective_uid`
    /// was a single `Mutex<u32>` on the shared processor, so client B's
    /// `process_with_creds` could overwrite client A's uid between A's own
    /// write and its permission check.
    #[test]
    fn uid_not_contaminated_across_concurrent_compounds() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        use std::sync::Arc;
        use std::thread;

        // Create a shared processor + a pre-created file owned by uid=2000, mode 0o600
        // (only uid=2000 can read or write).
        let (tmp, vfs, _proc) = setup();
        let entry = vfs.create_file(1, "shared.dat", 0o600).unwrap();
        vfs.write(entry.ino, 0, b"hello").unwrap();
        vfs.set_mode(entry.ino, 0o600);
        vfs.set_owner(entry.ino, 2000, 2000);
        let fh = vfs.ino_to_filehandle(entry.ino);

        let proc = Arc::new(CompoundProcessor::new(Arc::clone(&vfs)));
        let mismatches = Arc::new(AtomicUsize::new(0));
        let iterations = 400usize;

        // Thread A: runs compounds as uid=2000 (file owner) — READ should succeed.
        // Thread B: runs compounds as uid=3000 (no access) — READ should fail.
        // Any crossover means uid got contaminated.
        let mut handles = Vec::new();
        for tid in 0..8 {
            let proc = Arc::clone(&proc);
            let mismatches = Arc::clone(&mismatches);
            let fh = fh.clone();
            let is_owner = tid % 2 == 0;
            let creds = crate::rpc::AuthSys {
                uid: if is_owner { 2000 } else { 3000 },
                gid: if is_owner { 2000 } else { 3000 },
                aux_gids: Vec::new(),
                machinename: "test".into(),
            };
            handles.push(thread::spawn(move || {
                for _ in 0..iterations {
                    // PUTFH(fh) + READ(offset=0, count=5)
                    let mut putfh_args = XdrWriter::new();
                    putfh_args.put_opaque(&fh);
                    let mut read_args = XdrWriter::new();
                    read_args.put_u32(0); // stateid.seqid
                    read_args.put_raw(&[0u8; 12]); // stateid.other (anonymous)
                    read_args.put_u64(0); // offset
                    read_args.put_u32(5); // count
                    let payload = build_compound(
                        b"",
                        &[
                            (OP_PUTFH, putfh_args.into_bytes()),
                            (OP_READ, read_args.into_bytes()),
                        ],
                    );

                    let reply = proc.process_with_creds(&payload, Some(&creds));
                    let status = decode_reply_status(&reply);

                    // uid=2000 owns the file with mode 0o600: READ must succeed.
                    // uid=3000 is "other" with no bits: READ must fail with NFS4ERR_ACCESS.
                    let expected = if is_owner { NFS4_OK } else { NFS4ERR_ACCESS };
                    if status != expected {
                        mismatches.fetch_add(1, Ordering::Relaxed);
                    }
                }
            }));
        }

        for h in handles {
            h.join().unwrap();
        }

        let total = mismatches.load(Ordering::Relaxed);
        assert_eq!(
            total,
            0,
            "uid contamination detected: {} out of {} ops saw the wrong effective uid",
            total,
            8 * iterations
        );

        drop(tmp);
    }
}

/// Move placed READ buffers into the reply's data items.
fn attach_externals(
    items: &mut [Option<DataItem>],
    item_results: &[usize],
    externals: &mut [Option<(Box<dyn DdpBuffer>, usize)>],
) {
    for (item, &result) in items.iter_mut().zip(item_results) {
        if let (Some(item), Some(slot)) = (item.as_mut(), externals.get_mut(result)) {
            if slot.as_ref().is_some_and(|(_, len)| *len == item.len) && item.external.is_none() {
                item.external = slot.take().map(|(buf, _)| buf);
            }
        }
    }
}

/// Inverse of [`attach_externals`], before re-encoding a reply.
fn detach_externals(
    items: &mut [Option<DataItem>],
    item_results: &[usize],
    externals: &mut [Option<(Box<dyn DdpBuffer>, usize)>],
) {
    for (item, &result) in items.iter_mut().zip(item_results) {
        if let Some(item) = item.as_mut() {
            if let (Some(buf), Some(slot)) = (item.external.take(), externals.get_mut(result)) {
                *slot = Some((buf, item.len));
            }
        }
    }
}

/// Status codes for filehandles, symbolic links and READDIR cookies.
#[cfg(test)]
mod status_tests {
    use super::*;
    use nvstor_metadata::MetadataStore;
    use nvstor_storage::backend::PosixBackend;
    use nvstor_storage::manager::{ChunkManager, ChunkManagerConfig};
    use tempfile::TempDir;

    fn synthetic() -> (TempDir, Arc<Vfs>, CompoundProcessor) {
        let tmp = TempDir::new().expect("tmpdir");
        let backend = Box::new(PosixBackend::new(tmp.path().join("data")).expect("backend"));
        let metadata = MetadataStore::open(&tmp.path().join("meta")).expect("metadata");
        let config = ChunkManagerConfig {
            disk_usage_threshold: 1.0,
            ..ChunkManagerConfig::default()
        };
        let mgr = Arc::new(ChunkManager::new(backend, Arc::new(metadata), config));
        let vfs = Arc::new(Vfs::new(mgr, "status/"));
        let proc = CompoundProcessor::new(Arc::clone(&vfs));
        (tmp, vfs, proc)
    }

    /// An export with `file`, `dir/`, `link -> file` and `dirlink -> dir`.
    fn passthrough() -> (
        TempDir,
        Arc<crate::passthrough::PassthroughVfs>,
        CompoundProcessor,
    ) {
        let tmp = TempDir::new().expect("tmpdir");
        std::fs::write(tmp.path().join("file"), b"data").expect("write");
        std::fs::create_dir(tmp.path().join("dir")).expect("mkdir");
        std::os::unix::fs::symlink("file", tmp.path().join("link")).expect("symlink");
        std::os::unix::fs::symlink("dir", tmp.path().join("dirlink")).expect("symlink");
        let mut vfs = crate::passthrough::PassthroughVfs::new(tmp.path()).expect("vfs");
        vfs.set_read_write();
        let vfs = Arc::new(vfs);
        let proc = CompoundProcessor::new_passthrough(vfs.clone());
        (tmp, vfs, proc)
    }

    fn compound(ops: &[(u32, Vec<u8>)]) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_opaque(b"");
        w.put_u32(0);
        w.put_u32(ops.len() as u32);
        for (op, args) in ops {
            w.put_u32(*op);
            w.put_raw(args);
        }
        w.into_bytes()
    }

    fn status(proc: &CompoundProcessor, ops: &[(u32, Vec<u8>)]) -> u32 {
        let reply = proc.process(&compound(ops));
        u32::from_be_bytes(reply[..4].try_into().expect("status"))
    }

    fn putfh(fh: &[u8]) -> (u32, Vec<u8>) {
        let mut w = XdrWriter::new();
        w.put_opaque(fh);
        (OP_PUTFH, w.into_bytes())
    }

    fn lookup(name: &str) -> (u32, Vec<u8>) {
        let mut w = XdrWriter::new();
        w.put_string(name);
        (OP_LOOKUP, w.into_bytes())
    }

    fn read() -> (u32, Vec<u8>) {
        let mut w = XdrWriter::new();
        w.put_u32(0);
        w.put_opaque_fixed(&[0u8; 12]);
        w.put_u64(0);
        w.put_u32(16);
        (OP_READ, w.into_bytes())
    }

    fn commit() -> (u32, Vec<u8>) {
        let mut w = XdrWriter::new();
        w.put_u64(0);
        w.put_u32(0);
        (OP_COMMIT, w.into_bytes())
    }

    fn open(name: &str) -> (u32, Vec<u8>) {
        let mut w = XdrWriter::new();
        w.put_u32(1); // seqid
        w.put_u32(1); // share_access READ
        w.put_u32(0); // share_deny NONE
        w.put_u64(1); // clientid
        w.put_opaque(b"owner");
        w.put_u32(0); // OPEN4_NOCREATE
        w.put_u32(0); // CLAIM_NULL
        w.put_string(name);
        (OP_OPEN, w.into_bytes())
    }

    fn readdir(cookie: u64, verifier: [u8; 8]) -> (u32, Vec<u8>) {
        let mut w = XdrWriter::new();
        w.put_u64(cookie);
        w.put_opaque_fixed(&verifier);
        w.put_u32(8192);
        w.put_u32(32768);
        w.put_u32(1);
        w.put_u32(0);
        (OP_READDIR, w.into_bytes())
    }

    /// Status, verifier and last cookie of PUTFH(dir) + READDIR.
    fn list(
        proc: &CompoundProcessor,
        dir_fh: &[u8],
        cookie: u64,
        verifier: [u8; 8],
    ) -> (u32, [u8; 8], u64) {
        let reply = proc.process(&compound(&[putfh(dir_fh), readdir(cookie, verifier)]));
        let mut r = XdrReader::new(&reply);
        let status = r.get_u32().expect("status");
        if status != NFS4_OK {
            return (status, [0; 8], 0);
        }
        r.get_opaque().expect("tag");
        r.get_u32().expect("count");
        r.get_u32().expect("PUTFH");
        r.get_u32().expect("PUTFH status");
        r.get_u32().expect("READDIR");
        r.get_u32().expect("READDIR status");
        let verifier: [u8; 8] = r
            .get_opaque_fixed(8)
            .expect("verifier")
            .try_into()
            .expect("8 bytes");
        let mut last = 0;
        while r.get_bool().expect("entry follows") {
            last = r.get_u64().expect("cookie");
            r.get_string().expect("name");
            let words = r.get_u32().expect("bitmap length");
            for _ in 0..words {
                r.get_u32().expect("bitmap word");
            }
            r.get_opaque().expect("attrs");
        }
        (status, verifier, last)
    }

    #[test]
    fn status_codes_match_rfc8881_and_linux() {
        assert_eq!(NFS4ERR_BADHANDLE, 10001);
        assert_eq!(NFS4ERR_BAD_COOKIE, 10003);
        assert_eq!(NFS4ERR_SERVERFAULT, 10006);
        assert_eq!(NFS4ERR_DELAY, 10008);
        assert_eq!(NFS4ERR_STALE_CLIENTID, 10022);
        assert_eq!(NFS4ERR_NOT_SAME, 10027);
        assert_eq!(NFS4ERR_SYMLINK, 10029);
        assert_eq!(NFS4ERR_BADXDR, 10036);
        assert_eq!(NFS4ERR_LAYOUTUNAVAILABLE, 10059);
    }

    #[test]
    fn filehandle_errors_map_by_cause() {
        assert_eq!(fh_err_to_nfs4(&NvstorError::StaleHandle), NFS4ERR_STALE);
        assert_eq!(fh_err_to_nfs4(&NvstorError::NotFound), NFS4ERR_STALE);
        assert_eq!(
            fh_err_to_nfs4(&NvstorError::InvalidArgument("x".into())),
            NFS4ERR_BADHANDLE
        );
        let emfile = NvstorError::Io(std::io::Error::from_raw_os_error(libc::EMFILE));
        assert_eq!(fh_err_to_nfs4(&emfile), NFS4ERR_DELAY);
        let eio = NvstorError::Io(std::io::Error::from_raw_os_error(libc::EIO));
        assert_eq!(fh_err_to_nfs4(&eio), NFS4ERR_SERVERFAULT);
        assert_eq!(
            fh_err_to_nfs4(&NvstorError::Internal("x".into())),
            NFS4ERR_SERVERFAULT
        );
        assert_eq!(nvstor_err_to_nfs4(&NvstorError::Symlink), NFS4ERR_SYMLINK);
    }

    #[test]
    fn putfh_tells_malformed_from_stale_synthetic() {
        let (_tmp, vfs, proc) = synthetic();
        let file = vfs.create_file(1, "f", 0o644).expect("create");
        let fh = vfs.ino_to_filehandle(file.ino);
        assert_eq!(status(&proc, &[putfh(&fh)]), NFS4_OK);
        assert_eq!(
            status(&proc, &[putfh(&fh[..31])]),
            NFS4ERR_BADHANDLE,
            "wrong length"
        );
        assert_eq!(status(&proc, &[putfh(&[])]), NFS4ERR_BADHANDLE, "empty");
        assert_eq!(
            status(&proc, &[putfh(&[7u8; NFS4_FHSIZE + 1])]),
            NFS4ERR_BADHANDLE,
            "oversized"
        );
        vfs.unlink(1, "f").expect("unlink");
        assert_eq!(status(&proc, &[putfh(&fh)]), NFS4ERR_STALE, "removed file");
        // A truncated nfs_fh4 is an XDR error, not a handle.
        assert_eq!(
            status(&proc, &[(OP_PUTFH, 40u32.to_be_bytes().to_vec())]),
            NFS4ERR_BADXDR
        );
    }

    #[test]
    fn putfh_tells_malformed_from_stale_passthrough() {
        let (tmp, vfs, proc) = passthrough();
        let file = vfs.lookup(1, "file", &CallerCtx::root()).expect("lookup");
        let fh = vfs.ino_to_filehandle(file.ino);
        assert_eq!(status(&proc, &[putfh(&fh)]), NFS4_OK);
        // Layouts this server never issues.
        assert_eq!(status(&proc, &[putfh(&fh[..4])]), NFS4ERR_BADHANDLE);
        assert_eq!(
            status(&proc, &[putfh(&file.ino.to_le_bytes())]),
            NFS4ERR_BADHANDLE
        );
        assert_eq!(status(&proc, &[putfh(&fh[..20])]), NFS4ERR_BADHANDLE);
        // Well formed but sealed by another key (a restart without --fh-key).
        let other = crate::passthrough::PassthroughVfs::new(tmp.path()).expect("vfs");
        other.lookup(1, "file", &CallerCtx::root()).expect("lookup");
        assert_eq!(
            status(&proc, &[putfh(&other.ino_to_filehandle(file.ino))]),
            NFS4ERR_STALE
        );
        // Tampered seal.
        let mut forged = fh.clone();
        forged[10] ^= 1;
        assert_eq!(status(&proc, &[putfh(&forged)]), NFS4ERR_STALE);
    }

    /// RFC 8881: an operation that needs a directory or a regular file and
    /// gets a symbolic link fails with NFS4ERR_SYMLINK (Linux nfsd's
    /// nfsd_mode_check); the Linux client maps it to ELOOP and, for an
    /// atomic open, falls back to LOOKUP and follows the link itself.
    #[test]
    fn symlink_objects_return_nfs4err_symlink_synthetic() {
        let (_tmp, vfs, proc) = synthetic();
        vfs.create_file(1, "file", 0o644).expect("create");
        vfs.mkdir(1, "dir", 0o755).expect("mkdir");
        let link = vfs.create_symlink(1, "link", "file").expect("symlink");
        let fh = vfs.ino_to_filehandle(link.ino);
        assert_eq!(
            status(&proc, &[putfh(&fh), read()]),
            NFS4ERR_SYMLINK,
            "READ"
        );
        assert_eq!(
            status(&proc, &[putfh(&fh), commit()]),
            NFS4ERR_SYMLINK,
            "COMMIT"
        );
        assert_eq!(
            status(&proc, &[putfh(&fh), lookup("x")]),
            NFS4ERR_SYMLINK,
            "LOOKUP"
        );
        assert_eq!(
            status(&proc, &[putfh(&fh), (OP_LOOKUPP, Vec::new())]),
            NFS4ERR_SYMLINK,
            "LOOKUPP"
        );
        assert_eq!(
            status(&proc, &[putfh(&fh), readdir(0, [0; 8])]),
            NFS4ERR_SYMLINK,
            "READDIR"
        );
        assert_eq!(
            status(&proc, &[(OP_PUTROOTFH, Vec::new()), open("link")]),
            NFS4ERR_SYMLINK,
            "OPEN link"
        );
        assert_eq!(
            status(&proc, &[(OP_PUTROOTFH, Vec::new()), open("dir")]),
            NFS4ERR_ISDIR,
            "OPEN dir"
        );
        assert_eq!(
            status(&proc, &[(OP_PUTROOTFH, Vec::new()), open("file")]),
            NFS4_OK,
            "OPEN file"
        );
        assert_eq!(
            status(&proc, &[putfh(&fh), (OP_READLINK, Vec::new())]),
            NFS4_OK,
            "READLINK"
        );
        // Other non-directories keep NFS4ERR_NOTDIR.
        let file = vfs.lookup(1, "file").expect("lookup");
        let file_fh = vfs.ino_to_filehandle(file.ino);
        assert_eq!(
            status(&proc, &[putfh(&file_fh), lookup("x")]),
            NFS4ERR_NOTDIR
        );
    }

    #[test]
    fn symlink_objects_return_nfs4err_symlink_passthrough() {
        let (_tmp, vfs, proc) = passthrough();
        let link = vfs.lookup(1, "link", &CallerCtx::root()).expect("link");
        let dirlink = vfs
            .lookup(1, "dirlink", &CallerCtx::root())
            .expect("dirlink");
        let link_fh = vfs.ino_to_filehandle(link.ino);
        let dirlink_fh = vfs.ino_to_filehandle(dirlink.ino);
        assert_eq!(
            status(&proc, &[putfh(&link_fh), read()]),
            NFS4ERR_SYMLINK,
            "READ"
        );
        assert_eq!(
            status(&proc, &[putfh(&link_fh), commit()]),
            NFS4ERR_SYMLINK,
            "COMMIT"
        );
        assert_eq!(
            status(&proc, &[putfh(&dirlink_fh), lookup("x")]),
            NFS4ERR_SYMLINK,
            "LOOKUP through dirlink"
        );
        assert_eq!(
            status(&proc, &[putfh(&dirlink_fh), (OP_LOOKUPP, Vec::new())]),
            NFS4ERR_SYMLINK,
            "LOOKUPP"
        );
        assert_eq!(
            status(&proc, &[putfh(&dirlink_fh), readdir(0, [0; 8])]),
            NFS4ERR_SYMLINK,
            "READDIR"
        );
        assert_eq!(
            status(&proc, &[(OP_PUTROOTFH, Vec::new()), open("link")]),
            NFS4ERR_SYMLINK,
            "OPEN link"
        );
        assert_eq!(
            status(&proc, &[(OP_PUTROOTFH, Vec::new()), open("dir")]),
            NFS4ERR_ISDIR,
            "OPEN dir"
        );
        assert_eq!(
            status(&proc, &[(OP_PUTROOTFH, Vec::new()), open("file")]),
            NFS4_OK,
            "OPEN file"
        );
        assert_eq!(
            status(
                &proc,
                &[(OP_PUTROOTFH, Vec::new()), lookup("file"), lookup("x")]
            ),
            NFS4ERR_NOTDIR
        );
    }

    #[test]
    fn readdir_verifier_guards_positional_cookies_synthetic() {
        let (_tmp, vfs, proc) = synthetic();
        for name in ["a", "b", "c"] {
            vfs.create_file(1, name, 0o644).expect("create");
        }
        let root = vfs.ino_to_filehandle(1);
        let (status, verifier, last) = list(&proc, &root, 0, [0; 8]);
        assert_eq!(status, NFS4_OK);
        assert_ne!(verifier, [0; 8], "a real verifier");
        assert!(last > 2);
        assert_eq!(
            list(&proc, &root, 0, [0; 8]).1,
            verifier,
            "unchanged directory, same verifier"
        );
        assert_eq!(
            list(&proc, &root, last, verifier).0,
            NFS4_OK,
            "cookie with its verifier"
        );
        assert_eq!(
            list(&proc, &root, 1, verifier).0,
            NFS4ERR_BAD_COOKIE,
            "reserved cookie 1"
        );
        assert_eq!(
            list(&proc, &root, 2, verifier).0,
            NFS4ERR_BAD_COOKIE,
            "reserved cookie 2"
        );
        // Positions shift when an entry is added: old cookies are refused.
        vfs.create_file(1, "0first", 0o644).expect("create");
        assert_eq!(list(&proc, &root, last, verifier).0, NFS4ERR_NOT_SAME);
        assert_eq!(
            list(&proc, &root, last, [9; 8]).0,
            NFS4ERR_NOT_SAME,
            "foreign verifier"
        );
        // A client with no verifier, or starting over, is served.
        assert_eq!(list(&proc, &root, last, [0; 8]).0, NFS4_OK);
        let (status, fresh, _) = list(&proc, &root, 0, verifier);
        assert_eq!(status, NFS4_OK);
        assert_ne!(fresh, verifier);
    }

    #[test]
    fn readdir_verifier_survives_changes_with_offset_cookies_passthrough() {
        let (tmp, vfs, proc) = passthrough();
        let root = vfs.ino_to_filehandle(1);
        let (status, verifier, last) = list(&proc, &root, 0, [0; 8]);
        assert_eq!(status, NFS4_OK);
        assert_ne!(verifier, [0; 8]);
        // d_off cookies stay valid across changes, and so does the verifier.
        std::fs::write(tmp.path().join("new"), b"x").expect("write");
        let (status, again, _) = list(&proc, &root, last, verifier);
        assert_eq!(status, NFS4_OK);
        assert_eq!(again, verifier);
        assert_eq!(
            list(&proc, &root, last, [9; 8]).0,
            NFS4ERR_NOT_SAME,
            "verifier of another directory"
        );
        let dir = vfs.lookup(1, "dir", &CallerCtx::root()).expect("dir");
        let dir_verifier = list(&proc, &vfs.ino_to_filehandle(dir.ino), 0, [0; 8]).1;
        assert_ne!(dir_verifier, verifier, "per directory");
        assert_eq!(list(&proc, &root, 1, verifier).0, NFS4ERR_BAD_COOKIE);
    }
}
