#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Unit test for resolve_seal_mode() in helm/scripts/deploy.sh. It mocks the
# `kubectl exec ... bao status` call and checks that the resolver picks the
# right seal mode, fails closed on a chart/server mismatch, and fails closed on
# an unreadable or malformed status. No cluster is required.
#
# Run: bash deploy/helm/openbao/tests/resolve-seal-mode-test.sh

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SH="${SCRIPT_DIR}/../helm/scripts/deploy.sh"

workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT

# Fake kubectl: ignores args and emits MOCK_JSON with exit code MOCK_RC, which
# is how `kubectl exec ... -- bao status -format=json` is consumed.
cat >"${workdir}/kubectl" <<'MOCK'
#!/usr/bin/env bash
printf '%s' "${MOCK_JSON:-}"
exit "${MOCK_RC:-0}"
MOCK
chmod +x "${workdir}/kubectl"
export PATH="${workdir}:${PATH}"

# Loggers are defined in log.sh; stub them so the test has no dependency and so
# resolve_seal_mode's log output does not interfere.
log_error() { :; }
log_info() { :; }
log_warn() { :; }

# Load only the RESOLVED_SEAL_MODE global and the resolve_seal_mode function,
# not the script's top-level install flow.
eval "$(sed -n '/^RESOLVED_SEAL_MODE=""/,/^}$/p' "${DEPLOY_SH}")"

pass=0
fail=0
check() { # description  AUTO_UNSEAL  MOCK_JSON  MOCK_RC  expect_rc  expect_mode
  local desc="$1"
  export AUTO_UNSEAL="$2" MOCK_JSON="$3" MOCK_RC="$4"
  local exp_rc="$5" exp_mode="$6"
  RESOLVED_SEAL_MODE=""
  resolve_seal_mode ns sts
  local rc=$?
  if [ "${rc}" = "${exp_rc}" ] && { [ "${exp_rc}" != "0" ] || [ "${RESOLVED_SEAL_MODE}" = "${exp_mode}" ]; }; then
    echo "PASS: ${desc}"
    pass=$((pass + 1))
  else
    echo "FAIL: ${desc} -> rc=${rc} mode=${RESOLVED_SEAL_MODE:-n/a} (wanted rc=${exp_rc} mode=${exp_mode})"
    fail=$((fail + 1))
  fi
}

#     description                           AUTO_UNSEAL  status JSON                               rc  want_rc want_mode
check "shamir server, flag unset (default)" ""    '{"sealed":true,"recovery_seal":false}' 2 0 shamir
check "shamir server, flag=false"           false '{"sealed":true,"recovery_seal":false}' 2 0 shamir
check "shamir server, field omitted"        false '{"sealed":true}'                       2 0 shamir
check "auto server, flag=true, unsealed"    true  '{"sealed":false,"recovery_seal":true}' 0 0 auto
check "auto server, flag=true, sealed rc2"  true  '{"sealed":true,"recovery_seal":true}'  2 0 auto
check "mismatch: flag=true, server shamir"  true  '{"recovery_seal":false}'               2 1 ''
check "mismatch: flag=false, server auto"   false '{"recovery_seal":true}'                0 1 ''
check "status read error (exit 1)"          true  ''                                      1 1 ''
check "malformed status, fail closed"       false 'not json'                              2 1 ''
check "empty status body, fail closed"      false ''                                      2 1 ''
check "non-boolean recovery_seal, fail"     true  '{"recovery_seal":"true"}'              0 1 ''

echo "-----"
echo "pass=${pass} fail=${fail}"
[ "${fail}" -eq 0 ]
