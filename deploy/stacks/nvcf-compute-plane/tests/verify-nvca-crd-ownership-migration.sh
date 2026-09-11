#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
helper="$stack_dir/renderers/adopt-nvca-crd-ownership.sh"
work_dir="$(mktemp -d)"
calls_file="$work_dir/kubectl-calls"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-nvca-crd-ownership-migration: $*" >&2
  exit 1
}

test -x "$helper" || fail "helper is not executable: $helper"

mkdir -p "$work_dir/bin"
cat >"$work_dir/bin/kubectl" <<'KUBECTL'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"$KUBECTL_CALLS"

args=("$@")
mode=""
resource=""
name=""
output=""

for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[$i]}" in
    get|annotate|label)
      mode="${args[$i]}"
      resource="${args[$((i + 1))]:-}"
      name="${args[$((i + 2))]:-}"
      ;;
    -o)
      output="${args[$((i + 1))]:-}"
      ;;
  esac
done

[[ "$resource" == "customresourcedefinition" ]] || exit 0
[[ "$name" == "nvcfbackends.nvcf.nvidia.io" ]] || exit 0

if [[ "$mode" == "get" && -z "$output" ]]; then
  [[ "${KUBECTL_SCENARIO:-}" != "missing" ]] || exit 1
  exit 0
fi

if [[ "$mode" == "get" && "$output" == jsonpath=* ]]; then
  case "${KUBECTL_SCENARIO:-}" in
    legacy)
      case "$output" in
        *release-namespace*) printf 'nvca-operator' ;;
        *release-name*) printf 'nvca-operator' ;;
        *managed-by*) printf 'Helm' ;;
      esac
      ;;
    named-legacy)
      case "$output" in
        *release-namespace*) printf 'plane-a-nvca-operator' ;;
        *release-name*) printf 'nvca-operator' ;;
        *managed-by*) printf 'Helm' ;;
      esac
      ;;
    shared)
      case "$output" in
        *release-namespace*) printf 'nvcf-shared' ;;
        *release-name*) printf 'nvcf-nvca-crds' ;;
        *managed-by*) printf 'Helm' ;;
      esac
      ;;
    foreign)
      case "$output" in
        *release-namespace*) printf 'other-namespace' ;;
        *release-name*) printf 'other-release' ;;
        *managed-by*) printf 'Helm' ;;
      esac
      ;;
    unowned)
      ;;
    *)
      exit 1
      ;;
  esac
  exit 0
fi

if [[ "$mode" == "annotate" || "$mode" == "label" ]]; then
  exit 0
fi

exit 0
KUBECTL
chmod +x "$work_dir/bin/kubectl"

run_helper() {
  local scenario="$1"
  shift
  : >"$calls_file"
  PATH="$work_dir/bin:$PATH" \
    KUBECTL_CALLS="$calls_file" \
    KUBECTL_SCENARIO="$scenario" \
    "$helper" "$@"
}

assert_called() {
  local expected="$1"
  grep -Fq -- "$expected" "$calls_file" || fail "missing kubectl call: $expected"
}

assert_not_called() {
  local unexpected="$1"
  if grep -Fq -- "$unexpected" "$calls_file"; then
    fail "unexpected kubectl call: $unexpected"
  fi
}

run_helper missing >/dev/null
assert_called "get customresourcedefinition nvcfbackends.nvcf.nvidia.io"
assert_not_called "annotate customresourcedefinition nvcfbackends.nvcf.nvidia.io"

run_helper legacy --kubeconfig /tmp/kubeconfig --context admin@gpu >/dev/null
assert_called "--kubeconfig /tmp/kubeconfig --context admin@gpu annotate customresourcedefinition nvcfbackends.nvcf.nvidia.io helm.sh/resource-policy=keep meta.helm.sh/release-name=nvcf-nvca-crds meta.helm.sh/release-namespace=nvcf-shared --overwrite"
assert_called "--kubeconfig /tmp/kubeconfig --context admin@gpu label customresourcedefinition nvcfbackends.nvcf.nvidia.io app.kubernetes.io/managed-by=Helm nvcf.nvidia.com/control-plane-owner=shared --overwrite"

run_helper named-legacy >/dev/null
assert_called "annotate customresourcedefinition nvcfbackends.nvcf.nvidia.io helm.sh/resource-policy=keep meta.helm.sh/release-name=nvcf-nvca-crds meta.helm.sh/release-namespace=nvcf-shared --overwrite"
assert_called "label customresourcedefinition nvcfbackends.nvcf.nvidia.io app.kubernetes.io/managed-by=Helm nvcf.nvidia.com/control-plane-owner=shared --overwrite"

run_helper shared >/dev/null
assert_called "annotate customresourcedefinition nvcfbackends.nvcf.nvidia.io helm.sh/resource-policy=keep meta.helm.sh/release-name=nvcf-nvca-crds meta.helm.sh/release-namespace=nvcf-shared --overwrite"

run_helper unowned >/dev/null
assert_called "label customresourcedefinition nvcfbackends.nvcf.nvidia.io app.kubernetes.io/managed-by=Helm nvcf.nvidia.com/control-plane-owner=shared --overwrite"

err="$work_dir/foreign.err"
if run_helper foreign >"$work_dir/foreign.out" 2>"$err"; then
  fail "foreign-owned CRD should have been refused"
fi
grep -Fq -- "other-release" "$err" || fail "foreign owner error did not name the release"
assert_not_called "annotate customresourcedefinition nvcfbackends.nvcf.nvidia.io"

echo "verify-nvca-crd-ownership-migration: all checks passed"
