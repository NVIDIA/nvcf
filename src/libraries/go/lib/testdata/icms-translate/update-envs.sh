#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


set -euo pipefail

for dir in `find testdata/ -type f -name exp.yaml -exec dirname {} \;`; do
    jq . "${dir}/config.json" > temp.json && mv temp.json "${dir}/config.json"
    jq . "${dir}/message.json" > temp.json && mv temp.json "${dir}/message.json"
    # C conformance forces sort to be consistent across versions.
    LC_ALL=C sort "${dir}/worker.env" -o "${dir}/worker.env"
    if [ -f "${dir}/env.json" ]; then
        WORKER_ENV_JSON_B64=$(cat "${dir}/env.json" | tr -d '\n' | base64 -w0)

        sed -Ei 's/_CONTAINER_ENV=.*/_CONTAINER_ENV='$WORKER_ENV_JSON_B64'/' "${dir}/worker.env"
    fi

    ENV_B64=$(cat "${dir}/worker.env" | base64 -w0)

    jq '.launchSpecification.environment = "'$ENV_B64'"' "${dir}/message.json" > temp.json && mv temp.json "${dir}/message.json"
done
