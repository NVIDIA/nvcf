#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-gpushare-memory.svg: the GPU memory gpushare tracks, what
"release" does with each kind before the driver checkpoint, and what "load",
"remap" and "resume" do after the restore.

Sources: docker/agent/gpushare/gpushare.h (the header comment: quiesce,
release, remap, resume, fabmap; legacy IPC handles; IMEX re-subscription),
docker/agent/gpushare/gpushare.c (do_release, do_load, do_remap, do_resume),
docs/design/GPUSHARE.md "How it works" and "Chunk store".
"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-gpushare-memory", 1240, 700,
    "gpushare: The Memory It Releases and Re-Creates",
    "The CUDA checkpoint API cannot checkpoint a process that maps GPU memory another process exported. "
    "For each kind of shared memory, the figure shows what gpushare's release does before the driver "
    "checkpoint and what load, remap and resume do after the restore. Allocations are saved to a chunk "
    "store and re-created at the same virtual addresses, so pointers and captured CUDA graphs stay valid.",
    "Tracked in every process that preloads libnvsnap_gpushare.so; driven by nvsnap-gpu-suspend.",
)

KX, KW = 40, 330          # memory kind
RX, RW = 430, 330         # at release
SX, SW = 820, 380         # after restore
Y0, RH, GAP = 128, 62, 14

f.note(KX, Y0 - 14, "Memory the processes share", "note-b")
f.note(RX, Y0 - 14, "At release, before the driver checkpoint", "note-b")
f.note(SX, Y0 - 14, "After the restore", "note-b")

rows = [
    ("icon-data", "Imported cuMem memory", "NCCL peer-to-peer buffers",
     "Unmap and release", "the address range stays reserved",
     "Remap: re-import from the exporter", "fresh export, same address, same access"),
    ("icon-data", "CUDA IPC handles", "vLLM custom all-reduce, PyTorch",
     "Served by gpushare's own handles", "on cuMem memory (2 MiB and up)",
     "Remap: re-open from the exporter", "the handle the app holds stays valid"),
    ("icon-data", "Multicast objects", "NCCL NVLS",
     "Unbind, unmap and drop", "",
     "Load and remap: re-create or re-import,", "rejoin devices; resume binds the memory"),
    ("icon-data", "Fabric handles", "multi-node NVLink, over IMEX",
     "Unmap and release, as imports", "",
     "Handle exchange (--fabric-map): new", "handles between nodes; remap re-imports"),
    ("icon-hardware", "Page-locked host memory", "Grace, driver 580: the driver restores none",
     "Unregister", "",
     "Remap: register again", ""),
    ("icon-data", "cuMem allocations", "cuMemAlloc of 2 MiB and up; app-shared",
     "Save to the chunk store, then free", "the address range stays reserved",
     "Load: re-create at the same address", "with the same properties and access"),
]
for i, (sym, k, ks, r, rs, s, ss) in enumerate(rows):
    y = Y0 + i * (RH + GAP)
    f.hcard(f"card-k{i}", KX, y, KW, RH, sym, k, ks)
    f.tcard(f"card-r{i}", RX, y, RW, RH, r, rs or None)
    f.tcard(f"card-s{i}", SX, y, SW, RH, s, ss or None)
    cy = y + RH // 2
    f.path(f"M{KX + KW} {cy} L{RX} {cy}")
    f.path(f"M{RX + RW} {cy} L{SX} {cy}")
    if i in (1, 2):  # footnotes below
        f.callout(i, (KX + KW + RX) // 2, cy)

y = Y0 + len(rows) * (RH + GAP) + 16
f.hcard("card-store", RX, y, SX + SW - RX, 70, "icon-folder",
        "Chunk store: 64 MiB content-addressed chunks",
        "Written once per content (model weights repeat across checkpoints); all-zero chunks are not stored;",
        "each chunk written to a temporary file, flushed, then renamed. Moved into the checkpoint as gpushare/.")
last = Y0 + 5 * (RH + GAP) + RH
f.path(f"M{RX + 60} {last} L{RX + 60} {y}")
f.plabel(RX + 66, last + 14, "save", anchor="start")
f.path(f"M{SX + 60} {y} L{SX + 60} {last}")
f.plabel(SX + 66, last + 14, "read", anchor="start")

y = f.legend(40, y + 104, [
    (1, "Smaller cuMemAlloc memory stays legacy memory. gpushare hands out its own handle for it and makes the driver "
        "export only when a peer opens the handle; a handle that is opened stays a legacy export, which the driver "
        "checkpoint cannot handle."),
    (2, "Multicast needs a driver that lets a restored process create and join multicast objects: 610.57.04 on GB300 "
        "does, 580.173.02 does not."),
])
f.write(y + 14)
