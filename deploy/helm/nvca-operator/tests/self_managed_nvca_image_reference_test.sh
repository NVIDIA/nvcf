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
stage_manifest="${tmp_dir}/stage-manifest.yaml"
manifest_with_byoo_override="${tmp_dir}/manifest-with-byoo-override.yaml"
legacy_stage_manifest="${tmp_dir}/legacy-stage-manifest.yaml"
legacy_values="${tmp_dir}/legacy-values.yaml"
manifest_without_byoo_default="${tmp_dir}/manifest-without-byoo-default.yaml"
supplemental_manifest="${tmp_dir}/supplemental-manifest.yaml"
stage_supplemental_manifest="${tmp_dir}/stage-supplemental-manifest.yaml"
image_manifest="${tmp_dir}/image-manifest.txt"
stage_image_manifest="${tmp_dir}/stage-image-manifest.txt"
manifest_without_nvca_image_override="${tmp_dir}/manifest-without-nvca-image-override.yaml"
manifest_with_transport_tls_bundle="${tmp_dir}/manifest-with-transport-tls-bundle.yaml"
transport_tls_merge_config="${tmp_dir}/transport-tls-merge-config.yaml"

decode_byoo_override() {
  local manifest_path="$1"
  local flag="$2"
  local encoded_overrides

  encoded_overrides="$(
    awk -v flag="${flag}" '
      $0 ~ "-[[:space:]]+" flag "$" {
        expect_value = 1
        next
      }
      expect_value && /^[[:space:]]*-[[:space:]]*/ {
        value = $0
        sub(/^[[:space:]]*-[[:space:]]*"?/, "", value)
        sub(/"[[:space:]]*$/, "", value)
        print value
        exit
      }
    ' "${manifest_path}"
  )"

  if [[ -z "${encoded_overrides}" ]]; then
    echo "missing ${flag} in ${manifest_path}" >&2
    exit 1
  fi

  if decoded_overrides="$(printf '%s' "${encoded_overrides}" | base64 --decode 2>/dev/null)"; then
    :
  elif decoded_overrides="$(printf '%s' "${encoded_overrides}" | base64 -D 2>/dev/null)"; then
    :
  else
    echo "invalid ${flag} base64 in ${manifest_path}" >&2
    exit 1
  fi

  printf '%s' "${decoded_overrides}" | yq -er '.BYOO_OTEL_COLLECTOR_CONTAINER'
}

cat > "${transport_tls_merge_config}" <<'EOF'
workload:
  transportTLS:
    trustMode: bundle
    trustBundleFingerprint: sha256:9a7814909424061a68756ee5c26aa1a1491b8d20a7b813fb24fa7e73b2fa1c93
    trustBundlePem: test-ca-bundle
    installerImage: nvcr.io/nvidia/nvcf-byoc/nvca:test
EOF

yq eval '
  del(.agent.byooOtelCollector) |
  .image.repository = "stg.nvcr.io/nvidia/nvcf-byoc/nvca-operator" |
  .nvcaImage.repositoryOverride = "stg.nvcr.io/nvidia/nvcf-byoc/nvca" |
  .ngcConfig.clusterSource = "self-managed" |
  .agent.functionEnvOverrides.BYOO_OTEL_COLLECTOR_CONTAINER = "nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector:0.157.11" |
  .agent.taskEnvOverrides.BYOO_OTEL_COLLECTOR_CONTAINER = "nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector:0.157.11"
' "${repo_root}/nvca-operator/values.yaml" > "${legacy_values}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --set-string ngcConfig.clusterSource=self-managed \
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
  --set-string ngcConfig.clusterSource=self-managed \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string image.repository=stg.nvcr.io/nvidia/nvcf-byoc/nvca-operator \
  --set-string nvcaImage.repositoryOverride=stg.nvcr.io/nvidia/nvcf-byoc/nvca \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.icmsServiceHostHeaderOverride=sis.gateway.example.invalid \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.revalServiceHostHeaderOverride=reval.gateway.example.invalid \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set-string selfManaged.natsHostOverride=nats.gateway.example.invalid \
  > "${stage_manifest}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --set-string ngcConfig.clusterSource=self-managed \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string agent.functionEnvOverrides.BYOO_OTEL_COLLECTOR_CONTAINER=registry.example.test/nvcf/byoo-otel-collector:1.2.3 \
  --set-string agent.taskEnvOverrides.BYOO_OTEL_COLLECTOR_CONTAINER=registry.example.test/nvcf/byoo-otel-collector:1.2.3 \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.icmsServiceHostHeaderOverride=sis.gateway.example.invalid \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.revalServiceHostHeaderOverride=reval.gateway.example.invalid \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set-string selfManaged.natsHostOverride=nats.gateway.example.invalid \
  > "${manifest_with_byoo_override}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${legacy_values}" \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.icmsServiceHostHeaderOverride=sis.gateway.example.invalid \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.revalServiceHostHeaderOverride=reval.gateway.example.invalid \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set-string selfManaged.natsHostOverride=nats.gateway.example.invalid \
  > "${legacy_stage_manifest}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --set-string ngcConfig.clusterSource=self-managed \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string agent.byooOtelCollector.imageTag= \
  --set-string selfManaged.icmsServiceURL=http://sis.example.invalid:8080 \
  --set-string selfManaged.icmsServiceHostHeaderOverride=sis.gateway.example.invalid \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.revalServiceHostHeaderOverride=reval.gateway.example.invalid \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  --set-string selfManaged.natsHostOverride=nats.gateway.example.invalid \
  > "${manifest_without_byoo_default}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --set-string ngcConfig.clusterSource=self-managed \
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
  --set-string ngcConfig.clusterSource=self-managed \
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
byoo_function_image="$(decode_byoo_override "${manifest}" '--function-env-overrides-b64')"
byoo_task_image="$(decode_byoo_override "${manifest}" '--task-env-overrides-b64')"
stage_byoo_function_image="$(decode_byoo_override "${stage_manifest}" '--function-env-overrides-b64')"
stage_byoo_task_image="$(decode_byoo_override "${stage_manifest}" '--task-env-overrides-b64')"
override_byoo_function_image="$(decode_byoo_override "${manifest_with_byoo_override}" '--function-env-overrides-b64')"
override_byoo_task_image="$(decode_byoo_override "${manifest_with_byoo_override}" '--task-env-overrides-b64')"
legacy_stage_byoo_function_image="$(decode_byoo_override "${legacy_stage_manifest}" '--function-env-overrides-b64')"
legacy_stage_byoo_task_image="$(decode_byoo_override "${legacy_stage_manifest}" '--task-env-overrides-b64')"

if [[ "${byoo_function_image}" != "${byoo_task_image}" ]]; then
  echo "expected function and task BYOO collector defaults to match" >&2
  exit 1
fi

if [[ "${byoo_function_image}" != "nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector:0.157.0-nv-0.1.1" ]]; then
  echo "unexpected BYOO collector default: ${byoo_function_image}" >&2
  exit 1
fi

if [[ "${stage_byoo_function_image}" != "stg.nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector:0.157.0-nv-0.1.1" || \
  "${stage_byoo_task_image}" != "${stage_byoo_function_image}" ]]; then
  echo "expected BYOO collector defaults to use the staging image repository" >&2
  exit 1
fi

if [[ "${override_byoo_function_image}" != "registry.example.test/nvcf/byoo-otel-collector:1.2.3" || \
  "${override_byoo_task_image}" != "${override_byoo_function_image}" ]]; then
  echo "expected explicit BYOO collector image overrides to take precedence" >&2
  exit 1
fi

if [[ "${legacy_stage_byoo_function_image}" != "${stage_byoo_function_image}" || \
  "${legacy_stage_byoo_task_image}" != "${stage_byoo_function_image}" ]]; then
  echo "expected the legacy packaged BYOO default to migrate to the staging image repository" >&2
  exit 1
fi

if grep -Fq -- '--function-env-overrides-b64' "${manifest_without_byoo_default}" || \
  grep -Fq -- '--task-env-overrides-b64' "${manifest_without_byoo_default}"; then
  echo "expected an empty BYOO collector tag to disable the chart default" >&2
  exit 1
fi

"${repo_root}/scripts/render_release_supplemental_images.sh" \
  "${manifest}" \
  "${supplemental_manifest}"
"${repo_root}/scripts/build_release_image_manifest.sh" \
  "${manifest}" \
  "${supplemental_manifest}" \
  "${image_manifest}"
"${repo_root}/scripts/render_release_supplemental_images.sh" \
  "${stage_manifest}" \
  "${stage_supplemental_manifest}"
"${repo_root}/scripts/build_release_image_manifest.sh" \
  "${stage_manifest}" \
  "${stage_supplemental_manifest}" \
  "${stage_image_manifest}"

if ! grep -Fxq "${byoo_function_image}" "${image_manifest}"; then
  echo "expected release image manifest to include the production BYOO collector image" >&2
  exit 1
fi

if ! grep -Fxq "${stage_byoo_function_image}" "${stage_image_manifest}"; then
  echo "expected release image manifest to include the staging BYOO collector image" >&2
  exit 1
fi

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
