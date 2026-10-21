<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# deploy/

nvsnap is installed with the Helm chart in [helm/nvsnap](helm/nvsnap/); see
[docs/operate/INSTALL.md](../docs/operate/INSTALL.md).

| Path | Holds |
|---|---|
| [helm/nvsnap](helm/nvsnap/) | The chart |
| [k8s/](k8s/) | Plain manifests, cluster-preparation manifests and sample workloads |
| [crds/](crds/) | Custom resource definitions |
| [tests/](tests/) | Deployment tests |
| `nvsnap-installer`, `setup-node.sh`, `install*.sh`, `systemd/`, `config/`, `nfs/` | The node installer that predates the chart; not maintained, and its guide is removed (git history keeps it) |
