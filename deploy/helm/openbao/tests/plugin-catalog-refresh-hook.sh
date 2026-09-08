#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
chart_source="${repo_root}/deploy/helm/openbao/helm"
values_file="${repo_root}/tools/ci/helm-validate-values/openbao.yaml"
test_root="$(mktemp -d)"
trap 'rm -rf "${test_root}"' EXIT

chart_dir="${test_root}/helm"
cp -R "${chart_source}" "${chart_dir}"
helm dependency build "${chart_dir}" >/dev/null
rendered="${test_root}/rendered.yaml"
helm template openbao "${chart_dir}" -f "${values_file}" >"${rendered}"

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
grep -q 'image: "example.com/nvcf/nvcf-openbao:2.5.5-nv-1.3.1"' "${hook_document}"
grep -q 'bao plugin register' "${hook_document}"

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
