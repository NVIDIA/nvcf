#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Copy the generated InferenceEndpoint CRD and ClusterRole from the operator
# module into the chart templates.
#
# The operator owns both files: controller-gen writes them under
# src/compute-plane-services/pylon-operator/config/ from the API types and RBAC
# markers. This script is the only writer of the two chart templates. It keeps
# the source body byte for byte and adds only the chart wrapping: the
# installCRDs gate, chart labels, the keep resource policy on the CRD and the
# release-scoped ClusterRole name.
#
# Usage:
#   scripts/sync-crd.sh          rewrite the chart templates
#   scripts/sync-crd.sh --check  fail when the templates differ from config/
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_root="$(cd "${script_dir}/.." && pwd)"
repo_root="$(cd "${chart_root}/../../.." && pwd)"
operator_rel="src/compute-plane-services/pylon-operator"
operator_dir="${OPERATOR_DIR:-${repo_root}/${operator_rel}}"
templates_dir="${chart_root}/pylon-operator/templates"

crd_rel="config/crd/bases/pylon.nvidia.com_inferenceendpoints.yaml"
role_rel="config/rbac/role.yaml"
crd_template="${templates_dir}/crds/pylon.nvidia.com_inferenceendpoints.yaml"
role_template="${templates_dir}/clusterrole.yaml"

mode="write"
case "${1:-}" in
    "") ;;
    --check) mode="check" ;;
    -h|--help)
        echo "usage: sync-crd.sh [--check]"
        exit 0
        ;;
    *) echo "sync-crd.sh: unknown argument '$1'" >&2; exit 2 ;;
esac

for rel in "${crd_rel}" "${role_rel}"; do
    if [ ! -f "${operator_dir}/${rel}" ]; then
        echo "sync-crd.sh: ${operator_dir}/${rel} not found" >&2
        exit 1
    fi
    # The files become Helm templates, so template delimiters in them would
    # be evaluated rather than copied.
    if grep -qF -e '{{' -e '}}' "${operator_dir}/${rel}"; then
        echo "sync-crd.sh: ${rel} contains {{ or }}; escape them before copying" >&2
        exit 1
    fi
done

header() {  # header <source path relative to the operator module>
    cat <<EOF
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Copied from ${operator_rel}/$1
# by scripts/sync-crd.sh. Do not edit. Run make sync-crd after
# make codegen-update in the operator module.
EOF
}

# Drop the leading comment block of a source file, which holds its own SPDX
# header, and any document separator right after it.
strip_leading_comments() {
    awk '
        !body && (/^#/ || /^[[:space:]]*$/ || /^---[[:space:]]*$/) { next }
        { body = 1; print }
    ' "$1"
}

render_crd() {
    echo '{{- if eq (include "pylon-operator.manageCRDs" .) "true" }}'
    header "${crd_rel}"
    strip_leading_comments "${operator_dir}/${crd_rel}" | awk '
        # Top-level metadata: add chart labels and the keep policy. The keep
        # policy goes into an existing annotations block, or a new one before
        # the first other key.
        /^metadata:[[:space:]]*$/ && !done {
            print
            print "  labels:"
            print "    {{- include \"pylon-operator.labels\" . | nindent 4 }}"
            inmeta = 1
            next
        }
        inmeta && /^  annotations:[[:space:]]*$/ {
            if (kept) {
                print "sync-crd.sh: metadata.annotations follows another metadata key" > "/dev/stderr"
                exit 1
            }
            print
            print "    helm.sh/resource-policy: keep"
            kept = 1
            next
        }
        inmeta && /^  [^ ]/ && !kept {
            print "  annotations:"
            print "    helm.sh/resource-policy: keep"
            kept = 1
        }
        inmeta && /^[^ ]/ {
            inmeta = 0
            done = 1
        }
        { print }
    '
    echo '{{- end }}'
}

render_role() {
    header "${role_rel}"
    strip_leading_comments "${operator_dir}/${role_rel}" | awk '
        # Top-level metadata: release-scoped name and chart labels.
        /^metadata:[[:space:]]*$/ && !done {
            print
            inmeta = 1
            next
        }
        inmeta && /^  name:/ {
            print "  name: {{ include \"pylon-operator.fullname\" . }}"
            print "  labels:"
            print "    {{- include \"pylon-operator.labels\" . | nindent 4 }}"
            next
        }
        inmeta && /^[^ ]/ {
            inmeta = 0
            done = 1
        }
        { print }
    '
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

render_crd >"${tmp_dir}/crd.yaml"
render_role >"${tmp_dir}/clusterrole.yaml"

grep -q 'pylon-operator.labels' "${tmp_dir}/crd.yaml" ||
    { echo "sync-crd.sh: ${crd_rel} has no top-level metadata block" >&2; exit 1; }
grep -q 'pylon-operator.fullname' "${tmp_dir}/clusterrole.yaml" ||
    { echo "sync-crd.sh: ${role_rel} has no metadata.name" >&2; exit 1; }

if [ "${mode}" = "write" ]; then
    mkdir -p "$(dirname "${crd_template}")"
    cp "${tmp_dir}/crd.yaml" "${crd_template}"
    cp "${tmp_dir}/clusterrole.yaml" "${role_template}"
    echo "sync-crd.sh: wrote ${crd_template#"${repo_root}/"} and ${role_template#"${repo_root}/"}"
    exit 0
fi

drift=0
check() {  # check <generated> <committed>
    if [ ! -f "$2" ]; then
        echo "sync-crd.sh: ${2#"${repo_root}/"} is missing" >&2
        drift=1
        return
    fi
    if ! diff -u "$2" "$1" >&2; then
        drift=1
    fi
}
check "${tmp_dir}/crd.yaml" "${crd_template}"
check "${tmp_dir}/clusterrole.yaml" "${role_template}"

if [ "${drift}" -ne 0 ]; then
    echo "sync-crd.sh: chart CRD or ClusterRole differs from ${operator_rel}/config. Run make -C deploy/helm/pylon-operator sync-crd." >&2
    exit 1
fi
echo "sync-crd.sh: chart CRD and ClusterRole match ${operator_rel}/config"
