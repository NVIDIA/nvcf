#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Manual, unwired acceptance check for two self-managed NVCF control planes in
# one Kubernetes cluster. This script is intentionally opt-in until named
# control-plane mode is implemented and CI has enough capacity to run it.

set -euo pipefail

if [[ "${RUN_NVCF_TWO_PLANE_ACCEPTANCE:-}" != "1" ]]; then
  echo "two-plane acceptance is authored but unwired; set RUN_NVCF_TWO_PLANE_ACCEPTANCE=1 to run it"
  exit 0
fi

for tool in bash comm jq kubectl sort; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 127
  fi
done

KUBE_CONTEXT="${NVCF_TWO_PLANE_KUBE_CONTEXT:-k3d-ncp-local}"
OWNER_LABEL="nvcf.nvidia.com/control-plane-owner"
PLANE_A_ID="${NVCF_TWO_PLANE_A_ID:-plane-a}"
PLANE_B_ID="${NVCF_TWO_PLANE_B_ID:-plane-b}"
SHARED_OWNER="${NVCF_TWO_PLANE_SHARED_OWNER:-shared}"

: "${NVCF_TWO_PLANE_A_SMOKE_CMD:?set NVCF_TWO_PLANE_A_SMOKE_CMD to a command that invokes plane A successfully}"
: "${NVCF_TWO_PLANE_B_SMOKE_CMD:?set NVCF_TWO_PLANE_B_SMOKE_CMD to a command that invokes plane B successfully}"
: "${NVCF_TWO_PLANE_A_TEARDOWN_CMD:?set NVCF_TWO_PLANE_A_TEARDOWN_CMD to the command that removes plane A}"

fail() {
  echo "two-plane-isolation-acceptance: $*" >&2
  exit 1
}

run_step() {
  local label="$1" command="$2"

  echo ">>> $label"
  bash -lc "$command" || fail "$label failed"
}

namespace_count_for_owner() {
  local owner="$1"

  kubectl --context "$KUBE_CONTEXT" get namespaces \
    -l "$OWNER_LABEL=$owner" \
    -o json | jq '.items | length'
}

assert_owner_has_namespace() {
  local owner="$1" count

  count="$(namespace_count_for_owner "$owner")"
  [[ "$count" -gt 0 ]] || fail "expected at least one namespace labelled $OWNER_LABEL=$owner"
}

assert_owner_has_no_namespace() {
  local owner="$1" count

  count="$(namespace_count_for_owner "$owner")"
  [[ "$count" -eq 0 ]] || fail "expected no namespaces labelled $OWNER_LABEL=$owner after teardown; found $count"
}

assert_shared_survives() {
  local count

  count="$(namespace_count_for_owner "$SHARED_OWNER")"
  [[ "$count" -gt 0 ]] || fail "expected at least one shared namespace labelled $OWNER_LABEL=$SHARED_OWNER"
}

assert_owner_sets_are_disjoint() {
  local plane_a_names plane_b_names overlap

  plane_a_names="$(kubectl --context "$KUBE_CONTEXT" get namespaces \
    -l "$OWNER_LABEL=$PLANE_A_ID" \
    -o json | jq -r '.items[].metadata.name' | sort)"
  plane_b_names="$(kubectl --context "$KUBE_CONTEXT" get namespaces \
    -l "$OWNER_LABEL=$PLANE_B_ID" \
    -o json | jq -r '.items[].metadata.name' | sort)"
  overlap="$(comm -12 <(printf '%s\n' "$plane_a_names") <(printf '%s\n' "$plane_b_names"))"

  [[ -z "$overlap" ]] || fail "plane namespace ownership overlaps: $overlap"
}

kubectl --context "$KUBE_CONTEXT" cluster-info >/dev/null

assert_owner_has_namespace "$PLANE_A_ID"
assert_owner_has_namespace "$PLANE_B_ID"
assert_shared_survives
assert_owner_sets_are_disjoint

run_step "plane A smoke" "$NVCF_TWO_PLANE_A_SMOKE_CMD"
run_step "plane B smoke" "$NVCF_TWO_PLANE_B_SMOKE_CMD"
run_step "plane A teardown" "$NVCF_TWO_PLANE_A_TEARDOWN_CMD"

assert_owner_has_no_namespace "$PLANE_A_ID"
assert_owner_has_namespace "$PLANE_B_ID"
assert_shared_survives
run_step "plane B smoke after plane A teardown" "$NVCF_TWO_PLANE_B_SMOKE_CMD"

echo "two-plane-isolation-acceptance: all checks passed"
