#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# 10_setup_admin-issuer.sh grants any pod running as the proxy's service account
# the right to sign admin-scoped tokens, and OpenBao keeps that grant after the
# Helm release is uninstalled. Gating the release alone would leave it behind,
# so this holds the migration to ADMIN_ISSUER_PROXY_ENABLED.

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
migration="$script_dir/../migrations/10_setup_admin-issuer.sh"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

# Stands in for the binary rather than the helper functions, so the migration's
# real code path runs and only the write is intercepted.
mkdir -p "$work_dir/bin"
cat >"$work_dir/bin/bao" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$BAO_CALLS"
STUB
chmod +x "$work_dir/bin/bao"
export PATH="$work_dir/bin:$PATH"

run_migration() {
  export BAO_CALLS="$work_dir/calls-$1.log"
  : >"$BAO_CALLS"
  # A subshell: the migration is sourced by the runner, and one case sets an
  # environment the next must not inherit.
  ( bash -c "source '$migration'" ) >"$work_dir/out-$1.log" 2>&1 ||
    { echo "admin-issuer-gate: the migration failed in case $1" >&2; cat "$work_dir/out-$1.log" >&2; exit 1; }
}

fail() {
  echo "admin-issuer-gate: $*" >&2
  exit 1
}

# 1. Default: unchanged. The grant is still created, so an install that sets
#    nothing keeps working.
unset ADMIN_ISSUER_PROXY_ENABLED
run_migration default
grep -q 'auth/jwt/role/admin-issuer-proxy' "$work_dir/calls-default.log" ||
  fail "default: the JWT auth role was not created"
grep -q 'sys/policies/acl/services-admin-issuer-proxy' "$work_dir/calls-default.log" ||
  fail "default: the signing policy was not written"
grep -q 'services/nvcf-api/jwt/roles/admin-issuer-proxy' "$work_dir/calls-default.log" ||
  fail "default: the JWT secret role was not created"

# 2. Explicit true: same as the default.
export ADMIN_ISSUER_PROXY_ENABLED=true
run_migration enabled
test -s "$work_dir/calls-enabled.log" ||
  fail "enabled: the migration wrote nothing to OpenBao"

# 3. Disabled: nothing reaches OpenBao at all. Not a narrower grant, none.
export ADMIN_ISSUER_PROXY_ENABLED=false
run_migration disabled
if [ -s "$work_dir/calls-disabled.log" ]; then
  fail "disabled: the migration still wrote to OpenBao:
$(cat "$work_dir/calls-disabled.log")"
fi

echo "admin-issuer-gate: all checks passed"
