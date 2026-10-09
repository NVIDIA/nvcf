#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

helm template api-keys "$repo_root/deploy/helm/api-keys-colocated/api-keys" -n api-keys \
  --set apikeys.image.registry=example.com \
  --set apikeys.image.repository=api-keys >"$work_dir/api-keys.yaml"

selector='select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext'
test "$(yq -r "$selector | .runAsNonRoot" "$work_dir/api-keys.yaml")" = true
test "$(yq -r "$selector | .runAsUser" "$work_dir/api-keys.yaml")" = 1000
test "$(yq -r "$selector | .runAsGroup" "$work_dir/api-keys.yaml")" = 1000
test "$(yq -r "$selector | .allowPrivilegeEscalation" "$work_dir/api-keys.yaml")" = false
test "$(yq -r "$selector | .capabilities.drop[]" "$work_dir/api-keys.yaml")" = ALL

echo "nonroot-api-keys: all checks passed"
