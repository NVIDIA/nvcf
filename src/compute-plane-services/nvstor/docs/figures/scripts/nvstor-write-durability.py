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

"""docs/figures/nvstor-write-durability.svg: when each front end's write is durable.

Source of truth: crates/nvstor-storage/src/durability.rs (SyncMode), pm/iouring.rs (linked write+fsync),
manager.rs (object_write Full, unsealed rows, flush), crates/nvstor-nfs/src/nfs4.rs (WRITE stability, COMMIT),
crates/nvstor-block/src/nbd.rs (FUA, FLUSH), bins/nvstor-server/src/main.rs + config.rs (fast_path_sync),
crates/nvstor-object/src/s3/handlers.rs (PUT, replicate, Raft), crates/nvstor-metadata/src/store.rs (commit_batch).
"""
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig

f = Fig(
    "nvstor-write-durability", 1090, 900,
    "Write Durability: Who Syncs, and When",
    "Every write API takes a SyncMode: None leaves bytes in the page cache, Data is fdatasync, Full is fsync. "
    "NFS unstable writes and NBD writes use None and rely on the protocol's barrier (COMMIT, FUA, FLUSH). "
    "The S3-over-RDMA and S3-over-TCP fast paths sync per the fast_path_sync setting, data by default. "
    "S3 HTTP PUT writes chunks with Full and its rows through RocksDB, then in cluster mode waits for every replica "
    "(at least min_write_nodes must succeed) and proposes the manifest through Raft.",
    "SyncMode: None = page cache, caller flushes later; Data = fdatasync; Full = fsync (crates/nvstor-storage/src/durability.rs).",
)

R = [160, 244, 328, 412, 496]
H = 64
mid = lambda r: R[r] + H // 2

f.group(30, 110, 330, 470, "Write requests")
f.hcard("w-nfs", 45, R[0], 300, H, "icon-third", "NFS WRITE, UNSTABLE4", "SyncMode::None; reply UNSTABLE4")
f.hcard("w-commit", 45, R[1], 300, H, "icon-third", "NFS COMMIT, stable WRITE", "fdatasync before the reply")
f.hcard("w-nbd", 45, R[2], 300, H, "icon-third", "NBD WRITE / FUA / FLUSH", "None / Data / flush(Data)")
f.hcard("w-fast", 45, R[3], 300, H, "icon-actor", "S3-RDMA PERSIST, S3-TCP PUT", "fast_path_sync, default data")
f.hcard("w-s3", 45, R[4], 300, H, "icon-third", "S3 HTTP PUT", "object_write: SyncMode::Full")

f.group(410, 110, 650, 620, "nvstor-server, one node", kind="nv")
f.card("d-pm", 450, R[0], 260, R[3] + H - R[0], "icon-data", "PmStore", "write(id, off, data, SyncMode)", "flush(id, SyncMode)")
f.note(580, R[0] + 132, "io_uring: write + fsync as", "small", anchor="middle")
f.note(580, R[0] + 145, "linked SQEs on one ring shard", "small", anchor="middle")
f.card("d-ca", 450, R[4], 260, 112, "icon-data", "ChunkManager", "chunks fsynced (Full)", "None: row unsealed until flush")
f.card("d-nvme", 780, R[1], 250, 2 * H + 20, "icon-hardware", "Local NVMe", "sparse file per inode,", "volume or object")
f.card("d-meta", 780, R[4], 250, 112, "icon-db", "MetadataStore", "commit_batch(batch, Full)", "chunk rows, manifest, inodes")
f.hcard("d-repl", 450, 640, 260, H, "icon-service", "Replicate, wait for all", "≥ min_write_nodes, else 503", nv=True)
f.hcard("d-raft", 780, 640, 250, H, "icon-service", "Raft", "manifest proposed", nv=True)

# Protocol -> store
f.path(f"M345 {mid(0)} L450 {mid(0)}"); f.callout(1, 385, mid(0))
f.path(f"M345 {mid(1)} L450 {mid(1)}"); f.callout(2, 385, mid(1))
f.path(f"M345 {mid(2)} L450 {mid(2)}"); f.callout(3, 385, mid(2))
f.path(f"M345 {mid(3)} L450 {mid(3)}"); f.callout(4, 385, mid(3))
f.path(f"M345 {mid(4)} L450 {mid(4)}"); f.callout(5, 385, mid(4))
# Store -> media
nv_mid = R[1] + H + 10
f.path(f"M710 {nv_mid} L780 {nv_mid}")
f.path(f"M710 {R[4] + 56} L780 {R[4] + 56}")
# Cluster mode
f.path(f"M195 {R[4] + H} L195 672 L450 672", opt=True); f.callout(6, 300, 672)
f.path("M710 672 L780 672", opt=True)

y = f.legend(30, 772, [
    (1, "An UNSTABLE4 WRITE lands in PmStore with SyncMode::None and is acknowledged as UNSTABLE4; the client keeps its copy until COMMIT. Inode metadata rides the per-COMPOUND RocksDB batch (commit_batch, Full)."),
    (2, "COMMIT, and DATA_SYNC4 or FILE_SYNC4 WRITEs, fdatasync the inode's file before the reply. If the sync fails the reply says UNSTABLE4, so the client must COMMIT."),
    (3, "NBD WRITE uses None, FUA uses Data, and FLUSH flushes the volume with Data. On io_uring a Data or Full write is a write SQE linked to an fsync SQE."),
    (4, "PERSIST and S3-over-TCP PUT go through PmObjectStore with storage.fast_path_sync: \"data\" by default (fdatasync before the reply); \"none\" is for replication-before-ACK or regenerable data."),
    (5, "S3 HTTP PUT writes chunks with Full, then chunk rows and the manifest in RocksDB. A chunk written with None has an unsealed row until ChunkManager::flush; a dedup hit never trusts an unsealed row from an earlier process."),
    (6, "Cluster mode only: the PUT then writes every replica in parallel and waits for all of them (REPLICATE over RDMA, else or on failure the TCP RPC); if fewer than min_write_nodes succeeded it deletes the local and replica copies and returns 503. Then the manifest is proposed through Raft."),
])
f.write(y + 10)
