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
# Runs controller-gen for the gds-mcs-controller ClusterRole, from the
# +kubebuilder:rbac markers under internal/mcs. The controller defines no
# CRDs of its own: ServiceExport and ServiceImport come from
# sigs.k8s.io/mcs-api config/crd and ClusterProperty is kept by hand in
# config/crd/bases.
#
# Usage:
#   hack/codegen.sh          write config/rbac/role.yaml in place
#   hack/codegen.sh --check  generate into a temporary directory and fail when
#                            the checked-in file differs
set -euo pipefail

module_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${module_dir}"

: "${GO:=go}"
: "${CONTROLLER_TOOLS_VERSION:=v0.19.0}"
: "${CONTROLLER_GEN:=${GO} run sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_TOOLS_VERSION}}"
export GOWORK=off

readonly rbac_dir="config/rbac"

# controller-gen writes YAML without a license header; replace its leading
# document marker with the two SPDX lines other manifests carry.
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

# generate RBAC_OUT
generate() {
    mkdir -p "$1"
    ${CONTROLLER_GEN} rbac:roleName=gds-mcs-controller paths=./internal/mcs/... "output:rbac:dir=$1"
    add_yaml_headers "$1"
}

case "${1:-}" in
    "")
        rm -f "${rbac_dir}/role.yaml"
        generate "${rbac_dir}"
        ;;
    --check)
        tmp="$(mktemp -d)"
        trap 'rm -rf "${tmp}"' EXIT
        generate "${tmp}/rbac"
        if ! diff -ru "${rbac_dir}" "${tmp}/rbac"; then
            echo "error: config/rbac is stale; run 'make codegen-update' and commit the result" >&2
            exit 1
        fi
        echo "codegen: generated files are up to date"
        ;;
    *)
        echo "usage: $0 [--check]" >&2
        exit 2
        ;;
esac
