#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""docs/figures/nvsnap-model-volume.svg: one download per model per cluster, read
by every other pod through a read-only view (block storage).

Sources: internal/modelvolume/modelvolume.go (the package comment; Mode,
ReaderMode, DownloadOwnerName, ClaimName, ReadOnlyClaimName,
ReadOnlyPVName), internal/agent/modelvolume_controller.go (staging pods:
measure, claim, copy, label complete, release; pending readers: mint the
read-only claim, bind it onto the hostPath landing), internal/webhook/model_volume.go
(modelVolumePatches, readerLanding, placeholderModelPatches), internal/modelid.
"""
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig  # noqa: E402

f = Fig(
    "nvsnap-model-volume", 1240, 900,
    "Model Volumes: One Download per Model, Read-Only Everywhere",
    "At admission the webhook decides which model a pod reads. A Lease names the one namespace whose Job downloads "
    "it. On block storage the Job downloads into a pod-local directory; the agent on its node measures the tree, "
    "creates a primary volume of that size in the nvsnap namespace, copies the tree in, and marks the volume "
    "complete. Every pod that reads the model gets a read-only view in its own namespace, landed at the path its "
    "engine already reads.",
    "Block storage shown. On a filesystem shared while written, the Job writes straight into the primary through a "
    "read-write view, and readers mount at admission.",
)

f.card("card-webhook", 40, 150, 220, 112, "icon-admit", "nvsnap webhook", "decides the model at admission", nv=True)
f.card("card-lease", 40, 320, 220, 112, "icon-guard", "Download owner (Lease)", "one namespace per model", "downloads it")
f.path("M150 262 L150 320")
f.callout(1, 150, 291)

f.group(300, 100, 280, 360, "The owner's namespace", "the first pod that needs the model")
f.card("card-job", 320, 150, 240, 112, "icon-run", "Download Job", "into a pod-local directory")
f.card("card-agent", 320, 320, 240, 112, "icon-service", "nvsnap agent", "on the Job's node: measures,", "creates, copies in", nv=True)
f.path("M260 376 L280 376 L280 206 L320 206")
f.callout(2, 280, 291)
f.path("M440 262 L440 320")

f.group(620, 100, 260, 360, "nvsnap namespace", "one per cluster", kind="nv")
f.card("card-primary", 640, 320, 220, 112, "icon-data", "Primary volume", "sized from what landed;", "retained, labelled complete")
f.path("M560 376 L640 376")
f.callout(3, 600, 376)

f.group(920, 100, 280, 360, "Each reader namespace", "any function that reads the model")
f.card("card-view", 940, 150, 240, 112, "icon-data", "Read-only view", "a static PV over the same volume,", "pre-bound to a -ro claim")
f.card("card-reader", 940, 320, 240, 112, "icon-compute", "Pod that reads the model", "landed at the path", "its engine reads")
f.path("M750 320 L750 206 L940 206")
f.callout(4, 750, 263)
f.path("M1060 262 L1060 320")
f.callout(5, 1060, 291)

y = f.legend(40, 510, [
    (1, "The webhook decides the model from the pod (internal/modelid). The first admission for a model claims the "
        "download-owner Lease; a create only one admission can win. 2 The owner's Job downloads the model."),
    (3, "The agent on the Job's node measures the tree, creates a claim of that size in the nvsnap namespace, copies "
        "the tree in, labels the retained volume complete and releases the claim, so the volume detaches. After "
        "repeated failures it records the failure, and new pods download for themselves."),
    (4, "Once the volume is complete, a read-only view is minted in each reader's namespace. 5 In PVC mode the pod "
        "references the read-only claim; in hostPath mode it is scheduled at once on a hostPath landing and the agent "
        "binds the volume in. Either way it lands at the path the engine already reads."),
])
f.note(40, y + 4, "Under CRIU capture a restore placeholder gets only the landing, so CRIU can bind the mount the "
       "captured pod had.")
f.write(y + 24)
