#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-data-tiers.svg: where a checkpoint's bytes live, and the
order in which a restore looks for a copy.

Sources: internal/agent/cascade_fetch.go (EnsureLocal: localCheckpointComplete,
the FSStore, fetchFromL2, then the catalog's peers in least-loaded order,
large files spread across every peer; registerAsPeer), internal/agent/checkpoint.go
(registerAsPeer after a capture, runL2PromoteAsync),
internal/checkpointstore/promoter_snapshot.go and promoter_shared.go,
internal/agent/replication*.go (the opt-in bucket, outside this order).
"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-data-tiers", 1240, 900,
    "Where a Checkpoint Lives and How a Restore Finds It",
    "A capture writes the checkpoint to its node's disk, registers the node as the checkpoint's first source, and "
    "copies it in the background to an rwx volume that is promoted to a read-only rox volume. A restore takes the "
    "first copy it finds: its own disk, a shared filesystem when one is configured, the rox volume, then the "
    "agents that hold it, over HTTP, those it has the fewest fetches in flight from first. Every node that fetches becomes a source.",
    "Numbers 1-4 are the order a restore tries. Dashed: only when configured.",
)

# Capture node.
f.group(40, 100, 360, 330, "GPU node: the capture", "the checkpoint's first source")
f.card("card-cagent", 60, 150, 320, 100, "icon-service", "nvsnap agent", "captures, then copies in the background", nv=True)
f.card("card-cdisk", 60, 290, 320, 110, "icon-hardware", "Node disk (L1)", "the checkpoint, written first")
f.path("M220 250 L220 290")

# Middle: peers and the shared filesystem.
f.card("card-peers", 440, 150, 360, 112, "icon-service", "Agents that hold it (L3)",
       "every source the catalog lists;", "HTTP, large files spread across them", nv=True)
f.card("card-fs", 440, 300, 360, 110, "icon-folder", "Shared filesystem (L2)", "only when configured")

# Restore node.
f.group(840, 100, 360, 330, "GPU node: the restore", "becomes a source once it has a copy")
f.card("card-ragent", 860, 150, 320, 100, "icon-service", "nvsnap agent", "takes the first copy it finds", nv=True)
f.card("card-rdisk", 860, 290, 320, 110, "icon-hardware", "Node disk (L1)", "a copy already here is used as is")

# Cluster storage.
f.group(440, 480, 760, 190, "Cluster storage", "the chart's storage class", kind="partner")
f.card("card-rwx", 460, 530, 240, 112, "icon-data", "rwx-<hash>", "written once, after the capture")
f.card("card-rox", 890, 530, 240, 112, "icon-data", "rox-<hash> (L2)", "read-only, promoted from rwx", "by snapshot or shared attach")

# Capture side.
f.path("M220 400 L220 586 L460 586")
f.plabel(330, 582, "copy in the background")
f.path("M700 586 L890 586")
f.plabel(795, 582, "promote")
f.path("M380 345 L410 345 L410 206 L440 206")
f.plabel(414, 280, "serves", anchor="start")

# Restore order.
f.path("M1020 290 L1020 250")
f.callout(1, 1020, 270)
f.path("M800 355 L860 355", opt=True)
f.callout(2, 830, 355)
f.path("M1010 530 L1010 400")
f.callout(3, 1010, 465)
f.path("M800 206 L830 206 L830 320 L860 320")
f.callout(4, 830, 263)

y = f.legend(40, 720, [
    (1, "A copy on the restoring node's own disk is used as is. 2 Then a shared filesystem, when the cluster has one. "
        "3 Then a copy from the checkpoint's rox volume."),
    (4, "Then the agents the catalog lists as sources, those this agent has the fewest fetches in flight from first; when several hold the checkpoint, its large "
        "files are fetched from all of them at once. A node that fetched registers itself as a source."),
])
f.note(40, y + 4, "Not in this order: an object-store bucket shared between clusters (L4), which opt-in replication fills "
       "and pulls from in the background.")
f.write(y + 24)
