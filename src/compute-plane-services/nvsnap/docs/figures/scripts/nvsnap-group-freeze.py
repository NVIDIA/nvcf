#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-group-freeze.svg: the group freeze point of a group capture.

Sources: internal/agent/group_freeze.go (the package comment; awaitGroupFreeze,
the post-dump barrier script and markPostDumpReached, freezeHandler with
?phase=post, driveGroupFreeze: release with acknowledgement and retries,
abort on a member that ends first or misses its release, groupFreezeWait).
"""
import pathlib
import sys
import textwrap

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-group-freeze", 1240, 900,
    "The Group Freeze Point: Stop Together, Resume Together",
    "Each member of a group capture is dumped by its own node's agent after its own preparation. Every member "
    "waits at a freeze point; the leader's agent, the driver, releases them all at once when every member has "
    "arrived, or aborts them all. After the dump, each member's CRIU waits in its post-dump hook with its "
    "process tree stopped and its network locked, until the driver releases them all again.",
    "Twice per capture: before the dump and after it. A member that ends first, or misses a release, aborts the "
    "capture for every member.",
)

f.hcard("card-driver", 40, 100, 1160, 90, "icon-service", "Driver: the leader's agent",
        "polls every member's agent until all have reached the freeze point, then posts go to all;",
        "a member that ends first, or a go that is not acknowledged after retries, makes it post abort to all",
        nv=True)

GX = [40, 435, 830]
GW = 370
names = [("Member: rank 0", "a GPU rank: its gpushare suspend takes minutes"),
         ("Member: rank 1", "another GPU rank, on another node"),
         ("Member: a companion", "etcd, NATS: nothing to prepare")]
for i, (g, gs) in enumerate(names):
    x = GX[i]
    f.group(x, 240, GW, 370, g, gs)
    f.card(f"card-agent{i}", x + 20, 300, GW - 40, 100, "icon-service", "nvsnap agent",
           "holds its member at the freeze point", nv=True)
    f.card(f"card-criu{i}", x + 20, 470, GW - 40, 110, "icon-third", "CRIU dump",
           "then the post-dump hook: tree stopped,", "network locked, waits for go")
    cx = x + GW // 2
    # Clear of the group's label: the label is on the left.
    f.path(f"M{x + GW - 100} 300 L{x + GW - 100} 190")
    f.path(f"M{x + GW - 50} 190 L{x + GW - 50} 300")
    f.path(f"M{cx - 50} 470 L{cx - 50} 400")
    f.path(f"M{cx + 50} 400 L{cx + 50} 470")
f.callout(1, GX[0] + GW - 100, 215)
f.callout(2, GX[0] + GW - 50, 215)
cx = GX[0] + GW // 2
f.callout(3, cx - 50, 436)
f.callout(4, cx + 50, 436)
f.plabel(GX[1] + GW - 106, 220, "reached", anchor="end")
f.plabel(GX[1] + GW - 44, 220, "go or abort", anchor="start")
cx = GX[1] + GW // 2
f.plabel(cx - 56, 440, "dumped", anchor="end")
f.plabel(cx + 56, 440, "dump; resume", anchor="start")

y = f.legend(40, 660, [
    (1, "Each member's agent finishes its member's preparation and holds it at the freeze point. The driver polls "
        "each agent until every member has reached it, for up to 30 minutes."),
    (2, "The driver posts go to every member at once; each must acknowledge it."),
    (3, "Each member's CRIU dumps it and waits in its post-dump hook; the driver polls again until every member is dumped."),
    (4, "The driver posts go again, and every member resumes."),
])
for i, line in enumerate(textwrap.wrap(
        "Why both points: members dumped seconds apart saw the gap as real (an etcd expired a client's lease), and a "
        "member that resumed while another was still being dumped left a TCP connection at different points in the "
        "two images, which never resynchronised after the restore.", 190)):
    f.note(40, y + 4 + 15 * i, line)
f.write(y + 4 + 15 * (i + 1) + 10)
