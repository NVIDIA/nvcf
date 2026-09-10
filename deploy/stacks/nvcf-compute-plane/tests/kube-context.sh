#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

cp "${stack_dir}/Makefile.dist" "${test_dir}/Makefile"
mkdir -p "${test_dir}/registration"
printf 'clusterID: generated-id\n' > "${test_dir}/registration/gpu-a-register-values.yaml"

for target in template install apply destroy; do
  output="$({
    make -n -C "${test_dir}" "${target}" \
      DEV_MODE=1 \
      CLUSTER_NAME=gpu-a \
      HELMFILE_ENV=local \
      KUBECONFIG_FILE=/tmp/gpu-kubeconfig \
      COMPUTE_KUBE_CONTEXT='compute context'
  } 2>&1)"
  if ! grep -Fq -- '--kube-context "compute context"' <<<"${output}"; then
    printf '%s did not pass the selected context to Helmfile:\n%s\n' "${target}" "${output}" >&2
    exit 1
  fi
done

destroy_output="$({
  make -n -C "${test_dir}" destroy \
    DEV_MODE=1 \
    CLUSTER_NAME=gpu-a \
    HELMFILE_ENV=local \
    KUBECONFIG_FILE=/tmp/gpu-kubeconfig \
    COMPUTE_KUBE_CONTEXT='compute context'
} 2>&1)"
if ! grep -Fq -- 'kubectl --kubeconfig /tmp/gpu-kubeconfig --context "compute context"' <<<"${destroy_output}"; then
  printf 'destroy did not preserve the selected context for namespace cleanup:\n%s\n' "${destroy_output}" >&2
  exit 1
fi

unset_output="$({
  make -n -C "${test_dir}" install \
    DEV_MODE=1 \
    CLUSTER_NAME=gpu-a \
    HELMFILE_ENV=local \
    KUBECONFIG_FILE=/tmp/gpu-kubeconfig
} 2>&1)"
if grep -Eq -- '--kube-context|--context' <<<"${unset_output}"; then
  printf 'install emitted a context flag when COMPUTE_KUBE_CONTEXT was unset:\n%s\n' "${unset_output}" >&2
  exit 1
fi

echo 'kube-context: all checks passed'
