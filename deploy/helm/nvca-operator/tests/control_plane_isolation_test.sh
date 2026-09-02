#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
source_chart="${repo_root}/../../../src/compute-plane-services/nvca/deployments/nvca-operator"
vendored_chart="${repo_root}/nvca-operator"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

render_chart() {
  local chart="$1"
  local release="$2"
  local namespace="$3"
  local control_plane_id="$4"
  local output="$5"
  local secret_mirror_selector="${6-nvcf.nvidia.com/mirror-test=true}"
  local secret_mirror_namespace="${7-}"
  local -a identity_args=()
  local -a secret_mirror_args=()

  if [[ -n "${control_plane_id}" ]]; then
    identity_args+=(--set-string "controlPlane.id=${control_plane_id}")
  fi
  if [[ -n "${secret_mirror_selector}" ]]; then
    secret_mirror_args+=(--set-string "agent.secretMirrorLabelSelector=${secret_mirror_selector}")
  fi
  if [[ -n "${secret_mirror_namespace}" ]]; then
    secret_mirror_args+=(--set-string "agent.secretMirrorNamespace=${secret_mirror_namespace}")
  fi

  helm template "${release}" "${chart}" \
    --include-crds \
    --namespace "${namespace}" \
    --set-string ngcConfig.serviceKey=test-service-key \
    --set-string ngcConfig.clusterSource=self-managed \
    --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
    --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
    --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
    "${identity_args[@]}" \
    "${secret_mirror_args[@]}" > "${output}"
}

assert_equal() {
  local expected="$1"
  local actual="$2"
  local description="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    printf 'expected %s to be %q, got %q\n' "${description}" "${expected}" "${actual}" >&2
    exit 1
  fi
}

assert_manifest_not_contains() {
  local needle="$1"
  local manifest="$2"
  local description="$3"

  if grep -Fq -- "${needle}" "${manifest}"; then
    printf 'expected %s to be absent in %s\n' "${description}" "${manifest}" >&2
    exit 1
  fi
}

manifest_value() {
  local expression="$1"
  local manifest="$2"
  yq -r "${expression}" "${manifest}"
}

assert_no_resource_collisions() {
  local manifest_a="$1"
  local manifest_b="$2"
  local resources_a="${tmp_dir}/resources-a.txt"
  local resources_b="${tmp_dir}/resources-b.txt"
  local collisions="${tmp_dir}/collisions.txt"

  yq -r 'select(.kind != null and .kind != "CustomResourceDefinition") |
    [.apiVersion, .kind, (.metadata.namespace // "<cluster>"), .metadata.name] | @tsv' \
    "${manifest_a}" | sed '/^$/d' | sort -u > "${resources_a}"
  yq -r 'select(.kind != null and .kind != "CustomResourceDefinition") |
    [.apiVersion, .kind, (.metadata.namespace // "<cluster>"), .metadata.name] | @tsv' \
    "${manifest_b}" | sed '/^$/d' | sort -u > "${resources_b}"
  comm -12 "${resources_a}" "${resources_b}" > "${collisions}"

  if [[ -s "${collisions}" ]]; then
    echo "control-plane chart renders collide:" >&2
    cat "${collisions}" >&2
    exit 1
  fi
}

for chart in "${source_chart}" "${vendored_chart}"; do
  chart_name="$(basename "$(dirname "${chart}")")-$(basename "${chart}")"
  crd_template="${chart}/templates/crds/nvidia.io_nvcfbackends_crd.yaml"
  legacy_manifest="${tmp_dir}/${chart_name}-legacy.yaml"
  plane_a_manifest="${tmp_dir}/${chart_name}-plane-a.yaml"
  plane_b_manifest="${tmp_dir}/${chart_name}-plane-b.yaml"
  selector_off_manifest="${tmp_dir}/${chart_name}-plane-a-selector-off.yaml"
  custom_source_manifest="${tmp_dir}/${chart_name}-plane-a-custom-source.yaml"

  render_chart "${chart}" nvca-operator nvca-operator "" "${legacy_manifest}"
  render_chart "${chart}" plane-a-nvca-operator plane-a-nvca-operator plane-a "${plane_a_manifest}"
  render_chart "${chart}" plane-b-nvca-operator plane-b-nvca-operator plane-b "${plane_b_manifest}"
  render_chart "${chart}" plane-a-nvca-operator plane-a-nvca-operator plane-a "${selector_off_manifest}" ""
  render_chart "${chart}" plane-a-nvca-operator plane-a-nvca-operator plane-a "${custom_source_manifest}" "mirror=true" "shared-secrets"

  # The CRD used to be owned as a normal Helm template. Keep it templated so
  # an upgrade cannot prune it, preserve it on uninstall, and omit it when a
  # different release already owns the shared definition.
  [[ -f "${crd_template}" ]] || {
    echo "expected ownership-gated CRD template in ${chart}" >&2
    exit 1
  }
  [[ ! -e "${chart}/crds/nvidia.io_nvcfbackends_crd.yaml" ]] || {
    echo "expected CRD to remain upgrade-safe under templates/, not crds/, in ${chart}" >&2
    exit 1
  }
  grep -Fq 'helm.sh/resource-policy: keep' "${crd_template}" || {
    echo "expected shared CRD to be retained on uninstall in ${chart}" >&2
    exit 1
  }
  grep -Fq 'lookup "apiextensions.k8s.io/v1" "CustomResourceDefinition"' "${crd_template}" || {
    echo "expected shared CRD ownership lookup in ${chart}" >&2
    exit 1
  }
  grep -Fq 'meta.helm.sh/release-name' "${crd_template}" || {
    echo "expected shared CRD release-owner gate in ${chart}" >&2
    exit 1
  }
  grep -Fq 'meta.helm.sh/release-namespace' "${crd_template}" || {
    echo "expected shared CRD release-namespace gate in ${chart}" >&2
    exit 1
  }

  # Empty identity is a strict compatibility mode: names and target namespaces
  # remain unchanged, and no new runtime identity is emitted.
  assert_equal nvca-operator "$(manifest_value 'select(.kind == "Deployment") | .metadata.name' "${legacy_manifest}")" "legacy Deployment name"
  assert_equal nvca-operator "$(manifest_value 'select(.kind == "Deployment") | .metadata.namespace' "${legacy_manifest}")" "legacy Deployment namespace"
  assert_equal nvca-operator "$(manifest_value 'select(.kind == "ResourceQuota") | .metadata.name' "${legacy_manifest}")" "legacy ResourceQuota name"
  assert_equal nvca-operator "$(manifest_value 'select(.kind == "ResourceQuota") | .metadata.namespace' "${legacy_manifest}")" "legacy ResourceQuota namespace"
  assert_equal nvca-system "$(manifest_value 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "nvca-mirror") | .args[3]' "${legacy_manifest}")" "legacy mirror target"
  assert_manifest_not_contains '--control-plane-id' "${legacy_manifest}" "legacy control-plane CLI flag"
  assert_manifest_not_contains 'NVCF_CONTROL_PLANE_ID' "${legacy_manifest}" "legacy control-plane environment variable"
  legacy_args="$(manifest_value 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.args[0] == "/usr/bin/nvca-operator") | .args | @json' "${legacy_manifest}")"
  if [[ "${legacy_args}" != *'"--nvca-secret-mirror-source-namespace","nvca-operator"'* ]]; then
    printf 'expected legacy secret mirror source namespace, got %s\n' "${legacy_args}" >&2
    exit 1
  fi

  for plane in a b; do
    manifest_var="plane_${plane}_manifest"
    manifest="${!manifest_var}"
    control_plane_id="plane-${plane}"
    operator_namespace="${control_plane_id}-nvca-operator"
    agent_namespace="${control_plane_id}-nvca-system"
    requests_namespace="${control_plane_id}-nvcf-backend"

    assert_equal "${operator_namespace}" "$(manifest_value 'select(.kind == "Deployment") | .metadata.name' "${manifest}")" "named Deployment name"
    assert_equal "${operator_namespace}" "$(manifest_value 'select(.kind == "Deployment") | .metadata.namespace' "${manifest}")" "named Deployment namespace"
    assert_equal "${control_plane_id}" "$(manifest_value 'select(.kind == "Deployment") | .metadata.labels."nvcf.nvidia.com/control-plane-id"' "${manifest}")" "named resource identity label"
    assert_equal "${control_plane_id}" "$(manifest_value 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.args[0] == "/usr/bin/nvca-operator") | .env[] | select(.name == "NVCF_CONTROL_PLANE_ID") | .value' "${manifest}")" "named runtime identity environment variable"
    assert_equal "${control_plane_id}" "$(manifest_value 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") | .data."cluster-dto.yaml" | from_yaml | .controlPlaneID' "${manifest}")" "named DTO identity"
    assert_equal "${agent_namespace}" "$(manifest_value 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") | .data."cluster-dto.yaml" | from_yaml | .systemNamespace' "${manifest}")" "named agent namespace"
    assert_equal "${requests_namespace}" "$(manifest_value 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") | .data."cluster-dto.yaml" | from_yaml | .requestsNamespace' "${manifest}")" "named requests namespace"
    assert_equal "${agent_namespace}" "$(manifest_value 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "nvca-mirror") | .args[3]' "${manifest}")" "named mirror target"
    assert_equal "${operator_namespace}" "$(manifest_value 'select(.kind == "ResourceQuota") | .metadata.name' "${manifest}")" "named ResourceQuota name"
    assert_equal "${operator_namespace}" "$(manifest_value 'select(.kind == "ResourceQuota") | .metadata.namespace' "${manifest}")" "named ResourceQuota namespace"

    args="$(manifest_value 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.args[0] == "/usr/bin/nvca-operator") | .args | @json' "${manifest}")"
    if [[ "${args}" != *'"--control-plane-id","'"${control_plane_id}"'"'* ]]; then
      printf 'expected named runtime args to include --control-plane-id %s, got %s\n' "${control_plane_id}" "${args}" >&2
      exit 1
    fi
    if [[ "${args}" != *'"--nvca-secret-mirror-source-namespace","'"${operator_namespace}"'"'* ]]; then
      printf 'expected named secret mirror source namespace %s, got %s\n' "${operator_namespace}" "${args}" >&2
      exit 1
    fi
    if [[ "${args}" != *'"--nvca-secret-mirror-label-selector","nvcf.nvidia.com/mirror-test=true"'* ]]; then
      printf 'expected named secret mirror selector, got %s\n' "${args}" >&2
      exit 1
    fi
  done

  selector_off_args="$(manifest_value 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.args[0] == "/usr/bin/nvca-operator") | .args | @json' "${selector_off_manifest}")"
  if [[ "${selector_off_args}" != *'"--nvca-secret-mirror-source-namespace","plane-a-nvca-operator"'* ]] ||
      [[ "${selector_off_args}" == *'"--nvca-secret-mirror-label-selector"'* ]]; then
    printf 'expected selector-off named render to keep only its plane-local source, got %s\n' "${selector_off_args}" >&2
    exit 1
  fi

  custom_source_args="$(manifest_value 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.args[0] == "/usr/bin/nvca-operator") | .args | @json' "${custom_source_manifest}")"
  if [[ "${custom_source_args}" != *'"--nvca-secret-mirror-source-namespace","shared-secrets"'* ]] ||
      [[ "${custom_source_args}" != *'"--nvca-secret-mirror-label-selector","mirror=true"'* ]]; then
    printf 'expected explicit shared secret source and selector to remain authoritative, got %s\n' "${custom_source_args}" >&2
    exit 1
  fi

  assert_no_resource_collisions "${plane_a_manifest}" "${plane_b_manifest}"

  for cleanup_kind in ServiceAccount ClusterRole ClusterRoleBinding Job; do
    cleanup_policy="$(manifest_value 'select(.kind == "'"${cleanup_kind}"'" and .metadata.name == "plane-a-nvca-operator-pre-delete-cleanup") | .metadata.annotations."helm.sh/hook-delete-policy"' "${plane_a_manifest}")"
    assert_equal before-hook-creation,hook-succeeded "${cleanup_policy}" "${cleanup_kind} pre-delete hook cleanup policy"
  done

  assert_equal 1 "$(grep -Fc 'kind: CustomResourceDefinition' "${legacy_manifest}")" "legacy CRD upgrade ownership"
  assert_equal 0 "$(grep -Fc 'kind: CustomResourceDefinition' "${plane_a_manifest}")" "plane A defers CRD to shared prerequisites"
  assert_equal 0 "$(grep -Fc 'kind: CustomResourceDefinition' "${plane_b_manifest}")" "plane B defers CRD to shared prerequisites"
done

for chart in "${source_chart}" "${vendored_chart}"; do
  for invalid_id in default Plane-A plane_a -plane plane- aaaaaaaaaaaaaaaaaaaaa; do
    release_name="${invalid_id}-nvca-operator"
    if helm template "${release_name}" "${chart}" \
      --namespace "${release_name}" \
      --set-string ngcConfig.serviceKey=test-service-key \
      --set-string "controlPlane.id=${invalid_id}" > /dev/null 2>&1; then
      printf 'expected invalid controlPlane.id %q to fail for %s\n' "${invalid_id}" "${chart}" >&2
      exit 1
    fi
  done
  grep -Fq 'if eq $id "default"' "${chart}/templates/_helpers.tpl" || {
    echo "expected helper-level reserved default rejection in ${chart}" >&2
    exit 1
  }
  grep -Fq '"const": "default"' "${chart}/values.schema.json" || {
    echo "expected schema-level reserved default rejection in ${chart}" >&2
    exit 1
  }
done

if helm template nvca-operator "${source_chart}" \
  --namespace nvca-operator \
  --set-string ngcConfig.serviceKey=test-service-key \
  --set-string controlPlane.id=plane-a > /dev/null 2>&1; then
  echo "expected named control plane to reject the legacy release and namespace" >&2
  exit 1
fi

echo "validated legacy compatibility, dual control-plane chart and secret-mirror isolation, runtime identity propagation, shared CRD deferral, and invalid identities"
