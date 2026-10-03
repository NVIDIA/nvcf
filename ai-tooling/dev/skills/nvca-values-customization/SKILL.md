---
name: nvca-values-customization
description: Customize NVCA Operator Helm chart values in the native monorepo. Use when modifying vendored defaults, changing stack-derived install values, adding deployment-time overrides, or updating scripts under deploy/helm/nvca-operator.
license: Apache-2.0
compatibility: Requires a local checkout of the NVCF monorepo with deploy/helm/nvca-operator/ present, plus helm and yq.
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, nvca, helm, values, customization]
tools: [Read, Grep]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags: [nvcf, nvca, helm, values]
  languages: [bash, yaml]
  frameworks: [helm]
  domain: cloud-infrastructure
---

# Customizing NVCA Operator Chart Values

Use this skill from `deploy/helm/nvca-operator`.

## Values Flow

Two install paths, and only one of them renders values from a stack.

```text
nvca-operator/values.yaml                        the chart's own defaults
  -> make install values=<path>                  the values file, used directly

stack environment
  -> scripts/render_values_from_stack_env.sh     stack-aware generated values
  -> make install-from-stack                     the generated values
```

Either accepts `additional_values=<path>` for further overrides.

## Permanent Defaults

Edit `nvca-operator/values.yaml` directly. There is one chart and no vendoring
step, so that file is the source of truth.

Only defaults that suit every consumer belong there. Values tied to one
deployment are supplied by whoever installs the chart:

- the compute-plane stack sets them under
  `deploy/stacks/nvcf-compute-plane/`, including `nameOverride`,
  `fullnameOverride` and `selfManaged.nvcaVersion`
- an ngc-managed install passes `ngcConfig.serviceKey` and the `helmManaged.*`
  values on the command line

`image.tag` ships empty so templates fall back to `appVersion`, which the
release stamps at packaging time.

## Deploy-time Overrides

Use `additional_values` for one-off validation or environment-specific values:

```bash
make install-from-stack \
  stack_repo=../../../deploy/stacks/self-managed \
  stack_env=local \
  additional_values=override.yml
```

Use deploy-time overrides for secrets, credentials, cluster-specific IDs, and
temporary validation changes.

## Validation

```bash
make lint
make template
make validate
tools/ci/validate-helm-chart deploy/helm/nvca-operator/nvca-operator \
  -f tools/ci/helm-validate-values/nvca-operator.yaml
```

## Gotchas

- Install-time values are layered after generated stack-aware values.
- Use `clusterValidator.role: control-plane` only in a single-cluster
  topology, where the NVCF control plane runs in the same cluster. On a
  compute-only cluster every run is Not-Ready.
- With `clusterValidator.role: control-plane`, set
  `clusterValidator.gatewayNames` to every stack Gateway as `namespace/name`.
  Unset, the validator finds Gateways from route labels, and those can never
  fail Tier-1. `make render-values-from-stack` fills it in.
- List stack quorum components that run outside the cluster (`nats`,
  `openbao`, `cassandra`) in `clusterValidator.externalComponents`, or Tier-2
  reports them missing after install. `make render-values-from-stack` lists
  each one the stack environment disables.
- Use `yq` carefully for nested keys and quoted strings.
- `Chart.yaml` name stays in git and must match the subproject's service_name;
  the release refuses to publish when they differ. Only the version is set at
  packaging time, and `appVersion` is stamped from the nvca release the chart
  installs.
- Never commit real service keys or rendered secret material.
