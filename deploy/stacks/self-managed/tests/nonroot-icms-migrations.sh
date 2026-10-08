#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

helm template sis "$repo_root/deploy/helm/icms/icms-api" -n sis \
  --set sis.image.registry=example.com \
  --set sis.image.repository=sis \
  --set sis.lls.enabled=true \
  --set sis.lls.hmacRotation.image.registry=example.com \
  --set sis.lls.hmacRotation.image.repository=nvcf-openbao-migrations \
  --set sis.lls.hmacRotation.image.tag=test >"$work_dir/sis.yaml"

selector='select(.kind == "Job" and .metadata.name == "addons-lls-migrations")'
test "$(yq -r "$selector | .spec.template.spec.securityContext.runAsUser" "$work_dir/sis.yaml")" = 100
test "$(yq -r "$selector | .spec.template.spec.containers[0].securityContext.runAsNonRoot" "$work_dir/sis.yaml")" = true

echo "nonroot-icms-migrations: all checks passed"
