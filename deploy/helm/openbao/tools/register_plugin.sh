#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

# Registration is declarative. This helper only creates a version-pinned mount.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../utils/utils.sh"
namespace=${1:-vault-system}
statefulset=${2:-openbao-server}
check_resources "$namespace" "$statefulset"
root_token=$(get_root_token "$namespace" "$statefulset")
kubectl exec -n "$namespace" "${statefulset}-0" -c openbao -- \
    env BAO_TOKEN="$root_token" sh -ec '
        . /openbao/jwt-plugin-versions.env
        bao secrets enable -path=jwt -plugin-version="$JWT_PLUGIN_TARGET_VERSION" vault-plugin-secrets-jwt
    '
