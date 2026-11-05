#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
[[ $# -ge 2 && $1 =~ ^[012]$ ]] || { echo 'Usage: runtime-raft-bao.sh {0|1|2} COMMAND...' >&2; exit 1; }
node=$1
shift
state=/tmp/nvcf-raft-runtime
export BAO_ADDR="http://127.0.0.1:$((18200 + node * 10))"
export BAO_TOKEN
BAO_TOKEN=$(jq -er '.root_token' "$state/init.json")
layout=$(cat "$state/node$node/layout")
export JWT_PLUGIN_DIR="$state/$layout/plugins"
export JWT_PLUGIN_CONFIG_DIR="$state/$layout/plugin-config"
exec "$@"
