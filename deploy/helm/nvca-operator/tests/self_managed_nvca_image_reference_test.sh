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

manifest="${tmp_dir}/manifest.yaml"
manifest_without_nvca_image_override="${tmp_dir}/manifest-without-nvca-image-override.yaml"
manifest_with_transport_tls_bundle="${tmp_dir}/manifest-with-transport-tls-bundle.yaml"
transport_tls_merge_config="${tmp_dir}/transport-tls-merge-config.yaml"

cat > "${transport_tls_merge_config}" <<'EOF'
workload:
  transportTLS:
    trustMode: bundle
    trustBundleFingerprint: sha256:9a7814909424061a68756ee5c26aa1a1491b8d20a7b813fb24fa7e73b2fa1c93
    trustBundlePem: test-ca-bundle
    installerImage: nvcr.io/nvidia/nvcf-byoc/nvca:test
EOF

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.icmsServiceHostHeaderOverride=sis.gateway.example.invalid \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.revalServiceHostHeaderOverride=reval.gateway.example.invalid \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set-string selfManaged.natsHostOverride=nats.gateway.example.invalid \
  > "${manifest}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string nvcaImage.repositoryOverride= \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.icmsServiceHostHeaderOverride=sis.gateway.example.invalid \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.revalServiceHostHeaderOverride=reval.gateway.example.invalid \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set-string selfManaged.natsHostOverride=nats.gateway.example.invalid \
  > "${manifest_without_nvca_image_override}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set-file agentConfig.mergeConfig="${transport_tls_merge_config}" \
  > "${manifest_with_transport_tls_bundle}"

nvca_image_repository="$(yq -r '.nvcaImage.repositoryOverride' "${repo_root}/values.release-sbom.yaml")"
nvca_version="$(yq -r '.selfManaged.nvcaVersion' "${repo_root}/nvca-operator/values.yaml")"
image_credential_helper_repository="$(yq -r '.selfManaged.imageCredHelper.imageRepository' "${repo_root}/values.release-sbom.yaml")"
image_credential_helper_tag="$(yq -r '.selfManaged.imageCredHelper.imageTag' "${repo_root}/nvca-operator/values.yaml")"
samba_repository="$(yq -r '.selfManaged.sharedStorage.imageRepository' "${repo_root}/values.release-sbom.yaml")"
samba_tag="$(yq -r '.selfManaged.sharedStorage.imageTag' "${repo_root}/nvca-operator/values.yaml")"

expected_annotations=(
  "release-artifact-nvca-image: \"${nvca_image_repository}:${nvca_version}\""
  "release-artifact-nvcf-image-credential-helper-image: \"${image_credential_helper_repository}:${image_credential_helper_tag}\""
  "release-artifact-samba-image: \"${samba_repository}:${samba_tag}\""
)

for expected_annotation in "${expected_annotations[@]}"; do
  if ! grep -Fq "${expected_annotation}" "${manifest}"; then
    echo "expected rendered self-managed manifest to expose ${expected_annotation}" >&2
    exit 1
  fi
done

if grep -Fq "release-artifact-nvca-image:" "${manifest_without_nvca_image_override}"; then
  echo "expected rendered self-managed manifest without nvcaImage.repositoryOverride to omit release-artifact-nvca-image" >&2
  exit 1
fi

for expected_annotation in \
  "release-artifact-nvcf-image-credential-helper-image: \"${image_credential_helper_repository}:${image_credential_helper_tag}\"" \
  "release-artifact-samba-image: \"${samba_repository}:${samba_tag}\""
do
  if ! grep -Fq "${expected_annotation}" "${manifest_without_nvca_image_override}"; then
    echo "expected rendered self-managed manifest without nvcaImage.repositoryOverride to expose ${expected_annotation}" >&2
    exit 1
  fi
done

for expected_host in \
  'icmsServiceHostHeaderOverride: "sis.gateway.example.invalid"' \
  'helmReValServiceHostHeaderOverride: "reval.gateway.example.invalid"' \
  'natsHostOverride: "nats.gateway.example.invalid"'
do
  if ! grep -Fq "${expected_host}" "${manifest}"; then
    echo "expected rendered self-managed manifest to expose ${expected_host}" >&2
    exit 1
  fi
done

if grep -Fq 'trustBundlePem:' "${manifest}"; then
  echo "expected default self-managed manifest not to render trustBundlePem when unset" >&2
  exit 1
fi

if grep -Fq '    transportTls:' "${manifest}"; then
  echo "expected default self-managed manifest not to render transportTls when only defaults are set" >&2
  exit 1
fi

cluster_dto_has_transport_tls="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") | .data."cluster-dto.yaml" | from_yaml | has("transportTls")' \
    "${manifest_with_transport_tls_bundle}"
)"
if [[ "${cluster_dto_has_transport_tls}" != "false" ]]; then
  echo "expected bundle-mode self-managed manifest not to render transportTls in cluster-dto.yaml" >&2
  exit 1
fi

agent_config_transport_tls_trust_mode="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "agent-config-merge") | .data."config.yaml" | from_yaml | .workload.transportTLS.trustMode' \
    "${manifest_with_transport_tls_bundle}"
)"
if [[ "${agent_config_transport_tls_trust_mode}" != "bundle" ]]; then
  echo "expected bundle-mode self-managed manifest to render workload.transportTLS.trustMode in agent-config-merge" >&2
  exit 1
fi

agent_config_transport_tls_installer_image="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "agent-config-merge") | .data."config.yaml" | from_yaml | .workload.transportTLS.installerImage' \
    "${manifest_with_transport_tls_bundle}"
)"
if [[ "${agent_config_transport_tls_installer_image}" != "nvcr.io/nvidia/nvcf-byoc/nvca:test" ]]; then
  echo "expected bundle-mode self-managed manifest to render workload.transportTLS.installerImage from agentConfig.mergeConfig" >&2
  exit 1
fi

echo "self-managed chart render exposes release-artifact image references and service host overrides"
