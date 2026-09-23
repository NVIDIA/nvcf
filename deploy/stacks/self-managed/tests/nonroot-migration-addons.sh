#!/usr/bin/env bash
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

helm template llm "$repo_root/deploy/helm/llm-request-router/llm-request-router" -n nvcf \
  --set llmRequestRouter.image.registry=example.com \
  --set llmRequestRouter.image.repository=llm \
  --set llmRequestRouter.pki.enabled=true \
  --set llmRequestRouter.pki.allowedDomains=cluster.local \
  --set llmRequestRouter.pki.image.registry=example.com \
  --set llmRequestRouter.pki.image.repository=nvcf-openbao-migrations \
  --set llmRequestRouter.pki.image.tag=test >"$work_dir/llm.yaml"

helm template ui "$repo_root/src/uis/nvcf-ui/helm" -n nvcf \
  --set nvcfUi.image.registry=example.com \
  --set nvcfUi.openbaoMigrations.image.registry=example.com >"$work_dir/ui.yaml"

for pair in sis:addons-lls-migrations llm:addons-llm-migrations ui:ui-helm-nvcf-ui-openbao-migrations; do
  file="${pair%%:*}"
  name="${pair#*:}"
  selector="select(.kind == \"Job\" and .metadata.name == \"$name\")"
  test "$(yq -r "$selector | .spec.template.spec.securityContext.runAsUser" "$work_dir/$file.yaml")" = 100
  test "$(yq -r "$selector | .spec.template.spec.containers[0].securityContext.runAsNonRoot" "$work_dir/$file.yaml")" = true
done

echo "nonroot-migration-addons: all checks passed"
