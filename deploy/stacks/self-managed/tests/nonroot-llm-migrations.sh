#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

helm template llm "$repo_root/deploy/helm/llm-request-router/llm-request-router" -n nvcf \
  --set llmRequestRouter.image.registry=example.com \
  --set llmRequestRouter.image.repository=llm \
  --set llmRequestRouter.pki.enabled=true \
  --set llmRequestRouter.pki.allowedDomains=cluster.local \
  --set llmRequestRouter.pki.image.registry=example.com \
  --set llmRequestRouter.pki.image.repository=nvcf-openbao-migrations \
  --set llmRequestRouter.pki.image.tag=test >"$work_dir/llm.yaml"

selector='select(.kind == "Job" and .metadata.name == "addons-llm-migrations")'
test "$(yq -r "$selector | .spec.template.spec.securityContext.runAsUser" "$work_dir/llm.yaml")" = 100
test "$(yq -r "$selector | .spec.template.spec.containers[0].securityContext.runAsNonRoot" "$work_dir/llm.yaml")" = true

echo "nonroot-llm-migrations: all checks passed"
