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

//! NFS v4 integration tests — simulates real Linux NFS client mount + file ops.
//!
//! These tests speak the exact wire protocol a Linux kernel NFS client uses.
//! If these pass, `mount -t nfs4` should work.

use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};

use nvstor_metadata::MetadataStore;
use nvstor_nfs::nfs4::{self, CompoundProcessor};
use nvstor_nfs::rpc;
use nvstor_nfs::server::{NfsServer, NfsServerConfig};
use nvstor_nfs::vfs::Vfs;
use nvstor_nfs::xdr::{XdrReader, XdrWriter};
use nvstor_storage::backend::PosixBackend;
use nvstor_storage::manager::{ChunkManager, ChunkManagerConfig};

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

struct TestNfsClient {
    stream: TcpStream,
    xid: u32,
}

impl TestNfsClient {
    async fn connect(addr: std::net::SocketAddr) -> Self {
        let stream = TcpStream::connect(addr).await.expect("connect");
        Self { stream, xid: 1 }
    }

    fn next_xid(&mut self) -> u32 {
        let x = self.xid;
        self.xid += 1;
        x
    }

    /// Send a raw RPC record.
    async fn send_record(&mut self, data: &[u8]) {
        let header = 0x8000_0000 | (data.len() as u32);
        self.stream.write_u32(header).await.unwrap();
        self.stream.write_all(data).await.unwrap();
        self.stream.flush().await.unwrap();
    }

    /// Receive a raw RPC record.
    async fn recv_record(&mut self) -> Vec<u8> {
        let header = self.stream.read_u32().await.unwrap();
        let len = (header & 0x7FFF_FFFF) as usize;
        let mut data = vec![0u8; len];
        self.stream.read_exact(&mut data).await.unwrap();
        data
    }

    /// Build an RPC CALL envelope around a payload.
    fn build_call(&mut self, proc_num: u32, payload: &[u8]) -> Vec<u8> {
        let xid = self.next_xid();
        let mut w = XdrWriter::new();
        w.put_u32(xid);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(rpc::NFS_PROGRAM);
        w.put_u32(rpc::NFS_V4);
        w.put_u32(proc_num);
        // AUTH_SYS credentials: stamp=0, machine="test", uid=0, gid=0, 0 aux gids
        w.put_u32(rpc::AUTH_SYS);
        let mut cred = XdrWriter::new();
        cred.put_u32(0); // stamp
        cred.put_string("nvstor-test"); // machinename
        cred.put_u32(0); // uid
        cred.put_u32(0); // gid
        cred.put_u32(0); // 0 aux gids
        w.put_opaque(&cred.into_bytes());
        // Verifier: AUTH_NONE
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        // Payload
        w.put_raw(payload);
        w.into_bytes()
    }

    /// Build a COMPOUND payload (tag + minorversion + ops).
    fn build_compound(tag: &str, minorversion: u32, ops: &[(u32, Vec<u8>)]) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_string(tag);
        w.put_u32(minorversion);
        w.put_u32(ops.len() as u32);
        for (opnum, args) in ops {
            w.put_u32(*opnum);
            w.put_raw(args);
        }
        w.into_bytes()
    }

    /// Send a COMPOUND request and return the parsed reply.
    async fn compound(
        &mut self,
        tag: &str,
        minorversion: u32,
        ops: &[(u32, Vec<u8>)],
    ) -> CompoundReply {
        let payload = Self::build_compound(tag, minorversion, ops);
        let call = self.build_call(1, &payload); // proc 1 = COMPOUND
        self.send_record(&call).await;
        let record = self.recv_record().await;
        CompoundReply::parse(&record)
    }

    /// Send NULL RPC and verify success.
    async fn null_ping(&mut self) {
        let call = self.build_call(0, &[]);
        self.send_record(&call).await;
        let record = self.recv_record().await;
        let mut r = XdrReader::new(&record);
        let _xid = r.get_u32().unwrap();
        let msg_type = r.get_u32().unwrap();
        assert_eq!(msg_type, rpc::MSG_TYPE_REPLY);
        let accepted = r.get_u32().unwrap();
        assert_eq!(accepted, rpc::MSG_ACCEPTED);
    }
}

#[allow(dead_code)]
struct CompoundReply {
    xid: u32,
    compound_status: u32,
    tag: Vec<u8>,
    num_results: u32,
    /// Raw remaining bytes after the header for per-op parsing.
    results_data: Vec<u8>,
}

impl CompoundReply {
    fn parse(record: &[u8]) -> Self {
        let mut r = XdrReader::new(record);
        let xid = r.get_u32().unwrap();
        let _msg_type = r.get_u32().unwrap(); // MSG_TYPE_REPLY
        let _accepted = r.get_u32().unwrap(); // MSG_ACCEPTED
        let _verf_flavor = r.get_u32().unwrap();
        let _verf_body = r.get_opaque().unwrap();
        let _accept_status = r.get_u32().unwrap(); // ACCEPT_SUCCESS
                                                   // COMPOUND reply
        let compound_status = r.get_u32().unwrap();
        let tag = r.get_opaque().unwrap();
        let num_results = r.get_u32().unwrap();
        let results_data = r.remaining_bytes().to_vec();
        Self {
            xid,
            compound_status,
            tag,
            num_results,
            results_data,
        }
    }

    fn assert_ok(&self) {
        assert_eq!(
            self.compound_status, 0,
            "COMPOUND status = {}",
            self.compound_status
        );
    }
}

fn setup_server() -> (tempfile::TempDir, Arc<CompoundProcessor>) {
    let tmp = tempfile::TempDir::new().expect("tempdir");
    let data_dir = tmp.path().join("data");
    let meta_dir = tmp.path().join("meta");
    let backend = Box::new(PosixBackend::new(&data_dir).expect("backend"));
    let metadata = Arc::new(MetadataStore::open(&meta_dir).expect("metadata"));
    let config = ChunkManagerConfig {
        disk_usage_threshold: 1.0,
        ..ChunkManagerConfig::default()
    };
    let mgr = Arc::new(ChunkManager::new(backend, metadata.clone(), config));
    let vfs = Arc::new(Vfs::with_metadata(mgr, Some(metadata), "nfs-integ/"));
    let proc = Arc::new(CompoundProcessor::new(vfs));
    (tmp, proc)
}

async fn start_server(proc: Arc<CompoundProcessor>) -> std::net::SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let server = NfsServer::new(proc, NfsServerConfig::default());
    tokio::spawn(async move { server.run(listener).await.unwrap() });
    // Give server a moment to start.
    tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    addr
}

// ---------------------------------------------------------------------------
// Helper builders for NFS ops
// ---------------------------------------------------------------------------

fn setclientid_args(verifier: &[u8; 8], id: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_raw(verifier);
    w.put_opaque(id.as_bytes());
    w.put_u32(0); // cb_program
    w.put_string("tcp");
    w.put_string("127.0.0.1.8.1");
    w.put_u32(0); // cb_ident
    w.into_bytes()
}

fn setclientid_confirm_args(clientid: u64, confirm: &[u8]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(clientid);
    w.put_raw(confirm);
    w.into_bytes()
}

fn renew_args(clientid: u64) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(clientid);
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

fn lookup_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn create_file_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(1); // NF4REG
    w.put_string(name);
    w.put_u32(0); // empty bitmap
    w.put_opaque(&[]); // empty attr values
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

fn write_args(offset: u64, data: &[u8]) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // stateid seqid
    w.put_raw(&[0u8; 12]); // stateid other (anonymous)
    w.put_u64(offset);
    w.put_u32(2); // FILE_SYNC4
    w.put_opaque(data);
    w.into_bytes()
}

fn read_args(offset: u64, count: u32) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u32(0); // stateid seqid
    w.put_raw(&[0u8; 12]); // stateid other
    w.put_u64(offset);
    w.put_u32(count);
    w.into_bytes()
}

fn remove_args(name: &str) -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_string(name);
    w.into_bytes()
}

fn commit_args() -> Vec<u8> {
    let mut w = XdrWriter::new();
    w.put_u64(0); // offset
    w.put_u32(0); // count
    w.into_bytes()
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

/// Test 1: Simulate the exact NFS v4.0 mount sequence from a Linux client.
#[tokio::test]
async fn nfsv4_mount_sequence() {
    let (_tmp, proc) = setup_server();
    let addr = start_server(proc).await;
    let mut client = TestNfsClient::connect(addr).await;

    // Step 1: NULL ping.
    client.null_ping().await;

    // Step 2: SETCLIENTID.
    let reply = client
        .compound(
            "mount",
            0,
            &[(
                nfs4::OP_SETCLIENTID,
                setclientid_args(&[0xAA; 8], "linux-nfs-client-1"),
            )],
        )
        .await;
    reply.assert_ok();
    assert_eq!(reply.num_results, 1);

    // Parse clientid + confirm verifier from SETCLIENTID response.
    let mut r = XdrReader::new(&reply.results_data);
    let _opnum = r.get_u32().unwrap();
    let _status = r.get_u32().unwrap();
    let clientid = r.get_u64().unwrap();
    let confirm = r.get_opaque_fixed(8).unwrap();

    // Step 3: SETCLIENTID_CONFIRM.
    let reply = client
        .compound(
            "confirm",
            0,
            &[(
                nfs4::OP_SETCLIENTID_CONFIRM,
                setclientid_confirm_args(clientid, &confirm),
            )],
        )
        .await;
    reply.assert_ok();

    // Step 4: PUTROOTFH + GETFH + GETATTR (get root handle + attributes).
    // This is what the Linux client does right after confirming.
    let reply = client
        .compound(
            "rootfh",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_GETFH, Vec::new()),
                (
                    nfs4::OP_GETATTR,
                    getattr_args(
                        (1 << 1) | (1 << 4) | (1 << 10) | (1 << 20), // type + size + lease_time + fileid
                        (1 << 1) | (1 << 3),                         // mode + numlinks
                    ),
                ),
            ],
        )
        .await;
    reply.assert_ok();
    assert_eq!(reply.num_results, 3);

    // Step 5: RENEW — keep lease alive.
    let reply = client
        .compound("renew", 0, &[(nfs4::OP_RENEW, renew_args(clientid))])
        .await;
    reply.assert_ok();
}

/// Test 2: Full file lifecycle — create, write, read, stat, delete.
#[tokio::test]
async fn file_lifecycle() {
    let (_tmp, proc) = setup_server();
    let addr = start_server(proc).await;
    let mut client = TestNfsClient::connect(addr).await;

    // Create file.
    let reply = client
        .compound(
            "create",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_CREATE, create_file_args("checkpoint.pt")),
            ],
        )
        .await;
    reply.assert_ok();

    // Write data.
    let test_data = b"PyTorch model weights layer 42";
    let reply = client
        .compound(
            "write",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoint.pt")),
                (nfs4::OP_WRITE, write_args(0, test_data)),
            ],
        )
        .await;
    reply.assert_ok();

    // Read data back.
    let reply = client
        .compound(
            "read",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoint.pt")),
                (nfs4::OP_READ, read_args(0, 4096)),
            ],
        )
        .await;
    reply.assert_ok();
    assert_eq!(reply.num_results, 3);

    // Parse READ result to verify data.
    let mut r = XdrReader::new(&reply.results_data);
    // Skip PUTROOTFH result.
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    // Skip LOOKUP result.
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    // READ result.
    let read_opnum = r.get_u32().unwrap();
    assert_eq!(read_opnum, nfs4::OP_READ);
    let read_status = r.get_u32().unwrap();
    assert_eq!(read_status, 0);
    let eof = r.get_u32().unwrap();
    assert_eq!(eof, 1); // EOF
    let data = r.get_opaque().unwrap();
    assert_eq!(&data, test_data);

    // Stat the file (GETATTR).
    let reply = client
        .compound(
            "stat",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoint.pt")),
                (
                    nfs4::OP_GETATTR,
                    getattr_args(
                        (1 << 1) | (1 << 4), // type + size
                        0,
                    ),
                ),
            ],
        )
        .await;
    reply.assert_ok();

    // Commit.
    let reply = client
        .compound(
            "commit",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoint.pt")),
                (nfs4::OP_COMMIT, commit_args()),
            ],
        )
        .await;
    reply.assert_ok();

    // Delete.
    let reply = client
        .compound(
            "delete",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_REMOVE, remove_args("checkpoint.pt")),
            ],
        )
        .await;
    reply.assert_ok();

    // Verify deleted — LOOKUP should fail.
    let reply = client
        .compound(
            "verify-deleted",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoint.pt")),
            ],
        )
        .await;
    assert_ne!(reply.compound_status, 0); // Should be NFS4ERR_NOENT
}

/// Test 3: Directory operations — mkdir, readdir, rmdir.
#[tokio::test]
async fn directory_operations() {
    let (_tmp, proc) = setup_server();
    let addr = start_server(proc).await;
    let mut client = TestNfsClient::connect(addr).await;

    // Create directory.
    let reply = client
        .compound(
            "mkdir",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_CREATE, create_dir_args("checkpoints")),
            ],
        )
        .await;
    reply.assert_ok();

    // Create file inside directory.
    let reply = client
        .compound(
            "create-in-dir",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoints")),
                (nfs4::OP_CREATE, create_file_args("step-100.pt")),
            ],
        )
        .await;
    reply.assert_ok();

    // Write data to file in subdirectory.
    let reply = client
        .compound(
            "write-in-dir",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoints")),
                (nfs4::OP_LOOKUP, lookup_args("step-100.pt")),
                (nfs4::OP_WRITE, write_args(0, b"checkpoint data step 100")),
            ],
        )
        .await;
    reply.assert_ok();

    // READDIR on checkpoints directory.
    let mut readdir_args = XdrWriter::new();
    readdir_args.put_u64(0); // cookie
    readdir_args.put_raw(&[0u8; 8]); // cookieverf
    readdir_args.put_u32(4096); // dircount
    readdir_args.put_u32(8192); // maxcount
    readdir_args.put_u32(1); // attr bitmap 1 word
    readdir_args.put_u32((1 << 1) | (1 << 4)); // type + size

    let reply = client
        .compound(
            "readdir",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoints")),
                (nfs4::OP_READDIR, readdir_args.into_bytes()),
            ],
        )
        .await;
    reply.assert_ok();

    // ACCESS check.
    let reply = client
        .compound(
            "access",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("checkpoints")),
                (nfs4::OP_ACCESS, access_args(0x3F)),
            ],
        )
        .await;
    reply.assert_ok();
}

/// Test 4: Server-side COPY — the headline feature for AI checkpoint cloning.
#[tokio::test]
async fn server_side_copy() {
    let (_tmp, proc) = setup_server();
    let addr = start_server(proc).await;
    let mut client = TestNfsClient::connect(addr).await;

    // Create source file with data.
    let model_data = vec![0xABu8; 1024 * 64]; // 64 KB model weights
    let reply = client
        .compound(
            "create-src",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_CREATE, create_file_args("model-v1.pt")),
            ],
        )
        .await;
    reply.assert_ok();

    let reply = client
        .compound(
            "write-src",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("model-v1.pt")),
                (nfs4::OP_WRITE, write_args(0, &model_data)),
            ],
        )
        .await;
    reply.assert_ok();

    // Create destination file.
    let reply = client
        .compound(
            "create-dst",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_CREATE, create_file_args("model-v1-clone.pt")),
            ],
        )
        .await;
    reply.assert_ok();

    // Get filehandles for COPY.
    let reply = client
        .compound(
            "get-src-fh",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("model-v1.pt")),
                (nfs4::OP_GETFH, Vec::new()),
            ],
        )
        .await;
    reply.assert_ok();

    // Parse source filehandle from GETFH result.
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // PUTROOTFH
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // LOOKUP
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // GETFH opnum + status
    let src_fh = r.get_opaque().unwrap();

    let reply = client
        .compound(
            "get-dst-fh",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("model-v1-clone.pt")),
                (nfs4::OP_GETFH, Vec::new()),
            ],
        )
        .await;
    reply.assert_ok();
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    let dst_fh = r.get_opaque().unwrap();

    // Server-side COPY: PUTFH(src) + SAVEFH + PUTFH(dst) + COPY
    let mut putfh_src = XdrWriter::new();
    putfh_src.put_opaque(&src_fh);
    let mut putfh_dst = XdrWriter::new();
    putfh_dst.put_opaque(&dst_fh);

    let mut copy_args = XdrWriter::new();
    copy_args.put_u32(0);
    copy_args.put_raw(&[0u8; 12]); // src stateid
    copy_args.put_u32(0);
    copy_args.put_raw(&[0u8; 12]); // dst stateid
    copy_args.put_u64(0); // src_offset
    copy_args.put_u64(0); // dst_offset
    copy_args.put_u64(0); // count (0 = full file)
    copy_args.put_u32(1); // consecutive
    copy_args.put_u32(1); // synchronous
    copy_args.put_u32(0); // 0 netlocs

    let reply = client
        .compound(
            "copy",
            0,
            &[
                (nfs4::OP_PUTFH, putfh_src.into_bytes()),
                (nfs4::OP_SAVEFH, Vec::new()),
                (nfs4::OP_PUTFH, putfh_dst.into_bytes()),
                (nfs4::OP_COPY, copy_args.into_bytes()),
            ],
        )
        .await;
    reply.assert_ok();

    // Read back the clone and verify data matches.
    let reply = client
        .compound(
            "read-clone",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("model-v1-clone.pt")),
                (nfs4::OP_READ, read_args(0, 1024 * 128)),
            ],
        )
        .await;
    reply.assert_ok();

    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // PUTROOTFH
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // LOOKUP
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // READ opnum + status
    let _eof = r.get_u32().unwrap();
    let clone_data = r.get_opaque().unwrap();
    assert_eq!(clone_data.len(), model_data.len());
    assert_eq!(clone_data, model_data);
}

/// Test 5: Symlinks and hardlinks.
#[tokio::test]
async fn symlinks_and_hardlinks() {
    let (_tmp, proc) = setup_server();
    let addr = start_server(proc).await;
    let mut client = TestNfsClient::connect(addr).await;

    // Create a file.
    let reply = client
        .compound(
            "create",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_CREATE, create_file_args("real-file.txt")),
            ],
        )
        .await;
    reply.assert_ok();

    // Create symlink: NF4LNK type.
    let mut symlink_args = XdrWriter::new();
    symlink_args.put_u32(5); // NF4LNK
    symlink_args.put_string("real-file.txt"); // linkdata (target)
    symlink_args.put_string("my-symlink"); // name
    symlink_args.put_u32(0); // empty bitmap
    symlink_args.put_opaque(&[]);

    let reply = client
        .compound(
            "symlink",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_CREATE, symlink_args.into_bytes()),
            ],
        )
        .await;
    reply.assert_ok();

    // READLINK on the symlink.
    let reply = client
        .compound(
            "readlink",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("my-symlink")),
                (nfs4::OP_READLINK, Vec::new()),
            ],
        )
        .await;
    reply.assert_ok();

    // Parse READLINK result.
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // PUTROOTFH
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // LOOKUP
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // READLINK opnum + status
    let target = r.get_string().unwrap();
    assert_eq!(target, "real-file.txt");

    // Hardlink: SAVEFH on real-file, then LINK in root.
    let reply = client
        .compound(
            "get-fh",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LOOKUP, lookup_args("real-file.txt")),
                (nfs4::OP_GETFH, Vec::new()),
            ],
        )
        .await;
    reply.assert_ok();
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    let file_fh = r.get_opaque().unwrap();

    let mut putfh_args = XdrWriter::new();
    putfh_args.put_opaque(&file_fh);
    let mut link_args = XdrWriter::new();
    link_args.put_string("hardlink.txt");

    let reply = client
        .compound(
            "hardlink",
            0,
            &[
                (nfs4::OP_PUTFH, putfh_args.into_bytes()),
                (nfs4::OP_SAVEFH, Vec::new()),
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_LINK, link_args.into_bytes()),
            ],
        )
        .await;
    reply.assert_ok();
}

/// Test 6: NFS v4.1 session-based mount sequence.
#[tokio::test]
async fn nfsv41_session_mount() {
    let (_tmp, proc) = setup_server();
    let addr = start_server(proc).await;
    let mut client = TestNfsClient::connect(addr).await;

    // Step 1: EXCHANGE_ID (v4.1 equivalent of SETCLIENTID).
    let mut eid_args = XdrWriter::new();
    eid_args.put_raw(&[0xBB; 8]); // verifier
    eid_args.put_opaque(b"v41-test-client");
    eid_args.put_u32(0x30000); // flags
    eid_args.put_u32(0); // SP4_NONE
    eid_args.put_u32(0); // 0 impl_id

    let reply = client
        .compound(
            "exchange-id",
            1,
            &[(nfs4::OP_EXCHANGE_ID, eid_args.into_bytes())],
        )
        .await;
    reply.assert_ok();

    // Parse clientid.
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap(); // opnum + status
    let clientid = r.get_u64().unwrap();

    // Step 2: CREATE_SESSION.
    let mut cs_args = XdrWriter::new();
    cs_args.put_u64(clientid);
    cs_args.put_u32(1); // sequenceid
    cs_args.put_u32(0); // flags
                        // fore_chan_attrs (7 fields)
    cs_args.put_u32(0); // headerpadsize
    cs_args.put_u32(1048576);
    cs_args.put_u32(1048576);
    cs_args.put_u32(1048576);
    cs_args.put_u32(64);
    cs_args.put_u32(16); // maxreqs = 16 slots
    cs_args.put_u32(0); // rdma count
                        // back_chan_attrs (7 fields)
    cs_args.put_u32(0);
    cs_args.put_u32(1048576);
    cs_args.put_u32(1048576);
    cs_args.put_u32(1048576);
    cs_args.put_u32(64);
    cs_args.put_u32(16);
    cs_args.put_u32(0);
    cs_args.put_u32(0); // cb_program
    cs_args.put_u32(0); // 0 sec_parms

    let reply = client
        .compound(
            "create-session",
            1,
            &[(nfs4::OP_CREATE_SESSION, cs_args.into_bytes())],
        )
        .await;
    reply.assert_ok();

    // Parse session_id.
    let mut r = XdrReader::new(&reply.results_data);
    r.get_u32().unwrap();
    r.get_u32().unwrap();
    let session_id = r.get_opaque_fixed(16).unwrap();

    // Step 3: SEQUENCE + PUTROOTFH + GETFH + GETATTR (standard v4.1 compound).
    let mut seq_args = XdrWriter::new();
    seq_args.put_raw(&session_id);
    seq_args.put_u32(1); // sequenceid (first request in this slot)
    seq_args.put_u32(0); // slotid
    seq_args.put_u32(0); // highest_slotid
    seq_args.put_u32(1); // cachethis

    let root_ops = vec![
        (nfs4::OP_SEQUENCE, seq_args.into_bytes()),
        (nfs4::OP_PUTROOTFH, Vec::new()),
        (nfs4::OP_GETFH, Vec::new()),
        (nfs4::OP_GETATTR, getattr_args((1 << 1) | (1 << 4), 0)),
    ];
    let reply = client.compound("session-getroot", 1, &root_ops).await;
    reply.assert_ok();
    assert_eq!(reply.num_results, 4);
    // Reconnect and retry the same session/slot/sequence with a new RPC XID.
    let mut replacement = TestNfsClient::connect(addr).await;
    let retry = replacement.compound("session-getroot", 1, &root_ops).await;
    retry.assert_ok();
    assert_eq!(retry.results_data, reply.results_data);

    // Step 4: SEQUENCE + RECLAIM_COMPLETE.
    let mut seq_args2 = XdrWriter::new();
    seq_args2.put_raw(&session_id);
    seq_args2.put_u32(2); // next sequenceid in the same slot
    seq_args2.put_u32(0); // slotid (same slot, seqid should advance)
    seq_args2.put_u32(0);
    seq_args2.put_u32(0);

    let mut rc_args = XdrWriter::new();
    rc_args.put_u32(0); // one_fs = false

    let reply = client
        .compound(
            "reclaim-complete",
            1,
            &[
                (nfs4::OP_SEQUENCE, seq_args2.into_bytes()),
                (nfs4::OP_RECLAIM_COMPLETE, rc_args.into_bytes()),
            ],
        )
        .await;
    reply.assert_ok();
}

/// Test 7: Multiple files, large-ish data, verify integrity.
#[tokio::test]
async fn checkpoint_workflow() {
    let (_tmp, proc) = setup_server();
    let addr = start_server(proc).await;
    let mut client = TestNfsClient::connect(addr).await;

    // Simulate a training checkpoint: create dir, write multiple files.
    let reply = client
        .compound(
            "mkdir",
            0,
            &[
                (nfs4::OP_PUTROOTFH, Vec::new()),
                (nfs4::OP_CREATE, create_dir_args("checkpoint-step-1000")),
            ],
        )
        .await;
    reply.assert_ok();

    // Write 4 "shard" files with different data.
    for shard in 0..4 {
        let name = format!("shard-{shard}.pt");
        let data = vec![shard as u8 + 0x41; 4096]; // 4KB per shard, different content

        let reply = client
            .compound(
                "create-shard",
                0,
                &[
                    (nfs4::OP_PUTROOTFH, Vec::new()),
                    (nfs4::OP_LOOKUP, lookup_args("checkpoint-step-1000")),
                    (nfs4::OP_CREATE, create_file_args(&name)),
                ],
            )
            .await;
        reply.assert_ok();

        let reply = client
            .compound(
                "write-shard",
                0,
                &[
                    (nfs4::OP_PUTROOTFH, Vec::new()),
                    (nfs4::OP_LOOKUP, lookup_args("checkpoint-step-1000")),
                    (nfs4::OP_LOOKUP, lookup_args(&name)),
                    (nfs4::OP_WRITE, write_args(0, &data)),
                ],
            )
            .await;
        reply.assert_ok();
    }

    // Verify all 4 shards by reading them back.
    for shard in 0..4 {
        let name = format!("shard-{shard}.pt");
        let expected = vec![shard as u8 + 0x41; 4096];

        let reply = client
            .compound(
                "read-shard",
                0,
                &[
                    (nfs4::OP_PUTROOTFH, Vec::new()),
                    (nfs4::OP_LOOKUP, lookup_args("checkpoint-step-1000")),
                    (nfs4::OP_LOOKUP, lookup_args(&name)),
                    (nfs4::OP_READ, read_args(0, 8192)),
                ],
            )
            .await;
        reply.assert_ok();

        let mut r = XdrReader::new(&reply.results_data);
        r.get_u32().unwrap();
        r.get_u32().unwrap(); // PUTROOTFH
        r.get_u32().unwrap();
        r.get_u32().unwrap(); // LOOKUP 1
        r.get_u32().unwrap();
        r.get_u32().unwrap(); // LOOKUP 2
        r.get_u32().unwrap();
        r.get_u32().unwrap(); // READ
        let _eof = r.get_u32().unwrap();
        let data = r.get_opaque().unwrap();
        assert_eq!(data.len(), expected.len(), "shard {shard} size mismatch");
        assert_eq!(data, expected, "shard {shard} data mismatch");
    }
}
