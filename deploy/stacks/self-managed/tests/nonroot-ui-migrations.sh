#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

helm template ui "$repo_root/src/uis/nvcf-ui/helm" -n nvcf \
  --set nvcfUi.image.registry=example.com \
  --set nvcfUi.openbaoMigrations.image.registry=example.com >"$work_dir/ui.yaml"

selector='select(.kind == "Job" and .metadata.name == "ui-helm-nvcf-ui-openbao-migrations")'
test "$(yq -r "$selector | .spec.template.spec.securityContext.runAsUser" "$work_dir/ui.yaml")" = 100
test "$(yq -r "$selector | .spec.template.spec.containers[0].securityContext.runAsNonRoot" "$work_dir/ui.yaml")" = true

echo "nonroot-ui-migrations: all checks passed"
