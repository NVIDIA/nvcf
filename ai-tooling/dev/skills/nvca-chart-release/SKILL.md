---
name: nvca-chart-release
description: Release NVCA Operator chart changes from the native monorepo source to the vendored Helm chart. Use when updating the vendored NVCA Operator chart, changing NVCA image refs, publishing helm-nvca-operator, or validating the chart against a self-managed control plane.
license: Apache-2.0
compatibility: Requires a local checkout of the NVCF monorepo with deploy/helm/nvca-operator/ and src/compute-plane-services/nvca/ present, plus helm and yq.
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, nvca, helm, chart-release, self-managed]
tools: [Read, Grep, Glob, Shell]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags: [nvcf, nvca, helm, chart-release]
  languages: [bash]
  frameworks: [helm]
  domain: cloud-infrastructure
---

# NVCA Operator Chart Release Workflow

Propagates Helm chart changes through the native monorepo paths:

| Component | Path | Purpose |
|-----------|------|---------|
| NVCA source | `src/compute-plane-services/nvca` | Operator and agent source plus source chart at `deployments/nvca-operator/` |
| Vendored chart | `deploy/helm/nvca-operator` | Vendors the source chart, applies self-managed defaults, publishes `helm-nvca-operator` |
| Self-managed stack | `deploy/stacks/self-managed` | Control-plane Helmfile deployment and environment defaults |

## Workflow

1. Make source changes in `src/compute-plane-services/nvca` when operator,
   agent, or source chart behavior changes. Test them there.
2. Vendor from the monorepo source chart at
   `src/compute-plane-services/nvca/deployments/nvca-operator/`.
3. Set the version inputs, either in the environment or in
   `deploy/helm/nvca-operator/.env`:

   ```bash
   NVCA_OPERATOR_VERSION=<operator-image-tag>
   NVCA_VERSION=<agent-image-tag>
   NVCA_SHARED_STORAGE_IMAGE_TAG=<shared-storage-tag>
   ```

4. Vendor and validate from `deploy/helm/nvca-operator`:

   ```bash
   make vendor-chart
   make lint
   make template
   make validate
   ```

5. If the chart is tested against a local self-managed control plane, render
   stack-aware values and install from this chart subtree:

   ```bash
   make render-values-from-stack stack_repo=../../../deploy/stacks/self-managed stack_env=local
   make install-from-stack stack_repo=../../../deploy/stacks/self-managed stack_env=local
   ```

   Use `additional_values=override.yml` for one-off validation. Do not edit the
   stack just to test this chart.

## CI and Release

Umbrella CI is declared in `tools/ci/subproject-validations.yaml` with
subproject id `nvca-operator`. Do not add a chart-local `.gitlab-ci.yml`.

Validate the same chart path CI uses:

```bash
tools/ci/validate-helm-chart deploy/helm/nvca-operator/nvca-operator \
  -f tools/ci/helm-validate-values/nvca-operator.yaml
```

## Local Image Testing

When testing local images in k3d, build them from `src/compute-plane-services/nvca`
and import them into the test cluster. Keep tags explicit and match them in the
chart values:

```bash
export NVCA_OPERATOR_VERSION=dev-local
export NVCA_VERSION=dev-local

# Run from src/compute-plane-services/nvca.
docker build -f docker/Dockerfile.nvca-operator \
  -t nvca-operator:$NVCA_OPERATOR_VERSION .
docker build -f docker/Dockerfile.nvca \
  -t nvca:$NVCA_VERSION .
```

Use the local-dev safety guidance before creating or deleting k3d clusters.

## Gotchas

- `make vendor-chart` overwrites the vendored `nvca-operator/` chart.
- Chart release generation depends on Conventional Commit semantics at the
  umbrella level. Use `feat` or `fix` when a chart release is required.
- Keep `image.*`, `nvcaImage.*`, `ngcConfig.*`, and `selfManaged.*` values in
  sync with the stack and source image tags.
- Never commit service keys, kubeconfigs, rendered secrets, or local registry
  credentials.
