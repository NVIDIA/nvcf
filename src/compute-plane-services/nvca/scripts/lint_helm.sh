#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"

install_kubeconform() {
  if ! command -v kubeconform; then
    go install github.com/yannh/kubeconform/cmd/kubeconform@latest
    export PATH="${PATH}:${HOME}/go/bin"
  fi
}

run_lint() {
  local chart_name=${1}
  shift
  local chart_dir="${repo_root}/deployments/${chart_name}"
  local values_file="${repo_root}/deployments/${chart_name}/values.yaml"
  local args=()

  # Process arguments
  while [[ $# -gt 0 ]]; do
    case $1 in
      --values)
        args+=("-f" "$2")
        shift 2
        ;;
      *)
        args+=("$1")
        shift
        ;;
    esac
  done

  echo -e "\nRunning lint test with args: ${args[*]}"

  # shellcheck disable=SC2086
  helm lint --strict -f "$values_file" "${args[@]}" "$chart_dir"
  # shellcheck disable=SC2086
  helm template -f "$values_file" "${args[@]}" "$chart_dir" | kubeconform -ignore-missing-schemas

  echo -e "Test passed"
}

assert_eq() {
  local want=${1}
  local got=${2}
  local message=${3}

  if [[ "${got}" != "${want}" ]]; then
    printf '✗ %s: got %q, want %q\n' "${message}" "${got}" "${want}" >&2
    return 1
  fi
  printf '✓ %s\n' "${message}"
}

assert_pre_delete_cleanup_rbac() {
  local rendered
  rendered="$(mktemp)"
  trap 'rm -f "${rendered}"' RETURN

  local release_name="test-release"
  local cleanup_name="${release_name}-nvca-operator-pre-delete-cleanup"

  helm template "${release_name}" "${repo_root}/deployments/nvca-operator" \
    --set "ngcConfig.serviceKey=fakekey" >"${rendered}"

  assert_eq "${cleanup_name}" \
    "$(yq 'select(.kind == "Job" and .metadata.name == "test-release-nvca-operator-pre-delete-cleanup") | .spec.template.spec.serviceAccountName' "${rendered}")" \
    "pre-delete cleanup Job uses hook-scoped ServiceAccount"
  assert_eq "pre-delete" \
    "$(yq 'select(.kind == "ServiceAccount" and .metadata.name == "test-release-nvca-operator-pre-delete-cleanup") | .metadata.annotations."helm.sh/hook"' "${rendered}")" \
    "pre-delete cleanup ServiceAccount is a pre-delete hook"
  assert_eq "-20" \
    "$(yq 'select(.kind == "ClusterRoleBinding" and .metadata.name == "test-release-nvca-operator-pre-delete-cleanup") | .metadata.annotations."helm.sh/hook-weight"' "${rendered}")" \
    "pre-delete cleanup RBAC runs before cleanup Job"
  assert_eq "${cleanup_name}" \
    "$(yq 'select(.kind == "ClusterRoleBinding" and .metadata.name == "test-release-nvca-operator-pre-delete-cleanup") | .subjects[0].name' "${rendered}")" \
    "pre-delete cleanup ClusterRoleBinding binds hook ServiceAccount"
  assert_eq "${cleanup_name}" \
    "$(yq 'select(.kind == "ClusterRoleBinding" and .metadata.name == "test-release-nvca-operator-pre-delete-cleanup") | .roleRef.name' "${rendered}")" \
    "pre-delete cleanup ClusterRoleBinding uses hook ClusterRole"
  assert_eq "before-hook-creation,hook-succeeded" \
    "$(yq 'select(.kind == "ClusterRoleBinding" and .metadata.name == "test-release-nvca-operator-pre-delete-cleanup") | .metadata.annotations."helm.sh/hook-delete-policy"' "${rendered}")" \
    "pre-delete cleanup ClusterRoleBinding is removed by Helm after job succeeds"
  assert_eq "before-hook-creation" \
    "$(yq 'select(.kind == "ClusterRole" and .metadata.name == "test-release-nvca-operator-pre-delete-cleanup") | .metadata.annotations."helm.sh/hook-delete-policy"' "${rendered}")" \
    "pre-delete cleanup hook RBAC is kept for the running Job"
}

# The Bazel-built NVCA operator image is distroless, so the rendered workload
# commands must execute the packaged binaries directly. A /tini wrapper would
# fail at container startup because that binary is not present in the image.
assert_distroless_operator_commands() {
  local chart_dir=${1}
  local chart_label=${2}
  local rendered
  rendered="$(mktemp)"
  trap 'rm -f "${rendered}"' RETURN

  helm template test-release "${chart_dir}" --set "ngcConfig.serviceKey=fakekey" >"${rendered}"

  assert_eq "/usr/bin/nvca-operator" \
    "$(yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].args[0]' "${rendered}")" \
    "${chart_label} operator starts the packaged binary directly"
  assert_eq "/usr/bin/nvca-mirror" \
    "$(yq 'select(.kind == "Deployment") | .spec.template.spec.containers[1].args[0]' "${rendered}")" \
    "${chart_label} mirror starts the packaged binary directly"
  assert_eq "/usr/bin/nvca-operator-cleanup" \
    "$(yq 'select(.kind == "Job") | .spec.template.spec.containers[0].args[0]' "${rendered}")" \
    "${chart_label} cleanup Job starts the packaged binary directly"
}

assert_distroless_operator_commands "${repo_root}/deployments/nvca-operator" "service chart"
assert_distroless_operator_commands "${repo_root}/../../../deploy/helm/nvca-operator/nvca-operator" "release chart"
install_kubeconform
assert_pre_delete_cleanup_rbac
run_lint nvca-operator --set "ngcConfig.serviceKey=fakekey"
run_lint nvca-operator --set "generateImagePullSecret=false" --set "imagePullSecretName=foo-bar-image-pull"

echo -e "\nTesting self-managed endpoint validation..."
missing_endpoint_output="$(mktemp)"
if helm template test-release "${repo_root}/deployments/nvca-operator" \
  --set "generateImagePullSecret=false" \
  --set "ngcConfig.clusterSource=self-managed" \
  --set-string "clusterID=id" \
  --set-string "clusterGroupID=group" \
  --set-string "clusterName=ncp-local-compute-1" \
  --set-string "selfManaged.nvcaVersion=3.0.0-test" \
  > "${missing_endpoint_output}" 2>&1; then
  echo "Expected self-managed render without control plane endpoints to fail"
  cat "${missing_endpoint_output}"
  rm -f "${missing_endpoint_output}"
  exit 1
fi
for endpoint in icmsServiceURL revalServiceURL natsURL; do
  if ! grep -q "${endpoint}" "${missing_endpoint_output}"; then
    echo "Expected validation output to mention ${endpoint}"
    cat "${missing_endpoint_output}"
    rm -f "${missing_endpoint_output}"
    exit 1
  fi
done
rm -f "${missing_endpoint_output}"
echo -e "Test passed"

run_lint nvca-operator \
  --set "generateImagePullSecret=false" \
  --set "ngcConfig.clusterSource=self-managed" \
  --set-string "clusterID=id" \
  --set-string "clusterGroupID=group" \
  --set-string "clusterName=ncp-local-compute-1" \
  --set-string "selfManaged.nvcaVersion=3.0.0-test" \
  --set-string "selfManaged.icmsServiceURL=http://sis.nvcf-control-plane.test:18080" \
  --set-string "selfManaged.revalServiceURL=http://reval.nvcf-control-plane.test:18080" \
  --set-string "selfManaged.natsURL=nats://nats.nvcf-control-plane.test:14222"

# Regression test: `helm upgrade --reuse-values` from a pre-3.0.0-rc.12 chart leaves
# agent.serviceOAuth unset (the new chart defaults are not merged). The cluster-dto
# ConfigMaps must render nil-safely instead of failing at template render with
# "nil pointer evaluating interface {}.helmReVal".
echo -e "\nTesting nil-safe agent.serviceOAuth (reuse-values upgrade simulation)..."
reuse_values_file="${repo_root}/test/test-reuse-values-no-service-oauth.yaml"

assert_service_oauth_nil_safe() {
  local label="${1}"
  shift
  local render_output
  render_output="$(mktemp)"
  if ! helm template test-release "${repo_root}/deployments/nvca-operator" "$@" \
    --values "${reuse_values_file}" > "${render_output}" 2>&1; then
    echo "Expected ${label} cluster-dto to render without agent.serviceOAuth defaults"
    cat "${render_output}"
    rm -f "${render_output}"
    exit 1
  fi
  if ! grep -q 'helmReValStageOAuthTokenURL: ""' "${render_output}"; then
    echo "Expected ${label} cluster-dto to emit empty nil-safe serviceOAuth values"
    cat "${render_output}"
    rm -f "${render_output}"
    exit 1
  fi
  rm -f "${render_output}"
  echo "✓ ${label} cluster-dto renders nil-safely without agent.serviceOAuth defaults"
}

assert_service_oauth_nil_safe "helm-managed" \
  --set "ngcConfig.serviceKey=fakekey" \
  --set "ngcConfig.clusterSource=helm-managed" \
  --set-string "clusterName=ncp-helm-managed-1" \
  --show-only templates/helm-managed-nvcfbackend-cm.yaml

# Helm-managed Vault authentication must carry an explicit, usable server URL
# into the cluster DTO. This keeps environment selection outside the OSS chart.
assert_helm_managed_vault_address() {
  local chart_dir=${1}
  local chart_label=${2}
  local rendered
  local invalid_address
  rendered="$(mktemp)"
  trap 'rm -f "${rendered}"' RETURN

  helm template test-release "${chart_dir}" \
    --set "ngcConfig.serviceKey=fakekey" \
    --set "ngcConfig.clusterSource=helm-managed" \
    --set-string "helmManaged.oAuthClientID=oauth-client-1" \
    --set-string "vaultConfig.address=https://vault.example.test:443" \
    --show-only templates/helm-managed-nvcfbackend-cm.yaml >"${rendered}"

  assert_eq "https://vault.example.test:443" \
    "$(yq '.data."cluster-dto.yaml" | from_yaml | .vaultConfig.address' "${rendered}")" \
    "${chart_label} helm-managed cluster DTO includes the configured Vault address"

  for invalid_address in \
    "" \
    "https://:443" \
    "http://vault.example.test:8200" \
    " https://vault.example.test:443 " \
    "https://user@vault.example.test:443" \
    "https://vault.example.test:443?" \
    "https://vault.example.test:443?namespace=test" \
    "https://vault.example.test:443#" \
    "https://vault.example.test:443#test"; do
    if helm template test-release "${chart_dir}" \
      --set "ngcConfig.serviceKey=fakekey" \
      --set "ngcConfig.clusterSource=helm-managed" \
      --set-string "helmManaged.oAuthClientID=oauth-client-1" \
      --set-string "vaultConfig.address=${invalid_address}" \
      --show-only templates/helm-managed-nvcfbackend-cm.yaml >"${rendered}" 2>&1; then
      printf 'Expected %s helm-managed render with invalid Vault address %q to fail\n' "${chart_label}" "${invalid_address}"
      return 1
    fi
    grep -q "vaultConfig.address" "${rendered}"
  done
}

assert_helm_managed_vault_address "${repo_root}/deployments/nvca-operator" "service chart"
assert_helm_managed_vault_address "${repo_root}/../../../deploy/helm/nvca-operator/nvca-operator" "release chart"

assert_service_oauth_nil_safe "self-managed" \
  --set "generateImagePullSecret=false" \
  --set "ngcConfig.clusterSource=self-managed" \
  --set-string "clusterID=id" \
  --set-string "clusterGroupID=group" \
  --set-string "clusterName=ncp-local-compute-1" \
  --set-string "selfManaged.nvcaVersion=3.0.0-test" \
  --set-string "selfManaged.icmsServiceURL=http://sis.nvcf-control-plane.test:18080" \
  --set-string "selfManaged.revalServiceURL=http://reval.nvcf-control-plane.test:18080" \
  --set-string "selfManaged.natsURL=nats://nats.nvcf-control-plane.test:14222" \
  --show-only templates/self-managed-nvcfbackend-cm.yaml

# Secret-backed workload transport trust is operator-owned configuration, not a
# raw agentConfig.mergeConfig overlay. Confirm its ConfigMap is rendered with
# defaults, configured values, and values inherited by reuse-values upgrades.
echo -e "\nTesting workload transport trust ConfigMap renders..."
assert_transport_trust_config() {
  local label="${1}"
  local expected_name="${2}"
  local expected_fingerprint="${3}"
  local expected_mount_path="${4}"
  shift 4
  local render_output
  render_output="$(mktemp)"

  if ! helm template test-release "${repo_root}/deployments/nvca-operator" "$@" \
    --show-only templates/operator-config-cm.yaml > "${render_output}" 2>&1; then
    echo "Expected ${label} workload transport trust ConfigMap to render"
    cat "${render_output}"
    rm -f "${render_output}"
    exit 1
  fi
  if ! grep -Fq "name: \"${expected_name}\"" "${render_output}" ||
    ! grep -Fq "fingerprint: \"${expected_fingerprint}\"" "${render_output}"; then
    echo "Expected ${label} workload transport trust configuration"
    cat "${render_output}"
    rm -f "${render_output}"
    exit 1
  fi
  if [[ -z "${expected_mount_path}" ]]; then
    if grep -Fq "installedBundleMountPath:" "${render_output}"; then
      echo "Expected ${label} workload transport trust configuration to omit installedBundleMountPath"
      cat "${render_output}"
      rm -f "${render_output}"
      exit 1
    fi
  elif ! grep -Fq "installedBundleMountPath: \"${expected_mount_path}\"" "${render_output}"; then
    echo "Expected ${label} workload transport trust configuration to set installedBundleMountPath"
    cat "${render_output}"
    rm -f "${render_output}"
    exit 1
  fi
  rm -f "${render_output}"
  echo "✓ ${label} workload transport trust ConfigMap renders"
}

assert_transport_trust_config "default" "" "" "" --set "ngcConfig.serviceKey=fakekey"
assert_transport_trust_config "configured" "nvcf-trust" "sha256:example" "/nvcf/transport-tls" \
  --set "ngcConfig.serviceKey=fakekey" \
  --set "operatorConfig.workload.transportTLS.trustBundle.secretKeyRef.name=nvcf-trust" \
  --set "operatorConfig.workload.transportTLS.fingerprint=sha256:example" \
  --set "operatorConfig.workload.transportTLS.installedBundleMountPath=/nvcf/transport-tls"
assert_transport_trust_config "reuse-values upgrade simulation" "" "" "" \
  --set "ngcConfig.serviceKey=fakekey" \
  --values "${reuse_values_file}" \
  --set "operatorConfig=null"

# Test secret mirroring feature
# Test with only source namespace (should not add args)
run_lint nvca-operator --set "agent.secretMirror.sourceNamespace=custom-ns" --set "ngcConfig.serviceKey=fakekey"

# Test with both source namespace and label selector (should add args)
run_lint nvca-operator --set "agent.secretMirror.sourceNamespace=custom-ns" --set "agent.secretMirror.labelSelector=mirror=true" --set "ngcConfig.serviceKey=fakekey"

# Test custom annotations feature
run_lint nvca-operator --values "${repo_root}/test/test-custom-annotations.yaml" --set "ngcConfig.serviceKey=fakekey"

# Test both features together
run_lint nvca-operator \
  --set "agent.secretMirror.sourceNamespace=custom-ns" \
  --set "agent.secretMirror.labelSelector=mirror=true" \
  --values "${repo_root}/test/test-custom-annotations.yaml" \
  --set "ngcConfig.serviceKey=fakekey"

# Test network policies feature
run_lint nvca-operator --values "${repo_root}/test/test-network-policies.yaml" --set "ngcConfig.serviceKey=fakekey"

# Test network policies with annotations
run_lint nvca-operator --values "${repo_root}/test/test-network-policies.yaml" --values "${repo_root}/test/test-custom-annotations.yaml" --set "ngcConfig.serviceKey=fakekey"

# Test ConfigMaps contain expected structure when custom values provided
echo "Testing ConfigMap structure with custom values..."
helm template test-release "${repo_root}/deployments/nvca-operator" \
  --values "${repo_root}/test/test-network-policies.yaml" \
  --set "ngcConfig.serviceKey=fakekey" \
  --show-only templates/custom-network-policies-configmap.yaml \
  | grep -q "nvcf-custom-network-policies" && echo "✓ Network policies ConfigMap created" || echo "✗ Network policies ConfigMap missing"

helm template test-release "${repo_root}/deployments/nvca-operator" \
  --values "${repo_root}/test/test-custom-annotations.yaml" \
  --set "ngcConfig.serviceKey=fakekey" \
  --show-only templates/custom-annotations-configmap.yaml \
  | grep -q "nvca-namespace-pod-annotations" && echo "✓ Custom annotations ConfigMap created" || echo "✗ Custom annotations ConfigMap missing"

# Test ConfigMaps are created even without custom values (always created behavior)
echo "Testing ConfigMaps are always created..."
helm template test-release "${repo_root}/deployments/nvca-operator" \
  --set "ngcConfig.serviceKey=fakekey" \
  --show-only templates/custom-annotations-configmap.yaml \
  | grep -q "nvca-namespace-pod-annotations" && echo "✓ Annotations ConfigMap always created" || echo "✗ Annotations ConfigMap not created"

helm template test-release "${repo_root}/deployments/nvca-operator" \
  --set "ngcConfig.serviceKey=fakekey" \
  --show-only templates/custom-network-policies-configmap.yaml \
  | grep -q "nvcf-custom-network-policies" && echo "✓ Network policies ConfigMap always created" || echo "✗ Network policies ConfigMap not created"
