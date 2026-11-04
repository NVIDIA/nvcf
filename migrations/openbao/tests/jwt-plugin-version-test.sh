#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu
repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/../../.." && pwd)
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT
export repo_root test_root
bash <<'BASH'
set -euo pipefail
source "$repo_root/infra/openbao/jwt-plugin-versions.env"
source "$repo_root/migrations/openbao/migrations/utils/functions.sh"
log_info() { :; }
log_step() { :; }
log_success() { :; }
log_error() { echo "$*" >&2; }
bao() {
    if [[ $1 == secrets && $2 == list ]]; then
        if [[ -n ${TEST_MOUNTS:-} ]]; then
            printf '%s\n' "$TEST_MOUNTS"
        else
            printf '{}\n'
        fi
    else
        printf '%s\n' "$*" >> "$test_root/calls"
    fi
}
enable_secrets_mount legacy vault-plugin-secrets-jwt
grep -Fxq 'secrets enable -path=legacy vault-plugin-secrets-jwt' "$test_root/calls"
export JWT_PLUGIN_LIFECYCLE_MODE=versioned
export BAO_JWT_PLUGIN_VERSIONS_FILE="$repo_root/infra/openbao/jwt-plugin-versions.env"
enable_secrets_mount services/test/jwt vault-plugin-secrets-jwt
grep -Fxq "secrets enable -path=services/test/jwt -plugin-version=$JWT_PLUGIN_TARGET_VERSION vault-plugin-secrets-jwt" "$test_root/calls"
BAO_JWT_PLUGIN_VERSION=v99.1.0 enable_secrets_mount override vault-plugin-secrets-jwt
grep -Fxq 'secrets enable -path=override -plugin-version=v99.1.0 vault-plugin-secrets-jwt' "$test_root/calls"
printf 'JWT_PLUGIN_TARGET_VERSION=v99.2.0\n' > "$test_root/manifest.env"
BAO_JWT_PLUGIN_VERSIONS_FILE="$test_root/manifest.env" enable_secrets_mount changed vault-plugin-secrets-jwt
grep -Fxq 'secrets enable -path=changed -plugin-version=v99.2.0 vault-plugin-secrets-jwt' "$test_root/calls"
if BAO_JWT_PLUGIN_VERSIONS_FILE="$test_root/missing" enable_secrets_mount missing vault-plugin-secrets-jwt; then
    echo 'Missing manifest must fail' >&2
    exit 1
fi
if BAO_JWT_PLUGIN_VERSIONS_FILE= enable_secrets_mount unversioned vault-plugin-secrets-jwt; then
    echo 'Versioned mode must not create unversioned mounts' >&2
    exit 1
fi
enable_secrets_mount transit transit
grep -Fxq 'secrets enable -path=transit transit' "$test_root/calls"
: > "$test_root/calls"
TEST_MOUNTS='{"services/test/jwt/":{"type":"vault-plugin-secrets-jwt"}}'
enable_secrets_mount services/test/jwt vault-plugin-secrets-jwt
test ! -s "$test_root/calls"
echo 'JWT mount version pin checks passed'
BASH
