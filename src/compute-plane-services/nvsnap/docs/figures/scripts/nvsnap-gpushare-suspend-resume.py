#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-gpushare-suspend-resume.svg: what nvsnap-gpu-suspend does
around a CRIU checkpoint and restore.

Sources: docker/agent/gpushare/nvsnap-gpu-suspend.c (holder: quiesce, release,
lock_all, freeze, checkpoint; undo/rollback; resume_unheld: full_restore,
remap_all: numa, load, fabric exchange, remap, resume), gpushare.h (the
control commands), internal/agent/gpushare.go (suspend, stop, resume with
--gpu-map and --fabric-map; the store moved into <checkpoint>/gpushare).
"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-gpushare-suspend-resume", 1240, 860,
    "gpushare: Suspend for a Checkpoint, Resume After a Restore",
    "Capture: nvsnap-gpu-suspend quiesces every process that loads the gpushare library, releases the "
    "GPU memory they share and saves their cuMem allocations to the chunk store, then locks and "
    "checkpoints them through the CUDA checkpoint API and holds them frozen; on stop, the agent dumps "
    "them with CRIU. A failed step is rolled back and the workload runs on. Restore: after CRIU "
    "restores the processes, resume restores and unlocks them in the driver, loads the saved "
    "allocations at the same addresses, re-imports shared memory and multicast objects, and reopens "
    "kernel launches.",
    "Each step is a command to every process's gpushare control thread, or a CUDA checkpoint API call. "
    "Solid paths are the normal run; the dashed path is the rollback.",
)

C = [70, 300, 530, 760, 990]  # column x
W, H = 180, 118
CAP_Y, MID_Y, RES_Y = 150, 330, 500

# Capture row, left to right.
f.group(40, CAP_Y - 48, 1160, H + 74, "Capture: nvsnap-gpu-suspend suspend, then stop",
        "run by the agent inside the pod's pid and network namespaces", kind="nv")
f.card("card-quiesce", C[0], CAP_Y, W, H, "icon-guard", "1  Quiesce",
       "hold launches, drain the GPU", "check all can be released")
f.card("card-release", C[1], CAP_Y, W, H, "icon-data", "2  Release",
       "unmap shared memory, save", "cuMem allocations, free them")
f.card("card-lock", C[2], CAP_Y, W, H, "icon-hardware", "3  Driver lock",
       "cuCheckpointProcessLock", "every process in parallel")
f.card("card-ckpt", C[3], CAP_Y, W, H, "icon-hardware", "4  Driver checkpoint",
       "cuCheckpointProcessCheckpoint", "processes held frozen")
f.card("card-dump", C[4], CAP_Y, W, H, "icon-third", "5  CRIU dump",
       "after stop; host memory only", "no CUDA plugin")
cy = CAP_Y + H // 2
for a, b in zip(C, C[1:]):
    f.path(f"M{a + W} {cy} L{b} {cy}")

# Middle row: rollback, chunk store, checkpoint.
f.card("card-rollback", C[0], MID_Y, W, 112, "icon-guard", "Rollback",
       "driver restore and unlock,", "load, remap, resume")
f.card("card-store", C[1], MID_Y, C[2] + W - C[1], 112, "icon-data", "Chunk store (/nvsnap-gpushare)",
       "64 MiB content-addressed chunks; a chunk already stored", "is not written again; all-zero chunks are not stored")
f.card("card-image", C[4], MID_Y, W, 112, "icon-folder", "Node-local checkpoint",
       "CRIU images, and the store", "moved in as gpushare/")

# Restore row, right to left.
f.group(40, RES_Y - 48, 1160, H + 74, "Restore: CRIU restore, then nvsnap-gpu-suspend resume",
        "--gpu-map onto the placeholder's GPUs; --fabric-map across nodes", kind="nv")
f.card("card-restore", C[4], RES_Y, W, H, "icon-third", "6  CRIU restore",
       "same pids, same addresses", "processes stopped")
f.card("card-unlock", C[3], RES_Y, W, H, "icon-hardware", "7  Driver restore",
       "restore, then unlock", "every process in parallel")
f.card("card-load", C[2], RES_Y, W, H, "icon-data", "8  Load",
       "re-create allocations at the", "same addresses and access")
f.card("card-remap", C[1], RES_Y, W, H, "icon-compute", "9  Remap",
       "re-import peers' memory,", "rebuild multicast objects")
f.card("card-resume", C[0], RES_Y, W, H, "icon-compute", "10  Resume",
       "bind multicast memory,", "reopen launches: serving")
ry = RES_Y + H // 2
for a, b in zip(C[::-1], C[::-1][1:]):
    f.path(f"M{a} {ry} L{b + W} {ry}")

# Between the rows.
c = lambda i: C[i] + W // 2  # noqa: E731
f.path(f"M{c(1)} {CAP_Y + H} L{c(1)} {MID_Y}")
f.plabel(c(1) + 6, MID_Y - 16, "save", anchor="start")
f.path(f"M{c(2)} {MID_Y + 112} L{c(2)} {RES_Y}")
f.plabel(c(2) + 6, RES_Y - 22, "read", anchor="start")
f.path(f"M{c(4)} {CAP_Y + H} L{c(4)} {MID_Y}")
f.path(f"M{c(4)} {MID_Y + 112} L{c(4)} {RES_Y}")
f.path(f"M{c(0)} {CAP_Y + H + 26} L{c(0)} {MID_Y}", opt=True)
f.plabel(c(0) + 6, MID_Y - 16, "any step 1-4 fails", anchor="start")

y = f.legend(40, 690, [
    (1, "Quiesce holds every process's kernel and graph launches, copies and memsets in a gate and drains the GPU. "
        "Across nodes, each node votes and none releases until every node is quiesced (--fabric-map)."),
    (2, "Release unmaps memory imported from other processes (NCCL P2P, CUDA IPC, fabric handles), drops "
        "multicast objects, unregisters host memory, and saves then frees the cuMem allocations; addresses stay reserved."),
    (8, "Load re-creates the saved allocations; 9 remap re-imports from each exporter, with new fabric handles "
        "exchanged between nodes; 10 resume rejoins multicast and reopens launches. Pointers and captured CUDA graphs stay valid."),
])
f.write(y + 14)
