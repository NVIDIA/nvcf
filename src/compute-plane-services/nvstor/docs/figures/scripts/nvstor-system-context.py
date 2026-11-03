#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""docs/figures/nvstor-system-context.svg: what talks to nvstor and where each protocol's bytes land.

Source of truth: bins/nvstor-server/src/main.rs (service wiring and routing),
bins/nvstor-server/src/config.rs (ports), crates/nvstor-placement/src/distributed.rs (replication),
crates/nvstor-object/src/s3/handlers.rs (S3 PUT then replicate then Raft).
"""
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig

f = Fig(
    "nvstor-system-context", 1400, 900,
    "nvstor System Context: Clients, Front Ends, Two Data Planes",
    "Clients reach nvstor-server through five protocol front ends. NFS file content, NBD volumes and "
    "the S3-over-RDMA and S3-over-TCP fast paths store bytes in PmStore, one sparse file per inode, volume "
    "or object. The S3 HTTP gateway and the native wire protocol store content-addressed chunks through "
    "ChunkManager, whose rows live in the RocksDB metadata store. In cluster mode an S3 PUT is replicated "
    "to peer nodes after the local write, over RDMA when possible and the TCP RPC otherwise, and its "
    "manifest is proposed through Raft. Standalone binaries run without the metadata store.",
    "Solid: data path. Dashed: cluster mode only. Ports are nvstor-server defaults (bins/nvstor-server/src/config.rs).",
)

R = [160, 244, 328, 412, 496]        # row tops
H = 64                               # compact card height
mid = lambda r: R[r] + H // 2

# Clients
f.group(30, 110, 250, 480, "Clients")
f.hcard("c-nfs", 45, R[0], 220, H, "icon-third", "Linux NFS client", "kernel; kubelet via nvstor-csi")
f.hcard("c-nbd", 45, R[1], 220, H, "icon-third", "nbd-client", "Linux NBD")
f.hcard("c-fast", 45, R[2], 220, H, "icon-actor", "Fast-path clients", "s3_hydrate, S3RdmaClient")
f.hcard("c-s3", 45, R[3], 220, H, "icon-third", "S3 clients", "aws CLI, SDKs")
f.hcard("c-wire", 45, R[4], 220, H, "icon-actor", "Native Rust client", "nvstor-client")

# nvstor-server
f.group(310, 110, 820, 590, "nvstor-server", "one process per node", kind="nv")
f.hcard("s-nfs", 330, R[0], 230, H, "icon-service", "NFS v4.2 server", "TCP 2049, RDMA 20049", nv=True)
f.hcard("s-nbd", 330, R[1], 230, H, "icon-service", "NBD server", "10809", nv=True)
f.hcard("s-fast", 330, R[2], 230, H, "icon-service", "S3-over-RDMA / TCP", "9877 + rails, 9879", nv=True)
f.hcard("s-s3", 330, R[3], 230, H, "icon-service", "S3 HTTP gateway", "9000, SigV4, IAM, SSE", nv=True)
f.hcard("s-wire", 330, R[4], 230, H, "icon-service", "Native wire protocol", "9100; volumes go to PmStore", nv=True)

f.card("d-pm", 620, R[0], 230, R[2] + H - R[0], "icon-data", "PmStore", "one sparse file per inode,", "volume or object (PmObjectStore)")
f.note(735, R[0] + 130, "POSIX or io_uring backend", "small", anchor="middle")
f.note(735, R[0] + 145, "no chunking, no hashing", "small", anchor="middle")
f.card("d-ca", 620, R[3], 230, R[4] + H - R[3], "icon-data", "ChunkManager", "sha256 chunks, 16 MiB, dedup", "objects up to 64 KiB inline")
f.hcard("d-meta", 620, 610, 230, H, "icon-db", "MetadataStore", "RocksDB, 10 column families")

f.hcard("x-iam", 900, R[0], 210, H, "icon-guard", "IAM", "users, groups, policies", nv=True)
f.hcard("x-kms", 900, R[1], 210, H, "icon-guard", "KMS", "envelope keys for SSE", nv=True)
f.hcard("x-place", 900, R[3], 210, H, "icon-service", "Placement + replication", "jump hash, rack-aware", nv=True)
f.hcard("x-raft", 900, R[4], 210, H, "icon-service", "Raft", "9102, metadata log", nv=True)

# Peer nodes
f.group(1170, 390, 200, 190, "Other nodes", "cluster mode")
f.card("p-peer", 1190, 440, 160, 112, "icon-server", "nvstor-server", "REPLICATE, RPC 9878", "Raft 9102")

# Client -> front end
for r, (a, b) in enumerate([("c-nfs", "s-nfs"), ("c-nbd", "s-nbd"), ("c-fast", "s-fast"), ("c-s3", "s-s3"), ("c-wire", "s-wire")]):
    f.path(f"M265 {mid(r)} L330 {mid(r)}")
# Front end -> data plane
for r in range(5):
    f.path(f"M560 {mid(r)} L620 {mid(r)}")
f.path(f"M735 {R[4] + H} L735 610")                                   # chunk rows -> RocksDB
# Cluster mode
f.path(f"M850 {mid(3)} L900 {mid(3)}", opt=True)                      # after the local write
f.path(f"M1110 {mid(3)} L1190 {mid(3)}", opt=True)
f.path(f"M1110 {mid(4)} L1190 {mid(4)}", opt=True)

# Standalone binaries
f.group(30, 730, 1340, 124, "Standalone binaries", "no metadata store; each serves or copies a plain local directory")
f.hcard("b-export", 50, 780, 300, H, "icon-folder", "nvstor-nfs-export", "NFS TCP + RDMA export of a directory", nv=True)
f.hcard("b-peer", 380, 780, 300, H, "icon-folder", "nvstor-peer", "serve / fetch a directory over RDMA", nv=True)
f.hcard("b-fuse", 710, 780, 300, H, "icon-folder", "nvstor-mount + nvstor-fs-serve", "FUSE, read-only, TCP", nv=True)
f.hcard("b-csi", 1040, 780, 310, H, "icon-service", "nvstor-csi", "CSI v1; provisions NFS subdirectories", nv=True)

y = f.legend(30, 894, [
    ("A", "NFS file content, NBD volumes and the RDMA/TCP object fast paths write in place in PmStore. NFS inodes are kept in RocksDB; PmObjectStore keys need no metadata (the file id is a hash of the key)."),
    ("B", "In cluster mode an S3 PUT writes locally, then writes to every replica in parallel, waits for all of them, needs at least min_write_nodes to succeed, and proposes the manifest through Raft. Replication uses the S3-over-RDMA REPLICATE verb when the object fits a pool slot, otherwise (or if REPLICATE fails) the TCP RPC on rdma_port + 1."),
])
f.write(y + 10)
