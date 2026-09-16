#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render regression tests for the configurable PodDisruptionBudget.
# Run from the chart subtree:
#   bash tests/render-pdb-test.sh
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

render() {
  local label="$1"
  shift
  helm template test "$CHART_DIR" "$@" >"$TMP_DIR/$label.yaml" ||
    fail "$label: helm template failed"
}

pdb_count() {
  local file="$1"
  yq -r 'select(.kind == "PodDisruptionBudget") | .kind' "$file" |
    grep -c '^PodDisruptionBudget$' || true
}

assert_pdb_count() {
  local label="$1"
  local expected="$2"
  local actual
  actual="$(pdb_count "$TMP_DIR/$label.yaml")"
  test "$actual" = "$expected" ||
    fail "$label: expected $expected PodDisruptionBudget resources, found $actual"
}

assert_pdb_value() {
  local label="$1"
  local expression="$2"
  local message="$3"
  yq -e "select(.kind == \"PodDisruptionBudget\") | $expression" \
    "$TMP_DIR/$label.yaml" >/dev/null || fail "$label: $message"
}

render default
assert_pdb_count default 0

render disabled-multiple-replicas \
  --set replicaCount=3 \
  --set podDisruptionBudget.enabled=false
assert_pdb_count disabled-multiple-replicas 0

render min-available \
  --set podDisruptionBudget.enabled=true \
  --set podDisruptionBudget.minAvailable=2
assert_pdb_count min-available 1
assert_pdb_value min-available \
  '.spec.minAvailable == 2 and (.spec | has("maxUnavailable") | not)' \
  'expected only minAvailable: 2'

render max-unavailable \
  --set podDisruptionBudget.enabled=true \
  --set-string podDisruptionBudget.maxUnavailable=25%
assert_pdb_count max-unavailable 1
assert_pdb_value max-unavailable \
  '.spec.maxUnavailable == "25%" and (.spec | has("minAvailable") | not)' \
  'expected only maxUnavailable: "25%"'

render fallback \
  --set podDisruptionBudget.enabled=true
assert_pdb_count fallback 1
assert_pdb_value fallback \
  '.spec.minAvailable == "50%" and (.spec | has("maxUnavailable") | not)' \
  'expected only the minAvailable: "50%" fallback'

render zero \
  --set podDisruptionBudget.enabled=true \
  --set podDisruptionBudget.minAvailable=0
assert_pdb_count zero 1
assert_pdb_value zero \
  '.spec.minAvailable == 0 and (.spec | has("maxUnavailable") | not)' \
  'expected minAvailable: 0 instead of the fallback'

if helm template test "$CHART_DIR" \
  --set podDisruptionBudget.enabled=true \
  --set podDisruptionBudget.minAvailable=1 \
  --set podDisruptionBudget.maxUnavailable=1 \
  >"$TMP_DIR/both.yaml" 2>"$TMP_DIR/both.log"; then
  fail 'both: expected helm template to reject both availability fields'
fi
grep -Fq 'podDisruptionBudget: set only one of minAvailable or maxUnavailable' \
  "$TMP_DIR/both.log" || fail 'both: expected validation error was not reported'

echo "PASS: PodDisruptionBudget render tests"
