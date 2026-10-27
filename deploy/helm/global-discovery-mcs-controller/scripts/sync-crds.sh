#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Copy the CRDs gds-mcs-controller needs into the chart templates.
#
# The global-discovery module is the source of truth: it embeds the
# ServiceExport and ServiceImport CRDs of sigs.k8s.io/mcs-api (the version in
# its go.mod) and keeps the ClusterProperty CRD in config/crd/bases. This
# script is the only writer of templates/crds/. It keeps each CRD body byte
# for byte and adds only the chart wrapping: the crds.install gate, the chart
# labels and helm.sh/resource-policy: keep.
#
# Usage:
#   scripts/sync-crds.sh                   rewrite templates/crds/
#   scripts/sync-crds.sh --check           fail when templates/crds/ differs
#                                          from the module. Without Go or the
#                                          mcs-api module, compares the
#                                          ClusterProperty CRD and the mcs-api
#                                          version only, and says so.
#   scripts/sync-crds.sh --check --strict  as --check, but fail when the
#                                          mcs-api module cannot be read
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_root="$(cd "${script_dir}/.." && pwd)"
repo_root="$(cd "${chart_root}/../../.." && pwd)"
module_rel="src/control-plane-services/global-discovery"
module_dir="${MODULE_DIR:-${repo_root}/${module_rel}}"
crd_dir="${chart_root}/global-discovery-mcs-controller/templates/crds"
mcs_module="sigs.k8s.io/mcs-api"
cp_rel="config/crd/bases/about.k8s.io_clusterproperties.yaml"
mcs_files="multicluster.x-k8s.io_serviceexports.yaml multicluster.x-k8s.io_serviceimports.yaml"

mode="write"
strict=0
for arg in "$@"; do
    case "${arg}" in
        --check) mode="check" ;;
        --strict) strict=1 ;;
        -h|--help)
            echo "usage: sync-crds.sh [--check [--strict]]"
            exit 0
            ;;
        *) echo "sync-crds.sh: unknown argument '${arg}'" >&2; exit 2 ;;
    esac
done

mcs_version="$(awk -v m="${mcs_module}" '$1 == m {print $2; exit} $1 == "require" && $2 == m {print $3; exit}' "${module_dir}/go.mod")"
[ -n "${mcs_version}" ] || { echo "sync-crds.sh: ${mcs_module} not found in ${module_rel}/go.mod" >&2; exit 1; }

# mcs_dir: the mcs-api module directory in the Go module cache, downloaded
# when missing. Empty when Go is not installed or the download fails.
mcs_dir() {
    command -v go >/dev/null 2>&1 || return 0
    local cache dir
    cache="$(cd "${module_dir}" && GOWORK=off go env GOMODCACHE 2>/dev/null)" || return 0
    dir="${cache}/${mcs_module}@${mcs_version}"
    if [ ! -d "${dir}/config/crd" ]; then
        dir="$(cd "${module_dir}" && GOWORK=off go mod download -json "${mcs_module}@${mcs_version}" 2>/dev/null |
            sed -n 's/^[[:space:]]*"Dir": "\(.*\)",*$/\1/p')" || dir=""
    fi
    [ -n "${dir}" ] && [ -d "${dir}/config/crd" ] && printf '%s' "${dir}"
    return 0
}

nvidia_header() {  # nvidia_header <source description>
    cat <<EOF
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Copied from $1
# by scripts/sync-crds.sh. Do not edit; run make sync-crds.
EOF
}

# upstream_header <file> <source description>: the upstream license block
# kept verbatim, with SPDX lines for its copyright holder in front.
upstream_header() {
    local holder
    holder="$(sed -n 's/^# \(Copyright .*\)$/\1/p' "$1" | head -n 1)"
    [ -n "${holder}" ] || { echo "sync-crds.sh: $1 has no Copyright line" >&2; exit 1; }
    printf '# SPDX-FileCopyrightText: %s\n# SPDX-License-Identifier: Apache-2.0\n#\n' "${holder}"
    leading_comments "$1"
    printf '#\n# Copied from %s\n# by scripts/sync-crds.sh. Do not edit; run make sync-crds.\n' "$2"
}

leading_comments() {
    awk '/^#/ { print; next } { exit }' "$1"
}

# body <file>: the CRD without its leading comment block, with the chart
# labels and the keep policy merged into the top-level metadata.
body() {
    awk '
        !started && (/^#/ || /^[[:space:]]*$/ || /^---[[:space:]]*$/) { next }
        { started = 1 }
        /^metadata:[[:space:]]*$/ && !done { print; inmeta = 1; next }
        inmeta && /^[^ #]/ {
            if (!labeled) {
                print "  labels:"
                print "    {{- include \"gds-mcs-controller.labels\" . | nindent 4 }}"
            }
            if (!kept) {
                print "  annotations:"
                print "    helm.sh/resource-policy: keep"
            }
            inmeta = 0
            done = 1
        }
        inmeta && /^  labels:[[:space:]]*$/ {
            print
            print "    {{- include \"gds-mcs-controller.labels\" . | nindent 4 }}"
            labeled = 1
            next
        }
        inmeta && /^  annotations:[[:space:]]*$/ {
            print
            print "    helm.sh/resource-policy: keep"
            kept = 1
            next
        }
        { print }
    ' "$1"
}

render() {  # render <source file> <header function> <source description>
    if grep -qF -e '{{' -e '}}' "$1"; then
        echo "sync-crds.sh: $1 contains {{ or }}; escape them before copying" >&2
        exit 1
    fi
    echo '{{- if .Values.crds.install }}'
    "$2" "$1" "$3"
    body "$1"
    echo '{{- end }}'
}

nvidia_header_for() {  # adapter: nvidia_header ignores the file argument
    nvidia_header "$2"
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

cp_file="$(basename "${cp_rel}")"
render "${module_dir}/${cp_rel}" nvidia_header_for "${module_rel}/${cp_rel}" >"${tmp_dir}/${cp_file}"

dir="$(mcs_dir)"
if [ -n "${dir}" ]; then
    for f in ${mcs_files}; do
        render "${dir}/config/crd/${f}" upstream_header "${mcs_module}@${mcs_version}/config/crd/${f}" >"${tmp_dir}/${f}"
    done
fi

for f in "${tmp_dir}"/*.yaml; do
    grep -q 'gds-mcs-controller.labels' "${f}" ||
        { echo "sync-crds.sh: $(basename "${f}") has no top-level metadata block" >&2; exit 1; }
done

if [ "${mode}" = "write" ]; then
    [ -n "${dir}" ] || { echo "sync-crds.sh: cannot read ${mcs_module} ${mcs_version}; install Go and run from a checkout with network access" >&2; exit 1; }
    mkdir -p "${crd_dir}"
    cp "${tmp_dir}"/*.yaml "${crd_dir}/"
    echo "sync-crds.sh: wrote ${crd_dir#"${repo_root}/"} (${mcs_module} ${mcs_version} and ${cp_rel})"
    exit 0
fi

drift=0
check() {  # check <generated> <committed>
    if [ ! -f "$2" ]; then
        echo "sync-crds.sh: ${2#"${repo_root}/"} is missing" >&2
        drift=1
        return
    fi
    if ! diff -u "$2" "$1" >&2; then
        drift=1
    fi
}
for f in "${tmp_dir}"/*.yaml; do
    check "${f}" "${crd_dir}/$(basename "${f}")"
done

if [ -z "${dir}" ]; then
    if [ "${strict}" = "1" ]; then
        echo "sync-crds.sh: cannot read ${mcs_module} ${mcs_version} (Go missing or download failed)" >&2
        exit 1
    fi
    for f in ${mcs_files}; do
        if ! grep -qF "Copied from ${mcs_module}@${mcs_version}/config/crd/${f}" "${crd_dir}/${f}" 2>/dev/null; then
            echo "sync-crds.sh: ${f} is not the ${mcs_module} ${mcs_version} copy that ${module_rel}/go.mod requires" >&2
            drift=1
        fi
    done
    echo "sync-crds.sh: note: Go or the ${mcs_module} module is not available; compared the mcs-api version only" >&2
fi

if [ "${drift}" -ne 0 ]; then
    echo "sync-crds.sh: templates/crds differs from ${module_rel}. Run make -C deploy/helm/global-discovery-mcs-controller sync-crds." >&2
    exit 1
fi
echo "sync-crds.sh: templates/crds matches ${module_rel} (${mcs_module} ${mcs_version})"
