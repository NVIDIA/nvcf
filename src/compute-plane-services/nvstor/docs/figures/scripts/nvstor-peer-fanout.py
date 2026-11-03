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

"""docs/figures/nvstor-peer-fanout.svg: nvstor-peer serve / fetch, the fetcher cap and serve-after-fetch.

Source of truth: crates/nvstor-peer/src/{server.rs,sessions.rs,fetch.rs,manifest.rs,content.rs},
crates/nvstor-peer/src/bin/nvstor-peer.rs, docs/peer-copy.md.
"""
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig

f = Fig(
    "nvstor-peer-fanout", 1400, 1000,
    "nvstor-peer: Copy a Directory Node to Node, Then Fan Out",
    "An origin node serves a directory read-only over RDMA on every rail. It admits at most --max-fetchers "
    "fetch sessions (default 4); a fifth fetcher is told BUSY in the bootstrap, before any queue pair exists, "
    "and tries its other sources. A replica started with --then-serve serves its finished copy, so later "
    "replicas fetch from it and the copy spreads as a tree. Inside one fetch, the manifest is read first, "
    "ranges are pulled in parallel and checked, files are staged privately and each is renamed into place "
    "when complete.",
    "The fetcher cap, BUSY and failover have run on RDMA hardware; trees of --then-serve fetchers have not. Numbers 1-3 are the fan-out; 4-7 one fetch.",
)

R = [162, 252, 342, 432, 522]
H = 64
mid = lambda r: R[r] + H // 2

# Origin
f.group(40, 110, 320, 610, "Origin node", "nvstor-peer serve, 9890 + rail", kind="nv")
f.card("o-serve", 70, R[0], 260, R[4] + H - R[0], "icon-service", "nvstor-peer serve", "--max-fetchers 4", "read-only export, every rail", nv=True)
f.note(200, R[0] + 132, "S3-over-RDMA GET only", "small", anchor="middle")
f.note(200, R[0] + 147, "CRC-32C + generation per range", "small", anchor="middle")
f.hcard("o-dir", 70, 632, 260, H, "icon-folder", "Model or checkpoint dir", "local NVMe")

# First wave
f.group(430, 110, 340, 500, "First fetchers", "fetch --then-serve")
f.hcard("r-a", 450, R[0], 300, H, "icon-server", "Replica A", "fetches, then serves its copy")
f.hcard("r-b", 450, R[1], 300, H, "icon-server", "Replica B", "fetches, then serves its copy")
f.hcard("r-c", 450, R[2], 300, H, "icon-server", "Replica C", "fetches, then serves its copy")
f.hcard("r-d", 450, R[3], 300, H, "icon-server", "Replica D", "fetches, then serves its copy")
f.hcard("r-busy", 450, R[4], 300, H, "icon-admit", "Fifth fetcher", "BUSY; tries its other sources")

# Later wave
f.group(840, 110, 340, 230, "Later fetchers", "--source origin --source replicas (repeatable)")
f.hcard("r-e", 860, R[0], 300, H, "icon-server", "Replica E", "fetches from A")
f.hcard("r-f", 860, R[1], 300, H, "icon-server", "Replica F", "fetches from B")

# Fan-out paths
f.path(f"M200 632 L200 {R[4] + H}")
for r in range(4):
    f.path(f"M330 {mid(r)} L450 {mid(r)}")
f.callout(1, 390, mid(0))
f.path(f"M330 {mid(4)} L450 {mid(4)}", opt=True); f.callout(2, 390, mid(4))
f.path(f"M750 {mid(0)} L860 {mid(0)}"); f.callout(3, 805, mid(0))
f.path(f"M750 {mid(1)} L860 {mid(1)}")

# Inside one fetch
f.group(40, 750, 1320, 134, "Inside one fetch (any replica)", "flock on DEST; one retry budget; staging removed on success or error")
X = [60, 326, 592, 858, 1124]
W = 220
f.hcard("p-man", X[0], 800, W, H, "icon-data", "Manifest v3", "/manifest3:PREFIX")
f.hcard("p-work", X[1], 800, W, H, "icon-run", "Range workers", "32 conns, 16 MiB ranges")
f.hcard("p-stage", X[2], 800, W, H, "icon-folder", "Private staging", ".nvstor-peer-staging-*")
f.hcard("p-dest", X[3], 800, W, H, "icon-folder", "DEST", "fsync, rename per file")
f.hcard("p-content", X[4], 800, W, H, "icon-data", "Content record", "DEST/.nvstor-peer-content")
for i, n in enumerate([4, 5, 6, 7]):
    a, b = X[i] + W, X[i + 1]
    f.path(f"M{a} 832 L{b} 832")
    f.callout(n, a + 16, 832)

y = f.legend(40, 920, [
    (1, "One fetch is one session (access key nvstor-peer:<session id>) across all its rails and connections; serve admits at most --max-fetchers of them."),
    (2, "The next fetcher gets BUSY in the bootstrap, before any queue pair exists. It skips that source without counting a failure and tries its other --source entries, backing off with jitter up to --wait-s (1800 s)."),
    (3, "A replica fetched with --then-serve serves its copy under --prefix. Mid-copy failover only goes to a source whose manifest (v2 or later) has the same size and content identity for every unfinished file."),
    (4, "The fetcher reads the manifest (at most --max-manifest-mib, default 256 MiB): path, size, generation, content identity and attributes of every entry, and then every file's ranges in parallel; each range is CRC-32C checked and pinned to the file's generation."),
    (5, "Ranges land in a 0700 staging directory under DEST, opened with openat2 beneath DEST and no symlinks."),
    (6, "Each complete file gets the source's mtime and mode (and owner, when the fetch may chown) on its staged descriptor, is fsynced (fdatasynced with --no-attrs) and renamed into DEST; directory attributes are applied and the directories fsynced at the end."),
    (7, "The fetch records each file's content identity, which a later serve of DEST advertises while the file is unchanged."),
])
f.write(y + 10)
