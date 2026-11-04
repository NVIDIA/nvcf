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

"""docs/figures/nvstor-s3-rdma-v2.svg: S3-over-RDMA protocol v2, authenticated bootstrap then one GET.

Source of truth: crates/nvstor-rdma/src/bootstrap.rs, transport.rs, s3_rdma.rs, s3_rdma_proto.rs,
connection.rs, auth.rs; bins/nvstor-server/src/main.rs (pool access flags, routing).
"""
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig

f = Fig(
    "nvstor-s3-rdma-v2", 1300, 900,
    "S3-over-RDMA Protocol v2: Authenticate, Then the Server Moves the Bytes",
    "A client connects over TCP. The server sends a random challenge; the client proves a credential with an "
    "HMAC over the challenge before any queue pair exists. Admission maps the credential to a role, applies "
    "connection limits and, in nvstor-peer, the fetcher cap. Then queue pairs are created; server queue pairs "
    "grant no remote access. A signed GET on lane 0 names the client's buffer; the server reads the range into a "
    "local pool slot and RDMA-writes it into that buffer on a bulk lane, then replies with the generation and "
    "CRC-32C. PERSIST is the reverse: the server RDMA-reads the buffer the client named.",
    "nvstor-server (S3-over-RDMA, rail 0 on 9877) and nvstor-peer serve (9890 + rail) run this protocol. Numbers 1-6 are a bootstrap and one GET.",
)

# Client
f.group(40, 110, 290, 500, "Client node", "nvstor-peer fetch, s3_hydrate", kind="nv")
f.card("c-client", 70, 160, 230, 262, "icon-service", "S3RdmaClient", "one RdmaTransport per rail", "control_txn: one request per peer", nv=True)
f.card("c-buf", 70, 470, 230, 112, "icon-hardware", "Client buffer", "registered, REMOTE_WRITE", "named per request")

f.note(490, 136, "TCP bootstrap, then RC queue pairs", "note-b", anchor="middle")
f.note(490, 151, "lane 0: SEND/RECV control; lanes 1+: bulk", "note", anchor="middle")

# Server
f.group(650, 110, 610, 500, "Server: nvstor-server or nvstor-peer serve", "one transport per RDMA NIC (rail)", kind="nv")
f.card("s-boot", 690, 160, 170, 112, "icon-guard", "Bootstrap", "HELLO challenge, AUTH", "HMAC-SHA256", nv=True)
f.card("s-admit", 920, 160, 170, 112, "icon-admit", "Admission", "role, AcceptLimits", "BUSY (nvstor-peer)", nv=True)
f.card("s-disp", 690, 300, 170, 112, "icon-run", "Lane 0 dispatcher", "verify signature, replay", "IAM action, permit", nv=True)
f.card("s-store", 1060, 300, 170, 112, "icon-data", "PmObjectStore", "or nvstor-peer export")
f.card("s-pool", 920, 470, 310, 112, "icon-hardware", "Server pool slot", "LOCAL access; never advertised", "rail 0 REMOTE_READ in nvstor-server")

# Primary path: bootstrap and one GET
f.path("M300 206 L690 206"); f.callout(1, 495, 206)
f.path("M860 216 L920 216"); f.callout(2, 890, 216)
f.path("M300 336 L690 336"); f.callout(3, 495, 336)
f.path("M860 356 L1060 356"); f.callout(4, 960, 356)
f.path("M1145 412 L1145 470")
f.path("M920 512 L300 512"); f.callout(5, 495, 512)
f.path("M690 376 L300 376"); f.callout(6, 495, 376)

# Optional: PERSIST runs the other way
f.path("M300 552 L920 552", opt=True); f.plabel(700, 556, "PERSIST: server RDMA-READs the named buffer")
f.path("M1200 470 L1200 412", opt=True)

y = f.legend(40, 652, [
    (1, "The server sends HELLO with a 32-byte random challenge and its GID. The client answers AUTH: access key, GID, one QPN per lane and an HMAC-SHA256 over \"nvstor-rdma-bootstrap-v3\\0\", the HELLO and its own message."),
    (2, "The verifier maps the credential to Denied (REJECT), Client or Cluster; only Cluster may REPLICATE. Accept limits: 64 handshakes, 4096 peers, 512 per source address, 5 s. nvstor-peer's admitter answers BUSY past --max-fetchers. Only now are CQs and QPs created, and the TCP stream stays open as the liveness channel."),
    (3, "The client SENDs a signed GET naming its buffer (address, rkey, length) and, optionally, the generation it expects. A process-wide replay guard rejects a reused nonce."),
    (4, "The dispatcher reads the range into a slot of the server's own pool (PmObjectStore in nvstor-server, the exported directory in nvstor-peer)."),
    (5, "The server RDMA-WRITEs the slot into the client buffer on a bulk lane: chains of at most 128 work requests, only the last one signaled."),
    (6, "The reply on lane 0 carries length, object size, generation and CRC-32C; the client checks both. Dashed: PERSIST names the buffer and its CRC-32C after ALLOC reserved a slot; the server RDMA-READs it, recomputes the CRC and stores it."),
])
f.write(y + 10)
