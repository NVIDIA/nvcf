#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# generate_jwt_secret_role turns a comma-separated scope list into the role's
# claims.scopes array. An empty list must produce [], not [""], so services
# that issue no scopes (for example the API Keys role) sign tokens without a
# bogus empty scope. Scope values must also be JSON-escaped.

# The suite runs tests with `sh`, but functions.sh needs non-POSIX bash.
# An exported POSIXLY_CORRECT starts every new bash in POSIX mode, so drop it
# before re-executing and re-execute at most once to avoid an endless loop.
if [ -z "${BASH_VERSION:-}" ] || shopt -oq posix; then
  if [ -n "${JWT_SCOPES_TEST_REEXECED:-}" ]; then
    echo "jwt-secret-role-scopes: requires bash outside POSIX mode" >&2
    exit 1
  fi
  unset POSIXLY_CORRECT
  JWT_SCOPES_TEST_REEXECED=1
  export JWT_SCOPES_TEST_REEXECED
  exec bash "$0" "$@"
fi

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../migrations/utils/functions.sh
source "$script_dir/../migrations/utils/functions.sh"

fail() {
  echo "jwt-secret-role-scopes: $*" >&2
  exit 1
}

check_scopes() {
  local input=$1 expected=$2 role actual
  role=$(generate_jwt_secret_role "ns" "target" "client" "$input") ||
    fail "generate_jwt_secret_role failed for scopes [$input]"
  actual=$(jq -c '.claims.scopes' <<<"$role")
  [ "$actual" = "$expected" ] ||
    fail "scopes [$input]: expected $expected, got $actual"
}

check_scopes "" '[]'
check_scopes "a" '["a"]'
check_scopes "a,b" '["a","b"]'
check_scopes "a,,b" '["a","b"]'
check_scopes "a," '["a"]'
check_scopes ",a" '["a"]'
check_scopes 'x"y' '["x\"y"]'
check_scopes 'x\y' '["x\\y"]'

# The rest of the role is unaffected by an empty scope list.
role=$(generate_jwt_secret_role "ns" "target" "client" "")
jq -e '
  .issuer == "http://target.ns.svc.cluster.local"
  and .claims.azp == "client"
  and .claims.sub == "client"
  and .claims.aud == ["client", "s:target"]
' <<<"$role" >/dev/null ||
  fail "unexpected role for empty scopes: $role"

echo "jwt-secret-role-scopes: all checks passed"
