#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
chart_source="${repo_root}/deploy/helm/openbao/helm"
values_file="${repo_root}/tools/ci/helm-validate-values/openbao.yaml"
openbao_tag="$(yq -r '.appVersion' "${chart_source}/Chart.yaml")"
migrations_tag="$(yq -r '.openbao.migrations.image.tag' "${chart_source}/values.yaml")"
upgrade_values="${repo_root}/deploy/helm/openbao/upgrade/values-upgrades.yaml"
test_root="$(mktemp -d)"
trap 'rm -rf "${test_root}"' EXIT

test "$(yq -r '.openbao.server.image.tag' "${upgrade_values}")" = "${openbao_tag}"
test "$(yq -r '.openbao.injector.agentImage.tag' "${upgrade_values}")" = "${openbao_tag}"

chart_dir="${test_root}/helm"
cp -R "${chart_source}" "${chart_dir}"
helm dependency build "${chart_dir}" >/dev/null
helm lint "${chart_dir}" -f "${values_file}"
rendered="${test_root}/rendered.yaml"
helm template openbao "${chart_dir}" -f "${values_file}" >"${rendered}"

for hook in openbao-server-initialize-cluster openbao-server-refresh-jwt-plugin-catalog openbao-server-migrations; do
  test "$(yq -r "select(.kind == \"Job\" and .metadata.name == \"$hook\") | .spec.template.spec.securityContext.runAsNonRoot" "${rendered}")" = true
  test "$(yq -r "select(.kind == \"Job\" and .metadata.name == \"$hook\") | .spec.template.spec.securityContext.runAsUser" "${rendered}")" = 100
done

# Helm upgrades using --reuse-values do not add newly introduced default
# subtrees. Rendering must remain compatible with persisted pre-change values.
legacy_values="${test_root}/legacy-values.yaml"
awk '
  /^    refreshJWTPluginCatalog:$/ { skip = 1; next }
  skip && /^  [a-zA-Z]/ { skip = 0 }
  !skip { print }
' "${chart_source}/values.yaml" >"${legacy_values}"
legacy_hook="${test_root}/legacy-refresh-hook.yaml"
helm template openbao "${chart_dir}" -f "${legacy_values}" \
  --set-json 'openbao.hooks.refreshJWTPluginCatalog=null' \
  --set openbao.hooks.migrations.resources.requests.cpu=99m \
  --set openbao.server.image.registry=example.com \
  --set openbao.server.image.repository=nvcf/nvcf-openbao \
  --set openbao.migrations.image.registry=example.com \
  --set openbao.migrations.image.repository=nvcf/nvcf-openbao-migrations \
  --show-only templates/hook-post-01-refresh-jwt-plugin-catalog.yaml \
  >"${legacy_hook}"
grep -q 'cpu: 99m' "${legacy_hook}"

hook_document="${test_root}/refresh-hook.yaml"
awk '
  /^---$/ {
    if (capture) exit
    document = ""
    capture = 0
    next
  }
  { document = document $0 ORS }
  /^  name: openbao-server-refresh-jwt-plugin-catalog$/ { capture = 1 }
  END { if (capture) printf "%s", document }
' "${rendered}" >"${hook_document}"

test -s "${hook_document}"
grep -q 'helm.sh/hook: post-upgrade' "${hook_document}"
grep -q 'helm.sh/hook-weight: "0"' "${hook_document}"
grep -Fq "image: \"example.com/nvcf/nvcf-openbao:${openbao_tag}\"" "${hook_document}"
grep -q 'bao plugin register' "${hook_document}"
grep -Fq "image: \"example.com/nvcf/nvcf-openbao-migrations:${migrations_tag}\"" "${rendered}"

if grep -q 'bao plugin reload' "${hook_document}"; then
  echo "plugin catalog refresh hook must not reload the plugin before pod rotation" >&2
  exit 1
fi

migrations_weight="$(awk '
  /^---$/ { in_migrations = 0 }
  /^  name: openbao-server-migrations$/ { in_migrations = 1 }
  in_migrations && /helm.sh\/hook-weight:/ { gsub(/"/, "", $2); print $2; exit }
' "${rendered}")"
test "${migrations_weight}" = "1"

echo "OpenBao plugin catalog refresh hook render checks passed"

# Package with unrelated metadata to catch accidental appVersion image fallback.
helm package "${chart_dir}" --version 99.0.0 --app-version 99.0.0 -d "${test_root}" >/dev/null
package="${test_root}/$(yq -r '.name' "${chart_source}/Chart.yaml")-99.0.0.tgz"
for minor in '' 1.36 1.37; do
  selector_args=()
  if [[ -n "${minor}" ]]; then
    selector_args=(--set-string "openbao.migrations.kubectlMinor=${minor}")
  fi
  helm template openbao "${package}" -f "${values_file}" \
    "${selector_args[@]}" >"${rendered}"
  for hook in openbao-server-initialize-cluster openbao-server-migrations; do
    container="select(.kind == \"Job\" and .metadata.name == \"${hook}\") | .spec.template.spec.containers[0]"
    test "$(yq -r "${container} | .image" "${rendered}")" = "example.com/nvcf/nvcf-openbao-migrations:${migrations_tag}"
    test "$(yq -r "${container} | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "${rendered}")" = "${minor}"
    if [[ -n "${minor}" ]]; then
      test "$(yq -r "${container} | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value | tag" "${rendered}")" = '!!str'
    fi
  done
  test "$(yq -r 'select(.kind == "StatefulSet") | .spec.template.spec.containers[] | select(.name == "openbao") | .image' "${rendered}")" = "example.com/nvcf/nvcf-openbao:${openbao_tag}"
  test "$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[].env[] | select(.name == "AGENT_INJECT_VAULT_IMAGE") | .value' "${rendered}" | sed 's/.*://')" = "${openbao_tag}"
done

# Consumer overrides still win in both migration-image consumers.
helm template openbao "${package}" -f "${values_file}" \
  --set-string openbao.migrations.kubectlMinor=1.36 \
  --set openbao.migrations.image.registry=mirror.example.com \
  --set openbao.migrations.image.repository=custom/migrations \
  --set openbao.migrations.image.tag=override >"${rendered}"
for hook in openbao-server-initialize-cluster openbao-server-migrations; do
  test "$(yq -r "select(.kind == \"Job\" and .metadata.name == \"${hook}\") | .spec.template.spec.containers[0].image" "${rendered}")" = mirror.example.com/custom/migrations:override
done
for invalid in 1.35 1.38 false; do
  if helm template openbao "${chart_dir}" -f "${values_file}" \
    --set-string "openbao.migrations.kubectlMinor=${invalid}" >"${test_root}/invalid.log" 2>&1; then
    echo "unsupported kubectl minor ${invalid} was accepted" >&2
    exit 1
  fi
  grep -Fq 'openbao.migrations.kubectlMinor must be empty, 1.36, or 1.37' "${test_root}/invalid.log"
done

echo "OpenBao migration client and packaged image checks passed"

# Retain legacy migration-only env settings, but reject conflicting controls.
legacy_env='openbao.migrations.env=[{"name":"NVCF_KUBECTL_MINOR","value":"1.36"}]'
helm template openbao "${chart_dir}" -f "${values_file}" \
  --set-json "${legacy_env}" >"${rendered}"
test "$(yq -r 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_KUBECTL_MINOR") | .value' "${rendered}")" = 1.36
if helm template openbao "${chart_dir}" -f "${values_file}" \
  --set-json "${legacy_env}" --set-string openbao.migrations.kubectlMinor=1.37 \
  >"${test_root}/conflict.log" 2>&1; then
  echo "conflicting kubectl selector controls were accepted" >&2
  exit 1
fi
grep -Fq 'set openbao.migrations.kubectlMinor or NVCF_KUBECTL_MINOR in openbao.migrations.env, not both' "${test_root}/conflict.log"
echo "OpenBao legacy client env and conflict checks passed"
