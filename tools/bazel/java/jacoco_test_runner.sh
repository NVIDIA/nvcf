#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [ "$#" -lt 5 ]; then
    echo "Usage: $0 <junit-runner> <classfiles> <source-root> <title> <jacoco-cli> [junit-args...]" >&2
    exit 2
fi

absolute_path() {
    case "$1" in
        /*) printf '%s\n' "$1" ;;
        *) printf '%s/%s\n' "${PWD}" "$1" ;;
    esac
}

junit_runner="$(absolute_path "$1")"
classfiles="$(absolute_path "$2")"
source_root="$3"
report_title="$4"
jacoco_cli="$(absolute_path "$5")"
shift 5

report_dir="${TEST_UNDECLARED_OUTPUTS_DIR:?TEST_UNDECLARED_OUTPUTS_DIR is required}"
junit_report_dir="${report_dir}/junit"
junit_xml="${junit_report_dir}/TEST-junit-jupiter.xml"
exec_file="${PWD}/jacoco.exec"
sourcefiles=""
if [ -n "${source_root}" ]; then
    sourcefiles="${TEST_SRCDIR:?TEST_SRCDIR is required}/${TEST_WORKSPACE:?TEST_WORKSPACE is required}/${source_root}"
fi

mkdir -p "${report_dir}" "${junit_report_dir}"
rm -f "${exec_file}"

set +e
"${junit_runner}" "$@" --reports-dir="${junit_report_dir}"
junit_status=$?
set -e

report_status=0
if [ ! -s "${junit_xml}" ]; then
    echo "ERROR: JUnit did not create ${junit_xml}" >&2
    report_status=1
fi

if [ ! -s "${exec_file}" ]; then
    echo "ERROR: JaCoCo did not create ${exec_file}" >&2
    report_status=1
else
    cp "${exec_file}" "${report_dir}/jacoco.exec"
    report_args=(
        report "${report_dir}/jacoco.exec"
        --classfiles "${classfiles}"
        --html "${report_dir}"
        --xml "${report_dir}/jacoco.xml"
        --name "${report_title}"
    )
    if [ -n "${sourcefiles}" ]; then
        report_args+=(--sourcefiles "${sourcefiles}")
    fi

    set +e
    "${jacoco_cli}" "${report_args[@]}"
    jacoco_status=$?
    set -e
    if [ "${jacoco_status}" -ne 0 ]; then
        report_status="${jacoco_status}"
    fi
fi

if [ "${junit_status}" -ne 0 ]; then
    exit "${junit_status}"
fi
exit "${report_status}"
