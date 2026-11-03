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

//! Protocol exerciser — drives every NFS op the dispatcher knows about over
//! a real TCP connection, using PassthroughVfs (the production export
//! backend). At the end every `op_calls[opnum]` must be > 0.
//!
//! Why this exists: a real Linux client only sends ~30 of the 56 dispatched
//! ops during normal shell commands. The remaining 26 are recovery ops,
//! v4.0-only ops, pNFS, delegations, etc. The "in the open we can't control
//! what client sends" guarantee requires every dispatched op to be
//! exercised — this test fills the gap the kernel-driven smoketest leaves.
//!
//! Failure mode: any op left at count=0 fails the test.

#![allow(clippy::too_many_arguments)]

use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};

use nvstor_nfs::nfs4::{self, CompoundProcessor};
use nvstor_nfs::passthrough::PassthroughVfs;
use nvstor_nfs::rpc;
use nvstor_nfs::server::{NfsServer, NfsServerConfig};
use nvstor_nfs::xdr::{XdrReader, XdrWriter};

// ---------------------------------------------------------------------------
// RPC client mini-harness (mirrors integration.rs but self-contained).
// ---------------------------------------------------------------------------

struct Client {
    stream: TcpStream,
    xid: u32,
}

impl Client {
    async fn connect(addr: std::net::SocketAddr) -> Self {
        let stream = TcpStream::connect(addr).await.expect("connect");
        Self { stream, xid: 1 }
    }

    fn next_xid(&mut self) -> u32 {
        self.xid += 1;
        self.xid
    }

    async fn send_record(&mut self, data: &[u8]) {
        let header = 0x8000_0000 | (data.len() as u32);
        self.stream.write_u32(header).await.unwrap();
        self.stream.write_all(data).await.unwrap();
        self.stream.flush().await.unwrap();
    }

    async fn recv_record(&mut self) -> Vec<u8> {
        let header = self.stream.read_u32().await.unwrap();
        let len = (header & 0x7FFF_FFFF) as usize;
        let mut data = vec![0u8; len];
        self.stream.read_exact(&mut data).await.unwrap();
        data
    }

    fn build_call(&mut self, proc_num: u32, payload: &[u8]) -> Vec<u8> {
        let xid = self.next_xid();
        let mut w = XdrWriter::new();
        w.put_u32(xid);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(rpc::NFS_PROGRAM);
        w.put_u32(rpc::NFS_V4);
        w.put_u32(proc_num);
        w.put_u32(rpc::AUTH_SYS);
        // The export checks each request against these credentials: act as
        // the user who owns the exported tempdir (root would be squashed).
        let mut cred = XdrWriter::new();
        cred.put_u32(0);
        cred.put_string("nvstor-exerciser");
        // SAFETY: geteuid / getegid have no preconditions and cannot fail.
        cred.put_u32(unsafe { libc::geteuid() });
        cred.put_u32(unsafe { libc::getegid() });
        cred.put_u32(0);
        w.put_opaque(&cred.into_bytes());
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.put_raw(payload);
        w.into_bytes()
    }

    async fn compound(&mut self, minorversion: u32, ops: Vec<(u32, Vec<u8>)>) -> Reply {
        let mut w = XdrWriter::new();
        w.put_string("");
        w.put_u32(minorversion);
        w.put_u32(ops.len() as u32);
        for (opnum, args) in &ops {
            w.put_u32(*opnum);
            w.put_raw(args);
        }
        let payload = w.into_bytes();
        let call = self.build_call(1, &payload);
        self.send_record(&call).await;
        let record = self.recv_record().await;
        Reply::parse(&record)
    }
}

#[allow(dead_code)]
struct Reply {
    xid: u32,
    compound_status: u32,
    num_results: u32,
    results_data: Vec<u8>,
}

impl Reply {
    fn parse(record: &[u8]) -> Self {
        let mut r = XdrReader::new(record);
        let xid = r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        r.get_u32().unwrap();
        let compound_status = r.get_u32().unwrap();
        r.get_opaque().unwrap();
        let num_results = r.get_u32().unwrap();
        let results_data = r.remaining_bytes().to_vec();
        Self {
            xid,
            compound_status,
            num_results,
            results_data,
        }
    }
}

// ---------------------------------------------------------------------------
// Setup: a real PassthroughVfs over a tempdir, behind a real TCP listener.
// ---------------------------------------------------------------------------

struct Harness {
    _tmp: tempfile::TempDir,
    addr: std::net::SocketAddr,
    metrics: Arc<nvstor_nfs::metrics::NfsMetrics>,
}

async fn start() -> Harness {
    let tmp = tempfile::TempDir::new().expect("tempdir");
    let mut vfs = PassthroughVfs::new(tmp.path()).expect("passthrough vfs");
    vfs.set_read_write();
    let proc = Arc::new(CompoundProcessor::new_passthrough(Arc::new(vfs)));
    let metrics = proc.metrics_arc();

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let server = NfsServer::new(proc, NfsServerConfig::default());
    tokio::spawn(async move {
        let _ = server.run(listener).await;
    });
    tokio::time::sleep(Duration::from_millis(50)).await;
    Harness {
        _tmp: tmp,
        addr,
        metrics,
    }
}

// ---------------------------------------------------------------------------
// Op-arg builders.
// ---------------------------------------------------------------------------

fn lookup_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn getattr_args(word0: u32, word1: u32) -> Vec<u8> {
    let mut w = XdrWriter::new();
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
    w.into_bytes()
}

fn access_args(bits: u32) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(bits);
    w.into_bytes()
}

fn create_file_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // NF4REG
    w.put_string(name);
    w.put_u32(0);
    w.put_opaque(&[]);
    w.into_bytes()
}

fn create_dir_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(2); // NF4DIR
    w.put_string(name);
    w.put_u32(0);
    w.put_opaque(&[]);
    w.into_bytes()
}

fn create_symlink_args(name: &str, target: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(5); // NF4LNK
    w.put_string(target);
    w.put_string(name);
    w.put_u32(0);
    w.put_opaque(&[]);
    w.into_bytes()
}

fn write_args(offset: u64, data: &[u8]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0);
    w.put_raw(&[0u8; 12]);
    w.put_u64(offset);
    w.put_u32(2);
    w.put_opaque(data);
    w.into_bytes()
}

fn read_args(offset: u64, count: u32) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0);
    w.put_raw(&[0u8; 12]);
    w.put_u64(offset);
    w.put_u32(count);
    w.into_bytes()
}

fn commit_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0);
    w.put_u32(0);
    w.into_bytes()
}

fn remove_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn rename_args(old: &str, new: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(old);
    w.put_string(new);
    w.into_bytes()
}

fn link_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn readdir_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0); // cookie
    w.put_raw(&[0u8; 8]); // verifier
    w.put_u32(8192); // dircount
    w.put_u32(8192); // maxcount
    w.put_u32(2);
    w.put_u32(0);
    w.put_u32(0x10); // attr bitmap (FILEID)
    w.into_bytes()
}

fn setattr_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]); // stateid (anonymous)
                           // empty attr bitmap → no actual change requested. The dispatcher still
                           // counts the op; that's all we need for coverage.
    w.put_u32(0);
    w.put_opaque(&[]);
    w.into_bytes()
}

fn open_create_args(seqid: u32, owner: &[u8], file_name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(seqid);
    w.put_u32(0x03); // share_access=BOTH
    w.put_u32(0x00);
    w.put_u64(0);
    w.put_opaque(owner);
    // openhow = OPEN4_CREATE (1) + UNCHECKED4 (0) + empty bitmap + empty attrs.
    w.put_u32(1);
    w.put_u32(0); // UNCHECKED4
    w.put_u32(0); // attr bitmap
    w.put_opaque(&[]);
    w.put_u32(0); // CLAIM_NULL
    w.put_string(file_name);
    w.into_bytes()
}

fn close_args(seqid: u32, stateid: &[u8; 16]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(seqid);
    w.put_raw(stateid);
    w.into_bytes()
}

fn open_downgrade_args(seqid: u32, stateid: &[u8; 16]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(stateid);
    w.put_u32(seqid);
    w.put_u32(0x01); // share_access
    w.put_u32(0x00); // share_deny
    w.into_bytes()
}

fn open_confirm_args(stateid: &[u8; 16], seqid: u32) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(stateid);
    w.put_u32(seqid);
    w.into_bytes()
}

fn lock_args(reclaim: bool, owner: &[u8]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // READ_LT
    w.put_u32(if reclaim { 1 } else { 0 });
    w.put_u64(0); // offset
    w.put_u64(u64::MAX); // length
    w.put_u32(1); // new_lock_owner=true
    w.put_u32(0); // open_seqid
    w.put_raw(&[0u8; 16]); // open_stateid
    w.put_u32(0); // lock_seqid
    w.put_u64(0); // clientid
    w.put_opaque(owner);
    w.into_bytes()
}

fn lockt_args(owner: &[u8]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // READ_LT
    w.put_u64(0);
    w.put_u64(u64::MAX);
    w.put_u64(0); // clientid
    w.put_opaque(owner);
    w.into_bytes()
}

fn locku_args(stateid: &[u8; 16]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // READ_LT
    w.put_u32(0); // seqid
    w.put_raw(stateid);
    w.put_u64(0);
    w.put_u64(u64::MAX);
    w.into_bytes()
}

fn allocate_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]); // stateid
    w.put_u64(0); // offset
    w.put_u64(4096); // length
    w.into_bytes()
}

fn deallocate_args() -> Vec<u8> {
    allocate_args()
}

fn copy_args(src_offset: u64, dst_offset: u64, count: u64) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]); // src stateid
    w.put_raw(&[0u8; 16]); // dst stateid
    w.put_u64(src_offset);
    w.put_u64(dst_offset);
    w.put_u64(count);
    w.put_u32(1); // consecutive
    w.put_u32(1); // synchronous
    w.put_u32(0); // 0 source servers (netloc list)
    w.into_bytes()
}

fn clone_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]);
    w.put_raw(&[0u8; 16]);
    w.put_u64(0);
    w.put_u64(0);
    w.put_u64(0);
    w.into_bytes()
}

fn seek_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]);
    w.put_u64(0);
    w.put_u32(0); // SEEK_DATA
    w.into_bytes()
}

fn io_advise_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]);
    w.put_u64(0);
    w.put_u64(4096);
    // Hint bitmap (1 word, NORMAL hint).
    w.put_u32(1);
    w.put_u32(1 << 1);
    w.into_bytes()
}

fn delegreturn_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]);
    w.into_bytes()
}

fn delegpurge_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0);
    w.into_bytes()
}

fn release_lockowner_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0);
    w.put_opaque(b"exerciser-lockowner");
    w.into_bytes()
}

fn renew_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0);
    w.into_bytes()
}

fn setclientid_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0xAA; 8]);
    w.put_opaque(b"exerciser-v40-id");
    w.put_u32(0);
    w.put_string("tcp");
    w.put_string("127.0.0.1.8.1");
    w.put_u32(0);
    w.into_bytes()
}

fn setclientid_confirm_args(clientid: u64) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(clientid);
    w.put_raw(&[0u8; 8]);
    w.into_bytes()
}

fn openattr_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // createdir = false
    w.into_bytes()
}

fn secinfo_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn secinfo_no_name_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // SECINFO_STYLE4_CURRENT_FH
    w.into_bytes()
}

fn test_stateid_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // 1 stateid
    w.put_raw(&[0u8; 16]);
    w.into_bytes()
}

fn free_stateid_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]);
    w.into_bytes()
}

fn bind_conn_to_session_args(session_id: &[u8]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(session_id);
    w.put_u32(0);
    w.put_u32(0);
    w.into_bytes()
}

fn backchannel_ctl_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // cb_program
    w.put_u32(0); // 0 sec_parms
    w.into_bytes()
}

fn layoutget_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // signal_layout_avail
    w.put_u32(1); // layout_type = LAYOUT4_NFSV4_1_FILES
    w.put_u32(2); // iomode = LAYOUTIOMODE4_RW
    w.put_u64(0);
    w.put_u64(u64::MAX);
    w.put_u64(0);
    w.put_raw(&[0u8; 16]); // stateid
    w.put_u32(8192); // maxcount
    w.into_bytes()
}

fn layoutcommit_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0);
    w.put_u64(4096);
    w.put_u32(0);
    w.put_raw(&[0u8; 16]); // stateid
    w.put_u32(0); // last_write_offset present? false
    w.put_u32(0); // time_modify present? false
    w.put_u32(1); // layout_update.layouttype
    w.put_opaque(&[]); // layout body
    w.into_bytes()
}

fn layoutreturn_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // reclaim
    w.put_u32(1); // layout_type
    w.put_u32(2); // iomode
    w.put_u32(1); // return_type = LAYOUTRETURN4_FILE
                  // file body
    w.put_u64(0);
    w.put_u64(u64::MAX);
    w.put_raw(&[0u8; 16]);
    w.put_opaque(&[]);
    w.into_bytes()
}

fn getdeviceinfo_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]); // device_id (16 bytes)
    w.put_u32(1); // layout_type
    w.put_u32(8192); // maxcount
    w.put_u32(0); // notify_types bitmap len
    w.into_bytes()
}

fn getdevicelist_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // layout_type
    w.put_u32(16); // maxdevices
    w.put_u64(0); // cookie
    w.put_raw(&[0u8; 8]); // verifier
    w.into_bytes()
}

// ----- v4.2 ops added later -----

fn getxattr_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn setxattr_args(name: &str, value: &[u8], option: u32) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(option);
    w.put_string(name);
    w.put_opaque(value);
    w.into_bytes()
}

fn listxattrs_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0); // cookie
    w.put_u32(8192); // maxcount
    w.into_bytes()
}

fn removexattr_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn read_plus_args(offset: u64, count: u32) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]); // stateid
    w.put_u64(offset);
    w.put_u32(count);
    w.into_bytes()
}

fn write_same_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]); // stateid
    w.put_u32(2); // FILE_SYNC4
                  // app_data_block: offset, block_size, block_count, reloff, pattern_size,
                  // pattern (opaque).
    w.put_u64(0); // offset
    w.put_u64(64); // block_size
    w.put_u64(1); // block_count = 1 block
    w.put_u32(0); // reloff
    w.put_u32(4); // pattern_size
    w.put_opaque(b"abcd");
    w.into_bytes()
}

fn copy_notify_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]); // stateid
    w.put_opaque(b"127.0.0.1"); // dst_server netaddr (just a placeholder)
    w.into_bytes()
}

fn offload_cancel_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]);
    w.into_bytes()
}

fn offload_status_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(&[0u8; 16]);
    w.into_bytes()
}

fn layouterror_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0);
    w.put_u64(4096);
    w.put_raw(&[0u8; 16]); // stateid
    w.put_u32(0); // 0 device_error4 entries
    w.into_bytes()
}

fn layoutstats_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0);
    w.put_u64(4096);
    w.put_raw(&[0u8; 16]); // stateid
                           // io_info4 read: count + bytes + delay
    w.put_u64(0);
    w.put_u64(0);
    w.put_u64(0);
    // io_info4 write
    w.put_u64(0);
    w.put_u64(0);
    w.put_u64(0);
    w.put_raw(&[0u8; 16]); // device_id
    w.put_u32(0);
    w.put_opaque(&[]); // layoutupdate4 hint
    w.put_u32(1);
    w.put_opaque(&[]); // layoutupdate4 stats
    w.into_bytes()
}

fn get_dir_delegation_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // signal
    w.put_u32(0); // empty notify bitmap
    w.put_u32(0);
    w.put_u32(0); // dir_notif_delay
    w.put_u32(0);
    w.put_u32(0); // dir_entry_notif_delay
    w.into_bytes()
}

fn want_delegation_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // OPEN_DELEGATE_READ
    w.put_u32(0); // CLAIM_NULL
    w.put_string("does-not-matter");
    w.into_bytes()
}

fn set_ssv_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_opaque(b"ssv-payload");
    w.put_opaque(&[0u8; 8]);
    w.into_bytes()
}

// ---------------------------------------------------------------------------
// The exerciser test
// ---------------------------------------------------------------------------

#[tokio::test]
async fn protocol_exerciser_drives_every_op() {
    let h = start().await;
    let mut c = Client::connect(h.addr).await;

    // ------------------------------------------------------------------
    // v4.1 session setup. Drives EXCHANGE_ID, CREATE_SESSION, SEQUENCE,
    // RECLAIM_COMPLETE.
    // ------------------------------------------------------------------
    let mut eid_args = XdrWriter::new();
    eid_args.put_raw(&[0xCC; 8]);
    eid_args.put_opaque(b"exerciser-v41");
    eid_args.put_u32(0x30000);
    eid_args.put_u32(0);
    eid_args.put_u32(0);
    let reply = c
        .compound(1, vec![(nfs4::OP_EXCHANGE_ID, eid_args.into_bytes())])
        .await;
    assert_eq!(reply.compound_status, 0);
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    let clientid = r.get_u64().unwrap();

    let mut cs = XdrWriter::new();
    cs.put_u64(clientid);
    cs.put_u32(1);
    cs.put_u32(0);
    for _ in 0..2 {
        cs.put_u32(0);
        cs.put_u32(1048576);
        cs.put_u32(1048576);
        cs.put_u32(1048576);
        cs.put_u32(64);
        cs.put_u32(16);
        cs.put_u32(0);
    }
    cs.put_u32(0);
    cs.put_u32(0);
    let reply = c
        .compound(1, vec![(nfs4::OP_CREATE_SESSION, cs.into_bytes())])
        .await;
    assert_eq!(reply.compound_status, 0);
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    let session_id = r.get_opaque_fixed(16).unwrap().to_vec();

    let mut slot_seq: u32 = 1;

    fn seq_op(sid: &[u8], slot: u32, seq: u32) -> (u32, Vec<u8>) {
        let mut w = XdrWriter::new();
        w.put_raw(sid);
        w.put_u32(seq); // sequenceid
        w.put_u32(slot); // slotid
        w.put_u32(slot); // highest_slotid
        w.put_u32(0); // cachethis
        (nfs4::OP_SEQUENCE, w.into_bytes())
    }

    // RECLAIM_COMPLETE.
    let mut rc = XdrWriter::new();
    rc.put_u32(0);
    let s = seq_op(&session_id, 0, slot_seq);
    slot_seq += 1;
    let reply = c
        .compound(1, vec![s, (nfs4::OP_RECLAIM_COMPLETE, rc.into_bytes())])
        .await;
    assert_eq!(reply.compound_status, 0);

    // Helper closure that wraps a list of ops in a SEQUENCE-prefixed COMPOUND.
    // Bumps slot_seq each call. Prints the compound_status if non-zero so test
    // failures show which op aborted the dispatch chain.
    macro_rules! send {
        ($ops:expr) => {{
            let s = seq_op(&session_id, 0, slot_seq);
            #[allow(unused_assignments)]
            {
                slot_seq += 1;
            }
            let mut full = vec![s];
            full.extend($ops);
            let opnums: Vec<u32> = full.iter().map(|(op, _)| *op).collect();
            let r = c.compound(1, full).await;
            if r.compound_status != 0 {
                eprintln!("compound status={} for ops {:?}", r.compound_status, opnums);
            }
            r
        }};
    }

    // ------------------------------------------------------------------
    // File-tree setup. Each CREATE goes in its own COMPOUND because after a
    // CREATE the CFH advances to the new node — chaining CREATEs against
    // CFH=root requires re-PUTROOTFH between each.
    // ------------------------------------------------------------------
    macro_rules! mkfile {
        ($name:expr) => {{
            let _ = send!(vec![
                (nfs4::OP_PUTROOTFH, vec![]),
                (nfs4::OP_CREATE, create_file_args($name)),
            ]);
        }};
    }
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_CREATE, create_dir_args("dir1")),
    ]);
    mkfile!("a.txt");
    mkfile!("b.txt");
    mkfile!("c.txt");
    mkfile!("d.txt");
    mkfile!("e.txt");
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_CREATE, create_symlink_args("link.lnk", "a.txt")),
    ]);

    // PUTROOTFH + LOOKUP + GETFH + GETATTR + ACCESS — covers the metadata path.
    let reply = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("a.txt")),
        (nfs4::OP_GETFH, vec![]),
        (nfs4::OP_GETATTR, getattr_args(0x10, 0)),
        (nfs4::OP_ACCESS, access_args(0x3F)),
    ]);
    assert_eq!(reply.compound_status, 0);

    // Extract a.txt's filehandle from the GETFH result for a later PUTFH.
    let a_fh = {
        let mut r = XdrReader::new(&reply.results_data);
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque_fixed(16).unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap(); // PUTROOTFH
        r.get_u32().unwrap();
        r.get_u32().unwrap(); // LOOKUP
        r.get_u32().unwrap();
        r.get_u32().unwrap(); // GETFH opnum + status
        r.get_opaque().unwrap().to_vec()
    };

    // Drive PUTFH explicitly with a known FH.
    let mut putfh_args = XdrWriter::new();
    putfh_args.put_opaque(&a_fh);
    let _ = send!(vec![
        (nfs4::OP_PUTFH, putfh_args.into_bytes()),
        (nfs4::OP_GETATTR, getattr_args(0x10, 0)),
    ]);

    // OPEN (CLAIM_NULL) needs the parent dir as CFH. Don't LOOKUP first.
    let owner = b"exerciser-owner-1".to_vec();
    let reply = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_OPEN, open_create_args(1, &owner, "b.txt")),
        (nfs4::OP_GETFH, vec![]),
    ]);
    assert_eq!(reply.compound_status, 0);

    // Extract OPEN stateid for OPEN_DOWNGRADE/CLOSE.
    let open_stateid: Vec<u8> = {
        let mut r = XdrReader::new(&reply.results_data);
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque_fixed(16).unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque_fixed(16).unwrap().to_vec()
    };
    let mut sid = [0u8; 16];
    sid.copy_from_slice(&open_stateid);

    // Data-path ops. Each separate so a single failure doesn't cascade.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_WRITE, write_args(0, b"hello nvstor")),
        (nfs4::OP_COMMIT, commit_args()),
        (nfs4::OP_READ, read_args(0, 64)),
        (nfs4::OP_SETATTR, setattr_args()),
    ]);

    // LINK: SAVEFH=src(a.txt), CFH=parent(root), then LINK newname.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("a.txt")),
        (nfs4::OP_SAVEFH, vec![]),
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LINK, link_args("a-hard.txt")),
        (nfs4::OP_RESTOREFH, vec![]),
    ]);

    // RENAME: SAVEFH=src dir, CFH=dst dir (both root here).
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_SAVEFH, vec![]),
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_RENAME, rename_args("c.txt", "c-renamed.txt")),
    ]);

    // READLINK: PUTROOTFH then LOOKUP the symlink, in its own COMPOUND.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("link.lnk")),
        (nfs4::OP_READLINK, vec![]),
    ]);

    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_READDIR, readdir_args()),
    ]);

    // VERIFY / NVERIFY require a non-empty bitmap that will match (or not
    // match) actual attrs. Use TYPE attribute (FATTR4_TYPE bit 1, word 0)
    // and supply value=NF4DIR for root → VERIFY succeeds, NVERIFY does too
    // when it doesn't match. We just need the dispatch counter; either
    // status is fine.
    let mut verify_match = XdrWriter::new();
    verify_match.put_u32(1);
    verify_match.put_u32(1 << 1); // bitmap: TYPE
    let mut vals = XdrWriter::new();
    vals.put_u32(2); // NF4DIR=2
    verify_match.put_opaque(&vals.into_bytes());
    let verify_match = verify_match.into_bytes();
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_VERIFY, verify_match.clone()),
    ]);
    let mut verify_mismatch = XdrWriter::new();
    verify_mismatch.put_u32(1);
    verify_mismatch.put_u32(1 << 1);
    let mut vals = XdrWriter::new();
    vals.put_u32(1); // NF4REG=1 (won't match dir)
    verify_mismatch.put_opaque(&vals.into_bytes());
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_NVERIFY, verify_mismatch.into_bytes()),
    ]);

    // LOCK / LOCKT / LOCKU.
    let lock_owner = b"exerciser-lock-1".to_vec();
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_LOCK, lock_args(false, &lock_owner)),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_LOCKT, lockt_args(&lock_owner)),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_LOCKU, locku_args(&[0u8; 16])),
    ]);

    // OPEN_DOWNGRADE: CFH must be the open file. Use PUTROOTFH + LOOKUP first.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_OPEN_DOWNGRADE, open_downgrade_args(2, &sid)),
    ]);

    // OPEN_CONFIRM: typically v4.0; on v4.1 it's permitted but server may
    // return NFS4ERR_NOTSUPP. Either way the dispatch counter rises.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_OPEN_CONFIRM, open_confirm_args(&sid, 2)),
    ]);

    // CLOSE.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_CLOSE, close_args(2, &sid)),
    ]);

    // v4.2 ALLOCATE / DEALLOCATE / SEEK / IO_ADVISE — separate compounds so
    // an early failure doesn't suppress later ops.
    for (op, args) in [
        (nfs4::OP_ALLOCATE, allocate_args()),
        (nfs4::OP_DEALLOCATE, deallocate_args()),
        (nfs4::OP_SEEK, seek_args()),
        (nfs4::OP_IO_ADVISE, io_advise_args()),
    ] {
        let _ = send!(vec![
            (nfs4::OP_PUTROOTFH, vec![]),
            (nfs4::OP_LOOKUP, lookup_args("b.txt")),
            (op, args),
        ]);
    }

    // COPY: SAVEFH=src(b.txt), CFH=dst(d.txt).
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_SAVEFH, vec![]),
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("d.txt")),
        (nfs4::OP_COPY, copy_args(0, 0, 0)),
    ]);

    // CLONE: same shape, but to e.txt.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_SAVEFH, vec![]),
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("e.txt")),
        (nfs4::OP_CLONE, clone_args()),
    ]);

    // PUTPUBFH / LOOKUPP / OPENATTR — each separate.
    let _ = send!(vec![(nfs4::OP_PUTPUBFH, vec![])]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("dir1")),
        (nfs4::OP_LOOKUPP, vec![]),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("a.txt")),
        (nfs4::OP_OPENATTR, openattr_args()),
    ]);

    // SECINFO / SECINFO_NO_NAME.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_SECINFO, secinfo_args("a.txt")),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_SECINFO_NO_NAME, secinfo_no_name_args()),
    ]);

    // TEST_STATEID, FREE_STATEID, BIND_CONN_TO_SESSION, BACKCHANNEL_CTL —
    // each separate so the chain doesn't abort on the first malformed op.
    let _ = send!(vec![(nfs4::OP_TEST_STATEID, test_stateid_args())]);
    let _ = send!(vec![(nfs4::OP_FREE_STATEID, free_stateid_args())]);
    let _ = send!(vec![(
        nfs4::OP_BIND_CONN_TO_SESSION,
        bind_conn_to_session_args(&session_id)
    )]);
    let _ = send!(vec![(nfs4::OP_BACKCHANNEL_CTL, backchannel_ctl_args())]);

    // pNFS ops — each in its own COMPOUND. Server returns
    // NFS4ERR_LAYOUTUNAVAILABLE / NFS4ERR_UNKNOWN_LAYOUTTYPE; that still
    // increments the dispatcher counter for the op.
    for (op, args) in [
        (nfs4::OP_LAYOUTGET, layoutget_args()),
        (nfs4::OP_LAYOUTCOMMIT, layoutcommit_args()),
        (nfs4::OP_LAYOUTRETURN, layoutreturn_args()),
    ] {
        let _ = send!(vec![
            (nfs4::OP_PUTROOTFH, vec![]),
            (nfs4::OP_LOOKUP, lookup_args("b.txt")),
            (op, args),
        ]);
    }
    let _ = send!(vec![(nfs4::OP_GETDEVICEINFO, getdeviceinfo_args())]);
    let _ = send!(vec![(nfs4::OP_GETDEVICELIST, getdevicelist_args())]);

    // Delegations — each separate.
    let _ = send!(vec![(nfs4::OP_DELEGRETURN, delegreturn_args())]);
    let _ = send!(vec![(nfs4::OP_DELEGPURGE, delegpurge_args())]);

    // ------------------------------------------------------------------
    // v4.2: xattr family — needs a real file CFH.
    // ------------------------------------------------------------------
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("a.txt")),
        (nfs4::OP_SETXATTR, setxattr_args("user.greet", b"hello", 0)),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("a.txt")),
        (nfs4::OP_GETXATTR, getxattr_args("user.greet")),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("a.txt")),
        (nfs4::OP_LISTXATTRS, listxattrs_args()),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("a.txt")),
        (nfs4::OP_REMOVEXATTR, removexattr_args("user.greet")),
    ]);

    // ------------------------------------------------------------------
    // v4.2: READ_PLUS, WRITE_SAME — need an open-able file.
    // ------------------------------------------------------------------
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_READ_PLUS, read_plus_args(0, 64)),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_WRITE_SAME, write_same_args()),
    ]);

    // ------------------------------------------------------------------
    // v4.2: async-copy ops — accept whatever status (we run COPY synchronously).
    // ------------------------------------------------------------------
    let _ = send!(vec![(nfs4::OP_COPY_NOTIFY, copy_notify_args())]);
    let _ = send!(vec![(nfs4::OP_OFFLOAD_CANCEL, offload_cancel_args())]);
    let _ = send!(vec![(nfs4::OP_OFFLOAD_STATUS, offload_status_args())]);

    // ------------------------------------------------------------------
    // pNFS v4.2: LAYOUTERROR, LAYOUTSTATS.
    // ------------------------------------------------------------------
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_LAYOUTERROR, layouterror_args()),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_LOOKUP, lookup_args("b.txt")),
        (nfs4::OP_LAYOUTSTATS, layoutstats_args()),
    ]);

    // ------------------------------------------------------------------
    // v4.1: GET_DIR_DELEGATION, WANT_DELEGATION, SET_SSV.
    // ------------------------------------------------------------------
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_GET_DIR_DELEGATION, get_dir_delegation_args()),
    ]);
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_WANT_DELEGATION, want_delegation_args()),
    ]);
    let _ = send!(vec![(nfs4::OP_SET_SSV, set_ssv_args())]);

    // REMOVE — last for the v4.1 client.
    let _ = send!(vec![
        (nfs4::OP_PUTROOTFH, vec![]),
        (nfs4::OP_REMOVE, remove_args("a-hard.txt")),
    ]);

    // Drive DESTROY_SESSION + DESTROY_CLIENTID at the end.
    let mut ds = XdrWriter::new();
    ds.put_raw(&session_id);
    let _ = c
        .compound(1, vec![(nfs4::OP_DESTROY_SESSION, ds.into_bytes())])
        .await;

    let mut dc = XdrWriter::new();
    dc.put_u64(clientid);
    let _ = c
        .compound(1, vec![(nfs4::OP_DESTROY_CLIENTID, dc.into_bytes())])
        .await;

    // ------------------------------------------------------------------
    // v4.0-only ops (RENEW, SETCLIENTID, SETCLIENTID_CONFIRM, OPEN_CONFIRM,
    // RELEASE_LOCKOWNER) — send via minorversion=0 compound.
    // ------------------------------------------------------------------
    let mut c2 = Client::connect(h.addr).await;
    let reply = c2
        .compound(0, vec![(nfs4::OP_SETCLIENTID, setclientid_args())])
        .await;
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    let v40_clientid = r.get_u64().unwrap_or(0);
    let _ = c2
        .compound(
            0,
            vec![(
                nfs4::OP_SETCLIENTID_CONFIRM,
                setclientid_confirm_args(v40_clientid),
            )],
        )
        .await;
    let _ = c2.compound(0, vec![(nfs4::OP_RENEW, renew_args())]).await;
    let _ = c2
        .compound(
            0,
            vec![(nfs4::OP_RELEASE_LOCKOWNER, release_lockowner_args())],
        )
        .await;

    // ------------------------------------------------------------------
    // Final assertion: every dispatched op has count > 0.
    // ------------------------------------------------------------------
    let prom = h.metrics.to_prometheus();
    let dispatcher_ops = [
        nfs4::OP_ACCESS,
        nfs4::OP_CLOSE,
        nfs4::OP_COMMIT,
        nfs4::OP_CREATE,
        nfs4::OP_DELEGPURGE,
        nfs4::OP_DELEGRETURN,
        nfs4::OP_GETATTR,
        nfs4::OP_GETFH,
        nfs4::OP_LINK,
        nfs4::OP_LOCK,
        nfs4::OP_LOCKT,
        nfs4::OP_LOCKU,
        nfs4::OP_LOOKUP,
        nfs4::OP_LOOKUPP,
        nfs4::OP_NVERIFY,
        nfs4::OP_OPEN,
        nfs4::OP_OPENATTR,
        nfs4::OP_OPEN_CONFIRM,
        nfs4::OP_OPEN_DOWNGRADE,
        nfs4::OP_PUTFH,
        nfs4::OP_PUTPUBFH,
        nfs4::OP_PUTROOTFH,
        nfs4::OP_READ,
        nfs4::OP_READDIR,
        nfs4::OP_READLINK,
        nfs4::OP_REMOVE,
        nfs4::OP_RENAME,
        nfs4::OP_RENEW,
        nfs4::OP_RESTOREFH,
        nfs4::OP_SAVEFH,
        nfs4::OP_SECINFO,
        nfs4::OP_SETATTR,
        nfs4::OP_SETCLIENTID,
        nfs4::OP_SETCLIENTID_CONFIRM,
        nfs4::OP_VERIFY,
        nfs4::OP_WRITE,
        nfs4::OP_RELEASE_LOCKOWNER,
        nfs4::OP_BACKCHANNEL_CTL,
        nfs4::OP_BIND_CONN_TO_SESSION,
        nfs4::OP_EXCHANGE_ID,
        nfs4::OP_CREATE_SESSION,
        nfs4::OP_DESTROY_SESSION,
        nfs4::OP_FREE_STATEID,
        nfs4::OP_GETDEVICEINFO,
        nfs4::OP_GETDEVICELIST,
        nfs4::OP_LAYOUTCOMMIT,
        nfs4::OP_LAYOUTGET,
        nfs4::OP_LAYOUTRETURN,
        nfs4::OP_SECINFO_NO_NAME,
        nfs4::OP_SEQUENCE,
        nfs4::OP_TEST_STATEID,
        nfs4::OP_DESTROY_CLIENTID,
        nfs4::OP_RECLAIM_COMPLETE,
        nfs4::OP_ALLOCATE,
        nfs4::OP_COPY,
        nfs4::OP_DEALLOCATE,
        nfs4::OP_IO_ADVISE,
        nfs4::OP_SEEK,
        nfs4::OP_CLONE,
        // v4.2 ops
        nfs4::OP_GETXATTR,
        nfs4::OP_SETXATTR,
        nfs4::OP_LISTXATTRS,
        nfs4::OP_REMOVEXATTR,
        nfs4::OP_READ_PLUS,
        nfs4::OP_WRITE_SAME,
        nfs4::OP_COPY_NOTIFY,
        nfs4::OP_OFFLOAD_CANCEL,
        nfs4::OP_OFFLOAD_STATUS,
        nfs4::OP_LAYOUTERROR,
        nfs4::OP_LAYOUTSTATS,
        // v4.1 ops we now dispatch
        nfs4::OP_GET_DIR_DELEGATION,
        nfs4::OP_WANT_DELEGATION,
        nfs4::OP_SET_SSV,
    ];

    let mut zero = Vec::new();
    for op in dispatcher_ops {
        let count = h.metrics.op_calls[op as usize].load(std::sync::atomic::Ordering::Relaxed);
        if count == 0 {
            let name = nvstor_nfs::metrics::OP_NAMES[op as usize];
            zero.push(format!("{name} (op={op})"));
        }
    }
    if !zero.is_empty() {
        eprintln!("Prometheus snapshot:\n{prom}");
        panic!("ops never invoked: {}", zero.join(", "));
    }
}
