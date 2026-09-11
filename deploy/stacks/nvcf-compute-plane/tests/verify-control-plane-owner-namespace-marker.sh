#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
helper="$stack_dir/renderers/mark-control-plane-namespaces.sh"
work_dir="$(mktemp -d)"
calls_file="$work_dir/kubectl-calls"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-control-plane-owner-namespace-marker: $*" >&2
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
namespace=""
output=""

for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[$i]}" in
    get)
      if [[ "${args[$((i + 1))]:-}" == "namespace" ]]; then
        mode="get"
        namespace="${args[$((i + 2))]:-}"
      fi
      ;;
    label|annotate)
      if [[ "${args[$((i + 1))]:-}" == "namespace" ]]; then
        mode="${args[$i]}"
        namespace="${args[$((i + 2))]:-}"
      fi
      ;;
    -o)
      output="${args[$((i + 1))]:-}"
      ;;
  esac
done

if [[ "$mode" == "get" && "$output" == jsonpath=* ]]; then
  case "$namespace" in
    plane-owned|primary-owned)
      printf 'plane-a'
      ;;
    foreign-owned|shared-foreign)
      printf 'plane-b'
      ;;
    shared-owned)
      printf 'shared'
      ;;
    primary-unlabeled|shared-unlabeled|missing)
      printf ''
      ;;
    *)
      printf ''
      ;;
  esac
  exit 0
fi

if [[ "$mode" == "get" ]]; then
  [[ "$namespace" != "missing" ]] || exit 1
  exit 0
fi

if [[ "$mode" == "label" || "$mode" == "annotate" ]]; then
  exit 0
fi

exit 0
KUBECTL
chmod +x "$work_dir/bin/kubectl"

run_helper() {
  : >"$calls_file"
  PATH="$work_dir/bin:$PATH" KUBECTL_CALLS="$calls_file" "$helper" "$@"
}

assert_called() {
  local expected="$1"
  grep -Fq -- "$expected" "$calls_file" || fail "missing kubectl call: $expected"
}

run_helper \
  --owner plane-a \
  --primary-namespace primary-unlabeled \
  --stack nvcf-compute-plane \
  --chart-version 1.21.8 \
  --nvca-operator-version 3.3.2 \
  --namespace primary-unlabeled \
  --namespace plane-owned \
  --namespace missing \
  --shared-namespace shared-unlabeled \
  --shared-namespace shared-owned
assert_called "label namespace primary-unlabeled nvcf.nvidia.com/control-plane-owner=plane-a --overwrite"
assert_called "annotate namespace primary-unlabeled nvcf.nvidia.com/control-plane-stack=nvcf-compute-plane nvcf.nvidia.com/control-plane-chart-version=1.21.8 nvcf.nvidia.com/nvca-operator-version=3.3.2 --overwrite"
assert_called "label namespace plane-owned nvcf.nvidia.com/control-plane-owner=plane-a --overwrite"
assert_called "label namespace shared-unlabeled nvcf.nvidia.com/control-plane-owner=shared --overwrite"
assert_called "label namespace shared-owned nvcf.nvidia.com/control-plane-owner=shared --overwrite"
if grep -Fq -- "label namespace missing" "$calls_file"; then
  fail "missing namespace should not be labelled"
fi

err="$work_dir/foreign.err"
if run_helper \
  --owner plane-a \
  --primary-namespace foreign-owned \
  --stack nvcf-compute-plane \
  --chart-version 1.21.8 \
  --nvca-operator-version 3.3.2 \
  2>"$err"; then
  fail "foreign-owned primary namespace should have been refused"
fi
grep -Fq -- "plane-b" "$err" || fail "foreign owner error did not name plane-b"

err="$work_dir/shared-foreign.err"
if run_helper \
  --owner plane-a \
  --primary-namespace primary-owned \
  --stack nvcf-compute-plane \
  --chart-version 1.21.8 \
  --nvca-operator-version 3.3.2 \
  --shared-namespace shared-foreign \
  2>"$err"; then
  fail "foreign-owned shared namespace should have been refused"
fi
grep -Fq -- "expected shared" "$err" || fail "shared namespace error did not name expected shared owner"

err="$work_dir/invalid.err"
if run_helper \
  --owner shared \
  --primary-namespace primary-owned \
  --stack nvcf-compute-plane \
  --chart-version 1.21.8 \
  --nvca-operator-version 3.3.2 \
  2>"$err"; then
  fail "shared should not be accepted as the control-plane owner"
fi
grep -Fq -- "owner must be default" "$err" || fail "invalid owner error did not explain owner validation"

run_helper \
  --owner plane-a \
  --primary-namespace primary-owned \
  --stack nvcf-compute-plane \
  --chart-version 1.21.8 \
  --nvca-operator-version 3.3.2 \
  --kubeconfig /tmp/kubeconfig \
  --context admin@gpu
assert_called "--kubeconfig /tmp/kubeconfig --context admin@gpu annotate namespace primary-owned"

echo "verify-control-plane-owner-namespace-marker: all checks passed"
