#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "$0")/.." && pwd)"
workspace_root="$(cd "${chart_root}/../../.." && pwd)"
collector_root="${workspace_root}/src/compute-plane-services/byoo-otel-collector"
config_template="${workspace_root}/src/compute-plane-services/nvca/pkg/operator/reconcile/manifests/otel_collector_config.yaml"
compatible_tag="0.157.0-nv-0.2.1"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

manifest="${tmp_dir}/manifest.yaml"
config="${tmp_dir}/config.yaml"
collector="${NVCF_OTEL_COLLECTOR_BINARY:-${tmp_dir}/otelcol-contrib}"
token_file="${tmp_dir}/service-api-key"
build_parallelism="${GO_BUILD_P:-4}"
go_build_cache="${NVCF_OTEL_COMPAT_GOCACHE:-${TMPDIR:-/tmp}/nvcf-otel-compat-go-build-cache}"

helm template nvca-operator "${chart_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${chart_root}/nvca-operator/values.yaml" \
  --values "${chart_root}/values.release-sbom.yaml" \
  --set-string ngcConfig.clusterSource=self-managed \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set otelCollector.enabled=true \
  --set selfManaged.otelCollector.enabled=true \
  > "${manifest}"

operator_repository="$(
  yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
    .spec.template.spec.containers[] | select(.name == "nvca-operator") |
    .env[] | select(.name == "OTEL_COLLECTOR_IMAGE_REPO") | .value' "${manifest}"
)"
operator_tag="$(
  yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
    .spec.template.spec.containers[] | select(.name == "nvca-operator") |
    .env[] | select(.name == "OTEL_COLLECTOR_IMAGE_TAG") | .value' "${manifest}"
)"
backend_tag="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") |
    .data."cluster-dto.yaml" | from_yaml | .otelCollector.imageConfig.tag' "${manifest}"
)"
helm_managed_tag="$(yq -r '.helmManaged.otelCollector.imageTag' "${chart_root}/nvca-operator/values.yaml")"

expected_repository="nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector"
if [[ "${operator_repository}" != "${expected_repository}" ]]; then
  echo "expected release chart to select ${expected_repository}, got ${operator_repository}" >&2
  exit 1
fi

for selected_tag in "${operator_tag}" "${backend_tag}" "${helm_managed_tag}"; do
  if [[ "${selected_tag}" != "${compatible_tag}" ]]; then
    echo "expected every collector path to select ${compatible_tag}, got ${selected_tag}" >&2
    exit 1
  fi
done

# Select the service API key branch from the same Go template that NVCA renders
# into its collector ConfigMap. This leaves runtime env substitutions intact for
# the collector's env provider to resolve below.
awk '
  /^\{\{ if \.UseOAuth2 \}\}$/ { in_auth = 1; include = 0; next }
  /^\{\{ else \}\}$/ && in_auth { include = 1; next }
  /^\{\{ end \}\}$/ && in_auth { in_auth = 0; include = 0; next }
  !in_auth || include { print }
' "${config_template}" > "${config}"

if ! grep -Fq 'k8sobjectsreceiver v0.157.0' "${collector_root}/otel-collector-build.yaml"; then
  echo "collector distribution for ${compatible_tag} does not declare the k8sobjects receiver" >&2
  exit 1
fi

# Both release image targets use this checked-in otelcol module. Build the same
# collector-only entrypoint as nvcf-otel-collector unless release validation
# supplies the binary it already built for the image.
if [[ -z "${NVCF_OTEL_COLLECTOR_BINARY:-}" ]]; then
  (
    cd "${collector_root}/otelcol"
    mkdir -p "${go_build_cache}"
    GOWORK=off GOCACHE="${go_build_cache}" GOMAXPROCS="${GOMAXPROCS:-${build_parallelism}}" \
      go build -p "${build_parallelism}" -trimpath -o "${collector}" .
  )
elif [[ ! -x "${collector}" ]]; then
  echo "NVCF_OTEL_COLLECTOR_BINARY is not executable: ${collector}" >&2
  exit 1
fi

# Let the binary's validate command check every receiver, processor, exporter,
# extension, and setting in NVCA's generated configuration.

printf 'test-token\n' > "${token_file}"
NGC_SERVICE_API_KEY_FILE="${token_file}" \
NVCA_OTEL_COLLECTOR_MEMORY_LIMIT_PERCENTAGE=75 \
NVCA_OTEL_COLLECTOR_SPIKE_LIMIT_PERCENTAGE=20 \
NVCA_OTEL_COLLECTOR_HEALTH_CHECK_PORT=13133 \
NVCA_OTEL_COLLECTOR_FNDS_ENDPOINT=http://127.0.0.1:4318 \
NVCA_OTEL_COLLECTOR_METRICS_PORT=8888 \
NVCA_OTEL_COLLECTOR_AUTHENTICATOR=bearertokenauth \
  "${collector}" validate --config="${config}"

echo "validated generated NVCA config with ${operator_repository}:${operator_tag} collector source"
