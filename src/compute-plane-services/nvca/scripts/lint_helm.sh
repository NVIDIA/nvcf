#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


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

set -euo pipefail

install_kubeconform
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
