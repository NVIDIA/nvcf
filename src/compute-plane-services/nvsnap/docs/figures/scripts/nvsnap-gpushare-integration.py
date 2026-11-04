#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-gpushare-integration.svg: how nvsnap runs gpushare around a
CRIU capture and restore of one GPU pod.

Sources: internal/webhook/gpushare.go (gpushareMode: on by default where CRIU
is the capture method, fabric when the engine spans pods; gpusharePatches:
bundle at /nvsnap, LD_PRELOAD, per-pod store at /nvsnap-gpushare from
gpushare-pods/<pod uid>), internal/webhook/criu_restore.go (placeholder),
internal/agent/gpushare.go (gpus, suspend, stop, resume --gpu-map; the store
moved into <checkpoint>/gpushare), internal/agent/checkpoint_v2.go (CRIU dump
without the CUDA plugin, --image-io-mode direct),
internal/agent/cascade_fetch.go (EnsureLocal), internal/agent/criu_auto_restore.go.
"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-gpushare-integration", 1240, 900,
    "gpushare in nvsnap: From Admission to a Restored Pod",
    "At admission the nvsnap webhook gives every GPU container of a pod nvsnap may capture the gpushare "
    "library, the node bundle and a per-pod chunk store. At capture, the agent on the pod's node runs "
    "nvsnap-gpu-suspend suspend and stop in the pod, dumps it with CRIU and moves the chunk store into the "
    "checkpoint. A later pod of the same configuration becomes a placeholder; the agent on its node gets "
    "the checkpoint, restores it with CRIU and resumes the GPU processes on the placeholder's GPUs.",
    "Where CRIU is the capture method, gpushare is on for every GPU pod that is not a Job's or a task's; "
    "nvsnap.io/gpushare: \"false\" turns it off.",
)

LX, LW, LH = 40, 180, 112
f.card("card-nvca", LX, 130, LW, LH, "icon-actor", "NVCA or a controller", "creates the pod")
f.card("card-api", LX, 330, LW, LH, "icon-third", "Kubernetes API", "pod CREATE")
f.card("card-webhook", LX, 510, LW, LH, "icon-admit", "nvsnap webhook", "served by every agent", nv=True)

f.path(f"M{LX + LW // 2} {130 + LH} L{LX + LW // 2} 330")
f.callout(1, LX + LW // 2, 286)
f.path(f"M{LX + 60} {330 + LH} L{LX + 60} 510")
f.callout(2, LX + 60, 476)
f.path(f"M{LX + 120} 510 L{LX + 120} {330 + LH}")
f.callout(3, LX + 120, 476)

GX, GW = 500, 700
NX = [GX + 20, GX + 255, GX + 490]
NW, NH = 190, 118

# Capture node.
CY = 120
f.group(GX, CY, GW, NH + 82, "GPU node: the captured pod", "agent DaemonSet, local checkpoint disk")
f.card("card-pod", NX[0], CY + 56, NW + 10, NH, "icon-compute", "Workload pod",
       "LD_PRELOAD libnvsnap_gpushare.so", "/nvsnap bundle, /nvsnap-gpushare store")
f.card("card-agent-c", NX[1] + 10, CY + 56, NW - 10, NH, "icon-service", "nvsnap agent",
       "suspend and stop in the pod,", "then the CRIU dump", nv=True)
f.card("card-ckpt", NX[2], CY + 56, NW, NH, "icon-folder", "Node-local checkpoint",
       "CRIU images; the store", "moved in as gpushare/")
pc = CY + 56 + NH // 2
f.path(f"M{NX[1] + 10} {pc} L{NX[0] + NW + 10} {pc}")
f.callout(5, (NX[1] + 10 + NX[0] + NW + 10) // 2, pc)
f.path(f"M{NX[1] + NW} {pc} L{NX[2]} {pc}")
f.callout(6, (NX[1] + NW + NX[2]) // 2, pc)

# Restore node.
RY = 440
f.group(GX, RY, GW, NH + 82, "GPU node: a later pod of the same configuration",
        "any node; nodes that hold the checkpoint preferred")
f.card("card-ph", NX[0], RY + 56, NW + 10, NH, "icon-compute", "Placeholder pod",
       "same spec, GPU container waits;", "checkpoint's gpushare/ at the store path")
f.card("card-agent-r", NX[1] + 10, RY + 56, NW - 10, NH, "icon-service", "nvsnap agent",
       "CRIU restore, then", "resume --gpu-map", nv=True)
f.card("card-copy", NX[2], RY + 56, NW, NH, "icon-folder", "Checkpoint copy",
       "on this node's disk", nv=False)
rc = RY + 56 + NH // 2
f.path(f"M{NX[2]} {rc} L{NX[1] + NW} {rc}")
f.callout(8, (NX[1] + NW + NX[2]) // 2, rc)
f.path(f"M{NX[1] + 10} {rc} L{NX[0] + NW + 10} {rc}")
f.callout(9, (NX[1] + 10 + NX[0] + NW + 10) // 2, rc)

# Checkpoint reaches the restore node.
cx = NX[2] + NW // 2
f.path(f"M{cx} {CY + 56 + NH} L{cx} {RY + 56}")
f.callout(7, cx, (CY + 56 + NH + RY + 56) // 2)
f.plabel(cx - 16, (CY + 56 + NH + RY + 56) // 2 + 4, "local, cluster storage or a peer", anchor="end")

# The API server schedules the patched pods.
f.path(f"M{LX + LW} 370 L440 370 L440 {pc} L{NX[0]} {pc}")
f.callout(4, 440, 280)
f.path(f"M{LX + LW} 402 L470 402 L470 {rc} L{NX[0]} {rc}")
f.callout(4, 470, 476)

y = f.legend(40, 700, [
    (1, "NVCA (for an NVCF function) or any controller creates the pod. 2 The API server sends it to the nvsnap "
        "webhook, which 3 returns a patch: the bundle read-only at /nvsnap, the library appended to LD_PRELOAD, and a "
        "per-pod directory of the node's checkpoint disk at /nvsnap-gpushare. An engine that spans pods also gets "
        "NVSNAP_GPUSHARE_FABRIC=1. A pod whose configuration has a usable capture becomes a placeholder instead."),
    (5, "Once the pod is Ready, its node's agent runs nvsnap-gpu-suspend gpus, suspend and stop inside the pod, and "
        "6 dumps it with CRIU without the CUDA plugin; the chunk store moves into the checkpoint."),
    (7, "The restore node's agent uses its own copy, or fetches one from cluster storage or a peer agent. 8 It "
        "restores the processes with CRIU and 9 resumes them on the placeholder's GPUs (--gpu-map)."),
])
f.write(y + 14)
