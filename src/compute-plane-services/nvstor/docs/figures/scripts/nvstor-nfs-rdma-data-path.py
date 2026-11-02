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

"""docs/figures/nvstor-nfs-rdma-data-path.svg: one NFS READ over RPC-over-RDMA v1.

Source of truth: crates/nvstor-nfs/src/rdma_server.rs, crates/nvstor-nfs/src/rpcrdma/chunks.rs,
crates/nvstor-nfs/src/server.rs (limits), docs/nfs-rdma.md.
"""
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig

f = Fig(
    "nvstor-nfs-rdma-data-path", 1300, 900,
    "NFS over RDMA: One READ, Zero Copy",
    "A Linux kernel NFS client sends an NFS call over an RDMA reliable-connection queue pair. "
    "The connection's I/O thread hands the COMPOUND to the same NFS workers that serve NFS over TCP. "
    "For a READ the file is read straight into a slot of the device's registered pool and the server "
    "RDMA-writes the slot into the client's Write chunk, then sends the reply. A WRITE runs the other way: "
    "the server RDMA-reads the client's Read chunk into a slot and writes the file from it. "
    "Connections are admitted by the RDMA-CM listener against the client allow list.",
    "nvstor-nfs-export and nvstor-server share this listener (crates/nvstor-nfs/src/rdma_server.rs). Numbers 1-5 are one READ.",
)

# Client side (third-party: the Linux kernel client)
f.group(40, 110, 260, 500, "Client node or pod", "Linux kernel, xprtrdma", kind="partner")
f.card("card-client", 70, 160, 200, 112, "icon-third", "Kernel NFS client", "mount proto=rdma / rdma6", "vers=4.1 or 4.2")
f.card("card-cmem", 70, 350, 200, 112, "icon-hardware", "Client memory", "Write chunk (READ data)", "Read chunk (WRITE data)")

# Fabric lane label
f.note(450, 136, "RoCE v2 or InfiniBand", "note-b", anchor="middle")
f.note(450, 151, "RC queue pairs, one per rail", "note", anchor="middle")

# Server side
f.group(600, 110, 660, 500, "nvstor-nfs-export or nvstor-server", "NFS/RDMA listener, port 20049, dual-stack [::]", kind="nv")
f.card("card-io", 640, 160, 170, 112, "icon-run", "Connection I/O thread", "128 credits", "posts WRITE chains + SEND", nv=True)
f.card("card-workers", 870, 160, 160, 112, "icon-service", "NFS COMPOUND workers", "same processor as TCP", nv=True)
f.card("card-file", 1070, 160, 160, 112, "icon-folder", "Export directory", "or PmStore (nvstor-server)")
f.card("card-pool", 950, 350, 280, 112, "icon-hardware", "Registered pool, per device", "256 MiB of 1 MiB slots", "on-demand MRs, pinned-memory caps")
f.card("card-cm", 640, 480, 170, 112, "icon-admit", "RDMA-CM listener", "--allow-client, 64 per client", "15 s first-call timeout", nv=True)

# Primary path: READ
f.path("M270 196 L640 196"); f.callout(1, 455, 196)                 # call SEND
f.path("M810 216 L870 216"); f.callout(2, 836, 216)                 # dispatch
f.path("M1030 216 L1070 216")                                        # read the file
f.path("M1130 272 L1130 350"); f.callout(3, 1130, 306)              # file -> pool slot
f.path("M950 396 L270 396"); f.callout(4, 455, 396)                 # RDMA WRITE into Write chunk
f.path("M640 236 L270 236"); f.callout(5, 455, 236)                 # reply SEND

# Optional paths: WRITE direction and connection setup
f.path("M270 436 L950 436", opt=True); f.plabel(560, 440, "WRITE: server RDMA-reads the Read chunk")
f.path("M1200 350 L1200 272", opt=True)
f.path("M300 536 L640 536", opt=True); f.plabel(470, 540, "RDMA-CM connect")

y = f.legend(40, 652, [
    (1, "The client SENDs the call inline (4 KiB negotiated, RFC 8797) with a Write chunk naming where READ data should land. A call holds one of 128 credits until its reply is posted."),
    (2, "The connection's I/O thread hands the COMPOUND to NFS workers: the same COMPOUND processor, session table and export policy as NFS over TCP."),
    (3, "The file is read (O_DIRECT when offset and length are 4 KiB aligned) straight into a 1 MiB pool slot. With the pool empty the transfer registers its own memory, within per-connection and per-process caps."),
    (4, "The server RDMA-WRITEs the slot into the client's Write chunk. Chains are posted without waiting on each one; only RPC/RDMA headers are copied."),
    (5, "The reply SEND follows the WRITE chain on the same queue pair. Dashed: a WRITE pulls the client's Read chunk into a slot with RDMA READ, then writes the file from it."),
])
f.write(y + 10)
