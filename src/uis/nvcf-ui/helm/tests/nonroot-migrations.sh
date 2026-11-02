#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

helm template ui "$repo_root/src/uis/nvcf-ui/helm" -n nvcf \
  --set nvcfUi.image.registry=example.com \
  --set nvcfUi.openbaoMigrations.image.registry=example.com >"$work_dir/ui.yaml"

selector='select(.kind == "Job" and .metadata.name == "ui-helm-nvcf-ui-openbao-migrations")'
test "$(yq -r "$selector | .spec.template.spec.securityContext.runAsNonRoot" "$work_dir/ui.yaml")" = true
test "$(yq -r "$selector | .spec.template.spec.securityContext.runAsUser" "$work_dir/ui.yaml")" = 100
test "$(yq -r "$selector | .spec.template.spec.securityContext.runAsGroup" "$work_dir/ui.yaml")" = 1000
test "$(yq -r "$selector | .spec.template.spec.containers[0].securityContext.runAsNonRoot" "$work_dir/ui.yaml")" = true
test "$(yq -r "$selector | .spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation" "$work_dir/ui.yaml")" = false
test "$(yq -r "$selector | .spec.template.spec.containers[0].securityContext.capabilities.drop[0]" "$work_dir/ui.yaml")" = ALL

chart="$repo_root/src/uis/nvcf-ui/helm"
container="$selector | .spec.template.spec.containers[0]"
image_repository="$(yq -r '.nvcfUi.openbaoMigrations.image.repository' "$chart/values.yaml")"
image_tag="$(yq -r '.nvcfUi.openbaoMigrations.image.tag' "$chart/values.yaml")"
test -n "$image_tag" && test "$image_tag" != null
test "$(yq -rN "$container | .image" "$work_dir/ui.yaml")" = "example.com/$image_repository:$image_tag"
test -z "$(yq -rN "$container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "$work_dir/ui.yaml")"

render_migration_case() {
  helm template ui "${migration_chart:-$chart}" -n nvcf "$@" > "$work_dir/migration.yaml"
}

# Older saved values can omit the selector; null is also removed by Helm.
cp -R "$chart" "$work_dir/legacy-chart"
yq -i 'del(.nvcfUi.openbaoMigrations.kubectlMinor)' "$work_dir/legacy-chart/values.yaml"
migration_chart="$work_dir/legacy-chart" render_migration_case
test -z "$(yq -rN "$container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "$work_dir/migration.yaml")"
render_migration_case --set nvcfUi.openbaoMigrations.kubectlMinor=null
test -z "$(yq -rN "$container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "$work_dir/migration.yaml")"
for invalid in false 0; do
  if render_migration_case --set "nvcfUi.openbaoMigrations.kubectlMinor=$invalid" 2> "$work_dir/invalid-client.err"; then
    echo "FAIL: unsupported falsy migration client selector rendered" >&2
    exit 1
  fi
  grep -Fq 'nvcfUi.openbaoMigrations.kubectlMinor must be empty, 1.36, or 1.37' "$work_dir/invalid-client.err"
done

for minor in 1.36 1.37; do
  render_migration_case \
    --set-string "nvcfUi.openbaoMigrations.kubectlMinor=$minor" \
    --set-string nvcfUi.openbaoMigrations.image.registry=registry.example.com \
    --set-string nvcfUi.openbaoMigrations.image.repository=mirror/openbao-migrations
  test "$(yq -rN "$container | .image" "$work_dir/migration.yaml")" = "registry.example.com/mirror/openbao-migrations:$image_tag"
  test "$(yq -rN "$container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "$work_dir/migration.yaml")" = "$minor"
  test "$(yq -rN "$container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value | tag" "$work_dir/migration.yaml")" = '!!str'
  test "$(yq -rN "$container | .env[] | select(.name == \"CORE_MIGRATIONS_ENABLED\") | .value" "$work_dir/migration.yaml")" = false
  test "$(yq -rN "$container | .env[] | select(.name == \"ADDONS_NVCF_UI_ENABLED\") | .value" "$work_dir/migration.yaml")" = true
done

# Preserve registry-free repositories and explicit image tag overrides.
render_migration_case \
  --set-string nvcfUi.openbaoMigrations.image.registry= \
  --set-string nvcfUi.openbaoMigrations.image.repository=mirror/openbao-migrations \
  --set-string nvcfUi.openbaoMigrations.image.tag=custom
test "$(yq -rN "$container | .image" "$work_dir/migration.yaml")" = mirror/openbao-migrations:custom

if render_migration_case --set-string nvcfUi.openbaoMigrations.kubectlMinor=1.35 \
  2> "$work_dir/invalid-client.err"; then
  echo "FAIL: unsupported migration client selector rendered" >&2
  exit 1
fi
grep -Fq 'nvcfUi.openbaoMigrations.kubectlMinor must be empty, 1.36, or 1.37' "$work_dir/invalid-client.err"

echo "nonroot-ui-migrations: all checks passed"
