---
name: nvcf-self-managed-prerequisite
description: >-
  Install the cluster-level prerequisites the NVCA operator needs before
  nvcf-nvca-install can succeed — KAI Scheduler (for the KAIScheduler feature
  gate) and the SMB CSI driver (for the sharedStorage Samba sidecar PVCs).
  Both are cloud-neutral helm installs at the NVCF-validated version pins;
  same install on AKS, EKS, GKE, k3d, or bare metal. Use when the user
  mentions NVCA prereqs, KAI Scheduler, SMB CSI, csi-driver-smb, queue
  quotas, default-parent-queue, NVCA shared-storage PVCs stuck Pending, or
  asks how to prepare a cluster before installing the NVCA operator.
license: Apache-2.0
compatibility: Requires helm >= 3.12, kubectl matching cluster version.
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, self-managed, self-hosted, kai-scheduler, smb-csi, csi-driver-smb, prereq, nvca-prereq, shared-storage]
tools: [Shell, Read, Edit, Grep, Glob]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags: [nvcf, self-managed, self-hosted, kai-scheduler, smb-csi, csi-driver-smb, prereq, nvca-prereq, shared-storage]
  languages: [bash, yaml]
  frameworks: [helm, kubectl]
  domain: cloud-infrastructure
---

# NVCA prerequisites — KAI Scheduler + SMB CSI

Two cluster-level components the NVCA operator depends on. Install both before running `nvcf-nvca-install`.

| Prereq | Why NVCA needs it | Detail |
| ------ | ------------------ | ------ |
| KAI Scheduler | `selfManaged.featureGateValues` includes `KAIScheduler`; NVCA polls `Queue` CRs and refuses to become healthy until their quotas are `-1` | [references/kai-scheduler.md](references/kai-scheduler.md) |
| SMB CSI driver (`smb.csi.k8s.io`) | NVCA's `selfManaged.sharedStorage` runs Samba sidecar pods that export file shares; the resulting PVCs need this CSI driver to bind | [references/smb-csi.md](references/smb-csi.md) |

Both installs are cloud-neutral helm commands pinned to the NVCF-validated versions — see `manifest.yaml` for current pins.

## Prerequisites

- A running Kubernetes cluster (any cloud — AKS, EKS, GKE, k3d, MicroK8s) with `kubectl` configured and admin access.
- `helm` 3.12+.
- Cluster has CPU headroom on a general-purpose node pool for KAI's 7 pods.

## Install

### 1 — KAI Scheduler

```bash
helm upgrade -i kai-scheduler \
  oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler \
  -n kai-scheduler --create-namespace \
  --version v0.12.6 \
  --wait --timeout 5m

# Required quota patch — default queues ship with quota=0; NVCA needs -1.
for q in default-parent-queue default-queue; do
  kubectl patch queue "$q" --type=merge -p '{
    "spec":{"resources":{
      "cpu":{"limit":-1,"quota":-1,"overQuotaWeight":1},
      "gpu":{"limit":-1,"quota":-1,"overQuotaWeight":1},
      "memory":{"limit":-1,"quota":-1,"overQuotaWeight":1}
    }}
  }'
done
```

If you skip the patch, NVCA later spams `kai-scheduler-queues_errors="[CPU resource violation … quota=0]"`. Full failure modes + version-pin guidance: [references/kai-scheduler.md](references/kai-scheduler.md).

### 2 — SMB CSI driver

```bash
helm repo add csi-driver-smb \
  https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts
helm repo update
helm install csi-driver-smb csi-driver-smb/csi-driver-smb \
  -n kube-system \
  --version v1.17.0 \
  --wait --timeout 5m
```

AKS clusters can use the managed `csi-driver-smb` add-on instead — see [references/smb-csi.md](references/smb-csi.md).

## Definition of done

- `kubectl get pods -n kai-scheduler` shows 7 pods Running.
- `kubectl get queues` shows both `default-parent-queue` and `default-queue` with `limit: -1, quota: -1` on cpu / gpu / memory.
- `kubectl get csidriver smb.csi.k8s.io` returns the driver without error.

After this, run `nvcf-nvca-install`.

## Uninstall

```bash
helm uninstall csi-driver-smb -n kube-system
helm uninstall kai-scheduler -n kai-scheduler
kubectl delete namespace kai-scheduler
```

## References

- [references/kai-scheduler.md](references/kai-scheduler.md) — KAI install detail, queue-quota theory, failure modes
- [references/smb-csi.md](references/smb-csi.md) — SMB CSI install detail, AKS managed-add-on alternative, verification
- Companion skill: `nvcf-self-managed-installation` — Section 7 covers enabling and validating the NVCA operator after these prerequisites are satisfied.
