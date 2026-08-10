---
name: nvcf-self-managed-prerequisite
description: >-
  Install the prerequisites the NVCA operator / compute plane needs before
  nvcf-nvca-install can succeed: the operator tool nvcf-cli (required by the
  compute-plane stack's register-cluster step), KAI Scheduler (for the
  KAIScheduler feature gate), and the SMB CSI driver (for the sharedStorage
  Samba sidecar PVCs). The two cluster components are cloud-neutral helm
  installs at the NVCF-validated version pins; same install on AKS, EKS, GKE,
  k3d, or bare metal. Use when the user mentions NVCA prereqs, nvcf-cli, "nvcf-cli
  not found", ensure-nvcf-cli, register-cluster, KAI Scheduler, SMB CSI,
  csi-driver-smb, queue quotas, default-parent-queue, NVCA shared-storage PVCs
  stuck Pending, or asks how to prepare a cluster before installing the NVCA
  operator.
license: Apache-2.0
compatibility: Requires helm >= 3.12, < 4 (Helm 4 not supported), kubectl matching cluster version.
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

One operator tool plus two cluster-level components the NVCA operator / compute plane depends on. Satisfy all three before running `nvcf-nvca-install`.

| Prereq | Why it is needed | Detail |
| ------ | ------------------ | ------ |
| `nvcf-cli` | The compute-plane stack's `make register-cluster` (and `install`/`apply`/`sync`, which abort without the registration values it writes) shells out to `nvcf-cli`. The shipped stack defaults to building it from a sibling `../cli` checkout that the release does not include, so a green-field repo fails with `ensure-nvcf-cli` / "Registration values not found". | See Step 0b below |
| KAI Scheduler | `selfManaged.featureGateValues` includes `KAIScheduler`; NVCA polls `Queue` CRs and refuses to become healthy until their quotas are `-1`. Skip the standalone helm step below when `addons.kaiScheduler.enabled` is set in `nvcf-compute-plane` (required when enabling Grove or Dynamo). | [references/kai-scheduler.md](references/kai-scheduler.md) |
| SMB CSI driver (`smb.csi.k8s.io`) | NVCA's `selfManaged.sharedStorage` runs Samba sidecar pods that export file shares; the resulting PVCs need this CSI driver to bind | [references/smb-csi.md](references/smb-csi.md) |

The KAI Scheduler and SMB CSI installs are cloud-neutral helm commands pinned to NVCF-validated versions. These are upstream third-party charts (not NVCF images), so they are not in `manifest.yaml`; the per-component reference docs carry the current pin and link the NVCF docs version table. `nvcf-cli` is an operator workstation tool, not an in-cluster install.

## Prerequisites

- A running Kubernetes cluster (any cloud — AKS, EKS, GKE, k3d, MicroK8s) with `kubectl` configured and admin access.
- `helm` **>= 3.12 and < 4**. **Helm 4 is NOT supported** (matches `nvcf-self-managed-stack/README.md`). On Helm 4 the KAI install below hangs silently for many minutes — Helm 4 runs the chart's pre-install `crd-manager` hook through a `before-hook-creation` delete and then waits `--timeout` *per already-absent hook resource*, so the release sits in `pending-install` with no pods and never errors cleanly. Use Helm 3.x.
- Cluster has CPU headroom on a general-purpose node pool for KAI's 7 pods.

## Install

### 0 — Preflight: verify Helm 3.x (fail fast on Helm 4)

Helm 4 is not supported and causes a silent multi-minute hang on the KAI install below. Check the major version before installing anything:

```bash
helm_major="$(helm version --template '{{.Version}}' | sed -E 's/^v?([0-9]+).*/\1/')"
if [ "$helm_major" != "3" ]; then
  echo "ERROR: Helm $helm_major detected; this prerequisite requires Helm 3.x (>= 3.12, < 4)." >&2
  echo "Helm 4 hangs on the KAI Scheduler chart's crd-manager hook. Install a 3.x release and retry." >&2
  exit 1
fi
```

### 0b — nvcf-cli (compute-plane registration tool)

The compute-plane stack (`nvcf-compute-plane-stack`) registers each GPU cluster with the control plane via `make register-cluster`, which shells out to `nvcf-cli` (`init` + `cluster register`) and writes `registration/<cluster>-register-values.yaml`. The stack's `install`/`apply`/`sync` targets abort if that file is missing. By default the stack builds `nvcf-cli` from a sibling `../cli` checkout (`NVCF_CLI_REPO ?= $(MAKEFILE_DIR)/../cli`) that is not bundled with the release, so a fresh checkout fails at `ensure-nvcf-cli`.

`nvcf-cli` is a convenience wrapper: its only job in this flow is to produce `registration/<cluster>-register-values.yaml` (the `clusterID` / `selfManaged.clusterId`/`clusterGroupId` schema the NVCA helmfile loads). There are two ways to satisfy this prerequisite.

Register without `nvcf-cli` (supported path for self-hosted deployments). Obtain the cluster registration data from the running control plane and hand-author `registration/<cluster>-register-values.yaml` in the schema the NVCA helmfile expects, then run `make install CLUSTER_NAME=<name> HELMFILE_ENV=<env>` directly (no `register-cluster`). The step-by-step procedure for gathering that data without the CLI is being published by the NVCF team; until it lands, use an `nvcf-cli` build if NVIDIA has provided you one.

Use an `nvcf-cli` binary if you have one. Point the stack at the binary with an absolute path so it does not try to build the missing `../cli`:

```bash
cd nvcf-compute-plane-stack
make register-cluster \
  CLUSTER_NAME=<name> NCA_ID=<nca> CLUSTER_REGION=<region> \
  ICMS_URL=https://sis.<your-domain> \
  NVCF_CLI=/abs/path/to/nvcf-cli
make install CLUSTER_NAME=<name> HELMFILE_ENV=<env>
```

### 1 — KAI Scheduler

Skip this step when `addons.kaiScheduler.enabled` is true in `nvcf-compute-plane`. That stack installs KAI Scheduler (release and namespace `kai-scheduler`). Enable that flag whenever Grove or Dynamo is enabled. Use the standalone install below when you need KAI without the compute-plane add-on (for example the NVCA `KAIScheduler` feature gate alone).

```bash
cat > nvca-values.yaml << 'EOF'
scheduler:
  placementStrategy: binpack
  plugins:
    nodeplacement:
      arguments:
        gpu: binpack
        cpu: spread
  actions:
    preempt:
      enabled: false
    consolidation:
      enabled: false

defaultQueue:
  createDefaultQueue: true
  parentName: default-parent-queue
  childName: default-queue
  parentResources:
    cpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    gpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    memory:
      quota: -1
      limit: -1
      overQuotaWeight: 1
  childResources:
    cpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    gpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    memory:
      quota: -1
      limit: -1
      overQuotaWeight: 1
EOF


helm install kai-scheduler \
  oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler \
  -n kai-scheduler --create-namespace -f nvca-values.yaml \
  --version v0.14.0 \
  --wait --timeout 5m
```

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

- Compute-plane registration is satisfiable: either you can produce `registration/<cluster>-register-values.yaml` without the CLI, or an `nvcf-cli` binary is available (on `PATH` or via `NVCF_CLI=<abs-path>`).
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
