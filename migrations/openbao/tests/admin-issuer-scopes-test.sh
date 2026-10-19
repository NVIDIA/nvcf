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
# Stdin writes also replace a per-path state file, as `bao write` replaces a
# JWT role, so a test can seed an existing role and read back the result.
mkdir -p "$work_dir/bin"
cat >"$work_dir/bin/bao" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$BAO_CALLS"
if [ "$1" = "write" ] && [ "${!#}" = "-" ]; then
  payload="$(cat)"
  printf '%s\n\n' "$payload" >>"$BAO_CALLS"
  printf '%s\n' "$payload" >"$BAO_STATE/$(printf '%s' "$2" | tr '/' '_')"
fi
STUB
chmod +x "$work_dir/bin/bao"
export PATH="$work_dir/bin:$PATH"
export BAO_CALLS="$work_dir/calls.log"
export BAO_STATE="$work_dir/state"
mkdir -p "$BAO_STATE"
: >"$BAO_CALLS"
role_state="$BAO_STATE/services_nvcf-api_jwt_roles_admin-issuer-proxy"

fail() {
  echo "admin-issuer-scopes: $*" >&2
  exit 1
}

run_migration() {
  ( bash -c "source '$migration'" ) >"$work_dir/out.log" 2>&1 ||
    { cat "$work_dir/out.log" >&2; fail "the migration failed"; }
}

unset ADMIN_ISSUER_PROXY_ENABLED
run_migration

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

# Upgrade: the migration job re-runs every script on each Helm upgrade. Seed the
# role an earlier release wrote, without list_cluster_groups, then run twice.
# The migration must rewrite the role each time rather than skip an existing one.
cat >"$role_state" <<'OLD_ROLE'
{"claims": {"scopes": ["register_function", "list_functions", "cluster-management"]}}
OLD_ROLE
for run in 1 2; do
  : >"$BAO_CALLS"
  run_migration
  grep -q '^write services/nvcf-api/jwt/roles/admin-issuer-proxy -$' "$BAO_CALLS" ||
    fail "upgrade run $run did not rewrite the existing admin-issuer-proxy role"
done
test "$(grep -c '"list_cluster_groups"' "$role_state")" -eq 1 ||
  fail "after upgrade runs the role does not hold list_cluster_groups exactly once:
$(cat "$role_state")"

echo "admin-issuer-scopes: all checks passed"
