#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

expected_dir="$work_dir/expected"
actual_dir="$work_dir/actual"
mkdir -p "$expected_dir/release/templates" "$actual_dir/release/templates"

printf '%s\n' \
  'apiVersion: v1' \
  'kind: ConfigMap' \
  'metadata:' \
  '  name: stable' \
  >"$expected_dir/release/templates/configmap.yaml"

cp "$expected_dir/release/templates/configmap.yaml" \
  "$actual_dir/release/templates/configmap.yaml"

"$script_dir/compare-golden.sh" "$expected_dir" "$actual_dir" >/dev/null

printf '%s\n' \
  '  labels:' \
  '    changed: "true"' \
  >>"$actual_dir/release/templates/configmap.yaml"

if "$script_dir/compare-golden.sh" "$expected_dir" "$actual_dir" >/dev/null 2>&1; then
  echo "verify-golden-detects-change: compare-golden accepted a modified file" >&2
  exit 1
fi

echo "verify-golden-detects-change: all checks passed"
