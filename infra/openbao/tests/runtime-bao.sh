#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
state=/tmp/nvcf-jwt-runtime
export BAO_ADDR=http://127.0.0.1:18200
export BAO_TOKEN
BAO_TOKEN=$(jq -er '.root_token' "$state/init.json")
layout=$(cat "$state/layout")
export JWT_PLUGIN_DIR="$state/$layout/plugins"
export JWT_PLUGIN_CONFIG_DIR="$state/$layout/plugin-config"
exec "$@"
