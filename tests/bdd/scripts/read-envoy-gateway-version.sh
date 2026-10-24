#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "$script_dir/../../.." && pwd -P)"
setup_script="$repo_root/tools/ncp-local-cluster/scripts/setup-gateway-api.sh"

version="$(sed -n 's/^ENVOY_GATEWAY_VERSION="\([^"]*\)"$/\1/p' "$setup_script")"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  echo "could not read one valid ENVOY_GATEWAY_VERSION from the supported gateway setup" >&2
  exit 1
fi

printf '%s\n' "$version"
