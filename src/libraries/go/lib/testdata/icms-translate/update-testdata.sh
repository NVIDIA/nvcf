#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
testdata_root="${repo_root}/testdata/icms-translate"
check_only=false

if [[ "${1:-}" == "--check" ]]; then
    check_only=true
fi

normalize_dir() {
    local dir="$1"
    local tmp_json
    local worker_env_json_b64
    local env_b64

    tmp_json="$(mktemp)"
    jq . "${dir}/config.json" > "${tmp_json}"
    mv "${tmp_json}" "${dir}/config.json"

    tmp_json="$(mktemp)"
    jq . "${dir}/message.json" > "${tmp_json}"
    mv "${tmp_json}" "${dir}/message.json"

    LC_ALL=C sort "${dir}/worker.env" -o "${dir}/worker.env"

    if [ -f "${dir}/env.json" ]; then
        worker_env_json_b64="$(tr -d '\n' < "${dir}/env.json" | base64 -w0)"
        sed -Ei 's/_CONTAINER_ENV=.*/_CONTAINER_ENV='"${worker_env_json_b64}"'/' "${dir}/worker.env"
    fi

    env_b64="$(base64 -w0 < "${dir}/worker.env")"
    tmp_json="$(mktemp)"
    jq '.launchSpecification.environment = "'"${env_b64}"'"' "${dir}/message.json" > "${tmp_json}"
    mv "${tmp_json}" "${dir}/message.json"
}

cd "${repo_root}"
while IFS= read -r dir; do
    echo "Updating testdata: ${dir}"
    normalize_dir "${dir}"
    go run ./cmd/icms-translate -c "${dir}/config.json" -m "${dir}/message.json" > "${dir}/exp.yaml"
    if [ -f "${dir}/config_utdep.json" ]; then
        go run ./cmd/icms-translate -c "${dir}/config_utdep.json" -m "${dir}/message.json" > "${dir}/exp_utdep.yaml"
    fi
done < <(find "${testdata_root}" -type f -name exp.yaml -exec dirname {} \; | LC_ALL=C sort -u)

if [[ "${check_only}" == true ]]; then
    git diff --exit-code -- "${testdata_root}"
fi
