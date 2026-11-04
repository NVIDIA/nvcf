#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-instance-capture-restore.svg: an instance (a StatefulSet,
LeaderWorkerSet or Grove group) captured and restored as one group.

Sources: internal/agent/criu_group.go (criuGroupCaptureLeader, captureOptedIn,
identityBlocksCapture, criuGroupConfigMapName, updateGroupRecord,
replaceColliding), internal/agent/criu_group_guard.go (instanceCoversEngine,
freezeTolerated, agentsSettled), internal/agent/capture_preflight.go,
internal/agent/group_freeze.go, internal/agent/gpushare_group_restore.go
(groupRestore), internal/webhook/criu_group.go (nvsnap.io/criu-group,
-ordinal, -size).
"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-instance-capture-restore", 1240, 900,
    "Instances: Captured and Restored as One Group",
    "The agent on rank 0's node leads. Once the instance is Ready it checks the instance and has every node's "
    "agent dump its own member at a shared freeze point, then writes a group record naming each member's "
    "checkpoint. A later instance of the same configuration and size is admitted as placeholders, one per "
    "slot; the leader restores every member with one address map and connections locked: the GPU ranks first, "
    "each reloading its GPU memory, then the companions; then it unlocks them all.",
    "Two GPU nodes and the companions of one tensor-parallel instance. The same paths carry the capture and "
    "the restore.",
)

f.hcard("card-leader", 40, 100, 1160, 90, "icon-service", "Leader: the agent on rank 0's node",
        "checks the instance, drives the freeze point and writes the group record;",
        "at restore, restores the GPU ranks with their GPU memory, then the companions, then unlocks them all", nv=True)

GX = [40, 435, 830]
GW = 370
members = [
    ("GPU node", "rank 0's node", "Rank 0 pod", "GPU ranks under gpushare", "or its placeholder"),
    ("GPU node", "rank 1's node", "Rank 1 pod", "GPU ranks under gpushare", "or its placeholder"),
    ("Any node", "the companions' node", "Companion pods", "etcd, NATS, a frontend; no GPUs", "or their placeholders"),
]
for i, (g, gs, m, ms, ms2) in enumerate(members):
    x = GX[i]
    f.group(x, 240, GW, 340, g, gs)
    f.card(f"card-agent{i}", x + 20, 300, GW - 40, 100, "icon-service", "nvsnap agent",
           "dumps or restores its own node's member", nv=True)
    f.card(f"card-member{i}", x + 20, 450, GW - 40, 110, "icon-compute", m, ms, ms2)
    cx = x + GW // 2
    f.path(f"M{cx} 190 L{cx} 300")
    f.path(f"M{cx} 400 L{cx} 450")
f.callout(1, GX[0] + GW // 2, 220)
f.callout(2, GX[0] + GW // 2, 425)

# Group record and placeholders.
f.card("card-record", 40, 640, 350, 112, "icon-db", "Group record (ConfigMap)",
       "every member's checkpoint and slot", nv=False)
f.path("M422 190 L422 696 L390 696")
f.callout(3, 422, 620)
f.card("card-webhook", 455, 640, 330, 112, "icon-admit", "nvsnap webhook",
       "a new member becomes the placeholder", "of its slot", nv=True)
f.path(f"M{GX[1] + GW // 2} 640 L{GX[1] + GW // 2} 560")
f.callout(4, GX[1] + GW // 2, 600)

y = f.legend(40, 800, [
    (1, "The leader refuses the capture unless the instance is opted in (when it has more than one pod), every rank runs under gpushare, its ranks form a whole engine, no "
        "member's liveness probe would restart it during the freeze, every node's agent runs the same revision, "
        "and every member passes the preflight."),
    (2, "Each node's agent dumps its own member. Every member waits at the freeze point and the leader releases "
        "them together, or aborts them all (next figure). 3 The group record names every member's checkpoint."),
    (4, "Each new member of an instance of the same configuration and size becomes a placeholder of its slot "
        "(nvsnap.io/criu-group, its ordinal and the group size). A member that cannot restore with its instance "
        "starts cold; the other placeholders then cannot restore together and are deleted once the restore stalls. A member whose new address collides with an old one is replaced."),
])
f.write(y + 14)
