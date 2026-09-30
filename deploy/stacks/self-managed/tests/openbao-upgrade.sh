#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
cp -R "$stack_dir" "$work_dir/self-managed"
test_stack="$work_dir/self-managed"
cat > "$test_stack/environments/openbao-upgrade-test.yaml" <<'YAML'
ingress:
  gatewayApi:
    controllerNamespace: gateway-system
    gateways:
      shared:
        name: shared
        namespace: gateway-system
      grpc:
        name: grpc
        namespace: gateway-system
YAML
printf '{}\n' > "$test_stack/secrets/openbao-upgrade-test-secrets.yaml"
export HELMFILE_ENV=openbao-upgrade-test
export HELMFILE_CACHE_HOME="$work_dir/cache"
state="$test_stack/helmfile.d/01-dependencies.yaml.gotmpl"

write_values() {
  helmfile -f "$state" -l name=openbao-server "$@" write-values \
    --output-file-template "$work_dir/values.yaml" >/dev/null
}

write_values
test "$(yq -r '.openbao.server.image.tag' "$work_dir/values.yaml")" = '2.5.5-nv-1.3.1'
test "$(yq -r '.openbao.injector.agentImage.tag' "$work_dir/values.yaml")" = '2.5.5-nv-1.3.1'
test "$(yq -r '.openbao.migrations.image.tag' "$work_dir/values.yaml")" = '0.16.2'
cp "$work_dir/values.yaml" "$work_dir/default-values.yaml"

write_values \
  --state-values-set openbao.server.image.tag=2.5.5-nv-1.3.2 \
  --state-values-set openbao.migrations.image.tag=0.16.3 \
  --state-values-set openbao.injector.webhook.failurePolicy=Fail \
  --state-values-set openbao.injector.webhook.namespaceSelector.matchLabels.test=scope
test "$(yq -r '.openbao.server.image.tag' "$work_dir/values.yaml")" = '2.5.5-nv-1.3.2'
test "$(yq -r '.openbao.injector.agentImage.tag' "$work_dir/values.yaml")" = '2.5.5-nv-1.3.2'
test "$(yq -r '.openbao.migrations.image.tag' "$work_dir/values.yaml")" = '0.16.3'
test "$(yq -r '.openbao.server.extraContainers[] | select(.name == "auto-unseal-sidecar") | .image' "$work_dir/values.yaml")" = \
  'nvcr.io/YOUR_ORG/YOUR_TEAM/nvcf-openbao:2.5.5-nv-1.3.2'
test "$(yq -r '.openbao.injector.webhook.failurePolicy' "$work_dir/values.yaml")" = 'Fail'
test "$(yq -r '.openbao.injector.webhook.namespaceSelector.matchLabels.test' "$work_dir/values.yaml")" = 'scope'
echo 'OpenBao 0.6 image and webhook override render checks passed'

if [ -z "${NVCF_OPENBAO_CHART:-}" ]; then
  exit 0
fi

version="$(helmfile -f "$state" -l name=openbao-server list --skip-charts --output json | yq -r '.[0].version')"
test "$(helm show chart "$NVCF_OPENBAO_CHART" --version "$version" | yq -r '.version')" = "$version"
helm template openbao-server "$NVCF_OPENBAO_CHART" --version "$version" \
  --namespace vault-system -f "$work_dir/default-values.yaml" > "$work_dir/manifest.yaml"
yq 'select(.kind == "Job" and .metadata.name == "openbao-server-refresh-jwt-plugin-catalog")' \
  "$work_dir/manifest.yaml" > "$work_dir/hook.yaml"
test "$(yq -r '.metadata.annotations."helm.sh/hook"' "$work_dir/hook.yaml")" = 'post-upgrade'
test "$(yq -r '.metadata.annotations."helm.sh/hook-weight"' "$work_dir/hook.yaml")" = '0'
test "$(yq -r '.spec.template.spec.containers[0].image' "$work_dir/hook.yaml")" = \
  'nvcr.io/YOUR_ORG/YOUR_TEAM/nvcf-openbao:2.5.5-nv-1.3.1'
test "$(yq -r 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations") | .metadata.annotations."helm.sh/hook-weight"' "$work_dir/manifest.yaml")" = '1'
test "$(yq -r 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations") | .spec.template.spec.containers[0].image' "$work_dir/manifest.yaml")" = \
  'nvcr.io/YOUR_ORG/YOUR_TEAM/nvcf-openbao-migrations:0.16.2'
yq -r '.spec.template.spec.containers[0].args[0]' "$work_dir/hook.yaml" > "$work_dir/hook.sh"
grep -q 'bao plugin register' "$work_dir/hook.sh"
if grep -q 'bao plugin reload' "$work_dir/hook.sh"; then
  echo 'Catalog refresh must wait for server pod rotation before plugin reload' >&2
  exit 1
fi
echo "Published OpenBao $version refresh hook render checks passed"
