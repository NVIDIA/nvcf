#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

default_manifest="${tmp_dir}/default-manifest.yaml"
explicit_manifest="${tmp_dir}/explicit-manifest.yaml"
explicit_values="${tmp_dir}/explicit-values.yaml"
legacy_manifest="${tmp_dir}/legacy-manifest.yaml"
legacy_values="${tmp_dir}/legacy-values.yaml"

render() {
  local manifest="$1"
  shift

  helm template nvca-operator "${repo_root}/nvca-operator" \
    --namespace nvca-operator \
    --values "${repo_root}/nvca-operator/values.yaml" \
    --values "${repo_root}/values.release-sbom.yaml" \
    --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
    --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
    --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
    "$@" \
    > "${manifest}"
}

agent_config_value() {
  local manifest="$1"
  local expression="$2"

  yq -er "select(.kind == \"ConfigMap\" and .metadata.name == \"agent-config-merge\") | .data.\"config.yaml\" | from_yaml | ${expression}" "${manifest}"
}

# agent_config_has reports key presence without yq -e's exit-status handling,
# since -e also reports failure for a present `false` result, which would
# make a boolean `has()` check indistinguishable from a real error.
agent_config_has() {
  local manifest="$1"
  local parent_expression="$2"
  local key="$3"

  yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"agent-config-merge\") | .data.\"config.yaml\" | from_yaml | (${parent_expression} // {}) | has(\"${key}\")" "${manifest}"
}

assert_equal() {
  local expected="$1"
  local actual="$2"
  local message="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    echo "${message}: expected ${expected}, got ${actual}" >&2
    exit 1
  fi
}

assert_absent() {
  local manifest="$1"
  local expression="$2"
  local message="$3"

  if agent_config_value "${manifest}" "${expression}" >/dev/null 2>&1; then
    echo "${message}: expected field to be absent, but it was present" >&2
    exit 1
  fi
}

# assert_key_absent checks key presence with `has()` rather than relying on
# assert_absent's yq -er exit status, which also returns non-zero for a
# present `false` value. That makes assert_absent unsuitable for boolean
# fields: it cannot tell "unset" apart from "explicitly false".
assert_key_absent() {
  local manifest="$1"
  local parent_expression="$2"
  local key="$3"
  local message="$4"

  local has
  has="$(agent_config_has "${manifest}" "${parent_expression}" "${key}")"
  assert_equal "false" "${has}" "${message}"
}

# storage.* and worker.* default to unset so a no-op chart upgrade keeps the
# agent's built-in storage/worker defaults.
render "${default_manifest}"
assert_absent "${default_manifest}" '.agent.sharedStorage' "sharedStorage should be unset by default"
assert_absent "${default_manifest}" '.agent.internalPersistentStorage' "internalPersistentStorage should be unset by default"
assert_absent "${default_manifest}" '.agent.minHealthcheckRefreshWait' "minHealthcheckRefreshWait should be unset by default"
assert_absent "${default_manifest}" '.agent.staticGPUCapacity' "staticGPUCapacity should be unset by default"
assert_absent "${default_manifest}" '.agent.computeBackend' "computeBackend should be unset by default"
assert_absent "${default_manifest}" '.agent.requestsNamespace' "requestsNamespace should be unset by default"
assert_absent "${default_manifest}" '.agent.namespaceLabels' "namespaceLabels should be unset by default"
assert_absent "${default_manifest}" '.agent.featureFlags' "featureFlags should be unset by default"
assert_key_absent "${default_manifest}" '.agent' "skipSelfDestruct" "skipSelfDestruct should be unset by default"
assert_key_absent "${default_manifest}" '.agent' "forceSelfDestruct" "forceSelfDestruct should be unset by default"
assert_absent "${default_manifest}" '.agent.csiVolumeMountOptions' "csiVolumeMountOptions should be unset by default"
assert_absent "${default_manifest}" '.agent.credRenewInterval' "credRenewInterval should be unset by default"
assert_absent "${default_manifest}" '.agent.heartbeatInterval' "heartbeatInterval should be unset by default"

cat > "${explicit_values}" <<'EOF'
storage:
  sharedStorage:
    server:
      image: "nvcr.io/nvidia/smb:1.0"
      resources:
        limits:
          cpu: 500m
          memory: 512Mi
    taskData:
      storageClassName: fast-ssd
      mountOptions:
        - noatime
  internalPersistentStorage:
    storageClassName: standard
    hardResourceQuota:
      storage: 50Gi
worker:
  minHealthcheckRefreshWait: "30s"
  staticGPUCapacity: 8
  computeBackend: k8s
  requestsNamespace: nvcf-requests
  namespaceLabels:
    team: nvcf
  featureFlags:
    - NVCA2.0
  skipSelfDestruct: true
  csiVolumeMountOptions:
    - ro
  timeouts:
    credRenewInterval: "45m"
    heartbeatInterval: "5m"
    icmsRequestAckRetryTimeout: "5m"
EOF
render "${explicit_manifest}" --values "${explicit_values}"
assert_equal "nvcr.io/nvidia/smb:1.0" "$(agent_config_value "${explicit_manifest}" '.agent.sharedStorage.server.image')" "unexpected shared-storage server image"
assert_equal "500m" "$(agent_config_value "${explicit_manifest}" '.agent.sharedStorage.server.containerResources.limits.cpu')" "unexpected shared-storage server CPU limit"
assert_equal "fast-ssd" "$(agent_config_value "${explicit_manifest}" '.agent.sharedStorage.taskData.storageClassName')" "unexpected shared-storage task data storage class"
assert_equal "noatime" "$(agent_config_value "${explicit_manifest}" '.agent.sharedStorage.taskData.pvMountOptions[0]')" "unexpected shared-storage task data mount options"
assert_equal "standard" "$(agent_config_value "${explicit_manifest}" '.agent.internalPersistentStorage.storageClassName')" "unexpected IPS storage class"
assert_equal "50Gi" "$(agent_config_value "${explicit_manifest}" '.agent.internalPersistentStorage.hardResourceQuota.storage')" "unexpected IPS hard resource quota"
assert_equal "30s" "$(agent_config_value "${explicit_manifest}" '.agent.minHealthcheckRefreshWait')" "unexpected healthcheck refresh wait"
assert_equal "8" "$(agent_config_value "${explicit_manifest}" '.agent.staticGPUCapacity')" "unexpected static GPU capacity"
assert_equal "k8s" "$(agent_config_value "${explicit_manifest}" '.agent.computeBackend')" "unexpected compute backend"
assert_equal "nvcf-requests" "$(agent_config_value "${explicit_manifest}" '.agent.requestsNamespace')" "unexpected requests namespace"
assert_equal "nvcf" "$(agent_config_value "${explicit_manifest}" '.agent.namespaceLabels.team')" "unexpected namespace labels"
assert_equal "NVCA2.0" "$(agent_config_value "${explicit_manifest}" '.agent.featureFlags[0]')" "unexpected feature flags"
assert_equal "true" "$(agent_config_value "${explicit_manifest}" '.agent.skipSelfDestruct')" "unexpected skip self-destruct"
assert_equal "ro" "$(agent_config_value "${explicit_manifest}" '.agent.csiVolumeMountOptions[0]')" "unexpected CSI volume mount options"
assert_equal "45m" "$(agent_config_value "${explicit_manifest}" '.agent.credRenewInterval')" "unexpected cred renew interval"
assert_equal "5m" "$(agent_config_value "${explicit_manifest}" '.agent.heartbeatInterval')" "unexpected heartbeat interval"
assert_equal "5m" "$(agent_config_value "${explicit_manifest}" '.agent.icmsRequestAckRetryTimeout')" "unexpected ICMS request ack retry timeout"

yq eval '
  .agentConfig.mergeConfig = "agent:\n  computeBackend: legacy-backend\n  sharedStorage:\n    server:\n      image: legacy-image\ncluster:\n  validationPolicy:\n    name: Unrestricted\n    allowedExtraKubernetesTypes:\n      - group: nvidia.com\n        kind: DynamoGraphDeployment\n        resource: dynamographdeployments\n        version: v1alpha1" |
  .agentConfig.mergeConfig style="literal"
' "${repo_root}/nvca-operator/values.yaml" > "${legacy_values}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${legacy_values}" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  > "${legacy_manifest}"

assert_equal "legacy-backend" "$(agent_config_value "${legacy_manifest}" '.agent.computeBackend')" "legacy computeBackend did not override the chart default"
assert_equal "legacy-image" "$(agent_config_value "${legacy_manifest}" '.agent.sharedStorage.server.image')" "legacy sharedStorage did not override the chart default"
assert_equal "dynamographdeployments" "$(agent_config_value "${legacy_manifest}" '.cluster.validationPolicy.allowedExtraKubernetesTypes[0].resource')" "legacy validation policy was dropped"
assert_equal "true" "$(yq -er 'select(.kind == "ConfigMap" and .metadata.name == "agent-config-merge") | .metadata.annotations."nvcf.nvidia.com/legacy-first-class-config"' "${legacy_manifest}")" "legacy storage/worker config was not annotated"
