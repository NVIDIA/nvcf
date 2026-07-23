#!/usr/bin/env bash
set -euo pipefail

runfiles_root="${TEST_SRCDIR:?TEST_SRCDIR is not set}"
workspace="${runfiles_root}/_main"

generator="$(find "${runfiles_root}" -path '*/tools/bazel/generate_notice.py' -print -quit)"
runtime_jar="$(find "${runfiles_root}" -path '*/nvct-service/app.jar' -print -quit)"

if [[ -z "${generator}" || -z "${runtime_jar}" ]]; then
    printf 'Could not locate NOTICE generator or app.jar in test runfiles\n' >&2
    exit 1
fi

exec python3 "${generator}" \
    --maven-install "${workspace}/maven_install.json" \
    --metadata "${workspace}/tools/bazel/notice_metadata.json" \
    --notice "${workspace}/NOTICE" \
    --runtime-jar "${runtime_jar}" \
    --first-party-group com.nvidia.nvct \
    --check
