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

"""docs/figures/nvstor-gpu-rails.svg: NFS over RDMA on the GPU nodes' rails (deploy/nfs-rdma/).

Source of truth: deploy/nfs-rdma/{source-pod.yaml,replica-pod.yaml,rails-mount.sh,README.md}, docs/nfs-rdma.md.
"""
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from figlib import Fig

f = Fig(
    "nvstor-gpu-rails", 1220, 900,
    "Loading a Model over the GPU Rails with NFS over RDMA",
    "A source pod holding the four rail VFs serves a local model directory read-only with nvstor-nfs-export, "
    "RDMA only. On a new replica node a privileged rails-mount sidecar pins one route per source rail address, "
    "mounts the export once with session trunking so each rail carries one transport, and shares the mount with "
    "the unprivileged vLLM container. Model bytes flow from the source NVMe through the export, across all four "
    "rails, into the kernel NFS client and the workload.",
    "GPU nodes whose RDMA NICs are SR-IOV VFs, four rails per node in this example. Numbers 1-4 follow the model bytes.",
)

# Source
f.group(40, 110, 420, 420, "Source node", "nvstor-source pod (source-pod.yaml)", kind="nv")
f.card("s-dir", 70, 170, 160, 112, "icon-folder", "Model directory", "local NVMe, read-only")
f.card("s-export", 270, 170, 170, 112, "icon-service", "nvstor-nfs-export", "[::]:20049, --no-tcp", "--allow-client, --fh-key", nv=True)
f.card("s-vf", 270, 380, 170, 112, "icon-hardware", "Rail VFs net1..net4", "IPv6 routable GIDs", "--trunk-addr per rail")

# Replica
f.group(700, 110, 480, 420, "Replica node", "vllm-replica pod (replica-pod.yaml)", kind="nv")
f.card("r-side", 720, 170, 190, 112, "icon-guard", "rails-mount sidecar", "privileged; /128 route per rail", "trunkdiscovery, max_connect=4", nv=True)
f.card("r-vllm", 970, 170, 190, 112, "icon-compute", "vLLM", "unprivileged, no capabilities", "reads /nvstor/<uid>/data")
f.card("r-vf", 720, 380, 190, 112, "icon-hardware", "Rail VFs net1..net4", "one per plane")
f.card("r-nfs", 970, 380, 190, 112, "icon-third", "Kernel NFS client", "proto=rdma6, ro, soft", "one transport per rail")

# Data path
f.path("M230 226 L270 226")                                  # dir -> export
f.path("M355 282 L355 380"); f.callout(1, 355, 331)        # export -> VFs
for i, y in enumerate([400, 422, 444, 466]):
    f.line(f"M440 {y} L720 {y}")
f.note(580, 384, "four rails, RoCE v2", "note-b", anchor="middle")
f.callout(2, 520, 400)
f.note(580, 486, "one RC transport per rail", "note", anchor="middle")
f.path("M910 436 L970 436"); f.callout(3, 935, 436)
f.path("M1065 380 L1065 282"); f.callout(4, 1065, 331)    # kernel client -> vLLM
f.path("M910 226 L970 226", opt=True)                         # sidecar shares the mount
f.path("M815 282 L815 380", opt=True)                         # sidecar pins routes on the VFs

y = f.legend(40, 572, [
    (1, "nvstor-nfs-export serves the directory over NFS/RDMA only, to the replicas' rail addresses (--allow-client), with sealed filehandles that survive a restart (--fh-key)."),
    (2, "Session trunking: the source advertises its four rail addresses as fs_locations (--trunk-addr) and the client mounts once with trunkdiscovery (Linux 5.18+), one transport per rail. The sidecar pins a /128 route per source address to the client VF on the same plane; without that, every transport leaves through one VF."),
    (3, "The rails are reachable only from pods holding the rail VFs, so the kernel client mounts from the sidecar's network namespace, not the node's."),
    (4, "vLLM starts only after the sidecar's startup probe sees every rail carrying a transport, and reads the mount read-only through a hostPath (HostToContainer). Dashed: the sidecar's mount and routes."),
])
f.write(y + 10)
