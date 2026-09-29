#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
# Runs controller-gen for the deepcopy functions, the CRD and the RBAC role.
#
# Usage:
#   hack/codegen.sh          write generated files in place
#   hack/codegen.sh --check  generate into a temporary directory and fail when
#                            the checked-in files differ
#
# The check compares against the working tree rather than git state, so it
# gives the same answer before and after the files are committed.
set -euo pipefail

module_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${module_dir}"

: "${GO:=go}"
: "${CONTROLLER_TOOLS_VERSION:=v0.19.0}"
: "${CONTROLLER_GEN:=${GO} run sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_TOOLS_VERSION}}"

readonly object_dir="api/v1alpha1"
readonly crd_dir="config/crd/bases"
readonly rbac_dir="config/rbac"

# controller-gen writes YAML without a license header; replace its leading
# document marker with the two SPDX lines other CRDs in the repository carry.
add_yaml_headers() {
    local f
    for f in "$1"/*.yaml; do
        {
            printf '%s\n' \
                '# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.' \
                '# SPDX-License-Identifier: Apache-2.0'
            sed '1{/^---$/d;}' "${f}"
        } > "${f}.tmp"
        mv "${f}.tmp" "${f}"
    done
}

# generate OBJECT_OUT CRD_OUT RBAC_OUT
generate() {
    mkdir -p "$1" "$2" "$3"
    ${CONTROLLER_GEN} object:headerFile=hack/boilerplate.go.txt paths=./api/... "output:object:dir=$1"
    ${CONTROLLER_GEN} crd paths=./api/... "output:crd:dir=$2"
    ${CONTROLLER_GEN} rbac:roleName=pylon-operator paths=./internal/... "output:rbac:dir=$3"
    add_yaml_headers "$2"
    add_yaml_headers "$3"
}

case "${1:-}" in
    "")
        rm -f "${object_dir}/zz_generated.deepcopy.go" "${crd_dir}"/*.yaml "${rbac_dir}/role.yaml"
        generate "${object_dir}" "${crd_dir}" "${rbac_dir}"
        ;;
    --check)
        tmp="$(mktemp -d)"
        trap 'rm -rf "${tmp}"' EXIT
        generate "${tmp}/object" "${tmp}/crd" "${tmp}/rbac"
        status=0
        diff -u "${object_dir}/zz_generated.deepcopy.go" "${tmp}/object/zz_generated.deepcopy.go" || status=1
        diff -ru "${crd_dir}" "${tmp}/crd" || status=1
        diff -ru "${rbac_dir}" "${tmp}/rbac" || status=1
        if [[ "${status}" -ne 0 ]]; then
            echo "error: generated files are stale; run 'make codegen-update' and commit the result" >&2
            exit 1
        fi
        echo "codegen: generated files are up to date"
        ;;
    *)
        echo "usage: $0 [--check]" >&2
        exit 2
        ;;
esac
