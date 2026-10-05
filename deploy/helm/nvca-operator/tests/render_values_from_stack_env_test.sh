#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

stack_env_file="${tmp_dir}/stack-env.yaml"
output_file="${tmp_dir}/rendered-values.yaml"
existing_values_file="${tmp_dir}/existing-values.yaml"
stub_bin_dir="${tmp_dir}/bin"
mkdir -p "${stub_bin_dir}"

cat > "${stack_env_file}" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: <your-org>
  storageClass: ceph-rbd
nats:
  enabled: true
openbao:
  enabled: false
cassandra:
  enabled: false
ingress:
  gatewayApi:
    routes:
      nats:
        enabled: true
    gateways:
      shared: {name: nvcf-gateway, namespace: envoy-gateway}
      grpc: {name: nvcf-gateway, namespace: envoy-gateway}
      nats: {name: nats-gateway, namespace: envoy-gateway}
      llmGrpc: {name: llm-grpc-gateway, namespace: envoy-gateway}
EOF

cat > "${stub_bin_dir}/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" == "status" ]]; then
  exit 0
fi

if [[ "$1" == "get" && "$2" == "values" ]]; then
  cat "${STUB_EXISTING_VALUES}"
  exit 0
fi

echo "unexpected helm invocation: $*" >&2
exit 1
EOF
chmod +x "${stub_bin_dir}/helm"

cat > "${existing_values_file}" <<'EOF'
image:
  tag: 3.2.6
selfManaged:
  nvcaVersion: 3.2.6
EOF

PATH="${stub_bin_dir}:$PATH" \
STUB_EXISTING_VALUES="${existing_values_file}" \
STACK_ENV_FILE="${stack_env_file}" \
OUTPUT_FILE="${output_file}" \
RELEASE="nvca-operator" \
NAMESPACE="nvca-operator" \
NVCA_OPERATOR_VERSION="3.2.7" \
NVCA_VERSION="3.2.7" \
"${repo_root}/scripts/render_values_from_stack_env.sh"

actual_image_tag="$(yq -r '.image.tag' "${output_file}")"
actual_nvca_version="$(yq -r '.selfManaged.nvcaVersion' "${output_file}")"

if [[ "${actual_image_tag}" != "3.2.7" ]]; then
  echo "expected image.tag to use the requested operator version, got ${actual_image_tag}" >&2
  exit 1
fi

if [[ "${actual_nvca_version}" != "3.2.7" ]]; then
  echo "expected selfManaged.nvcaVersion to use the requested NVCA version, got ${actual_nvca_version}" >&2
  exit 1
fi

actual_gateway_names="$(yq -r '.clusterValidator.gatewayNames | join(",")' "${output_file}")"
if [[ "${actual_gateway_names}" != "envoy-gateway/nats-gateway,envoy-gateway/nvcf-gateway" ]]; then
  echo "expected the wired stack Gateways in clusterValidator.gatewayNames, got ${actual_gateway_names}" >&2
  exit 1
fi

actual_storage_class="$(yq -r '.clusterValidator.storageClass' "${output_file}")"
if [[ "${actual_storage_class}" != "ceph-rbd" ]]; then
  echo "expected clusterValidator.storageClass from global.storageClass, got ${actual_storage_class}" >&2
  exit 1
fi

actual_external="$(yq -r '.clusterValidator.externalComponents | join(",")' "${output_file}")"
if [[ "${actual_external}" != "openbao,cassandra" ]]; then
  echo "expected the components the stack disables in clusterValidator.externalComponents, got ${actual_external}" >&2
  exit 1
fi

actual_probe_image="$(yq -r '.clusterValidator.nodeToNodeProbeImage' "${output_file}")"
if [[ -n "${actual_probe_image}" ]]; then
  echo "expected no probe image on nvcr.io, which has no busybox, got ${actual_probe_image}" >&2
  exit 1
fi

# An upgrade reuses the release's values, but the stack-derived validator
# keys follow the stack: a component it installs again, a StorageClass or
# Gateways it no longer sets, and a mirror registry the probe must pull from.
cat > "${stack_env_file}" <<'EOF'
global:
  image:
    registry: registry.example.com
    repository: mirror/nvcf
cassandra:
  enabled: true
ingress:
  gatewayApi:
    enabled: false
    gateways:
      shared: {name: nvcf-gateway, namespace: envoy-gateway}
EOF
cat > "${existing_values_file}" <<'EOF'
clusterValidator:
  gatewayNames: [envoy-gateway/nvcf-gateway]
  storageClass: ceph-rbd
  externalComponents: [cassandra]
  nodeToNodeProbeImage: busybox:1.36
EOF

render_upgrade() {
  PATH="${stub_bin_dir}:$PATH" \
  STUB_EXISTING_VALUES="${existing_values_file}" \
  STACK_ENV_FILE="${stack_env_file}" \
  OUTPUT_FILE="${output_file}" \
  "${repo_root}/scripts/render_values_from_stack_env.sh" >/dev/null
}

render_upgrade
for key in gatewayNames externalComponents; do
  actual="$(yq -o=json -I=0 ".clusterValidator.${key}" "${output_file}")"
  if [[ "${actual}" != "[]" ]]; then
    echo "expected clusterValidator.${key} to follow the stack and be empty, got ${actual}" >&2
    exit 1
  fi
done
actual_storage_class="$(yq -o=json '.clusterValidator.storageClass' "${output_file}")"
if [[ "${actual_storage_class}" != '""' ]]; then
  echo "expected clusterValidator.storageClass to follow the stack and be empty, got ${actual_storage_class}" >&2
  exit 1
fi
actual_probe_image="$(yq -r '.clusterValidator.nodeToNodeProbeImage' "${output_file}")"
if [[ "${actual_probe_image}" != "registry.example.com/mirror/nvcf/busybox:1.36" ]]; then
  echo "expected the probe image from the stack's mirror registry, got ${actual_probe_image}" >&2
  exit 1
fi

NODE_TO_NODE_PROBE_IMAGE="tools.example.com/busybox:1.37" render_upgrade
actual_probe_image="$(yq -r '.clusterValidator.nodeToNodeProbeImage' "${output_file}")"
if [[ "${actual_probe_image}" != "tools.example.com/busybox:1.37" ]]; then
  echo "expected NODE_TO_NODE_PROBE_IMAGE to override the probe image, got ${actual_probe_image}" >&2
  exit 1
fi

echo "render_values_from_stack_env.sh keeps upgrade versions and stack-derived validator values aligned with the stack"
