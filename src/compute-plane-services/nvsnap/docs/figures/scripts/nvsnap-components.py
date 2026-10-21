#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-components.svg: what an nvsnap deployment runs on one cluster
and what talks to what.

Sources: deploy/helm/nvsnap/templates (agent DaemonSet, webhook registration,
server Deployment), cmd/agent and internal/agent (capture, restore, the agent
HTTP API, EnsureLocal and registerAsPeer in cascade_fetch.go, runL2PromoteAsync
in checkpoint.go, replication*.go), internal/webhook (admission, bundle_mount.go),
cmd/nvsnap-server, internal/server and internal/db (the catalog),
internal/checkpointstore (the L2 volume pair), internal/modelvolume.
"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-components", 1240, 900,
    "nvsnap on One Cluster",
    "NVCA or another controller creates GPU pods; the Kubernetes API sends each to the nvsnap webhook, which "
    "the agent on every GPU node serves. The agent captures and restores pods on its node, keeps checkpoints on "
    "the node's disk, registers them in the server's catalog, copies them to an L2 volume pair in cluster storage, "
    "and serves them to other nodes' agents. Model volumes hold one copy of each model for the cluster. An "
    "object-store bucket shared between clusters is optional.",
    "One agent per GPU node; one server per cluster. Dashed: optional.",
)

# Callers.
f.card("card-nvca", 40, 130, 190, 112, "icon-actor", "NVCA or a controller", "creates GPU pods")
f.card("card-api", 40, 330, 190, 118, "icon-third", "Kubernetes API", "pod CREATE, scheduling")
f.path("M135 242 L135 330")
f.callout(1, 135, 286)

# One GPU node.
f.group(280, 100, 580, 400, "GPU node", "one of many")
f.card("card-pods", 300, 150, 240, 118, "icon-compute", "Workload pods", "GPU engines and companions")
f.card("card-bundle", 600, 150, 240, 118, "icon-folder", "Node bundle at /nvsnap",
       "CRIU, gpushare, helper;", "staged by the agent")
f.card("card-agent", 300, 330, 240, 118, "icon-service", "nvsnap agent (DaemonSet)",
       "webhook, capture, restore,", "agent HTTP API", nv=True)
f.card("card-disk", 600, 330, 240, 118, "icon-hardware", "Node's checkpoint disk",
       "L1: checkpoints, chunk stores", nv=False)
f.path("M230 389 L300 389")
f.callout(2, 265, 389)
f.path("M420 330 L420 268")
f.callout(3, 420, 299)
f.path("M600 209 L540 209")
f.callout(4, 570, 209)
f.path("M540 389 L600 389")

# Other nodes and the object store.
f.group(900, 100, 300, 200, "Other GPU nodes", "each runs its own agent")
f.card("card-peer", 920, 150, 260, 118, "icon-service", "nvsnap agents", "fetch and serve checkpoints", nv=True)
f.card("card-bucket", 920, 360, 260, 118, "icon-third", "Object-store bucket", "L4: shared between clusters",
       "opt-in replication")
f.path("M840 350 L880 350 L880 209 L920 209")
f.callout(7, 880, 280)
f.path("M840 419 L920 419", opt=True)
f.callout(8, 880, 419)

# Server.
f.group(40, 540, 440, 172, "nvsnap server (Deployment)", "one per cluster", kind="nv")
f.card("card-server", 60, 590, 190, 112, "icon-service", "Server", "REST API, web UI", nv=True)
f.card("card-catalog", 270, 590, 190, 112, "icon-db", "Catalog (SQLite)", "checkpoints, sources,", "retention")
f.path("M365 448 L365 590")
f.callout(5, 365, 520)

# Cluster storage.
f.group(520, 540, 460, 172, "Cluster storage", "the chart's storage class", kind="partner")
f.card("card-l2", 540, 590, 200, 112, "icon-data", "L2 volume pair", "rwx-<hash> written once,", "rox-<hash> read by all")
f.card("card-model", 760, 590, 200, 112, "icon-data", "Model volumes", "one download per model;", "read-only views in pods")
f.path("M700 448 L700 590")
f.callout(6, 700, 520)

y = f.legend(40, 760, [
    (1, "NVCA deploys an NVCF function's pods; nvsnap does not author their specs. 2 The webhook prepares each pod "
        "it covers: the node bundle, gpushare, the model landing, or a restore placeholder."),
    (3, "The agent captures a warm pod, or restores a placeholder, on its node; 4 the bundle it staged is mounted "
        "read-only in the pods it prepares."),
    (5, "Every checkpoint is registered in the catalog with its sources; its capture record carries its tenant scope. 6 After a capture the "
        "checkpoint is copied in the background to the L2 pair. 7 Agents fetch checkpoints from each other over HTTP, "
        "and every node that fetches becomes a source. 8 Cross-cluster replication through a bucket is opt-in."),
])
f.write(y + 14)
