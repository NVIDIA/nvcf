#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
helper="$stack_dir/renderers/delete-owned-namespace.sh"
work_dir="$(mktemp -d)"
calls_file="$work_dir/kubectl-calls"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-control-plane-owner-namespace-delete: $*" >&2
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
    delete)
      if [[ "${args[$((i + 1))]:-}" == "namespace" ]]; then
        mode="delete"
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
    default-owned)
      printf 'default'
      ;;
    plane-owned)
      printf 'plane-a'
      ;;
    foreign-owned)
      printf 'plane-b'
      ;;
    shared-owned)
      printf 'shared'
      ;;
    unlabeled)
      printf ''
      ;;
    missing)
      exit 1
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

if [[ "$mode" == "delete" ]]; then
  exit 0
fi

exit 0
KUBECTL
chmod +x "$work_dir/bin/kubectl"

run_helper() {
  : >"$calls_file"
  PATH="$work_dir/bin:$PATH" KUBECTL_CALLS="$calls_file" "$helper" "$@"
}

assert_deleted() {
  local namespace="$1"
  grep -Fq -- "delete namespace $namespace --wait=true" "$calls_file" ||
    fail "expected namespace $namespace to be deleted"
}

assert_not_deleted() {
  local namespace="$1"
  if grep -Fq -- "delete namespace $namespace --wait=true" "$calls_file"; then
    fail "expected namespace $namespace not to be deleted"
  fi
}

run_helper --owner default --namespace default-owned
assert_deleted default-owned

run_helper --owner default --namespace unlabeled
assert_deleted unlabeled

run_helper --owner default --namespace missing
assert_not_deleted missing

err="$work_dir/foreign.err"
if run_helper --owner default --namespace foreign-owned 2>"$err"; then
  fail "foreign-owned namespace should have been refused"
fi
assert_not_deleted foreign-owned
grep -Fq -- "plane-b" "$err" || fail "foreign owner error did not name plane-b"

err="$work_dir/shared.err"
if run_helper --owner default --namespace shared-owned 2>"$err"; then
  fail "shared-owned namespace should have been refused"
fi
assert_not_deleted shared-owned
grep -Fq -- "shared" "$err" || fail "shared owner error did not name shared"

err="$work_dir/unlabeled-named.err"
if run_helper --owner plane-a --namespace unlabeled 2>"$err"; then
  fail "named owner should not delete an unlabeled namespace"
fi
assert_not_deleted unlabeled
grep -Fq -- "missing nvcf.nvidia.com/control-plane-owner label" "$err" ||
  fail "unlabeled namespace error did not mention the missing owner label"

run_helper --owner plane-a --namespace plane-owned --kubeconfig /tmp/kubeconfig --context admin@gpu
assert_deleted plane-owned
grep -Fq -- "--kubeconfig /tmp/kubeconfig --context admin@gpu get namespace plane-owned" "$calls_file" ||
  fail "kubectl options were not passed through"

echo "verify-control-plane-owner-namespace-delete: all checks passed"
