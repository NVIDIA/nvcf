#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# The admin token from `nvcf-cli init` is signed by the admin-issuer-proxy role.
# nvcf-api checks each CLI admin command against these scopes, so a scope that
# is missing here makes that command fail with 403 on self-hosted installs.

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
migration="$script_dir/../migrations/10_setup_admin-issuer.sh"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

# Records each call and, for writes that read from stdin, the payload too.
mkdir -p "$work_dir/bin"
cat >"$work_dir/bin/bao" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$BAO_CALLS"
if [ "${!#}" = "-" ]; then
  cat >>"$BAO_CALLS"
  printf '\n' >>"$BAO_CALLS"
fi
STUB
chmod +x "$work_dir/bin/bao"
export PATH="$work_dir/bin:$PATH"
export BAO_CALLS="$work_dir/calls.log"
: >"$BAO_CALLS"

fail() {
  echo "admin-issuer-scopes: $*" >&2
  exit 1
}

unset ADMIN_ISSUER_PROXY_ENABLED
( bash -c "source '$migration'" ) >"$work_dir/out.log" 2>&1 ||
  { cat "$work_dir/out.log" >&2; fail "the migration failed"; }

# The payload runs from the role write up to the next bao call.
sed -n '/^write services\/nvcf-api\/jwt\/roles\/admin-issuer-proxy -$/,/^write /p' \
  "$BAO_CALLS" >"$work_dir/role.json"
test -s "$work_dir/role.json" ||
  fail "the admin-issuer-proxy JWT role was not written"

# Scopes that nvcf-cli admin commands require from the admin token.
for scope in \
  register_function \
  list_functions \
  list_functions_details \
  deploy_function \
  update_function \
  delete_function \
  list_cluster_groups; do
  grep -q "\"$scope\"" "$work_dir/role.json" ||
    fail "the admin-issuer-proxy role is missing scope $scope"
done

echo "admin-issuer-scopes: all checks passed"
