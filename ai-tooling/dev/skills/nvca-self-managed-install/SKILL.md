---
name: nvca-self-managed-install
description: Install or validate the NVCA Operator chart against a self-managed NVCF control plane from the native monorepo. Use when the control plane comes from deploy/stacks/self-managed and NVCA must be installed with stack-derived image repository settings.
license: Apache-2.0
compatibility: Requires a local checkout of the NVCF monorepo with deploy/helm/nvca-operator/ and deploy/stacks/self-managed/ present, plus helm.
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, nvca, helm, install, self-managed]
tools: [Read, Shell]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags: [nvcf, nvca, helm, install]
  languages: [bash]
  frameworks: [helm]
  domain: cloud-infrastructure
---

# NVCA Self-Managed Install

Use `deploy/helm/nvca-operator` as the install surface for NVCA Operator chart
validation. Do not add NVCA back into the stack Helmfile just to test this
chart.

## Source of Truth

- Control-plane deployment: `deploy/stacks/self-managed`
- NVCA chart and install workflow: `deploy/helm/nvca-operator`
- NVCA source images and source chart: `src/compute-plane-services/nvca`
- Cluster prerequisites: use the public `nvcf-self-managed-prerequisite` skill
- Full stack install and teardown: use the public `nvcf-self-managed-installation` skill

## Preferred Workflow

Run from `deploy/helm/nvca-operator`:

```bash
make render-values-from-stack \
  stack_env=local \
  stack_repo=../../../deploy/stacks/self-managed
```

Inspect the generated file under `bin/`, then install or upgrade:

```bash
make install-from-stack \
  stack_env=local \
  stack_repo=../../../deploy/stacks/self-managed
```

## Optional Inputs

- `additional_values=override.yml` for one-off overrides
- `NCA_ID=<value>` to set the primary account ID explicitly
- `CLUSTER_NAME=<value>` and `CLUSTER_ID=<value>` to preserve or pin cluster identity
- `IMAGE_PULL_SECRET_NAME=<secret>` to attach a pre-created image pull secret

## Existing Release Reuse

If `nvca-operator` is already installed in the `nvca-operator` namespace, the
render helper reuses current release values first, then overlays repository
paths derived from the stack environment file. Use this to preserve cluster IDs
and operator settings during upgrades.

## Safety

Before creating, deleting, or reusing local k3d clusters, read the local-dev
safety guidance in `.cursor/skills/nvcf-self-hosted-local-dev/SKILL.md` from
the repo root. Do not delete clusters or Helm releases without explicit user
approval.
