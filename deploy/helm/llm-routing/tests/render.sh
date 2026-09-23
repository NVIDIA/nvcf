#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
chart=$(cd "$(dirname "$0")/.." && pwd)
output=$(mktemp)
trap 'rm -f "$output"' EXIT
helm dependency build --skip-refresh "$chart"
args=(--set gateway.llmApiGateway.image.repository=example/gateway
      --set router.llmRequestRouter.image.repository=example/stargate
      --set fixture.pylonImage=example/pylon)
if helm template test "$chart" "${args[@]}" > "$output" 2>&1; then
  echo "Expected development opt-in to be required" >&2
  exit 1
fi
grep -q 'developmentMode=true' "$output"
helm lint "$chart" -f "$chart/values.poc.yaml" "${args[@]}"
helm template test "$chart" -n test-routing -f "$chart/values.poc.yaml" "${args[@]}" > "$output"
test "$(grep -c '^kind: Deployment$' "$output")" -eq 5
if grep -Eq '^kind: (PersistentVolumeClaim|ClusterRole|Certificate)$|vault.hashicorp.com/agent-inject:' "$output"; then
  echo "Unexpected infrastructure or Vault injection in standalone stack" >&2
  exit 1
fi
helm template test "$chart" --set developmentMode=true "${args[@]}" > "$output"
test "$(grep -c '^kind: Deployment$' "$output")" -eq 3
echo 'PASS development opt-in, POC rendering, and three-component base rendering'
