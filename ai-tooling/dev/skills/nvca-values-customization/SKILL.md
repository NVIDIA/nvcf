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

```text
src/compute-plane-services/nvca/deployments/nvca-operator/   source chart
  -> scripts/ci_vendor_nvca_operator_chart                   applies self-managed defaults
  -> nvca-operator/values.yaml                               vendored chart values
  -> scripts/render_values_from_stack_env.sh                 stack-aware generated values
  -> make install or make install-from-stack                 optional additional overrides
```

## Permanent Defaults

For defaults that every self-managed deployment should receive, edit
`scripts/ci_vendor_nvca_operator_chart` and re-vendor:

```bash
make vendor-chart
git diff nvca-operator/values.yaml
```

The vendoring script already applies defaults such as:

- `ngcConfig.clusterSource = "self-managed"`
- `ngcConfig.serviceKey = "dummy-api-key"`
- `image.tag = "$NVCA_OPERATOR_VERSION"`
- `selfManaged.nvcaVersion = "$NVCA_VERSION"`
- `generateImagePullSecret = false`
- `selfManaged.sharedStorage.imageTag = "$NVCA_SHARED_STORAGE_IMAGE_TAG"`
- `nameOverride = "nvca-operator"`
- `fullnameOverride = "nvca-operator"`

Do not edit `nvca-operator/values.yaml` directly for a permanent default. The
next vendor run will overwrite it.

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

## Adding .env Inputs

For version-like values that the vendoring script needs, add a variable to
`.env`, require it in `scripts/ci_vendor_nvca_operator_chart`, and re-vendor:

```bash
MY_NEW_CONFIG=some-value
```

```bash
update_yaml_key ".myConfig = \"${MY_NEW_CONFIG:?MY_NEW_CONFIG is not set}\"" "${TARGET_DIR}/values.yaml"
```

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
- Use `yq` carefully for nested keys and quoted strings.
- Keep `Chart.yaml` name/version changes in the vendoring script when they are
  part of the self-managed packaging contract.
- Never commit real service keys or rendered secret material.
