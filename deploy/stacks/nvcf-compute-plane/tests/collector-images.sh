#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "${stack_dir}/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "collector-images: $*" >&2
  exit 1
}

state="${stack_dir}/helmfile.d/02-nvca.yaml.gotmpl"
base="${stack_dir}/environments/base.yaml"
chart_version="$(sed -n '/chart: nvcf\/helm-nvca-operator/{n;s/^[[:space:]]*version: //p;}' "${state}")"
chart_path="deploy/helm/nvca-operator/nvca-operator"
git -C "${repo_root}" archive "deploy/helm/nvca-operator/v${chart_version}" "${chart_path}" |
  tar -x -C "${work_dir}"
chart="${work_dir}/${chart_path}"
cp "${stack_dir}/testdata/registration/ncp-local-register-values.yaml" "${work_dir}/collector-test-register-values.yaml"

byoo_tag="$(yq -er '.global.nvcaOperator.byooOtelCollector.imageTag' "${base}")"
ordinary_tag="$(yq -er '.global.nvcaOperator.selfManaged.otelCollector.imageTag' "${base}")"

render() {
  local name="$1"
  shift
  HELMFILE_ENV=base CLUSTER_NAME=collector-test NCA_ID=collector-test OUTPUT_DIR="${work_dir}" \
    helmfile --file "${state}" --environment default \
    --state-values-set-string global.image.registry=nvcr.io \
    --state-values-set-string global.image.repository=nvidia/nvcf \
    "$@" --selector name=nvca-operator write-values \
    --output-file-template "${work_dir}/${name}-values.yaml" >/dev/null
  helm template nvca-operator "${chart}" --namespace nvca-operator \
    --values "${work_dir}/${name}-values.yaml" >"${work_dir}/${name}.yaml"
}

check_images() {
  local name="$1" byoo="$2" ordinary="$3" enabled="$4"
  local manifest="${work_dir}/${name}.yaml" flag encoded actual
  for flag in --function-env-overrides-b64 --task-env-overrides-b64; do
    encoded="$(FLAG="${flag}" yq -r 'select(.kind == "Deployment") |
      .spec.template.spec.containers[] | select(.name == "nvca-operator") |
      .args | .[to_entries | map(select(.value == strenv(FLAG))) | .[0].key + 1]' "${manifest}")"
    actual="$(printf '%s' "${encoded}" | base64 --decode | yq -r '.BYOO_OTEL_COLLECTOR_CONTAINER')"
    test "${actual}" = "${byoo}" || fail "${name}: ${flag} selected ${actual}, expected ${byoo}"
  done
  yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[] |
    select(.name == "nvca-operator") | .env | map({"key": .name, "value": .value}) | from_entries' \
    "${manifest}" >"${work_dir}/operator-env.yaml"
  actual="$(yq -r '.OTEL_COLLECTOR_IMAGE_REPO + ":" + .OTEL_COLLECTOR_IMAGE_TAG' "${work_dir}/operator-env.yaml")"
  test "${actual}" = "${ordinary}" || fail "${name}: operator collector selected ${actual}"
  test "$(yq -r '.OTEL_COLLECTOR_ENABLED' "${work_dir}/operator-env.yaml")" = "${enabled}" ||
    fail "${name}: operator collector enablement changed"
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") |
    .data."cluster-dto.yaml" | from_yaml | .otelCollector' "${manifest}" >"${work_dir}/backend-collector.yaml"
  actual="$(yq -r '.imageConfig.repository + ":" + .imageConfig.tag' "${work_dir}/backend-collector.yaml")"
  test "${actual}" = "${ordinary}" || fail "${name}: backend collector selected ${actual}"
  test "$(yq -r '.enabled' "${work_dir}/backend-collector.yaml")" = "${enabled}" ||
    fail "${name}: backend collector enablement changed"
  test "$(yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-helm-managed") | .data | length' "${manifest}")" = 0 ||
    fail "${name}: self-managed stack unexpectedly instantiated the helm-managed backend"
}

render default
check_images default "nvcr.io/nvidia/nvcf/byoo-otel-collector:${byoo_tag}" \
  "nvcr.io/nvidia/nvcf/nvcf-otel-collector:${ordinary_tag}" false

inventory_override="$(yq -er '.states[] | select(.path == "deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl") | .fullOverrides[] | select(. == "global.nvcaOperator.selfManaged.otelCollector.enabled=true")' "${stack_dir}/release-inventory.yaml")"
render inventory --state-values-set "${inventory_override}"
check_images inventory "nvcr.io/nvidia/nvcf/byoo-otel-collector:${byoo_tag}" \
  "nvcr.io/nvidia/nvcf/nvcf-otel-collector:${ordinary_tag}" true

render mirror --state-values-set-string global.image.registry=mirror.example.invalid \
  --state-values-set-string global.image.repository=team/nvcf
check_images mirror "mirror.example.invalid/team/nvcf/byoo-otel-collector:${byoo_tag}" \
  "mirror.example.invalid/team/nvcf/nvcf-otel-collector:${ordinary_tag}" false

render override \
  --state-values-set-string global.nvcaOperator.byooOtelCollector.imageRepository=mirror.example.invalid/custom/byoo \
  --state-values-set-string global.nvcaOperator.byooOtelCollector.imageTag=byoo-fixture \
  --state-values-set-string global.nvcaOperator.selfManaged.otelCollector.imageRepository=mirror.example.invalid/custom/ordinary \
  --state-values-set-string global.nvcaOperator.selfManaged.otelCollector.imageTag=ordinary-fixture
check_images override mirror.example.invalid/custom/byoo:byoo-fixture \
  mirror.example.invalid/custom/ordinary:ordinary-fixture false

echo "collector-images: all checks passed with released chart ${chart_version}"
